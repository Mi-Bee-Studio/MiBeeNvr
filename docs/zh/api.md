# API 概览

> 适用于 MiBeeNvr v0.11.0 · 完整接口文档见仓库 [docs/zh/api/](https://github.com/Mi-Bee-Studio/MiBeeNvr/tree/main/docs/zh/api)

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
| `GET /api/events` | **SSE 事件流**（限速 60 次/分钟，见下） |
| `GET /api/recordings/{id}/download` | 录像下载（支持 Range，供播放器拖动） |
| `GET /api/recordings/{id}/merged` | 延时摄影等合并产物 |
| `GET /models/{filename}` | 浏览器端 AI 模型文件 |

## 核心端点组

| 组 | 端点 | 说明 |
|----|------|------|
| 摄像头 | `GET/POST /api/cameras`、`GET/PUT/DELETE /api/cameras/{id}` | 摄像头 CRUD |
| 实时流 | `GET /api/cameras/{id}/stream.flv`、HLS / WebRTC / MJPEG 端点 | 拉流（FLV 需 BasicAuth） |
| 录像 | `GET /api/recordings` | 列表 / 筛选 / 分页 |
| 回放 | `GET /api/cameras/{id}/playback/playlist.m3u8` | 按录像回放 |
| AI 事件 | `POST /api/ai/events`、`GET /api/ai/events`、`GET /api/ai/stats` | 外部 AI 后端写入（Bearer）与查询统计 |
| 设置 | `GET/PUT /api/settings`、`POST /api/settings/api-keys` | 运行配置与密钥 |
| 存储 | `GET /api/storage`（含 `candidates`） | 存储统计与可用卷 |
| GB28181 | `/api/gb28181/*` | 设备 / 通道 / PTZ / 回放 |
| 系统 | `GET /api/version`、`GET /api/capabilities`、`GET /api/stats` | 版本 / 能力 / 统计 |

## SSE 事件流

`GET /api/events` 以 Server-Sent Events 推送 NVR 内部事件总线，`filter` 查询参数按主题前缀过滤：

```bash
# 只订阅录像片段完成事件（外部 AI 后端的典型接入方式）
curl -N "http://localhost:9090/api/events?filter=segment."
```

事件负载为嵌套结构，业务字段在 `Data` 内（含 `recording_id`）：

```
data: {"Topic":"segment.completed","Data":{ ... }}
```

> 事件也可通过 [MQTT](mqtt.md) 订阅，适合家庭自动化集成。

## 完整文档

每个端点组的请求 / 响应字段、错误码见仓库：

**[docs/zh/api/](https://github.com/Mi-Bee-Studio/MiBeeNvr/tree/main/docs/zh/api)** — 认证 · 摄像头 · 录像 · 流媒体 · AI 检测 · 事件 · 设置 · GB28181 · 备份 …

## 下一步

- [MQTT 集成](mqtt.md) — 事件的家庭自动化集成
- [WebDAV / FTP](webdav-ftp.md) — 文件级访问录像
- [CLI 手册](cli.md) — 命令行管理
