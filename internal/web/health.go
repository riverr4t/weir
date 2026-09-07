package web

import (
	"net/http"
	"time"

	"github.com/riverr4t/weir/internal/config"
)

type appRow struct{ App, Version, State, Age, Err string }
type pollRow struct{ App, Kind, State, Age, Err string }
type indexerRow struct {
	Name, Protocol, Privacy string
	Enable                  bool
	Queries, Grabs, Failed  int
	Response                int
}
type subtitlesModel struct {
	Movies, Episodes int
	Providers        []struct{ Name, Status, Retry string }
}

type healthModel struct {
	Warnings  []Warning
	Apps      []appRow
	Polls     []pollRow
	Indexers  []indexerRow
	Subtitles *subtitlesModel
}

func ageStr(at time.Time, now time.Time) string {
	if at.IsZero() {
		return "never"
	}
	d := now.Sub(at)
	switch {
	case d < 2*time.Second:
		return "now"
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s ago"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m ago"
	}
	return itoa(int(d.Hours())) + "h ago"
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 90 {
		s = s[:90] + "…"
	}
	return s
}

func (s *Server) healthModel() *healthModel {
	now := s.Now()
	m := &healthModel{Warnings: s.warnings()}
	poll := func(app, kind string, at time.Time, down bool, err error) {
		m.Polls = append(m.Polls, pollRow{App: app, Kind: kind, State: cellState(at, down), Age: ageStr(at, now), Err: errStr(err)})
	}
	for _, app := range arrOrder {
		c := s.Snap.Arr(app)
		if c == nil || !s.Cfg.Enabled(config.App(app)) {
			continue
		}
		st := c.Status.Get()
		m.Apps = append(m.Apps, appRow{App: app, Version: st.Data.Version, State: cellState(st.FetchedAt, st.Down), Age: ageStr(st.FetchedAt, now), Err: errStr(st.Err)})
		q := c.Queue.Get()
		poll(app, "queue", q.FetchedAt, q.Down, q.Err)
		poll(app, "status", st.FetchedAt, st.Down, st.Err)
		h := c.Health.Get()
		poll(app, "health", h.FetchedAt, h.Down, h.Err)
		w := c.Missing.Get()
		poll(app, "wanted", w.FetchedAt, w.Down, w.Err)
	}
	if s.Cfg.Enabled(config.Qbit) {
		q := s.Snap.Qbit.Get()
		m.Apps = append(m.Apps, appRow{App: "qbit", Version: q.Data.Version, State: cellState(q.FetchedAt, q.Down), Age: ageStr(q.FetchedAt, now), Err: errStr(q.Err)})
		poll("qbit", "sync", q.FetchedAt, q.Down, q.Err)
	}
	if p := s.prowlarr(); p != nil {
		st := p.Status.Get()
		m.Apps = append(m.Apps, appRow{App: "prowlarr", Version: st.Data.Version, State: cellState(st.FetchedAt, st.Down), Age: ageStr(st.FetchedAt, now), Err: errStr(st.Err)})
		poll("prowlarr", "status", st.FetchedAt, st.Down, st.Err)
		ix := p.Indexers.Get()
		poll("prowlarr", "indexers", ix.FetchedAt, ix.Down, ix.Err)
		stats := map[int64]indexerRow{}
		for _, st := range p.Stats.Get().Data.Indexers {
			stats[st.IndexerID] = indexerRow{Queries: st.NumberOfQueries + st.NumberOfRssQueries, Grabs: st.NumberOfGrabs, Failed: st.NumberOfFailed + st.NumberOfFailedGrabs, Response: st.AverageResponseTime}
		}
		for _, in := range ix.Data {
			row := stats[in.ID]
			row.Name, row.Protocol, row.Privacy, row.Enable = in.Name, in.Protocol, in.Privacy, in.Enable
			m.Indexers = append(m.Indexers, row)
		}
	}
	if b := s.bazarr(); b != nil {
		st := b.Status.Get()
		m.Apps = append(m.Apps, appRow{App: "bazarr", Version: st.Data.Version, State: cellState(st.FetchedAt, st.Down), Age: ageStr(st.FetchedAt, now), Err: errStr(st.Err)})
		poll("bazarr", "status", st.FetchedAt, st.Down, st.Err)
		sub := &subtitlesModel{Movies: b.MoviesWanted.Get().Data.Total, Episodes: b.EpisodesWanted.Get().Data.Total}
		for _, pr := range b.Providers.Get().Data {
			sub.Providers = append(sub.Providers, struct{ Name, Status, Retry string }{pr.Name, pr.Status, pr.Retry})
		}
		m.Subtitles = sub
	}
	if j := s.jellyfin(); j != nil {
		st := j.Info.Get()
		m.Apps = append(m.Apps, appRow{App: "jellyfin", Version: st.Data.Version, State: cellState(st.FetchedAt, st.Down), Age: ageStr(st.FetchedAt, now), Err: errStr(st.Err)})
		poll("jellyfin", "info", st.FetchedAt, st.Down, st.Err)
		se := j.Sessions.Get()
		poll("jellyfin", "sessions", se.FetchedAt, se.Down, se.Err)
		ps := j.PlayState.Get()
		poll("jellyfin", "playstate", ps.FetchedAt, ps.Down, ps.Err)
	}
	if j := s.jellyseerr(); j != nil {
		st := j.Status.Get()
		m.Apps = append(m.Apps, appRow{App: "jellyseerr", Version: st.Data.Version, State: cellState(st.FetchedAt, st.Down), Age: ageStr(st.FetchedAt, now), Err: errStr(st.Err)})
		poll("jellyseerr", "status", st.FetchedAt, st.Down, st.Err)
		pe := j.Pending.Get()
		poll("jellyseerr", "pending", pe.FetchedAt, pe.Down, pe.Err)
	}
	return m
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	p := s.base("Health", "health")
	p.Health = s.healthModel()
	s.render(w, "health.html", p)
}

func (s *Server) apiHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Now(), s.healthModel(), nil)
}
