# 摄像头统计与事件

## 摄像头录制统计

**端点：** `GET /api/cameras/{id}/stats`

获取指定摄像头的录制统计信息。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stats"
```

**响应：**
```json
{
  "recording_count": 150,
  "total_size": 1073741824
}
```

## 摄像头事件流（SSE）

**端点：** `GET /api/cameras/{id}/events`

服务端推送事件（SSE）端点，从事件总线流式传输摄像头特定事件。按摄像头 ID 过滤事件。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/events"
```

**响应：** SSE 流（`text/event-stream`）。15 秒心跳保活。

```text
event: camera.status
data: {"topic":"camera.status","data":{"camera_id":"front-door","status":"recording"}}

: ping
```
