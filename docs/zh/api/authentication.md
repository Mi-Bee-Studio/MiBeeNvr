# 身份验证

MiBee NVR 支持双重认证：外部服务（MiBeeVision）使用 **API Key 认证**，浏览器用户使用 **HTTP Basic Authentication**。当请求携带 Bearer token 时，优先尝试 API Key 认证，否则使用 BasicAuth。

### 如何使用 Basic Auth

```bash
curl -u username:password http://localhost:9090/api/cameras
```

## API Key 认证

API Key 允许外部服务（如 MiBeeVision AI 处理）或按设备客户端 token（如家庭成员的手机）无需用户凭据即可进行认证。密钥使用 `mbv_` 前缀，生成时带标签，可单独吊销 — 手机丢失时只需吊销该设备的 token，不影响其他凭据。

**密钥变更即时生效** — 生成或吊销密钥在下一个请求即生效，无需重启服务。密钥列表（设置 → AI 检测 → MiBeeVision，或 `GET /api/settings` → `mibeevision.api_keys`）显示每个密钥的前缀、吊销状态和最近使用时间（每密钥每分钟最多更新一次）。

### 如何使用 API Key

```bash
# 使用 Authorization 请求头（推荐）
curl -H "Authorization: Bearer mbv_your_api_key_here" \
  http://localhost:9090/api/recordings

# 使用查询参数（适用于无法设置请求头的客户端，如 SSE/WebSocket）
curl "http://localhost:9090/api/ai/events?api_key=mbv_your_api_key_here"
```

### 认证顺序

1. **公开路由** — 无需认证（`/api/health`、`/api/readyz`、`/api/capabilities`、`/api/trigger/webhook/*`、`/models/{filename}`）
2. **API Key** — 如果请求包含 `Authorization: Bearer mbv_...`，优先尝试 API Key 认证
3. **设置门控** — 如果未配置密码，返回 `503 SETUP_REQUIRED`（必须先完成初始化向导）
4. **本机旁路** — 环回请求在 `auth.local_bypass` 开启时（桌面安装默认）完全绕过认证
5. **会话令牌 / BasicAuth** — `Bearer mbs_...` 会话令牌优先于 BasicAuth

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
  "status": "ok",
  "token": "mbs_eyJhbGciOi…",
  "expires_at": "2026-08-17T14:00:00Z"
}
```

`token` 为 HMAC 签名的无状态会话令牌（`mbs_` 前缀），有效期内可通过
`Authorization: Bearer <token>` 使用，临近过期时服务端会经 `X-Renewed-Token`
响应头自动续期。

**响应（401 Unauthorized）：**
```json
{
  "error": "authentication failed: invalid username or password",
  "code": "AUTH_FAILED"
}
```

## 修改密码

**端点：** `POST /api/auth/password`

设置新的管理员密码，**无需提供旧密码**。授权依据是「本机性」：请求必须来自 NVR 本机的环回会话（环回连接 + 环回 / `localhost` Host 头、无代理转发头），远程调用一律 403——桌面版托盘 / macOS 菜单栏助手从 NVR 自己的机器上调用它，本机操作者本来就能手改配置文件。密码本身服务于**非本机**（局域网）登录：本机会话在开启 `auth.local_bypass` 时（桌面安装默认）完全绕过认证。

**请求体：**
```json
{
  "new_password": "new-secure-password"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `new_password` | string | 是 | 新密码（至少 8 个字符） |

**请求：**
```bash
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"new_password": "new-secure-password"}' \
  "http://127.0.0.1:9090/api/auth/password"
```

**响应（200 OK）：**
```json
{
  "status": "ok"
}
```

**响应（403 Forbidden，远程请求）：**
```json
{
  "error": "password change is only available from a local session on the NVR machine"
}
```

> 注意副作用：bcrypt 哈希参与会话令牌的签名密钥，改密成功后**所有已签发的会话令牌（`mbs_`）立即失效**，远程会话需重新登录。尚未完成初始化（未配置密码）时返回 409。

## 设置

**端点：** `POST /api/setup`

首次初始化。仅在未配置 `password_hash` 时成功。在**已加载的配置**上增量写入管理员凭据（用户名 + bcrypt 密码哈希）—— 预置在 YAML 里的其它字段（`listen`、`vision`、`api_keys`、摄像头列表等）全部保留，不会被重置。`storage_path` 可选，仅在填写时覆盖当前 `storage.root_dir`，留空则沿用服务端配置。返回用于自动登录的签名会话令牌（`mbs_` 前缀）与过期时间。

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
  "token": "mbs_eyJhbGciOi…",
  "expires_at": "2026-08-17T14:00:00Z"
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

## v0.13 鉴权范围变更

自 v0.13 起，以下端点从匿名（无需认证）移入鉴权组，与其它 `/api` 端点采用相同的认证方式（BasicAuth / Bearer API Key / 会话令牌 / 流媒体 cookie）：

- `GET/HEAD /api/recordings/{id}/download` — 录像下载
- `GET/HEAD /api/recordings/{id}/merged` — 合并产物下载
- `GET/HEAD /api/timelapse/merges/{id}/download` — 延时合并下载
- `GET /api/cameras/{cameraID}/playback/*` — 按录像回放（播放列表与切片）
- `GET /api/events` — 全量事件流（SSE）
- `GET /api/health/cameras` — 摄像头健康明细

原因：录像 ID 可由时间戳预测，事件流与摄像头健康明细会暴露摄像头名称等拓扑信息，匿名可达的风险大于便利。浏览器场景不受影响——SPA 的 `<video src>` / `<a download>` 链接可继续通过 `?token=`（仅接受 `mbs_` 会话令牌）或流媒体 cookie 携带凭据。

同时，**旧版 `?token=` 的 base64(user:pass) 透传已移除**：凭据以明文形式出现在 URL 中会进入浏览器历史与代理日志。`?token=` 现在只接受 `mbs_` 会话令牌；无法设置请求头的客户端请改用 `?api_key=`（`mbv_` 前缀的 API Key）或 BasicAuth。
