# 身份验证

MiBee NVR 支持双重认证：外部服务（MiBeeVision）使用 **API Key 认证**，浏览器用户使用 **HTTP Basic Authentication**。当请求携带 Bearer token 时，优先尝试 API Key 认证，否则使用 BasicAuth。

### 如何使用 Basic Auth

```bash
curl -u username:password http://localhost:9090/api/cameras
```

## API Key 认证

API Key 允许外部服务（如 MiBeeVision AI 处理）无需用户凭据即可进行认证。密钥使用 `mbv_` 前缀，以 Bearer token 形式发送。

### 如何使用 API Key

```bash
# 使用 Authorization 请求头（推荐）
curl -H "Authorization: Bearer mbv_your_api_key_here" \
  http://localhost:9090/api/recordings

# 使用查询参数（仅限 SSE/WebSocket）
curl "http://localhost:9090/api/ai/events?api_key=mbv_your_api_key_here"
```

### 认证顺序

1. **公开路由** — 无需认证（`/api/health`、`/api/metrics`、`/models/{filename}`、`/api/recordings/{id}/download`、`/api/recordings/{id}/merged`）
2. **API Key** — 如果请求包含 `Authorization: Bearer mbv_...`，优先尝试 API Key 认证
3. **BasicAuth** — 如果没有 Bearer token，则使用 BasicAuth
4. **设置门控** — 如果未配置密码，返回 `503 SETUP_REQUIRED`

### 管理 API Key

**生成新的 API Key：**

```bash
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{"name": "MiBeeVision Production"}' \
  "http://localhost:9090/api/settings/api-keys"
```

响应：
```json
{
  "name": "MiBeeVision Production",
  "key": "mbv_a1b2c3d4e5f67890123456789012345678901234"
}
```

**撤销 API Key：**

```bash
curl -u admin:password \
  -X DELETE \
  "http://localhost:9090/api/settings/api-keys/MiBeeVision%20Production"
```

### 身份验证行为

- 如果设置中配置了 `password_hash`：所有受保护的端点需要有效的 Basic Auth 凭据
- 如果设置中 `password_hash` 为空：跳过身份验证（无保护）
- 身份验证失败返回 `401 Unauthorized`，响应体为空

## 登录

**端点：** `POST /api/auth/login`

验证身份认证凭据。如果凭据有效，返回 200 OK；否则转发认证中间件的响应（401 Unauthorized 或 503 SETUP_REQUIRED）。

**请求：**
```bash
curl -u username:password -X POST "http://localhost:9090/api/auth/login"
```

**响应（200 OK）：**
```json
{
  "status": "ok"
}
```

**响应（401 Unauthorized）：**
```json
{
  "error": "authentication failed: invalid username or password",
  "code": "AUTH_FAILED"
}
```

## 设置

**端点：** `POST /api/setup`

首次初始化。仅在未配置 `password_hash` 时成功。创建管理员凭据、设置存储路径，并返回用于自动登录的 Basic Auth 令牌。

**请求体：**
```json
{
  "username": "admin",
  "password": "securepassword",
  "language": "en",
  "storage_path": "/var/lib/mibee-nvr"
}
```

**请求：**
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

**响应：**
```json
{
  "status": "ok",
  "token": "YWRtaW46c2VjdXJlcGFzc3dvcmQ="
}
```

**响应（已配置）：**
```json
{
  "error": "setup already completed",
  "code": "INVALID_INPUT"
}
```

## 能力查询

**端点：** `GET /api/capabilities`

获取系统摄取能力（RTMP、SRT）。公开端点。

**请求：**
```bash
curl http://localhost:9090/api/capabilities
```

**响应：**
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
