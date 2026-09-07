// Package arrapp starts the pollers every arr app shares (spec §5).
package arrapp

import (
	"context"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/poll"
	"github.com/riverr4t/weir/internal/snapshot"
)

// Spec builds a poll.Spec for one app and kind.
func Spec(app, kind string, every time.Duration, notify func(string, bool)) poll.Spec {
	return poll.Spec{App: app, Kind: kind, Interval: every, Notify: notify}
}

// StartCommon launches queue (15 s), health/status/disk/calendar/tags (5 min)
// and wanted counts (15 min) for one app into its ArrCells.
func StartCommon(ctx context.Context, app string, c *arr.Client, cells *snapshot.ArrCells, m *metrics.M, notify func(string, bool)) {
	go poll.Run(ctx, Spec(app, "queue", 15*time.Second, notify), &cells.Queue, m, c.Queue)
	go poll.Run(ctx, Spec(app, "health", 5*time.Minute, notify), &cells.Health, m, c.Health)
	go poll.Run(ctx, Spec(app, "status", 5*time.Minute, notify), &cells.Status, m, c.SystemStatus)
	go poll.Run(ctx, Spec(app, "disk", 5*time.Minute, notify), &cells.Disk, m, c.DiskSpace)
	go poll.Run(ctx, Spec(app, "tags", 5*time.Minute, notify), &cells.Tags, m, c.Tags)
	go poll.Run(ctx, Spec(app, "calendar", 5*time.Minute, notify), &cells.Calendar, m, func(ctx context.Context) ([]arr.CalendarItem, error) {
		now := time.Now()
		return c.Calendar(ctx, now.AddDate(0, 0, -1), now.AddDate(0, 0, 7))
	})
	go poll.Run(ctx, Spec(app, "missing", 15*time.Minute, notify), &cells.Missing, m, func(ctx context.Context) (arr.Wanted, error) {
		return c.Wanted(ctx, false, 1, 10)
	})
	go poll.Run(ctx, Spec(app, "cutoff", 15*time.Minute, notify), &cells.Cutoff, m, func(ctx context.Context) (arr.Wanted, error) {
		return c.Wanted(ctx, true, 1, 10)
	})
}
