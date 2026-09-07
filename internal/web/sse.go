package web

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/riverr4t/weir/internal/metrics"
)

type Hub struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
	m       *metrics.M
}

func NewHub(m *metrics.M) *Hub { return &Hub{clients: map[chan string]struct{}{}, m: m} }

// Broadcast sends one event; slow clients (full buffer) are dropped, not waited for.
func (h *Hub) Broadcast(event, data string) {
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, oneLine(data))
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
			delete(h.clients, ch)
			close(ch)
		}
	}
	h.m.SSEClients.Set(float64(len(h.clients)))
}

// Close drops every client so a graceful server shutdown does not wait on
// long-lived event streams.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		delete(h.clients, ch)
		close(ch)
	}
	h.m.SSEClients.Set(0)
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	ch := make(chan string, 16)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.m.SSEClients.Set(float64(len(h.clients)))
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if _, live := h.clients[ch]; live {
			delete(h.clients, ch)
			close(ch)
		}
		h.m.SSEClients.Set(float64(len(h.clients)))
		h.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": hello\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			fl.Flush()
		}
	}
}

func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }
