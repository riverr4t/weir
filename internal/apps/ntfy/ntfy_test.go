package ntfy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type captured struct {
	mu      sync.Mutex
	bodies  []string
	headers []http.Header
}

func (c *captured) get() ([]string, []http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...), append([]http.Header(nil), c.headers...)
}

func capture(t *testing.T) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, r.URL.Path+" "+string(b))
		c.headers = append(c.headers, r.Header.Clone())
		c.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestPublishSetsHeaders(t *testing.T) {
	srv, cap := capture(t)
	p := New(srv.URL, "weir")
	err := p.Publish(context.Background(), Message{Title: "T", Body: "B", Click: "https://x", Priority: 4, Tags: []string{"warning"}})
	if err != nil {
		t.Fatal(err)
	}
	bodies, headers := cap.get()
	if bodies[0] != "/weir B" {
		t.Fatalf("body: %q", bodies[0])
	}
	h := headers[0]
	if h.Get("Title") != "T" || h.Get("Priority") != "4" || h.Get("Click") != "https://x" || h.Get("Tags") != "warning" {
		t.Fatalf("headers: %v", h)
	}
}

func TestNilPublisherIsSilent(t *testing.T) {
	p := New("", "")
	if p != nil {
		t.Fatal("empty url must yield nil")
	}
	if err := p.Publish(context.Background(), Message{Body: "x"}); err != nil {
		t.Fatal("nil publisher must not error")
	}
}

func TestCoalescerBatchesTransitions(t *testing.T) {
	srv, cap := capture(t)
	c := NewCoalescer(New(srv.URL, "weir"), 50*time.Millisecond, "https://weir")
	for _, a := range []string{"radarr", "sonarr", "qbit"} {
		c.Transition(a, true)
	}
	c.Transition("radarr", false)
	time.Sleep(150 * time.Millisecond)
	bodies, _ := cap.get()
	if len(bodies) != 1 {
		t.Fatalf("want one batched push, got %d: %v", len(bodies), bodies)
	}
	b := bodies[0]
	for _, want := range []string{"down: qbit, sonarr", "up: radarr"} {
		if !strings.Contains(b, want) {
			t.Fatalf("missing %q in %q", want, b)
		}
	}
}

func TestCoalescerNilPublisher(t *testing.T) {
	c := NewCoalescer(nil, time.Millisecond, "")
	c.Transition("radarr", true)
	c.Flush(context.Background())
}
