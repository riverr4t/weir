package cleaner

import (
	"strings"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/qbit"
	"github.com/riverr4t/weir/internal/config"
)

type Condition string

const (
	Stalled     Condition = "stalled"
	Slow        Condition = "slow"
	DeadTracker Condition = "deadtracker"
	Orphaned    Condition = "orphaned"
)

// obs is what the cleaner remembers about a download between runs.
type obs struct {
	Downloaded   int64
	LastProgress time.Time
	SlowSince    time.Time // zero when not currently slow
}

// untouchable: the bytes are complete; spec §7 says never act.
func untouchable(item arr.QueueItem, t qbit.Torrent, found bool) bool {
	switch item.TrackedDownloadState {
	case "importPending", "importBlocked", "importFailed", "imported":
		return true
	}
	if item.Completed() {
		return true
	}
	return found && t.Progress >= 1
}

// skippedState: operator-controlled or transient states the cleaner leaves alone.
func skippedState(state string) bool {
	switch state {
	case "pausedDL", "stoppedDL", "checkingDL", "checkingResumeData", "queuedDL", "moving", "forcedMetaDL":
		return true
	}
	return false
}

func detect(cfg config.Cleaner, item arr.QueueItem, t qbit.Torrent, found bool, trackers []qbit.Tracker, prev obs, now time.Time) (conds []Condition, next obs, progressed bool) {
	next = prev
	if untouchable(item, t, found) {
		return nil, next, false
	}
	if !found {
		if item.Protocol == "torrent" {
			conds = append(conds, Orphaned)
		}
		return conds, next, false
	}
	if skippedState(t.State) {
		next.SlowSince = time.Time{}
		return nil, next, false
	}
	if prev.LastProgress.IsZero() || t.Downloaded > prev.Downloaded {
		progressed = !prev.LastProgress.IsZero()
		next.Downloaded, next.LastProgress = t.Downloaded, now
	}
	if !progressed && now.Sub(next.LastProgress) >= cfg.StallAfter {
		conds = append(conds, Stalled)
	}
	if t.DLSpeed < cfg.SlowBelow {
		if next.SlowSince.IsZero() {
			next.SlowSince = now
		}
		eta := time.Duration(t.ETA) * time.Second
		if now.Sub(next.SlowSince) >= cfg.SlowFor && (t.ETA <= 0 || eta > cfg.MaxETA) {
			conds = append(conds, Slow)
		}
	} else {
		next.SlowSince = time.Time{}
	}
	if dead(trackers) {
		conds = append(conds, DeadTracker)
	}
	return conds, next, progressed
}

// dead: every real tracker (status != 0, i.e. not the DHT/PeX/LSD pseudo-entries)
// is "not working" (4), or any carries an "unregistered" message.
func dead(trackers []qbit.Tracker) bool {
	real, bad := 0, 0
	for _, tr := range trackers {
		if strings.Contains(strings.ToLower(tr.Msg), "unregistered") {
			return true
		}
		if tr.Status == 0 {
			continue
		}
		real++
		if tr.Status == 4 {
			bad++
		}
	}
	return real > 0 && bad == real
}
