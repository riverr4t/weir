package prowlarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
)

func TestStartFillsCells(t *testing.T) {
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", "prowlarr")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := map[string]string{"/api/v1/system/status": "system-status.json", "/api/v1/health": "health.json", "/api/v1/indexer": "indexer.json", "/api/v1/indexerstats": "indexerstats.json", "/api/v1/applications": "applications.json"}[r.URL.Path]
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cells Cells
	Start(ctx, config.AppConfig{URL: srv.URL, Key: "k"}, &cells, metrics.New(), nil)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !cells.Indexers.Get().FetchedAt.IsZero() && !cells.Stats.Get().FetchedAt.IsZero() && !cells.Apps.Get().FetchedAt.IsZero() && !cells.Status.Get().FetchedAt.IsZero() {
			if len(cells.Indexers.Get().Data) == 0 || cells.Indexers.Get().Data[0].Name == "" {
				t.Fatalf("indexers: %+v", cells.Indexers.Get().Data)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cells not filled")
}
