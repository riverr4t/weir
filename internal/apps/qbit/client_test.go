package qbit

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
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", "qbit")
	var seen []string
	loggedIn := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.URL.Path == "/api/v2/auth/login":
			r.ParseForm()
			if r.Form.Get("username") != "u" || r.Form.Get("password") != "p" {
				w.Write([]byte("Fails."))
				return
			}
			loggedIn = true
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s", Path: "/"})
			w.Write([]byte("Ok."))
		case !loggedIn || cookie(r) != "s":
			w.WriteHeader(403)
		case r.URL.Path == "/api/v2/sync/maindata":
			f := "maindata-full.json"
			if r.URL.Query().Get("rid") != "0" {
				f = "maindata-delta.json"
			}
			b, _ := os.ReadFile(filepath.Join(root, f))
			w.Write(b)
		case r.URL.Path == "/api/v2/torrents/trackers":
			b, _ := os.ReadFile(filepath.Join(root, "trackers.json"))
			w.Write(b)
		case r.URL.Path == "/api/v2/app/version":
			b, _ := os.ReadFile(filepath.Join(root, "version.txt"))
			w.Write(b)
		case strings.HasPrefix(r.URL.Path, "/api/v2/torrents/"):
			w.Write([]byte(""))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func cookie(r *http.Request) string {
	if c, err := r.Cookie("SID"); err == nil {
		return c.Value
	}
	return ""
}

func TestSyncLogsInThenDeltas(t *testing.T) {
	srv, seen := server(t)
	c := New(srv.URL, "u", "p")
	ctx := context.Background()
	s1, err := c.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Version == "" || len(s1.Torrents) == 0 {
		t.Fatalf("full sync: version %q torrents %d", s1.Version, len(s1.Torrents))
	}
	for h, tr := range s1.Torrents {
		if tr.Hash != h || tr.Name == "" {
			t.Fatalf("torrent %s not filled: %+v", h, tr)
		}
		break
	}
	s2, err := c.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Torrents) != len(s1.Torrents) {
		t.Fatalf("delta changed torrent count %d -> %d", len(s1.Torrents), len(s2.Torrents))
	}
	joined := strings.Join(*seen, "\n")
	if !strings.Contains(joined, "POST /api/v2/auth/login") || !strings.Contains(joined, "sync/maindata?rid=0") || !strings.Contains(joined, "sync/maindata?rid=1") {
		t.Fatalf("unexpected request sequence:\n%s", joined)
	}
	if strings.Count(joined, "auth/login") != 1 {
		t.Fatal("should log in once")
	}
}

func TestDeepCopy(t *testing.T) {
	srv, _ := server(t)
	c := New(srv.URL, "u", "p")
	s, _ := c.Sync(context.Background())
	for h := range s.Torrents {
		delete(s.Torrents, h)
	}
	if len(c.state.Torrents) == 0 {
		t.Fatal("caller mutation leaked into client state")
	}
}

func TestStopStartAndTrackers(t *testing.T) {
	srv, seen := server(t)
	c := New(srv.URL, "u", "p")
	ctx := context.Background()
	if err := c.Stop(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	trs, err := c.Trackers(ctx, "abc")
	if err != nil || len(trs) == 0 {
		t.Fatalf("trackers: %v", err)
	}
	j := strings.Join(*seen, "\n")
	for _, want := range []string{"POST /api/v2/torrents/stop", "POST /api/v2/torrents/start", "GET /api/v2/torrents/trackers?hash=abc"} {
		if !strings.Contains(j, want) {
			t.Fatalf("missing %s in\n%s", want, j)
		}
	}
}

func TestBadPassword(t *testing.T) {
	srv, _ := server(t)
	if _, err := New(srv.URL, "u", "wrong").Sync(context.Background()); err == nil {
		t.Fatal("expected login failure")
	}
}
