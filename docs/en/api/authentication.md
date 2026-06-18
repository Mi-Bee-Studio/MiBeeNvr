# Authentication

MiBee NVR supports dual authentication: **API Key authentication** for external services (MiBeeVision) and **HTTP Basic Authentication** for browser users. API Key auth is attempted first when a Bearer token is present, otherwise BasicAuth is used.

### How to Use Basic Auth

```bash
curl -u username:password http://localhost:9090/api/cameras
```

## API Key Authentication

API Keys allow external services (e.g., MiBeeVision AI processing) to authenticate without user credentials. Keys use the `mbv_` prefix and are sent as Bearer tokens.

### How to Use API Key Auth

```bash
# Using Authorization header (recommended)
curl -H "Authorization: Bearer mbv_your_api_key_here" \
  http://localhost:9090/api/recordings

# Using query parameter (for SSE/WebSocket only)
curl "http://localhost:9090/api/ai/events?api_key=mbv_your_api_key_here"
```

### Authentication Order

1. **Public routes** — no auth required (`/api/health`, `/api/metrics`, `/models/{filename}`, `/api/recordings/{id}/download`, `/api/recordings/{id}/merged`)
2. **API Key** — if `Authorization: Bearer mbv_...` header is present, API Key auth is attempted first
3. **BasicAuth** — if no Bearer token, BasicAuth is used
4. **Setup gate** — if no password is configured, `503 SETUP_REQUIRED` is returned

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
  "status": "ok"
}
```

**Response (401 Unauthorized):**
```json
{
  "error": "authentication failed: invalid username or password",
  "code": "AUTH_FAILED"
}
```

## Setup

**Endpoint:** `POST /api/setup`

First-time initialization. Only succeeds when no `password_hash` is configured. Creates admin credentials, sets storage path, and returns a Basic Auth token for auto-login.

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
  "token": "YWRtaW46c2VjdXJlcGFzc3dvcmQ="
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
