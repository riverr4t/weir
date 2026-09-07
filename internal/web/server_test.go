package web

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
	"github.com/riverr4t/weir/internal/apps/qbit"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/snapshot"
	"github.com/riverr4t/weir/internal/store"
)

type fakeQbit struct{ calls []string }

func (f *fakeQbit) Stop(_ context.Context, h string) error {
	f.calls = append(f.calls, "stop "+h)
	return nil
}
func (f *fakeQbit) Start(_ context.Context, h string) error {
	f.calls = append(f.calls, "start "+h)
	return nil
}

func testServer(t *testing.T) (*Server, *snapshot.Store, *fakeQbit, *[]string) {
	t.Helper()
	var arrCalls []string
	arrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrCalls = append(arrCalls, r.Method+" "+r.URL.RequestURI())
		w.Write([]byte("{}"))
	}))
	t.Cleanup(arrSrv.Close)
	db, _ := store.Open(filepath.Join(t.TempDir(), "weir.db"))
	t.Cleanup(func() { db.Close() })
	snap := &snapshot.Store{}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	snap.Radarr.Queue.SetOK([]arr.QueueItem{{ID: 1, Title: "Film", DownloadID: "aaa", Size: 100, SizeLeft: 40, MovieID: 3, TrackedDownloadState: "downloading"}}, now)
	snap.Sonarr.Queue.SetOK([]arr.QueueItem{{ID: 2, Title: "Show S01E01", DownloadID: "bbb", Size: 100, SizeLeft: 0, EpisodeID: 9, TrackedDownloadState: "importBlocked"}}, now)
	snap.Qbit.SetOK(qbit.State{DLSpeed: 1 << 20, Torrents: map[string]qbit.Torrent{
		"aaa": {Hash: "aaa", State: "downloading", Progress: .6, DLSpeed: 1 << 20, ETA: 40},
		"bbb": {Hash: "bbb", State: "uploading", Progress: 1},
	}}, now)
	fq := &fakeQbit{}
	cl := arr.New(arrSrv.URL, "k", "v3")
	h := New(Deps{Cfg: config.Config{Apps: map[config.App]config.AppConfig{"radarr": {}, "sonarr": {}, "qbit": {}}},
		Snap: snap, DB: db, M: metrics.New(), Arrs: map[string]*arr.Client{"radarr": cl, "sonarr": cl},
		Qbit: fq, Now: func() time.Time { return now }})
	return h, snap, fq, &arrCalls
}

