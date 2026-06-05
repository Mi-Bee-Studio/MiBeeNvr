# AI 检测 API

## AI 检测状态

**端点：** `GET /api/ai/status`

获取 AI 引擎的可用性、状态和模型信息。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/ai/status"
```

**响应：**
```json
{
  "available": true,
  "engine_status": "running",
  "model": "/var/lib/mibee-nvr/models/ai/model.onnx",
  "probe": {}
}
```

## 启用 AI 检测

**端点：** `POST /api/ai/enable`

为特定摄像头启用 AI 检测。摄像头必须处于运行状态且具有活跃的 StreamHub。

**请求体：**
```json
{
  "camera_id": "front-door"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door"
  }' \
  "http://localhost:9090/api/ai/enable"
```

**响应：**
```json
{
  "status": "enabled",
  "camera_id": "front-door"
}
```

## 禁用 AI 检测

**端点：** `POST /api/ai/disable`

禁用特定摄像头的 AI 检测。幂等操作——即使 AI 未启用也返回成功。

**请求体：**
```json
{
  "camera_id": "front-door"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door"
  }' \
  "http://localhost:9090/api/ai/disable"
```

**响应：**
```json
{
  "status": "disabled",
  "camera_id": "front-door"
}
```

## AI 检测事件（SSE）

**端点：** `GET /api/ai/events`

服务端推送事件（SSE）端点，实时向前端推送 AI 检测结果。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/ai/events"
```

**响应：** SSE 流（`text/event-stream`）。15 秒心跳保活。事件为 JSON 编码的检测结果。

```
data: {"camera_id":"front-door","timestamp":"2024-01-01T12:34:56Z","detections":[{"label":"person","confidence":0.95,"bbox":{"x":100,"y":200,"width":50,"height":100}}]}

: ping
```
