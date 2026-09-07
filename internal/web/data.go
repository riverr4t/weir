package web

import (
	"sort"
	"strings"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/bazarr"
	"github.com/riverr4t/weir/internal/apps/jellyfin"
	"github.com/riverr4t/weir/internal/apps/jellyseerr"
	"github.com/riverr4t/weir/internal/apps/lidarr"
	"github.com/riverr4t/weir/internal/apps/prowlarr"
	"github.com/riverr4t/weir/internal/apps/readarr"
	"github.com/riverr4t/weir/internal/snapshot"
)

// typed accessors for the cells that live in app packages
func extra[T any](s *snapshot.Store, key string) *T {
	if s.Extra == nil {
		return nil
	}
	v, _ := s.Extra[key].(*T)
	return v
}

func (s *Server) lidarr() *lidarr.Cells         { return extra[lidarr.Cells](s.Snap, "lidarr") }
func (s *Server) readarr() *readarr.Cells       { return extra[readarr.Cells](s.Snap, "readarr") }
func (s *Server) prowlarr() *prowlarr.Cells     { return extra[prowlarr.Cells](s.Snap, "prowlarr") }
func (s *Server) bazarr() *bazarr.Cells         { return extra[bazarr.Cells](s.Snap, "bazarr") }
func (s *Server) jellyfin() *jellyfin.Cells     { return extra[jellyfin.Cells](s.Snap, "jellyfin") }
func (s *Server) jellyseerr() *jellyseerr.Cells { return extra[jellyseerr.Cells](s.Snap, "jellyseerr") }

// Warning is one health item from any app.
type Warning struct {
	App, Type, Message, Wiki string
}

func (s *Server) warnings() []Warning {
	var out []Warning
	add := func(app string, items []arr.HealthItem) {
		for _, h := range items {
			if h.Type == "ok" {
				continue
			}
			out = append(out, Warning{App: app, Type: h.Type, Message: h.Message, Wiki: h.WikiURL})
		}
	}
	for _, app := range arrOrder {
		if c := s.Snap.Arr(app); c != nil {
			add(app, c.Health.Get().Data)
		}
	}
	if p := s.prowlarr(); p != nil {
		add("prowlarr", p.Health.Get().Data)
	}
	// apps that are down are warnings too
	for _, a := range s.base("", "").AppDots {
		if a.State == "down" {
			out = append(out, Warning{App: a.Label, Type: "error", Message: a.Label + " is not answering"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Type == "error" && out[j].Type != "error" })
	return out
}

// Disk is one root folder's space; deduped across apps by path.
type Disk struct {
	Path        string
	Free, Total int64
	UsedPct     float64
	Apps        []string
}

func (s *Server) disks() []Disk {
	byPath := map[string]*Disk{}
	var order []string
	for _, app := range arrOrder {
		c := s.Snap.Arr(app)
		if c == nil {
			continue
		}
		for _, d := range c.Disk.Get().Data {
			if d.TotalSpace == 0 || strings.HasPrefix(d.Path, "/config") || strings.HasPrefix(d.Path, "/boot") {
				continue
			}
			// the same filesystem shows up under every mount path; key by size
			k := itoa(int(d.TotalSpace>>20)) + ":" + itoa(int(d.FreeSpace>>20))
			if e, ok := byPath[k]; ok {
				if len(d.Path) < len(e.Path) {
					e.Path = d.Path
				}
				if !contains(e.Apps, app) {
					e.Apps = append(e.Apps, app)
				}
				continue
			}
			byPath[k] = &Disk{Path: d.Path, Free: d.FreeSpace, Total: d.TotalSpace,
				UsedPct: 100 * float64(d.TotalSpace-d.FreeSpace) / float64(d.TotalSpace), Apps: []string{app}}
			order = append(order, k)
		}
	}
	out := make([]Disk, 0, len(order))
	for _, k := range order {
		out = append(out, *byPath[k])
	}
	return out
}

// Upcoming is a calendar entry across apps.
type Upcoming struct {
	App, Title, Sub, Date string
	When                  time.Time
	HasFile               bool
}

func (s *Server) upcoming(now time.Time) []Upcoming {
	var out []Upcoming
	seriesTitle := map[int64]string{}
	for _, sr := range s.Snap.SonarrSeries.Get().Data {
		seriesTitle[sr.ID] = sr.Title
	}
	parse := func(d string) time.Time {
		if len(d) >= 10 {
			t, err := time.ParseInLocation("2006-01-02", d[:10], now.Location())
			if err == nil {
				return t
			}
		}
		return time.Time{}
	}
	for _, it := range s.Snap.Sonarr.Calendar.Get().Data {
		when := parse(it.AirDate)
		out = append(out, Upcoming{App: "sonarr", Title: seriesTitle[it.SeriesID], Sub: epCode(it.SeasonNumber, it.EpisodeNumber) + " · " + it.Title, When: when, Date: when.Format("Mon Jan 2"), HasFile: it.HasFile})
	}
	for _, it := range s.Snap.Radarr.Calendar.Get().Data {
		d := it.ReleaseDate
		if d == "" {
			d = it.AirDate
		}
		when := parse(d)
		out = append(out, Upcoming{App: "radarr", Title: it.Title, Sub: yearStr(it.Year), When: when, Date: when.Format("Mon Jan 2"), HasFile: it.HasFile})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].When.Before(out[j].When) })
	if len(out) > 14 {
		out = out[:14]
	}
	return out
}

