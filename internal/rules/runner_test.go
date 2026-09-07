package rules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/jellyfin"
	"github.com/riverr4t/weir/internal/snapshot"
	"github.com/riverr4t/weir/internal/store"
)

func TestTagLabel(t *testing.T) {
	for in, want := range map[string]string{"stale": "weir-stale", "Stale Movies!": "weir-stale-movies", "a--b": "weir-a-b", "": "weir"} {
		if got := TagLabel(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestNextAt(t *testing.T) {
	now := time.Date(2026, 9, 6, 5, 0, 0, 0, time.UTC)
	n, err := nextAt(now, "04:10")
	if err != nil || n.Day() != 7 || n.Hour() != 4 || n.Minute() != 10 {
		t.Fatalf("%v %v", n, err)
	}
	if _, err := nextAt(now, "25:00"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunRuleTagsAndRecords(t *testing.T) {
	var calls []string
	arrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/api/v3/tag" && r.Method == "GET":
			w.Write([]byte(`[{"id":1,"label":"keep"}]`))
		case r.URL.Path == "/api/v3/tag" && r.Method == "POST":
			w.Write([]byte(`{"id":9,"label":"weir-stale"}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer arrSrv.Close()
	db, _ := store.Open(filepath.Join(t.TempDir(), "weir.db"))
	defer db.Close()
	snap := &snapshot.Store{}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	snap.Radarr.Tags.SetOK([]arr.Tag{{ID: 1, Label: "keep"}}, now)
	snap.RadarrMovies.SetOK([]arr.Movie{
		{ID: 1, Title: "Old unwatched", TmdbID: 10, Added: now.AddDate(0, 0, -200), HasFile: true, Monitored: true, SizeOnDisk: 3 << 30},
		{ID: 2, Title: "Watched recently", TmdbID: 20, Added: now.AddDate(0, 0, -200), HasFile: true, Monitored: true, SizeOnDisk: 1 << 30},
		{ID: 3, Title: "Not in jellyfin", TmdbID: 30, Added: now.AddDate(0, 0, -200), HasFile: true, Monitored: true},
	}, now)
	jf := &jellyfin.Cells{}
	ps := jellyfin.PlayState{Users: []jellyfin.User{{ID: "u1", Name: "alice"}}, Items: map[string][]jellyfin.Item{}}
	var i1, i2 jellyfin.Item
	i1.Type, i1.ProviderIds = "Movie", map[string]string{"Tmdb": "10"}
	i2.Type, i2.ProviderIds = "Movie", map[string]string{"Tmdb": "20"}
	i2.UserData.Played, i2.UserData.PlayCount, i2.UserData.LastPlayedDate = true, 1, now.AddDate(0, 0, -2)
	ps.Items["u1"] = []jellyfin.Item{i1, i2}
	jf.PlayState.SetOK(ps, now)
	r := &Runner{Src: Sources{Snap: snap, Jellyfin: jf}, DB: db, Arrs: map[string]*arr.Client{"radarr": arr.New(arrSrv.URL, "k", "v3")}}
	id, err := db.SaveRule(context.Background(), store.Rule{Name: "stale", Scope: "movie", Enabled: true, Tag: "stale",
		Conditions: json.RawMessage(`[{"kind":"never_played","days":90}]`)}, now)
	if err != nil {
		t.Fatal(err)
	}
	sr, _ := db.Rule(context.Background(), id)
	runID, err := r.RunRule(context.Background(), sr, now)
	if err != nil {
		t.Fatal(err)
	}
	run, ms, err := db.Run(context.Background(), runID)
	if err != nil || run.Matched != 1 || len(ms) != 1 || ms[0].Title != "Old unwatched" || run.Bytes != 3<<30 || run.Tagged != 1 {
		t.Fatalf("run %+v matches %+v err %v", run, ms, err)
	}
	if !strings.Contains(run.Note, "1 items not found in Jellyfin") {
		t.Fatalf("note: %q", run.Note)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "POST /api/v3/tag") || !strings.Contains(joined, "PUT /api/v3/movie/editor") {
		t.Fatalf("calls:\n%s", joined)
	}
	if strings.Contains(joined, "DELETE") {
		t.Fatal("the rule engine must never delete")
	}
}
