# MiBee NVR API 参考文档

## 概述

MiBee NVR 提供完整的 REST API 接口，支持摄像头管理、录像管理、配置管理等操作。所有 API 接口都使用 JSON 格式进行数据交换，支持认证和权限控制。

### 认证方式

MiBee NVR 使用基本认证（Basic Auth）保护所有 API 接口：

```bash
# 认证头格式
Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=
```

其中 `YWRtaW46cGFzc3dvcmQxMjM=` 是 `admin:password123` 的 Base64 编码。

## API 接口总览

| 接口路径 | 方法 | 描述 | 认证要求 |
|---------|------|------|----------|
| `/api/health` | GET | 健康检查 | 无 |
| `/api/auth/login` | POST | 登录验证 | 无 |
| `/api/recordings` | GET | 获取录像列表 | 是 |
| `/api/recordings/:id` | GET | 获取录像详情 | 是 |
| `/api/recordings/:id` | DELETE | 删除录像 | 是 |
| `/api/recordings/:id/pin` | POST | 收藏录像 | 是 |
| `/api/recordings/:id/unpin` | POST | 取消收藏 | 是 |
| `/api/recordings/:id/download` | GET | 下载录像 | 是 |
| `/api/recordings/:id/frames` | GET | 获取 MJPEG 帧列表 | 是 |
| `/api/cameras` | GET | 获取摄像头列表 | 是 |
| `/api/cameras` | POST | 创建摄像头 | 是 |
| `/api/cameras/:id` | GET | 获取摄像头详情 | 是 |
| `/api/cameras/:id` | PUT | 更新摄像头 | 是 |
| `/api/cameras/:id` | DELETE | 删除摄像头 | 是 |
| `/api/cameras/:id/snapshot` | GET | 摄像头快照 | 是 |
| `/api/stats` | GET | 获取系统统计 | 是 |
| `/api/settings` | GET | 获取系统设置 | 是 |
| `/api/settings` | PUT | 更新系统设置 | 是 |
| `/api/upload` | POST | 文件上传 | 是 |

## 认证相关接口

### 登录验证

**接口**: `POST /api/auth/login`

**请求**:
```json
{
  "username": "admin",
  "password": "password123"
}
```

**响应**:
```json
{
  "success": true,
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expires_at": "2024-01-01T12:00:00Z"
}
```

**curl 示例**:
```bash
curl -X POST http://localhost:9090/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "username": "admin",
    "password": "password123"
  }'
```

## 录像管理接口

### 获取录像列表

**接口**: `GET /api/recordings`

**查询参数**:
| 参数 | 类型 | 描述 | 默认值 |
|------|------|------|--------|
| `camera_id` | string | 摄像头 ID | - |
| `start_time` | string | 开始时间 (ISO 8601) | - |
| `end_time` | string | 结束时间 (ISO 8601) | - |
| `limit` | int | 返回记录数 | 50 |
| `offset` | int | 偏移量 | 0 |
| `order` | string | 排序方式 (asc/desc) | desc |
| `pinned` | boolean | 是否只显示收藏 | - |

**响应**:
```json
{
  "success": true,
  "data": [
    {
      "id": "1704110400000000000",
      "camera_id": "cam1",
      "camera_name": "前门摄像头",
      "start_time": "2024-01-01T10:00:00Z",
      "end_time": "2024-01-01T10:10:00Z",
      "duration": 600,
      "file_size": 15432000,
      "file_path": "/mnt/data/nvr/recordings/2024/01/01/cam1_1704110400.mp4",
      "pinned": true,
      "created_at": "2024-01-01T10:10:00Z"
    }
  ],
  "total": 156,
  "page": 1,
  "limit": 50
}
```

