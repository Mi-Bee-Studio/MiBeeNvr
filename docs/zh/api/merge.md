# 合并与延时摄影配置 API

## 合并 API

### 获取合并状态

**端点：** `GET /api/merge/status`

获取当前合并管理器的状态和统计信息。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/merge/status"
```

**响应：**
```json
{
  "enabled": true,
  "error_count": 0,
  "files_created": 9,
  "last_run_time": "2026-05-11T06:37:41Z",
  "segments_merged": 235
}
```

### 获取待合并数量

**端点：** `GET /api/merge/pending`

获取每台摄像头待合并的片段数量。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/merge/pending"
```

**响应：**
```json
{
  "pending": {
    "front-door": 99,
    "backyard": 145
  }
}
```

## 摄像头合并配置

### 更新摄像头合并配置

**端点：** `PUT /api/cameras/{id}/merge-config`

为特定摄像头设置合并配置覆盖项。

**请求体：**
```json
{
  "enabled": true,
  "check_interval": "30m",
  "window_size": "1h",
  "batch_limit": 150,
  "min_segment_age": "5m",
  "min_segments_to_merge": 2
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": false,
    "batch_limit": 50
  }' \
  "http://localhost:9090/api/cameras/front-door/merge-config"
```

**响应：**
```json
{
  "status": "updated"
}
```

### 删除摄像头合并配置

**端点：** `DELETE /api/cameras/{id}/merge-config`

移除摄像头的合并配置覆盖项。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/front-door/merge-config"
```

**响应：**
```json
{
  "status": "reset"
}
```

## 摄像头延时摄影配置

### 获取摄像头延时摄影配置

**端点：** `GET /api/cameras/{id}/timelapse`

获取摄像头的延时摄影配置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/timelapse"
```

**响应：**
```json
{
  "enabled": false,
  "interval": "30s",
  "output_fps": 30,
  "video_codec": "h264",
  "delete_original": false
}
```

### 更新摄像头延时摄影配置

**端点：** `PUT /api/cameras/{id}/timelapse`

更新摄像头的延时摄影配置。

**请求体：**
```json
{
  "enabled": true,
  "interval": "10s",
  "output_fps": 15,
  "video_codec": "h264",
  "delete_original": false,
  "merge_mode": "auto",
  "merge_output_fps": 30
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "interval": "10s",
    "output_fps": 15
  }' \
  "http://localhost:9090/api/cameras/front-door/timelapse"
```

**响应：** 返回更新后的完整摄像头配置数组。
