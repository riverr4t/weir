package bazarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReads(t *testing.T) {
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", "bazarr")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "k" {
			w.WriteHeader(401)
			return
		}
		f := map[string]string{"/api/system/status": "system-status.json", "/api/movies/wanted": "movies-wanted.json", "/api/episodes/wanted": "episodes-wanted.json", "/api/providers": "providers.json"}[r.URL.Path]
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	c := New(srv.URL, "k")
	ctx := context.Background()
	st, err := c.Status(ctx)
	if err != nil || st.Version == "" {
		t.Fatalf("status: %v %+v", err, st)
	}
	if _, err := c.Wanted(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Wanted(ctx, "episodes"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Providers(ctx); err != nil {
		t.Fatal(err)
	}
}
