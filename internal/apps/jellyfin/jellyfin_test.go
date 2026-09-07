package jellyfin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func server(t *testing.T) *httptest.Server {
	t.Helper()
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", "jellyfin")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), `MediaBrowser Token="k"`) {
			w.WriteHeader(401)
			return
		}
		f := map[string]string{"/System/Info": "system-info.json", "/Sessions": "sessions.json", "/Items/Counts": "counts.json", "/Users": "users.json"}[r.URL.Path]
		if strings.HasPrefix(r.URL.Path, "/Users/") && strings.HasSuffix(r.URL.Path, "/Items") {
			f = "user-items.json"
		}
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReads(t *testing.T) {
	c := New(server(t).URL, "k")
	ctx := context.Background()
	info, err := c.Info(ctx)
	if err != nil || info.Version == "" {
		t.Fatalf("info: %v %+v", err, info)
	}
	if _, err := c.Sessions(ctx); err != nil {
		t.Fatal(err)
	}
	counts, err := c.Counts(ctx)
	if err != nil || counts.MovieCount == 0 {
		t.Fatalf("counts: %v %+v", err, counts)
	}
	ps, err := c.PlayState(ctx)
	if err != nil || len(ps.Users) == 0 {
		t.Fatalf("playstate: %v", err)
	}
	items := ps.Items[ps.Users[0].ID]
	if len(items) == 0 || items[0].ProviderIds["Tmdb"] == "" {
		t.Fatalf("items: %+v", items)
	}
}

func TestBadKey(t *testing.T) {
	if _, err := New(server(t).URL, "x").Info(context.Background()); err == nil {
		t.Fatal("expected 401")
	}
}
