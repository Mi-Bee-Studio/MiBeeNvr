# Authentication

MiBee NVR supports dual authentication: **API Key authentication** for external services (MiBeeVision) and **HTTP Basic Authentication** for browser users. API Key auth is attempted first when a Bearer token is present, otherwise BasicAuth is used.

### How to Use Basic Auth

```bash
curl -u username:password http://localhost:9090/api/cameras
```

## API Key Authentication

API Keys allow external services (e.g., MiBeeVision AI processing) or per-device app tokens (e.g., a family member's phone) to authenticate without user credentials. Keys use the `mbv_` prefix, are minted with a label, and can be revoked individually — a lost device's token is revoked without touching other credentials.

**Key changes apply immediately** — minting or revoking a key takes effect on the next request, no service restart required. The key list (Settings → AI Detection → MiBeeVision, or `GET /api/settings` → `mibeevision.api_keys`) shows each key's prefix, revocation state, and last-used timestamp (updated at most once per minute per key).

### How to Use API Key Auth

```bash
# Using Authorization header (recommended)
curl -H "Authorization: Bearer mbv_your_api_key_here" \
  http://localhost:9090/api/recordings

# Using query parameter (for clients that cannot set headers, e.g. SSE/WebSocket)
curl "http://localhost:9090/api/ai/events?api_key=mbv_your_api_key_here"
```

### Authentication Order

1. **Public routes** — no auth required (`/api/health`, `/api/readyz`, `/api/capabilities`, `/api/trigger/webhook/*`, `/models/{filename}`)
2. **API Key** — if `Authorization: Bearer mbv_...` header is present, API Key auth is attempted first
3. **Setup gate** — if no password is configured, `503 SETUP_REQUIRED` is returned (the first-run wizard must complete first)
4. **Local bypass** — loopback requests skip auth entirely when `auth.local_bypass` is on (the desktop-install default)
5. **Session token / BasicAuth** — a `Bearer mbs_...` session token is tried before BasicAuth

### Managing API Keys

**Generate a new API key:**

```bash
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{"name": "MiBeeVision Production"}' \
  "http://localhost:9090/api/settings/api-keys"
```

Response:
```json
{
  "name": "MiBeeVision Production",
  "key": "mbv_a1b2c3d4e5f67890123456789012345678901234"
}
```

**Revoke an API key:**

```bash
curl -u admin:password \
  -X DELETE \
  "http://localhost:9090/api/settings/api-keys/MiBeeVision%20Production"
```

### Authentication Behavior

- If `password_hash` is configured in the settings: All protected endpoints require valid Basic Auth credentials
- If `password_hash` is empty in settings: Authentication is bypassed (no protection)
- Failed authentication returns `401 Unauthorized` with empty body

## Login

**Endpoint:** `POST /api/auth/login`

Validate authentication credentials. Returns 200 OK if credentials are valid, or forwards the auth middleware response (401 Unauthorized or 503 SETUP_REQUIRED).

**Request:**
```bash
curl -u username:password -X POST "http://localhost:9090/api/auth/login"
```

**Response (200 OK):**
```json
{
  "status": "ok",
  "token": "mbs_eyJhbGciOi…",
  "expires_at": "2026-08-17T14:00:00Z"
}
```

`token` is an HMAC-signed stateless session token (`mbs_` prefix); use it as
`Authorization: Bearer <token>` until expiry. Near expiry the server renews it
transparently via the `X-Renewed-Token` response header.

**Response (401 Unauthorized):**
```json
{
  "error": "authentication failed: invalid username or password",
  "code": "AUTH_FAILED"
}
```

## Change Password

**Endpoint:** `POST /api/auth/password`

Set a new admin password **without knowing the old one**. Authorization is locality: the request must come from a loopback session on the NVR's own machine (a loopback connection with a loopback / `localhost` Host header and no proxy headers); remote callers always get 403 — the desktop tray / macOS menu-bar helper calls it from the NVR's own machine, where the operator could equally edit the config file by hand. The password exists for NON-local (LAN) logins — local sessions bypass auth entirely when `auth.local_bypass` is on (the desktop-install default).

**Request Body:**
```json
{
  "new_password": "new-secure-password"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `new_password` | string | Yes | New password (at least 8 characters) |

**Request:**
```bash
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"new_password": "new-secure-password"}' \
  "http://127.0.0.1:9090/api/auth/password"
```

**Response (200 OK):**
```json
{
  "status": "ok"
}
```

**Response (403 Forbidden, remote request):**
```json
{
  "error": "password change is only available from a local session on the NVR machine"
}
```

> Side effect worth knowing: the bcrypt hash is part of the session-token signing key, so a successful change invalidates **every outstanding session token (`mbs_`)** — remote sessions must re-login. Returns 409 when setup has not been completed (no password configured).

## Setup

**Endpoint:** `POST /api/setup`

First-time initialization. Only succeeds when no `password_hash` is configured. Patches the admin credentials (username + bcrypt hash) onto the **loaded config** — every other field pre-set in the YAML (`listen`, `vision`, `api_keys`, cameras, …) is preserved, never reset. `storage_path` is optional and only overrides `storage.root_dir` when provided; leave it empty to keep the server's current setting. Returns a signed session token (`mbs_` prefix) plus its expiry for auto-login.

**Request Body:**
```json
{
  "username": "admin",
  "password": "securepassword",
  "language": "en",
  "storage_path": "/var/lib/mibee-nvr"
}
```

**Request:**
```bash
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "admin",
    "password": "securepassword",
    "language": "en"
  }' \
  "http://localhost:9090/api/setup"
```

**Response:**
```json
{
  "status": "ok",
  "token": "mbs_eyJhbGciOi…",
  "expires_at": "2026-08-17T14:00:00Z"
}
```

**Response (already configured):**
```json
{
  "error": "setup already completed",
  "code": "INVALID_INPUT"
}
```

## Capabilities

**Endpoint:** `GET /api/capabilities`

Get system ingest capabilities (RTMP, SRT). Public endpoint.

**Request:**
```bash
curl http://localhost:9090/api/capabilities
```

**Response:**
```json
{
  "ingest": {
    "rtmp": {
      "enabled": true,
      "port": 1935
    },
    "srt": {
      "enabled": true,
      "port": 8890
    }
  }
}
```

## v0.13 Auth Scope Changes

As of v0.13 the following endpoints moved from anonymous (no auth) into the authenticated group, using the same credentials as every other `/api` endpoint (BasicAuth / Bearer API key / session token / streaming cookie):

- `GET/HEAD /api/recordings/{id}/download` — recording download
- `GET/HEAD /api/recordings/{id}/merged` — merged-output download
- `GET/HEAD /api/timelapse/merges/{id}/download` — timelapse merge download
- `GET /api/cameras/{cameraID}/playback/*` — per-recording playback (playlist and segments)
- `GET /api/events` — full event stream (SSE)
- `GET /api/health/cameras` — per-camera health detail

Rationale: recording IDs are timestamp-predictable, and the event stream plus camera health detail expose fleet topology such as camera names — the risk of anonymous reachability outweighed the convenience. Browser flows are unaffected — the SPA's `<video src>` / `<a download>` links keep working via `?token=` (session tokens only) or the streaming cookie.

At the same time, the **legacy base64(user:pass) `?token=` passthrough was removed**: credentials in URLs leak into browser history and proxy logs. `?token=` now accepts `mbs_` session tokens only; clients that cannot set headers should use `?api_key=` (an `mbv_`-prefixed API key) or BasicAuth instead.
