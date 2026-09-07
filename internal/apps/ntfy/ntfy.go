// Package ntfy publishes to one topic and coalesces app down/up transitions
// into a single message per window (spec §5).
package ntfy

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Publisher struct {
	URL, Topic string
	HTTP       *http.Client
}

// New returns nil when url is empty; a nil *Publisher drops messages.
func New(url, topic string) *Publisher {
	if url == "" {
		return nil
	}
	return &Publisher{URL: strings.TrimRight(url, "/"), Topic: topic, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

type Message struct {
	Title, Body, Click string
	Priority           int
	Tags               []string
}

func (p *Publisher) Publish(ctx context.Context, m Message) error {
	if p == nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+"/"+p.Topic, strings.NewReader(m.Body))
	if err != nil {
		return err
	}
	if m.Title != "" {
		req.Header.Set("Title", m.Title)
	}
	if m.Priority != 0 {
		req.Header.Set("Priority", strconv.Itoa(m.Priority))
	}
	if m.Click != "" {
		req.Header.Set("Click", m.Click)
	}
	if len(m.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(m.Tags, ","))
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ntfy: %s", resp.Status)
	}
	return nil
}

// Coalescer collects down/up transitions for `window` after the first one,
// then sends one message. An app that goes down and comes back inside the
// window is reported by its final state only.
type Coalescer struct {
	p         *Publisher
	window    time.Duration
	publicURL string
	mu        sync.Mutex
	pending   map[string]bool
	timer     *time.Timer
}

func NewCoalescer(p *Publisher, window time.Duration, publicURL string) *Coalescer {
	return &Coalescer{p: p, window: window, publicURL: publicURL, pending: map[string]bool{}}
}

func (c *Coalescer) Transition(app string, down bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[app] = down
	if c.timer == nil {
		c.timer = time.AfterFunc(c.window, func() { c.Flush(context.Background()) })
	}
}

func (c *Coalescer) Flush(ctx context.Context) {
	c.mu.Lock()
	batch := c.pending
	c.pending = map[string]bool{}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.mu.Unlock()
	if len(batch) == 0 || c.p == nil {
		return
	}
	var downs, ups []string
	for app, d := range batch {
		if d {
			downs = append(downs, app)
		} else {
			ups = append(ups, app)
		}
	}
	sort.Strings(downs)
	sort.Strings(ups)
	var parts []string
	if len(downs) > 0 {
		parts = append(parts, "down: "+strings.Join(downs, ", "))
	}
	if len(ups) > 0 {
		parts = append(parts, "up: "+strings.Join(ups, ", "))
	}
	m := Message{Title: "Weir: app state", Body: strings.Join(parts, "\n"), Priority: 2, Click: c.publicURL + "/health"}
	if len(downs) == 0 {
		m.Tags = []string{"white_check_mark"}
	} else {
		m.Tags = []string{"warning"}
	}
	_ = c.p.Publish(ctx, m)
}
