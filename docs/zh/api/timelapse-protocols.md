# 延时摄影与协议 API

## 延时摄影 API

### 延时摄影管理器状态

**端点：** `GET /api/timelapse/status`

获取全局延时摄影管理器状态和默认合并设置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/timelapse/status"
```

**响应：**
```json
{
  "merge_enabled": false,
  "merge_mode": "auto",
  "daily_merge": true,
  "merge_output_fps": 30
}
```

## 协议 API

### 获取支持的协议

**端点：** `GET /api/protocols`

获取所有支持的摄像头协议列表，包括其编码格式和能力。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/protocols"
```

**响应：**
```json
{
  "protocols": [
    {
      "id": "rtsp",
      "label": "RTSP",
      "encodings": ["h264", "h265", "mjpeg"],
      "built_in": true,
      "capabilities": {
        "hls": true,
        "ptz": false,
        "snapshot": false,
        "discovery": false,
        "auth": true
      }
    },
    {
      "id": "http",
      "label": "HTTP JPEG",
      "encodings": ["jpeg"],
      "built_in": true,
      "capabilities": {
        "hls": false,
        "ptz": false,
        "snapshot": true,
        "discovery": false,
        "auth": true
      }
    },
    {
      "id": "onvif",
      "label": "ONVIF",
      "encodings": ["h264", "h265", "mjpeg"],
      "built_in": true,
      "capabilities": {
        "hls": true,
        "ptz": true,
        "snapshot": false,
        "discovery": true,
        "auth": true
      }
    },
    {
      "id": "xiaomi",
      "label": "Xiaomi",
      "encodings": ["h264", "h265"],
      "built_in": true,
      "capabilities": {
        "hls": true,
        "ptz": false,
        "snapshot": false,
        "discovery": true,
        "auth": true
      }
    }
  ]
}
```

## 功能 API

### 获取功能标志

**端点：** `GET /api/features`

获取启用/禁用的协议功能标志。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/features"
```

**响应：**
```json
{
  "protocols": {
    "rtsp": true,
    "onvif": true,
    "xiaomi": false
  }
}
```

### 更新功能标志

**端点：** `PUT /api/features`

更新协议功能标志。

**请求体：**
```json
{
  "protocols": {
    "rtsp": true,
    "xiaomi": false
  }
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "protocols": {
      "rtsp": true,
      "xiaomi": false
    }
  }' \
  "http://localhost:9090/api/features"
```

**响应：**
```json
{
  "protocols": {
    "rtsp": true,
    "onvif": true,
    "xiaomi": false
  }
}
```
