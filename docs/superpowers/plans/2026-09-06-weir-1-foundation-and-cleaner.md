# Weir Plan 1: Foundation, Radarr/Sonarr/qBittorrent, Downloads page, Cleaner (report mode)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go binary on the Pi at host port 3006 that polls Radarr, Sonarr and qBittorrent, shows the merged download queue live, and runs the queue cleaner in report mode.

**Architecture:** Pollers write into one in-memory snapshot behind a RWMutex; SQLite holds strikes and the action log; the web layer renders Go templates with HTMX and one SSE stream. The cleaner reads the snapshot every 5 minutes and, in report mode, only logs and notifies.

**Tech Stack:** Go 1.23+ (`go 1.23` in go.mod), `modernc.org/sqlite`, `github.com/prometheus/client_golang`, `net/http` method routing, `html/template`, `log/slog`, `embed`, HTMX vendored.

**Spec:** `docs/superpowers/specs/2026-09-06-weir-design.md` (this plan implements §3–§7, §9 partially, §10 downloads page, §11 poll metrics, §12; later plans cover the other six apps, rules, health page, cutover).

## Global Constraints

- **No delete route, no delete code path for library media.** The only upstream removal is the arr queue DELETE in the cleaner and the `/downloads/{hash}/blocklist` write, both on uncompleted downloads only.
- Module path `github.com/riverr4t/weir`. Go 1.23 in go.mod. `CGO_ENABLED=0` everywhere.
- Dependencies limited to `modernc.org/sqlite` and `github.com/prometheus/client_golang`. No web frameworks, no ORMs, no bundlers.
- Every env var is prefixed `WEIR_`. Defaults exactly as spec §4.
- Write routes are `POST` and require header `X-Weir-Action: 1`; otherwise 403.
- JSON reads answer `{"ok":true,"fetchedAt":...,"data":...}` or `{"ok":false,"error":"..."}` with HTTP 200.
- Cleaner default mode `report`. Items with `trackedDownloadState` in `importPending|importBlocked|importFailed`, or progress ≥ 1.0, are never acted on.
- `gofmt -l` must print nothing; `go vet ./...` clean; `go test ./...` is the gate. Run tests once, narrowly, per task (Work/CLAUDE.md rule 3).
- Commit after every task with the trailer `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- Fixtures under `testdata/fixtures/<app>/` are recorded from the Pi and redacted (keys, `ratholepi` hostnames, tailnet names). The gitleaks pre-commit hook runs on every commit.

## File structure (this plan)

```
go.mod, go.sum
cmd/weir/main.go                 wiring; `weir healthcheck`
internal/config/config.go        Config, Load(getenv) → (Config, error) all errors at once
internal/config/config_test.go
internal/snapshot/snapshot.go    Result[T], Store with typed fields, Get/Set per app
internal/snapshot/snapshot_test.go
internal/metrics/metrics.go      registry + collectors used by poll and cleaner
internal/poll/poll.go            Run(ctx, Poller): interval, backoff, jitter, panic recovery, Down transitions
internal/poll/poll_test.go
internal/apps/arr/client.go      Client{BaseURL, Key, APIVersion}; Queue, Health, SystemStatus, DiskSpace, Calendar, DeleteQueueItem
internal/apps/arr/types.go
internal/apps/arr/client_test.go
internal/apps/radarr/radarr.go   Movies(); Poller wiring for queue/health/library
internal/apps/sonarr/sonarr.go   Series(); same
internal/apps/qbit/client.go     Login, MainData(rid) delta merge, Trackers, Stop, Start
internal/apps/qbit/client_test.go
internal/apps/ntfy/ntfy.go       Publish; Coalescer for down/up
internal/apps/ntfy/ntfy_test.go
internal/store/store.go          Open, migrate; strikes + actions queries
internal/store/migrations/0001_init.sql
internal/store/store_test.go
internal/cleaner/cleaner.go      Evaluate(snapshot) → decisions; Run loop; modes
internal/cleaner/cleaner_test.go
internal/web/server.go           router, middleware (action header), JSON negotiation
internal/web/templates/*.html    layout, downloads
internal/web/static/htmx.min.js, static/app.css
internal/web/sse.go              hub
internal/web/server_test.go
tools/record-fixtures.sh         run from the dev box; ssh to the Pi, curl, redact
Dockerfile, .github/workflows/ci.yml, .env.example, README.md
```

---

### Task 0: Fixture spike against the Pi (also proves Bookshelf speaks Readarr v1)

**Files:**
- Create: `tools/record-fixtures.sh`
- Create: `testdata/fixtures/{radarr,sonarr,qbit,readarr}/*.json`

**Interfaces:**
- Produces: fixture files that Tasks 5, 6, 7 replay. File names are fixed below and referenced verbatim by later tests.

- [ ] **Step 1: Write the recorder**

The Pi holds the keys in `~/ratholepi-stack/homepage/.env` (`HOMEPAGE_VAR_*`). Apps are reachable on the Pi's loopback: radarr 7878, sonarr 8989, qBittorrent 8080, readarr (Bookshelf) 8788. Readarr's key is not in that file; read it from `/srv/appdata/readarr/config.xml` on the Pi.

```bash
#!/usr/bin/env bash
# Record redacted API fixtures from the live Pi apps into testdata/fixtures/.
# Runs on the dev box; everything happens over one ssh session.
set -euo pipefail
cd "$(dirname "$0")/.."
out=testdata/fixtures
mkdir -p $out/{radarr,sonarr,qbit,readarr}

ssh ratholepi 'bash -s' <<'REMOTE' > /tmp/weir-fixtures.tar
set -euo pipefail
set -a; . ~/ratholepi-stack/homepage/.env; set +a
RK=$(sed -n "s:.*<ApiKey>\(.*\)</ApiKey>.*:\1:p" /srv/appdata/readarr/config.xml)
d=$(mktemp -d); mkdir -p $d/{radarr,sonarr,qbit,readarr}
arr() { curl -sf -H "X-Api-Key: $2" "http://127.0.0.1:$1/api/$3"; }
for a in "radarr 7878 $HOMEPAGE_VAR_RADARR_KEY v3" "sonarr 8989 $HOMEPAGE_VAR_SONARR_KEY v3" "readarr 8788 $RK v1"; do
  set -- $a
  arr $2 $3 "$4/system/status"  > $d/$1/system-status.json
  arr $2 $3 "$4/health"         > $d/$1/health.json
  arr $2 $3 "$4/diskspace"      > $d/$1/diskspace.json
  arr $2 $3 "$4/queue?page=1&pageSize=1000&includeUnknownMovieItems=true&includeUnknownSeriesItems=true&includeUnknownAuthorItems=true" > $d/$1/queue.json
  arr $2 $3 "$4/calendar?start=$(date -u +%F)&end=$(date -u -d '+7 days' +%F)" > $d/$1/calendar.json
  arr $2 $3 "$4/tag"            > $d/$1/tag.json
done
arr 7878 $HOMEPAGE_VAR_RADARR_KEY v3/movie  > $d/radarr/movie.json
arr 8989 $HOMEPAGE_VAR_SONARR_KEY v3/series > $d/sonarr/series.json
arr 8788 $RK v1/author                      > $d/readarr/author.json
arr 8788 $RK v1/book                        > $d/readarr/book.json
arr 8788 $RK "v1/wanted/missing?page=1&pageSize=10" > $d/readarr/wanted-missing.json
# qBittorrent: cookie login, then delta sync twice (rid 0 = full)
j=$(mktemp)
curl -sf -c $j --data-urlencode "username=$HOMEPAGE_VAR_QBIT_USER" --data-urlencode "password=$HOMEPAGE_VAR_QBIT_PASS" http://127.0.0.1:8080/api/v2/auth/login >/dev/null
curl -sf -b $j "http://127.0.0.1:8080/api/v2/sync/maindata?rid=0" > $d/qbit/maindata-full.json
sleep 2
rid=$(python3 -c "import json;print(json.load(open('$d/qbit/maindata-full.json'))['rid'])")
curl -sf -b $j "http://127.0.0.1:8080/api/v2/sync/maindata?rid=$rid" > $d/qbit/maindata-delta.json
h=$(python3 -c "import json;t=json.load(open('$d/qbit/maindata-full.json'))['torrents'];print(next(iter(t)) if t else '')")
[ -n "$h" ] && curl -sf -b $j "http://127.0.0.1:8080/api/v2/torrents/trackers?hash=$h" > $d/qbit/trackers.json || echo '[]' > $d/qbit/trackers.json
curl -sf -b $j http://127.0.0.1:8080/api/v2/app/version > $d/qbit/version.txt
tar -C $d -cf - .
REMOTE

tar -C $out -xf /tmp/weir-fixtures.tar
rm /tmp/weir-fixtures.tar
# redact: hostnames, keys, tailnet names, LAN addresses
find $out -type f -name '*.json' -exec sed -i -E \
  -e 's/\b[0-9a-f]{32}\b/REDACTED_KEY/g' \
  -e 's/ratholepi\.tail[a-z0-9]+\.ts\.net/HOST/g' \
  -e 's/192\.168\.[0-9]+\.[0-9]+/10.0.0.1/g' \
  -e 's/"(apiKey|password|passwordConfirmation)": *"[^"]*"/"\1":"REDACTED"/g' {} +
echo "recorded:"; find $out -type f | sort
```

- [ ] **Step 2: Run it**

Run: `bash tools/record-fixtures.sh`
Expected: a list of files under `testdata/fixtures/`. If any `curl -sf` fails, the script stops at that line; note which app and endpoint.

- [ ] **Step 3: Check the Bookshelf answers**

Run: `python3 -c "import json;[print(f, type(json.load(open('testdata/fixtures/readarr/'+f))).__name__) for f in ['system-status.json','queue.json','author.json','book.json','wanted-missing.json','tag.json']]"`
Expected: six lines, each naming a `dict` or `list`. If any file is missing or is not JSON, record that in the spec §3 line for `internal/apps/readarr/` before Plan 3 starts.

- [ ] **Step 4: Scan and commit**

Run: `grep -rlE 'tail[a-z0-9]{8}|[0-9a-f]{32}' testdata/fixtures | wc -l`
Expected: `0`

```bash
git add tools/record-fixtures.sh testdata/fixtures
git commit -m "Record redacted fixtures from the Pi; Bookshelf answers Readarr v1

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 1: Module, main, healthz, `weir healthcheck`

**Files:**
- Create: `go.mod`, `cmd/weir/main.go`, `cmd/weir/main_test.go`

**Interfaces:**
- Produces: `func healthcheck(addr string) error` used by the Dockerfile HEALTHCHECK via `weir healthcheck`; main reads `WEIR_LISTEN` only for now (config comes in Task 2 and replaces this).

- [ ] **Step 1: Init the module**

Run: `cd ~/Work/weir && go mod init github.com/riverr4t/weir && sed -i 's/^go .*/go 1.23/' go.mod && cat go.mod`
Expected: `module github.com/riverr4t/weir` and `go 1.23`.

- [ ] **Step 2: Write the failing test**

```go
// cmd/weir/main_test.go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheckOKWhenHealthzIs200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	if err := healthcheck(srv.URL); err != nil {
		t.Fatalf("expected healthy, got %v", err)
	}
}

func TestHealthcheckFailsWhenDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	if err := healthcheck(srv.URL); err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 3: Run it to see it fail**

Run: `go test ./cmd/weir/ 2>&1 | tail -3`
Expected: build failure, `undefined: healthcheck`.

- [ ] **Step 4: Write main**

```go
// cmd/weir/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := listenURL(os.Getenv("WEIR_LISTEN"))
		if err := healthcheck(addr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("weir exited", "err", err)
		os.Exit(1)
	}
}

// listenURL turns a bind address like ":3004" or "0.0.0.0:3004" into a loopback URL.
func listenURL(listen string) string {
	if listen == "" {
		listen = ":3004"
	}
	_, port, ok := strings.Cut(listen, ":")
	if !ok || port == "" {
		port = "3004"
	}
	return "http://127.0.0.1:" + port
}

