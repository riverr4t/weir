// Package cleaner removes downloads that will never finish (spec §7). It is
// the only code in Weir that removes anything, and it refuses completed items.
package cleaner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/ntfy"
	"github.com/riverr4t/weir/internal/apps/qbit"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/snapshot"
	"github.com/riverr4t/weir/internal/store"
)

type TrackerSource interface {
	Trackers(ctx context.Context, hash string) ([]qbit.Tracker, error)
}

type Decision struct {
	App       string
	Item      arr.QueueItem
	Condition Condition
	Strikes   int
	Act       bool // strike threshold reached and cap not exceeded
	Capped    bool // threshold reached but per-title cap hit
}

type Cleaner struct {
	cfg       config.Cleaner
	snap      *snapshot.Store
	db        *store.Store
	arrs      map[string]*arr.Client
	trackers  TrackerSource
	pub       *ntfy.Publisher
	m         *metrics.M
	publicURL string
	mu        sync.Mutex
	prev      map[string]obs
}

func New(cfg config.Cleaner, snap *snapshot.Store, db *store.Store, arrs map[string]*arr.Client, trackers TrackerSource,
	pub *ntfy.Publisher, m *metrics.M, publicURL string) *Cleaner {
	return &Cleaner{cfg: cfg, snap: snap, db: db, arrs: arrs, trackers: trackers, pub: pub, m: m, publicURL: publicURL, prev: map[string]obs{}}
}

func (c *Cleaner) Mode() string { return c.cfg.Mode }

func (c *Cleaner) Evaluate(ctx context.Context, now time.Time) ([]Decision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.Mode == "off" {
		return nil, nil
	}
	qs := c.snap.Qbit.Get()
	if qs.FetchedAt.IsZero() || qs.Down {
		return nil, nil // never judge against a missing torrent map
	}
	apps := make([]string, 0, len(c.arrs))
	for app := range c.arrs {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	seen := map[string]bool{}
	var out []Decision
	for _, app := range apps {
		cells := c.snap.Arr(app)
		if cells == nil {
			continue
		}
		q := cells.Queue.Get()
		if q.FetchedAt.IsZero() || q.Down {
			continue
		}
		for _, item := range q.Data {
			if item.DownloadID == "" {
				continue
			}
			seen[item.DownloadID] = true
			t, found := qs.Data.Torrents[item.DownloadID]
			var trs []qbit.Tracker
			if found && !untouchable(item, t, found) && !skippedState(t.State) {
				trs, _ = c.trackers.Trackers(ctx, item.DownloadID) // a failed tracker read means "not dead"
			}
			conds, next, progressed := detect(c.cfg, item, t, found, trs, c.prev[item.DownloadID], now)
			c.prev[item.DownloadID] = next
			if progressed {
				if err := c.db.ClearStrikes(ctx, item.DownloadID, now); err != nil {
					return nil, err
				}
			}
			for _, cond := range conds {
				n, err := c.db.AddStrike(ctx, item.DownloadID, app, item.Title, string(cond), now)
				if err != nil {
					return nil, err
				}
				d := Decision{App: app, Item: item, Condition: cond, Strikes: n}
				if n >= c.cfg.Strikes {
					used, err := c.db.ActionsForTitleSince(ctx, item.TitleKey(), now.Add(-24*time.Hour))
					if err != nil {
						return nil, err
					}
					if used >= c.cfg.MaxActionsPerTitle {
						d.Capped = true
					} else {
						d.Act = true
					}
				}
				out = append(out, d)
			}
		}
	}
	for hash := range c.prev {
		if !seen[hash] {
			delete(c.prev, hash)
		}
	}
	c.m.CleanerStrikes.Reset()
	if strikes, err := c.db.Strikes(ctx); err == nil {
		for _, s := range strikes {
			c.m.CleanerStrikes.WithLabelValues(s.Condition).Add(float64(s.Count))
		}
	}
	return out, nil
}

func (c *Cleaner) Apply(ctx context.Context, ds []Decision, now time.Time) {
	for _, d := range ds {
		atThreshold := d.Strikes == c.cfg.Strikes // report exactly once, at the threshold
		switch {
		case d.Capped && atThreshold:
			c.record(ctx, d, "cleaner.report", now, "ok", "", fmt.Sprintf("Giving up on %q for today: %d removals in 24 h already.", d.Item.Title, c.cfg.MaxActionsPerTitle))
		case d.Act && c.cfg.Mode == "act":
			err := c.arrs[d.App].DeleteQueueItem(ctx, d.Item.ID, true)
			outcome, errText := "ok", ""
			if err != nil {
				outcome, errText = "failed", err.Error()
			} else {
				_ = c.db.ClearStrikes(ctx, d.Item.DownloadID, now)
				c.mu.Lock()
				delete(c.prev, d.Item.DownloadID)
				c.mu.Unlock()
			}
			c.record(ctx, d, "cleaner.remove", now, outcome, errText, fmt.Sprintf("Removed, blocklisted and searching again: %s (%s).", d.Item.Title, d.Condition))
		case d.Act && c.cfg.Mode == "report" && atThreshold:
			c.record(ctx, d, "cleaner.report", now, "ok", "", fmt.Sprintf("Would remove: %s (%s).", d.Item.Title, d.Condition))
		}
	}
}

func (c *Cleaner) record(ctx context.Context, d Decision, kind string, now time.Time, outcome, errText, body string) {
	detail, _ := json.Marshal(map[string]any{"titleKey": d.Item.TitleKey(), "condition": string(d.Condition),
		"hash": d.Item.DownloadID, "queueId": d.Item.ID, "mode": c.cfg.Mode, "strikes": d.Strikes})
	if _, err := c.db.LogAction(ctx, store.Action{At: now, Kind: kind, App: d.App, Subject: d.Item.Title,
		Detail: string(detail), Actor: "weir", Outcome: outcome, Error: errText}); err != nil {
		slog.Error("cleaner: log action", "err", err)
	}
	c.m.CleanerActions.WithLabelValues(c.cfg.Mode, string(d.Condition)).Inc()
	slog.Info("cleaner", "kind", kind, "app", d.App, "title", d.Item.Title, "condition", d.Condition, "outcome", outcome, "err", errText)
	_ = c.pub.Publish(ctx, ntfy.Message{Title: "Weir cleaner", Body: body, Priority: 3, Click: c.publicURL + "/downloads",
		Tags: []string{"broom"}})
}

func (c *Cleaner) Run(ctx context.Context) {
	if c.cfg.Mode == "off" {
		slog.Info("cleaner off")
		return
	}
	slog.Info("cleaner running", "mode", c.cfg.Mode, "every", c.cfg.Interval)
	tick := time.NewTicker(c.cfg.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			ds, err := c.Evaluate(ctx, now)
			if err != nil {
				slog.Error("cleaner evaluate", "err", err)
				continue
			}
			c.Apply(ctx, ds, now)
		}
	}
}
