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
  "server": {
    "listen": ":9090"
  },
  "storage": {
    "root_dir": "/var/lib/mibee-nvr",
    "segment_duration": "30s"
  },
  "cleanup": {
    "retention_days": 30,
    "check_interval": "1h",
    "disk_threshold_percent": 95
  },
  "auth": {
    "username": "admin"
  }
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
