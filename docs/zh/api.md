# API 概览

> 适用于 MiBeeNvr v0.13.0 · 完整接口文档见仓库 [docs/zh/api/](https://github.com/Mi-Bee-Studio/MiBeeNvr/tree/main/docs/zh/api)

MiBee NVR 的全部功能都可通过 REST API 驱动（Web UI 本身就是这套路 API 的消费者），并提供 SSE 实时事件流。本页是认证方式与核心端点速查。

## 认证

| 方式 | 适用 | 用法 |
|------|------|------|
| **BasicAuth** | 管理员脚本 / curl | `curl -u admin:password ...` |
| **API Key**（`mbv_` 前缀） | 外部服务 / 按设备 token | `Authorization: Bearer mbv_...`（SSE/WebSocket 等无法带头的场景可用 `?api_key=` 查询参数） |
| 会话（`mbs_`） | 浏览器 | `POST /api/auth/login` 签发 |
| 网关会话 | fnOS 桌面集成 | `GET /api/auth/gateway-session` 用 fnOS 统一网关身份换发会话 |

认证顺序：公开路由 → Bearer API Key → BasicAuth → 未设置密码时全部返回 `503 SETUP_REQUIRED`。

**API Key 管理**：设置 → AI 检测 → MiBeeVision 集成（或 `POST /api/settings/api-keys`）。密钥只在创建时显示一次，可单独吊销，**生成/吊销即时生效**，无需重启。

```bash
# 生成密钥
curl -u admin:password -X POST -H "Content-Type: application/json" \
  -d '{"name": "my-integration"}' \
  http://localhost:9090/api/settings/api-keys

# 使用密钥
curl -H "Authorization: Bearer mbv_xxx" http://localhost:9090/api/recordings
```

## 公开端点（无需认证）

| 端点 | 说明 |
|------|------|
| `GET /api/health` | 健康汇总（含存储 / 摄像头状态） |
| `GET /api/readyz` | 就绪探针 |
| `GET /api/capabilities` | 摄取能力（RTMP / SRT 端口） |
| `POST /api/trigger/webhook/{camera_id}` | Webhook 触发（公开限流组，凭据为 HMAC 签名，见下表） |
| `GET /models/{filename}` | 浏览器端 AI 模型文件 |

> v0.13 起录像下载（`/api/recordings/{id}/download`、`/api/recordings/{id}/merged`、延时合并下载）、回放、`GET /api/events` 与 `GET /api/health/cameras` 已从匿名移入鉴权组（浏览器经 `?token=` 会话令牌或 cookie 透明携带凭据）；旧版 base64 `?token=` 透传已移除。详见[身份验证 · v0.13 鉴权范围变更](api/authentication.md)。

## 核心端点组

| 组 | 端点 | 说明 |
|----|------|------|
| 摄像头 | `GET/POST /api/cameras`、`GET/PUT/DELETE /api/cameras/{id}`、`POST /api/cameras/{id}/adaptive/trigger`、`PUT/GET /api/cameras/{id}/storage-root` | 摄像头 CRUD、自适应录制外部触发、按相机存储根 |
| 实时流 | `GET /api/cameras/{id}/stream.flv`、HLS / WebRTC / MJPEG 端点 | 拉流（FLV 需 BasicAuth） |
| 录像 | `GET /api/recordings` | 列表 / 筛选 / 分页 |
| 回放 | `GET /api/cameras/{id}/playback/playlist.m3u8` | 按录像回放 |
| AI 事件 | `POST /api/ai/events`、`GET /api/ai/events`、`GET /api/ai/events/{id}`、`GET /api/ai/events/{id}/snapshot` | 外部 AI 后端写入（Bearer）与查询统计；事件快照图片 |
| 触发 | `POST /api/trigger/webhook/{camera_id}` | HMAC 签名 webhook 触发录像 / 停止 / 抓拍（完整文档见 [Webhook 集成](webhook-integration.md)） |
| 设置 | `GET/PUT /api/settings`、`POST /api/settings/api-keys` | 运行配置与密钥 |
| 存储 | `GET /api/storage`、`GET/POST/DELETE /api/storage/candidates`、`POST /api/storage/migrate` | 存储统计、候选卷管理、批量迁移（[存储管理](storage-management.md)） |
| GB28181 | `/api/gb28181/*` | 设备 / 通道 / PTZ / 回放 |
| 系统 | `GET /api/version`、`GET /api/capabilities`、`GET /api/stats` | 版本 / 能力 / 统计 |

## AI 事件快照

`GET /api/ai/events/{id}/snapshot` 返回该事件的快照 JPEG（外部 AI 后端在事件里上报的
`snapshot_path`，相对 NVR 存储 root，如同机部署写入 `<存储根>/ai-snapshots/`）：

```bash
curl -o snap.jpg "http://localhost:9090/api/ai/events/7141/snapshot?api_key=mbv_…"
```

- 鉴权与其它 AI 查询端点一致（BasicAuth 会话 / Bearer API key / `?api_key=`）
- **一切「无图可用」情形统一 404**（事件无 `snapshot_path`、文件不存在、事件不存在），
  客户端无需解析错误体即可回退占位图；非法 id 返回 400
- 响应带 `Cache-Control: private, max-age=86400`（快照不可变），支持 Range 与
  条件请求
- 路径在存储 root 内解析，穿越或根外路径一律 404——不会读到存储树之外的文件

远程部署的外部 AI 后端无法直写 NVR 存储树时，先 POST 事件拿到 id，再把
快照 JPEG 原始字节上传到同一资源路径（`Content-Type: image/jpeg`，API Key
鉴权）：

```bash
curl -X POST -H "Authorization: Bearer mbv_…" -H "Content-Type: image/jpeg"   --data-binary @snap.jpg "http://localhost:9090/api/ai/events/7141/snapshot"
```

- 上限 4MB；非 JPEG 魔数返回 400；文件落地 `<存储根>/ai-snapshots/` 并回填
  事件的 `snapshot_path`（原子写），成功后 GET 立即可服务
- 同机（sidecar）部署无需上传——直接写 `ai-snapshots/` 子目录并在事件里带
  相对路径即可，两种形态收敛到同一 GET 端点


## SSE 事件流

`GET /api/events` 以 Server-Sent Events 推送 NVR 内部事件总线，`filter` 查询参数按主题前缀过滤。v0.13 起需认证（BasicAuth / 会话令牌 / API Key；浏览器 EventSource 经 `?token=` 或 `?api_key=` 携带）：

```bash
# 只订阅录像片段完成事件（外部 AI 后端的典型接入方式）
curl -N "http://localhost:9090/api/events?filter=segment.&api_key=mbv_…"
```

事件负载为嵌套结构，业务字段在 `Data` 内（含 `recording_id`）：

```text
data: {"Topic":"segment.completed","Data":{ ... }}
```

> 事件也可通过 [MQTT](mqtt-integration.md) 订阅，适合家庭自动化集成。

## 完整文档

每个端点组的请求 / 响应字段、错误码见仓库：

**[docs/zh/api/](https://github.com/Mi-Bee-Studio/MiBeeNvr/tree/main/docs/zh/api)** — 认证 · 摄像头 · 录像 · 流媒体 · AI 检测 · 事件 · 设置 · GB28181 · 备份 …

## 下一步

- [MQTT 集成](mqtt-integration.md) — 事件的家庭自动化集成
- [WebDAV / FTP](webdav-ftp.md) — 文件级访问录像
- [CLI 手册](cli.md) — 命令行管理
