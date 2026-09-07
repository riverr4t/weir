package poll

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/snapshot"
)

func TestDownAfterThreeFailuresThenRecovers(t *testing.T) {
	var cell snapshot.Cell[int]
	var mu sync.Mutex
	var transitions []bool
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	Run(ctx, Spec{App: "radarr", Kind: "queue", Interval: time.Millisecond,
		Notify: func(_ string, down bool) { mu.Lock(); transitions = append(transitions, down); mu.Unlock() }},
		&cell, metrics.New(), func(context.Context) (int, error) {
			calls++
			if calls <= 3 {
				return 0, errors.New("nope")
			}
			if calls == 5 {
				cancel()
			}
			return calls, nil
		})
	r := cell.Get()
	if r.Down || r.Err != nil || r.Data < 4 {
		t.Fatalf("expected recovered cell, got %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(transitions) != 2 || transitions[0] != true || transitions[1] != false {
		t.Fatalf("expected [down up], got %v", transitions)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	var cell snapshot.Cell[int]
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	Run(ctx, Spec{App: "a", Kind: "k", Interval: time.Millisecond}, &cell, metrics.New(), func(context.Context) (int, error) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		cancel()
		return 7, nil
	})
	if cell.Get().Data != 7 {
		t.Fatal("poller did not survive the panic")
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	if d := backoff(time.Second, 1); d < 1600*time.Millisecond || d > 2400*time.Millisecond {
		t.Fatalf("1 failure: %v", d)
	}
	if d := backoff(time.Second, 20); d > 6*time.Minute {
		t.Fatalf("should cap near 5m, got %v", d)
	}
}