func epCode(s, e int) string { return "S" + pad2(s) + "E" + pad2(e) }

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func yearStr(y int) string {
	if y == 0 {
		return ""
	}
	return itoa(y)
}

// Playing is one active Jellyfin session.
type Playing struct {
	User, Device, Title, Sub string
	Pct                      float64
	Paused                   bool
}

func (s *Server) playing() []Playing {
	j := s.jellyfin()
	if j == nil {
		return nil
	}
	var out []Playing
	for _, se := range j.Sessions.Get().Data {
		if se.NowPlayingItem == nil {
			continue
		}
		p := Playing{User: se.UserName, Device: se.DeviceName, Title: se.NowPlayingItem.Name, Paused: se.PlayState.IsPaused}
		if se.NowPlayingItem.SeriesName != "" {
			p.Title, p.Sub = se.NowPlayingItem.SeriesName, se.NowPlayingItem.Name
		}
		if se.NowPlayingItem.RunTimeTicks > 0 {
			p.Pct = 100 * float64(se.PlayState.PositionTicks) / float64(se.NowPlayingItem.RunTimeTicks)
		}
		out = append(out, p)
	}
	return out
}

// PendingRequest is a Jellyseerr request awaiting approval, with its title resolved.
type PendingRequest struct {
	ID          int64
	Type, Title string
	By          string
	Age         time.Time
}

func (s *Server) pending() []PendingRequest {
	j := s.jellyseerr()
	if j == nil {
		return nil
	}
	var out []PendingRequest
	for _, r := range j.Pending.Get().Data {
		out = append(out, PendingRequest{ID: r.ID, Type: r.Type, Title: s.titleFor(r.Type, r.Media.TmdbID, r.Media.TvdbID), By: r.RequestedBy.DisplayName, Age: r.CreatedAt})
	}
	return out
}

// titleFor resolves a TMDb/TVDb id to a title from the arr libraries; falls back to the id.
func (s *Server) titleFor(kind string, tmdb, tvdb int64) string {
	if kind == "movie" {
		for _, m := range s.Snap.RadarrMovies.Get().Data {
			if m.TmdbID == tmdb {
				return m.Title
			}
		}
		return "TMDb " + itoa(int(tmdb))
	}
	for _, sr := range s.Snap.SonarrSeries.Get().Data {
		if sr.TvdbID == tvdb {
			return sr.Title
		}
	}
	return "TVDb " + itoa(int(tvdb))
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
