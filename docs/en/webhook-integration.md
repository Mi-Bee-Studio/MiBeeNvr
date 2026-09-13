# Webhook Trigger Integration

MiBee NVR exposes an HMAC-signed HTTP trigger endpoint — the counterpart of the [MQTT trigger](mqtt-integration.md): external systems (sensors, automation platforms, IoT gateways) POST a signed request to start/stop recording or capture a snapshot, with no MQTT broker required.

## Overview

- **Endpoint**: `POST /api/trigger/webhook/{camera_id}?action=record|stop|snapshot`
- **Auth**: no BasicAuth — the credential is an HMAC-SHA256 signature over the request body (Stripe-style `X-MiBee-Signature` header); third parties only ever hold the pre-shared secret
- **Action semantics**: identical to the MQTT trigger by construction (both feed the same action dispatcher)
- **Replay protection**: the signed timestamp must fall inside the replay window (default ±300 seconds, checked in both directions)
- **Rate limit**: the same public rate-limited group as `/api/health` (60 req/min per IP)
- **Body cap**: 1 MB

## Configuration

```yaml
trigger:
  webhook:
    enabled: true            # Mount the endpoint (default false)
    secret: "whsec_your_key" # Pre-shared HMAC-SHA256 key, required when enabled (encrypt-config supported)
    replay_window_s: 300     # Allowed timestamp skew window in seconds (default 300)
```

## Signing a Request

Header format:

```text
X-MiBee-Signature: t=<unix-seconds>,v1=<hex(HMAC-SHA256(secret, "<t>.<body>"))>
```

The timestamp is inside the MAC input, so a captured signature cannot be re-freshed; a replayed request only validates inside the window.

```bash
# Trigger bodies are usually empty — the signed string is just "<t>."
T=$(date +%s)
SIG=$(printf '%s.' "$T" | openssl dgst -sha256 -hmac "whsec_your_key" -hex | cut -d' ' -f2)

curl -X POST "http://192.168.1.50:9090/api/trigger/webhook/cam-xxxx?action=record" \
  -H "X-MiBee-Signature: t=$T,v1=$SIG"
```

**Responses**:

| Status | Meaning |
|--------|---------|
| `202 accepted` | Trigger accepted (actions run asynchronously; outcomes land in the structured/audit log) |
| `401` | Signature invalid / expired / missing (uniform response — no oracle distinguishing the cases) |
| `400` | `action` is not one of `record` / `stop` / `snapshot` |
| `404` | Unknown camera (returned only after signature verification — unsigned callers get no existence oracle) |
| `503` | Trigger dispatcher not available |

## Webhook vs MQTT

| | MQTT trigger | Webhook trigger |
|---|---|---|
| Dependency | Requires an MQTT broker | None (direct HTTP) |
| Direction | Bidirectional (also [publishes status](mqtt-integration.md#status-publishing)) | One-way (trigger only) |
| Auth | Broker username/password | HMAC signature + replay window |
| Best for | Environments already on a smart-home bus | Single sensors / scripts / cloud-function callbacks |
