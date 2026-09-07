// Package rules evaluates library rules over a scope-agnostic view of the
// library (spec §8). It never deletes: a rule tags and lists, nothing else.
package rules

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Scope string

const (
	Movie  Scope = "movie"
	Series Scope = "series"
	Album  Scope = "album"
	Book   Scope = "book"
)

var Scopes = []Scope{Movie, Series, Album, Book}

// Condition kinds and which scopes they apply to.
var Kinds = []struct {
	Kind, Label, Help string
	Args              []string // "days", "gib", "users", "tag"
	Scopes            []Scope
}{
	{"watched_by_all", "Watched by everyone", "every Jellyfin user who played it has finished it, and nobody played it recently", []string{"days"}, []Scope{Movie, Series}},
	{"watched_by", "Watched by named users", "each named user has finished it, none played it recently", []string{"users", "days"}, []Scope{Movie, Series}},
	{"never_played", "Never played", "added long enough ago and no user has a play record", []string{"days"}, []Scope{Movie, Series}},
	{"requester_done", "Requester is done", "requested through Jellyseerr by someone who has finished it, and nobody else played it recently", []string{"days"}, []Scope{Movie, Series}},
	{"ended_and_finished", "Ended and finished", "series has ended, every episode is on disk, everyone who watched has finished", nil, []Scope{Series}},
	{"unmonitored", "Unmonitored", "the arr no longer monitors it but files are on disk", nil, Scopes},
	{"larger_than", "Larger than", "size on disk above the threshold", []string{"gib"}, Scopes},
	{"added_before", "Added more than N days ago", "", []string{"days"}, Scopes},
	{"tagged", "Has tag", "", []string{"tag"}, Scopes},
	{"not_tagged", "Does not have tag", "", []string{"tag"}, Scopes},
}

type Condition struct {
	Kind  string   `json:"kind"`
	Days  int      `json:"days,omitempty"`
	GiB   float64  `json:"gib,omitempty"`
	Users []string `json:"users,omitempty"`
	Tag   string   `json:"tag,omitempty"`
}

type Rule struct {
	ID         int64
	Name       string
	Scope      Scope
	Enabled    bool
	Tag        string
	Conditions []Condition
}

func ParseConditions(raw json.RawMessage) ([]Condition, error) {
	var cs []Condition
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &cs); err != nil {
		return nil, err
	}
	for _, c := range cs {
		if !kindValid(c.Kind) {
			return nil, fmt.Errorf("unknown condition %q", c.Kind)
		}
	}
	return cs, nil
}

func kindValid(k string) bool {
	for _, kd := range Kinds {
		if kd.Kind == k {
			return true
		}
	}
	return false
}

// KindAllowed reports whether a condition kind applies to a scope.
func KindAllowed(kind string, scope Scope) bool {
	for _, kd := range Kinds {
		if kd.Kind == kind {
			for _, s := range kd.Scopes {
				if s == scope {
					return true
				}
			}
		}
	}
	return false
}

// PlayRecord is one user's relationship to an item.
type PlayRecord struct {
	UserID, UserName string
	Played           bool
	PlayCount        int
	LastPlayed       time.Time
}

// Item is one library entry, whichever app it came from.
type Item struct {
	ID          int64
	Title       string
	Path        string
	Size        int64
	Added       time.Time
	Monitored   bool
	HasFile     bool // movies/albums/books: file present; series: every monitored episode on disk
	Ended       bool
	Tags        []string
	InJellyfin  bool
	Play        []PlayRecord
	RequestedBy string // Jellyfin user id of the Jellyseerr requester, if any
}

type Match struct {
	Item   Item
	Reason string
}

func (it Item) touched() bool {
	for _, p := range it.Play {
		if p.Played || p.PlayCount > 0 {
			return true
		}
	}
	return false
}

func (it Item) lastPlayed() time.Time {
	var t time.Time
	for _, p := range it.Play {
		if p.LastPlayed.After(t) {
			t = p.LastPlayed
		}
	}
	return t
}

func (it Item) hasTag(tag string) bool {
	for _, t := range it.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// eval returns (matched, reason) for one condition.
func eval(c Condition, it Item, now time.Time) (bool, string) {
	cut := now.AddDate(0, 0, -c.Days)
	switch c.Kind {
	case "watched_by_all":
		if !it.InJellyfin || !it.touched() {
			return false, ""
		}
		for _, p := range it.Play {
			if (p.PlayCount > 0 || p.Played) && !p.Played {
				return false, ""
			}
		}
		if lp := it.lastPlayed(); lp.After(cut) {
			return false, ""
		}
		return true, fmt.Sprintf("watched by everyone, last played %s", it.lastPlayed().Format("Jan 2 2006"))
	case "watched_by":
		if !it.InJellyfin {
			return false, ""
		}
		for _, u := range c.Users {
			ok := false
			for _, p := range it.Play {
				if strings.EqualFold(p.UserName, u) && p.Played && !p.LastPlayed.After(cut) {
					ok = true
				}
			}
			if !ok {
				return false, ""
			}
		}
		return true, "watched by " + strings.Join(c.Users, ", ")
	case "never_played":
		if !it.InJellyfin || it.touched() || it.Added.IsZero() || it.Added.After(cut) {
			return false, ""
		}
		return true, fmt.Sprintf("never played, added %s", it.Added.Format("Jan 2 2006"))
	case "requester_done":
		if it.RequestedBy == "" || !it.InJellyfin {
			return false, ""
		}
		var who string
		for _, p := range it.Play {
			if p.UserID == it.RequestedBy {
				if !p.Played {
					return false, ""
				}
				who = p.UserName
			} else if p.LastPlayed.After(cut) {
				return false, ""
			}
		}
		if who == "" {
			return false, ""
		}
		return true, "requested by " + who + ", who has finished it"
	case "ended_and_finished":
		if !it.Ended || !it.HasFile {
			return false, ""
		}
		ok, _ := eval(Condition{Kind: "watched_by_all"}, it, now)
		if !ok {
			return false, ""
		}
		return true, "ended, complete on disk, and watched"
	case "unmonitored":
		return !it.Monitored && it.HasFile, "unmonitored"
	case "larger_than":
		if float64(it.Size) > c.GiB*float64(1<<30) {
			return true, fmt.Sprintf("%.1f GiB on disk", float64(it.Size)/float64(1<<30))
		}
		return false, ""
	case "added_before":
		if !it.Added.IsZero() && it.Added.Before(cut) {
			return true, "added " + it.Added.Format("Jan 2 2006")
		}
		return false, ""
	case "tagged":
		return it.hasTag(c.Tag), "tagged " + c.Tag
	case "not_tagged":
		return !it.hasTag(c.Tag), "not tagged " + c.Tag
	}
	return false, ""
}

// Evaluate returns the items matching every condition of the rule.
func Evaluate(r Rule, items []Item, now time.Time) []Match {
	var out []Match
	if len(r.Conditions) == 0 {
		return nil
	}
	for _, it := range items {
		var reasons []string
		ok := true
		for _, c := range r.Conditions {
			m, why := eval(c, it, now)
			if !m {
				ok = false
				break
			}
			if why != "" {
				reasons = append(reasons, why)
			}
		}
		if ok {
			out = append(out, Match{Item: it, Reason: strings.Join(reasons, "; ")})
		}
	}
	return out
}
