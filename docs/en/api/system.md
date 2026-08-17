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
  "device_name": "bananapim5",
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
