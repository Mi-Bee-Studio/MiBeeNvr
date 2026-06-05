# Xiaomi API

## Xiaomi Cloud Authentication

**Endpoint:** `POST /api/xiaomi/auth`

Authenticate with Xiaomi cloud services.

**Request Body:**
```json
{
  "username": "xiaomi@example.com",
  "password": "password123"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/auth"
```

**Response:**
```json
{
  "user_id": "1234567890",
  "token": "xiaomi_token_123",
  "region": "cn"
}
```

## Get Xiaomi Captcha

**Endpoint:** `POST /api/xiaomi/captcha`

Get captcha for Xiaomi authentication.

**Request Body:**
```json
{
  "username": "xiaomi@example.com"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com"
  }' \
  "http://localhost:9090/api/xiaomi/captcha"
```

**Response:**
```json
{
  "captcha_id": "captcha_123",
  "captcha_image": "base64_encoded_image"
}
```

## Verify Xiaomi Captcha

**Endpoint:** `POST /api/xiaomi/verify`

Verify captcha and complete authentication.

**Request Body:**
```json
{
  "captcha_id": "captcha_123",
  "captcha_code": "ABC123",
  "username": "xiaomi@example.com",
  "password": "password123"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "captcha_id": "captcha_123",
    "captcha_code": "ABC123",
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/verify"
```

**Response:**
```json
{
  "user_id": "1234567890",
  "token": "xiaomi_token_123",
  "region": "cn"
}
```

## List Xiaomi Devices

**Endpoint:** `GET /api/xiaomi/devices`

Get list of Xiaomi devices from cloud.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/xiaomi/devices"
```

**Response:**
```json
{
  "devices": [
    {
      "did": "camera_did_123",
      "name": "Front Door Camera",
      "model": "xiaomi.camera.v2",
      "online": true
    }
  ]
}
```

## Sync Xiaomi Devices

**Endpoint:** `POST /api/xiaomi/sync`

Sync Xiaomi devices with local configuration.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/xiaomi/sync"
```

**Response:**
```json
{
  "synced": 2,
  "added": 1,
  "removed": 0
}
```

## Check Xiaomi Vendor

**Endpoint:** `GET /api/xiaomi/check-vendor`

Check the vendor protocol for a Xiaomi device by DID. Validates whether the device uses the CS2 protocol (supported) or TUTK protocol (not supported).

**Query Parameters:**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `did` | string | Yes | Device DID to check | `camera_did_123` |

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/xiaomi/check-vendor?did=camera_did_123"
```

**Response (CS2 - compatible):**
```json
{
  "vendor": "cs2",
  "compatible": true
}
```

**Response (TUTK - incompatible):**
```json
{
  "vendor": "tutk",
  "compatible": false,
  "message": "This device uses TUTK protocol which is not supported by MiBee NVR. Only CS2 protocol cameras are supported."
}
```
