# 统计与设置 API

## 系统统计

**端点：** `GET /api/stats`

获取系统统计信息，包括存储使用量和录制数量。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats"
```

**响应：**
```json
{
  "total_bytes": 1073741824,
  "used_bytes": 536870912,
  "recording_count": 1000,
  "camera_count": 4
}
```

## 统计趋势

**端点：** `GET /api/stats/trends`

获取存储使用量随时间变化的趋势。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats/trends"
```

**响应：**
```json
{
  "trends": [
    {
      "date": "2024-01-01",
      "total_bytes": 1000000000,
      "used_bytes": 500000000,
      "recording_count": 950
    }
  ]
}
```

## 按相机存储统计

**端点：** `GET /api/stats/cameras`

每路相机的录像段数与磁盘占用（仪表盘「存储趋势」子页的数据源；2 分钟缓存）。

**响应：**
```json
[
  {
    "camera_id": "front-door",
    "camera_name": "前门",
    "archived": false,
    "recordings": 1204,
    "total_bytes": 68945475584
  }
]
```

## 存储候选卷管理

### 列出候选卷

**端点：** `GET /api/storage/candidates`

**响应：**
```json
{
  "current": "/var/lib/mibee-nvr",
  "candidates": [
    {"path": "/var/lib/mibee-nvr", "label": "current"},
    {"path": "/media/nvr-recordings", "label": "nvr-recordings"}
  ],
  "restart_hint": "切换立即生效：新录像将写入所选位置（无需重启）",
  "env_managed": false
}
```

> `env_managed=true` 表示候选由部署平台管理（如飞牛授权目录，经 `NVR_STORAGE_CANDIDATES` 注入）——手动添加的路径重启后以平台列表为准。

### 添加候选卷

**端点：** `POST /api/storage/candidates`

**请求体：**
```json
{"path": "/mnt/newdisk"}
```

**响应（200 OK）：** `{"status": "added", "path": "/mnt/newdisk"}`

校验：路径须为绝对目录、已存在、可写；当前根与已被按相机覆盖占用的路径会被拒绝（400）。

### 移除候选卷

**端点：** `DELETE /api/storage/candidates?path=/mnt/newdisk`

**响应：** `{"status": "removed", "path": "/mnt/newdisk"}`（当前根不可移除）

## 批量录像迁移

### 一键换盘

**端点：** `POST /api/storage/migrate`

热切换默认存储根 + 清除全部按相机覆盖 + 每路有历史录像的相机排入迁移队列（全程无需重启）。

**请求体：**
```json
{"target": "/mnt/newdisk", "delete_source": true}
```

**响应（202 Accepted）：**
```json
{
  "status": "updated",
  "target": "/mnt/newdisk",
  "jobs_enqueued": 3
}
```

### 迁移状态

**端点：** `GET /api/storage/migrate/status`

**响应：**
```json
{
  "state": "running",
  "jobs": [
    {
      "camera_id": "backyard",
      "to_root": "/mnt/newdisk",
      "state": "running",
      "total_files": 1200,
      "done_files": 512,
      "total_bytes": 20971520000,
      "done_bytes": 8928000000
    }
  ]
}
```

任务 `state`：`queued`（排队）/ `running`（迁移中）/ `paused`（等待迁移时间窗）/ `done` / `failed`。

