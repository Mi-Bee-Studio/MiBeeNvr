# 录制 API

## 查询录制列表

**端点：** `GET /api/recordings`

获取分页的录制列表，支持可选过滤条件。

**查询参数：**

| 参数 | 类型 | 必填 | 说明 | 示例 |
|-----------|------|----------|-------------|---------|
| `camera_id` | string | 否 | 按摄像头 ID 过滤 | `front-door` |
| `format` | string | 否 | 按格式过滤（h264, h265, mjpeg） | `h264` |
| `merged` | boolean | 否 | 按合并状态过滤 | `true` |
| `start` | string | 否 | 开始时间（RFC3339 格式） | `2024-01-01T00:00:00Z` |
| `end` | string | 否 | 结束时间（RFC3339 格式） | `2024-01-02T00:00:00Z` |
| `limit` | integer | 否 | 最大结果数（默认：50） | `20` |
| `offset` | integer | 否 | 分页偏移量 | `0` |

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings?format=h264&limit=10&offset=0"
```

**响应：**
```json
{
  "recordings": [
    {
      "id": "1704123456789012345",
      "camera_id": "front-door",
      "file_path": "/data/recordings/h264/front-door_1704123456789012345.mp4",
      "format": "h264",
      "started_at": "2024-01-01T12:34:56.789Z",
      "ended_at": "2024-01-01T12:35:06.789Z",
      "duration": 10.0,
      "file_size": 1048576,
      "frame_count": 300,
      "merged": false
    }
  ],
  "total": 1
}
```

## 获取单个录制

**端点：** `GET /api/recordings/{id}`

根据 ID 获取特定的录制记录。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345"
```

**响应：**
```json
{
  "id": "1704123456789012345",
  "camera_id": "front-door",
  "file_path": "/data/recordings/h264/front-door_1704123456789012345.mp4",
  "format": "h264",
  "started_at": "2024-01-01T12:34:56.789Z",
  "ended_at": "2024-01-01T12:35:06.789Z",
  "duration": 10.0,
  "file_size": 1048576,
  "frame_count": 300,
  "merged": false
}
```

## 删除录制

**端点：** `DELETE /api/recordings/{id}`

根据 ID 删除录制。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/recordings/1704123456789012345"
```

**响应：**
```json
{
  "status": "deleted"
}
```

## 下载录制

**端点：** `GET /api/recordings/{id}/download`

下载录制文件。

**查询参数：**

| 参数 | 类型 | 必填 | 说明 | 示例 |
|-----------|------|----------|-------------|---------|
| `frame` | integer | 否 | MJPEG 格式时，下载指定帧 | `150` |

**请求（H264）：**
```bash
curl -u username:password \
  -o recording.mp4 \
  "http://localhost:9090/api/recordings/1704123456789012345/download"
```

**请求（MJPEG 指定帧）：**
```bash
curl -u username:password \
  -o frame_150.jpg \
  "http://localhost:9090/api/recordings/1704123456789012345/download?frame=150"
```

**响应：** 二进制文件内容（MP4 或 JPEG）

## 查询录制帧列表（仅 MJPEG）

**端点：** `GET /api/recordings/{id}/frames`

列出 MJPEG 录制的所有帧。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345/frames"
```

**响应：**
```json
{
  "frames": [
    {
      "index": 0,
      "filename": "front-door_1704123456789012345_0000.jpg",
      "size": 54321
    },
    {
      "index": 1,
      "filename": "front-door_1704123456789012345_0001.jpg",
      "size": 52345
    }
  ]
}
```

## 批量删除录制

**端点：** `POST /api/recordings/batch-delete`

根据 ID 批量删除录制。

**请求体：**
```json
{
  "recording_ids": ["id1", "id2", "id3"]
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "recording_ids": ["1704123456789012345", "1704123456789012346"]
  }' \
  "http://localhost:9090/api/recordings/batch-delete"
```

**响应：**
```json
{
  "deleted": 2,
  "failed": 0
}
```

## 下载合并录制

**端点：** `GET /api/recordings/{id}/merged`

下载延时摄影录制的合并 MP4 文件。通过 `http.ServeFile()` 提供服务，支持范围请求。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345/merged"
```

**响应：** 二进制 MP4 文件内容。若无可用合并录制则返回 404。

## 查询延时摄影帧列表

**端点：** `GET /api/recordings/{id}/timelapse-frames`

列出延时摄影录制的 JPEG 帧。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345/timelapse-frames"
```

**响应：**
```json
[
  {
    "filename": "frame_0001.jpg",
    "url": "/api/recordings/1704123456789012345/timelapse-frames/frame_0001.jpg",
    "size": 54321,
    "timestamp": "2024-01-01T12:34:56Z"
  },
  {
    "filename": "frame_0002.jpg",
    "url": "/api/recordings/1704123456789012345/timelapse-frames/frame_0002.jpg",
    "size": 52345,
    "timestamp": "2024-01-01T12:35:06Z"
  }
]
```

## 获取延时摄影帧

**端点：** `GET /api/recordings/{id}/timelapse-frames/{filename}`

获取延时摄影录制中的指定 JPEG 帧。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345/timelapse-frames/frame_0001.jpg" \
  -o frame_0001.jpg
```

**响应：** JPEG 图片二进制数据。无效文件名则返回 400（路径遍历防护）。
