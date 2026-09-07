package snapshot

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCellLifecycle(t *testing.T) {
	var c Cell[[]int]
	r := c.Get()
	if !r.FetchedAt.IsZero() || r.Data != nil || r.Age(time.Now()) != 0 {
		t.Fatalf("zero cell wrong: %+v", r)
	}
	t0 := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	c.SetOK([]int{1, 2}, t0)
	c.SetErr(errors.New("boom"), true)
	r = c.Get()
	if len(r.Data) != 2 || r.FetchedAt != t0 || r.Err == nil || !r.Down {
		t.Fatalf("SetErr must keep data and mark down: %+v", r)
	}
	if r.Age(t0.Add(90*time.Second)) != 90*time.Second {
		t.Fatal("age wrong")
	}
	c.SetOK([]int{3}, t0.Add(time.Minute))
	r = c.Get()
	if r.Err != nil || r.Down || len(r.Data) != 1 {
		t.Fatalf("SetOK must clear err/down: %+v", r)
	}
}

func TestCellConcurrent(t *testing.T) {
	var c Cell[int]
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); c.SetOK(i, time.Now()) }(i)
		go func() { defer wg.Done(); _ = c.Get() }()
	}
	wg.Wait()
}