func do(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var act = map[string]string{"X-Weir-Action": "1"}

func TestDownloadsPageRendersRows(t *testing.T) {
	h, _, _, _ := testServer(t)
	w := do(h, "GET", "/downloads", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Film") || !strings.Contains(body, "Show S01E01") {
		t.Fatalf("code %d body %.200s", w.Code, body)
	}
	for _, want := range []string{`hx-ext="sse"`, `X-Weir-Action`, "1 needs you", `class="crest"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestJSONEnvelope(t *testing.T) {
	h, _, _, _ := testServer(t)
	w := do(h, "GET", "/api/downloads", nil)
	var env struct {
		OK   bool  `json:"ok"`
		Data []Row `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || !env.OK || len(env.Data) != 2 {
		t.Fatalf("json: %v %s", err, w.Body.String())
	}
	// the import-blocked one is in trouble, so it sorts first
	if env.Data[0].App != "sonarr" || !env.Data[0].Completed || !env.Data[0].InTrouble || env.Data[1].Progress != .6 {
		t.Fatalf("rows: %+v", env.Data)
	}
}

func TestWritesRequireHeaderAndRefuseCompleted(t *testing.T) {
	h, _, fq, arrCalls := testServer(t)
	if w := do(h, "POST", "/downloads/aaa/pause", nil); w.Code != 403 {
		t.Fatalf("missing header should be 403, got %d", w.Code)
	}
	if w := do(h, "POST", "/downloads/aaa/pause", act); w.Code != 200 {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	if len(fq.calls) != 1 || fq.calls[0] != "stop aaa" {
		t.Fatalf("qbit calls: %v", fq.calls)
	}
	if w := do(h, "POST", "/downloads/aaa/blocklist", map[string]string{"X-Weir-Action": "1", "Tailscale-User-Login": "smm@github"}); w.Code != 200 {
		t.Fatalf("blocklist: %d %s", w.Code, w.Body.String())
	}
	if len(*arrCalls) != 1 || !strings.HasPrefix((*arrCalls)[0], "DELETE /api/v3/queue/1?") {
		t.Fatalf("arr calls: %v", *arrCalls)
	}
	if w := do(h, "POST", "/downloads/bbb/blocklist", act); w.Code != 409 {
		t.Fatalf("completed item must be refused with 409, got %d", w.Code)
	}
	if len(*arrCalls) != 1 {
		t.Fatal("no arr call may be made for a completed item")
	}
	acts, _ := h.DB.Actions(context.Background(), 10)
	if len(acts) != 2 || acts[0].Actor != "smm@github" {
		t.Fatalf("action log: %+v", acts)
	}
}

func TestHealthzAndMetrics(t *testing.T) {
	h, _, _, _ := testServer(t)
	if w := do(h, "GET", "/healthz", nil); w.Code != 200 {
		t.Fatal("healthz")
	}
	if w := do(h, "GET", "/metrics", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "weir_sse_clients") {
		t.Fatal("metrics")
	}
	if w := do(h, "GET", "/static/app.css", nil); w.Code != 200 {
		t.Fatal("static")
	}
}

func TestOverviewRenders(t *testing.T) {
	h, _, _, _ := testServer(t)
	w := do(h, "GET", "/", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Overview") || !strings.Contains(body, `sse-swap="overview"`) || !strings.Contains(body, "Film") {
		t.Fatalf("code %d, len %d", w.Code, len(body))
	}
	if w := do(h, "GET", "/api/overview", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"queueCount"`) {
		t.Fatalf("api overview: %d %.120s", w.Code, w.Body.String())
	}
}

func TestLibraryRequestsHealthRender(t *testing.T) {
	h, snap, _, _ := testServer(t)
	snap.RadarrMovies.SetOK([]arr.Movie{{ID: 3, Title: "Film", Year: 1992, TmdbID: 9273, HasFile: true, SizeOnDisk: 5 << 30}}, time.Now())
	for _, path := range []string{"/library/radarr", "/library/sonarr", "/health", "/api/library/radarr", "/api/health"} {
		if w := do(h, "GET", path, nil); w.Code != 200 {
			t.Fatalf("%s: %d %.200s", path, w.Code, w.Body.String())
		}
	}
	if w := do(h, "GET", "/library/lidarr", nil); w.Code != 404 {
		t.Fatalf("disabled app must 404, got %d", w.Code)
	}
	if w := do(h, "GET", "/library/radarr", nil); !strings.Contains(w.Body.String(), "Film") || !strings.Contains(w.Body.String(), "/img/radarr/3") {
		t.Fatal("library page should list the movie with its poster")
	}
	if w := do(h, "POST", "/library/radarr/search-missing", act); w.Code != 200 {
		t.Fatalf("search-missing: %d %s", w.Code, w.Body.String())
	}
	if w := do(h, "GET", "/requests", nil); w.Code != 200 {
		t.Fatalf("requests without jellyseerr configured should still render, got %d", w.Code)
	}
	if w := do(h, "POST", "/requests/1/approve", act); w.Code != 503 {
		t.Fatalf("approve without jellyseerr should be 503, got %d", w.Code)
	}
}

func TestPosterURLPrefersSmallTMDb(t *testing.T) {
	got := posterURL([]arr.Image{{CoverType: "fanart", RemoteURL: "https://image.tmdb.org/t/p/original/f.jpg"}, {CoverType: "poster", RemoteURL: "https://image.tmdb.org/t/p/original/p.jpg"}})
	if got != "https://image.tmdb.org/t/p/w342/p.jpg" {
		t.Fatalf("got %q", got)
	}
}

func TestRulesPagesAndSave(t *testing.T) {
	h, _, _, _ := testServer(t)
	for _, path := range []string{"/rules", "/rules/new"} {
		if w := do(h, "GET", path, nil); w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	form := "name=Stale&scope=movie&tag=stale&enabled=on&conditions=" + `[{"kind":"never_played","days":90}]`
	r := httptest.NewRequest("POST", "/rules", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Weir-Action", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/rules/") {
		t.Fatalf("save: %d %s %.200s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	loc := w.Header().Get("Location")
	if w := do(h, "GET", loc, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Stale") {
		t.Fatalf("edit page: %d", w.Code)
	}
	// a condition that does not fit the scope is refused
	bad := "name=X&scope=movie&conditions=" + `[{"kind":"ended_and_finished"}]`
	r = httptest.NewRequest("POST", "/rules", strings.NewReader(bad))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Weir-Action", "1")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 422 {
		t.Fatalf("bad scope condition should be 422, got %d", w.Code)
	}
	if w := do(h, "GET", "/api/rules", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Stale") {
		t.Fatal("api rules")
	}
	if w := do(h, "POST", loc+"/run", act); w.Code != 503 {
		t.Fatalf("run without a runner should be 503, got %d", w.Code)
	}
}