**curl 示例**:
```bash
# 获取所有录像
curl -X GET "http://localhost:9090/api/recordings?limit=10&offset=0" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="

# 获取特定摄像头的录像
curl -X GET "http://localhost:9090/api/recordings?camera_id=cam1" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="

# 获取时间范围内的录像
curl -X GET "http://localhost:9090/api/recordings?start_time=2024-01-01T00:00:00Z&end_time=2024-01-02T00:00:00Z" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="

# 获取收藏的录像
curl -X GET "http://localhost:9090/api/recordings?pinned=true" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
```

### 获取录像详情

**接口**: `GET /api/recordings/:id`

**响应**:
```json
{
  "success": true,
  "data": {
    "id": "1704110400000000000",
    "camera_id": "cam1",
    "camera_name": "前门摄像头",
    "start_time": "2024-01-01T10:00:00Z",
    "end_time": "2024-01-01T10:10:00Z",
    "duration": 600,
    "file_size": 15432000,
    "file_path": "/mnt/data/nvr/recordings/2024/01/01/cam1_1704110400.mp4",
    "pinned": true,
    "created_at": "2024-01-01T10:10:00Z",
    "metadata": {
      "video_codec": "h264",
      "audio_codec": "aac",
      "width": 1920,
      "height": 1080,
      "fps": 30,
      "bitrate": 256000
    }
  }
}
```

**curl 示例**:
```bash
curl -X GET "http://localhost:9090/api/recordings/1704110400000000000" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
```

### 删除录像

**接口**: `DELETE /api/recordings/:id`

**响应**:
```json
{
  "success": true,
  "message": "Recording deleted successfully"
}
```

**curl 示例**:
```bash
curl -X DELETE "http://localhost:9090/api/recordings/1704110400000000000" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
```

### 收藏/取消收藏录像

**接口**: `POST /api/recordings/:id/pin` 和 `POST /api/recordings/:id/unpin`

**响应**:
```json
{
  "success": true,
  "message": "Recording pinned successfully"
}
```

**curl 示例**:
```bash
# 收藏录像
curl -X POST "http://localhost:9090/api/recordings/1704110400000000000/pin" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="

# 取消收藏
curl -X POST "http://localhost:9090/api/recordings/1704110400000000000/unpin" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
```

### 下载录像

**接口**: `GET /api/recordings/:id/download`

**响应**: 文件流

**curl 示例**:
```bash
# 下载录像文件
curl -X GET "http://localhost:9090/api/recordings/1704110400000000000/download" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=" \
  -o recording.mp4

# 断点续传下载
curl -X GET "http://localhost:9090/api/recordings/1704110400000000000/download" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=" \
  -H "Range: bytes=0-1023" \
  -o recording_partial.mp4
```

### 获取 MJPEG 帧列表

**接口**: `GET /api/recordings/:id/frames`

**响应**:
```json
{
  "success": true,
  "data": {
    "recording_id": "1704110400000000000",
    "total_frames": 1800,
    "frames": [
      {
        "timestamp": "2024-01-01T10:00:00Z",
        "frame_number": 1,
        "file_path": "/mnt/data/nvr/recordings/2024/01/01/cam1_1704110400_frame_001.jpg",
        "file_size": 45678
      }
    ]
  }
}
```

**curl 示例**:
```bash
curl -X GET "http://localhost:9090/api/recordings/1704110400000000000/frames" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
```

## 摄像头管理接口

### 获取摄像头列表

**接口**: `GET /api/cameras`

**响应**:
```json
{
  "success": true,
  "data": [
    {
      "id": "cam1",
      "name": "前门摄像头",
      "protocol": "rtsp_h264",
      "url": "rtsp://192.168.1.100:554/stream",
      "enabled": true,
      "status": "recording",
      "last_seen": "2024-01-01T10:15:00Z",
      "retention_days": 30,
      "created_at": "2024-01-01T09:00:00Z",
      "updated_at": "2024-01-01T09:00:00Z"
    },
    {
      "id": "cam2",
      "name": "后院摄像头",
      "protocol": "rtsp_mjpeg",
      "url": "rtsp://192.168.1.101:554/live",
      "enabled": true,
      "status": "stopped",
      "last_seen": "2024-01-01T09:30:00Z",
      "retention_days": 7,
      "created_at": "2024-01-01T09:00:00Z",
      "updated_at": "2024-01-01T09:00:00Z"
    }
  ]
}
```

