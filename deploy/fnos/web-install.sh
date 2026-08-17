#!/usr/bin/env bash
# Install/upgrade a MiBee NVR .fpk on a fnOS NAS through the SAME web API the
# App Center UI uses (real-user manual-install path — no SSH involved).
#
# Discovered from the fnOS web client (AppStore-*.js) + nginx access log:
#   1. client-side taskID: app-upload-<rand>
#   2. GET  /app-center/v1/download/status?downloadTaskId=<id>   (init task)
#   3. POST /app-center/v1/download/upload   (multipart: taskID, file[, volumeID])
#   4. poll GET /app-center/v1/download/status until the package is parsed
#   5. GET  /app-center/v1/install/info | update/info?...&packageType=local (wizard)
#   6. POST /app-center/v1/install/task | update/task (systemParameters+wizard)
#   7. poll POST /app-center/v1/install/status | update/status until finished
#
# Usage:
#   ./web-install.sh <fpk-file> <nas-host> <fnos-token> [wizard-port]
#   FNOS_TOKEN can also be passed via env. The token is the fnos-token cookie
#   of a logged-in fnOS web session (admin).
set -euo pipefail

FPK="${1:?usage: web-install.sh <fpk> <host> <fnos-token> [wizard-port]}"
HOST="${2:?missing nas host}"
TOKEN="${3:-${FNOS_TOKEN:?missing fnos-token (cookie value or FNOS_TOKEN env)}}"
PORT="${4:-9090}"

BASE="http://${HOST}:5666"
AUTH="Cookie: fnos-token=${TOKEN}"

die() { echo "ERROR: $*" >&2; exit 1; }
command -v curl >/dev/null || die "curl required"
command -v python3 >/dev/null || die "python3 required (JSON field extraction)"

# jsonget <json> <python-expr over d> — extract a field from a JSON string.
jsonget() { printf '%s' "$1" | python3 -c "import sys,json; d=json.load(sys.stdin); print($2)" 2>/dev/null; }

api() { # api METHOD PATH [curl-extra...]
  local method="$1" path="$2"; shift 2
  curl -sS -X "$method" -H "$AUTH" -H 'Content-Type: application/json' "$BASE$path" "$@"
}

echo "== fnOS web-install: $FPK → $HOST =="
[ -f "$FPK" ] || die "fpk not found: $FPK"

# 1. client-side task id (mirrors the web client's app-upload-<rand>).
# NB: no `tr | head` pipelines here — under `set -o pipefail` the SIGPIPE from
# head killing tr aborts the whole script (exit 141).
TASK="app-upload-$(printf '%04x%04x%04x' $RANDOM $RANDOM $RANDOM)"

# 2/3. init task + upload the package (multipart; volumeID from sysconfig)
VOL=$(api GET "/app-center/v1/sysconfig?language=zh-CN" | python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["volumeID"])')
echo "== upload task=$TASK volume=$VOL =="
api GET "/app-center/v1/download/status?downloadTaskId=${TASK}&language=zh-CN" >/dev/null
UP=$(curl -sS -H "$AUTH" -F "taskID=${TASK}" -F "file=@${FPK}" \
  ${VOL:+-F "volumeID=${VOL}"} "$BASE/app-center/v1/download/upload")
echo "upload: $UP"

# 4. poll until the package is parsed. Observed status payload (2026-08-17):
# {"code":0,"data":{"status":2,"message":"success","progress":1,
#   "packageType":"local","packageSourceType":"upload",
#   "path":"/vol1/appcenter-downloads/<app>-<ver>-tpk",
#   "appName":"mibee-nvr","version":"0.12.0","name":"MiBee NVR",
#   "installed":true,"installedInfo":{...}}}
APP=""; VER=""
for i in $(seq 1 60); do
  ST=$(api GET "/app-center/v1/download/status?downloadTaskId=${TASK}&language=zh-CN")
  APP=$(jsonget "$ST" 'd["data"].get("appName","")' || echo "")
  VER=$(jsonget "$ST" 'd["data"].get("version","")' || echo "")
  [ -n "$APP" ] && break
  sleep 1