func healthcheck(base string) error {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(base + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

func run() error {
	listen := os.Getenv("WEIR_LISTEN")
	if listen == "" {
		listen = ":3004"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("weir listening", "addr", listen)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
```

- [ ] **Step 5: Run the test**

Run: `go test ./cmd/weir/ 2>&1 | tail -1`
Expected: `ok  	github.com/riverr4t/weir/cmd/weir`

- [ ] **Step 6: Commit**

```bash
git add go.mod cmd/weir
git commit -m "Module, main with /healthz and the healthcheck subcommand

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: Config from the environment, all errors at once

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`
- Modify: `cmd/weir/main.go` (`run()` calls `config.Load(os.Getenv)`)

**Interfaces:**
- Produces:
  ```go
  type App string // "radarr" "sonarr" "lidarr" "readarr" "prowlarr" "qbit" "bazarr" "jellyfin" "jellyseerr"
  type AppConfig struct{ URL, Key, User, Pass, PublicURL string }
  type Cleaner struct {
      Mode string; Interval, StallAfter, SlowFor, MaxETA time.Duration
      SlowBelow int64 // bytes/s
      Strikes, MaxActionsPerTitle int
  }
  type Config struct {
      Listen, DataDir, MediaDir, PublicURL, NtfyURL, NtfyTopic, RulesAt, LogLevel string
      Apps map[App]AppConfig   // only apps with a URL set
      Cleaner Cleaner
  }
  func Load(getenv func(string) string) (Config, error)
  func (c Config) Enabled(a App) bool
  ```

- [ ] **Step 1: Write the failing tests**

```go
// internal/config/config_test.go
package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":3004" || c.DataDir != "/config" || c.MediaDir != "/data" {
		t.Fatalf("defaults wrong: %+v", c)
	}
	if c.Cleaner.Mode != "report" || c.Cleaner.Interval != 5*time.Minute || c.Cleaner.StallAfter != 30*time.Minute ||
		c.Cleaner.SlowFor != 20*time.Minute || c.Cleaner.MaxETA != 48*time.Hour || c.Cleaner.SlowBelow != 50*1024 ||
		c.Cleaner.Strikes != 3 || c.Cleaner.MaxActionsPerTitle != 3 {
		t.Fatalf("cleaner defaults wrong: %+v", c.Cleaner)
	}
	if c.RulesAt != "04:10" || c.LogLevel != "info" || c.PublicURL != "https://ratholepi.tail98e0c3.ts.net:3004" {
		t.Fatalf("misc defaults wrong: %+v", c)
	}
	if len(c.Apps) != 0 {
		t.Fatalf("no apps expected, got %v", c.Apps)
	}
}

func TestAppsAndAllErrorsAtOnce(t *testing.T) {
	_, err := Load(env(map[string]string{
		"WEIR_RADARR_URL":   "http://radarr:7878", // key missing
		"WEIR_QBIT_URL":     "http://gluetun:8080", // user/pass missing
		"WEIR_CLEANER_MODE": "maybe",
	}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"WEIR_RADARR_KEY", "WEIR_QBIT_USER", "WEIR_QBIT_PASS", "WEIR_CLEANER_MODE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

func TestEnabledAppsParsed(t *testing.T) {
	c, err := Load(env(map[string]string{
		"WEIR_SONARR_URL": "http://sonarr:8989/", "WEIR_SONARR_KEY": "k", "WEIR_SONARR_PUBLIC_URL": "https://h:8989",
		"WEIR_QBIT_URL": "http://gluetun:8080", "WEIR_QBIT_USER": "u", "WEIR_QBIT_PASS": "p",
		"WEIR_CLEANER_SLOW_BELOW": "1MiB", "WEIR_CLEANER_STRIKES": "5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled("sonarr") || c.Enabled("radarr") {
		t.Fatal("Enabled wrong")
	}
	if c.Apps["sonarr"].URL != "http://sonarr:8989" { // trailing slash trimmed
		t.Fatalf("url: %q", c.Apps["sonarr"].URL)
	}
	if c.Apps["qbit"].User != "u" || c.Cleaner.SlowBelow != 1<<20 || c.Cleaner.Strikes != 5 {
		t.Fatalf("parsed wrong: %+v %+v", c.Apps["qbit"], c.Cleaner)
	}
}
```

- [ ] **Step 2: Run to see failure**

Run: `go test ./internal/config/ 2>&1 | tail -2`
Expected: `undefined: Load`.

- [ ] **Step 3: Implement**

```go
// internal/config/config.go
// Package config reads Weir's configuration from the environment (spec §4).
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type App string

const (
	Radarr App = "radarr"; Sonarr App = "sonarr"; Lidarr App = "lidarr"; Readarr App = "readarr"
	Prowlarr App = "prowlarr"; Qbit App = "qbit"; Bazarr App = "bazarr"; Jellyfin App = "jellyfin"; Jellyseerr App = "jellyseerr"
)

var AllApps = []App{Radarr, Sonarr, Lidarr, Readarr, Prowlarr, Qbit, Bazarr, Jellyfin, Jellyseerr}

type AppConfig struct{ URL, Key, User, Pass, PublicURL string }

type Cleaner struct {
	Mode                          string
	Interval, StallAfter, SlowFor, MaxETA time.Duration
	SlowBelow                     int64
	Strikes, MaxActionsPerTitle   int
}

type Config struct {
	Listen, DataDir, MediaDir, PublicURL, NtfyURL, NtfyTopic, RulesAt, LogLevel string
	Apps    map[App]AppConfig
	Cleaner Cleaner
}

func (c Config) Enabled(a App) bool { _, ok := c.Apps[a]; return ok }

func Load(getenv func(string) string) (Config, error) {
	var errs []error
	get := func(k, def string) string {
		if v := getenv("WEIR_" + k); v != "" {
			return v
		}
		return def
	}
	dur := func(k string, def time.Duration) time.Duration {
		v := get(k, "")
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("WEIR_%s: %w", k, err))
		}
		return d
	}
	num := func(k string, def int) int {
		v := get(k, "")
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("WEIR_%s: %w", k, err))
		}
		return n
	}

	c := Config{
		Listen: get("LISTEN", ":3004"), DataDir: get("DATA_DIR", "/config"), MediaDir: get("MEDIA_DIR", "/data"),
		PublicURL: get("PUBLIC_URL", "https://ratholepi.tail98e0c3.ts.net:3004"),
		NtfyURL: get("NTFY_URL", ""), NtfyTopic: get("NTFY_TOPIC", ""),
		RulesAt: get("RULES_AT", "04:10"), LogLevel: get("LOG_LEVEL", "info"),
		Apps: map[App]AppConfig{},
	}
	c.Cleaner = Cleaner{
		Mode: get("CLEANER_MODE", "report"), Interval: dur("CLEANER_INTERVAL", 5*time.Minute),
		StallAfter: dur("CLEANER_STALL_AFTER", 30*time.Minute), SlowFor: dur("CLEANER_SLOW_FOR", 20*time.Minute),
		MaxETA: dur("CLEANER_MAX_ETA", 48*time.Hour), Strikes: num("CLEANER_STRIKES", 3),
		MaxActionsPerTitle: num("CLEANER_MAX_ACTIONS_PER_TITLE", 3),
	}
	if b, err := parseBytes(get("CLEANER_SLOW_BELOW", "50KiB")); err != nil {
		errs = append(errs, fmt.Errorf("WEIR_CLEANER_SLOW_BELOW: %w", err))
	} else {
		c.Cleaner.SlowBelow = b
	}
	switch c.Cleaner.Mode {
	case "off", "report", "act":
	default:
		errs = append(errs, fmt.Errorf("WEIR_CLEANER_MODE: %q is not off, report or act", c.Cleaner.Mode))
	}
	for _, a := range AllApps {
		up := strings.ToUpper(string(a))
		url := get(up+"_URL", "")
		if url == "" {
			continue
		}
		ac := AppConfig{URL: strings.TrimRight(url, "/"), PublicURL: get(up+"_PUBLIC_URL", "")}
		if a == Qbit {
			ac.User, ac.Pass = get("QBIT_USER", ""), get("QBIT_PASS", "")
			if ac.User == "" {
				errs = append(errs, errors.New("WEIR_QBIT_URL set but WEIR_QBIT_USER missing"))
			}
			if ac.Pass == "" {
				errs = append(errs, errors.New("WEIR_QBIT_URL set but WEIR_QBIT_PASS missing"))
			}
		} else if ac.Key = get(up+"_KEY", ""); ac.Key == "" {
			errs = append(errs, fmt.Errorf("WEIR_%s_URL set but WEIR_%s_KEY missing", up, up))
		}
		c.Apps[a] = ac
	}
	if (c.NtfyURL == "") != (c.NtfyTopic == "") {
		errs = append(errs, errors.New("WEIR_NTFY_URL and WEIR_NTFY_TOPIC must be set together"))
	}
	return c, errors.Join(errs...)
}

// parseBytes accepts "50KiB", "1MiB", "2GiB", "800" (bytes).
func parseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	for suf, m := range map[string]int64{"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30} {
		if strings.HasSuffix(s, suf) {
			mult, s = m, strings.TrimSuffix(s, suf)
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}
```

Then in `cmd/weir/main.go` `run()`, replace the `listen := os.Getenv(...)` lines with:

```go
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	listen := cfg.Listen
```

and add `"github.com/riverr4t/weir/internal/config"` to the imports.

- [ ] **Step 4: Run tests**

Run: `gofmt -l . ; go test ./internal/config/ ./cmd/weir/ 2>&1 | tail -2`
Expected: no gofmt output, two `ok` lines.

- [ ] **Step 5: Commit**

```bash
git add internal/config cmd/weir/main.go
git commit -m "Config from WEIR_* env, every error at once

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: Snapshot store

**Files:**
- Create: `internal/snapshot/snapshot.go`, `internal/snapshot/snapshot_test.go`

**Interfaces:**
- Produces:
  ```go
  type Result[T any] struct { Data T; FetchedAt time.Time; Err error; Down bool }
  func (r Result[T]) Age(now time.Time) time.Duration   // 0 if never fetched
  type Cell[T any] struct{ ... }                          // one slot, safe for concurrent use
  func (c *Cell[T]) Get() Result[T]
  func (c *Cell[T]) SetOK(data T, at time.Time)          // clears Err and Down
  func (c *Cell[T]) SetErr(err error, down bool)         // keeps Data and FetchedAt
  type Store struct {
      RadarrQueue, SonarrQueue   Cell[[]arr.QueueItem]
      RadarrHealth, SonarrHealth Cell[[]arr.HealthItem]
      RadarrStatus, SonarrStatus Cell[arr.SystemStatus]
      RadarrDisk, SonarrDisk     Cell[[]arr.DiskSpace]
      RadarrCalendar             Cell[[]arr.CalendarItem]
      SonarrCalendar             Cell[[]arr.CalendarItem]
      RadarrMovies               Cell[[]arr.Movie]
      SonarrSeries               Cell[[]arr.Series]
      Qbit                       Cell[qbit.State]
  }
  ```
  The `arr.*` and `qbit.State` types are defined in Tasks 5 and 7; until then this task compiles against placeholder type aliases in `snapshot.go` that Task 5 and Task 7 replace (see their steps). Later plans add cells for the other apps.

- [ ] **Step 1: Write the failing test**

```go
// internal/snapshot/snapshot_test.go
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
```

- [ ] **Step 2: Run to see failure**

Run: `go test -race ./internal/snapshot/ 2>&1 | tail -2`
Expected: `undefined: Cell`.

- [ ] **Step 3: Implement**

```go
// internal/snapshot/snapshot.go
// Package snapshot holds the latest known state of every app (spec §5).
// Pollers write whole values; everything else reads copies.
package snapshot

import (
	"sync"
	"time"
)

type Result[T any] struct {
	Data      T
	FetchedAt time.Time // zero until the first successful poll
	Err       error     // last poll's error; Data keeps the last good value
	Down      bool      // true after 3 consecutive failures
}

func (r Result[T]) Age(now time.Time) time.Duration {
	if r.FetchedAt.IsZero() {
		return 0
	}
	return now.Sub(r.FetchedAt)
}

type Cell[T any] struct {
	mu sync.RWMutex
	r  Result[T]
}

func (c *Cell[T]) Get() Result[T] { c.mu.RLock(); defer c.mu.RUnlock(); return c.r }

func (c *Cell[T]) SetOK(data T, at time.Time) {
	c.mu.Lock()
	c.r = Result[T]{Data: data, FetchedAt: at}
	c.mu.Unlock()
}

func (c *Cell[T]) SetErr(err error, down bool) {
	c.mu.Lock()
	c.r.Err, c.r.Down = err, down
	c.mu.Unlock()
}

// Placeholder element types; Task 5 (arr) and Task 7 (qbit) replace these
// aliases with the real imported types.
type (
	arrQueueItem    = any
	arrHealthItem   = any
	arrSystemStatus = any
	arrDiskSpace    = any
	arrCalendarItem = any
	arrMovie        = any
	arrSeries       = any
	qbitState       = any
)

// Store is the whole snapshot: one Cell per (app, kind).
type Store struct {
	RadarrQueue, SonarrQueue       Cell[[]arrQueueItem]
	RadarrHealth, SonarrHealth     Cell[[]arrHealthItem]
	RadarrStatus, SonarrStatus     Cell[arrSystemStatus]
	RadarrDisk, SonarrDisk         Cell[[]arrDiskSpace]
	RadarrCalendar, SonarrCalendar Cell[[]arrCalendarItem]
	RadarrMovies                   Cell[[]arrMovie]
	SonarrSeries                   Cell[[]arrSeries]
	Qbit                           Cell[qbitState]
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/snapshot/ 2>&1 | tail -1`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/snapshot
git commit -m "Snapshot cells: last good data survives a failed poll

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: Metrics registry and the poll runner

**Files:**
- Create: `internal/metrics/metrics.go`, `internal/poll/poll.go`, `internal/poll/poll_test.go`

**Interfaces:**
- Produces:
  ```go
  // metrics
  type M struct {
      PollDuration   *prometheus.HistogramVec // app, kind
      PollErrors     *prometheus.CounterVec   // app, kind
      SnapshotAge    *prometheus.GaugeVec     // app, kind
      AppUp          *prometheus.GaugeVec     // app
      CleanerStrikes *prometheus.GaugeVec     // condition
      CleanerActions *prometheus.CounterVec   // mode, condition
      SSEClients     prometheus.Gauge
      Registry       *prometheus.Registry
  }
  func New() *M
  func (m *M) Handler() http.Handler
  // poll
  type Spec struct {
      App, Kind string
      Interval  time.Duration
      Notify    func(app string, down bool)  // may be nil; called on Down transitions only
  }
  func Run[T any](ctx context.Context, s Spec, cell *snapshot.Cell[T], m *metrics.M, fetch func(context.Context) (T, error))
  ```
  `Run` blocks until ctx is done; callers start it with `go`.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/prometheus/client_golang@latest && go mod tidy && grep -c prometheus go.mod`
Expected: `1` or more.

- [ ] **Step 2: Write metrics.go (no test; it is declarations)**

```go
// internal/metrics/metrics.go
// Package metrics owns the Prometheus registry (spec §11).
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type M struct {
	PollDuration   *prometheus.HistogramVec
	PollErrors     *prometheus.CounterVec
	SnapshotAge    *prometheus.GaugeVec
	AppUp          *prometheus.GaugeVec
	CleanerStrikes *prometheus.GaugeVec
	CleanerActions *prometheus.CounterVec
	SSEClients     prometheus.Gauge
	Registry       *prometheus.Registry
}

func New() *M {
	r := prometheus.NewRegistry()
	m := &M{
		Registry: r,
		PollDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "weir_poll_duration_seconds",
			Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"app", "kind"}),
		PollErrors:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "weir_poll_errors_total"}, []string{"app", "kind"}),
		SnapshotAge:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "weir_snapshot_age_seconds"}, []string{"app", "kind"}),
		AppUp:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "weir_app_up"}, []string{"app"}),
		CleanerStrikes: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "weir_cleaner_strikes"}, []string{"condition"}),
		CleanerActions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "weir_cleaner_actions_total"}, []string{"mode", "condition"}),
		SSEClients:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "weir_sse_clients"}),
	}
	r.MustRegister(m.PollDuration, m.PollErrors, m.SnapshotAge, m.AppUp, m.CleanerStrikes, m.CleanerActions, m.SSEClients,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *M) Handler() http.Handler { return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}) }
```

- [ ] **Step 3: Write the failing poll tests**

```go
// internal/poll/poll_test.go
package poll

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/snapshot"
)

func TestDownAfterThreeFailuresThenRecovers(t *testing.T) {
	var cell snapshot.Cell[int]
	var mu sync.Mutex
	var transitions []bool
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Run(ctx, Spec{App: "radarr", Kind: "queue", Interval: time.Millisecond,
			Notify: func(_ string, down bool) { mu.Lock(); transitions = append(transitions, down); mu.Unlock() }},
			&cell, metrics.New(), func(context.Context) (int, error) {
				calls++
				if calls <= 3 {
					return 0, errors.New("nope")
				}
				if calls == 5 {
					cancel()
				}
				return calls, nil
			})
		close(done)
	}()
	<-done
	r := cell.Get()
	if r.Down || r.Err != nil || r.Data < 4 {
		t.Fatalf("expected recovered cell, got %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(transitions) != 2 || transitions[0] != true || transitions[1] != false {
		t.Fatalf("expected [down up], got %v", transitions)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	var cell snapshot.Cell[int]
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	Run(ctx, Spec{App: "a", Kind: "k", Interval: time.Millisecond}, &cell, metrics.New(), func(context.Context) (int, error) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		cancel()
		return 7, nil
	})
	if cell.Get().Data != 7 {
		t.Fatal("poller did not survive the panic")
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	if d := backoff(time.Second, 1); d < 1600*time.Millisecond || d > 2400*time.Millisecond {
		t.Fatalf("1 failure: %v", d)
	}
	if d := backoff(time.Second, 20); d > 6*time.Minute {
		t.Fatalf("should cap near 5m, got %v", d)
	}
}
```

- [ ] **Step 4: Run to see failure**

Run: `go test ./internal/poll/ 2>&1 | tail -2`
Expected: `undefined: Run`.

- [ ] **Step 5: Implement**

```go
// internal/poll/poll.go
// Package poll runs one fetch loop per (app, kind) with backoff, jitter,
// panic recovery and Down transitions (spec §5).
package poll

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"time"

	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/snapshot"
)

