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
  "last_seen": "2026-08-17T12:00:00Z"
}
```

`last_seen` 在从未收到心跳时省略。只有 `healthy` 为 `true` 时 NVR 才会向
Vision 推送视频段；心跳恢复后，错过的段会被自动补偿重推。

### Vision 心跳上报

**端点：** `POST /api/vision/heartbeat`（公开端点，无需认证）

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
