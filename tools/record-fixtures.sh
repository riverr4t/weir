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
RK=$(sudo sed -n "s:.*<ApiKey>\(.*\)</ApiKey>.*:\1:p" /srv/appdata/readarr/config.xml 2>/dev/null || true)
d=$(mktemp -d); mkdir -p $d/{radarr,sonarr,qbit,readarr}
arr() { curl -sf -H "X-Api-Key: $2" "http://127.0.0.1:$1/api/$3"; }
for a in "radarr 7878 $HOMEPAGE_VAR_RADARR_KEY v3" "sonarr 8989 $HOMEPAGE_VAR_SONARR_KEY v3" "readarr 8788 ${RK:-none} v1"; do
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
  -e 's/"(apiKey|password|passwordConfirmation)": *"[^"]*"/"\1":"REDACTED"/g' {} +
echo "recorded:"; find $out -type f | sort | xargs wc -c | tail -n +1