**curl 示例**:
```bash
curl -X GET "http://localhost:9090/api/cameras" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
```

### 创建摄像头

**接口**: `POST /api/cameras`

**请求**:
```json
{
  "name": "新增摄像头",
  "protocol": "rtsp_h264",
  "url": "rtsp://192.168.1.102:554/stream",
  "enabled": true,
  "username": "admin",
  "password": "password123",
  "retention_days": 15
  
  # 注意：retention_days 是可选的，默认使用全局设置
}```

**响应**:
```json
{
  "success": true,
  "data": {
    "id": "cam3",
    "name": "新增摄像头",
    "protocol": "rtsp_h264",
    "url": "rtsp://192.168.1.102:554/stream",
    "enabled": true,
    "status": "initializing",
    "retention_days": 15,
    "username": "admin",
    "password": "password123",
    "created_at": "2024-01-01T10:20:00Z",
    "updated_at": "2024-01-01T10:20:00Z"
  }
}```

**curl 示例**:
```bash
curl -X POST "http://localhost:9090/api/cameras" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "新增摄像头",
    "protocol": "rtsp_h264",
    "url": "rtsp://192.168.1.102:554/stream",
    "enabled": true,
    "recording": true,
    "username": "admin",
    "password": "password123"
  }'
```

### 获取摄像头详情

**接口**: `GET /api/cameras/:id`

**响应**:
```json
{
  "success": true,
  "data": {
    {
      "id": "cam1",
      "name": "前门摄像头",
      "protocol": "rtsp_h264",
      "url": "rtsp://192.168.1.100:554/stream",
      "enabled": true,
      "status": "recording",
      "last_seen": "2024-01-01T10:15:00Z",
      "retention_days": 30,
      "username": "admin",
      "password": "password123",
      "statistics": {
        "uptime": "2h30m",
        "recording_count": 156,
        "total_size": "1.2GB",
        "bitrate": 256000,
        "framerate": 30
      },
      "created_at": "2024-01-01T09:00:00Z",
      "updated_at": "2024-01-01T09:00:00Z"
    }
```

**curl 示例**:
```bash
curl -X GET "http://localhost:9090/api/cameras/cam1" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
```

### 更新摄像头

**接口**: `PUT /api/cameras/:id`

**请求**:
```json
{
  "name": "前门摄像头（更新）",
  "url": "rtsp://192.168.1.100:554/stream_updated",
  "enabled": true,
  "retention_days": 7,
  "username": "admin",
  "password": "newpassword123"
  
  # 注意：retention_days 是可选的，更新按摄像头保留策略
}```

**响应**:
```json
{
  "success": true,
  "data": {
    "id": "cam1",
    "name": "前门摄像头（更新）",
    "protocol": "rtsp_h264",
    "url": "rtsp://192.168.1.100:554/stream_updated",
    "enabled": true,
    "recording": true,
    "username": "admin",
    "password": "newpassword123",
    "status": "restarting",
    "created_at": "2024-01-01T09:00:00Z",
    "updated_at": "2024-01-01T10:25:00Z"
  }
}
```

**curl 示例**:
```bash
curl -X PUT "http://localhost:9090/api/cameras/cam1" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "前门摄像头（更新）",
    "url": "rtsp://192.168.1.100:554/stream_updated",
    "enabled": true,
    "recording": true,
    "username": "admin",
    "password": "newpassword123"
  }'
```

### 删除摄像头

**接口**: `DELETE /api/cameras/:id`

**响应**:
```json
{
  "success": true,
  "message": "Camera deleted successfully"
}
```

**curl 示例**:
```bash
curl -X DELETE "http://localhost:9090/api/cameras/cam1" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
```


