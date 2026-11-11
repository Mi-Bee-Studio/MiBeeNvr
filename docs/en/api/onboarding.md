# Onboarding API

Zero-touch camera onboarding: discover cameras sitting on a factory-default subnet and move them onto the NVR's subnet for enrollment. Concept and walkthrough: [Zero-Touch Camera Onboarding](../onboarding.md).

All endpoints require authentication and answer **`409 Conflict`** while the feature is disabled (`camera_onboarding.enabled: false`, the default).

## Status

**Endpoint:** `GET /api/onboarding/status`

Feature state and the last scan outcome.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/onboarding/status"
```

**Response:**
```json
{
  "enabled": true,
  "subnet": "192.168.1.0/24",
  "alias_ip": "192.168.1.254",
  "alias_active": false,
  "last_scan": "2026-11-11T08:30:00Z",
  "last_scan_found": 2
}
```

| Field | Description |
|-------|-------------|
| `enabled` | Whether the feature is on |
| `subnet` | Configured `default_subnet` |
| `alias_ip` | Resolved alias address (explicit or the subnet's last host address) |
| `alias_active` | Whether the alias is bound right now (during a scan/adopt) |
| `last_scan` | RFC3339 timestamp of the last finished scan, when one exists |
| `last_scan_found` | Candidate count of that scan |

## Scan

**Endpoint:** `POST /api/onboarding/scan`

Starts a background sweep of the default subnet (port 80, /24 max, 16 parallel probes) and returns a job id.

**Request:**
```bash
curl -u username:password -X POST \
  "http://localhost:9090/api/onboarding/scan"
```

**Response — `202 Accepted`:**
```json
{
  "job_id": "onb-a1b2c3d4"
}
```

Poll the job via [Jobs](#jobs). A scan that answers `409 Conflict` with "one operation at a time" means a scan/adopt is still running — wait for it to finish.

## Adopt

**Endpoint:** `POST /api/onboarding/adopt`

Guided enrollment: rewrites the camera's network settings (ONVIF first, Hikvision ISAPI fallback in static mode), relocates it at the new address by serial, and enrolls it.

**Request:**
```bash
curl -u username:password -X POST \
  -H "Content-Type: application/json" \
  -d '{"ip":"192.168.1.64","username":"admin","password":"secret","mode":"static","static_ip":"192.168.10.84"}' \
  "http://localhost:9090/api/onboarding/adopt"
```

**Body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ip` | string | yes | Camera's current address on the default subnet |
| `username` / `password` | string | yes | Device credentials (post-activation) |
| `mode` | string | no | `"dhcp"` or `"static"`; empty = follow the configured target |
| `static_ip` | string | no | Static target override; must be a valid IPv4 in static mode |
| `prefix_len` | int | no | Prefix length override for the static target |

**Response — `202 Accepted`:**
```json
{
  "job_id": "onb-e5f6a7b8"
}
```

**Errors:** `400` missing `ip` or malformed body; `409` feature disabled or another scan/adopt is running.

## Jobs

**Endpoint:** `GET /api/onboarding/jobs/{id}`

Job status and result. Scan jobs carry candidates; adopt jobs carry the enrollment result.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/onboarding/jobs/onb-a1b2c3d4"
```

**Response — `200 OK` (finished scan):**
```json
{
  "id": "onb-a1b2c3d4",
  "kind": "scan",
  "state": "done",
  "started_at": "2026-11-11T08:30:00Z",
  "candidates": [
    {
      "ip": "192.168.1.64",
      "state": "auth_required",
      "serial": "DS-2CD2225Y-YN...",
      "manufacturer": "Hikvision",
      "model": "DS-2CD2225Y-YN",
      "device_name": "IP Camera"
    }
  ]
}
```

**Response — `200 OK` (finished adopt):**
```json
{
  "id": "onb-e5f6a7b8",
  "kind": "adopt",
  "state": "done",
  "started_at": "2026-11-11T08:32:10Z",
  "adopt": {
    "serial": "DS-2CD2225Y-YN...",
    "new_ip": "192.168.10.84",
    "camera_id": "cam-ab12cd34",
    "reboot_needed": false
  }
}
```

**Job model:**

| Field | Description |
|-------|-------------|
| `kind` | `"scan"` or `"adopt"` |
| `state` | `running` / `done` / `error` |
| `error` | Set on `state: "error"` |
| `candidates` | Scan result, ordered by IP; empty on adopt jobs |
| `adopt` | Enrollment result on adopt jobs |

Candidate `state` values: `unactivated` (factory-fresh, must be activated before adoption), `activated` (readable without credentials), `auth_required` (credentials required). See the guide for handling each.

**Errors:** `404` unknown job id; finished jobs are pruned oldest-first once the registry holds 64.

## See Also

- [Zero-Touch Camera Onboarding guide](../onboarding.md)
- [Camera API](cameras.md) — the enrolled camera is a regular camera afterwards
- [ONVIF API](onvif.md) — per-camera ONVIF operations
