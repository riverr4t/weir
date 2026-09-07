package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/riverr4t/weir/internal/apps/jellyseerr"
)

type requestRow struct {
	ID         int64
	Type       string
	Title      string
	By         string
	Age        time.Time
	Status     int
	StatusText string
}

type requestsModel struct {
	Count   jellyseerr.Count
	Pending []requestRow
	Recent  []requestRow
}

type searchRow struct {
	ID       int64
	Type     string
	Title    string
	Year     string
	Img      string
	State    string // here | requested | ""
	Overview string
}

func (s *Server) requestRows(reqs []jellyseerr.Request) []requestRow {
	var out []requestRow
	for _, r := range reqs {
		row := requestRow{ID: r.ID, Type: r.Type, Title: s.titleFor(r.Type, r.Media.TmdbID, r.Media.TvdbID), By: r.RequestedBy.DisplayName, Age: r.CreatedAt, Status: r.Status}
		switch {
		case r.Media.Status == 5:
			row.StatusText = "available"
		case r.Status == 1:
			row.StatusText = "pending"
		case r.Status == 2:
			row.StatusText = "approved"
		case r.Status == 3:
			row.StatusText = "declined"
		}
		out = append(out, row)
	}
	return out
}

func (s *Server) requestsModel() *requestsModel {
	j := s.jellyseerr()
	if j == nil {
		return nil
	}
	return &requestsModel{Count: j.Count.Get().Data, Pending: s.requestRows(j.Pending.Get().Data), Recent: s.requestRows(j.Recent.Get().Data)}
}

func (s *Server) requests(w http.ResponseWriter, r *http.Request) {
	p := s.base("Requests", "requests")
	p.Requests = s.requestsModel()
	p.PublicURL = s.publicURL("jellyseerr")
	p.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	if p.Query != "" {
		p.Search = s.search(r, p.Query)
	}
	s.render(w, "requests.html", p)
}

func (s *Server) search(r *http.Request, q string) []searchRow {
	if s.Jellyseerr == nil || q == "" {
		return nil
	}
	res, err := s.Jellyseerr.Search(r.Context(), q)
	if err != nil {
		return nil
	}
	var out []searchRow
	for _, x := range res {
		row := searchRow{ID: x.ID, Type: x.MediaType, Title: x.DisplayTitle(), Year: x.Year(), State: x.State(), Overview: x.Overview}
		if x.PosterPath != "" {
			row.Img = "/img/tmdb/0?p=" + x.PosterPath
		}
		out = append(out, row)
		if len(out) == 12 {
			break
		}
	}
	return out
}

// GET /requests/search?q=
func (s *Server) searchFragment(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	s.render(w, "search-results", page{Query: q, Search: s.search(r, q), Now: s.Now()})
}

// POST /requests {mediaType, mediaId}
func (s *Server) request(w http.ResponseWriter, r *http.Request) {
	if s.Jellyseerr == nil {
		http.Error(w, "Jellyseerr is not configured", http.StatusServiceUnavailable)
		return
	}
	mt := r.FormValue("mediaType")
	id, _ := strconv.ParseInt(r.FormValue("mediaId"), 10, 64)
	if (mt != "movie" && mt != "tv") || id == 0 {
		http.Error(w, "mediaType must be movie or tv and mediaId set", http.StatusBadRequest)
		return
	}
	err := s.Jellyseerr.Request(r.Context(), mt, id)
	s.log(r.Context(), r, "request.create", "jellyseerr", r.FormValue("title"), map[string]any{"mediaType": mt, "mediaId": id}, err)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Write([]byte(`<span class="chip up">Requested</span>`))
}

// POST /requests/{id}/approve|decline
func (s *Server) requestDecision(kind string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Jellyseerr == nil {
			http.Error(w, "Jellyseerr is not configured", http.StatusServiceUnavailable)
			return
		}
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var err error
		if kind == "approve" {
			err = s.Jellyseerr.Approve(r.Context(), id)
		} else {
			err = s.Jellyseerr.Decline(r.Context(), id)
		}
		s.log(r.Context(), r, "request."+kind, "jellyseerr", strconv.FormatInt(id, 10), map[string]any{"id": id}, err)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Write([]byte(`<li class="muted">` + strings.ToUpper(kind[:1]) + kind[1:] + `d.</li>`))
	})
}

func (s *Server) publicURL(app string) string {
	for k, v := range s.Cfg.Apps {
		if string(k) == app {
			return v.PublicURL
		}
	}
	return ""
}
