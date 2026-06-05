# 转码 API

## 转码自检

**端点：** `GET /api/transcoding/check`

执行系统转码能力的自检，包括硬件验证、FFmpeg 可用性和性能评估。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/check"
```

**响应：**
```json
{
  "supported": true,
  "ffmpeg_status": "available",
  "encoders": {
    "h264": "libx264",
    "h265": "libx265"
  },
  "decoders": {
    "h264": "h264",
    "h265": "hevc"
  },
  "warnings": [],
  "max_concurrent": 2,
  "estimated_fps": 3.5,
  "total_cores": 4,
  "total_memory_mb": 1024,
  "h264_encoder_type": "software",
  "h265_encoder_type": "software",
  "h264_decoder_type": "software",
  "h265_decoder_type": "software",
  "max_encode_width": 1920,
  "max_encode_height": 1080,
  "devices": [
    {
      "type": "cpu",
      "name": "ARMv7 Processor rev 5 (v7l)"
    }
  ]
}
```

**响应字段说明：**

| 字段 | 类型 | 描述 |
|-------|------|-------------|
| `supported` | boolean | 是否支持转码 |
| `ffmpeg_status` | string | FFmpeg 状态："available"、"downloading"、"not_installed"、"failed" |
| `encoders` | object | 可用的编码器库（h264、h265） |
| `decoders` | object | 可用的解码器库（h264、h265） |
| `warnings` | array[] | 关于限制的可读警告信息 |
| `max_concurrent` | integer | 预估的最大并发转码流数 |
| `estimated_fps` | float | 在此硬件上预估的转码 FPS |
| `total_cores` | integer | 可用的总 CPU 核心数 |
| `total_memory_mb` | integer | 系统总内存（MB） |
| `h264_encoder_type` | string | "software"、"hardware" 或 "unknown" |
| `h265_encoder_type` | string | "software"、"hardware" 或 "unknown" |
| `devices` | array[] | 硬件设备信息 |

## FFmpeg 状态

**端点：** `GET /api/transcoding/ffmpeg/status`

返回当前 FFmpeg 的下载/可用状态。出于安全考虑，不暴露二进制文件路径。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/ffmpeg/status"
```

**响应（已安装）：**
```json
{
  "status": "available",
  "version": "6.0",
  "download_progress": 100
}
```

**响应（未安装）：**
```json
{
  "status": "not_installed",
  "version": "",
  "download_progress": 0
}
```

## 下载 FFmpeg

**端点：** `POST /api/transcoding/ffmpeg/download`

启动后台 FFmpeg 二进制文件下载。幂等操作：如果正在下载或已可用，则返回当前状态。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/transcoding/ffmpeg/download"
```

**响应（已接受 - 开始下载）：**
```json
{
  "status": "downloading",
  "download_progress": 0
}
```

**响应（已可用）：**
```json
{
  "status": "available",
  "version": "6.0"
}
```

**响应（正在下载中）：**
```json
{
  "status": "downloading",
  "download_progress": 45
}
```

## 重试 FFmpeg 下载

**端点：** `POST /api/transcoding/ffmpeg/download/retry`

重试失败的 FFmpeg 下载。仅在状态为 "failed" 时生效。如果下载正在进行中，返回 409 Conflict。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/transcoding/ffmpeg/download/retry"
```

**响应（重试已开始）：**
```json
{
  "status": "downloading",
  "download_progress": 0
}
```

**响应（冲突 - 正在进行中）：**
```json
{
  "error": "download already in progress",
  "status": "downloading"
}
```

## 转码管理器状态

**端点：** `GET /api/transcoding/status`

返回转学子系统的整体状态，包括启用状态、硬件信息、队列长度和活跃任务数。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/status"
```

**响应：**
```json
{
  "enabled": true,
  "hardware": {
    "h264_encoder": "libx264",
    "h265_encoder": "libx265"
  },
  "queue_length": 5,
  "active_jobs": 2,
  "recent_results": []
}
```

## 列出转码任务

**端点：** `GET /api/transcoding/tasks`

返回分页的转码任务列表，支持可选筛选条件。

**查询参数：**

| 参数 | 类型 | 必填 | 描述 | 示例 |
|-----------|------|----------|-------------|---------|
| `status` | string | 否 | 按任务状态筛选 | `pending` |
| `camera_id` | string | 否 | 按摄像头 ID 筛选 | `front-door` |
| `limit` | integer | 否 | 最大结果数（默认：50） | `20` |
| `offset` | integer | 否 | 分页偏移量 | `0` |
| `page` | integer | 否 | 页码（从 1 开始，替代 offset） | `1` |

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/tasks?status=pending&limit=10"
```

