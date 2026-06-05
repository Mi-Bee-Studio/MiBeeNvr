# 健康监控 API

## 摄像头健康状态概览

**端点：** `GET /api/health/cameras`

获取聚合的摄像头健康状态概览。公开端点（频率受限）。

**请求：**
```bash
curl http://localhost:9090/api/health/cameras
```

**响应：**
```json
{
  "total": 4,
  "recording": 3,
  "reconnecting": 0,
  "error": 0,
  "offline": 1,
  "details": [
    {
      "id": "front-door",
      "name": "Front Door",
      "status": "recording",
      "score": 95
    },
    {
      "id": "backyard",
      "name": "Backyard",
      "status": "offline",
      "score": 0
    }
  ]
}
```

## 详细健康监控状态

**端点：** `GET /api/health/status`

获取所有摄像头的详细健康监控状态。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/health/status"
```

**响应：**
```json
{
  "front-door": {
    "status": "recording",
    "last_seen": "2024-01-01T12:34:56Z",
    "error_count": 0,
    "uptime": "2h34m15s"
  }
}
```

## 近期健康事件

**端点：** `GET /api/health/events`

从数据库获取分页的健康事件。

**查询参数：**

| 参数 | 类型 | 必填 | 说明 | 示例 |
|-----------|------|----------|-------------|---------|
| `camera_id` | string | 否 | 按摄像头 ID 筛选 | `front-door` |
| `event_type` | string | 否 | 按事件类型筛选 | `disconnected` |
| `since` | string | 否 | 返回此时间戳之后的事件 | `2024-01-01T00:00:00Z` |
| `limit` | integer | 否 | 最大结果数（默认：50） | `20` |
| `offset` | integer | 否 | 分页结果偏移量 | `0` |

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/health/events?camera_id=front-door&limit=10"
```

**响应：**
```json
{
  "events": [
    {
      "id": 1,
      "camera_id": "front-door",
      "event_type": "disconnected",
      "message": "Camera disconnected",
      "timestamp": "2024-01-01T12:34:56Z"
    }
  ],
  "total": 1
}
```

## 摄像头稳定性评分

**端点：** `GET /api/health/stability`

获取所有摄像头的稳定性质量评分。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/health/stability"
```

**响应：**
```json
{
  "cameras": {
    "front-door": {
      "score": 95,
      "uptime_percentage": 99.5,
      "total_events": 2
    },
    "backyard": {
      "score": 70,
      "uptime_percentage": 85.0,
      "total_events": 15
    }
  }
}
```

## 单个摄像头稳定性

**端点：** `GET /api/health/stability/{camera_id}`

获取单个摄像头的稳定性质量数据。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/health/stability/front-door"
```

**响应：**
```json
{
  "score": 95,
  "uptime_percentage": 99.5,
  "total_events": 2
}
```

## 摄像头特定健康信息

**端点：** `GET /api/cameras/{id}/health`

获取指定摄像头的健康状态。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/health"
```

**响应：**
```json
{
  "status": "recording",
  "last_seen": "2024-01-01T12:34:56Z",
  "error_count": 0,
  "uptime": "2h34m15s"
}
```
