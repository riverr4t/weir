package radarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/snapshot"
)

func TestStartFillsSnapshot(t *testing.T) {
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", "radarr")
	files := map[string]string{"/api/v3/queue": "queue.json", "/api/v3/health": "health.json",
		"/api/v3/system/status": "system-status.json", "/api/v3/diskspace": "diskspace.json",
		"/api/v3/calendar": "calendar.json", "/api/v3/movie": "movie.json", "/api/v3/tag": "tag.json"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v3/wanted/") {
			w.Write([]byte(`{"page":1,"pageSize":10,"totalRecords":0,"records":[]}`))
			return
		}
		b, err := os.ReadFile(filepath.Join(root, files[r.URL.Path]))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var s snapshot.Store
	Start(ctx, config.AppConfig{URL: srv.URL, Key: "k"}, &s, metrics.New(), nil)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r := &s.Radarr
		if !r.Queue.Get().FetchedAt.IsZero() && !s.RadarrMovies.Get().FetchedAt.IsZero() &&
			!r.Status.Get().FetchedAt.IsZero() && !r.Disk.Get().FetchedAt.IsZero() &&
			!r.Calendar.Get().FetchedAt.IsZero() && !r.Health.Get().FetchedAt.IsZero() &&
			!r.Missing.Get().FetchedAt.IsZero() && !r.Tags.Get().FetchedAt.IsZero() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("snapshot not filled within 3s")
}
