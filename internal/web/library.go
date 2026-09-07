package web

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/config"
)

// Tile is one poster in a grid.
type Tile struct {
	ID       int64
	Title    string
	Sub      string
	Img      string
	Added    time.Time
	HasFile  bool
	Size     int64
	Monitord bool
}

type libraryModel struct {
	App, Label, Kind string
	Status           arr.SystemStatus
	Stale            bool
	Total, OnDisk    int
	Missing, Cutoff  int
	Size             int64
	Recent           []Tile
	Wanted           []arr.WantedItem
	WantedTotal      int
	Health           []arr.HealthItem
	Tags             []arr.Tag
	Unmonitored      int
}

var libraryLabels = map[string][3]string{ // app -> label, item noun, search command
	"radarr":  {"Movies", "movie", "MissingMoviesSearch"},
	"sonarr":  {"Series", "series", "MissingEpisodeSearch"},
	"lidarr":  {"Music", "artist", "MissingAlbumSearch"},
	"readarr": {"Books", "author", "MissingBookSearch"},
}

func (s *Server) libraryModel(app string) *libraryModel {
	cells := s.Snap.Arr(app)
	if cells == nil {
		return nil
	}
	lbl := libraryLabels[app]
	m := &libraryModel{App: app, Label: lbl[0], Kind: lbl[1]}
	st := cells.Status.Get()
	m.Status, m.Stale = st.Data, st.Down
	m.Health = cells.Health.Get().Data
	m.Tags = cells.Tags.Get().Data
	wanted := cells.Missing.Get().Data
	m.Wanted, m.WantedTotal = wanted.Records, wanted.TotalRecords
	m.Missing = wanted.TotalRecords
	m.Cutoff = cells.Cutoff.Get().Data.TotalRecords
	switch app {
	case "radarr":
		for _, mv := range s.Snap.RadarrMovies.Get().Data {
			m.Total++
			m.Size += mv.SizeOnDisk
			if mv.HasFile {
				m.OnDisk++
			}
			if !mv.Monitored {
				m.Unmonitored++
			}
			m.Recent = append(m.Recent, Tile{ID: mv.ID, Title: mv.Title, Sub: yearStr(mv.Year), Img: "/img/radarr/" + strconv.FormatInt(mv.ID, 10), Added: mv.Added, HasFile: mv.HasFile, Size: mv.SizeOnDisk, Monitord: mv.Monitored})
		}
	case "sonarr":
		for _, sr := range s.Snap.SonarrSeries.Get().Data {
			m.Total++
			m.Size += sr.Statistics.SizeOnDisk
			if sr.Statistics.EpisodeFileCount > 0 {
				m.OnDisk++
			}
			if !sr.Monitored {
				m.Unmonitored++
			}
			sub := itoa(sr.Statistics.EpisodeFileCount) + "/" + itoa(sr.Statistics.EpisodeCount) + " episodes"
			m.Recent = append(m.Recent, Tile{ID: sr.ID, Title: sr.Title, Sub: sub, Img: "/img/sonarr/" + strconv.FormatInt(sr.ID, 10), Added: sr.Added, HasFile: sr.Statistics.EpisodeFileCount > 0, Size: sr.Statistics.SizeOnDisk, Monitord: sr.Monitored})
		}
	case "lidarr":
		if c := s.lidarr(); c != nil {
			for _, a := range c.Artists.Get().Data {
				m.Total++
				m.Size += a.Statistics.SizeOnDisk
				if a.Statistics.TrackFileCount > 0 {
					m.OnDisk++
				}
				if !a.Monitored {
					m.Unmonitored++
				}
				m.Recent = append(m.Recent, Tile{ID: a.ID, Title: a.Name, Sub: itoa(a.Statistics.AlbumCount) + " albums", Img: "/img/lidarr/" + strconv.FormatInt(a.ID, 10), Added: a.Added, HasFile: a.Statistics.TrackFileCount > 0, Size: a.Statistics.SizeOnDisk, Monitord: a.Monitored})
			}
		}
	case "readarr":
		if c := s.readarr(); c != nil {
			for _, b := range c.Books.Get().Data {
				m.Total++
				m.Size += b.Statistics.SizeOnDisk
				if b.Statistics.BookFileCount > 0 {
					m.OnDisk++
				}
				if !b.Monitored {
					m.Unmonitored++
				}
				m.Recent = append(m.Recent, Tile{ID: b.ID, Title: b.Title, Sub: b.AuthorTitle, Img: "/img/readarr/" + strconv.FormatInt(b.ID, 10), Added: b.Added, HasFile: b.Statistics.BookFileCount > 0, Size: b.Statistics.SizeOnDisk, Monitord: b.Monitored})
			}
		}
	}
	sort.SliceStable(m.Recent, func(i, j int) bool { return m.Recent[i].Added.After(m.Recent[j].Added) })
	if len(m.Recent) > 18 {
		m.Recent = m.Recent[:18]
	}
	return m
}

func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	if !s.Cfg.Enabled(config.App(app)) {
		http.NotFound(w, r)
		return
	}
	m := s.libraryModel(app)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	p := s.base(m.Label, app)
	p.Library = m
	p.PublicURL = s.Cfg.Apps[config.App(app)].PublicURL
	s.render(w, "library.html", p)
}

func (s *Server) apiLibrary(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	m := s.libraryModel(app)
	if m == nil {
		writeJSON(w, time.Time{}, nil, errNotFound)
		return
	}
	writeJSON(w, s.Snap.Arr(app).Status.Get().FetchedAt, m, nil)
}

// POST /library/{app}/search-missing
func (s *Server) searchMissing(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	c, ok := s.Arrs[app]
	if !ok {
		http.NotFound(w, r)
		return
	}
	cmd := libraryLabels[app][2]
	err := c.Command(r.Context(), cmd, nil)
	s.log(r.Context(), r, "library.search-missing", app, cmd, map[string]any{"command": cmd}, err)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Write([]byte(`<span class="chip up">Searching…</span>`))
}

// POST /library/jellyfin/scan
func (s *Server) jellyfinScan(w http.ResponseWriter, r *http.Request) {
	if s.Jellyfin == nil {
		http.Error(w, "Jellyfin is not configured", http.StatusServiceUnavailable)
		return
	}
	err := s.Jellyfin.RefreshLibrary(r.Context())
	s.log(r.Context(), r, "jellyfin.scan", "jellyfin", "Library/Refresh", nil, err)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Write([]byte(`<span class="chip up">Scanning…</span>`))
}

var errNotFound = &httpError{"not found"}

type httpError struct{ msg string }

func (e *httpError) Error() string { return e.msg }

var _ = context.Background
