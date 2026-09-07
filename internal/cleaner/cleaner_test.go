package cleaner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/qbit"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/snapshot"
	"github.com/riverr4t/weir/internal/store"
)

var cfg = config.Cleaner{Mode: "act", Interval: 5 * time.Minute, StallAfter: 30 * time.Minute, SlowFor: 20 * time.Minute,
	MaxETA: 48 * time.Hour, SlowBelow: 50 * 1024, Strikes: 3, MaxActionsPerTitle: 3}

var t0 = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

func item(id int64, hash string, sizeLeft float64) arr.QueueItem {
	return arr.QueueItem{ID: id, Title: "Thing", DownloadID: hash, Size: 1000, SizeLeft: sizeLeft, Protocol: "torrent", MovieID: 7, TrackedDownloadState: "downloading"}
}

func TestDetectTable(t *testing.T) {
	working := []qbit.Tracker{{URL: "http://t", Status: 2}}
	blocked := item(1, "h", 500)
	blocked.TrackedDownloadState = "importBlocked"
	cases := []struct {
		name     string
		item     arr.QueueItem
		t        qbit.Torrent
		found    bool
		trackers []qbit.Tracker
		prev     obs
		now      time.Time
		want     []Condition
	}{
		{"healthy", item(1, "h", 500), qbit.Torrent{State: "downloading", Downloaded: 600, DLSpeed: 900000, ETA: 60}, true, working,
			obs{Downloaded: 500, LastProgress: t0}, t0.Add(time.Minute), nil},
		{"stalled 30m no progress", item(1, "h", 500), qbit.Torrent{State: "stalledDL", Downloaded: 500}, true, working,
			obs{Downloaded: 500, LastProgress: t0}, t0.Add(31 * time.Minute), []Condition{Stalled}},
		{"not yet stalled", item(1, "h", 500), qbit.Torrent{State: "stalledDL", Downloaded: 500}, true, working,
			obs{Downloaded: 500, LastProgress: t0}, t0.Add(10 * time.Minute), nil},
		{"slow and far ETA", item(1, "h", 500), qbit.Torrent{State: "downloading", Downloaded: 501, DLSpeed: 1000, ETA: 8640000}, true, working,
			obs{Downloaded: 500, LastProgress: t0, SlowSince: t0}, t0.Add(21 * time.Minute), []Condition{Slow}},
		{"slow but near ETA is fine", item(1, "h", 500), qbit.Torrent{State: "downloading", Downloaded: 501, DLSpeed: 1000, ETA: 600}, true, working,
			obs{Downloaded: 500, LastProgress: t0, SlowSince: t0}, t0.Add(21 * time.Minute), nil},
		{"dead tracker", item(1, "h", 500), qbit.Torrent{State: "downloading", Downloaded: 600, DLSpeed: 900000, ETA: 60}, true,
			[]qbit.Tracker{{URL: "** [DHT] **", Status: 0}, {URL: "http://t", Status: 4, Msg: "unregistered torrent"}},
			obs{Downloaded: 500, LastProgress: t0}, t0.Add(time.Minute), []Condition{DeadTracker}},
		{"orphaned", item(1, "h", 500), qbit.Torrent{}, false, nil, obs{}, t0, []Condition{Orphaned}},
		{"paused by operator is skipped", item(1, "h", 500), qbit.Torrent{State: "pausedDL", Downloaded: 500}, true, working,
			obs{Downloaded: 500, LastProgress: t0}, t0.Add(2 * time.Hour), nil},
		{"completed is never touched", item(1, "h", 0), qbit.Torrent{State: "stalledDL", Progress: 1}, true, nil,
			obs{Downloaded: 1000, LastProgress: t0}, t0.Add(5 * time.Hour), nil},
		{"import blocked is never touched", blocked, qbit.Torrent{State: "stalledDL"}, true, nil,
			obs{Downloaded: 500, LastProgress: t0}, t0.Add(5 * time.Hour), nil},
		{"orphaned but completed is never touched", item(1, "h", 0), qbit.Torrent{}, false, nil, obs{}, t0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, _ := detect(cfg, c.item, c.t, c.found, c.trackers, c.prev, c.now)
			if strings.Join(condStrings(got), ",") != strings.Join(condStrings(c.want), ",") {
				t.Fatalf("want %v got %v", c.want, got)
			}
		})
	}
}

func condStrings(cs []Condition) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

type harness struct {
	c       *Cleaner
	snap    *snapshot.Store
	db      *store.Store
	mu      sync.Mutex
	deletes []string
}

func (h *harness) deleted() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.deletes...)
}

type noTrackers struct{}

func (noTrackers) Trackers(context.Context, string) ([]qbit.Tracker, error) {
	return []qbit.Tracker{{URL: "http://t", Status: 2}}, nil
}

