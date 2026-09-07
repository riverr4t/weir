package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/store"
)

// Runner evaluates stored rules against the snapshot, records runs, and
// applies tags. It never deletes.
type Runner struct {
	Src   Sources
	DB    *store.Store
	Arrs  map[string]*arr.Client // radarr sonarr lidarr readarr
	M     *metrics.M
	At    string // "HH:MM" local time for the daily run
	Local *time.Location
}

func clientFor(scope Scope, arrs map[string]*arr.Client) *arr.Client {
	switch scope {
	case Movie:
		return arrs["radarr"]
	case Series:
		return arrs["sonarr"]
	case Album:
		return arrs["lidarr"]
	case Book:
		return arrs["readarr"]
	}
	return nil
}

// RunRule evaluates one rule now and records the run. Returns the run id.
func (r *Runner) RunRule(ctx context.Context, sr store.Rule, now time.Time) (int64, error) {
	conds, err := ParseConditions(sr.Conditions)
	if err != nil {
		return 0, err
	}
	rule := Rule{ID: sr.ID, Name: sr.Name, Scope: Scope(sr.Scope), Enabled: sr.Enabled, Tag: sr.Tag, Conditions: conds}
	items := Collect(r.Src, rule.Scope)
	matches := Evaluate(rule, items, now)
	run := store.RuleRun{RuleID: sr.ID, Started: now, Matched: len(matches)}
	var ms []store.RuleMatch
	for _, m := range matches {
		run.Bytes += m.Item.Size
		ms = append(ms, store.RuleMatch{ItemID: m.Item.ID, Title: m.Item.Title, Path: m.Item.Path, Size: m.Item.Size, Reason: m.Reason})
	}
	var notes []string
	unmatched := 0
	for _, it := range items {
		if !it.InJellyfin && (rule.Scope == Movie || rule.Scope == Series) {
			unmatched++
		}
	}
	if unmatched > 0 && r.Src.Jellyfin != nil {
		notes = append(notes, fmt.Sprintf("%d items not found in Jellyfin (never counted as unplayed)", unmatched))
	}
	if rule.Tag != "" {
		if c := clientFor(rule.Scope, r.Arrs); c != nil {
			n, err := ApplyTag(ctx, c, rule.Scope, TagLabel(rule.Tag), items, matches)
			run.Tagged = n
			if err != nil {
				notes = append(notes, "tag: "+err.Error())
			}
		} else {
			notes = append(notes, "tag: no client for scope")
		}
	}
	run.Note = strings.Join(notes, "; ")
	run.Finished = time.Now()
	id, err := r.DB.RecordRun(ctx, run, ms)
	if err != nil {
		return 0, err
	}
	if r.M != nil {
		r.M.RuleMatches.WithLabelValues(sr.Name).Set(float64(run.Matched))
		r.M.RuleBytes.WithLabelValues(sr.Name).Set(float64(run.Bytes))
	}
	slog.Info("rule run", "rule", sr.Name, "matched", run.Matched, "bytes", run.Bytes, "tagged", run.Tagged, "note", run.Note)
	return id, nil
}

// RunAll runs every enabled rule.
func (r *Runner) RunAll(ctx context.Context, now time.Time) {
	rules, err := r.DB.Rules(ctx)
	if err != nil {
		slog.Error("rules: list", "err", err)
		return
	}
	for _, sr := range rules {
		if !sr.Enabled {
			continue
		}
		if _, err := r.RunRule(ctx, sr, now); err != nil {
			slog.Error("rule run failed", "rule", sr.Name, "err", err)
		}
	}
	_ = r.DB.PruneRuns(ctx, now.AddDate(0, 0, -90))
}

// Schedule runs RunAll daily at r.At (local time) until ctx is done.
func (r *Runner) Schedule(ctx context.Context) {
	loc := r.Local
	if loc == nil {
		loc = time.Local
	}
	for {
		now := time.Now().In(loc)
		next, err := nextAt(now, r.At)
		if err != nil {
			slog.Error("rules: bad WEIR_RULES_AT", "at", r.At, "err", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
			r.RunAll(ctx, time.Now())
		}
	}
}

func nextAt(now time.Time, hhmm string) (time.Time, error) {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil || h > 23 || m > 59 {
		return time.Time{}, fmt.Errorf("want HH:MM, got %q", hhmm)
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}

// ConditionsJSON is a helper for the editor round-trip.
func ConditionsJSON(cs []Condition) json.RawMessage {
	b, _ := json.Marshal(cs)
	return b
}