### 摄像头快照

**端点**: `GET /api/cameras/{id}/snapshot`

获取摄像头的 JPEG 快照图像。需要摄像头配置了 `snapshot_url`。

**路径参数:**

| 参数 | 类型 | 必需 | 描述 |
|------|------|------|------|
| `id` | 字符串 | 是 | 摄像头 ID |

**响应:**
- `200 OK` — JPEG 图像，`Content-Type: image/jpeg`，`Cache-Control: max-age=5`
- `404 Not Found` — 摄像头不存在或未配置快照 URL
- `502 Bad Gateway` — 快照 URL 不可达且无缓存图像

**缓存行为:**
- 快照在服务端缓存 10 秒
- 摄像头暂时不可达时，返回过期缓存并附带 `X-Cache: stale` 头
- 客户端缓存: 5 秒（`Cache-Control: max-age=5`）

**请求示例:**
```bash
curl -u admin:admin http://localhost:9090/api/cameras/cam1/snapshot -o snapshot.jpg
```
## 系统管理接口

### 健康检查

**接口**: `GET /api/health`

**响应**:
```json
{
  "success": true,
  "data": {
    "status": "ok",
    "version": "1.0.0",
    "uptime": "2h30m",
    "timestamp": "2024-01-01T10:30:00Z",
    "cameras": {
      "total": 3,
      "active": 2,
      "inactive": 1
    },
    "storage": {
      "total_gb": 500,
      "used_gb": 120,
      "available_gb": 380,
      "usage_percent": 24
    },
    "services": {
      "recorder": "running",
      "storage": "running",
      "cleanup": "running",
      "webdav": "running",
      "ftp": "running"
    }
  }
}
```

**curl 示例**:
```bash
curl -X GET "http://localhost:9090/api/health"
```

### 获取系统统计

**接口**: `GET /api/stats`

**响应**:
```json
{
  "success": true,
  "data": {
    "system": {
      "uptime": "2h30m",
      "cpu_usage": 15.5,
      "memory_usage": 45.2,
      "disk_usage": 24.0
    },
    "cameras": {
      "total": 3,
      "active": 2,
      "inactive": 1,
      "total_recordings": 156,
      "total_size_gb": 1.2
    },
    "recordings": {
      "today": 12,
      "this_week": 89,
      "this_month": 156,
      "total_size_gb": 1.2
    },
    "services": {
      "recorder": {
        "active_threads": 2,
        "total_recordings": 156,
        "current_segments": 3
      },
      "storage": {
        "total_files": 1560,
        "total_size_gb": 12.5,
        "cleaned_files": 156
      }
    }
  }
}
```

**curl 示例**:
```bash
curl -X GET "http://localhost:9090/api/stats" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
```

### 获取系统设置

**接口**: `GET /api/settings`

**响应**:
```json
{
  "success": true,
  "data": {
    "server": {
      "listen": ":9090",
      "read_timeout": "30s",
      "write_timeout": "30s",
      "idle_timeout": "60s"
    },
    "storage": {
      "root_dir": "/mnt/data/nvr",
      "segment_duration": "10m",
      "max_segments": 1000
    },
    "auth": {
      "username": "admin",
      "session_timeout": "24h"
    },
    "cleanup": {
      "retention_days": 30,
      "check_interval": "1h",
      "disk_threshold_percent": 95
    }
  }
}
```

**curl 示例**:
```bash
curl -X GET "http://localhost:9090/api/settings" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
```

### 更新系统设置

**接口**: `PUT /api/settings`

**请求**:
```json
{
  "storage": {
    "segment_duration": "30s",
    "max_segments": 500
  },
  "cleanup": {
    "retention_days": 60,
    "check_interval": "30m"
  }
}
```

