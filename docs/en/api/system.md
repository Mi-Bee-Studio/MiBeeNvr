# Health & System API

## Health Check

**Endpoint:** `GET /api/health`

Get overall system health status including database and storage disk space.

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
    "storage": {
      "status": "ok", 
      "message": ""
    }
  },
  "uptime": "2h34m15s"
}
```

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