> 单相机粒度的存储根设置见[摄像头 API](cameras.md#按相机存储根)；整体说明见[存储管理](../storage-management.md)。

## 获取设置

**端点：** `GET /api/settings`

获取当前配置设置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings"
```

**响应：**
```json
{
  "cleanup": {
    "retention_days": 30,
    "check_interval": "1h",
    "disk_threshold_percent": 85
  },
  "webdav": {
    "enabled": true,
    "path_prefix": "/dav",
    "read_write": false
  },
  "auth": {
    "username": "admin",
    "auth_configured": true
  },
  "mibeevision": {
    "api_keys": [
      {
        "name": "vision",
        "prefix": "mbv_1a2b…",
        "revoked": false,
        "last_used": "2026-08-17T02:00:00Z"
      }
    ]
  },
  "timezone": "Local",
  "timezone_display": "CST (UTC+8)",
  "server": {
    "listen": ":9090"
  },
  "gb28181": {
    "enabled": false,
    "sip_listen": ":5060",
    "server_id": "34020000002000000001",
    "realm": "3402000000",
    "password_configured": true,
    "port_range": "30000-30050",
    "allowed_device_ids": [],
    "heartbeat_interval": "60s",
    "catalog_interval": "30m",
    "tcp_mode": false,
    "tcp_framing": "auto",
    "media_transport": "udp",
    "sip_transport": "udp",
    "subscribe_catalog": true,
    "subscribe_alarm": false,
    "subscribe_mobile_position": false,
    "subscribe_expires": "3600s"
  }
}
```

`mibeevision.api_keys` 永远不会返回完整密钥（只有前缀）；`last_used`
为该密钥最近一次被使用的 UTC 时间（每分钟粒度），从未使用过的密钥省略该字段。

## MiBeeVision 集成状态

### 查询 Vision 消费端健康

**端点：** `GET /api/vision/status`

查询外部 AI 处理端（MiBeeVision）的连接健康状况，供 Web UI 展示。
未启用 Vision 集成时返回 `{"enabled": false}`。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/vision/status"
```

**响应：**
```json
{
  "enabled": true,
  "healthy": true,
  "device": "jetson-orin",
  "queue_depth": 0,
  "processed": 12841,
  "skip_cameras": [],
  "last_seen": "2026-08-17T12:00:00Z",
  "drops_marked_total": 5,
  "instances": [
    {
      "name": "default",
      "url": "http://127.0.0.1:8080",
      "healthy": true,
      "device": "jetson-orin",
      "queue_depth": 0,
      "processed": 12841,
      "last_seen": "2026-08-17T12:00:00Z",
      "drops_marked_total": 5,
      "push_state": "closed",
      "push_fails": 0
    }
  ]
}
```

顶层字段保持单实例时代的形状（即 `default` 实例）；多实例部署会附加
`instances[]` 数组逐实例展开（含推送熔断状态 `push_state`：`closed` /
`open` / `half-open`，与连续失败数 `push_fails`）。`last_seen` 在从未收到
心跳时省略。只有 `healthy` 为 `true` 时 NVR 才会向 Vision 推送视频段；
心跳恢复后，错过的段会被自动补偿重推。

### Vision 心跳上报

**端点：** `POST /api/vision/heartbeat`（需认证：API Key / BasicAuth / 会话令牌——心跳携带 SkipCameras、drops 等状态写操作）

Vision 服务每 30 秒上报一次。请求体：

```json
{
  "status": "ok",
  "device": "jetson-orin",
  "queue_depth": 0,
  "processed_count": 12841
}
```

**响应：**
```json
{
  "ok": true,
  "push_enabled": true
}
```

> 心跳 v2 请求体另有两个可选块：`drops`（批量丢弃报告，响应回 `ack_drops` 确认已消费，受影响录像标记为 `ai_status=skipped`）与 `metrics`（运行指标快照，进入 `GET /api/vision/metrics` 查询的历史环）。

### Vision 运行指标

**端点：** `GET /api/vision/metrics`

查询心跳历史采样环（内存保留，约 24 小时 @ 每 30 秒一次心跳），供仪表盘趋势图绘制。未启用 Vision 集成时返回 `{"enabled": false, "points": [], "marked_total": 0}`。

**查询参数：**

| 参数 | 类型 | 必填 | 说明 | 示例 |
|-----------|------|----------|-------------|---------|
| `hours` | integer | 否 | 回溯时长（1–168，默认 24；超出范围被钳制） | `12` |
| `instance` | string | 否 | 实例名（多实例部署时指定，默认 `default`） | `jetson-orin` |

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/vision/metrics?hours=12"
```

**响应：**
```json
{
  "enabled": true,
  "instance": "default",
  "points": [
    {
      "ts": "2026-08-17T11:30:00Z",
      "queue_depth": 2,
      "processed_count": 12841,
      "dropped_total": 0,
      "decode_workers": 2,
      "workers_busy": 1,
      "events_emitted": 3104
    }
  ],
  "marked_total": 5
}
```

`points` 内是逐次心跳的采样点；`marked_total` 为该实例累计已标记跳过（`ai_status=skipped`）的录像数。指定的实例不存在时返回空 `points`。

## RTSP 输出设置

内置 RTSP 输出服务（`rtsp://<host>:<port>/<camera_id>` 拉流地址，供第三方平台当作摄像头源接入）的开关、端口与凭据。

### 获取 RTSP 输出设置

**端点：** `GET /api/settings/rtsp-output`

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings/rtsp-output"
```

**响应：**
```json
{
  "enabled": true,
  "port": 8554,
  "username": "viewer",
  "password_configured": true
}
```

密码永不回显，只返回 `password_configured` 标志。

### 更新 RTSP 输出设置

**端点：** `PUT /api/settings/rtsp-output`

所有字段可选，支持部分更新。

**请求体：**

| 字段 | 类型 | 说明 |
|------|------|------|
| `enabled` | bool | 启用 / 停用 RTSP 输出服务（缺省为启用） |
| `port` | integer | 监听端口（1–65535，否则 400） |
| `username` | string | 拉流认证用户名（留空 = 保持不变） |
| `password` | string | 拉流认证密码（**留空 = 保持当前密码**；GET 不回显，UI 未改动时原样回传空串） |
| `clear_credentials` | bool | 一次清空用户名 + 密码（开放访问）；单独的 `password` 字段无法表达「清除」 |

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "port": 8554,
    "username": "viewer",
    "password": "new-secret"
  }' \
  "http://localhost:9090/api/settings/rtsp-output"
```

**响应：**
```json
{
  "status": "updated",
  "restart_required": true
}
```

> RTSP 服务在构造时读取配置快照，任何修改**重启后生效**——响应恒带 `restart_required: true` 供 UI 提示。

## 更新设置

**端点：** `PUT /api/settings`

更新配置设置。

**请求体：**
```json
{
  "cleanup": {
    "retention_days": 60,
    "disk_threshold_percent": 90,
    "check_interval": "30m"
  }
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "cleanup": {
      "retention_days": 60,
      "disk_threshold_percent": 90,
      "check_interval": "30m"
    }
  }' \
  "http://localhost:9090/api/settings"
```

**响应：**
```json
{
  "status": "updated"
}
```

## 获取合并设置

**端点：** `GET /api/settings/merge`

获取全局合并设置配置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings/merge"
```

**响应：**
```json
{
  "enabled": true,
  "check_interval": "1h",
  "window_size": "1h",
  "batch_limit": 200,
  "min_segment_age": "10m",
  "min_segments_to_merge": 3
}
```

## 更新合并设置

**端点：** `PUT /api/settings/merge`

更新全局合并设置。

**请求体：**
```json
{
  "enabled": true,
  "check_interval": "30m",
  "window_size": "2h",
  "batch_limit": 100,
  "min_segment_age": "15m",
  "min_segments_to_merge": 5
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "check_interval": "30m",
    "batch_limit": 100
  }' \
  "http://localhost:9090/api/settings/merge"
```

**响应：**
```json
{
  "status": "updated"
}
```

## 流媒体与转码设置 API

## 获取流媒体设置

**端点：** `GET /api/settings/streaming`

获取当前流媒体配置，包括默认协议、WebRTC、FLV 和 HLS 低延迟设置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings/streaming"
```

**响应：**
```json
{
  "default_protocol": "webrtc",
  "webrtc": {
    "enabled": true,
    "max_viewers": 10,
    "idle_timeout": "5m"
  },
  "flv": {
    "enabled": true,
    "max_viewers": 10,
    "idle_timeout": "5m",
    "gop_cache_size": 25
  },
  "hls": {
    "low_latency": true
  }
}
```

## 更新流媒体设置

**端点：** `PUT /api/settings/streaming`

更新流媒体配置。所有字段均为可选，支持部分更新。

**请求体：**
```json
{
  "default_protocol": "flv",
  "webrtc": {
    "enabled": true,
    "max_viewers": 5,
    "idle_timeout": "10m"
  },
  "flv": {
    "enabled": true,
    "max_viewers": 5,
    "idle_timeout": "10m",
    "gop_cache_size": 50
  },
  "hls": {
    "low_latency": false
  }
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "default_protocol": "flv",
    "webrtc": {
      "max_viewers": 5
    }
  }' \
  "http://localhost:9090/api/settings/streaming"
```

**响应：**
```json
{
  "status": "updated"
}
```

## 获取转码设置

**端点：** `GET /api/settings/transcoding`

获取全局转码配置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings/transcoding"
```

**响应：**
```json
{
  "enabled": true,
  "max_workers": 2
}
```

## 更新转码设置

**端点：** `PUT /api/settings/transcoding`

更新全局转码配置。

**请求体：**
```json
{
  "enabled": true,
  "max_workers": 2
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "max_workers": 2
  }' \
  "http://localhost:9090/api/settings/transcoding"
```

**响应：**
```json
{
  "status": "updated"
}
```