**响应**:
```json
{
  "success": true,
  "data": {
    "storage": {
      "root_dir": "/mnt/data/nvr",
      "segment_duration": "30s",
      "max_segments": 500
    },
    "cleanup": {
      "retention_days": 60,
      "check_interval": "30m",
      "disk_threshold_percent": 95
    }
  }
}
```

**curl 示例**:
```bash
curl -X PUT "http://localhost:9090/api/settings" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=" \
  -H "Content-Type: application/json" \
  -d '{
    "storage": {
      "segment_duration": "30s",
      "max_segments": 500
    },
    "cleanup": {
      "retention_days": 60,
      "check_interval": "30m"
    }
  }'
```

### 文件上传

**接口**: `POST /api/upload`

**请求**: multipart/form-data

**参数**:
| 参数 | 类型 | 描述 | 必须 |
|------|------|------|------|
| `file` | file | 要上传的文件 | 是 |
| `camera_id` | string | 摄像头 ID | 否 |

**响应**:
```json
{
  "success": true,
  "data": {
    "filename": "uploaded_video.mp4",
    "size": 15432000,
    "path": "/mnt/data/nvr/uploads/2024/01/01/uploaded_video.mp4",
    "camera_id": "cam1",
    "uploaded_at": "2024-01-01T10:35:00Z"
  }
}
```

**curl 示例**:
```bash
curl -X POST "http://localhost:9090/api/upload" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=" \
  -F "file=@/path/to/video.mp4" \
  -F "camera_id=cam1"
```

## 错误处理

### 错误响应格式

```json
{
  "success": false,
  "error": {
    "code": "ERROR_CODE",
    "message": "错误描述",
    "details": "详细错误信息"
  }
}
```

### 常见错误代码

| 错误代码 | HTTP 状态码 | 描述 |
|----------|-------------|------|
| `INVALID_REQUEST` | 400 | 请求格式错误 |
| `UNAUTHORIZED` | 401 | 未认证 |
| `FORBIDDEN` | 403 | 权限不足 |
| `NOT_FOUND` | 404 | 资源不存在 |
| `INTERNAL_ERROR` | 500 | 服务器内部错误 |
| `CAMERA_NOT_FOUND` | 404 | 摄像头不存在 |
| `RECORDING_NOT_FOUND` | 404 | 录像不存在 |
| `INVALID_PARAMETERS` | 400 | 无效参数 |

### 错误处理示例

```bash
# 处理认证失败
response=$(curl -s -w "%{http_code}" "http://localhost:9090/api/cameras")
if [[ "$response" == "401" ]]; then
  echo "认证失败，请检查用户名和密码"
  exit 1
fi

# 处理摄像头不存在
response=$(curl -s -X GET "http://localhost:9090/api/cameras/cam999" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=")

if [[ "$(echo "$response" | jq -r '.success')" == "false" ]]; then
  error_code=$(echo "$response" | jq -r '.error.code')
  if [[ "$error_code" == "CAMERA_NOT_FOUND" ]]; then
    echo "摄像头不存在"
  fi
fi
```

## API 使用示例

### Shell 脚本示例

```bash
#!/bin/bash
# MiBee NVR API 脚本示例

BASE_URL="http://localhost:9090"
AUTH="admin:password123"

# 获取认证令牌
login_response=$(curl -s -X POST "$BASE_URL/api/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"password123"}')

TOKEN=$(echo "$login_response" | jq -r '.token')

# 获取摄像头列表
curl -X GET "$BASE_URL/api/cameras" \
  -H "Authorization: Basic $(echo $AUTH | base64)"

# 创建摄像头
curl -X POST "$BASE_URL/api/cameras" \
  -H "Authorization: Basic $(echo $AUTH | base64)" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "测试摄像头",
    "protocol": "http_jpeg",
    "url": "http://192.168.1.100:8080/snapshot",
    "enabled": true,
    "recording": true
  }'

# 获取系统统计
curl -X GET "$BASE_URL/api/stats" \
  -H "Authorization: Basic $(echo $AUTH | base64)" | jq '.data.system'
```

