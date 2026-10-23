# Doorbell API

SIP doorbell / intercom endpoints (#971). All require authentication. The
call endpoints need the feature enabled in config (`doorbell.enabled: true`,
settable from Settings → Doorbell) — otherwise they answer
`501 Not Implemented`; the status endpoint below always answers. See
[Doorbell](../doorbell.md) for the feature guide.

## Runtime status

**Endpoint:** `GET /api/doorbell/status`

Config + runtime state of the ingress. `enabled` is the config-side toggle
and `running` whether the SIP server is actually built — the two diverge
between a settings save and the restart it needs, which is exactly the
"restart pending" state the settings panel surfaces.

```bash
curl -u username:password http://localhost:9090/api/doorbell/status
```

**Response:**
```json
{
  "enabled": true,
  "running": true,
  "registered": [
    {
      "user": "front-door",
      "source": "192.0.2.50:5060",
      "expires_at": "2026-10-15T20:35:00+08:00"
    }
  ]
}
```

`registered` lists the live mode-A registrations (door stations that
REGISTERed with the NVR and have not expired); mode-B stations behind a PBX
do not appear here.

## List calls

**Endpoint:** `GET /api/doorbell/calls`

Recent calls, newest first (live and history, in-memory, capped).

```bash
curl -u username:password http://localhost:9090/api/doorbell/calls
```

**Response:**
```json
{
  "calls": [
    {
      "id": "0f1e2d3c-...",
      "user": "front-door",
      "name": "Front door",
      "camera_id": "cam-xxxx",
      "snapshot_path": "snapshots/cam-xxxx/20261015-203000.123.jpg",
      "state": "ringing",
      "started_at": "2026-10-15T20:30:00+08:00",
      "end_reason": ""
    }
  ]
}
```

`state` is one of `ringing`, `answered`, `ended`.

## Answer a call

**Endpoint:** `POST /api/doorbell/calls/{id}/answer`

Accepts a ringing call: the door station receives `200 OK` with the NVR's
RTP endpoint and the call becomes `answered` (talk bridge attachable).

- `409` — the call is not ringing (already answered/ended).
- `404` — unknown call id.

## Reject a call

**Endpoint:** `POST /api/doorbell/calls/{id}/reject`

Declines a ringing call (`603 Decline` to the door station).

## Hang up

**Endpoint:** `POST /api/doorbell/calls/{id}/bye`

Ends an answered call (SIP `BYE` to the door station).

## Ring snapshot

**Endpoint:** `GET /api/doorbell/calls/{id}/snapshot`

Serves the JPEG captured when the bell rang (`image/jpeg`,
`Cache-Control: no-store`). `404` when the call has no snapshot (no linked
camera, or capture failed). The URL carries no auth query param — use
BasicAuth headers or front it with the session token via the web client.

## Talk WebSocket

**Endpoint:** `GET /api/doorbell/calls/{id}/talk`

Binary WebSocket bridging the answered call's audio — the same contract as
the [GB28181 talk socket](../gb28181-guide.md):

- Browser → server: binary frames, each frame's bytes are raw **G.711
  A-law** payloads (standard form) destined for the doorbell speaker.
- Server → browser: binary frames of raw G.711 A-law from the doorbell mic
  (decode and play at 8 kHz mono).
- One bridge per call; a second client gets `409`.
- Closing the WebSocket detaches the bridge; it does **not** end the call
  (use the `bye` endpoint for that). A JSON text frame `{"close": true}` is
  also accepted as a client-side detach.

Auth: like all WebSockets, the session token rides `?token=` when cookie
headers are unavailable.

## Events

Every call transition publishes on the event stream
([Events](events.md), filter `doorbell.`):

| Topic | When |
|-------|------|
| `doorbell.ring` | INVITE accepted from a registered/configured station |
| `doorbell.answered` | a client answered |
| `doorbell.rejected` | a client rejected |
| `doorbell.ended` | BYE / CANCEL / ring timeout / hangup / shutdown (`reason` field) |

Payload: `{"call_id", "user", "name", "camera_id", "snapshot_path", "reason", "timestamp"}`.
With `mqtt.status_events` enabled the same topics forward to MQTT as
`{prefix}/event/doorbell.*`.
