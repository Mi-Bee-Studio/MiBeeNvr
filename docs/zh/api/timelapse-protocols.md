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

### 批量获取合并产物帧

**端点：** `GET /api/timelapse/merges/{id}/frames`

从 MJPEG（`mjpa`）周期合并产物中切片一批 JPEG 帧，以 `multipart/mixed` 响应返回（每个 part 一帧）——供 MJPEG 播放器一次拉取 N 帧而非 N 次请求。帧位置来自纯 Go MP4 采样表（`stsz`/`stco`），按需读取，不加载整个文件。仅适用于 `codec=mjpeg` 的合并产物；H.264/H.265 产物用 `<video>` 原生播放。

**查询参数：**

| 参数 | 类型 | 必填 | 说明 | 示例 |
|-----------|------|----------|-------------|---------|
| `offset` | integer | 否 | 起始帧序号（默认 0；负数 / 非数字返回 400） | `120` |
| `limit` | integer | 否 | 本批帧数（默认 120，上限 240，超出钳制；0 / 负数 / 非数字返回 400） | `120` |

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/timelapse/merges/42/frames?offset=0&limit=120" \
  -o batch.txt
```

**响应：** `Content-Type: multipart/mixed; boundary=…`，每个 part 为 `image/jpeg`，part 头带 `X-Frame-Index`。响应头：

| 响应头 | 说明 |
|--------|------|
| `X-Frame-Total` | 合并产物总帧数 |
| `X-Frame-Offset` | 本批起始序号 |
| `X-Frame-Count` | 本批实际帧数 |
| `X-Frame-Fps` | 合并产物输出帧率（配置了 fps 时返回） |
| `Cache-Control` | `no-store` |

> `id` 非正整数返回 400；合并不存在 / 未完成（非 `completed` 状态）/ 非 MJPEG 编码 / 产物文件缺失均返回 404。`offset` 超出总帧数时返回空但合法的 multipart 体。

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
