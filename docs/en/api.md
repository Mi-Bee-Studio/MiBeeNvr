# API Overview

> For MiBeeNvr v0.11.0 · full endpoint docs live in the repo at [docs/en/api/](https://github.com/Mi-Bee-Studio/MiBeeNvr/tree/main/docs/en/api)

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
| `GET /api/events` | **SSE event stream** (rate-limited to 60/min, see below) |
| `GET /api/recordings/{id}/download` | Recording download (Range supported for player seeking) |
| `GET /api/recordings/{id}/merged` | Merged outputs such as timelapses |
| `GET /models/{filename}` | Browser-side AI model files |

## Core Endpoint Groups

| Group | Endpoints | Notes |
|-------|-----------|-------|
| Cameras | `GET/POST /api/cameras`, `GET/PUT/DELETE /api/cameras/{id}` | camera CRUD |
| Live streams | `GET /api/cameras/{id}/stream.flv`, HLS / WebRTC / MJPEG endpoints | pull streams (FLV needs BasicAuth) |
| Recordings | `GET /api/recordings` | list / filter / paginate |
| Playback | `GET /api/cameras/{id}/playback/playlist.m3u8` | per-recording playback |
| AI events | `POST /api/ai/events`, `GET /api/ai/events`, `GET /api/ai/stats` | write from external AI backends (Bearer) and query stats |
| Settings | `GET/PUT /api/settings`, `POST /api/settings/api-keys` | runtime config and keys |
| Storage | `GET /api/storage` (incl. `candidates`) | storage stats and available volumes |
| GB28181 | `/api/gb28181/*` | devices / channels / PTZ / playback |
| System | `GET /api/version`, `GET /api/capabilities`, `GET /api/stats` | version / capabilities / stats |

## SSE Event Stream

`GET /api/events` streams the internal event bus over Server-Sent Events; the `filter` query parameter narrows by topic prefix:

```bash
# subscribe only to segment-completed events (typical external AI backend usage)
curl -N "http://localhost:9090/api/events?filter=segment."
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
