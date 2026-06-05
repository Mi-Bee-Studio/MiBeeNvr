# 身份验证

MiBee NVR 对受保护的端点使用 HTTP Basic Authentication。认证凭据在应用设置中配置。

### 如何使用 Basic Auth

```bash
curl -u username:password http://localhost:9090/api/cameras
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
