# Weir — design

*2026-09-06. Replaces Sluice (`ratholepi-stack/sluice`), which is retired by the
cutover in §9. The upstream that prompted this is
[Kha-kis/arr-dashboard](https://github.com/Kha-kis/arr-dashboard); Weir keeps
its shape and drops its weight.*

## 1. What it is

A single Go binary that watches the media pipeline on ratholepi (nine apps),
shows it on one tailnet-only web page, and runs two pieces of automation:

- a **queue cleaner** that removes downloads that will never finish, and
- **library rules** that tag and list media matching conditions such as
  "watched by everyone and untouched for 90 days".

**Weir never deletes anything that has been downloaded.** The binary has no
delete route and no delete code path for library media. The only removal it
performs is the queue cleaner taking out a download that has not completed
(stalled, slow, dead tracker, or orphaned), and that is a failed fetch, not
something the operator has. Rules that match media produce a tag in the arr
and a line on a page; the operator deletes in the arr, or does not.

Decisions made in the brainstorm, in order, so nobody relitigates them:

| Decision | Choice |
|---|---|
| Scope | Automation first, management UI on top |
| Apps | All nine: Radarr, Sonarr, Lidarr, Bookshelf (Readarr API), Prowlarr, qBittorrent, Bazarr, Jellyfin, Jellyseerr |
| Stack | Go, one static binary, SQLite via `modernc.org/sqlite` (no cgo), web UI embedded |
| Home | Own repo `~/Work/weir` → `github.com/riverr4t/weir`, image on GHCR; ratholepi-stack carries only a compose file |
| UI | Go templates + HTMX, vanilla-JS islands only where needed, no bundler |
| Destructive actions | None on media. Queue cleaner acts on uncompleted downloads only, and ships in report mode |
| Omarchy bar card | Later, own spec; the API is shaped for a second client now |
| Name | Weir |
| State model | Pollers write an in-memory snapshot; SQLite holds only durable state |

## 2. Non-goals

- Deleting, unmonitoring, or moving library media. Not even behind a flag.
- TRaSH profile sync (Recyclarr does it), backups (restic covers the config
  volume), login/OIDC/passkeys (tailnet-only, like Homepage), notification
  channels other than ntfy, iframe embeds of the apps, multi-instance
  clustering, PostgreSQL.
- Per-episode, per-track or per-book data in the snapshot. Detail is fetched
  on demand for the one item opened.

## 3. Repository

```
weir/
  cmd/weir/main.go            env parsing, wiring, signal handling; `weir healthcheck` subcommand
  internal/config/            env → Config struct, validation, tests
  internal/snapshot/          the shared state (§5)
  internal/apps/arr/          shared Radarr/Sonarr/Lidarr/Readarr client (v3/v1 API)
  internal/apps/radarr/       thin: types + poller on top of arr
  internal/apps/sonarr/
  internal/apps/lidarr/
  internal/apps/readarr/      Bookshelf speaks Readarr v1 (verified against the live Pi 2026-09-06)
  internal/apps/prowlarr/
  internal/apps/qbit/         sync/maindata delta client
  internal/apps/bazarr/
  internal/apps/jellyfin/
  internal/apps/jellyseerr/
  internal/apps/ntfy/         publish only
  internal/poll/              the poller runner: intervals, backoff, panic recovery, metrics
  internal/store/             SQLite: schema, migrations, typed queries (§6)
  internal/cleaner/           queue cleaner (§7)
  internal/rules/             library rule engine (§8)
  internal/web/               router, handlers, SSE hub, templates/, static/ (embedded)
  internal/metrics/           Prometheus registry and collectors
  omarchy/                    empty placeholder; the bar plugin gets its own spec
  docs/superpowers/specs/     this file, and later ones
  docs/superpowers/plans/
  testdata/fixtures/<app>/    recorded, redacted responses from the real Pi apps
  Dockerfile, .github/workflows/ci.yml, .env.example, .gitignore, README.md
```

Go 1.23+ (`go 1.23` in `go.mod`; the dev box has 1.27). Dependencies, all
of them: `modernc.org/sqlite`, `github.com/prometheus/client_golang`. HTMX is
vendored into `internal/web/static/` as one file. Standard library for HTTP
(`net/http` with Go 1.22 method routing), templates (`html/template`),
logging (`log/slog`), embedding (`embed`).

## 4. Configuration

Environment only. No config file. Every variable is prefixed `WEIR_`.

| Variable | Default | Meaning |
|---|---|---|
| `WEIR_LISTEN` | `:3004` | bind address |
| `WEIR_DATA_DIR` | `/config` | holds `weir.db` |
| `WEIR_MEDIA_DIR` | `/data` | read-only mount of `/srv/data`, for the orphan-file rule; unset disables that rule |
| `WEIR_PUBLIC_URL` | `https://ratholepi.tail98e0c3.ts.net:3004` | used in ntfy click links |
| `WEIR_<APP>_URL` | — | `RADARR SONARR LIDARR READARR PROWLARR QBIT BAZARR JELLYFIN JELLYSEERR`; an unset URL disables that app entirely (pages hide, pollers never start) |
| `WEIR_<APP>_KEY` | — | API key for every app except qBittorrent |
| `WEIR_QBIT_USER`, `WEIR_QBIT_PASS` | — | qBittorrent login |
| `WEIR_<APP>_PUBLIC_URL` | — | the "Open in app" link target (tailnet URL); optional |
| `WEIR_NTFY_URL`, `WEIR_NTFY_TOPIC` | — | unset disables notifications |
| `WEIR_CLEANER_MODE` | `report` | `off`, `report`, or `act` |
| `WEIR_CLEANER_INTERVAL` | `5m` | |
| `WEIR_CLEANER_STALL_AFTER` | `30m` | no progress for this long = stalled |
| `WEIR_CLEANER_SLOW_BELOW` | `50KiB` | speed floor |
| `WEIR_CLEANER_SLOW_FOR` | `20m` | how long below the floor before a strike |
| `WEIR_CLEANER_MAX_ETA` | `48h` | ETA above this while slow = strike |
| `WEIR_CLEANER_STRIKES` | `3` | strikes before action |
| `WEIR_CLEANER_MAX_ACTIONS_PER_TITLE` | `3` | per movie/episode/album/book per 24 h |
| `WEIR_RULES_AT` | `04:10` | daily rule run, local time (`TZ`) |
| `WEIR_LOG_LEVEL` | `info` | |

The committed `.env.example` holds `op://Personal/ratholepi <app>/api key`
references, matching the items that already exist for Homepage and Sluice, so
`op inject -i .env.example -o .env` rebuilds it. `config.Load()` returns
every error at once ("WEIR_RADARR_URL set but WEIR_RADARR_KEY missing", "…")
rather than the first.

Poll intervals are constants, not config (§5); the cleaner thresholds above
are config because they are judgement calls the operator will tune.

## 5. Snapshot and pollers

`snapshot.Store` is one struct behind a `sync.RWMutex`. Each app owns a
field holding its latest `Result[T]`:

```go
type Result[T any] struct {
    Data      T
    FetchedAt time.Time   // zero until the first successful poll
    Err       error       // last poll's error, nil on success; Data keeps the last good value
    Down      bool        // true after 3 consecutive failures
}
```

Pollers replace the whole value under the write lock; readers take a copy
under the read lock. Nothing else in the process talks to an app except the
write handlers (§10), which make one upstream call each.

Poll plan, per app, one goroutine per row:

| App | What | Interval |
|---|---|---|
| qBittorrent | `sync/maindata` (delta, with `rid`) | 2 s |
| Radarr, Sonarr, Lidarr, Readarr | queue | 15 s |
| same | health, disk space, calendar (next 7 d) | 5 min |
| same | library (movies / series / artists+albums / authors+books, top level only), wanted-missing and cutoff-unmet counts | 15 min |
| Prowlarr | health, indexers, indexer stats, applications | 5 min |
| Bazarr | system status, wanted movies/episodes counts, providers | 5 min |
| Jellyfin | sessions (now playing) | 15 s |
| Jellyfin | system info, item counts, per-user played state for the library (§8) | 15 min |
| Jellyseerr | pending requests, counts | 30 s |

`poll.Run` wraps each poller: it calls the fetch, records duration and
outcome to metrics, and on error backs off exponentially from the base
interval to a 5-minute cap with ±20 % jitter. On the third consecutive
failure it sets `Down` and logs one line at `warn`; on the first success
after that it clears `Down` and logs one line. Down/up transitions go to a
notifier that **coalesces**: it waits 60 s after the first transition and
sends one low-priority ntfy message listing every app that changed state in
that window, so the SSD eject (seven containers stopped at once) produces
one push, and the recovery one more. Panics inside a fetch are recovered,
logged with the stack at `error`, and counted; the poller continues.

Startup: all pollers fire immediately, then settle to their intervals.
Pages render whatever the snapshot has, with an "updating…" state for
zero `FetchedAt` and a stale badge showing the age when `Down`.

Speed history for the overview sparkline is an in-memory ring buffer of
the last 10 minutes of qBittorrent transfer samples (300 entries at 2 s).
Weir reads nothing from Prometheus; its pages have no dependency on the
monitoring stack.

Memory budget: the four arr libraries at top level, for this Pi's sizes
(hundreds of movies, tens of series, tens of artists and authors), are well
under 10 MB of Go structs. Episode, track and book lists are fetched by the
detail pages and not retained. Target resident set: under 60 MB; the compose
limit is 128 MB.

## 6. Durable state (SQLite)

One file, `weir.db`, WAL mode, `busy_timeout` 5 s, migrations as numbered
embedded SQL applied at startup inside a transaction. Tables:

- `rules` — id, name, scope (`movie|series|album|book`), enabled, tag
  (nullable text: when set, matches are tagged `weir-<tag>` in the arr;
  the arrs accept only `[a-z0-9-]` in labels, so the tag is slugified),
  conditions (JSON, §8), created/updated.
- `rule_runs` — id, rule_id, started, finished, matched count, bytes.
- `rule_matches` — run_id, arr item id, title, path, size bytes, reason text.
- `strikes` — download id (the arr's `downloadId`, i.e. the torrent hash),
  app, title, condition, count, first/last seen, cleared_at.
- `actions` — id, at, kind (`cleaner.remove|cleaner.report|torrent.pause|…`),
  app, subject (title), detail (JSON), actor (tailnet login or `weir`),
  outcome (`ok|failed`), error text.
- `play_state` — jellyfin item id, user id, played bool, play count,
  last played at, observed at. One row per (item, user), replaced each poll.

The per-title action cap (§7) is a count over `actions` for the last 24 h;
there is no separate table to keep in sync.

Retention: `actions` and `rule_matches` are kept 90 days; a daily task
prunes. Nothing here is big.

## 7. Queue cleaner

Runs every `WEIR_CLEANER_INTERVAL` over the snapshot: the four arr queues
joined to qBittorrent's torrent map by `downloadId` = torrent hash. For each
queue item that has **not completed** (torrent progress < 1.0), it evaluates:

| Condition | Test | Strike when |
|---|---|---|
| stalled | qBittorrent state `stalledDL` with 0 connected seeds, or no byte progress since the last observation | held for `STALL_AFTER` |
| slow | download speed < `SLOW_BELOW` for `SLOW_FOR` and ETA > `MAX_ETA` | each interval it holds |
| dead tracker | every tracker's status is error/not-working, or a tracker message contains "unregistered" | each interval it holds |
| orphaned | queue item's `downloadId` not present in qBittorrent | each interval it holds |

A strike record is per (download, condition). Progress (bytes downloaded
increased since last observation) clears the download's strikes. Reaching
`STRIKES` triggers the action:

1. `DELETE /api/v3/queue/{id}?removeFromClient=true&blocklist=true&skipRedownload=false`
   on the owning arr (v1 for Readarr). The arr removes the torrent and its
   partial files, blocklists the release, and searches again. Weir never
   talks to qBittorrent to delete.
2. Log an `actions` row (with the title key in `detail`), post to ntfy at default
   priority with the title, condition, and a click link to Weir's downloads
   page.

Guards:

- Mode `report` performs step 2 with kind `cleaner.report` and does not call
  the arr. Mode `off` does nothing. Default is `report`; the operator flips
  to `act` after reading a week of reports.
- A title (movie id / episode id / album id / book id) is acted on at most
  `MAX_ACTIONS_PER_TITLE` times per rolling 24 h. Beyond that, report only,
  with a distinct ntfy message once ("giving up on X for today").
- Items with `trackedDownloadState` of `importPending`, `importBlocked`,
  `importFailed`, or with progress 1.0, are **never** touched: the bytes are
  complete. They are surfaced on the downloads page under "needs you" and
  reported once to ntfy when first seen.
- Items whose arr status is `paused` (operator paused it) are skipped.

## 8. Library rules

A rule is `{scope, conditions[], tag?}`. Conditions are AND-ed; each is
`{kind, args}`. Kinds, first version:

| kind | args | true when |
|---|---|---|
| `watched_by_all` | `days` | every Jellyfin user who has played it has it marked played, and none has played it in `days` |
| `watched_by` | `users[]`, `days` | each named user has it played, none in `days` |
| `never_played` | `days` | added to the arr more than `days` ago and no user has a play record |
| `requester_done` | `days` | requested via Jellyseerr by user U, whose linked Jellyfin user id (Jellyseerr's `jellyfinUserId`) has it played, and no other user has played it in `days`; a request from a Jellyseerr user with no linked Jellyfin account never matches |
| `ended_and_finished` | — | series only: Sonarr `ended`, all monitored episodes on disk, `watched_by_all` with 0 days |
| `unmonitored` | — | the arr item is unmonitored and has files |
| `larger_than` | `gib` | size on disk above |
| `added_before` | `days` | |
| `tagged` / `not_tagged` | `tag` | arr tag present/absent |

Scope `album` and `book` support the arr-side kinds (`unmonitored`,
`larger_than`, `added_before`, `tagged`, `never_played` by Jellyfin music
play state where it exists); the Jellyfin-user kinds apply to movies and
series. The editor hides kinds that do not apply to the chosen scope.

Matching movies/series to Jellyfin items uses provider ids: TMDb id for
movies, TVDb id for series, both of which Jellyfin exposes in
`ProviderIds`. Jellyseerr requests carry TMDb/TVDb ids too. Unmatched arr
items are reported on the rule run page as "not found in Jellyfin" and are
never counted as never-played.

Every run stores its matches (`rule_matches`) and shows them on the Rules
page with size and reason; that is what a rule *is*. A rule with `tag` set
additionally ensures a tag named `weir-<tag>` exists in the arr and is on
every matched item, and removes it from items that no longer match. That is
the only arr write the rule engine makes, and it is reversible from the arr
UI. A rule without `tag` writes nothing anywhere.

Runs happen daily at `WEIR_RULES_AT` and on demand per rule. A run reads
the snapshot plus `play_state`; it never fetches. Rule results feed
metrics: `weir_rule_matches{rule}` and `weir_rule_bytes{rule}`.

## 9. Deploy and cutover

**Image.** Two-stage Dockerfile: `golang:1.23-alpine` builds with
`CGO_ENABLED=0 -trimpath -ldflags "-s -w"`; runtime is `FROM scratch` plus
`/etc/ssl/certs`, `/usr/share/zoneinfo`, and the binary. `USER 1000:1000`.
`EXPOSE 3004`. `HEALTHCHECK CMD ["/weir", "healthcheck"]`: the binary's
`healthcheck` subcommand GETs `/healthz` on its own listen address and
exits 0/1, so a scratch image with no shell still reports health to Docker.
Kuma monitors `/healthz` externally as well.

**CI.** `.github/workflows/ci.yml` on push and PR: `go vet ./...`,
`go test ./...`, `gofmt -l` must be empty. On push to `main`: build and push
`ghcr.io/riverr4t/weir:main` and `:sha-<short>` for `linux/amd64,linux/arm64`
via buildx. On tag `v*`: also `:vX.Y.Z` and `:latest`, plus a GitHub release
carrying the two static binaries.

**Compose** in `ratholepi-stack/weir/compose.yaml`:

```yaml
services:
  weir:
    image: ghcr.io/riverr4t/weir:main
    container_name: weir
    env_file: .env
    environment: { TZ: America/New_York }
    volumes:
      - /srv/appdata/weir:/config
      - type: bind
        source: /srv/data
        target: /data
        read_only: true
        bind: { create_host_path: false }
    networks: [default, torrent, jellyfin, monitoring]
    ports: ["127.0.0.1:3004:3004"]
    mem_limit: 128m
    cpus: 1
    restart: unless-stopped
```

Service names on the compose networks, as Homepage documents them:
`radarr:7878`, `sonarr:8989`, `lidarr:8686`, `readarr:8787`,
`bazarr:6767`, `gluetun:8080` (qBittorrent) and `gluetun:9696` (Prowlarr),
`jellyfin:8096`, `jellyseerr:5055`, `prometheus:9090`, `ntfy:80`.

Prometheus scrapes `weir:3004/metrics` (one job added to
`monitoring/prometheus`). Kuma gets an HTTP monitor on `/healthz`.

**As built (2026-09-06).** Slices 1–2 followed plan 1; slices 3–5 were built
inline the same evening. Cells for the six non-arr apps live in their own
packages and hang off `snapshot.Store.Extra` (avoids an import cycle). Book
scope cannot tag (Readarr keeps tags on authors); the run notes say so.

**Cutover.**

1. Weir runs first on host port 3006 (`WEIR_LISTEN=:3006` is not needed;
   only the host side of the port mapping changes) with
   `tailscale serve --https=3006`, `WEIR_CLEANER_MODE=report`, beside Sluice.
2. After the reports look right, one commit in ratholepi-stack: port to
   3004, `sluice/` removed, `host/sandisk-eject.sh` MEDIA_CONTAINERS swaps
   `sluice` for `weir`, Homepage's link and the README updated. On the Pi:
   `docker compose -f sluice/compose.yaml down`, `tailscale serve` 3006
   reset, weir up on 3004.
3. `WEIR_CLEANER_MODE=act` is a separate, later `.env` change, made by the
   operator.

The Pi never builds anything; it pulls. The Docker DNS trap applies: a
`.env` change needs `up -d --force-recreate`, not `restart`.

## 10. Web

**Routes.** Reads render HTML; the same handlers answer `application/json`
when asked for it (`Accept` header), which is the API the later Omarchy
plugin uses. JSON reads answer `{ok, fetchedAt, data}` or `{ok:false, error}`
with HTTP 200 either way; an app being down is data.

| Route | Page |
|---|---|
| `GET /` | Overview |
| `GET /downloads` | merged queue + cleaner log |
| `GET /library/{app}` | per-arr page; `/library/{app}/{id}` detail (episodes/tracks/books fetched on demand) |
| `GET /requests` | Jellyseerr pending + search (`?q=`) |
| `GET /rules`, `/rules/new`, `/rules/{id}`, `/rules/{id}/runs/{run}` | |
| `GET /health` | indexers, subtitles, versions, Weir's own poll table |
| `GET /events` | SSE: `speeds` every 2 s, `queue` on change, `sessions` every 15 s |
| `GET /img/{app}/poster/{id}` | poster proxy, in-memory LRU capped at 32 MB, 24 h TTL, lost on restart; nothing on disk |
| `GET /healthz`, `GET /metrics` | |

Writes are `POST`, require header `X-Weir-Action: 1`, and are exactly:

| Route | Upstream call |
|---|---|
| `/downloads/{hash}/pause`, `/resume` | qBittorrent `torrents/stop`, `torrents/start` |
| `/downloads/{hash}/blocklist` | the arr queue DELETE described in §7, regardless of cleaner mode |
| `/library/{app}/search-missing` | `MissingMoviesSearch` / `MissingEpisodeSearch` / `MissingAlbumSearch` / `MissingBookSearch` |
| `/library/{app}/{id}/search` | per-item search command |
| `/requests` `{mediaType, mediaId}` | Jellyseerr request (tv: all seasons) |
| `/requests/{id}/approve`, `/decline` | Jellyseerr |
| `/library/jellyfin/scan` | `Library/Refresh` |
| `/rules` create, `/rules/{id}` update, `/rules/{id}/enable`, `/disable`, `/run` | store + engine |

Every write logs an `actions` row with the actor taken from the
`Tailscale-User-Login` header that `tailscale serve` adds (falls back to the
remote address). There is no generic proxy route and no delete route.

**Pages.** Left rail (bottom bar under 700 px) with a status dot per app.
Keys `1`–`9` switch pages, `/` focuses search. Charcoal palette, one accent,
system font stack, `prefers-color-scheme` respected. Installable: manifest
and icons. Overview and Downloads subscribe to `/events` and swap only
changed rows through HTMX's SSE extension; every other page is
request-rendered. The rule editor is the single vanilla-JS island (~300
lines): it manages the conditions list client-side and submits one form
(name, scope, conditions, optional tag).
"Open in app" links per page; no iframes.

## 11. Observability

`/metrics` exposes, at minimum:

- `weir_poll_duration_seconds{app,kind}`, `weir_poll_errors_total{app,kind}`,
  `weir_snapshot_age_seconds{app,kind}`, `weir_app_up{app}`
- `weir_cleaner_strikes{condition}`, `weir_cleaner_actions_total{mode,condition}`
- `weir_rule_matches{rule}`, `weir_rule_bytes{rule}`, `weir_rule_run_duration_seconds`
- `weir_sse_clients`
- Go runtime and process collectors.

Logs are `slog` JSON to stdout. One line per state transition, not per
poll. Docker's log driver and the existing journal collection handle the
rest.

## 12. Testing

- **App clients**: `httptest.Server` replaying fixtures from
  `testdata/fixtures/<app>/`, recorded from the real Pi apps and redacted
  (keys, hostnames, Jellyfin GUIDs). One test per endpoint used, asserting
  the parsed struct.
- **Cleaner and rules**: pure functions over a built snapshot and store
  state; table tests for every condition, the strike lifecycle, the
  per-title cap, the mode switch, and the "completed items are never
  touched" invariant, which is tested explicitly with a completed item in
  every failing state.
- **Store**: migrations apply on an empty file and are idempotent; typed
  queries round-trip.
- **Web**: handlers through the router with a fake snapshot; asserts
  status, a few key strings, JSON shape for `Accept: application/json`, and
  that every `POST` without `X-Weir-Action` is a 403.
- **Gate**: `go test ./...` plus `go vet` and `gofmt`. No browser tests, no
  e2e against the Pi in CI.

The implementation plan builds in slices so a useful binary reaches the Pi
early: (1) config, snapshot, poll runner, store, web shell; (2) Radarr,
Sonarr, qBittorrent, the downloads page and the cleaner in report mode,
deployed on 3006; (3) Lidarr, Readarr, Jellyfin, Jellyseerr, the overview
and requests pages; (4) the rule engine and editor; (5) Prowlarr, Bazarr,
the health page, metrics, cutover.

The first implementation task is a spike that records the fixtures against
the live Pi and confirms that Bookshelf answers the Readarr v1 calls Weir
needs (`system/status`, `queue`, `author`, `book`, `wanted/missing`,
`command`, `tag`). If it does not, the Readarr package narrows to what works
and the spec is amended.

## 13. Later

Separate specs when the time comes: the Omarchy bar card (`omarchy/`); a
per-rule `auto` action if the no-delete rule is ever relaxed by the operator
(the schema's `action` column leaves room, and nothing else does); Weir as a
Jellyseerr-less request path.