done
[ -n "$APP" ] || die "upload never parsed a package (last status: $ST)"
STATE=$(jsonget "$ST" 'd["data"].get("message","")' || echo "?")
echo "== package: $APP $VER (message=$STATE) =="

# 5/6. install (fresh) or update (already installed) — same fork the UI makes.
# TASK BODY IS DELIBERATELY MINIMAL: {appName, packageType:"local", version,
# language}. Adding systemParameters (installVolumeID/apiScope/...) makes the
# fnOS binder PANIC (server 10000, verified 2026-08-17) — the UI's S1 wrapper
# only injects those for wizard-driven installs; the backend then applies the
# existing install's volume + wizard values on its own.
INSTALLED=$(api GET "/app-center/v1/app/installed?language=zh-CN" \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(any(a['appName']=='$APP' for a in d['data']['list']))")

if [ "$INSTALLED" = "True" ]; then
  echo "== upgrading $APP → $VER =="
  api GET "/app-center/v1/update/info?appName=${APP}&updateVersion=${VER}&packageType=local&language=zh-CN" >/dev/null
  TASKRESP=$(api POST /app-center/v1/update/task -d \
    "{\"appName\":\"${APP}\",\"packageType\":\"local\",\"updateVersion\":\"${VER}\",\"language\":\"zh-CN\"}")
  STATUS_PATH=/app-center/v1/update/status
else
  echo "== installing $APP $VER =="
  api GET "/app-center/v1/install/info?appName=${APP}&version=${VER}&packageType=local&language=zh-CN" >/dev/null
  TASKRESP=$(api POST /app-center/v1/install/task -d \
    "{\"appName\":\"${APP}\",\"packageType\":\"local\",\"version\":\"${VER}\",\"language\":\"zh-CN\"}")
  STATUS_PATH=/app-center/v1/install/status
fi

TASKID=$(jsonget "$TASKRESP" 'd["data"]["taskId"]' || echo "")
[ -n "$TASKID" ] || die "no taskId from task creation: $TASKRESP"
echo "== install taskId=$TASKID =="

# 7. poll the task until it finishes. Observed status codes (2026-08-17):
# 1 = running, 2 = success, 5 = task closed after completion (the poller can
# first see 5 with progress 0 once the task record is recycled). We treat 2 as
# success, then cross-check the installed version via /app/installed.
for i in $(seq 1 180); do
  ST=$(api POST "$STATUS_PATH" -d "{\"taskId\":\"${TASKID}\"}")
  S=$(jsonget "$ST" '(d["data"] or {}).get("status")' || echo "?")
  case "$S" in
    2)
      FINAL=$(api GET "/app-center/v1/app/installed?language=zh-CN")
      GOT=$(printf '%s' "$FINAL" | python3 -c "import sys,json; d=json.load(sys.stdin); a=[x for x in d['data']['list'] if x['appName']=='$APP']; print(a[0]['version'] if a else 'ABSENT')")
      if [ "$GOT" = "$VER" ]; then echo "== OK: $APP $VER installed (task $TASKID)"; exit 0
      else die "task done but installed version is '$GOT' (want $VER)"; fi ;;
    5)
      # task closed — the cross-check decides
      FINAL=$(api GET "/app-center/v1/app/installed?language=zh-CN")
      GOT=$(printf '%s' "$FINAL" | python3 -c "import sys,json; d=json.load(sys.stdin); a=[x for x in d['data']['list'] if x['appName']=='$APP']; print(a[0]['version'] if a else 'ABSENT')")
      if [ "$GOT" = "$VER" ]; then echo "== OK: $APP $VER installed (task closed, verified via /app/installed)"; exit 0
      else die "task closed with version '$GOT' (want $VER): $ST"; fi ;;
    *fail*|*error*|*cancel*|6|7)
      die "task failed: $ST" ;;
  esac
  [ $((i % 5)) -eq 0 ] && echo "... status=$S progress=$(jsonget "$ST" '(d["data"] or {}).get("progress","?")' || echo '?')"
  sleep 2
done
die "timed out waiting for task $TASKID (last: $ST)"
