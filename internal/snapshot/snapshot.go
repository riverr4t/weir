// Package snapshot holds the latest known state of every app (spec §5).
// Pollers write whole values; everything else reads copies.
package snapshot

import (
	"sync"
	"time"
)

type Result[T any] struct {
	Data      T
	FetchedAt time.Time // zero until the first successful poll
	Err       error     // last poll's error; Data keeps the last good value
	Down      bool      // true after 3 consecutive failures
}

func (r Result[T]) Age(now time.Time) time.Duration {
	if r.FetchedAt.IsZero() {
		return 0
	}
	return now.Sub(r.FetchedAt)
}

// Cell is one slot of the snapshot, safe for concurrent use.
type Cell[T any] struct {
	mu sync.RWMutex
	r  Result[T]
}

func (c *Cell[T]) Get() Result[T] { c.mu.RLock(); defer c.mu.RUnlock(); return c.r }

func (c *Cell[T]) SetOK(data T, at time.Time) {
	c.mu.Lock()
	c.r = Result[T]{Data: data, FetchedAt: at}
	c.mu.Unlock()
}

func (c *Cell[T]) SetErr(err error, down bool) {
	c.mu.Lock()
	c.r.Err, c.r.Down = err, down
	c.mu.Unlock()
}

// Store is the whole snapshot: one Cell per (app, kind). App packages add
// their typed cells here as they arrive.
type Store struct{}
