package web

import (
	"container/list"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
)

// posterCache is an in-memory LRU of fetched poster bytes (spec §10: 32 MB, 24 h, nothing on disk).
type posterCache struct {
	mu    sync.Mutex
	max   int
	size  int
	ll    *list.List
	items map[string]*list.Element
	http  *http.Client
}

type posterEntry struct {
	key  string
	body []byte
	ct   string
	at   time.Time
}

func newPosterCache(maxBytes int) *posterCache {
	return &posterCache{max: maxBytes, ll: list.New(), items: map[string]*list.Element{}, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *posterCache) get(key string) (*posterEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*posterEntry)
	if time.Since(e.at) > 24*time.Hour {
		c.remove(el)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return e, true
}

func (c *posterCache) put(e *posterEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[e.key]; ok {
		c.remove(el)
	}
	el := c.ll.PushFront(e)
	c.items[e.key] = el
	c.size += len(e.body)
	for c.size > c.max && c.ll.Len() > 0 {
		c.remove(c.ll.Back())
	}
}

func (c *posterCache) remove(el *list.Element) {
	e := el.Value.(*posterEntry)
	c.ll.Remove(el)
	delete(c.items, e.key)
	c.size -= len(e.body)
}

func (c *posterCache) fetch(ctx context.Context, key, url string) (*posterEntry, error) {
	if e, ok := c.get(key); ok {
		return e, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, http.ErrMissingFile
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	e := &posterEntry{key: key, body: body, ct: resp.Header.Get("Content-Type"), at: time.Now()}
	c.put(e)
	return e, nil
}

// posterURL finds the remote poster for an arr item, sized for a grid tile:
// TMDb "original" posters are megabytes each, w342 is ~30 KB.
func posterURL(images []arr.Image) string {
	pick := ""
	for _, im := range images {
		if im.CoverType == "poster" && im.RemoteURL != "" {
			pick = im.RemoteURL
			break
		}
	}
	if pick == "" {
		for _, im := range images {
			if im.RemoteURL != "" {
				pick = im.RemoteURL
				break
			}
		}
	}
	return strings.Replace(pick, "image.tmdb.org/t/p/original/", "image.tmdb.org/t/p/w342/", 1)
}

// GET /img/{app}/{id}
func (s *Server) poster(w http.ResponseWriter, r *http.Request) {
	app, idStr := r.PathValue("app"), r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	url := ""
	switch app {
	case "radarr":
		for _, m := range s.Snap.RadarrMovies.Get().Data {
			if m.ID == id {
				url = posterURL(m.Images)
			}
		}
	case "sonarr":
		for _, m := range s.Snap.SonarrSeries.Get().Data {
			if m.ID == id {
				url = posterURL(m.Images)
			}
		}
	case "lidarr":
		if c := s.lidarr(); c != nil {
			for _, m := range c.Artists.Get().Data {
				if m.ID == id {
					url = posterURL(m.Images)
				}
			}
		}
	case "readarr":
		if c := s.readarr(); c != nil {
			for _, m := range c.Books.Get().Data {
				if m.ID == id {
					url = posterURL(m.Images)
				}
			}
		}
	case "tmdb":
		// Jellyseerr search results carry a TMDb poster path
		if p := r.URL.Query().Get("p"); p != "" && len(p) < 80 && p[0] == '/' {
			url = "https://image.tmdb.org/t/p/w342" + p
		}
	}
	if url == "" {
		http.NotFound(w, r)
		return
	}
	e, err := s.posters.fetch(r.Context(), app+"/"+idStr+r.URL.RawQuery, url)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", e.ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(e.body)
}
