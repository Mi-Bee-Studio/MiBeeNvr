# API Overview

> For MiBeeNvr v0.13.0 · full endpoint docs live in the repo at [docs/en/api/](https://github.com/Mi-Bee-Studio/MiBeeNvr/tree/main/docs/en/api)

Everything MiBee NVR does is drivable over its REST API (the web UI itself is a consumer of it), plus an SSE event stream. This page is an auth + core-endpoint cheat sheet.

## Authentication

| Method | Good for | Usage |
|--------|----------|-------|
| **BasicAuth** | admin scripts / curl | `curl -u admin:password ...` |
| **API Key** (`mbv_` prefix) | external services / per-device tokens | `Authorization: Bearer mbv_...` (for SSE/WebSocket clients that cannot set headers, use the `?api_key=` query parameter) |
| Session (`mbs_`) | browsers | issued by `POST /api/auth/login` |
| Gateway session | fnOS desktop integration | `GET /api/auth/gateway-session` exchanges the fnOS unified-gateway identity for a session |

Order: public routes → Bearer API key → BasicAuth → everything returns `503 SETUP_REQUIRED` until a password is configured.

**API key management**: Settings → AI Detection → MiBeeVision integration (or `POST /api/settings/api-keys`). Keys are shown once at creation, can be revoked individually, and **generation/revocation takes effect immediately** — no restart.

```bash
# create a key
curl -u admin:password -X POST -H "Content-Type: application/json" \
  -d '{"name": "my-integration"}' \
  http://localhost:9090/api/settings/api-keys

# use the key
curl -H "Authorization: Bearer mbv_xxx" http://localhost:9090/api/recordings
```

## Public Endpoints (no auth)

| Endpoint | Description |
|----------|-------------|
| `GET /api/health` | Health summary (storage / camera states) |
| `GET /api/readyz` | Readiness probe |
| `GET /api/capabilities` | Ingest capabilities (RTMP / SRT ports) |
| `POST /api/trigger/webhook/{camera_id}` | Webhook trigger (public rate-limited group; the HMAC signature is the credential — see below) |
| `GET /models/{filename}` | Browser-side AI model files |

> As of v0.13, recording downloads (`/api/recordings/{id}/download`, `/api/recordings/{id}/merged`, timelapse merge download), playback, `GET /api/events`, and `GET /api/health/cameras` moved from anonymous into the authenticated group (browsers carry credentials transparently via the `?token=` session token or cookie); the legacy base64 `?token=` passthrough was removed. See [Authentication · v0.13 auth scope changes](api/authentication.md).

## Core Endpoint Groups

| Group | Endpoints | Notes |
|-------|-----------|-------|
| Cameras | `GET/POST /api/cameras`, `GET/PUT/DELETE /api/cameras/{id}`, `POST /api/cameras/{id}/adaptive/trigger`, `PUT/GET /api/cameras/{id}/storage-root` | camera CRUD, adaptive-recording external trigger, per-camera storage root |
| Live streams | `GET /api/cameras/{id}/stream.flv`, HLS / WebRTC / MJPEG endpoints | pull streams (FLV needs BasicAuth) |
| Recordings | `GET /api/recordings` | list / filter / paginate |
| Playback | `GET /api/cameras/{id}/playback/playlist.m3u8` | per-recording playback |
| AI events | `POST /api/ai/events`, `GET /api/ai/events`, `GET /api/ai/events/{id}`, `GET /api/ai/events/{id}/snapshot` | write from external AI backends (Bearer), query stats; event snapshot images |
| Triggers | `POST /api/trigger/webhook/{camera_id}` | HMAC-signed webhook trigger for record / stop / snapshot (full docs: [Webhook Integration](webhook-integration.md)) |
| Settings | `GET/PUT /api/settings`, `POST /api/settings/api-keys` | runtime config and keys |
| Storage | `GET /api/storage`, `GET/POST/DELETE /api/storage/candidates`, `POST /api/storage/migrate` | storage stats, candidate volumes, batch migration ([Storage Management](storage-management.md)) |
| GB28181 | `/api/gb28181/*` | devices / channels / PTZ / playback |
| System | `GET /api/version`, `GET /api/capabilities`, `GET /api/stats` | version / capabilities / stats |

## AI Event Snapshots

`GET /api/ai/events/{id}/snapshot` returns the event's snapshot JPEG (the
`snapshot_path` reported with the event by the external AI backend, relative to
the NVR storage root — sidecar deployments write into
`<storage root>/ai-snapshots/`):

```bash
curl -o snap.jpg "http://localhost:9090/api/ai/events/7141/snapshot?api_key=mbv_…"
```

- Auth matches the other AI query endpoints (BasicAuth session / Bearer API key /
  `?api_key=`)
- **Every "no image available" case is a plain 404** (no `snapshot_path`, file
  missing, unknown event) so clients can fall back to a placeholder without
  parsing the error body; an invalid id returns 400
- Responses carry `Cache-Control: private, max-age=86400` (snapshots are
  immutable) and support Range and conditional requests
- Paths resolve inside the storage root; traversal or outside-root paths are
  rejected with 404 — files outside the storage tree are never served

Remote external AI backends that cannot write the NVR storage tree directly
POST the event first, then upload the snapshot JPEG bytes to the same resource
path (`Content-Type: image/jpeg`, API-key auth):

```bash
curl -X POST -H "Authorization: Bearer mbv_…" -H "Content-Type: image/jpeg"   --data-binary @snap.jpg "http://localhost:9090/api/ai/events/7141/snapshot"
```

- 4MB cap; non-JPEG bodies return 400; the file lands in
  `<storage root>/ai-snapshots/` (atomic write) and the event's
  `snapshot_path` is backfilled — GET serves it immediately afterwards
- Sidecar (same-host) deployments need no upload — write the
  `ai-snapshots/` subtree directly and reference the relative path in the
  event; both shapes converge on the same GET endpoint


## SSE Event Stream

`GET /api/events` streams the internal event bus over Server-Sent Events; the `filter` query parameter narrows by topic prefix. As of v0.13 it requires auth (BasicAuth / session token / API key; browser EventSource clients pass `?token=` or `?api_key=`):

```bash
# subscribe only to segment-completed events (typical external AI backend usage)
curl -N "http://localhost:9090/api/events?filter=segment.&api_key=mbv_…"
```

The payload is nested — business fields live inside `Data` (including `recording_id`):

```text
data: {"Topic":"segment.completed","Data":{ ... }}
```

> Events are also available via [MQTT](mqtt-integration.md) for home-automation integrations.

## Full Documentation

Request/response fields and error codes per group live in the repo:

**[docs/en/api/](https://github.com/Mi-Bee-Studio/MiBeeNvr/tree/main/docs/en/api)** — authentication · cameras · recordings · streaming · AI detection · events · settings · GB28181 · backup …

## Next Steps

- [MQTT Integration](mqtt-integration.md) — events for home automation
- [WebDAV / FTP](webdav-ftp.md) — file-level access to recordings
- [CLI Reference](cli.md) — command-line administration
