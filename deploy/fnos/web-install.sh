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

# 1. client-side task id (mirrors the web client's app-upload-<rand>)
TASK="app-upload-$(tr -dc a-z0-9 </dev/urandom | head -c 8)"

# 2/3. init task + upload the package (multipart; volumeID from sysconfig)
VOL=$(api GET "/app-center/v1/sysconfig?language=zh-CN" | python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["volumeID"])')
echo "== upload task=$TASK volume=$VOL =="
api GET "/app-center/v1/download/status?downloadTaskId=${TASK}&language=zh-CN" >/dev/null
UP=$(curl -sS -H "$AUTH" -F "taskID=${TASK}" -F "file=@${FPK}" \
  ${VOL:+-F "volumeID=${VOL}"} "$BASE/app-center/v1/download/upload")
echo "upload: $UP"

# 4. poll until the package is parsed (status carries app name/version)
APP=""; VER=""
for i in $(seq 1 60); do
  ST=$(api GET "/app-center/v1/download/status?downloadTaskId=${TASK}&language=zh-CN")
  STATE=$(jsonget "$ST" 'd["data"].get("state","")' || echo "")
  APP=$(jsonget "$ST" '(d["data"].get("app") or {}).get("appName","")' || echo "")
  VER=$(jsonget "$ST" '(d["data"].get("app") or {}).get("version","")' || echo "")
  [ -n "$APP" ] && break
  sleep 1
done
[ -n "$APP" ] || die "upload never parsed a package (last status: $ST)"
echo "== package: $APP $VER (state=$STATE) =="

# 5/6. install (fresh) or update (already installed) — same fork the UI makes
INSTALLED=$(api GET "/app-center/v1/app/installed?language=zh-CN" \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(any(a['appName']=='$APP' for a in d['data']['list']))")
SYS_PARAMS="{\"installVolumeID\":${VOL:-1},\"agreedToProtocol\":true,\"immediateStart\":true}"
WIZARD_PARAMS="{\"wizard_port\":\"${PORT}\"}"

if [ "$INSTALLED" = "True" ]; then
  echo "== upgrading $APP → $VER =="
  api GET "/app-center/v1/update/info?appName=${APP}&updateVersion=${VER}&packageType=local&language=zh-CN" >/dev/null
  TASKRESP=$(api POST /app-center/v1/update/task -d \
    "{\"appName\":\"${APP}\",\"packageType\":\"local\",\"updateVersion\":\"${VER}\",\"systemParameters\":${SYS_PARAMS},\"customParameters\":${WIZARD_PARAMS}}")
  STATUS_PATH=/app-center/v1/update/status
else
  echo "== installing $APP $VER =="
  api GET "/app-center/v1/install/info?appName=${APP}&version=${VER}&packageType=local&language=zh-CN" >/dev/null
  TASKRESP=$(api POST /app-center/v1/install/task -d \
    "{\"appName\":\"${APP}\",\"packageType\":\"local\",\"version\":\"${VER}\",\"systemParameters\":${SYS_PARAMS},\"customParameters\":${WIZARD_PARAMS}}")
  STATUS_PATH=/app-center/v1/install/status
fi

TASKID=$(jsonget "$TASKRESP" 'd["data"]["taskId"]' || echo "")
[ -n "$TASKID" ] || die "no taskId from task creation: $TASKRESP"
echo "== install taskId=$TASKID =="

# 7. poll the task until it finishes
for i in $(seq 1 180); do
  ST=$(api POST "$STATUS_PATH" -d "{\"taskId\":\"${TASKID}\"}")
  STATE=$(jsonget "$ST" '(d["data"] or {}).get("status", d["data"])' || echo "?")
  case "$STATE" in
    *finish*|*success*|*complete*|*done*) echo "== OK: $ST"; exit 0 ;;
    *fail*|*error*|*cancel*)              die "task failed: $ST" ;;
  esac
  [ $((i % 5)) -eq 0 ] && echo "... $STATE"
  sleep 2
done
die "timed out waiting for task $TASKID (last: $ST)"
