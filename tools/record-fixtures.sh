#!/usr/bin/env bash
# Record redacted API fixtures from the live Pi apps into testdata/fixtures/.
# Runs on the dev box; everything happens over one ssh session.
set -euo pipefail
cd "$(dirname "$0")/.."
out=testdata/fixtures
mkdir -p $out/{radarr,sonarr,qbit,readarr,lidarr,prowlarr,bazarr,jellyfin,jellyseerr}

ssh ratholepi 'bash -s' <<'REMOTE' > /tmp/weir-fixtures.tar
set -euo pipefail
set -a; . ~/ratholepi-stack/homepage/.env; set +a
RK=$(sudo sed -n "s:.*<ApiKey>\(.*\)</ApiKey>.*:\1:p" /srv/appdata/readarr/config.xml 2>/dev/null || true)
d=$(mktemp -d); mkdir -p $d/{radarr,sonarr,qbit,readarr,lidarr,prowlarr,bazarr,jellyfin,jellyseerr}
arr() { curl -sf -H "X-Api-Key: $2" "http://127.0.0.1:$1/api/$3"; }
for a in "radarr 7878 $HOMEPAGE_VAR_RADARR_KEY v3" "sonarr 8989 $HOMEPAGE_VAR_SONARR_KEY v3" "readarr 8788 ${RK:-none} v1" "lidarr 8686 $HOMEPAGE_VAR_LIDARR_KEY v1"; do
  set -- $a
  [ "$3" = none ] && { echo "no readarr key" >&2; continue; }
  arr $2 $3 "$4/system/status"  > $d/$1/system-status.json || echo "FAIL $1 status" >&2
  arr $2 $3 "$4/health"         > $d/$1/health.json || echo "FAIL $1 health" >&2
  arr $2 $3 "$4/diskspace"      > $d/$1/diskspace.json || echo "FAIL $1 diskspace" >&2
  arr $2 $3 "$4/queue?page=1&pageSize=1000&includeUnknownMovieItems=true&includeUnknownSeriesItems=true&includeUnknownAuthorItems=true" > $d/$1/queue.json || echo "FAIL $1 queue" >&2
  arr $2 $3 "$4/calendar?start=$(date -u +%F)&end=$(date -u -d '+7 days' +%F)" > $d/$1/calendar.json || echo "FAIL $1 calendar" >&2
  arr $2 $3 "$4/tag"            > $d/$1/tag.json || echo "FAIL $1 tag" >&2
done
arr 7878 $HOMEPAGE_VAR_RADARR_KEY v3/movie  > $d/radarr/movie.json
arr 8989 $HOMEPAGE_VAR_SONARR_KEY v3/series > $d/sonarr/series.json
if [ -n "$RK" ]; then
  arr 8788 $RK v1/author > $d/readarr/author.json || echo "FAIL readarr author" >&2
  arr 8788 $RK v1/book   > $d/readarr/book.json || echo "FAIL readarr book" >&2
  arr 8788 $RK "v1/wanted/missing?page=1&pageSize=10" > $d/readarr/wanted-missing.json || echo "FAIL readarr wanted" >&2
