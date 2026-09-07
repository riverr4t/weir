// Package web serves the pages, the JSON API and the SSE stream (spec §10).
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/jellyfin"
	"github.com/riverr4t/weir/internal/apps/jellyseerr"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/rules"
	"github.com/riverr4t/weir/internal/snapshot"
	"github.com/riverr4t/weir/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

var assetVersion = strconv.FormatInt(time.Now().Unix(), 36)

type QbitControl interface {
	Stop(ctx context.Context, hash string) error
	Start(ctx context.Context, hash string) error
}

type Deps struct {
	Cfg         config.Config
	Snap        *snapshot.Store
	DB          *store.Store
	M           *metrics.M
	Arrs        map[string]*arr.Client
	Qbit        QbitControl
	Jellyfin    *jellyfin.Client
	Jellyseerr  *jellyseerr.Client
	Rules       *rules.Runner
	CleanerMode string
	Now         func() time.Time
}

type Server struct {
	Deps
	Hub     *Hub
	History *History
	posters *posterCache
	pages   map[string]*template.Template // one set per page: layout + partials + the page
	parts   *template.Template            // partials alone, for fragments
}

func New(d Deps) *Server {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.CleanerMode == "" {
		d.CleanerMode = d.Cfg.Cleaner.Mode
	}
	s := &Server{Deps: d, Hub: NewHub(d.M), History: NewHistory(300), posters: newPosterCache(32 << 20)}
	s.parts = template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/partials.html"))
	s.pages = map[string]*template.Template{}
	entries, _ := fs.ReadDir(templateFS, "templates")
	for _, e := range entries {
		n := e.Name()
		if n == "layout.html" || n == "partials.html" {
			continue
		}
		s.pages[n] = template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", "templates/"+n))
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServer(http.FS(static)))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("GET /metrics", s.M.Handler())
	mux.Handle("GET /events", s.Hub)
	mux.HandleFunc("GET /{$}", s.overview)
	mux.HandleFunc("GET /overview/live", s.overviewLive)
	mux.HandleFunc("GET /api/overview", s.apiOverview)
	mux.HandleFunc("GET /img/{app}/{id}", s.poster)
	mux.HandleFunc("GET /library/{app}", s.library)
	mux.HandleFunc("GET /api/library/{app}", s.apiLibrary)
	mux.Handle("POST /library/jellyfin/scan", requireAction(http.HandlerFunc(s.jellyfinScan)))
	mux.Handle("POST /library/{app}/search-missing", requireAction(http.HandlerFunc(s.searchMissing)))
	mux.HandleFunc("GET /requests", s.requests)
	mux.HandleFunc("GET /requests/search", s.searchFragment)
	mux.Handle("POST /requests", requireAction(http.HandlerFunc(s.request)))
	mux.Handle("POST /requests/{id}/approve", requireAction(s.requestDecision("approve")))
	mux.Handle("POST /requests/{id}/decline", requireAction(s.requestDecision("decline")))
	mux.HandleFunc("GET /rules", s.rulesPage)
	mux.HandleFunc("GET /rules/new", s.ruleEdit)
	mux.HandleFunc("GET /rules/{id}", s.ruleEdit)
	mux.HandleFunc("GET /rules/{id}/runs/{run}", s.ruleRunPage)
	mux.HandleFunc("GET /api/rules", s.apiRules)
	mux.Handle("POST /rules", requireAction(http.HandlerFunc(s.ruleSave)))
	mux.Handle("POST /rules/{id}", requireAction(http.HandlerFunc(s.ruleSave)))
	mux.Handle("POST /rules/{id}/enable", requireAction(s.ruleToggle(true)))
	mux.Handle("POST /rules/{id}/disable", requireAction(s.ruleToggle(false)))
	mux.Handle("POST /rules/{id}/delete", requireAction(http.HandlerFunc(s.ruleDelete)))
	mux.Handle("POST /rules/{id}/run", requireAction(http.HandlerFunc(s.ruleRun)))
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/health", s.apiHealth)
	mux.HandleFunc("GET /downloads", s.downloads)
	mux.HandleFunc("GET /downloads/rows", s.downloadRows)
	mux.HandleFunc("GET /api/downloads", s.apiDownloads)
	mux.Handle("POST /downloads/{hash}/pause", requireAction(s.torrentAction("pause")))
	mux.Handle("POST /downloads/{hash}/resume", requireAction(s.torrentAction("resume")))
	mux.Handle("POST /downloads/{hash}/blocklist", requireAction(http.HandlerFunc(s.blocklist)))
	return mux
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

func requireAction(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Weir-Action") != "1" {
			http.Error(w, "missing X-Weir-Action header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func actor(r *http.Request) string {
	if v := r.Header.Get("Tailscale-User-Login"); v != "" {
		return v
	}
	return r.RemoteAddr
}

type navItem struct {
	Key, Href, Label, Icon, Num, Dot string
}

type appDot struct{ Href, Title, Label, State string }

type page struct {
	Title, Page string
	V           string // asset cache-buster: process start time
	Nav         []navItem
	AppDots     []appDot
	Flow        string
	Rows        []Row
	NeedsYou    int
	DL, UP      int64
	Actions     []store.Action
	CleanerMode string
	Now         time.Time
	Overview    *overviewModel
	Library     *libraryModel
	Requests    *requestsModel
	Health      *healthModel
	Search      []searchRow
	Query       string
	PublicURL   string
	Rules       *rulesModel
	RuleEdit    *ruleEditModel
	RuleRun     *runModel
}

func (s *Server) base(title, key string) page {
	dl, _, _ := speeds(s.Snap)
	p := page{Title: title, Page: key, Now: s.Now(), CleanerMode: s.CleanerMode, V: assetVersion,
		Flow: strconv.FormatFloat(flow(dl), 'f', 2, 64)}
	p.Nav = []navItem{
		{Key: "overview", Href: "/", Label: "Overview", Icon: "≋", Num: "1"},
		{Key: "downloads", Href: "/downloads", Label: "Downloads", Icon: "↓", Num: "2"},
	}
	for _, a := range []struct{ app, label, icon, num string }{{"radarr", "Movies", "▣", "3"}, {"sonarr", "Series", "▤", "4"}, {"lidarr", "Music", "♫", "5"}, {"readarr", "Books", "▯", "6"}} {
		if s.Cfg.Enabled(config.App(a.app)) {
			p.Nav = append(p.Nav, navItem{Key: a.app, Href: "/library/" + a.app, Label: a.label, Icon: a.icon, Num: a.num})
		}
	}
	if s.Cfg.Enabled(config.Jellyseerr) {
		n := ""
		if j := s.jellyseerr(); j != nil && len(j.Pending.Get().Data) > 0 {
			n = "amber"
		}
		p.Nav = append(p.Nav, navItem{Key: "requests", Href: "/requests", Label: "Requests", Icon: "✚", Num: "7", Dot: n})
	}
	p.Nav = append(p.Nav, navItem{Key: "rules", Href: "/rules", Label: "Rules", Icon: "☰", Num: "8"})
	hd := ""
	if len(s.warningsQuick()) > 0 {
		hd = "amber"
	}
	p.Nav = append(p.Nav, navItem{Key: "health", Href: "/health", Label: "Health", Icon: "♥", Num: "9", Dot: hd})
	for _, a := range config.AllApps {
		if !s.Cfg.Enabled(a) {
			continue
		}
		state := "off"
		switch a {
		case config.Qbit:
			state = cellState(s.Snap.Qbit.Get().FetchedAt, s.Snap.Qbit.Get().Down)
		default:
			if c := s.Snap.Arr(string(a)); c != nil {
				st := c.Status.Get()
				state = cellState(st.FetchedAt, st.Down)
			}
		}
		href := s.Cfg.Apps[a].PublicURL
		if href == "" {
			href = "#"
		}
		p.AppDots = append(p.AppDots, appDot{Href: href, Title: string(a), Label: string(a), State: state})
	}
	return p
}

// warningsQuick counts arr health warnings without building the page (for the rail dot).
func (s *Server) warningsQuick() []arr.HealthItem {
	var out []arr.HealthItem
	for _, app := range arrOrder {
		if c := s.Snap.Arr(app); c != nil {
			for _, h := range c.Health.Get().Data {
				if h.Type != "ok" {
					out = append(out, h)
				}
			}
		}
	}
	return out
}

func cellState(at time.Time, down bool) string {
	switch {
	case down:
		return "down"
	case at.IsZero():
		return "off"
	}
	return "up"
}

// render executes a page (a file name under templates/) or a partial (any other name).
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.execute(&buf, name, data); err != nil {
		slog.Error("render", "template", name, "err", err)
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (s *Server) execute(buf *bytes.Buffer, name string, data any) error {
	if t, ok := s.pages[name]; ok {
		return t.ExecuteTemplate(buf, "layout", data)
	}
	return s.parts.ExecuteTemplate(buf, name, data)
}

func (s *Server) rows(ctx context.Context) []Row {
	strikes, _ := s.DB.Strikes(ctx)
	return rows(s.Snap, strikes)
}

func needsYou(rs []Row) int {
	n := 0
	for _, r := range rs {
		if r.Completed && r.InTrouble {
			n++
		}
	}
	return n
}

func (s *Server) downloads(w http.ResponseWriter, r *http.Request) {
	p := s.base("Downloads", "downloads")
	p.Rows = s.rows(r.Context())
	p.NeedsYou = needsYou(p.Rows)
	p.Actions, _ = s.DB.Actions(r.Context(), 50)
	p.DL, p.UP, _ = speeds(s.Snap)
	s.render(w, "downloads.html", p)
}

func (s *Server) downloadRows(w http.ResponseWriter, r *http.Request) {
	s.render(w, "rows", page{Rows: s.rows(r.Context()), Now: s.Now()})
}

// RowsHTML renders the fragment the SSE ticker broadcasts.
func (s *Server) RowsHTML(ctx context.Context) string {
	var buf bytes.Buffer
	_ = s.execute(&buf, "rows", page{Rows: s.rows(ctx), Now: s.Now()})
	return buf.String()
}

func (s *Server) SpeedsHTML() string {
	dl, up, _ := speeds(s.Snap)
	var buf bytes.Buffer
	_ = s.execute(&buf, "speeds", page{DL: dl, UP: up})
	return buf.String()
}

func (s *Server) FlowValue() string {
	dl, _, _ := speeds(s.Snap)
	return strconv.FormatFloat(flow(dl), 'f', 2, 64)
}

func writeJSON(w http.ResponseWriter, fetchedAt time.Time, data any, err error) {
	w.Header().Set("Content-Type", "application/json")
	env := map[string]any{"ok": err == nil}
	if err == nil {
		env["fetchedAt"], env["data"] = fetchedAt, data
	} else {
		env["error"] = err.Error()
	}
	json.NewEncoder(w).Encode(env)
}

func (s *Server) apiDownloads(w http.ResponseWriter, r *http.Request) {
	_, _, at := speeds(s.Snap)
	writeJSON(w, at, s.rows(r.Context()), nil)
}

func (s *Server) log(ctx context.Context, r *http.Request, kind, app, subject string, detail map[string]any, err error) {
	d, _ := json.Marshal(detail)
	outcome, errText := "ok", ""
	if err != nil {
		outcome, errText = "failed", err.Error()
	}
	_, _ = s.DB.LogAction(ctx, store.Action{At: s.Now(), Kind: kind, App: app, Subject: subject, Detail: string(d), Actor: actor(r), Outcome: outcome, Error: errText})
}

func (s *Server) torrentAction(kind string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Qbit == nil {
			http.Error(w, "qBittorrent is not configured", http.StatusServiceUnavailable)
			return
		}
		hash := r.PathValue("hash")
		var err error
		if kind == "pause" {
			err = s.Qbit.Stop(r.Context(), hash)
		} else {
			err = s.Qbit.Start(r.Context(), hash)
		}
		_, item, _ := ownerOf(s.Snap, hash)
		s.log(r.Context(), r, "torrent."+kind, "qbit", item.Title, map[string]any{"hash": hash}, err)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.downloadRows(w, r)
	})
}

// blocklist: remove an UNCOMPLETED download through its arr, blocklist the
// release and search again. Completed items are refused (spec §7, §10).
func (s *Server) blocklist(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	app, item, ok := ownerOf(s.Snap, hash)
	if !ok {
		http.Error(w, "no arr queue item for that download", http.StatusNotFound)
		return
	}
	if item.Completed() {
		http.Error(w, "That download is complete. Weir does not remove completed downloads; handle it in the app.", http.StatusConflict)
		return
	}
	err := s.Arrs[app].DeleteQueueItem(r.Context(), item.ID, true)
	s.log(r.Context(), r, "torrent.blocklist", app, item.Title, map[string]any{"hash": hash, "queueId": item.ID, "titleKey": item.TitleKey()}, err)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.downloadRows(w, r)
}

var funcs = template.FuncMap{
	"bytes": func(n int64) string {
		f := float64(n)
		units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
		for i, u := range units {
			if f < 1024 || i == len(units)-1 {
				if i == 0 {
					return fmt.Sprintf("%d %s", n, u)
				}
				return fmt.Sprintf("%.1f %s", f, u)
			}
			f /= 1024
		}
		return ""
	},
	"pct": func(p float64) string { return fmt.Sprintf("%.0f%%", p*100) },
	"eta": func(secs int64) string {
		if secs < 0 {
			return "—"
		}
		d := time.Duration(secs) * time.Second
		switch {
		case d < time.Minute:
			return fmt.Sprintf("%ds", secs)
		case d < time.Hour:
			return fmt.Sprintf("%dm", int(d.Minutes()))
		default:
			return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
		}
	},
	"ago": func(t, now time.Time) string {
		if t.IsZero() {
			return "never"
		}
		d := now.Sub(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 48*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		default:
			return t.Format("Jan 2")
		}
	},
	"hasSuffix": strings.HasSuffix,
	"json": func(v any) template.JS {
		b, _ := json.Marshal(v)
		return template.JS(b)
	},
	"title": func(s string) string { return strings.ToUpper(s[:1]) + s[1:] },
}

type overviewModel struct {
	DL         int64            `json:"dl"`
	UP         int64            `json:"up"`
	SparkDL    string           `json:"-"`
	SparkUP    string           `json:"-"`
	Active     []Row            `json:"active"`
	QueueCount int              `json:"queueCount"`
	Trouble    int              `json:"trouble"`
	Warnings   []Warning        `json:"warnings"`
	Disks      []Disk           `json:"disks"`
	Pending    []PendingRequest `json:"pending"`
	Playing    []Playing        `json:"playing"`
	Upcoming   []Upcoming       `json:"upcoming"`
	Counts     struct {
		Movies  int `json:"movies"`
		Series  int `json:"series"`
		Artists int `json:"artists"`
		Authors int `json:"authors"`
	} `json:"counts"`
	Missing struct {
		Movies   int `json:"movies"`
		Episodes int `json:"episodes"`
	} `json:"missing"`
}

func (s *Server) overviewModel(ctx context.Context) *overviewModel {
	m := &overviewModel{}
	m.DL, m.UP, _ = speeds(s.Snap)
	dl, up := s.History.Samples()
	m.SparkDL, m.SparkUP = sparkline(dl, 160, 28), sparkline(up, 160, 28)
	rows := s.rows(ctx)
	m.QueueCount = len(rows)
	for _, r := range rows {
		if r.InTrouble {
			m.Trouble++
		}
		if r.Speed > 0 && len(m.Active) < 5 {
			m.Active = append(m.Active, r)
		}
	}
	m.Warnings = s.warnings()
	m.Disks = s.disks()
	m.Pending = s.pending()
	m.Playing = s.playing()
	m.Upcoming = s.upcoming(s.Now())
	m.Counts.Movies = len(s.Snap.RadarrMovies.Get().Data)
	m.Counts.Series = len(s.Snap.SonarrSeries.Get().Data)
	if c := s.lidarr(); c != nil {
		m.Counts.Artists = len(c.Artists.Get().Data)
	}
	if c := s.readarr(); c != nil {
		m.Counts.Authors = len(c.Authors.Get().Data)
	}
	m.Missing.Movies = s.Snap.Radarr.Missing.Get().Data.TotalRecords
	m.Missing.Episodes = s.Snap.Sonarr.Missing.Get().Data.TotalRecords
	return m
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	p := s.base("Overview", "overview")
	p.Overview = s.overviewModel(r.Context())
	s.render(w, "overview.html", p)
}

// overviewLive is the SSE-swapped fragment: speeds, sparkline and active rows.
func (s *Server) overviewLive(w http.ResponseWriter, r *http.Request) {
	s.render(w, "overview-live", page{Overview: s.overviewModel(r.Context()), Now: s.Now()})
}

func (s *Server) OverviewLiveHTML(ctx context.Context) string {
	var buf bytes.Buffer
	_ = s.execute(&buf, "overview-live", page{Overview: s.overviewModel(ctx), Now: s.Now()})
	return buf.String()
}

func (s *Server) apiOverview(w http.ResponseWriter, r *http.Request) {
	_, _, at := speeds(s.Snap)
	writeJSON(w, at, s.overviewModel(r.Context()), nil)
}