func newHarness(t *testing.T, mode string) *harness {
	t.Helper()
	h := &harness{snap: &snapshot.Store{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			h.mu.Lock()
			h.deletes = append(h.deletes, r.URL.RequestURI())
			h.mu.Unlock()
		}
		w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "weir.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h.db = db
	c := cfg
	c.Mode = mode
	h.c = New(c, h.snap, db, map[string]*arr.Client{"radarr": arr.New(srv.URL, "k", "v3")}, noTrackers{}, nil, metrics.New(), "https://weir")
	return h
}

func (h *harness) stalledFor(minutes int) {
	h.snap.Radarr.Queue.SetOK([]arr.QueueItem{item(42, "h", 500)}, t0)
	h.snap.Qbit.SetOK(qbit.State{Torrents: map[string]qbit.Torrent{"h": {Hash: "h", State: "stalledDL", Downloaded: 500}}}, t0)
	h.c.prev["h"] = obs{Downloaded: 500, LastProgress: t0.Add(-time.Duration(minutes) * time.Minute)}
}

func TestStrikesThenActInActMode(t *testing.T) {
	h := newHarness(t, "act")
	h.stalledFor(60)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		ds, err := h.c.Evaluate(ctx, t0.Add(time.Duration(i)*time.Minute))
		if err != nil || len(ds) != 1 || ds[0].Strikes != i {
			t.Fatalf("round %d: %+v %v", i, ds, err)
		}
		if ds[0].Act != (i == 3) {
			t.Fatalf("round %d: Act=%v", i, ds[0].Act)
		}
		h.c.Apply(ctx, ds, t0.Add(time.Duration(i)*time.Minute))
	}
	if d := h.deleted(); len(d) != 1 || d[0] != "/api/v3/queue/42?blocklist=true&removeFromClient=true&skipRedownload=false" {
		t.Fatalf("deletes: %v", d)
	}
	acts, _ := h.db.Actions(ctx, 10)
	if len(acts) != 1 || acts[0].Kind != "cleaner.remove" || !strings.Contains(acts[0].Detail, `"titleKey":"movie:7"`) {
		t.Fatalf("actions: %+v", acts)
	}
	if st, _ := h.db.Strikes(ctx); len(st) != 0 {
		t.Fatal("strikes must be cleared after acting")
	}
}

func TestReportModeNeverDeletes(t *testing.T) {
	h := newHarness(t, "report")
	h.stalledFor(60)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		ds, _ := h.c.Evaluate(ctx, t0.Add(time.Duration(i)*time.Minute))
		h.c.Apply(ctx, ds, t0.Add(time.Duration(i)*time.Minute))
	}
	if d := h.deleted(); len(d) != 0 {
		t.Fatalf("report mode deleted: %v", d)
	}
	acts, _ := h.db.Actions(ctx, 10)
	if len(acts) != 1 || acts[0].Kind != "cleaner.report" {
		t.Fatalf("exactly one report at the threshold, got %+v", acts)
	}
}

func TestOffModeDoesNothing(t *testing.T) {
	h := newHarness(t, "off")
	h.stalledFor(60)
	ds, _ := h.c.Evaluate(context.Background(), t0)
	if len(ds) != 0 {
		t.Fatal("off must not evaluate")
	}
}

func TestProgressClearsStrikes(t *testing.T) {
	h := newHarness(t, "act")
	h.stalledFor(60)
	ctx := context.Background()
	h.c.Evaluate(ctx, t0.Add(time.Minute))
	h.snap.Qbit.SetOK(qbit.State{Torrents: map[string]qbit.Torrent{"h": {Hash: "h", State: "downloading", Downloaded: 900, DLSpeed: 1 << 20, ETA: 10}}}, t0)
	ds, _ := h.c.Evaluate(ctx, t0.Add(2*time.Minute))
	if len(ds) != 0 {
		t.Fatalf("progress should yield no decisions: %+v", ds)
	}
	if st, _ := h.db.Strikes(ctx); len(st) != 0 {
		t.Fatal("strikes should be cleared on progress")
	}
}

func TestSeasonPackIsJudgedOnce(t *testing.T) {
	h := newHarness(t, "act")
	pack := []arr.QueueItem{item(1, "h", 500), item(2, "h", 500), item(3, "h", 500)}
	pack[1].EpisodeID, pack[2].EpisodeID = 11, 12
	h.snap.Radarr.Queue.SetOK(pack, t0)
	h.snap.Qbit.SetOK(qbit.State{Torrents: map[string]qbit.Torrent{"h": {Hash: "h", State: "stalledDL", Downloaded: 500}}}, t0)
	h.c.prev["h"] = obs{Downloaded: 500, LastProgress: t0.Add(-time.Hour)}
	ds, err := h.c.Evaluate(context.Background(), t0)
	if err != nil || len(ds) != 1 || ds[0].Strikes != 1 {
		t.Fatalf("want one decision with one strike, got %+v %v", ds, err)
	}
}

func TestPerTitleCap(t *testing.T) {
	h := newHarness(t, "act")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		h.db.LogAction(ctx, store.Action{At: t0.Add(-time.Hour), Kind: "cleaner.remove", App: "radarr", Subject: "Thing",
			Detail: `{"titleKey":"movie:7"}`, Outcome: "ok"})
	}
	h.stalledFor(60)
	var ds []Decision
	for i := 1; i <= 3; i++ {
		ds, _ = h.c.Evaluate(ctx, t0.Add(time.Duration(i)*time.Minute))
	}
	if !ds[0].Capped || ds[0].Act {
		t.Fatalf("expected capped, got %+v", ds[0])
	}
	h.c.Apply(ctx, ds, t0)
	if len(h.deleted()) != 0 {
		t.Fatal("capped title must not be deleted")
	}
}
