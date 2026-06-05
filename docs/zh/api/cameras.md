# 摄像头 API

## 摄像头管理

### 列出摄像头

**端点：** `GET /api/cameras`

获取所有已配置摄像头的列表。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras"
```

**响应：**
```json
[
  {
    "id": "front-door",
    "name": "Front Door",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "enabled": true,
    "status": "recording",
    "last_seen": "2024-01-01T10:15:00Z",
    "retention_days": 30,
    "username": "admin",
    "has_password": true,
    "sub_stream_url": "",
    "snapshot_url": "",
    "sample_interval": 1,
    "hls_max_fps": 30,
    "did": "",
    "vendor": ""
  }
]
```

### 创建摄像头

**端点：** `POST /api/cameras`

添加新的摄像头配置。

**请求体：**
```json
{
  "name": "Front Door",
  "protocol": "rtsp",
  "encoding": "h264", 
  "url": "rtsp://192.168.1.100:554/stream",
  "username": "admin",
  "password": "secret",
  "enabled": true,
  "retention_days": 30,
  "sub_stream_url": "rtsp://192.168.1.100:554/sub_stream",
  "snapshot_url": "http://192.168.1.100:8080/snapshot",
  "sample_interval": 1,
  "hls_max_fps": 30
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Front Door",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "username": "admin",
    "password": "secret",
    "enabled": true
  }' \
  "http://localhost:9090/api/cameras"
```

**响应（201 Created）：**
```json
{
  "id": "front-door",
  "name": "Front Door",
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "enabled": true
}
```

### 获取摄像头

**端点：** `GET /api/cameras/{id}`

获取指定摄像头的配置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door"
```

**响应：**
```json
{
  "id": "front-door",
  "name": "Front Door", 
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "enabled": true,
  "status": "recording",
  "last_seen": "2024-01-01T10:15:00Z"
}
```

### 更新摄像头

**端点：** `PUT /api/cameras/{id}`

更新摄像头配置。所有字段均为可选，支持部分更新。

**请求体：**
```json
{
  "name": "Updated Front Door",
  "url": "rtsp://192.168.1.100:554/new_stream",
  "enabled": false,
  "retention_days": 7
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Updated Front Door",
    "url": "rtsp://192.168.1.100:554/new_stream",
    "enabled": false
  }' \
  "http://localhost:9090/api/cameras/front-door"
```

**响应：**
```json
{
  "id": "front-door",
  "name": "Updated Front Door",
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/new_stream",
  "enabled": false
}
```

### 删除摄像头

**端点：** `DELETE /api/cameras/{id}`

删除摄像头配置。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/backyard"
```

**响应：**
```json
{
  "status": "deleted"
}
```

### 测试连接

**端点：** `POST /api/cameras/test-connection`

使用提供的配置测试摄像头连接。

**请求体：**
```json
{
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "username": "admin", 
  "password": "secret"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "username": "admin",
    "password": "secret"
  }' \
  "http://localhost:9090/api/cameras/test-connection"
```

**响应：**
```json
{
  "success": true,
  "message": "Connection successful",
  "details": {
    "protocol": "rtsp",
    "encoding": "h264",
    "latency_ms": 45,
    "frames_received": 10
  }
}
```

### 启动摄像头

**端点：** `POST /api/cameras/{id}/start`

启动摄像头的录制。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/front-door/start"
```

**响应：**
```json
{
  "status": "started"
}
```

### 停止摄像头

**端点：** `POST /api/cameras/{id}/stop`

停止摄像头的录制。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/front-door/stop"
```

**响应：**
```json
{
  "status": "stopped"
}
```

## 摄像头快照

**端点：** `GET /api/cameras/{id}/snapshot`

从摄像头获取 JPEG 快照图像。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/snapshot" \
  -o snapshot.jpg
```

**响应：** JPEG 图像，`Content-Type: image/jpeg`，`Cache-Control: max-age=5`
