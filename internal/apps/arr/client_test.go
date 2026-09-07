package arr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixtureServer(t *testing.T, app string, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", app)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		if r.Method == "DELETE" || r.Method == "POST" {
			w.Write([]byte("{}"))
			return
		}
		f, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestRadarrReads(t *testing.T) {
	srv, seen := fixtureServer(t, "radarr", map[string]string{
		"/api/v3/queue": "queue.json", "/api/v3/health": "health.json", "/api/v3/system/status": "system-status.json",
		"/api/v3/diskspace": "diskspace.json", "/api/v3/calendar": "calendar.json", "/api/v3/movie": "movie.json",
	})
	c := New(srv.URL, "k", "v3")
	ctx := context.Background()
	q, err := c.Queue(ctx)
	if err != nil || len(q) == 0 {
		t.Fatalf("queue: %v", err)
	}
	for _, it := range q {
		if it.Title == "" || it.DownloadID == "" {
			t.Fatalf("queue item missing fields: %+v", it)
		}
	}
	st, err := c.SystemStatus(ctx)
	if err != nil || st.Version == "" {
		t.Fatalf("status: %v %+v", err, st)
	}
	ds, err := c.DiskSpace(ctx)
	if err != nil || len(ds) == 0 || ds[0].TotalSpace == 0 {
		t.Fatalf("diskspace: %v %+v", err, ds)
	}
	if _, err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	if _, err := c.Calendar(ctx, from, from.AddDate(0, 0, 7)); err != nil {
		t.Fatal(err)
	}
	mv, err := c.Movies(ctx)
	if err != nil || len(mv) == 0 || mv[0].Title == "" || mv[0].TmdbID == 0 {
		t.Fatalf("movies: %v", err)
	}
	if (*seen)[0] != "GET /api/v3/queue?page=1&pageSize=1000&includeUnknownMovieItems=true&includeUnknownSeriesItems=true&includeUnknownAuthorItems=true&includeUnknownArtistItems=true" {
		t.Fatalf("queue query: %s", (*seen)[0])
	}
}

func TestSonarrSeriesAndQueue(t *testing.T) {
	srv, _ := fixtureServer(t, "sonarr", map[string]string{"/api/v3/series": "series.json", "/api/v3/queue": "queue.json"})
	c := New(srv.URL, "k", "v3")
	s, err := c.Series(context.Background())
	if err != nil || len(s) == 0 || s[0].TvdbID == 0 {
		t.Fatalf("series: %v", err)
	}
	q, err := c.Queue(context.Background())
	if err != nil || len(q) == 0 || q[0].EpisodeID == 0 {
		t.Fatalf("queue: %v", err)
	}
}

func TestDeleteQueueItemAndCommand(t *testing.T) {
	srv, seen := fixtureServer(t, "radarr", nil)
	c := New(srv.URL, "k", "v3")
	if err := c.DeleteQueueItem(context.Background(), 42, true); err != nil {
		t.Fatal(err)
	}
	if err := c.Command(context.Background(), "MissingMoviesSearch", nil); err != nil {
		t.Fatal(err)
	}
	want := "DELETE /api/v3/queue/42?blocklist=true&removeFromClient=true&skipRedownload=false"
	if (*seen)[0] != want || (*seen)[1] != "POST /api/v3/command" {
		t.Fatalf("got %v", *seen)
	}
}

func TestBadKeyIsAnError(t *testing.T) {
	srv, _ := fixtureServer(t, "radarr", map[string]string{"/api/v3/health": "health.json"})
	if _, err := New(srv.URL, "wrong", "v3").Health(context.Background()); err == nil {
		t.Fatal("expected 401 error")
	}
}

func TestTitleKeyAndCompleted(t *testing.T) {
	if (QueueItem{EpisodeID: 9, MovieID: 3}).TitleKey() != "episode:9" || (QueueItem{DownloadID: "h"}).TitleKey() != "download:h" {
		t.Fatal("title key")
	}
	if !(QueueItem{Size: 10, SizeLeft: 0}).Completed() || (QueueItem{Size: 10, SizeLeft: 1}).Completed() || (QueueItem{}).Completed() {
		t.Fatal("completed")
	}
}
