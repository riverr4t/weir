package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "weir.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "weir.db")
	for i := 0; i < 2; i++ {
		s, err := Open(p)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		s.Close()
	}
}

func TestStrikesLifecycle(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC)
	for i := 1; i <= 3; i++ {
		n, err := s.AddStrike(ctx, "hash1", "radarr", "Film", "stalled", t0.Add(time.Duration(i)*time.Minute))
		if err != nil || n != i {
			t.Fatalf("strike %d: n=%d err=%v", i, n, err)
		}
	}
	if n, _ := s.AddStrike(ctx, "hash1", "radarr", "Film", "slow", t0); n != 1 {
		t.Fatal("conditions are independent")
	}
	all, _ := s.Strikes(ctx)
	if len(all) != 2 {
		t.Fatalf("want 2 strike rows, got %d", len(all))
	}
	if err := s.ClearStrikes(ctx, "hash1", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if all, _ = s.Strikes(ctx); len(all) != 0 {
		t.Fatal("cleared strikes must not be listed")
	}
	if n, _ := s.AddStrike(ctx, "hash1", "radarr", "Film", "stalled", t0.Add(2*time.Hour)); n != 1 {
		t.Fatalf("after clearing, counting restarts at 1, got %d", n)
	}
}

func TestActionsAndTitleCap(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		_, err := s.LogAction(ctx, Action{At: t0.Add(time.Duration(i) * time.Hour), Kind: "cleaner.remove", App: "sonarr",
			Subject: "Ep", Detail: `{"titleKey":"episode:9","condition":"stalled"}`, Actor: "weir", Outcome: "ok"})
		if err != nil {
			t.Fatal(err)
		}
	}
	s.LogAction(ctx, Action{At: t0, Kind: "cleaner.report", App: "sonarr", Subject: "Ep", Detail: `{"titleKey":"episode:9"}`, Outcome: "ok"})
	n, err := s.ActionsForTitleSince(ctx, "episode:9", t0.Add(30*time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("want 2 removes since t0+30m, got %d %v", n, err)
	}
	acts, _ := s.Actions(ctx, 10)
	if len(acts) != 4 || acts[0].At.Before(acts[1].At) {
		t.Fatalf("newest first, got %+v", acts)
	}
	if err := s.Prune(ctx, t0.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if acts, _ = s.Actions(ctx, 10); len(acts) != 1 {
		t.Fatalf("prune left %d", len(acts))
	}
}