### Python 脚本示例

```python
#!/usr/bin/env python3
import requests
import json
import base64

class MiBeeNVRClient:
    def __init__(self, base_url, username, password):
        self.base_url = base_url
        self.auth = base64.b64encode(f"{username}:{password}".encode()).decode()
        
    def get_cameras(self):
        response = requests.get(
            f"{self.base_url}/api/cameras",
            headers={"Authorization": f"Basic {self.auth}"}
        )
        return response.json()
    
    def create_camera(self, camera_data):
        response = requests.post(
            f"{self.base_url}/api/cameras",
            headers={
                "Authorization": f"Basic {self.auth}",
                "Content-Type": "application/json"
            },
            json=camera_data
        )
        return response.json()
    
    def get_recordings(self, camera_id=None, start_time=None, end_time=None):
        params = {}
        if camera_id:
            params["camera_id"] = camera_id
        if start_time:
            params["start_time"] = start_time
        if end_time:
            params["end_time"] = end_time
            
        response = requests.get(
            f"{self.base_url}/api/recordings",
            headers={"Authorization": f"Basic {self.auth}"},
            params=params
        )
        return response.json()

# 使用示例
if __name__ == "__main__":
    client = MiBeeNVRClient("http://localhost:9090", "admin", "password123")
    
    # 获取摄像头列表
    cameras = client.get_cameras()
    print("摄像头列表:", json.dumps(cameras, indent=2, ensure_ascii=False))
    
    # 创建摄像头
    new_camera = {
        "name": "Python 测试摄像头",
        "protocol": "http_jpeg",
        "url": "http://192.168.1.100:8080/snapshot",
        "enabled": True,
        "recording": True
    }
    
    result = client.create_camera(new_camera)
    print("创建结果:", json.dumps(result, indent=2, ensure_ascii=False))
```

## 性能优化

### 批量操作

```bash
# 批量获取多个摄像头的录像列表
for camera_id in cam1 cam2 cam3; do
  curl -X GET "http://localhost:9090/api/recordings?camera_id=$camera_id&limit=5" \
    -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM="
done

# 并发请求
{
  cam1=$(curl -s -X GET "http://localhost:9090/api/cameras/cam1" \
    -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=")
  cam2=$(curl -s -X GET "http://localhost:9090/api/cameras/cam2" \
    -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=")
  cam3=$(curl -s -X GET "http://localhost:9090/api/cameras/cam3" \
    -H "Authorization: Basic YWRtaW46cGFzc3dvcmQxMjM=")
  
  echo "摄像头 1: $cam1"
  echo "摄像头 2: $cam2"
  echo "摄像头 3: $cam3"
}
```

### 缓存策略

```bash
# 缓存健康检查结果
if [ -f /tmp/mibee_health ]; then
  if [ $(($(date +%s) - $(stat -c %Y /tmp/mibee_health))) -lt 60 ]; then
    echo "使用缓存的健康状态"
    cat /tmp/mibee_health
    exit 0
  fi
fi

curl -s -X GET "http://localhost:9090/api/health" > /tmp/mibee_health
cat /tmp/mibee_health
```

## 安全注意事项

1. **HTTPS 配置**: 在生产环境中启用 HTTPS
2. **认证保护**: 所有 API 接口都需要认证
3. **密码安全**: 使用强密码并定期更换
4. **输入验证**: 所有输入参数都需要验证
5. **日志记录**: 记录所有 API 访问日志
6. **速率限制**: 实施 API 访问频率限制
7. **CORS 配置**: 配置适当的 CORS 策略

## 总结

MiBee NVR 提供了完整的 REST API 接口，支持所有主要功能的程序化访问。通过合理使用这些 API 接口，可以：

- 自动化摄像头管理
- 集成第三方系统
- 开发自定义应用
- 实现监控和报警
- 进行批量操作

建议在使用 API 时注意错误处理和性能优化，确保系统的稳定性和可靠性。