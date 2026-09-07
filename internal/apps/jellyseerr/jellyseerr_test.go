package jellyseerr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func server(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", "jellyseerr")
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(403)
			return
		}
		if r.Method == "POST" {
			w.WriteHeader(201)
			w.Write([]byte("{}"))
			return
		}
		f := map[string]string{"/api/v1/status": "status.json", "/api/v1/request/count": "count.json", "/api/v1/user": "users.json", "/api/v1/search": "search.json"}[r.URL.Path]
		if r.URL.Path == "/api/v1/request" {
			f = "requests.json"
			if r.URL.Query().Get("filter") == "pending" {
				f = "requests-pending.json"
			}
		}
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestReadsAndWrites(t *testing.T) {
	srv, seen := server(t)
	c := New(srv.URL, "k")
	ctx := context.Background()
	if st, err := c.Status(ctx); err != nil || st.Version == "" {
		t.Fatalf("status: %v", err)
	}
	if _, err := c.Count(ctx); err != nil {
		t.Fatal(err)
	}
	reqs, err := c.Requests(ctx, "all", 20)
	if err != nil || len(reqs) == 0 || reqs[0].Media.TmdbID == 0 {
		t.Fatalf("requests: %v %+v", err, reqs)
	}
	if _, err := c.Requests(ctx, "pending", 20); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Users(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := c.Search(ctx, "sneakers")
	if err != nil || len(res) == 0 || res[0].DisplayTitle() == "" {
		t.Fatalf("search: %v", err)
	}
	if err := c.Request(ctx, "tv", 42); err != nil {
		t.Fatal(err)
	}
	if err := c.Approve(ctx, 7); err != nil {
		t.Fatal(err)
	}
	j := strings.Join(*seen, "\n")
	for _, want := range []string{"POST /api/v1/request\n", "POST /api/v1/request/7/approve"} {
		if !strings.Contains(j+"\n", want) {
			t.Fatalf("missing %q in\n%s", want, j)
		}
	}
}