const (
	downAfter  = 3
	backoffCap = 5 * time.Minute
)

type Spec struct {
	App, Kind string
	Interval  time.Duration
	Notify    func(app string, down bool)
}

// backoff doubles the base per failure, caps at 5 minutes, and adds ±20 % jitter.
func backoff(base time.Duration, failures int) time.Duration {
	d := base
	for i := 0; i < failures && d < backoffCap; i++ {
		d *= 2
	}
	if d > backoffCap {
		d = backoffCap
	}
	j := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(d) * j)
}

func Run[T any](ctx context.Context, s Spec, cell *snapshot.Cell[T], m *metrics.M, fetch func(context.Context) (T, error)) {
	log := slog.With("app", s.App, "kind", s.Kind)
	failures := 0
	for {
		start := time.Now()
		data, err := safeFetch(ctx, fetch)
		m.PollDuration.WithLabelValues(s.App, s.Kind).Observe(time.Since(start).Seconds())
		wait := s.Interval
		if err != nil {
			failures++
			m.PollErrors.WithLabelValues(s.App, s.Kind).Inc()
			down := failures >= downAfter
			wasDown := cell.Get().Down
			cell.SetErr(err, down)
			if down && !wasDown {
				m.AppUp.WithLabelValues(s.App).Set(0)
				log.Warn("app down", "err", err)
				if s.Notify != nil {
					s.Notify(s.App, true)
				}
			}
			wait = backoff(s.Interval, failures)
		} else {
			wasDown := cell.Get().Down
			failures = 0
			cell.SetOK(data, time.Now())
			m.AppUp.WithLabelValues(s.App).Set(1)
			m.SnapshotAge.WithLabelValues(s.App, s.Kind).Set(0)
			if wasDown {
				log.Info("app recovered")
				if s.Notify != nil {
					s.Notify(s.App, false)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func safeFetch[T any](ctx context.Context, fetch func(context.Context) (T, error)) (data T, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("poller panic", "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fetch(ctx)
}
```

Note on `SnapshotAge`: it is set to 0 on success here and updated by a ticker in `cmd/weir` (Task 12) that walks every cell each 15 s. Task 12 owns that loop.

- [ ] **Step 6: Run tests**

Run: `gofmt -l . ; go vet ./internal/poll/ ./internal/metrics/ && go test -race ./internal/poll/ 2>&1 | tail -1`
Expected: no gofmt output, `ok`.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/metrics internal/poll
git commit -m "Poll runner with backoff, panic recovery and down/up transitions; metrics registry

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 5: Shared arr client (Radarr/Sonarr/Lidarr/Readarr)

**Files:**
- Create: `internal/apps/arr/types.go`, `internal/apps/arr/client.go`, `internal/apps/arr/client_test.go`
- Modify: `internal/snapshot/snapshot.go` (replace the `arr*` placeholder aliases with the real types)

**Interfaces:**
- Consumes: fixtures `testdata/fixtures/{radarr,sonarr}/{queue,health,system-status,diskspace,calendar}.json` from Task 0.
- Produces:
  ```go
  type Client struct { BaseURL, Key, Version string; HTTP *http.Client } // Version "v3" or "v1"
  func New(baseURL, key, version string) *Client
  type QueueItem struct {
      ID int64 `json:"id"`; Title string `json:"title"`; Status string `json:"status"`
      TrackedDownloadStatus string `json:"trackedDownloadStatus"`; TrackedDownloadState string `json:"trackedDownloadState"`
      DownloadID string `json:"downloadId"`; Size, SizeLeft float64 `json:"size","sizeleft"`
      Protocol string `json:"protocol"`; ErrorMessage string `json:"errorMessage"`
      MovieID, SeriesID, EpisodeID, AlbumID, BookID int64  // whichever the app fills
      StatusMessages []StatusMessage `json:"statusMessages"`
  }
  type StatusMessage struct{ Title string; Messages []string }
  type HealthItem struct{ Source, Type, Message, WikiURL string }
  type SystemStatus struct{ AppName, Version, Branch string; StartTime time.Time }
  type DiskSpace struct{ Path string; FreeSpace, TotalSpace int64 }
  type CalendarItem struct{ ID int64; Title string; AirDate string; HasFile bool; Monitored bool; SeriesID int64; SeasonNumber, EpisodeNumber int; SeriesTitle string }
  type Movie struct{ ID int64; Title string; Year int; TmdbID int64; Added time.Time; HasFile, Monitored bool; SizeOnDisk int64; Tags []int64; Path string }
  type Series struct{ ID int64; Title string; Year int; TvdbID int64; Added time.Time; Ended bool; Monitored bool; Tags []int64; Path string; Statistics struct{ SizeOnDisk int64; EpisodeFileCount, EpisodeCount, TotalEpisodeCount int; PercentOfEpisodes float64 } }
  func (c *Client) Queue(ctx) ([]QueueItem, error)
  func (c *Client) Health(ctx) ([]HealthItem, error)
  func (c *Client) SystemStatus(ctx) (SystemStatus, error)
  func (c *Client) DiskSpace(ctx) ([]DiskSpace, error)
  func (c *Client) Calendar(ctx, from, to time.Time) ([]CalendarItem, error)
  func (c *Client) Movies(ctx) ([]Movie, error)     // Radarr only
  func (c *Client) Series(ctx) ([]Series, error)    // Sonarr only
  func (c *Client) DeleteQueueItem(ctx, id int64, blocklist bool) error  // removeFromClient=true&blocklist=<b>&skipRedownload=false
  func (c *Client) Command(ctx, name string, body map[string]any) error   // POST command
  func (c *Client) get(ctx, path string, out any) error   // unexported helper; every read uses it
  ```
  All JSON tags follow the arr APIs exactly (camelCase; `sizeleft` is lowercase in the queue payload).

- [ ] **Step 1: Write the failing tests**

```go
// internal/apps/arr/client_test.go
package arr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixtureServer serves testdata/fixtures/<app>/<file> for the given API paths
// and records every request; it also asserts the X-Api-Key header.
func fixtureServer(t *testing.T, app string, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", app)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		if r.Method == "DELETE" || r.Method == "POST" {
			w.WriteHeader(200)
			w.Write([]byte("{}"))
			return
		}
		f, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestRadarrReads(t *testing.T) {
	srv, seen := fixtureServer(t, "radarr", map[string]string{
		"/api/v3/queue": "queue.json", "/api/v3/health": "health.json", "/api/v3/system/status": "system-status.json",
		"/api/v3/diskspace": "diskspace.json", "/api/v3/calendar": "calendar.json", "/api/v3/movie": "movie.json",
	})
	c := New(srv.URL, "k", "v3")
	ctx := context.Background()
	q, err := c.Queue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range q {
		if it.Title == "" || it.DownloadID == "" {
			t.Fatalf("queue item missing fields: %+v", it)
		}
	}
	st, err := c.SystemStatus(ctx)
	if err != nil || st.Version == "" {
		t.Fatalf("status: %v %+v", err, st)
	}
	ds, err := c.DiskSpace(ctx)
	if err != nil || len(ds) == 0 || ds[0].TotalSpace == 0 {
		t.Fatalf("diskspace: %v %+v", err, ds)
	}
	if _, err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	if _, err := c.Calendar(ctx, from, from.AddDate(0, 0, 7)); err != nil {
		t.Fatal(err)
	}
	mv, err := c.Movies(ctx)
	if err != nil || len(mv) == 0 || mv[0].Title == "" {
		t.Fatalf("movies: %v", err)
	}
	if (*seen)[0] != "GET /api/v3/queue?page=1&pageSize=1000&includeUnknownMovieItems=true&includeUnknownSeriesItems=true&includeUnknownAuthorItems=true" {
		t.Fatalf("queue query: %s", (*seen)[0])
	}
}

func TestSonarrSeries(t *testing.T) {
	srv, _ := fixtureServer(t, "sonarr", map[string]string{"/api/v3/series": "series.json"})
	s, err := New(srv.URL, "k", "v3").Series(context.Background())
	if err != nil || len(s) == 0 || s[0].TvdbID == 0 {
		t.Fatalf("series: %v", err)
	}
}

func TestDeleteQueueItemAndCommand(t *testing.T) {
	srv, seen := fixtureServer(t, "radarr", nil)
	c := New(srv.URL, "k", "v3")
	if err := c.DeleteQueueItem(context.Background(), 42, true); err != nil {
		t.Fatal(err)
	}
	if err := c.Command(context.Background(), "MissingMoviesSearch", nil); err != nil {
		t.Fatal(err)
	}
	want := "DELETE /api/v3/queue/42?blocklist=true&removeFromClient=true&skipRedownload=false"
	if (*seen)[0] != want || (*seen)[1] != "POST /api/v3/command" {
		t.Fatalf("got %v", *seen)
	}
}

func TestBadKeyIsAnError(t *testing.T) {
	srv, _ := fixtureServer(t, "radarr", map[string]string{"/api/v3/health": "health.json"})
	if _, err := New(srv.URL, "wrong", "v3").Health(context.Background()); err == nil {
		t.Fatal("expected 401 error")
	}
}
```

- [ ] **Step 2: Run to see failure**

Run: `go test ./internal/apps/arr/ 2>&1 | tail -2`
Expected: `undefined: New`.

- [ ] **Step 3: Write types.go**

```go
// internal/apps/arr/types.go
package arr

import "time"

type StatusMessage struct {
	Title    string   `json:"title"`
	Messages []string `json:"messages"`
}

type QueueItem struct {
	ID                    int64           `json:"id"`
	Title                 string          `json:"title"`
	Status                string          `json:"status"`
	TrackedDownloadStatus string          `json:"trackedDownloadStatus"`
	TrackedDownloadState  string          `json:"trackedDownloadState"`
	DownloadID            string          `json:"downloadId"`
	Size                  float64         `json:"size"`
	SizeLeft              float64         `json:"sizeleft"`
	Protocol              string          `json:"protocol"`
	ErrorMessage          string          `json:"errorMessage"`
	MovieID               int64           `json:"movieId"`
	SeriesID              int64           `json:"seriesId"`
	EpisodeID             int64           `json:"episodeId"`
	AlbumID               int64           `json:"albumId"`
	BookID                int64           `json:"bookId"`
	StatusMessages        []StatusMessage `json:"statusMessages"`
}

// Completed reports whether every byte has arrived; such items are never
// touched by the cleaner (spec §7).
func (q QueueItem) Completed() bool { return q.Size > 0 && q.SizeLeft <= 0 }

// TitleKey identifies the media item the download is for, for the per-title cap.
func (q QueueItem) TitleKey() string {
	switch {
	case q.EpisodeID != 0:
		return "episode:" + itoa(q.EpisodeID)
	case q.MovieID != 0:
		return "movie:" + itoa(q.MovieID)
	case q.AlbumID != 0:
		return "album:" + itoa(q.AlbumID)
	case q.BookID != 0:
		return "book:" + itoa(q.BookID)
	}
	return "download:" + q.DownloadID
}

type HealthItem struct {
	Source  string `json:"source"`
	Type    string `json:"type"`
	Message string `json:"message"`
	WikiURL string `json:"wikiUrl"`
}

type SystemStatus struct {
	AppName   string    `json:"appName"`
	Version   string    `json:"version"`
	Branch    string    `json:"branch"`
	StartTime time.Time `json:"startTime"`
}

type DiskSpace struct {
	Path       string `json:"path"`
	FreeSpace  int64  `json:"freeSpace"`
	TotalSpace int64  `json:"totalSpace"`
}

type CalendarItem struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	AirDate       string `json:"airDate"`
	HasFile       bool   `json:"hasFile"`
	Monitored     bool   `json:"monitored"`
	SeriesID      int64  `json:"seriesId"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	SeriesTitle   string `json:"-"`
}

type Movie struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	Year       int       `json:"year"`
	TmdbID     int64     `json:"tmdbId"`
	Added      time.Time `json:"added"`
	HasFile    bool      `json:"hasFile"`
	Monitored  bool      `json:"monitored"`
	SizeOnDisk int64     `json:"sizeOnDisk"`
	Tags       []int64   `json:"tags"`
	Path       string    `json:"path"`
}

type Series struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	Year       int       `json:"year"`
	TvdbID     int64     `json:"tvdbId"`
	Added      time.Time `json:"added"`
	Ended      bool      `json:"ended"`
	Monitored  bool      `json:"monitored"`
	Tags       []int64   `json:"tags"`
	Path       string    `json:"path"`
	Statistics struct {
		SizeOnDisk        int64   `json:"sizeOnDisk"`
		EpisodeFileCount  int     `json:"episodeFileCount"`
		EpisodeCount      int     `json:"episodeCount"`
		TotalEpisodeCount int     `json:"totalEpisodeCount"`
		PercentOfEpisodes float64 `json:"percentOfEpisodes"`
	} `json:"statistics"`
}

func itoa(n int64) string { return strconvItoa(n) }
```

and in `client.go` define `func strconvItoa(n int64) string { return strconv.FormatInt(n, 10) }` (kept in one place so `types.go` has no imports beyond `time`).

- [ ] **Step 4: Write client.go**

```go
// internal/apps/arr/client.go
// Package arr is the shared client for Radarr, Sonarr, Lidarr and Readarr
// (Bookshelf). They share the API shape; only the version segment and the
// library endpoints differ.
package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL, Key, Version string
	HTTP                  *http.Client
}

func New(baseURL, key, version string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Key: key, Version: version,
		HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func strconvItoa(n int64) string { return strconv.FormatInt(n, 10) }

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/"+c.Version+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.Key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) Queue(ctx context.Context) ([]QueueItem, error) {
	var page struct {
		Records []QueueItem `json:"records"`
	}
	err := c.get(ctx, "/queue?page=1&pageSize=1000&includeUnknownMovieItems=true&includeUnknownSeriesItems=true&includeUnknownAuthorItems=true", &page)
	return page.Records, err
}

func (c *Client) Health(ctx context.Context) ([]HealthItem, error) {
	var out []HealthItem
	return out, c.get(ctx, "/health", &out)
}

func (c *Client) SystemStatus(ctx context.Context) (SystemStatus, error) {
	var out SystemStatus
	return out, c.get(ctx, "/system/status", &out)
}

func (c *Client) DiskSpace(ctx context.Context) ([]DiskSpace, error) {
	var out []DiskSpace
	return out, c.get(ctx, "/diskspace", &out)
}

func (c *Client) Calendar(ctx context.Context, from, to time.Time) ([]CalendarItem, error) {
	var out []CalendarItem
	q := url.Values{"start": {from.UTC().Format("2006-01-02")}, "end": {to.UTC().Format("2006-01-02")}}
	return out, c.get(ctx, "/calendar?"+q.Encode(), &out)
}

func (c *Client) Movies(ctx context.Context) ([]Movie, error) {
	var out []Movie
	return out, c.get(ctx, "/movie", &out)
}

func (c *Client) Series(ctx context.Context) ([]Series, error) {
	var out []Series
	return out, c.get(ctx, "/series", &out)
}

// DeleteQueueItem removes a download that has NOT completed: the arr drops
// it from the client with its partial files, optionally blocklists the
// release, and (skipRedownload=false) searches again. This is the only
// removal Weir ever performs (spec §7). Callers must check
// QueueItem.Completed() first; the cleaner and the blocklist route do.
func (c *Client) DeleteQueueItem(ctx context.Context, id int64, blocklist bool) error {
	q := url.Values{"removeFromClient": {"true"}, "blocklist": {strconv.FormatBool(blocklist)}, "skipRedownload": {"false"}}
	return c.do(ctx, http.MethodDelete, "/queue/"+strconv.FormatInt(id, 10)+"?"+q.Encode(), nil, nil)
}

func (c *Client) Command(ctx context.Context, name string, body map[string]any) error {
	if body == nil {
		body = map[string]any{}
	}
	body["name"] = name
	b, _ := json.Marshal(body)
	return c.do(ctx, http.MethodPost, "/command", strings.NewReader(string(b)), nil)
}
```

- [ ] **Step 5: Replace the snapshot placeholders**

In `internal/snapshot/snapshot.go`, delete the `arr*` alias lines from the placeholder `type (...)` block (keep `qbitState = any` until Task 7), add `"github.com/riverr4t/weir/internal/apps/arr"` to the imports, and change the `Store` fields to the real types:

```go
type Store struct {
	RadarrQueue, SonarrQueue       Cell[[]arr.QueueItem]
	RadarrHealth, SonarrHealth     Cell[[]arr.HealthItem]
	RadarrStatus, SonarrStatus     Cell[arr.SystemStatus]
	RadarrDisk, SonarrDisk         Cell[[]arr.DiskSpace]
	RadarrCalendar, SonarrCalendar Cell[[]arr.CalendarItem]
	RadarrMovies                   Cell[[]arr.Movie]
	SonarrSeries                   Cell[[]arr.Series]
	Qbit                           Cell[qbitState]
}
```

- [ ] **Step 6: Run tests**

Run: `gofmt -l . ; go vet ./internal/... && go test ./internal/apps/arr/ ./internal/snapshot/ 2>&1 | tail -2`
Expected: no gofmt output, two `ok`.

- [ ] **Step 7: Commit**

```bash
git add internal/apps/arr internal/snapshot
git commit -m "Shared arr client: queue, health, status, disk, calendar, movies, series, queue delete, command

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 6: Radarr and Sonarr pollers

**Files:**
- Create: `internal/apps/radarr/radarr.go`, `internal/apps/sonarr/sonarr.go`, `internal/apps/radarr/radarr_test.go`

**Interfaces:**
- Consumes: `arr.Client`, `snapshot.Store`, `poll.Run`, `metrics.M`, `config.AppConfig`.
- Produces:
  ```go
  // radarr
  func Start(ctx context.Context, cfg config.AppConfig, s *snapshot.Store, m *metrics.M, notify func(string, bool)) *arr.Client
  // sonarr
  func Start(ctx context.Context, cfg config.AppConfig, s *snapshot.Store, m *metrics.M, notify func(string, bool)) *arr.Client
  ```
  Each `Start` builds the client, launches the pollers from spec §5 (queue 15 s; health, disk, calendar 5 min; library 15 min) with `go poll.Run(...)`, and returns the client for the write handlers.

- [ ] **Step 1: Write the failing test**

```go
// internal/apps/radarr/radarr_test.go
package radarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/snapshot"
)

func TestStartFillsSnapshot(t *testing.T) {
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", "radarr")
	files := map[string]string{"/api/v3/queue": "queue.json", "/api/v3/health": "health.json",
		"/api/v3/system/status": "system-status.json", "/api/v3/diskspace": "diskspace.json",
		"/api/v3/calendar": "calendar.json", "/api/v3/movie": "movie.json"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join(root, files[r.URL.Path]))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var s snapshot.Store
	Start(ctx, config.AppConfig{URL: srv.URL, Key: "k"}, &s, metrics.New(), nil)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !s.RadarrQueue.Get().FetchedAt.IsZero() && !s.RadarrMovies.Get().FetchedAt.IsZero() &&
			!s.RadarrStatus.Get().FetchedAt.IsZero() && !s.RadarrDisk.Get().FetchedAt.IsZero() &&
			!s.RadarrCalendar.Get().FetchedAt.IsZero() && !s.RadarrHealth.Get().FetchedAt.IsZero() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("snapshot not filled within 3s")
}
```

- [ ] **Step 2: Run to see failure**

Run: `go test ./internal/apps/radarr/ 2>&1 | tail -2`
Expected: `undefined: Start`.

- [ ] **Step 3: Implement radarr.go**

```go
// internal/apps/radarr/radarr.go
// Package radarr starts the Radarr pollers on top of the shared arr client.
package radarr

import (
	"context"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/poll"
	"github.com/riverr4t/weir/internal/snapshot"
)

const app = "radarr"

func Start(ctx context.Context, cfg config.AppConfig, s *snapshot.Store, m *metrics.M, notify func(string, bool)) *arr.Client {
	c := arr.New(cfg.URL, cfg.Key, "v3")
	spec := func(kind string, every time.Duration) poll.Spec {
		return poll.Spec{App: app, Kind: kind, Interval: every, Notify: notify}
	}
	go poll.Run(ctx, spec("queue", 15*time.Second), &s.RadarrQueue, m, c.Queue)
	go poll.Run(ctx, spec("health", 5*time.Minute), &s.RadarrHealth, m, c.Health)
	go poll.Run(ctx, spec("status", 5*time.Minute), &s.RadarrStatus, m, c.SystemStatus)
	go poll.Run(ctx, spec("disk", 5*time.Minute), &s.RadarrDisk, m, c.DiskSpace)
	go poll.Run(ctx, spec("calendar", 5*time.Minute), &s.RadarrCalendar, m, func(ctx context.Context) ([]arr.CalendarItem, error) {
		now := time.Now()
		return c.Calendar(ctx, now, now.AddDate(0, 0, 7))
	})
	go poll.Run(ctx, spec("library", 15*time.Minute), &s.RadarrMovies, m, c.Movies)
	return c
}
```

- [ ] **Step 4: Implement sonarr.go**

Identical shape with `app = "sonarr"`, cells `SonarrQueue`, `SonarrHealth`, `SonarrStatus`, `SonarrDisk`, `SonarrCalendar`, and the library poller:

```go
	go poll.Run(ctx, spec("library", 15*time.Minute), &s.SonarrSeries, m, c.Series)
```

Sonarr's calendar items carry `seriesId` but not the series title; the downloads/overview pages resolve titles from `SonarrSeries` (Task 11 does this in the template helper), so nothing extra is fetched here.

- [ ] **Step 5: Run tests**

Run: `gofmt -l . ; go vet ./internal/apps/... && go test ./internal/apps/radarr/ 2>&1 | tail -1`
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/apps/radarr internal/apps/sonarr
git commit -m "Radarr and Sonarr pollers

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 7: qBittorrent client with delta sync

**Files:**
- Create: `internal/apps/qbit/client.go`, `internal/apps/qbit/client_test.go`
- Modify: `internal/snapshot/snapshot.go` (replace `qbitState = any` with `qbit.State`)

**Interfaces:**
- Consumes: `testdata/fixtures/qbit/{maindata-full,maindata-delta,trackers}.json`.
- Produces:
  ```go
  type Torrent struct {
      Hash, Name, State string; Progress float64; DLSpeed, UPSpeed int64 // bytes/s
      ETA int64 /* s, 8640000 = unknown */; NumSeeds, NumLeechs int; Downloaded, Size int64
      AddedOn int64; Tracker string; Category string
  }
  type State struct {
      DLSpeed, UPSpeed int64        // server_state.dl_info_speed / up_info_speed
      Torrents map[string]Torrent   // keyed by hash
      Version string
  }
  type Tracker struct{ URL string; Status int; Msg string } // Status: 0 disabled,1 not contacted,2 working,3 updating,4 not working
  type Client struct{ ... }
  func New(baseURL, user, pass string) *Client
  func (c *Client) Sync(ctx) (State, error)      // delta-merges into c.state and returns a deep copy
  func (c *Client) Trackers(ctx, hash string) ([]Tracker, error)
  func (c *Client) Stop(ctx, hash string) error   // POST torrents/stop
  func (c *Client) Start(ctx, hash string) error  // POST torrents/start
  func (t Torrent) Stalled() bool                 // State == "stalledDL"
  ```
  `Sync` logs in on the first call and on any 403, then retries once.

- [ ] **Step 1: Write the failing tests**

```go
// internal/apps/qbit/client_test.go
package qbit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func server(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", "qbit")
	var seen []string
	loggedIn := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.URL.Path == "/api/v2/auth/login":
			r.ParseForm()
			if r.Form.Get("username") != "u" || r.Form.Get("password") != "p" {
				w.Write([]byte("Fails."))
				return
			}
			loggedIn = true
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s"})
			w.Write([]byte("Ok."))
		case !loggedIn || cookie(r) != "s":
			w.WriteHeader(403)
		case r.URL.Path == "/api/v2/sync/maindata":
			f := "maindata-full.json"
			if r.URL.Query().Get("rid") != "0" {
				f = "maindata-delta.json"
			}
			b, _ := os.ReadFile(filepath.Join(root, f))
			w.Write(b)
		case r.URL.Path == "/api/v2/torrents/trackers":
			b, _ := os.ReadFile(filepath.Join(root, "trackers.json"))
			w.Write(b)
		case r.URL.Path == "/api/v2/app/version":
			b, _ := os.ReadFile(filepath.Join(root, "version.txt"))
			w.Write(b)
		case strings.HasPrefix(r.URL.Path, "/api/v2/torrents/"):
			w.Write([]byte(""))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func cookie(r *http.Request) string {
	if c, err := r.Cookie("SID"); err == nil {
		return c.Value
	}
	return ""
}

func TestSyncLogsInThenDeltas(t *testing.T) {
	srv, seen := server(t)
	c := New(srv.URL, "u", "p")
	ctx := context.Background()
	s1, err := c.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Version == "" {
		t.Fatal("version not read")
	}
	s2, err := c.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// after a delta, torrents from the full sync survive unless removed
	if len(s2.Torrents) == 0 && len(s1.Torrents) != 0 {
		t.Fatal("delta wiped torrents")
	}
	joined := strings.Join(*seen, "\n")
	if !strings.Contains(joined, "POST /api/v2/auth/login") || !strings.Contains(joined, "sync/maindata?rid=0") {
		t.Fatalf("unexpected request sequence:\n%s", joined)
	}
	if strings.Count(joined, "auth/login") != 1 {
		t.Fatal("should log in once")
	}
}

func TestDeepCopy(t *testing.T) {
	srv, _ := server(t)
	c := New(srv.URL, "u", "p")
	s, _ := c.Sync(context.Background())
	for h := range s.Torrents {
		delete(s.Torrents, h)
	}
	again, _ := c.Sync(context.Background())
	if len(again.Torrents) == 0 && len(c.state.Torrents) != 0 {
		t.Fatal("caller mutation leaked into client state")
	}
}

func TestStopStartAndTrackers(t *testing.T) {
	srv, seen := server(t)
	c := New(srv.URL, "u", "p")
	ctx := context.Background()
	if err := c.Stop(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Trackers(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	j := strings.Join(*seen, "\n")
	for _, want := range []string{"POST /api/v2/torrents/stop", "POST /api/v2/torrents/start", "GET /api/v2/torrents/trackers?hash=abc"} {
		if !strings.Contains(j, want) {
			t.Fatalf("missing %s in\n%s", want, j)
		}
	}
}

func TestBadPassword(t *testing.T) {
	srv, _ := server(t)
	if _, err := New(srv.URL, "u", "wrong").Sync(context.Background()); err == nil {
		t.Fatal("expected login failure")
	}
}
```

- [ ] **Step 2: Run to see failure**

Run: `go test ./internal/apps/qbit/ 2>&1 | tail -2`
Expected: `undefined: New`.

- [ ] **Step 3: Implement**

```go
// internal/apps/qbit/client.go
// Package qbit talks to qBittorrent's WebUI API v2 using sync/maindata
// deltas so a 2-second poll costs almost nothing (spec §5).
package qbit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Torrent struct {
	Hash       string  `json:"-"`
	Name       string  `json:"name"`
	State      string  `json:"state"`
	Progress   float64 `json:"progress"`
	DLSpeed    int64   `json:"dlspeed"`
	UPSpeed    int64   `json:"upspeed"`
	ETA        int64   `json:"eta"`
	NumSeeds   int     `json:"num_seeds"`
	NumLeechs  int     `json:"num_leechs"`
	Downloaded int64   `json:"downloaded"`
	Size       int64   `json:"size"`
	AddedOn    int64   `json:"added_on"`
	Tracker    string  `json:"tracker"`
	Category   string  `json:"category"`
}

func (t Torrent) Stalled() bool { return t.State == "stalledDL" }

type State struct {
	DLSpeed, UPSpeed int64
	Torrents         map[string]Torrent
	Version          string
}

type Tracker struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	Msg    string `json:"msg"`
}

type Client struct {
	base, user, pass string
	http             *http.Client
	mu               sync.Mutex
	rid              int64
	state            State
	loggedIn         bool
}

func New(baseURL, user, pass string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{base: strings.TrimRight(baseURL, "/") + "/api/v2", user: user, pass: pass,
		http: &http.Client{Timeout: 15 * time.Second, Jar: jar}, state: State{Torrents: map[string]Torrent{}}}
}

var errForbidden = errors.New("qbit: 403")

func (c *Client) login(ctx context.Context) error {
	form := url.Values{"username": {c.user}, "password": {c.pass}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "Ok." {
		return fmt.Errorf("qbit login: %s %q", resp.Status, strings.TrimSpace(string(b)))
	}
	c.loggedIn = true
	return nil
}

// call performs one request, logging in first if needed and once more on a 403.
func (c *Client) call(ctx context.Context, method, path string, form url.Values, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		if !c.loggedIn {
			if err := c.login(ctx); err != nil {
				return err
			}
		}
		err := c.once(ctx, method, path, form, out)
		if errors.Is(err, errForbidden) {
			c.loggedIn = false
			continue
		}
		return err
	}
	return errForbidden
}

func (c *Client) once(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	u := c.base + path
	if method == http.MethodGet && form != nil {
		u += "?" + form.Encode()
	} else if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 {
		return errForbidden
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("qbit %s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	if s, ok := out.(*string); ok {
		b, err := io.ReadAll(resp.Body)
		*s = strings.TrimSpace(string(b))
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type mainData struct {
	Rid             int64                      `json:"rid"`
	FullUpdate      bool                       `json:"full_update"`
	Torrents        map[string]json.RawMessage `json:"torrents"`
	TorrentsRemoved []string                   `json:"torrents_removed"`
	ServerState     struct {
		DL int64 `json:"dl_info_speed"`
		UP int64 `json:"up_info_speed"`
	} `json:"server_state"`
}

// Sync applies the next delta and returns a deep copy of the merged state.
func (c *Client) Sync(ctx context.Context) (State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Version == "" {
		var v string
		if err := c.call(ctx, http.MethodGet, "/app/version", nil, &v); err != nil {
			return State{}, err
		}
		c.state.Version = v
	}
	var md mainData
	if err := c.call(ctx, http.MethodGet, "/sync/maindata", url.Values{"rid": {strconv.FormatInt(c.rid, 10)}}, &md); err != nil {
		return State{}, err
	}
	if md.FullUpdate {
		c.state.Torrents = map[string]Torrent{}
	}
	for hash, raw := range md.Torrents {
		t := c.state.Torrents[hash] // partial updates only carry changed fields; start from the old value
		if err := json.Unmarshal(raw, &t); err != nil {
			return State{}, fmt.Errorf("torrent %s: %w", hash, err)
		}
		t.Hash = hash
		c.state.Torrents[hash] = t
	}
	for _, hash := range md.TorrentsRemoved {
		delete(c.state.Torrents, hash)
	}
	if md.ServerState.DL != 0 || md.ServerState.UP != 0 || md.FullUpdate {
		c.state.DLSpeed, c.state.UPSpeed = md.ServerState.DL, md.ServerState.UP
	}
	c.rid = md.Rid
	out := State{DLSpeed: c.state.DLSpeed, UPSpeed: c.state.UPSpeed, Version: c.state.Version,
		Torrents: make(map[string]Torrent, len(c.state.Torrents))}
	for h, t := range c.state.Torrents {
		out.Torrents[h] = t
	}
	return out, nil
}

func (c *Client) Trackers(ctx context.Context, hash string) ([]Tracker, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Tracker
	return out, c.call(ctx, http.MethodGet, "/torrents/trackers", url.Values{"hash": {hash}}, &out)
}

func (c *Client) Stop(ctx context.Context, hash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call(ctx, http.MethodPost, "/torrents/stop", url.Values{"hashes": {hash}}, nil)
}

func (c *Client) Start(ctx context.Context, hash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call(ctx, http.MethodPost, "/torrents/start", url.Values{"hashes": {hash}}, nil)
}
```

Note on `server_state` speeds: a delta omits `server_state` keys that did not change, so both decode as 0 when unchanged; the guard above keeps the previous value in that case. A genuine drop to 0/0 is picked up on the next full update or when either value next changes, which is within a few seconds in practice.

- [ ] **Step 4: Replace the snapshot placeholder**

In `internal/snapshot/snapshot.go`, remove the placeholder `type (...)` block entirely, import `"github.com/riverr4t/weir/internal/apps/qbit"`, and set `Qbit Cell[qbit.State]`.

- [ ] **Step 5: Run tests**

Run: `gofmt -l . ; go vet ./internal/... && go test ./internal/apps/qbit/ ./internal/snapshot/ 2>&1 | tail -2`
Expected: two `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/apps/qbit internal/snapshot
git commit -m "qBittorrent client: cookie login, maindata deltas, trackers, stop/start

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 8: SQLite store: strikes and the action log

**Files:**
- Create: `internal/store/store.go`, `internal/store/migrations/0001_init.sql`, `internal/store/store_test.go`

**Interfaces:**
- Produces:
  ```go
  type Store struct{ db *sql.DB }
  func Open(path string) (*Store, error)        // creates dir if needed, WAL, busy_timeout 5000, runs migrations
  func (s *Store) Close() error
  type Strike struct{ DownloadID, App, Title, Condition string; Count int; FirstSeen, LastSeen time.Time }
  func (s *Store) AddStrike(ctx, downloadID, app, title, condition string, now time.Time) (count int, err error)
  func (s *Store) ClearStrikes(ctx, downloadID string, now time.Time) error
  func (s *Store) Strikes(ctx) ([]Strike, error)                  // uncleared only
  type Action struct{ ID int64; At time.Time; Kind, App, Subject, Detail, Actor, Outcome, Error string }
  func (s *Store) LogAction(ctx, a Action) (int64, error)
  func (s *Store) Actions(ctx, limit int) ([]Action, error)      // newest first
  func (s *Store) ActionsForTitleSince(ctx, titleKey string, since time.Time) (int, error) // kind LIKE 'cleaner.remove%' AND json detail titleKey matches
  func (s *Store) Prune(ctx, before time.Time) error             // actions older than before
  ```

- [ ] **Step 1: Add the dependency**

Run: `go get modernc.org/sqlite@latest && go mod tidy && grep -c 'modernc.org/sqlite' go.mod`
Expected: `1`.

- [ ] **Step 2: Write the migration**

```sql
-- internal/store/migrations/0001_init.sql
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);

CREATE TABLE strikes (
  download_id TEXT NOT NULL,
  app         TEXT NOT NULL,
  title       TEXT NOT NULL,
  condition   TEXT NOT NULL,
  count       INTEGER NOT NULL DEFAULT 0,
  first_seen  TEXT NOT NULL,
  last_seen   TEXT NOT NULL,
  cleared_at  TEXT,
  PRIMARY KEY (download_id, condition)
);

CREATE TABLE actions (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  at      TEXT NOT NULL,
  kind    TEXT NOT NULL,
  app     TEXT NOT NULL,
  subject TEXT NOT NULL,
  detail  TEXT NOT NULL DEFAULT '{}',
  actor   TEXT NOT NULL DEFAULT 'weir',
  outcome TEXT NOT NULL,
  error   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX actions_at ON actions(at);
CREATE INDEX actions_kind_at ON actions(kind, at);
```

- [ ] **Step 3: Write the failing tests**

```go
// internal/store/store_test.go
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "weir.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "weir.db")
	for i := 0; i < 2; i++ {
		s, err := Open(p)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		s.Close()
	}
}

func TestStrikesLifecycle(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC)
	for i := 1; i <= 3; i++ {
		n, err := s.AddStrike(ctx, "hash1", "radarr", "Film", "stalled", t0.Add(time.Duration(i)*time.Minute))
		if err != nil || n != i {
			t.Fatalf("strike %d: n=%d err=%v", i, n, err)
		}
	}
	if n, _ := s.AddStrike(ctx, "hash1", "radarr", "Film", "slow", t0); n != 1 {
		t.Fatal("conditions are independent")
	}
	all, _ := s.Strikes(ctx)
	if len(all) != 2 {
		t.Fatalf("want 2 strike rows, got %d", len(all))
	}
	if err := s.ClearStrikes(ctx, "hash1", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if all, _ = s.Strikes(ctx); len(all) != 0 {
		t.Fatal("cleared strikes must not be listed")
	}
	if n, _ := s.AddStrike(ctx, "hash1", "radarr", "Film", "stalled", t0.Add(2*time.Hour)); n != 1 {
		t.Fatalf("after clearing, counting restarts at 1, got %d", n)
	}
}

func TestActionsAndTitleCap(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		_, err := s.LogAction(ctx, Action{At: t0.Add(time.Duration(i) * time.Hour), Kind: "cleaner.remove", App: "sonarr",
			Subject: "Ep", Detail: `{"titleKey":"episode:9","condition":"stalled"}`, Actor: "weir", Outcome: "ok"})
		if err != nil {
			t.Fatal(err)
		}
	}
	s.LogAction(ctx, Action{At: t0, Kind: "cleaner.report", App: "sonarr", Subject: "Ep", Detail: `{"titleKey":"episode:9"}`, Outcome: "ok"})
	n, err := s.ActionsForTitleSince(ctx, "episode:9", t0.Add(30*time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("want 2 removes since t0+30m, got %d %v", n, err)
	}
	acts, _ := s.Actions(ctx, 10)
	if len(acts) != 4 || acts[0].At.Before(acts[1].At) {
		t.Fatalf("newest first, got %+v", acts)
	}
	if err := s.Prune(ctx, t0.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if acts, _ = s.Actions(ctx, 10); len(acts) != 2 {
		t.Fatalf("prune left %d", len(acts))
	}
}
```

- [ ] **Step 4: Run to see failure**

Run: `go test ./internal/store/ 2>&1 | tail -2`
Expected: `undefined: Open`.

- [ ] **Step 5: Implement**

```go
// internal/store/store.go
// Package store is Weir's durable state: SQLite, one file, WAL (spec §6).
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

const timeLayout = time.RFC3339Nano

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite; serialises writers, and modernc is happiest this way
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&current)
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for i, name := range names {
		v := i + 1
		if v <= current {
			continue
		}
		sqlText, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(sqlText)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, v); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

type Strike struct {
	DownloadID, App, Title, Condition string
	Count                             int
	FirstSeen, LastSeen               time.Time
}

func (s *Store) AddStrike(ctx context.Context, downloadID, app, title, condition string, now time.Time) (int, error) {
	ts := now.UTC().Format(timeLayout)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO strikes(download_id, app, title, condition, count, first_seen, last_seen, cleared_at)
		VALUES (?,?,?,?,1,?,?,NULL)
		ON CONFLICT(download_id, condition) DO UPDATE SET
		  count = CASE WHEN cleared_at IS NULL THEN count + 1 ELSE 1 END,
		  first_seen = CASE WHEN cleared_at IS NULL THEN first_seen ELSE excluded.first_seen END,
		  last_seen = excluded.last_seen, cleared_at = NULL, title = excluded.title, app = excluded.app`,
		downloadID, app, title, condition, ts, ts)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT count FROM strikes WHERE download_id=? AND condition=?`, downloadID, condition).Scan(&n)
	return n, err
}

func (s *Store) ClearStrikes(ctx context.Context, downloadID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE strikes SET cleared_at=? WHERE download_id=? AND cleared_at IS NULL`,
		now.UTC().Format(timeLayout), downloadID)
	return err
}

func (s *Store) Strikes(ctx context.Context) ([]Strike, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT download_id, app, title, condition, count, first_seen, last_seen
		FROM strikes WHERE cleared_at IS NULL ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Strike
	for rows.Next() {
		var st Strike
		var f, l string
		if err := rows.Scan(&st.DownloadID, &st.App, &st.Title, &st.Condition, &st.Count, &f, &l); err != nil {
			return nil, err
		}
		st.FirstSeen, _ = time.Parse(timeLayout, f)
		st.LastSeen, _ = time.Parse(timeLayout, l)
		out = append(out, st)
	}
	return out, rows.Err()
}

type Action struct {
	ID                                             int64
	At                                             time.Time
	Kind, App, Subject, Detail, Actor, Outcome, Error string
}

func (s *Store) LogAction(ctx context.Context, a Action) (int64, error) {
	if a.At.IsZero() {
		a.At = time.Now()
	}
	if a.Actor == "" {
		a.Actor = "weir"
	}
	if a.Detail == "" {
		a.Detail = "{}"
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO actions(at, kind, app, subject, detail, actor, outcome, error) VALUES (?,?,?,?,?,?,?,?)`,
		a.At.UTC().Format(timeLayout), a.Kind, a.App, a.Subject, a.Detail, a.Actor, a.Outcome, a.Error)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) Actions(ctx context.Context, limit int) ([]Action, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, kind, app, subject, detail, actor, outcome, error
		FROM actions ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Action
	for rows.Next() {
		var a Action
		var at string
		if err := rows.Scan(&a.ID, &at, &a.Kind, &a.App, &a.Subject, &a.Detail, &a.Actor, &a.Outcome, &a.Error); err != nil {
			return nil, err
		}
		a.At, _ = time.Parse(timeLayout, at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActionsForTitleSince counts real removals for one title key (spec §7 cap).
func (s *Store) ActionsForTitleSince(ctx context.Context, titleKey string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions
		WHERE kind = 'cleaner.remove' AND at >= ? AND json_extract(detail, '$.titleKey') = ?`,
		since.UTC().Format(timeLayout), titleKey).Scan(&n)
	return n, err
}

func (s *Store) Prune(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM actions WHERE at < ?`, before.UTC().Format(timeLayout))
	return err
}
```

- [ ] **Step 6: Run tests**

Run: `gofmt -l . ; go vet ./internal/store/ && go test ./internal/store/ 2>&1 | tail -1`
Expected: `ok`.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "SQLite store: migrations, strikes, action log

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 9: ntfy publisher and the down/up coalescer

**Files:**
- Create: `internal/apps/ntfy/ntfy.go`, `internal/apps/ntfy/ntfy_test.go`

**Interfaces:**
- Produces:
  ```go
  type Publisher struct{ URL, Topic string; HTTP *http.Client }   // nil-safe: a nil *Publisher drops messages
  func New(url, topic string) *Publisher                          // returns nil when url == ""
  type Message struct{ Title, Body, Click string; Priority int; Tags []string }
  func (p *Publisher) Publish(ctx, m Message) error
  type Coalescer struct{ ... }
  func NewCoalescer(p *Publisher, window time.Duration, publicURL string) *Coalescer
  func (c *Coalescer) Transition(app string, down bool)   // safe from any goroutine; matches poll.Spec.Notify
  func (c *Coalescer) Flush(ctx)                          // sends the pending batch now (used by tests and shutdown)
  ```

- [ ] **Step 1: Write the failing tests**

```go
// internal/apps/ntfy/ntfy_test.go
package ntfy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func capture(t *testing.T) (*httptest.Server, *[]string, *[]http.Header) {
	t.Helper()
	var bodies []string
	var headers []http.Header
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.URL.Path+" "+string(b))
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies, &headers
}

func TestPublishSetsHeaders(t *testing.T) {
	srv, bodies, headers := capture(t)
	p := New(srv.URL, "weir")
	err := p.Publish(context.Background(), Message{Title: "T", Body: "B", Click: "https://x", Priority: 4, Tags: []string{"warning"}})
	if err != nil {
		t.Fatal(err)
	}
	if (*bodies)[0] != "/weir B" {
		t.Fatalf("body: %q", (*bodies)[0])
	}
	h := (*headers)[0]
	if h.Get("Title") != "T" || h.Get("Priority") != "4" || h.Get("Click") != "https://x" || h.Get("Tags") != "warning" {
		t.Fatalf("headers: %v", h)
	}
}

func TestNilPublisherIsSilent(t *testing.T) {
	var p *Publisher = New("", "")
	if p != nil {
		t.Fatal("empty url must yield nil")
	}
	if err := p.Publish(context.Background(), Message{Body: "x"}); err != nil {
		t.Fatal("nil publisher must not error")
	}
}

func TestCoalescerBatchesTransitions(t *testing.T) {
	srv, bodies, _ := capture(t)
	c := NewCoalescer(New(srv.URL, "weir"), 50*time.Millisecond, "https://weir")
	for _, a := range []string{"radarr", "sonarr", "qbit"} {
		c.Transition(a, true)
	}
	c.Transition("radarr", false)
	time.Sleep(120 * time.Millisecond)
	if len(*bodies) != 1 {
		t.Fatalf("want one batched push, got %d: %v", len(*bodies), *bodies)
	}
	b := (*bodies)[0]
	for _, want := range []string{"down: sonarr, qbit", "up: radarr"} {
		if !strings.Contains(b, want) {
			t.Fatalf("missing %q in %q", want, b)
		}
	}
}

func TestCoalescerNilPublisher(t *testing.T) {
	c := NewCoalescer(nil, time.Millisecond, "")
	c.Transition("radarr", true)
	c.Flush(context.Background()) // must not panic
}
```

- [ ] **Step 2: Run to see failure**

Run: `go test ./internal/apps/ntfy/ 2>&1 | tail -2`
Expected: `undefined: New`.

- [ ] **Step 3: Implement**

```go
// internal/apps/ntfy/ntfy.go
// Package ntfy publishes to one topic and coalesces app down/up transitions
// into a single message per window (spec §5).
package ntfy

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Publisher struct {
	URL, Topic string
	HTTP       *http.Client
}

func New(url, topic string) *Publisher {
	if url == "" {
		return nil
	}
	return &Publisher{URL: strings.TrimRight(url, "/"), Topic: topic, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

type Message struct {
	Title, Body, Click string
	Priority           int
	Tags               []string
}

func (p *Publisher) Publish(ctx context.Context, m Message) error {
	if p == nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+"/"+p.Topic, strings.NewReader(m.Body))
	if err != nil {
		return err
	}
	if m.Title != "" {
		req.Header.Set("Title", m.Title)
	}
	if m.Priority != 0 {
		req.Header.Set("Priority", strconv.Itoa(m.Priority))
	}
	if m.Click != "" {
		req.Header.Set("Click", m.Click)
	}
	if len(m.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(m.Tags, ","))
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ntfy: %s", resp.Status)
	}
	return nil
}

// Coalescer collects down/up transitions for `window` after the first one,
// then sends one message. An app that goes down and comes back inside the
// window is reported by its final state only.
type Coalescer struct {
	p         *Publisher
	window    time.Duration
	publicURL string
	mu        sync.Mutex
	pending   map[string]bool // app -> down?
	timer     *time.Timer
}

func NewCoalescer(p *Publisher, window time.Duration, publicURL string) *Coalescer {
	return &Coalescer{p: p, window: window, publicURL: publicURL, pending: map[string]bool{}}
}

func (c *Coalescer) Transition(app string, down bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[app] = down
	if c.timer == nil {
		c.timer = time.AfterFunc(c.window, func() { c.Flush(context.Background()) })
	}
}

func (c *Coalescer) Flush(ctx context.Context) {
	c.mu.Lock()
	batch := c.pending
	c.pending = map[string]bool{}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.mu.Unlock()
	if len(batch) == 0 || c.p == nil {
		return
	}
	var downs, ups []string
	for app, d := range batch {
		if d {
			downs = append(downs, app)
		} else {
			ups = append(ups, app)
		}
	}
	sort.Strings(downs)
	sort.Strings(ups)
	var parts []string
	if len(downs) > 0 {
		parts = append(parts, "down: "+strings.Join(downs, ", "))
	}
	if len(ups) > 0 {
		parts = append(parts, "up: "+strings.Join(ups, ", "))
	}
	m := Message{Title: "Weir: app state", Body: strings.Join(parts, "\n"), Priority: 2, Click: c.publicURL + "/health"}
	if len(downs) == 0 {
		m.Tags = []string{"white_check_mark"}
	} else {
		m.Tags = []string{"warning"}
	}
	_ = c.p.Publish(ctx, m)
}
```

- [ ] **Step 4: Run tests**

Run: `gofmt -l . ; go vet ./internal/apps/ntfy/ && go test -race ./internal/apps/ntfy/ 2>&1 | tail -1`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/apps/ntfy
git commit -m "ntfy publisher; down/up transitions coalesced into one push per minute

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 10: Queue cleaner

**Files:**
- Create: `internal/cleaner/cleaner.go`, `internal/cleaner/detect.go`, `internal/cleaner/cleaner_test.go`

**Interfaces:**
- Consumes: `config.Cleaner`, `snapshot.Store` (`RadarrQueue`, `SonarrQueue`, `Qbit`), `store.Store`, `arr.Client.DeleteQueueItem`, `qbit.Client.Trackers`, `ntfy.Publisher`, `metrics.M`.
- Produces:
  ```go
  type Condition string
  const (Stalled Condition = "stalled"; Slow Condition = "slow"; DeadTracker Condition = "deadtracker"; Orphaned Condition = "orphaned")
  type TrackerSource interface{ Trackers(ctx context.Context, hash string) ([]qbit.Tracker, error) }
  type Cleaner struct{ ... }
  func New(cfg config.Cleaner, snap *snapshot.Store, db *store.Store, arrs map[string]*arr.Client, trackers TrackerSource, pub *ntfy.Publisher, m *metrics.M, publicURL string) *Cleaner
  type Decision struct {
      App string; Item arr.QueueItem; Condition Condition; Strikes int
      Act bool          // strike threshold reached and cap not exceeded
      Capped bool       // threshold reached but per-title cap hit
  }
  func (c *Cleaner) Evaluate(ctx context.Context, now time.Time) ([]Decision, error) // detect, record strikes, decide
  func (c *Cleaner) Apply(ctx context.Context, ds []Decision, now time.Time)          // report/act per mode, log, notify
  func (c *Cleaner) Run(ctx context.Context)                                           // every cfg.Interval: Evaluate then Apply
  // detect.go (pure)
  type obs struct{ Downloaded int64; LastProgress, SlowSince time.Time }
  func detect(cfg config.Cleaner, item arr.QueueItem, t qbit.Torrent, found bool, trackers []qbit.Tracker, prev obs, now time.Time) (conds []Condition, next obs, progressed bool)
  ```

- [ ] **Step 1: Write the failing tests**

```go
// internal/cleaner/cleaner_test.go
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
		{"import blocked is never touched", func() arr.QueueItem { i := item(1, "h", 500); i.TrackedDownloadState = "importBlocked"; return i }(),
			qbit.Torrent{State: "stalledDL"}, true, nil, obs{Downloaded: 500, LastProgress: t0}, t0.Add(5 * time.Hour), nil},
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

// harness wires a Cleaner to a temp store, a fake Radarr that records DELETEs, and a fake tracker source.
type harness struct {
	c       *Cleaner
	snap    *snapshot.Store
	db      *store.Store
	mu      sync.Mutex
	deletes []string
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
	h.snap.RadarrQueue.SetOK([]arr.QueueItem{item(42, "h", 500)}, t0)
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
	if len(h.deletes) != 1 || h.deletes[0] != "/api/v3/queue/42?blocklist=true&removeFromClient=true&skipRedownload=false" {
		t.Fatalf("deletes: %v", h.deletes)
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
	if len(h.deletes) != 0 {
		t.Fatalf("report mode deleted: %v", h.deletes)
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
	if len(h.deletes) != 0 {
		t.Fatal("capped title must not be deleted")
	}
}
```

- [ ] **Step 2: Run to see failure**

Run: `go test ./internal/cleaner/ 2>&1 | tail -2`
Expected: `undefined: detect` (or `New`).

- [ ] **Step 3: Write detect.go**

```go
// internal/cleaner/detect.go
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

// untouchable states: the bytes are complete; spec §7 says never act.
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

// operator-controlled or transient states the cleaner leaves alone.
func skippedState(state string) bool {
	switch state {
	case "pausedDL", "stoppedDL", "checkingDL", "checkingResumeData", "queuedDL", "moving":
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
		next.Downloaded, next.LastProgress = t.Downloaded, now
		progressed = !prev.LastProgress.IsZero()
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
```

- [ ] **Step 4: Write cleaner.go**

```go
// internal/cleaner/cleaner.go
// Package cleaner removes downloads that will never finish (spec §7). It is
// the only code in Weir that removes anything, and it refuses completed items.
package cleaner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	Act       bool
	Capped    bool
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
	prev      map[string]obs
}

func New(cfg config.Cleaner, snap *snapshot.Store, db *store.Store, arrs map[string]*arr.Client, trackers TrackerSource,
	pub *ntfy.Publisher, m *metrics.M, publicURL string) *Cleaner {
	return &Cleaner{cfg: cfg, snap: snap, db: db, arrs: arrs, trackers: trackers, pub: pub, m: m, publicURL: publicURL, prev: map[string]obs{}}
}

func (c *Cleaner) queues() map[string][]arr.QueueItem {
	return map[string][]arr.QueueItem{
		"radarr": c.snap.RadarrQueue.Get().Data,
		"sonarr": c.snap.SonarrQueue.Get().Data,
	}
}

func (c *Cleaner) Evaluate(ctx context.Context, now time.Time) ([]Decision, error) {
	if c.cfg.Mode == "off" {
		return nil, nil
	}
	qs := c.snap.Qbit.Get()
	if qs.FetchedAt.IsZero() {
		return nil, nil // never judge against an empty torrent map
	}
	seen := map[string]bool{}
	var out []Decision
	for app, items := range c.arrs {
		for _, item := range c.queues()[app] {
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
		// report exactly once, at the threshold; act mode acts every time the threshold holds
		atThreshold := d.Strikes == c.cfg.Strikes
		switch {
		case d.Capped && atThreshold:
			c.record(ctx, d, "cleaner.report", now, "ok", "", fmt.Sprintf("giving up on %q for today: %d removals in 24 h", d.Item.Title, c.cfg.MaxActionsPerTitle))
		case d.Act && c.cfg.Mode == "act":
			err := c.arrs[d.App].DeleteQueueItem(ctx, d.Item.ID, true)
			outcome, errText := "ok", ""
			if err != nil {
				outcome, errText = "failed", err.Error()
			} else {
				_ = c.db.ClearStrikes(ctx, d.Item.DownloadID, now)
				delete(c.prev, d.Item.DownloadID)
			}
			c.record(ctx, d, "cleaner.remove", now, outcome, errText, fmt.Sprintf("removed, blocklisted and searching again: %s (%s)", d.Item.Title, d.Condition))
		case d.Act && c.cfg.Mode == "report" && atThreshold:
			c.record(ctx, d, "cleaner.report", now, "ok", "", fmt.Sprintf("would remove: %s (%s)", d.Item.Title, d.Condition))
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
```

- [ ] **Step 5: Run tests**

Run: `gofmt -l . ; go vet ./internal/cleaner/ && go test ./internal/cleaner/ 2>&1 | tail -1`
Expected: `ok`. If `TestDetectTable/"slow and far ETA"` fails, check that `prev.SlowSince` is honoured: the case sets it 21 minutes before `now`.

- [ ] **Step 6: Commit**

```bash
git add internal/cleaner
git commit -m "Queue cleaner: stalled/slow/dead-tracker/orphaned strikes, report and act modes, per-title cap; completed items are untouchable

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 11: Web layer: shell, downloads page, SSE, write routes

**Files:**
- Create: `internal/web/server.go`, `internal/web/sse.go`, `internal/web/view.go`, `internal/web/templates/layout.html`, `internal/web/templates/downloads.html`, `internal/web/static/app.css`, `internal/web/static/htmx.min.js`, `internal/web/static/sse.js`, `internal/web/server_test.go`

**Interfaces:**
- Consumes: `snapshot.Store`, `store.Store`, `arr.Client` (radarr, sonarr), `qbit.Client` (Stop/Start), `metrics.M`, `config.Config`.
- Produces:
  ```go
  type Deps struct {
      Cfg config.Config; Snap *snapshot.Store; DB *store.Store; M *metrics.M
      Arrs map[string]*arr.Client; Qbit QbitControl   // QbitControl interface{ Stop, Start(ctx, hash) error }
      Now func() time.Time
  }
  func New(d Deps) http.Handler
  func (h *Hub) Broadcast(event string, html string)   // SSE hub; the qbit speed ticker in Task 12 calls it
  ```
  Routes in this task: `GET /` (redirects to `/downloads` until Plan 3 adds the overview), `GET /downloads`, `GET /downloads/rows` (the HTMX fragment the SSE swaps), `GET /events`, `GET /healthz`, `GET /metrics`, `GET /static/*`, `POST /downloads/{hash}/pause|resume|blocklist`, `GET /api/downloads` (JSON).
- Middleware `requireAction` returns 403 on any `POST` without `X-Weir-Action: 1`. Actor = `Tailscale-User-Login` header or remote address.
- `view.go` merges the two queues with the qbit map into `[]Row{App, Title, Hash, State, Progress, Speed, ETA, Strikes, InTrouble, Completed, QueueID}`; that is the pure function the tests cover.

- [ ] **Step 1: Vendor HTMX**

Run: `mkdir -p internal/web/static && curl -sfL https://cdnjs.cloudflare.com/ajax/libs/htmx/2.0.4/htmx.min.js -o internal/web/static/htmx.min.js && curl -sfL https://cdnjs.cloudflare.com/ajax/libs/htmx-ext-sse/2.2.2/sse.js -o internal/web/static/sse.js && wc -c internal/web/static/*.js`
Expected: two files, each tens of KB.

- [ ] **Step 2: Write the failing tests**

```go
// internal/web/server_test.go
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

func (f *fakeQbit) Stop(_ context.Context, h string) error  { f.calls = append(f.calls, "stop "+h); return nil }
func (f *fakeQbit) Start(_ context.Context, h string) error { f.calls = append(f.calls, "start "+h); return nil }

func testServer(t *testing.T) (http.Handler, *snapshot.Store, *fakeQbit, *[]string) {
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
	snap.RadarrQueue.SetOK([]arr.QueueItem{{ID: 1, Title: "Film", DownloadID: "aaa", Size: 100, SizeLeft: 40, MovieID: 3, TrackedDownloadState: "downloading"}}, now)
	snap.SonarrQueue.SetOK([]arr.QueueItem{{ID: 2, Title: "Show S01E01", DownloadID: "bbb", Size: 100, SizeLeft: 0, EpisodeID: 9, TrackedDownloadState: "importBlocked"}}, now)
	snap.Qbit.SetOK(qbit.State{DLSpeed: 1 << 20, Torrents: map[string]qbit.Torrent{
		"aaa": {Hash: "aaa", State: "downloading", Progress: .6, DLSpeed: 1 << 20, ETA: 40},
		"bbb": {Hash: "bbb", State: "uploading", Progress: 1},
	}}, now)
	fq := &fakeQbit{}
	h := New(Deps{Cfg: config.Config{Apps: map[config.App]config.AppConfig{"radarr": {}, "sonarr": {}, "qbit": {}}},
		Snap: snap, DB: db, M: metrics.New(), Arrs: map[string]*arr.Client{"radarr": arr.New(arrSrv.URL, "k", "v3"), "sonarr": arr.New(arrSrv.URL, "k", "v3")},
		Qbit: fq, Now: func() time.Time { return now }})
	return h, snap, fq, &arrCalls
}

func get(h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func post(h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDownloadsPageRendersRows(t *testing.T) {
	h, _, _, _ := testServer(t)
	w := get(h, "/downloads", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Film") || !strings.Contains(body, "Show S01E01") {
		t.Fatalf("code %d body %.200s", w.Code, body)
	}
	if !strings.Contains(body, `hx-ext="sse"`) {
		t.Fatal("downloads page must subscribe to SSE")
	}
}

func TestJSONNegotiation(t *testing.T) {
	h, _, _, _ := testServer(t)
	w := get(h, "/api/downloads", nil)
	var env struct {
		OK   bool            `json:"ok"`
		Data []Row           `json:"data"`
		At   json.RawMessage `json:"fetchedAt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || !env.OK || len(env.Data) != 2 {
		t.Fatalf("json: %v %s", err, w.Body.String())
	}
	if env.Data[0].App != "radarr" || env.Data[0].Progress != .6 || env.Data[1].Completed != true {
		t.Fatalf("rows: %+v", env.Data)
	}
}

func TestWritesRequireHeader(t *testing.T) {
	h, _, fq, arrCalls := testServer(t)
	if w := post(h, "/downloads/aaa/pause", nil); w.Code != 403 {
		t.Fatalf("missing header should be 403, got %d", w.Code)
	}
	if w := post(h, "/downloads/aaa/pause", map[string]string{"X-Weir-Action": "1"}); w.Code != 200 {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	if len(fq.calls) != 1 || fq.calls[0] != "stop aaa" {
		t.Fatalf("qbit calls: %v", fq.calls)
	}
	// blocklist on an uncompleted item goes to the owning arr
	if w := post(h, "/downloads/aaa/blocklist", map[string]string{"X-Weir-Action": "1", "Tailscale-User-Login": "smm@github"}); w.Code != 200 {
		t.Fatalf("blocklist: %d %s", w.Code, w.Body.String())
	}
	if len(*arrCalls) != 1 || !strings.HasPrefix((*arrCalls)[0], "DELETE /api/v3/queue/1?") {
		t.Fatalf("arr calls: %v", *arrCalls)
	}
	// blocklist on a COMPLETED item is refused
	if w := post(h, "/downloads/bbb/blocklist", map[string]string{"X-Weir-Action": "1"}); w.Code != 409 {
		t.Fatalf("completed item must be refused with 409, got %d", w.Code)
	}
	if len(*arrCalls) != 1 {
		t.Fatal("no arr call may be made for a completed item")
	}
}

func TestHealthzAndMetrics(t *testing.T) {
	h, _, _, _ := testServer(t)
	if w := get(h, "/healthz", nil); w.Code != 200 {
		t.Fatal("healthz")
	}
	if w := get(h, "/metrics", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "weir_sse_clients") {
		t.Fatal("metrics")
	}
}
```

- [ ] **Step 3: Run to see failure**

Run: `go test ./internal/web/ 2>&1 | tail -2`
Expected: `undefined: New`.

- [ ] **Step 4: Write view.go (the pure merge)**

```go
// internal/web/view.go
package web

import (
	"sort"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/qbit"
	"github.com/riverr4t/weir/internal/snapshot"
	"github.com/riverr4t/weir/internal/store"
)

type Row struct {
	App       string  `json:"app"`
	QueueID   int64   `json:"queueId"`
	Title     string  `json:"title"`
	Hash      string  `json:"hash"`
	State     string  `json:"state"`     // qbit state, or the arr's trackedDownloadState when the torrent is gone
	Progress  float64 `json:"progress"`  // 0..1
	Speed     int64   `json:"speed"`     // bytes/s
	ETA       int64   `json:"eta"`       // seconds; -1 unknown
	Strikes   int     `json:"strikes"`
	Completed bool    `json:"completed"`
	InTrouble bool    `json:"inTrouble"` // strikes > 0, or importBlocked/importFailed, or orphaned
	Paused    bool    `json:"paused"`
	Message   string  `json:"message"`   // first arr status message, if any
}

func rows(s *snapshot.Store, strikes []store.Strike) []Row {
	q := s.Qbit.Get().Data
	byHash := map[string]int{}
	for _, st := range strikes {
		byHash[st.DownloadID] += st.Count
	}
	var out []Row
	add := func(app string, items []arr.QueueItem) {
		for _, it := range items {
			r := Row{App: app, QueueID: it.ID, Title: it.Title, Hash: it.DownloadID, Strikes: byHash[it.DownloadID], ETA: -1}
			if it.Size > 0 {
				r.Progress = 1 - it.SizeLeft/it.Size
			}
			r.Completed = it.Completed()
			if len(it.StatusMessages) > 0 && len(it.StatusMessages[0].Messages) > 0 {
				r.Message = it.StatusMessages[0].Messages[0]
			}
			if t, ok := q.Torrents[it.DownloadID]; ok {
				r.State, r.Speed, r.Progress = t.State, t.DLSpeed, t.Progress
				if t.ETA > 0 && t.ETA < 8640000 {
					r.ETA = t.ETA
				}
				r.Paused = t.State == "pausedDL" || t.State == "stoppedDL"
			} else {
				r.State = it.TrackedDownloadState
				if it.Protocol == "torrent" {
					r.InTrouble = true
				}
			}
			switch it.TrackedDownloadState {
			case "importBlocked", "importFailed":
				r.InTrouble = true
			}
			if r.Strikes > 0 {
				r.InTrouble = true
			}
			out = append(out, r)
		}
	}
	add("radarr", s.RadarrQueue.Get().Data)
	add("sonarr", s.SonarrQueue.Get().Data)
	sort.SliceStable(out, func(i, j int) bool { // trouble first, then fastest
		if out[i].InTrouble != out[j].InTrouble {
			return out[i].InTrouble
		}
		return out[i].Speed > out[j].Speed
	})
	return out
}

// ownerOf finds the arr queue item for a torrent hash, for the blocklist route.
func ownerOf(s *snapshot.Store, hash string) (app string, item arr.QueueItem, ok bool) {
	for _, it := range s.RadarrQueue.Get().Data {
		if it.DownloadID == hash {
			return "radarr", it, true
		}
	}
	for _, it := range s.SonarrQueue.Get().Data {
		if it.DownloadID == hash {
			return "sonarr", it, true
		}
	}
	return "", arr.QueueItem{}, false
}

func speeds(s *snapshot.Store) (dl, up int64, at time.Time) {
	r := s.Qbit.Get()
	return r.Data.DLSpeed, r.Data.UPSpeed, r.FetchedAt
}

var _ = qbit.State{}
```

- [ ] **Step 5: Write sse.go**

```go
// internal/web/sse.go
package web

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/riverr4t/weir/internal/metrics"
)

type Hub struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
	m       *metrics.M
}

func NewHub(m *metrics.M) *Hub { return &Hub{clients: map[chan string]struct{}{}, m: m} }

// Broadcast sends one event; slow clients (full buffer) are dropped, not waited for.
func (h *Hub) Broadcast(event, data string) {
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, oneLine(data))
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
			delete(h.clients, ch)
			close(ch)
		}
	}
	h.m.SSEClients.Set(float64(len(h.clients)))
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	ch := make(chan string, 16)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.m.SSEClients.Set(float64(len(h.clients)))
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if _, live := h.clients[ch]; live {
			delete(h.clients, ch)
			close(ch)
		}
		h.m.SSEClients.Set(float64(len(h.clients)))
		h.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": hello\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			fl.Flush()
		}
	}
}

// oneLine makes an HTML fragment safe as a single SSE data line.
func oneLine(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			out = append(out, ' ')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}
```

- [ ] **Step 6: Write server.go**

```go
// internal/web/server.go
// Package web serves the pages, the JSON API and the SSE stream (spec §10).
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/snapshot"
	"github.com/riverr4t/weir/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type QbitControl interface {
	Stop(ctx context.Context, hash string) error
	Start(ctx context.Context, hash string) error
}

type Deps struct {
	Cfg  config.Config
	Snap *snapshot.Store
	DB   *store.Store
	M    *metrics.M
	Arrs map[string]*arr.Client
	Qbit QbitControl
	Now  func() time.Time
}

type Server struct {
	Deps
	Hub *Hub
	tpl *template.Template
}

func New(d Deps) *Server {
	if d.Now == nil {
		d.Now = time.Now
	}
	s := &Server{Deps: d, Hub: NewHub(d.M)}
	s.tpl = template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html"))
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("GET /metrics", s.M.Handler())
	mux.Handle("GET /events", s.Hub)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/downloads", http.StatusFound) })
	mux.HandleFunc("GET /downloads", s.downloads)
	mux.HandleFunc("GET /downloads/rows", s.downloadRows)
	mux.HandleFunc("GET /api/downloads", s.apiDownloads)
	mux.Handle("POST /downloads/{hash}/pause", requireAction(s.torrentAction("pause")))
	mux.Handle("POST /downloads/{hash}/resume", requireAction(s.torrentAction("resume")))
	mux.Handle("POST /downloads/{hash}/blocklist", requireAction(http.HandlerFunc(s.blocklist)))
	return mux
}

// ServeHTTP lets tests use *Server directly.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }

func requireAction(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Weir-Action") != "1" {
			http.Error(w, "missing X-Weir-Action header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func actor(r *http.Request) string {
	if v := r.Header.Get("Tailscale-User-Login"); v != "" {
		return v
	}
	return r.RemoteAddr
}

type page struct {
	Title   string
	Page    string
	Apps    []string
	Rows    []Row
	DL, UP  int64
	Actions []store.Action
	Now     time.Time
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tpl.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render", "template", name, "err", err)
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (s *Server) rows(ctx context.Context) []Row {
	strikes, _ := s.DB.Strikes(ctx)
	return rows(s.Snap, strikes)
}

func (s *Server) downloads(w http.ResponseWriter, r *http.Request) {
	acts, _ := s.DB.Actions(r.Context(), 50)
	dl, up, _ := speeds(s.Snap)
	s.render(w, "downloads.html", page{Title: "Downloads", Page: "downloads", Rows: s.rows(r.Context()), DL: dl, UP: up, Actions: acts, Now: s.Now()})
}

func (s *Server) downloadRows(w http.ResponseWriter, r *http.Request) {
	s.render(w, "rows", page{Rows: s.rows(r.Context()), Now: s.Now()})
}

// RowsHTML renders the fragment the SSE ticker broadcasts (Task 12).
func (s *Server) RowsHTML(ctx context.Context) string {
	var buf bytes.Buffer
	_ = s.tpl.ExecuteTemplate(&buf, "rows", page{Rows: s.rows(ctx), Now: s.Now()})
	return buf.String()
}

func (s *Server) SpeedsHTML() string {
	dl, up, _ := speeds(s.Snap)
	var buf bytes.Buffer
	_ = s.tpl.ExecuteTemplate(&buf, "speeds", page{DL: dl, UP: up})
	return buf.String()
}

func writeJSON(w http.ResponseWriter, ok bool, fetchedAt time.Time, data any, err error) {
	w.Header().Set("Content-Type", "application/json")
	env := map[string]any{"ok": ok}
	if ok {
		env["fetchedAt"], env["data"] = fetchedAt, data
	} else {
		env["error"] = err.Error()
	}
	json.NewEncoder(w).Encode(env)
}

func (s *Server) apiDownloads(w http.ResponseWriter, r *http.Request) {
	_, _, at := speeds(s.Snap)
	writeJSON(w, true, at, s.rows(r.Context()), nil)
}

func (s *Server) log(ctx context.Context, r *http.Request, kind, app, subject string, detail map[string]any, err error) {
	d, _ := json.Marshal(detail)
	outcome, errText := "ok", ""
	if err != nil {
		outcome, errText = "failed", err.Error()
	}
	_, _ = s.DB.LogAction(ctx, store.Action{At: s.Now(), Kind: kind, App: app, Subject: subject, Detail: string(d), Actor: actor(r), Outcome: outcome, Error: errText})
}

func (s *Server) torrentAction(kind string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hash := r.PathValue("hash")
		var err error
		if kind == "pause" {
			err = s.Qbit.Stop(r.Context(), hash)
		} else {
			err = s.Qbit.Start(r.Context(), hash)
		}
		_, item, _ := ownerOf(s.Snap, hash)
		s.log(r.Context(), r, "torrent."+kind, "qbit", item.Title, map[string]any{"hash": hash}, err)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.downloadRows(w, r)
	})
}

// blocklist: remove an UNCOMPLETED download through its arr, blocklist the
// release and search again. Completed items are refused (spec §7, §10).
func (s *Server) blocklist(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	app, item, ok := ownerOf(s.Snap, hash)
	if !ok {
		http.Error(w, "no arr queue item for that download", http.StatusNotFound)
		return
	}
	if item.Completed() {
		http.Error(w, "download is complete; Weir does not remove completed downloads", http.StatusConflict)
		return
	}
	err := s.Arrs[app].DeleteQueueItem(r.Context(), item.ID, true)
	s.log(r.Context(), r, "torrent.blocklist", app, item.Title, map[string]any{"hash": hash, "queueId": item.ID, "titleKey": item.TitleKey()}, err)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.downloadRows(w, r)
}

var funcs = template.FuncMap{
	"bytes": func(n int64) string {
		f := float64(n)
		for _, u := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
			if f < 1024 || u == "TiB" {
				if u == "B" {
					return fmt.Sprintf("%d %s", n, u)
				}
				return fmt.Sprintf("%.1f %s", f, u)
			}
			f /= 1024
		}
		return ""
	},
	"pct": func(p float64) string { return fmt.Sprintf("%.0f%%", p*100) },
	"eta": func(secs int64) string {
		if secs < 0 {
			return "—"
		}
		d := time.Duration(secs) * time.Second
		switch {
		case d < time.Minute:
			return fmt.Sprintf("%ds", secs)
		case d < time.Hour:
			return fmt.Sprintf("%dm", int(d.Minutes()))
		default:
			return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
		}
	},
	"ago": func(t, now time.Time) string {
		if t.IsZero() {
			return "never"
		}
		d := now.Sub(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 48*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		default:
			return t.Format("Jan 2")
		}
	},
	"title": func(s string) string { return strings.ToUpper(s[:1]) + s[1:] },
}
```

Adjust the test helper's `New(Deps{...})` return: `New` returns `*Server`, which implements `http.Handler`, so the test compiles unchanged.

- [ ] **Step 7: Write the templates and CSS**

`internal/web/templates/layout.html` defines `layout` (html shell with the rail, `hx-headers='{"X-Weir-Action":"1"}'` on `<body>`, the keyboard shortcut script, and a `{{block "content" .}}{{end}}`). `internal/web/templates/downloads.html` defines `downloads.html` (uses `layout`), plus the fragments `rows` and `speeds`:

```html
{{define "downloads.html"}}{{template "layout" .}}{{end}}
{{define "content"}}
<section class="head">
  <h1>Downloads</h1>
  <div id="speeds" hx-ext="sse" sse-connect="/events" sse-swap="speeds">{{template "speeds" .}}</div>
</section>
<div id="rows" hx-ext="sse" sse-connect="/events" sse-swap="queue">{{template "rows" .}}</div>
<section class="log">
  <h2>Cleaner and actions</h2>
  {{if not .Actions}}<p class="muted">Nothing yet.</p>{{end}}
  <ul>{{range .Actions}}<li><span class="when">{{ago .At $.Now}}</span> <b>{{.Kind}}</b> {{.Subject}} <span class="muted">{{.Actor}} · {{.Outcome}}{{if .Error}} · {{.Error}}{{end}}</span></li>{{end}}</ul>
</section>
{{end}}

{{define "speeds"}}<span class="dl">↓ {{bytes .DL}}/s</span> <span class="up">↑ {{bytes .UP}}/s</span>{{end}}

{{define "rows"}}
{{if not .Rows}}<p class="empty">Queue is empty.</p>{{end}}
<table class="queue">
{{range .Rows}}<tr class="{{if .InTrouble}}trouble{{end}} {{if .Completed}}done{{end}}">
  <td class="app"><span class="dot {{.App}}"></span>{{.App}}</td>
  <td class="title">{{.Title}}{{if .Message}}<div class="msg">{{.Message}}</div>{{end}}</td>
  <td class="state">{{.State}}{{if .Strikes}} <span class="strikes">{{.Strikes}}✕</span>{{end}}</td>
  <td class="prog"><div class="bar"><i style="width:{{pct .Progress}}"></i></div><span>{{pct .Progress}}</span></td>
  <td class="speed">{{if .Speed}}{{bytes .Speed}}/s{{end}}</td>
  <td class="eta">{{eta .ETA}}</td>
  <td class="acts">
    {{if not .Completed}}
      {{if .Paused}}<button hx-post="/downloads/{{.Hash}}/resume" hx-target="#rows">Resume</button>
      {{else}}<button hx-post="/downloads/{{.Hash}}/pause" hx-target="#rows">Pause</button>{{end}}
      <button class="danger" hx-post="/downloads/{{.Hash}}/blocklist" hx-target="#rows" hx-confirm="Remove this download, blocklist the release and search again?">Blocklist</button>
    {{end}}
  </td>
</tr>{{end}}
</table>
{{end}}
```

The exact `layout.html` and `app.css` are written at implementation time following the design direction in spec §10 (charcoal, one accent, system font, rail left, bottom bar under 700 px). They contain no logic the tests depend on beyond the `hx-headers` attribute on `<body>` and the `{{block "content"}}`.

- [ ] **Step 8: Run tests**

Run: `gofmt -l . ; go vet ./internal/web/ && go test ./internal/web/ 2>&1 | tail -1`
Expected: `ok`.

- [ ] **Step 9: Commit**

```bash
git add internal/web
git commit -m "Web: shell, downloads page with live rows, JSON negotiation, action-header gate, pause/resume/blocklist

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 12: Wiring, image, CI, compose, first deploy on 3006

**Files:**
- Modify: `cmd/weir/main.go` (full wiring)
- Create: `Dockerfile`, `.github/workflows/ci.yml`, `.env.example`, `README.md`
- Create in ratholepi-stack: `weir/compose.yaml`, `weir/.env.example`, `weir/README.md`

**Interfaces:**
- Consumes everything above.

- [ ] **Step 1: Wire main**

`run()` in `cmd/weir/main.go` becomes:

```go
func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	setupLogging(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	m := metrics.New()
	db, err := store.Open(filepath.Join(cfg.DataDir, "weir.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	pub := ntfy.New(cfg.NtfyURL, cfg.NtfyTopic)
	coal := ntfy.NewCoalescer(pub, time.Minute, cfg.PublicURL)
	snap := &snapshot.Store{}
	arrs := map[string]*arr.Client{}
	if cfg.Enabled(config.Radarr) {
		arrs["radarr"] = radarr.Start(ctx, cfg.Apps[config.Radarr], snap, m, coal.Transition)
	}
	if cfg.Enabled(config.Sonarr) {
		arrs["sonarr"] = sonarr.Start(ctx, cfg.Apps[config.Sonarr], snap, m, coal.Transition)
	}
	var qb *qbit.Client
	if cfg.Enabled(config.Qbit) {
		qc := cfg.Apps[config.Qbit]
		qb = qbit.New(qc.URL, qc.User, qc.Pass)
		go poll.Run(ctx, poll.Spec{App: "qbit", Kind: "sync", Interval: 2 * time.Second, Notify: coal.Transition}, &snap.Qbit, m, qb.Sync)
	}
	srv := web.New(web.Deps{Cfg: cfg, Snap: snap, DB: db, M: m, Arrs: arrs, Qbit: qb})
	if qb != nil {
		cl := cleaner.New(cfg.Cleaner, snap, db, arrs, qb, pub, m, cfg.PublicURL)
		go cl.Run(ctx)
	}
	go ticker(ctx, srv, snap, m)
	go func() { // nightly prune of the action log (90 days)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(24 * time.Hour):
				_ = db.Prune(ctx, time.Now().AddDate(0, 0, -90))
			}
		}
	}()

	hs := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	slog.Info("weir listening", "addr", cfg.Listen, "apps", len(cfg.Apps), "cleaner", cfg.Cleaner.Mode)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		coal.Flush(context.Background())
		sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hs.Shutdown(sd)
	}
}

// ticker pushes speeds every 2 s and the queue rows when they change, and
// refreshes the snapshot-age gauges every 15 s.
func ticker(ctx context.Context, srv *web.Server, snap *snapshot.Store, m *metrics.M) {
	fast := time.NewTicker(2 * time.Second)
	slow := time.NewTicker(15 * time.Second)
	defer fast.Stop()
	defer slow.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			srv.Hub.Broadcast("speeds", srv.SpeedsHTML())
			if h := srv.RowsHTML(ctx); h != last {
				last = h
				srv.Hub.Broadcast("queue", h)
			}
		case <-slow.C:
			now := time.Now()
			m.SnapshotAge.WithLabelValues("radarr", "queue").Set(snap.RadarrQueue.Get().Age(now).Seconds())
			m.SnapshotAge.WithLabelValues("sonarr", "queue").Set(snap.SonarrQueue.Get().Age(now).Seconds())
			m.SnapshotAge.WithLabelValues("qbit", "sync").Set(snap.Qbit.Get().Age(now).Seconds())
		}
	}
}

func setupLogging(level string) {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}
```

Imports: `path/filepath`, and the internal packages `arr`, `cleaner`, `config`, `metrics`, `ntfy`, `poll`, `qbit`, `radarr`, `snapshot`, `sonarr`, `store`, `web`. The `web.QbitControl` interface is satisfied by `*qbit.Client`; when qBittorrent is disabled `Qbit` is a typed nil and the pause/resume routes would panic, so `web.torrentAction` must check `s.Qbit == nil` and answer 503 "qBittorrent not configured" (add that guard in `server.go`).

- [ ] **Step 2: Build and smoke locally against the Pi over the tailnet**

Tailnet URLs for the apps exist only for radarr/sonarr via `tailscale serve`? No: the apps bind loopback on the Pi. Smoke test with an ssh tunnel instead:

Run:
```bash
ssh -f -N -L 17878:127.0.0.1:7878 -L 18989:127.0.0.1:8989 -L 18080:127.0.0.1:8080 ratholepi
go build -o /tmp/weir ./cmd/weir && WEIR_LISTEN=:3006 WEIR_DATA_DIR=/tmp/weir-data \
 WEIR_RADARR_URL=http://127.0.0.1:17878 WEIR_RADARR_KEY=$(ssh ratholepi 'grep RADARR_KEY ~/ratholepi-stack/homepage/.env | cut -d= -f2') \
 WEIR_SONARR_URL=http://127.0.0.1:18989 WEIR_SONARR_KEY=$(ssh ratholepi 'grep SONARR_KEY ~/ratholepi-stack/homepage/.env | cut -d= -f2') \
 WEIR_QBIT_URL=http://127.0.0.1:18080 WEIR_QBIT_USER=$(ssh ratholepi 'grep QBIT_USER ~/ratholepi-stack/homepage/.env | cut -d= -f2') \
 WEIR_QBIT_PASS=$(ssh ratholepi 'grep QBIT_PASS ~/ratholepi-stack/homepage/.env | cut -d= -f2') \
 /tmp/weir & sleep 5; curl -s localhost:3006/api/downloads | head -c 300; echo; curl -s localhost:3006/metrics | grep -c '^weir_'
```
Expected: a JSON envelope with `"ok":true`, and a count above 5. Then `kill %1` and `pkill -f 'ssh -f -N -L 17878'`.

- [ ] **Step 3: Dockerfile**

```dockerfile
# Weir: one static Go binary, scratch runtime (spec §9).
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /weir ./cmd/weir

FROM alpine:3.20 AS certs
RUN apk add --no-cache ca-certificates tzdata

FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=certs /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /weir /weir
USER 1000:1000
EXPOSE 3004
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/weir", "healthcheck"]
ENTRYPOINT ["/weir"]
```

Add `var version = "dev"` to `cmd/weir/main.go` and log it at startup.

- [ ] **Step 4: CI**

```yaml
# .github/workflows/ci.yml
name: ci
on:
  push: { branches: [main], tags: ['v*'] }
  pull_request:
permissions: { contents: write, packages: write }
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - run: test -z "$(gofmt -l .)"
      - run: go vet ./...
      - run: go test ./...
  image:
    needs: test
    if: github.event_name == 'push'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with: { registry: ghcr.io, username: "${{ github.actor }}", password: "${{ secrets.GITHUB_TOKEN }}" }
      - id: meta
        uses: docker/metadata-action@v5
        with:
          images: ghcr.io/${{ github.repository }}
          tags: |
            type=ref,event=branch
            type=sha,prefix=sha-
            type=semver,pattern=v{{version}}
            type=raw,value=latest,enable=${{ startsWith(github.ref, 'refs/tags/v') }}
      - uses: docker/build-push-action@v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          build-args: VERSION=${{ github.ref_name }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
  release:
    needs: test
    if: startsWith(github.ref, 'refs/tags/v')
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - run: |
          for a in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "-s -w -X main.version=${{ github.ref_name }}" -o weir-linux-$a ./cmd/weir; done
      - uses: softprops/action-gh-release@v2
        with: { files: weir-linux-* }
```

- [ ] **Step 5: `.env.example` in the weir repo (generic) and README**

```bash
# Weir configuration. Copy to .env; values here are 1Password references
# for the ratholepi deployment (op inject -i .env.example -o .env).
WEIR_RADARR_URL=http://radarr:7878
WEIR_RADARR_KEY=op://Personal/ratholepi radarr/api key
WEIR_SONARR_URL=http://sonarr:8989
WEIR_SONARR_KEY=op://Personal/ratholepi sonarr/api key
WEIR_QBIT_URL=http://gluetun:8080
WEIR_QBIT_USER=op://Personal/ratholepi qbittorrent/username
WEIR_QBIT_PASS=op://Personal/ratholepi qbittorrent/password
WEIR_NTFY_URL=http://ntfy
WEIR_NTFY_TOPIC=ratholepi
WEIR_PUBLIC_URL=https://ratholepi.tail98e0c3.ts.net:3006
WEIR_CLEANER_MODE=report
```

README: what Weir is (three paragraphs from spec §1), the never-delete rule verbatim, the env table copied from spec §4, build (`go build ./cmd/weir`), test (`go test ./...`), image name.

- [ ] **Step 6: Commit and push; create the GitHub repo**

```bash
git add -A && git commit -m "Wire the binary; Dockerfile, CI, env template, README

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
gh repo create riverr4t/weir --public --source=. --push
```

Then watch: `gh run watch --exit-status` (the image job pushes `ghcr.io/riverr4t/weir:main`). Make the package public once: GitHub → Packages → weir → Package settings → Change visibility → Public, so the Pi can pull without a token. This is a browser step for the operator.

- [ ] **Step 7: Compose in ratholepi-stack**

`~/ratholepi-stack/weir/compose.yaml`:

```yaml
# Weir — the arr dashboard and queue cleaner (replaces sluice/).
# Design: ~/Work/weir/docs/superpowers/specs/2026-09-06-weir-design.md
# Trial on host 3006 beside Sluice; the cutover commit moves it to 3004.
services:
  weir:
    image: ghcr.io/riverr4t/weir:main
    container_name: weir
    env_file: .env
    environment:
      TZ: America/New_York
    volumes:
      - /srv/appdata/weir:/config
      - type: bind
        source: /srv/data
        target: /data
        read_only: true
        bind: { create_host_path: false }
    networks: [default, torrent, jellyfin, monitoring]
    ports:
      - "127.0.0.1:3006:3004"
    mem_limit: 128m
    cpus: 1
    restart: unless-stopped

networks:
  torrent:   { external: true, name: torrent_default }
  jellyfin:  { external: true, name: jellyfin_default }
  monitoring: { external: true, name: monitoring }
```

`weir/.env.example` = the file from Step 5. `weir/README.md`: three lines pointing at the spec and the deploy command.

- [ ] **Step 8: Deploy**

On the Pi (ratholepi-deploy skill applies: force-recreate on `.env` change, `sudo` strips env, published ports bypass ufw but this one is loopback-only):

```bash
ssh ratholepi 'cd ~/ratholepi-stack && git pull -q && sudo mkdir -p /srv/appdata/weir && sudo chown 1000:1000 /srv/appdata/weir && cd weir && grep -E "^HOMEPAGE_VAR_(RADARR|SONARR)_KEY=|^HOMEPAGE_VAR_QBIT_" ../homepage/.env | sed -E "s/^HOMEPAGE_VAR_/WEIR_/" > .env && grep -vE "_KEY=|QBIT_(USER|PASS)=" .env.example >> .env && chmod 600 .env && docker compose pull -q && docker compose up -d && sleep 8 && docker compose ps --format "{{.Name}} {{.Status}}" && curl -s 127.0.0.1:3006/api/downloads | head -c 200'
sudo tailscale serve --bg --https=3006 http://127.0.0.1:3006   # once, on the Pi
```

Expected: `weir Up ... (healthy)` and a JSON envelope. Then open `https://ratholepi.tail98e0c3.ts.net:3006/downloads` in the browser and check rows and live speeds.

- [ ] **Step 9: Commit ratholepi-stack**

```bash
cd ~/ratholepi-stack && git add weir && git commit -m "weir: trial deployment on 3006 beside sluice

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

---

## Self-review

- Spec §3 repo layout: Tasks 1–12 create every listed path for this slice; `internal/apps/{lidarr,readarr,prowlarr,bazarr,jellyfin,jellyseerr}` and `internal/rules` come in Plans 2–4.
- §4 config: Task 2, all variables and defaults, all-errors-at-once. §5 snapshot/pollers: Tasks 3, 4, 6, 7; backoff cap, jitter, panic recovery, Down after 3, coalesced notifications (Task 9). §6 store: Task 8 (rules tables arrive with the rules plan). §7 cleaner: Task 10, every condition, strikes, cap, report/act, untouchable states tested explicitly. §9 image/CI/compose: Task 12. §10 routes: downloads and its writes, JSON envelope, action header, actor. §11 metrics: Tasks 4, 10, 11. §12 tests: per task.
- Type consistency: `Row` fields used by templates match `view.go`; `store.Action.Detail` JSON key `titleKey` is used identically by cleaner, web and store; `poll.Spec.Notify` matches `Coalescer.Transition`; `web.QbitControl` matches `*qbit.Client`.
