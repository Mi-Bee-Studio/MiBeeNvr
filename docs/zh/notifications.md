# 告警通知（Webhook 转发）

MiBee NVR 可将自身告警——相机离线/画面冻结/恢复、存储健康变化、移动侦测、AI 检测事件、门铃、GB28181 设备告警、相机接入——转发到任意外部 Webhook 接收端：自托管告警中心、聊天机器人（企微/钉钉/飞书/Telegram/Slack）、监控平台等。

NVR 只负责报告事实。去重、聚合、冷却、升级与渠道路由由接收端完成——这使得转发通道简单且丢包安全：接收端宕机永远不会阻塞录像或 NVR 的任何其他功能。

## 概览

- **传输**：每条告警一个 HTTP `POST`，JSON 体（`Content-Type: application/json`）
- **鉴权**：接收端的路径 token（配置 URL 的一部分），静态加密存储
- **投递语义**：每个接收端独立有界队列（满时丢最旧）、瞬时失败有限重试（5s / 30s / 2m）、4xx 永不重试、每分钟发送上限（默认 30）
- **重启生效**：转发器在启动时构建

## 告警报文（schema `mibee-alert/1`）

```json
{
  "schema": "mibee-alert/1",
  "event_id": "0d7f0f9e-6f5c-4d8e-9a3f-7e5b1c2d3a4b",
  "occurred_at": "2026-10-25T04:05:06.000000007Z",
  "type": "camera.connection",
  "severity": "error",
  "title": "Camera offline: Studio",
  "body": "No frames for 30 seconds",
  "source_ref": "cam-fa049182",
  "dedup_key": "mibee-nvr/connection/cam-fa049182",
  "resolved": false,
  "snapshot_url": "http://192.168.1.10:9090/api/ai/events/42/snapshot?api_key=mbv_...",
  "tags": { "camera_name": "Studio" }
}
```

| 字段 | 含义 |
|------|------|
| `schema` | 报文版本标记。双方对未知字段一律忽略；词汇表只增不改语义 |
| `event_id` | 每次发送的 UUID（投递/审计标记，非语义身份） |
| `occurred_at` | RFC3339Nano UTC，事件发生时间（非发送时间） |
| `type` | 下方词汇表中的告警类型。接收端对未知类型应走默认规则而非拒绝 |
| `severity` | `info` \| `warning` \| `error` \| `critical` |
| `title` / `body` | 简短事实文本（含相机名）。渠道文案的本地化是接收端的职责 |
| `source_ref` | 相机类告警 = NVR 相机 ID（系统级为 `system` / `storage`）——推荐的去重/路由维度 |
| `dedup_key` | 仅状态型配对事件设置；恢复侧携带相同 key 且 `resolved: true` |
| `resolved` | `true` 表示恢复事件，应关闭同 `dedup_key` 的未决告警 |
| `snapshot_url` | 可选的快照链接（见下） |
| `tags` | 可选富化信息（`camera_name`、`source`）；未知 tag 请透传 |

### 事件类型

| 类型 | 覆盖 | 配对 |
|------|------|------|
| `camera.connection` | 离线 / 恢复在线 | lost↔restored 同 `dedup_key` 配对 |
| `camera.freeze` | 画面冻结 / 恢复 | detected↔recovered 配对 |
| `camera.stream` | 一次性码流异常 | — |
| `camera.quality` | 画质切换（HD↔SD） | — |
| `camera.motion` | 相机侧移动侦测（ONVIF MotionAlarm） | —（高频；接收端聚合） |
| `ai.detection` | AI 后端事件（`POST /api/ai/events`） | — |
| `doorbell.ring` | 门铃生命周期 | ring→接听/拒接/结束 即恢复 |
| `storage.health` | 存储健康变化 | 故障↔恢复 配对 |
| `gb28181.alarm` | GB/T 28181 设备告警 | — |
| `camera.added` | 相机注册接入 | — |
| `notify.test` | 设置面板的测试按钮 | — |

## 快照链接

告警可携带一个接收端（或经其 App 的用户）可直接拉取的链接：AI 事件的分析帧、门铃通话帧或相机实时帧。链接由基址 + 专用 `mbv_` API key 拼出：

- **基址**：`notifications.base_url`，留空回退 `server.advertise_url`（与 ONVIF 推送事件共用的外部可达地址）。两者都空 = 告警照发但无快照链接。
- **API key**：`notifications.api_key`——请使用单独的、可撤销的 key（在 设置 → AI / MiBeeVision 生成）。key 内嵌在 URL 中且接收端会存储 URL，专用 key 可缩小泄漏影响面。

服务端点是现有的鉴权端点（均支持 `?api_key=`）：

- `GET /api/ai/events/{id}/snapshot` — AI 事件的存档帧
- `GET /api/doorbell/calls/{callID}/snapshot` — 门铃来电帧
- `GET /api/cameras/{id}/snapshot` — 实时新帧（侦测类告警最接近事件时刻的画面）

## 配置

```yaml
notifications:
  enabled: true
  base_url: "http://192.168.1.10:9090"   # 快照链接基址；留空回退 server.advertise_url
  api_key: "mbv_..."                      # 内嵌进快照链接的专用 key（支持 encrypt-config）
  sinks:
    - name: "alert-hub"                   # 展示名（日志 + 状态端点）
      url: "http://192.168.1.20:9080/api/ingest/webhook"  # 接收端基址
      token: "my-secret-token"            # 拼接为路径最后一段（支持 encrypt-config）
      events: []                          # 类型过滤（空 = 全部）
      severity_min: "info"                # 低于该级别的告警丢弃
      timeout_s: 5                        # 单请求超时
      burst_per_min: 30                   # 每分钟发送上限
```

每个 sink 拥有独立队列与投递 worker——一个接收端宕机不影响其他接收端；按 sink 的 `events` / `severity_min` 过滤可实现「付费短信通道只收 critical、聊天机器人全收」这类分流。

设置 → 通知 面板用 Web UI 管理同一配置节：接收端列表编辑、事件类型勾选、实时投递计数，以及一个绕过过滤直达每个接收端的**发送测试告警**按钮。

## API

| 端点 | 用途 |
|------|------|
| `GET /api/notifications/status` | 启用状态、每 sink 投递计数（送达/失败/丢弃/队列深度）、事件类型词汇表 |
| `POST /api/notifications/test` | 向每个 sink 入队一条 `notify.test` 告警（体：`{severity?, title?}`）。`202` = 已入队——结果看 status 计数 |

## 接收端实现要点

- 以 `source_ref` + `type` 作为去重维度；仅在 `dedup_key` 存在时做配对。
- `resolved: true` 的告警应关闭匹配的未决告警，而不是新开一条。
- `camera.motion` / `ai.detection` 的量受 NVR 侧发送上限约束但仍可能成串——接收端聚合窗口体验最好。
- `snapshot_url` 是带 query key 的普通 HTTP GET；可放心缓存（AI/门铃帧不可变，相机帧每次刷新）。
