# Authentication

MiBee NVR uses HTTP Basic Authentication for protected endpoints. The authentication credentials are configured in the application settings.

### How to Use Basic Auth

```bash
curl -u username:password http://localhost:9090/api/cameras
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
