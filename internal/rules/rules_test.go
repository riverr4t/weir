package rules

import (
	"testing"
	"time"
)

var now = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

func days(n int) time.Time { return now.AddDate(0, 0, -n) }

func TestEvaluateTable(t *testing.T) {
	alice := func(played bool, last time.Time, count int) PlayRecord {
		return PlayRecord{UserID: "a", UserName: "alice", Played: played, LastPlayed: last, PlayCount: count}
	}
	bob := func(played bool, last time.Time, count int) PlayRecord {
		return PlayRecord{UserID: "b", UserName: "bob", Played: played, LastPlayed: last, PlayCount: count}
	}
	cases := []struct {
		name string
		c    Condition
		it   Item
		want bool
	}{
		{"watched_by_all: both finished long ago", Condition{Kind: "watched_by_all", Days: 30}, Item{InJellyfin: true, Play: []PlayRecord{alice(true, days(60), 1), bob(true, days(45), 1)}}, true},
		{"watched_by_all: one started but not finished", Condition{Kind: "watched_by_all", Days: 30}, Item{InJellyfin: true, Play: []PlayRecord{alice(true, days(60), 1), bob(false, days(45), 1)}}, false},
		{"watched_by_all: finished recently", Condition{Kind: "watched_by_all", Days: 30}, Item{InJellyfin: true, Play: []PlayRecord{alice(true, days(3), 1)}}, false},
		{"watched_by_all: nobody touched it", Condition{Kind: "watched_by_all", Days: 30}, Item{InJellyfin: true, Play: []PlayRecord{alice(false, time.Time{}, 0)}}, false},
		{"watched_by_all: not in jellyfin never matches", Condition{Kind: "watched_by_all"}, Item{Play: []PlayRecord{alice(true, days(60), 1)}}, false},
		{"watched_by alice", Condition{Kind: "watched_by", Users: []string{"Alice"}, Days: 7}, Item{InJellyfin: true, Play: []PlayRecord{alice(true, days(10), 1), bob(false, days(1), 1)}}, true},
		{"watched_by alice+bob needs both", Condition{Kind: "watched_by", Users: []string{"alice", "bob"}}, Item{InJellyfin: true, Play: []PlayRecord{alice(true, days(10), 1)}}, false},
		{"never_played old", Condition{Kind: "never_played", Days: 90}, Item{InJellyfin: true, Added: days(120), Play: []PlayRecord{alice(false, time.Time{}, 0)}}, true},
		{"never_played but recent add", Condition{Kind: "never_played", Days: 90}, Item{InJellyfin: true, Added: days(10)}, false},
		{"never_played but someone started", Condition{Kind: "never_played", Days: 90}, Item{InJellyfin: true, Added: days(120), Play: []PlayRecord{alice(false, days(5), 1)}}, false},
		{"never_played: unmatched in jellyfin never counts", Condition{Kind: "never_played", Days: 90}, Item{Added: days(120)}, false},
		{"requester_done", Condition{Kind: "requester_done", Days: 14}, Item{InJellyfin: true, RequestedBy: "a", Play: []PlayRecord{alice(true, days(20), 1), bob(false, days(40), 0)}}, true},
		{"requester_done but other user watching", Condition{Kind: "requester_done", Days: 14}, Item{InJellyfin: true, RequestedBy: "a", Play: []PlayRecord{alice(true, days(20), 1), bob(false, days(2), 1)}}, false},
		{"requester_done requester not finished", Condition{Kind: "requester_done"}, Item{InJellyfin: true, RequestedBy: "a", Play: []PlayRecord{alice(false, days(2), 1)}}, false},
		{"ended_and_finished", Condition{Kind: "ended_and_finished"}, Item{InJellyfin: true, Ended: true, HasFile: true, Play: []PlayRecord{alice(true, days(9), 3)}}, true},
		{"ended but incomplete on disk", Condition{Kind: "ended_and_finished"}, Item{InJellyfin: true, Ended: true, HasFile: false, Play: []PlayRecord{alice(true, days(9), 3)}}, false},
		{"unmonitored with files", Condition{Kind: "unmonitored"}, Item{Monitored: false, HasFile: true}, true},
		{"unmonitored without files", Condition{Kind: "unmonitored"}, Item{Monitored: false, HasFile: false}, false},
		{"larger_than 10 GiB", Condition{Kind: "larger_than", GiB: 10}, Item{Size: 11 << 30}, true},
		{"larger_than not", Condition{Kind: "larger_than", GiB: 10}, Item{Size: 9 << 30}, false},
		{"added_before", Condition{Kind: "added_before", Days: 30}, Item{Added: days(31)}, true},
		{"tagged", Condition{Kind: "tagged", Tag: "keep"}, Item{Tags: []string{"Keep"}}, true},
		{"not_tagged", Condition{Kind: "not_tagged", Tag: "keep"}, Item{Tags: []string{"other"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := eval(c.c, c.it, now)
			if got != c.want {
				t.Fatalf("want %v got %v", c.want, got)
			}
		})
	}
}

func TestEvaluateANDsConditionsAndJoinsReasons(t *testing.T) {
	r := Rule{Conditions: []Condition{{Kind: "unmonitored"}, {Kind: "larger_than", GiB: 1}}}
	items := []Item{{ID: 1, Title: "big unmonitored", Monitored: false, HasFile: true, Size: 2 << 30},
		{ID: 2, Title: "big monitored", Monitored: true, HasFile: true, Size: 2 << 30},
		{ID: 3, Title: "small unmonitored", Monitored: false, HasFile: true, Size: 1 << 20}}
	ms := Evaluate(r, items, now)
	if len(ms) != 1 || ms[0].Item.ID != 1 || ms[0].Reason != "unmonitored; 2.0 GiB on disk" {
		t.Fatalf("got %+v", ms)
	}
	if len(Evaluate(Rule{}, items, now)) != 0 {
		t.Fatal("a rule with no conditions matches nothing")
	}
}

func TestParseConditionsRejectsUnknown(t *testing.T) {
	if _, err := ParseConditions([]byte(`[{"kind":"delete_everything"}]`)); err == nil {
		t.Fatal("expected error")
	}
	cs, err := ParseConditions([]byte(`[{"kind":"never_played","days":90}]`))
	if err != nil || len(cs) != 1 || cs[0].Days != 90 {
		t.Fatalf("%v %+v", err, cs)
	}
	if !KindAllowed("ended_and_finished", Series) || KindAllowed("ended_and_finished", Movie) {
		t.Fatal("scope gating wrong")
	}
}
