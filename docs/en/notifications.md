# Alert Notifications (Webhook Forwarding)

MiBee NVR can forward its alerts — camera offline/frozen/recovered, storage health transitions, motion triggers, AI detection events, doorbell rings, GB28181 device alarms, camera registration — to any external webhook receiver: self-hosted alerting hubs, chat robots (WeCom / DingTalk / Feishu / Telegram / Slack bots), or monitoring platforms.

The NVR only reports facts. Deduplication, aggregation, cooldowns, escalation and channel routing belong to the receiver — this keeps the forwarding path simple and lossy-safe: a dead receiver can never block recording or any other NVR function.

## Overview

- **Transport**: HTTP `POST` per alert, JSON body (`Content-Type: application/json`)
- **Auth**: the receiver's path token (part of the configured URL), encrypted at rest
- **Delivery semantics**: bounded per-receiver queue (drops the oldest alert on overflow), bounded retries for transient failures (5s / 30s / 2m), 4xx never retried, per-minute burst cap (default 30)
- **Restart to apply**: the dispatcher is built at startup

## The Alert Envelope (schema `mibee-alert/1`)

```json
{
  "schema": "mibee-alert/1",
  "event_id": "0d7f0f9e-6f5c-4d8e-9a3f-7e5b1c2d3a4b",
  "occurred_at": "2026-10-25T04:05:06.000000007Z",
  "type": "camera.connection",
  "severity": "error",
  "title": "Camera offline: Studio",
  "body": "No frames for 30 seconds",
  "source_ref": "cam-fa049182",
  "dedup_key": "mibee-nvr/connection/cam-fa049182",
  "resolved": false,
  "snapshot_url": "http://192.168.1.10:9090/api/ai/events/42/snapshot?api_key=mbv_...",
  "tags": { "camera_name": "Studio" }
}
```

| Field | Meaning |
|-------|---------|
| `schema` | Envelope version marker. Unknown future versions keep the same shape philosophy; unknown fields are ignorable on both sides |
| `event_id` | Per-emission UUID (audit/delivery marker, not semantic identity) |
| `occurred_at` | RFC3339Nano UTC, event time (not send time) |
| `type` | Alert type from the vocabulary below. Receivers must route unknown types to a default rule rather than reject |
| `severity` | `info` \| `warning` \| `error` \| `critical` |
| `title` / `body` | Short factual text (includes the camera name when known). Localized channel copy is the receiver's job |
| `source_ref` | NVR camera ID for camera-scoped alerts (`system` / `storage` otherwise) — the recommended dedup/routing dimension |
| `dedup_key` | Only set for stateful pairs; the resolve leg carries the same key with `resolved: true` |
| `resolved` | `true` marks a recovery that resolves the earlier alert with the same `dedup_key` |
| `snapshot_url` | Optional fetchable frame link (see below) |
| `tags` | Optional enrichment (`camera_name`, `source`); pass unknown tags through |

### Event types

| Type | Covers | Pairing |
|------|--------|---------|
| `camera.connection` | Offline / back online | lost↔restored pair on `dedup_key` |
| `camera.freeze` | Frozen video / recovery | detected↔recovered pair |
| `camera.stream` | One-shot stream anomalies | — |
| `camera.quality` | Quality transitions (HD↔SD) | — |
| `camera.motion` | Camera-side motion (ONVIF MotionAlarm) | — (high-frequency; aggregate on the receiver) |
| `ai.detection` | AI backend events (`POST /api/ai/events`) | — |
| `doorbell.ring` | Doorbell lifecycle | ring→answered/rejected/ended resolves |
| `storage.health` | Storage health transitions | failure↔healthy pair |
| `gb28181.alarm` | GB/T 28181 device alarms | — |
| `camera.added` | Camera registration | — |
| `notify.test` | The settings-panel test button | — |

## Snapshot Links

Alerts can carry a link the receiver (or the user, via the receiver's app) can fetch: the AI event's analyzed frame, the doorbell call frame, or a live camera frame. Links are built from a base URL plus a dedicated `mbv_` API key:

- **Base URL**: `notifications.base_url`, falling back to `server.advertise_url` (the same externally-reachable address ONVIF push events use). Empty on both = alerts deliver without links.
- **API key**: `notifications.api_key` — use a separate, revocable key (generate one in Settings → AI / MiBeeVision). The key is embedded in the URLs and receivers store URLs, so a dedicated key limits the blast radius of a leak.

The serving endpoints are the existing, authenticated ones (`?api_key=` works on all of them):

- `GET /api/ai/events/{id}/snapshot` — the stored AI event frame
- `GET /api/doorbell/calls/{callID}/snapshot` — the doorbell ring frame
- `GET /api/cameras/{id}/snapshot` — a fresh live frame (closest available approximation of event time for motion alerts)

## Configuration

```yaml
notifications:
  enabled: true
  base_url: "http://192.168.1.10:9090"   # snapshot-link base; falls back to server.advertise_url
  api_key: "mbv_..."                      # dedicated key embedded in snapshot links (encrypt-config supported)
  sinks:
    - name: "alert-hub"                   # display name (logs + status endpoint)
      url: "http://192.168.1.20:9080/api/ingest/webhook"  # receiver base URL
      token: "my-secret-token"            # appended as the final path segment (encrypt-config supported)
      events: []                          # type filter (empty = all types)
      severity_min: "info"                # drop alerts below this severity
      timeout_s: 5                        # per-request timeout
      burst_per_min: 30                   # outbound posts per minute cap
```

Each sink gets its own queue and delivery worker — one dead receiver never affects another, and per-sink `events` / `severity_min` filters let you send, say, only critical alerts to a paid SMS bridge while everything goes to the chat robot.

The Settings → Notifications panel manages the same section with the web UI: sink list editing, event-type checkboxes, live delivery counters, and a **Send test alert** button that reaches every receiver bypassing the filters.

## API

| Endpoint | Purpose |
|----------|---------|
| `GET /api/notifications/status` | Enabled flag, per-sink delivery counters (sent / failed / dropped / queue depth), event-type vocabulary |
| `POST /api/notifications/test` | Enqueue a `notify.test` alert to every sink (body: `{severity?, title?}`). `202` = queued — check the status counters for the outcome |

## Receiving Side Tips

- Treat `source_ref` + `type` as your dedup dimension; only pair on `dedup_key` when it is set.
- `resolved: true` alerts should close the matching open alert, not open a new one.
- Alert volume from `camera.motion` / `ai.detection` is bounded by the NVR's own burst cap but can still be bursty — aggregation windows on the receiver side give the best UX.
- `snapshot_url` links are plain HTTP GETs with the API key in the query string; cache them freely (AI/doorbell frames are immutable, camera frames refresh).