fi
arr 8686 $HOMEPAGE_VAR_LIDARR_KEY v1/artist > $d/lidarr/artist.json || echo "FAIL lidarr artist" >&2
arr 8686 $HOMEPAGE_VAR_LIDARR_KEY v1/album  > $d/lidarr/album.json || echo "FAIL lidarr album" >&2
arr 8686 $HOMEPAGE_VAR_LIDARR_KEY "v1/wanted/missing?page=1&pageSize=10" > $d/lidarr/wanted-missing.json || echo "FAIL lidarr wanted" >&2
P=$HOMEPAGE_VAR_PROWLARR_KEY
arr 9696 $P v1/system/status > $d/prowlarr/system-status.json || echo "FAIL prowlarr status" >&2
arr 9696 $P v1/health        > $d/prowlarr/health.json || echo "FAIL prowlarr health" >&2
arr 9696 $P v1/indexer       > $d/prowlarr/indexer.json || echo "FAIL prowlarr indexer" >&2
arr 9696 $P v1/indexerstats  > $d/prowlarr/indexerstats.json || echo "FAIL prowlarr stats" >&2
arr 9696 $P v1/applications  > $d/prowlarr/applications.json || echo "FAIL prowlarr apps" >&2
B=$HOMEPAGE_VAR_BAZARR_KEY
bz() { curl -sf -H "X-API-KEY: $B" "http://127.0.0.1:6767/api/$1"; }
bz system/status            > $d/bazarr/system-status.json || echo "FAIL bazarr status" >&2
bz "movies/wanted?start=0&length=10"   > $d/bazarr/movies-wanted.json || echo "FAIL bazarr mw" >&2
bz "episodes/wanted?start=0&length=10" > $d/bazarr/episodes-wanted.json || echo "FAIL bazarr ew" >&2
bz providers                > $d/bazarr/providers.json || echo "FAIL bazarr providers" >&2
bz "movies/history?start=0&length=10"  > $d/bazarr/movies-history.json || echo "FAIL bazarr mh" >&2
bz "episodes/history?start=0&length=10" > $d/bazarr/episodes-history.json || echo "FAIL bazarr eh" >&2
J=$HOMEPAGE_VAR_JELLYFIN_KEY
jf() { curl -sf -H "Authorization: MediaBrowser Token=\"$J\", Client=\"weir\", Device=\"weir\", DeviceId=\"weir\", Version=\"0\"" "http://127.0.0.1:8096/$1"; }
jf System/Info   > $d/jellyfin/system-info.json || echo "FAIL jf info" >&2
jf Sessions      > $d/jellyfin/sessions.json || echo "FAIL jf sessions" >&2
jf Items/Counts  > $d/jellyfin/counts.json || echo "FAIL jf counts" >&2
jf Users         > $d/jellyfin/users.json || echo "FAIL jf users" >&2
U=$(python3 -c "import json;print(json.load(open('$d/jellyfin/users.json'))[0]['Id'])" 2>/dev/null || true)
jf "Users/$U/Items?IncludeItemTypes=Movie,Series&Recursive=true&Fields=ProviderIds,DateCreated,UserData&Limit=50" > $d/jellyfin/user-items.json || echo "FAIL jf items" >&2
jf "Users/$U/Items/Latest?Limit=12&Fields=ProviderIds" > $d/jellyfin/latest.json || echo "FAIL jf latest" >&2
S=$HOMEPAGE_VAR_JELLYSEERR_KEY
js() { curl -sf -H "X-Api-Key: $S" "http://127.0.0.1:5055/api/v1/$1"; }
js status                                > $d/jellyseerr/status.json || echo "FAIL js status" >&2
js "request?take=20&filter=all&sort=added" > $d/jellyseerr/requests.json || echo "FAIL js requests" >&2
js "request?take=20&filter=pending"      > $d/jellyseerr/requests-pending.json || echo "FAIL js pending" >&2
js request/count                         > $d/jellyseerr/count.json || echo "FAIL js count" >&2
js "user?take=20"                        > $d/jellyseerr/users.json || echo "FAIL js users" >&2
js "search?query=sneakers&page=1"        > $d/jellyseerr/search.json || echo "FAIL js search" >&2
j=$(mktemp)
curl -sf -c $j --data-urlencode "username=$HOMEPAGE_VAR_QBIT_USER" --data-urlencode "password=$HOMEPAGE_VAR_QBIT_PASS" http://127.0.0.1:8080/api/v2/auth/login >/dev/null
curl -sf -b $j "http://127.0.0.1:8080/api/v2/sync/maindata?rid=0" > $d/qbit/maindata-full.json
sleep 2
rid=$(python3 -c "import json;print(json.load(open('$d/qbit/maindata-full.json'))['rid'])")
curl -sf -b $j "http://127.0.0.1:8080/api/v2/sync/maindata?rid=$rid" > $d/qbit/maindata-delta.json
h=$(python3 -c "import json;t=json.load(open('$d/qbit/maindata-full.json')).get('torrents',{});print(next(iter(t)) if t else '')")
if [ -n "$h" ]; then curl -sf -b $j "http://127.0.0.1:8080/api/v2/torrents/trackers?hash=$h" > $d/qbit/trackers.json; else echo '[]' > $d/qbit/trackers.json; fi
curl -sf -b $j http://127.0.0.1:8080/api/v2/app/version > $d/qbit/version.txt
tar -C $d -cf - .
REMOTE

tar -C $out -xf /tmp/weir-fixtures.tar
rm /tmp/weir-fixtures.tar
find $out -type f -name '*.json' -exec sed -i -E \
  -e 's/\b[0-9a-f]{32}\b/REDACTED_KEY/g' \
  -e 's/ratholepi\.tail[a-z0-9]+\.ts\.net/HOST/g' \
  -e 's/192\.168\.[0-9]+\.[0-9]+/10.0.0.1/g' \
  -e 's/"(apiKey|password|passwordConfirmation|Password|AccessToken)": *"[^"]*"/"\1":"REDACTED"/g' \
  -e 's/"(Id|UserId|ServerId|DeviceId|jellyfinUserId|plexId)": *"[0-9a-f]{32}"/"\1":"00000000000000000000000000000000"/g' \
  -e 's/"(email|Email)": *"[^"]*"/"\1":"user@example.invalid"/g' \
  -e 's/"Key": *"[0-9a-f-]{36}"/"Key":"00000000-0000-0000-0000-000000000000"/g' {} +
echo "recorded:"; find $out -type f | sort | xargs wc -c | tail -n +1
# Jellyfin items carry blurhashes keyed by image tags; they trip secret scanners and mean nothing to Weir.
python3 - "$out" <<'PY'
import json,sys,glob
def strip(o):
    if isinstance(o,dict):
        for k in ("ImageBlurHashes","ImageTags","BackdropImageTags","ParentBackdropImageTags"): o.pop(k,None)
        for v in o.values(): strip(v)
    elif isinstance(o,list):
        for v in o: strip(v)
for f in glob.glob(sys.argv[1]+"/jellyfin/*.json"):
    d=json.load(open(f)); strip(d); json.dump(d,open(f,"w"))
PY
