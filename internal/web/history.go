package web

import (
	"fmt"
	"strings"
	"sync"
)

// History is a ring buffer of the last N transfer samples for the overview sparkline.
type History struct {
	mu   sync.Mutex
	dl   []int64
	up   []int64
	head int
	n    int
}

func NewHistory(size int) *History { return &History{dl: make([]int64, size), up: make([]int64, size)} }

func (h *History) Add(dl, up int64) {
	h.mu.Lock()
	h.dl[h.head], h.up[h.head] = dl, up
	h.head = (h.head + 1) % len(h.dl)
	if h.n < len(h.dl) {
		h.n++
	}
	h.mu.Unlock()
}

// Samples returns oldest-first copies.
func (h *History) Samples() (dl, up []int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	size := len(h.dl)
	for i := 0; i < h.n; i++ {
		idx := (h.head - h.n + i + size) % size
		dl = append(dl, h.dl[idx])
		up = append(up, h.up[idx])
	}
	return
}

// Sparkline renders an SVG path (0..w by 0..h) for the samples.
func sparkline(v []int64, w, h float64) string {
	if len(v) < 2 {
		return ""
	}
	var max int64 = 1
	for _, x := range v {
		if x > max {
			max = x
		}
	}
	var b strings.Builder
	for i, x := range v {
		px := float64(i) / float64(len(v)-1) * w
		py := h - float64(x)/float64(max)*(h-2) - 1
		if i == 0 {
			fmt.Fprintf(&b, "M%.1f,%.1f", px, py)
		} else {
			fmt.Fprintf(&b, " L%.1f,%.1f", px, py)
		}
	}
	return b.String()
}