**响应：**
```json
{
  "tasks": [
    {
      "id": 1,
      "camera_id": "front-door",
      "recording_id": "1704123456789012345",
      "status": "pending",
      "input_format": "h265",
      "output_format": "h264",
      "created_at": "2024-01-01T12:34:56.789Z"
    }
  ],
  "total": 1,
  "limit": 10,
  "offset": 0,
  "page": 1
}
```

## 创建转码任务

**端点：** `POST /api/transcoding/tasks`

手动加入一个转码任务。验证录制文件存在且摄像头已启用转码。

**请求体：**
```json
{
  "camera_id": "front-door",
  "recording_id": "1704123456789012345",
  "target_codec": "h264"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door",
    "recording_id": "1704123456789012345",
    "target_codec": "h264"
  }' \
  "http://localhost:9090/api/transcoding/tasks"
```

**响应（201 Created）：**
```json
{
  "id": 1,
  "camera_id": "front-door",
  "recording_id": "1704123456789012345",
  "input_path": "/data/recordings/h265/front-door_1704123456789012345.mp4",
  "input_format": "h265",
  "output_path": "/data/recordings/h265/front-door_1704123456789012345.mp4.transcoded.mp4",
  "output_format": "h264",
  "created_at": "2024-01-01T12:34:56.789999999"
}
```

## 删除转码任务

**端点：** `DELETE /api/transcoding/tasks/{id}`

取消一个待处理或正在运行的任务。已完成、失败或已取消的任务返回 409。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/transcoding/tasks/1"
```

**响应：**
```json
{
  "id": 1,
  "status": "cancelled"
}
```

## 重试转码任务

**端点：** `POST /api/transcoding/tasks/{id}/retry`

从失败的任务创建一个新的待处理转码任务。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/transcoding/tasks/1/retry"
```

**响应（201 Created）：**
```json
{
  "id": 2,
  "camera_id": "front-door",
  "recording_id": "1704123456789012345",
  "input_path": "/data/recordings/h265/front-door_1704123456789012345.mp4",
  "input_format": "h265",
  "output_format": "h264",
  "created_at": "2024-01-01T12:35:00.000000000"
}
```

## 创建回填任务

**端点：** `POST /api/transcoding/backfill`

将摄像头所有未转码的录制文件加入转码队列。

**查询参数：**

| 参数 | 类型 | 必填 | 描述 | 示例 |
|-----------|------|----------|-------------|---------|
| `camera_id` | string | 是 | 要回填的摄像头 ID | `front-door` |

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/transcoding/backfill?camera_id=front-door"
```

**响应：**
```json
{
  "enqueued": 45,
  "skipped": 5,
  "total": 50
}
```

## 摄像头转码配置

**端点：** `GET /api/transcoding/cameras`

返回每台摄像头的解析后转码配置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/cameras"
```

**响应：**
```json
{
  "global_enabled": true,
  "cameras": [
    {
      "camera_id": "front-door",
      "camera_name": "Front Door",
      "enabled": true,
      "target_codec": "h264",
      "preset": "fast",
      "bitrate": 1000000
    },
    {
      "camera_id": "backyard",
      "camera_name": "Backyard",
      "enabled": false,
      "target_codec": "h264",
      "preset": "",
      "bitrate": 0
    }
  ]
}
```

## 未转码的录制文件

**端点：** `GET /api/transcoding/recordings-without-transcode`

返回摄像头尚未转码的录制文件数量。

**查询参数：**

| 参数 | 类型 | 必填 | 描述 | 示例 |
|-----------|------|----------|-------------|---------|
| `camera_id` | string | 是 | 要检查的摄像头 ID | `front-door` |

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/recordings-without-transcode?camera_id=front-door"
```

**响应：**
```json
{
  "count": 45
}
```
