# Weir

A low dam across the media pipeline on ratholepi: one small Go binary that
watches Radarr, Sonarr, Lidarr, Bookshelf (Readarr), Prowlarr, qBittorrent,
Bazarr, Jellyfin and Jellyseerr, shows them on one tailnet-only page, and
runs two pieces of automation:

- a **queue cleaner** that removes downloads that will never finish
  (stalled, slow, dead tracker, orphaned) through the owning arr, with a
  blocklist and a fresh search, and
- **library rules** that tag and list media matching conditions such as
  "watched by everyone and untouched for 90 days".

**Weir never deletes anything that has been downloaded.** There is no delete
route and no delete code path for library media. The only removal it ever
performs is the cleaner taking out a download that has not completed, and the
cleaner ships in `report` mode until you flip it.

Design and every decision behind it: `docs/superpowers/specs/2026-09-06-weir-design.md`.

## Run

```
go build ./cmd/weir && WEIR_RADARR_URL=http://radarr:7878 WEIR_RADARR_KEY=… ./weir
```

Configuration is environment only; `.env.example` lists every variable with
1Password references for the ratholepi deployment. Image:
`ghcr.io/riverr4t/weir` (amd64 and arm64, from `scratch`, ~25 MB).
`weir healthcheck` is the Docker health probe.

| Variable | Default | Meaning |
|---|---|---|
| `WEIR_LISTEN` | `:3004` | bind address |
| `WEIR_DATA_DIR` | `/config` | holds `weir.db` |
| `WEIR_MEDIA_DIR` | `/data` | read-only media mount for the orphan-file rule |
| `WEIR_PUBLIC_URL` | tailnet URL | used in ntfy click links |
| `WEIR_<APP>_URL`, `WEIR_<APP>_KEY` | | `RADARR SONARR LIDARR READARR PROWLARR BAZARR JELLYFIN JELLYSEERR`; unset URL disables the app |
| `WEIR_QBIT_URL`, `WEIR_QBIT_USER`, `WEIR_QBIT_PASS` | | qBittorrent |
| `WEIR_<APP>_PUBLIC_URL` | | "Open in app" link |
| `WEIR_NTFY_URL`, `WEIR_NTFY_TOPIC` | | notifications; unset disables |
| `WEIR_CLEANER_MODE` | `report` | `off`, `report`, `act` |
| `WEIR_CLEANER_INTERVAL` | `5m` | |
| `WEIR_CLEANER_STALL_AFTER` | `30m` | no progress for this long = stalled |
| `WEIR_CLEANER_SLOW_BELOW` | `50KiB` | speed floor |
| `WEIR_CLEANER_SLOW_FOR` | `20m` | time below the floor before a strike |
| `WEIR_CLEANER_MAX_ETA` | `48h` | ETA above this while slow = strike |
| `WEIR_CLEANER_STRIKES` | `3` | strikes before action |
| `WEIR_CLEANER_MAX_ACTIONS_PER_TITLE` | `3` | per movie/episode/album/book per 24 h |
| `WEIR_RULES_AT` | `04:10` | daily rule run, local time |
| `WEIR_LOG_LEVEL` | `info` | |

## Develop

`go test ./...` is the whole gate. App clients are tested against recorded,
redacted fixtures in `testdata/fixtures/` (`tools/record-fixtures.sh`
refreshes them from the Pi). `/metrics` is Prometheus; `/healthz` is liveness.
