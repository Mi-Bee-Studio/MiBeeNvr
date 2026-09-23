# Health & System API

## Health Check

**Endpoint:** `GET /api/health`

Get overall system health status including database and storage disk space. Public endpoint (no auth).

`device_id` is a stable UUID generated on first start and persisted (`server.device_id`);
`device_name` defaults to the hostname — LAN clients can anchor on the identity instead of
a changeable IP address.

**Request:**
```bash
curl http://localhost:9090/api/health
```

**Response:**
```json
{
  "status": "ok",
  "checks": {
    "database": {
      "status": "ok",
      "message": ""
    },
    "goroutines": {
      "status": "ok",
      "message": "167 goroutines"
    },
    "storage": {
      "status": "ok",
      "message": "43% used (1272250408960 / 2953130397696 bytes)"
    }
  },
  "uptime": "2h34m15s",
  "setup_required": false,
  "device_id": "371da2dc-7804-4706-b424-ce50d14ce2d2",
  "device_name": "mibee-nvr",
  "cameras": {
    "total": 13,
    "recording": 10,
    "reconnecting": 0,
    "error": 3,
    "offline": 0,
    "details": [
      { "id": "front-door", "name": "Front Door", "status": "healthy", "score": 75 }
    ]
  }
}
```

`setup_required: true` means the NVR is not yet initialized (no admin password); guide the
user through the setup wizard.

## Readiness Check

**Endpoint:** `GET /api/readyz`

Check if the system is ready to accept requests (same as health check).

**Request:**
```bash
curl http://localhost:9090/api/readyz
```

**Response:**
```json
{
  "status": "ok"
}
```

## System Stats

**Endpoint:** `GET /api/stats/system`

Get detailed system statistics including CPU, memory, and network usage.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats/system"
```

**Response:**
```json
{
  "cpu": {
    "total": 1234567,
    "idle": 987654
  },
  "memory": {
    "total": 1073741824,
    "available": 536870912,
    "process_rss": 10485760
  },
  "network": {
    "bytes_sent": 1048576,
    "bytes_recv": 2097152
  },
  "uptime": "2h34m15s",
  "timestamp": 1716789012
}
```

## One-Click Upgrade (bare metal)

The human entry point to the upgrade pipeline: the app process itself cannot replace its binary (sandboxed), so `POST /apply` performs the same handoff as the automatic hook — write a request file, start the polkit-authorized root helper unit (`mibee-nvr-update.service`). Cross-restart progress comes from the lifecycle files the helper persists in the data directory. See [Auto-Update](../deployment-autoupdate.md) for the full picture.

### Trigger an Upgrade

**Endpoint:** `POST /api/update/apply`

Trigger a bare-metal upgrade to the latest version reported by `GET /api/update/check`. Idempotent: while a triggered apply is still pending or running, the endpoint returns its current status instead of stacking another request.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/update/apply"
```

**Response (200 OK):**
```json
{
  "id": "apply-18f2a4c1b3d9e701",
  "state": "requested",
  "from": "v0.12.3",
  "to": "v0.13.0"
}
```

**Response (409 Conflict, Docker deployment):**
```json
{
  "error": "auto-upgrade is not available for Docker deployments — the container is immutable",
  "deployment": "docker",
  "guidance": "use Watchtower or `docker compose pull && docker compose up -d` (see docs: deployment-autoupdate)"
}
```

**Response (409 Conflict, no update available):**
```json
{
  "error": "no update available",
  "state": "idle"
}
```

> A failed request-file write or helper start returns 500 (with a hint: check the polkit rule installed by `install.sh`; fallback `sudo mibee-nvr update`).

### Upgrade Status

**Endpoint:** `GET /api/update/apply/status`

The cross-restart apply state synthesized from the lifecycle files and helper-unit liveness. `state` values:

| State | Meaning |
|-------|---------|
| `applying` | the helper unit is running right now |
| `requested` | request file present, helper not (yet) running |
| `success` | last upgrade succeeded (terminal) |
| `failed_rolled_back` | last upgrade failed, rolled back to the previous version (terminal) |
| `failed` | last upgrade failed (terminal) |
| `idle` | nothing ever happened |

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/update/apply/status"
```

**Response:**
```json
{
  "state": "success",
  "auto_apply": false,
  "id": "apply-18f2a4c1b3d9e701",
  "from": "v0.12.3",
  "to": "v0.13.0",
  "time": "2026-09-20T03:12:45Z"
}
```

`auto_apply` mirrors the `update.auto_apply` config (when `true` the sensing layer applies new releases automatically, no human trigger needed). `id` / `from` / `to` / `error` / `time` appear only after at least one apply attempt; an empty `error` means success.

### Upgrade History

**Endpoint:** `GET /api/update/history`

The 10 most recent upgrade rows, newest first.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/update/history"
```

**Response:**
```json
[
  {
    "time": "2026-09-20T03:12:45Z",
    "from": "v0.12.3",
    "to": "v0.13.0",
    "result": "ok"
  },
  {
    "time": "2026-08-02T22:41:10Z",
    "from": "v0.12.1",
    "to": "v0.12.2",
    "result": "failed",
    "error": "health gate: /api/readyz timeout"
  }
]
```

`result`: `ok` | `failed` (`failed` rows carry the `error` reason).

## System Management (loopback only)

The two endpoints below serve the desktop builds (the Windows tray / macOS menu-bar helper is a separate process driving the local NVR through them). Both **accept loopback requests from the NVR's own machine only**: a loopback connection with a loopback / `localhost` Host header and no proxy headers — whoever sits at the machine could equally Ctrl+C the process or edit the config file; remote callers always get 403.

### Graceful Shutdown

**Endpoint:** `POST /api/system/shutdown`

Trigger the same graceful shutdown path as SIGINT/SIGTERM. The response is served first; the stop fires in the background.

**Request:**
```bash
curl -X POST "http://127.0.0.1:9090/api/system/shutdown"
```

**Response:**
```json
{
  "status": "shutting down"
}
```

**Response (403 Forbidden, remote request):**
```json
{
  "error": "shutdown is only available from a local session on the NVR machine"
}
```

### Change the Listen Address at Runtime

**Endpoint:** `PUT /api/system/listen`

Rebind the HTTP listener at runtime (e.g. `127.0.0.1:9090` → `0.0.0.0:9090` to open LAN access): bind the new address first, persist the config, then drain the old server — no restart involved.

**Request Body:**
```json
{"listen": "0.0.0.0:9090"}
```

| Field | Type | Description |
|-------|------|-------------|
| `listen` | string | Listen address in `[host:]port` form (e.g. `127.0.0.1:9090`, `0.0.0.0:9090`, `:9090`, `[::1]:9090`), port 1–65535 |

**Request:**
```bash
curl -X PUT \
  -H "Content-Type: application/json" \
  -d '{"listen": "0.0.0.0:9090"}' \
  "http://127.0.0.1:9090/api/system/listen"
```

**Response (202 Accepted):**
```json
{
  "status": "applying",
  "listen": "0.0.0.0:9090"
}
```

> The rebind runs in the background after the response is flushed; a failed swap is only logged server-side and the current listener keeps serving. A malformed address returns 400; remote requests return 403.
