# 事件 API

## 事件流（SSE）

**端点：** `GET /api/events`

服务端推送事件（SSE）端点，从全局 EventBus 推送事件。支持可选的主题筛选。

**查询参数：**

| 参数 | 类型 | 必填 | 描述 | 示例 |
|-----------|------|----------|-------------|---------|
| `filter` | string | 否 | 按主题前缀筛选事件 | `onvif.` |

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/events?filter=onvif."
```

**响应：** SSE 流（`text/event-stream`），15 秒心跳保活。

```
event: onvif.discovery
data: {"topic":"onvif.discovery","data":{"device":"192.168.1.104"}}

: ping
```

## 摄像头事件流（SSE）

**端点：** `GET /api/cameras/{id}/events`

服务端推送事件（SSE）端点，从事件总线推送特定摄像头的事件。通过事件数据中的摄像头 ID 进行筛选。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/events"
```

**响应：** SSE 流（`text/event-stream`），15 秒心跳保活。

```
event: camera.status
data: {"topic":"camera.status","data":{"camera_id":"front-door","status":"recording"}}

: ping
```

## 遥测

**端点：** `POST /api/telemetry`

提交播放遥测事件。每个 IP 限制为 10 次请求/秒。需要认证。

**请求体：**
```json
{
  "event": "playback_start",
  "camera_id": "front-door",
  "duration_ms": 15000,
  "details": {
    "protocol": "hls",
    "quality": "high"
  }
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "event": "playback_start",
    "camera_id": "front-door",
    "duration_ms": 15000
  }' \
  "http://localhost:9090/api/telemetry"
```

**响应：** 成功时返回 `204 No Content`。
