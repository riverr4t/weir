// Package poll runs one fetch loop per (app, kind) with backoff, jitter,
// panic recovery and Down transitions (spec §5).
package poll

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"time"

	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/snapshot"
)

const (
	downAfter  = 3
	backoffCap = 5 * time.Minute
)

type Spec struct {
	App, Kind string
	Interval  time.Duration
	Notify    func(app string, down bool)
}

// backoff doubles the base per failure, caps at 5 minutes, and adds ±20 % jitter.
func backoff(base time.Duration, failures int) time.Duration {
	d := base
	for i := 0; i < failures && d < backoffCap; i++ {
		d *= 2
	}
	if d > backoffCap {
		d = backoffCap
	}
	j := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(d) * j)
}

// Run blocks until ctx is done. Callers start it with `go`.
func Run[T any](ctx context.Context, s Spec, cell *snapshot.Cell[T], m *metrics.M, fetch func(context.Context) (T, error)) {
	log := slog.With("app", s.App, "kind", s.Kind)
	failures := 0
	for {
		start := time.Now()
		data, err := safeFetch(ctx, fetch)
		m.PollDuration.WithLabelValues(s.App, s.Kind).Observe(time.Since(start).Seconds())
		wait := s.Interval
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			m.PollErrors.WithLabelValues(s.App, s.Kind).Inc()
			down := failures >= downAfter
			wasDown := cell.Get().Down
			cell.SetErr(err, down)
			if down && !wasDown {
				m.AppUp.WithLabelValues(s.App).Set(0)
				log.Warn("app down", "err", err)
				if s.Notify != nil {
					s.Notify(s.App, true)
				}
			} else if !down {
				log.Debug("poll failed", "err", err, "failures", failures)
			}
			wait = backoff(s.Interval, failures)
		} else {
			wasDown := cell.Get().Down
			failures = 0
			cell.SetOK(data, time.Now())
			m.AppUp.WithLabelValues(s.App).Set(1)
			m.SnapshotAge.WithLabelValues(s.App, s.Kind).Set(0)
			if wasDown {
				log.Info("app recovered")
				if s.Notify != nil {
					s.Notify(s.App, false)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func safeFetch[T any](ctx context.Context, fetch func(context.Context) (T, error)) (data T, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("poller panic", "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fetch(ctx)
}
