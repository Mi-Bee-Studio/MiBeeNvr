# MiBee NVR API 参考

## 目录

- [身份验证](#身份验证)
- [健康检查与系统 API](#健康检查与系统-api)
- [摄像头 API](#摄像头-api)
  - [摄像头管理](#摄像头管理)
  - [摄像头快照](#摄像头快照)
  - [HLS 流媒体](#hls-流媒体)
  - [ONVIF 摄像头控制](#onvif-摄像头控制)
  - [摄像头合并配置](#摄像头合并配置)
- [录像 API](#录像-api)
- [归档 API](#归档-api)
- [统计与设置 API](#统计与设置-api)
- [ONVIF API](#onvif-api)
- [小米 API](#小米-api)
- [合并 API](#合并-api)
- [功能 API](#功能-api)
- [备份 API](#备份-api)
- [错误响应](#错误响应)
- [HTTP 状态码](#http-状态码)
- [快速开始](#快速开始)

## 身份验证

MiBee NVR 对受保护的端点使用 HTTP Basic 身份验证。身份验证凭据在应用程序设置中配置。

### 如何使用 Basic Auth

```bash
curl -u username:password http://localhost:9090/api/cameras
```

### 身份验证行为

- 如果 `password_hash` 在设置中配置：所有受保护的端点都需要有效的 Basic Auth 凭据
- 如果设置中 `password_hash` 为空：身份验证被绕过（无保护）
- 身份验证失败返回 `401 Unauthorized`，响应体为空

## 健康检查与系统 API

### 健康检查

**端点：** `GET /api/health`

获取系统整体健康状态，包括数据库和存储磁盘空间。

**请求：**
```bash
curl http://localhost:9090/api/health
```

**响应：**
```json
{
  "status": "ok",
  "checks": {
    "database": {
      "status": "ok",
      "message": ""
    },
    "storage": {
      "status": "ok", 
      "message": ""
    }
  },
  "uptime": "2h34m15s"
}
```

### 就绪检查

**端点：** `GET /api/readyz`

检查系统是否准备好接受请求（与健康检查相同）。

**请求：**
```bash
curl http://localhost:9090/api/readyz
```

**响应：**
```json
{
  "status": "ok"
}
```

### 系统统计

**端点：** `GET /api/stats/system`

获取详细的系统统计信息，包括 CPU、内存和网络使用情况。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats/system"
```

**响应：**
```json
{
  "cpu": {
    "total": 1234567,
    "idle": 987654
  },
  "memory": {
    "total": 1073741824,
    "available": 536870912,
    "process_rss": 10485760
  },
  "network": {
    "bytes_sent": 1048576,
    "bytes_recv": 2097152
  },
  "uptime": "2h34m15s",
  "timestamp": 1716789012
}
```

## 摄像头 API

### 摄像头管理

#### 列出摄像头

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

#### 创建摄像头

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

**响应 (201 Created)：**
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

#### 获取摄像头

**端点：** `GET /api/cameras/:id`

获取特定摄像头的配置。

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

#### 更新摄像头

**端点：** `PUT /api/cameras/:id`

更新摄像头配置。所有字段都是可选的，用于部分更新。

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

#### 删除摄像头

**端点：** `DELETE /api/cameras/:id`

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

#### 测试连接

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

#### 启动摄像头

**端点：** `POST /api/cameras/:id/start`

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

#### 停止摄像头

**端点：** `POST /api/cameras/:id/stop`

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

### 摄像头快照

**端点：** `GET /api/cameras/{id}/snapshot`

从摄像头获取 JPEG 快照图像。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/snapshot" \
  -o snapshot.jpg
```

**响应：** JPEG 图像，`Content-Type: image/jpeg` 和 `Cache-Control: max-age=5`

### HLS 流媒体

**端点：** `GET /api/cameras/:id/stream/*path`

提供按需 HLS 实时流媒体。

**请求 (HLS 播放列表)：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream/stream.m3u8"
```

**请求 (HLS 片段)：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream/segment_001.ts"
```

**响应：** HLS 播放列表或片段文件内容

### ONVIF 摄像头控制

#### 获取 ONVIF 配置文件

**端点：** `GET /api/cameras/{id}/onvif/profiles`

获取 ONVIF 摄像头的可用媒体配置文件。

**请求：**
```bash
curl -u username:password \n  "http://localhost:9090/api/cameras/lobby/onvif/profiles"
```

**响应：**
```json
{
  "profiles": [
    {
      "token": "profile_1",
      "name": "Profile 1",
      "encoding": "H264",
      "width": 1920,
      "height": 1080
    },
    {
      "token": "profile_2",
      "name": "Profile 2",
      "encoding": "H264",
      "width": 1280,
      "height": 720
    }
  ],
  "capabilities": {
    "ptz": true,
    "streaming": true
  }
}
```

#### 获取 ONVIF 能力

**端点：** `GET /api/cameras/{id}/onvif/capabilities`

获取 ONVIF 摄像头的详细设备能力。

**请求：**
```bash
curl -u username:password \n  "http://localhost:9090/api/cameras/lobby/onvif/capabilities"
```

**响应：**
```json
{
  "ptz": true,
  "imaging": true,
  "events": false,
  "snapshot": true,
  "streaming": true,
  "device": true
}
```

#### PTZ 预设点

**端点：** `GET /api/cameras/{id}/ptz/presets`

获取 ONVIF 摄像头的保存 PTZ 预设点。

**请求：**
```bash
curl -u username:password \n  "http://localhost:9090/api/cameras/lobby/ptz/presets"
```

**响应：**
```json
{
  "presets": [
    {
      "token": "preset_1",
      "name": "Home Position",
      "position": {
        "pan": 0.0,
        "tilt": 0.0,
        "zoom": 1.0
      }
    },
    {
      "token": "preset_2",
      "name": "Corner View",
      "position": {
        "pan": 0.5,
        "tilt": 0.3,
        "zoom": 1.5
      }
    }
  ]
}
```

#### 创建 PTZ 预设点

**端点：** `POST /api/cameras/{id}/ptz/presets`

创建新的 PTZ 预设点。

**请求体：**
```json
{
  "name": "Home Position"
}
```

**请求：**
```bash
curl -u username:password \n  -X POST \n  -H "Content-Type: application/json" \n  -d '{
    "name": "Home Position"
  }' \n  "http://localhost:9090/api/cameras/lobby/ptz/presets"
```

**响应：**
```json
{
  "token": "preset_123"
}
```

#### 移动到 PTZ 预设点

**端点：** `POST /api/cameras/{id}/ptz/presets/{token}/goto`

将摄像头移动到保存的 PTZ 预设点。

**请求：**
```bash
curl -u username:password \n  -X POST \n  "http://localhost:9090/api/cameras/lobby/ptz/presets/preset_123/goto"
```

**响应：**
```json
{
  "status": "ok"
}
```

#### 删除 PTZ 预设点

**端点：** `DELETE /api/cameras/{id}/ptz/presets/{token}`

删除 PTZ 预设点。

**请求：**
```bash
curl -u username:password \n  -X DELETE \n  "http://localhost:9090/api/cameras/lobby/ptz/presets/preset_123"
```

**响应：**
```json
{
  "status": "ok"
}
```

#### 移动 PTZ

**端点：** `POST /api/cameras/{id}/ptz/move`

使用绝对/相对位置移动 PTZ 摄像头。

**请求体：**
```json
{
  "mode": "absolute",
  "pan": 0.5,
  "tilt": 0.3,
  "zoom": 1.0,
  "speed": 1.0
}
```

**请求：**
```bash
curl -u username:password \n  -X POST \n  -H "Content-Type: application/json" \n  -d '{
    "mode": "absolute",
    "pan": 0.5,
    "tilt": 0.3,
    "zoom": 1.0
  }' \n  "http://localhost:9090/api/cameras/lobby/ptz/move"
```

**响应：**
```json
{
  "status": "moving"
}
```

#### 停止 PTZ

**端点：** `POST /api/cameras/{id}/ptz/stop`

停止 PTZ 移动。

**请求：**
```bash
curl -u username:password \n  -X POST \n  "http://localhost:9090/api/cameras/lobby/ptz/stop"
```

**响应：**
```json
{
  "status": "stopped"
}
```

#### 获取 PTZ 状态

**端点：** `GET /api/cameras/{id}/ptz/status`

获取当前 PTZ 位置和移动状态。

**请求：**
```bash
curl -u username:password \n  "http://localhost:9090/api/cameras/lobby/ptz/status"
```

**响应：**
```json
{
  "pan": 0.5,
  "tilt": 0.3,
  "zoom": 1.0,
  "moving": false
}
#### 成像设置

**端点：** `GET /api/cameras/{id}/imaging/settings`

获取 ONVIF 摄像头的当前成像设置。

**请求：**
```bash
curl -u username:password \n  "http://localhost:9090/api/cameras/lobby/imaging/settings"
```

**响应：**
```json
{
  "brightness": 0.5,
  "contrast": 0.7,
  "saturation": 0.6,
  "sharpness": 0.8,
  "exposure": {
    "mode": "auto",
    "exposure_time": 0.0,
    "gain": 1.0
  },
  "white_balance": {
    "mode": "auto",
    "color_temperature": 0.0
  }
}
```

#### 设置成像参数

**端点：** `PUT /api/cameras/{id}/imaging/settings`

更新 ONVIF 摄像头的成像参数。

**请求体：**
```json
{
  "brightness": 0.6,
  "contrast": 0.8,
  "saturation": 0.7,
  "sharpness": 0.9,
  "exposure": {
    "mode": "manual",
    "exposure_time": 0.02,
    "gain": 1.2
  },
  "white_balance": {
    "mode": "manual",
    "color_temperature": 4500
  }
}
```

**请求：**
```bash
curl -u username:password \n  -X PUT \n  -H "Content-Type: application/json" \n  -d '{
    "brightness": 0.6,
    "contrast": 0.8,
    "exposure": {
      "mode": "manual",
      "exposure_time": 0.02
    }
  }' \n  "http://localhost:9090/api/cameras/lobby/imaging/settings"
```

**响应：**
```json
{
  "status": "ok"
}
```

#### 获取成像选项

**端点：** `GET /api/cameras/{id}/imaging/options`

获取 ONVIF 摄像头支持的成像参数范围。

**请求：**
```bash
curl -u username:password \n  "http://localhost:9090/api/cameras/lobby/imaging/options"
```

**响应：**
```json
{
  "brightness": {
    "min": 0.0,
    "max": 1.0
  },
  "contrast": {
    "min": 0.0,
    "max": 1.0
  },
  "saturation": {
    "min": 0.0,
    "max": 1.0
  },
  "exposure": {
    "modes": ["auto", "manual"],
    "exposure_time": {
      "min": 0.001,
      "max": 0.1
    },
    "gain": {
      "min": 1.0,
      "max": 10.0
    }
  },
  "white_balance": {
    "modes": ["auto", "manual"],
    "color_temperature": {
      "min": 2000,
      "max": 8000
    }
  }
}
```

#### 快照 URI

**端点：** `GET /api/cameras/{id}/snapshot/uri`

获取 ONVIF 摄像头的快照 URI。

**请求：**
```bash
curl -u username:password \n  "http://localhost:9090/api/cameras/lobby/snapshot/uri"
```

**响应：**
```json
{
  "uri": "http://192.168.1.100:8080/snapshot.jpg"
}

### 设备管理

#### 重启摄像头

**端点：** `POST /api/cameras/{id}/onvif/reboot`

重启 ONVIF 摄像头。

**请求：**
```bash
curl -u username:password \n  -X POST \n  "http://localhost:9090/api/cameras/lobby/onvif/reboot"
```

**响应：**
```json
{
  "status": "ok"
}
```

**注意：** 某些摄像头可能不支持此操作，会返回 `501 Not Implemented`。

#### 网络配置

**端点：** `GET /api/cameras/{id}/onvif/network`

从 ONVIF 摄像头获取网络接口配置。

**请求：**
```bash
curl -u username:password \n  "http://localhost:9090/api/cameras/lobby/onvif/network"
```

**响应：**
```json
{
  "interfaces": [
    {
      "name": "eth0",
      "enabled": true,
      "ipv4": {
        "enabled": true,
        "dhcp": false,
        "address": "192.168.1.100",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1"
      },
      "dns": ["8.8.8.8", "8.8.4.4"]
    }
  ]
}
```

**端点：** `PUT /api/cameras/{id}/onvif/network`

配置 ONVIF 摄像头的网络接口。

**请求体：**
```json
{
  "interfaces": [
    {
      "name": "eth0",
      "enabled": true,
      "ipv4": {
        "enabled": true,
        "dhcp": false,
        "address": "192.168.1.101",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1"
      }
    }
  ]
}
```

**请求：**
```bash
curl -u username:password \n  -X PUT \n  -H "Content-Type: application/json" \n  -d '{
    "interfaces": [
      {
        "name": "eth0",
        "enabled": true,
        "ipv4": {
          "enabled": true,
          "dhcp": false,
          "address": "192.168.1.101",
          "netmask": "255.255.255.0",
          "gateway": "192.168.1.1"
        }
      }
    ]
  }' \n  "http://localhost:9090/api/cameras/lobby/onvif/network"
```

**响应：**
```json
{
  "status": "ok"
}
```

**注意：** 网络更改可能需要重启摄像头。某些摄像头可能不支持此操作。

#### 用户管理

**端点：** `GET /api/cameras/{id}/onvif/users`

从 ONVIF 摄像头获取用户账户。

**请求：**
```bash
curl -u username:password \n  "http://localhost:9090/api/cameras/lobby/onvif/users"
```

**响应：**
```json
{
  "users": [
    {
      "username": "admin",
      "level": "Administrator"
    },
    {
      "username": "operator",
      "level": "Operator"
    },
    {
      "username": "viewer",
      "level": "User"
    }
  ]
}
```

**端点：** `POST /api/cameras/{id}/onvif/users`

在 ONVIF 摄像头上创建用户账户。

**请求体：**
```json
{
  "users": [
    {
      "username": "newuser",
      "password": "securepassword123",
      "level": "Operator"
    }
  ]
}
```

**请求：**
```bash
curl -u username:password \n  -X POST \n  -H "Content-Type: application/json" \n  -d '{
    "users": [
      {
        "username": "newuser",
        "password": "securepassword123",
        "level": "Operator"
      }
    ]
  }' \n  "http://localhost:9090/api/cameras/lobby/onvif/users"
```

**响应：**
```json
{
  "status": "ok"
}
```

**端点：** `PUT /api/cameras/{id}/onvif/users/{username}`

更新 ONVIF 摄像头的用户密码。

**请求体：**
```json
{
  "password": "newpassword123"
}
```

**请求：**
```bash
curl -u username:password \n  -X PUT \n  -H "Content-Type: application/json" \n  -d '{
    "password": "newpassword123"
  }' \n  "http://localhost:9090/api/cameras/lobby/onvif/users/newuser"
```

**响应：**
```json
{
  "status": "ok"
}
```

**端点：** `DELETE /api/cameras/{id}/onvif/users`

删除 ONVIF 摄像头的用户账户。

**请求体：**
```json
{
  "usernames": ["olduser1", "olduser2"]
}
```

**请求：**
```bash
curl -u username:password \n  -X DELETE \n  -H "Content-Type: application/json" \n  -d '{
    "usernames": ["olduser1", "olduser2"]
  }' \n  "http://localhost:9090/api/cameras/lobby/onvif/users"
```

**响应：**
```json
{
  "status": "ok"
}
```

**注意：** 删除用户时要小心，避免将自己锁定在摄像头之外。

#### ONVIF 发现

**端点：** `POST /api/onvif/discover`

发现网络上的 ONVIF 设备。

**请求体：**
```json
{
  "timeout": 5,
  "target": "192.168.1.0/24"
}
```

**请求：**
```bash
curl -u username:password \n  -X POST \n  -H "Content-Type: application/json" \n  -d '{
    "timeout": 5
  }' \n  "http://localhost:9090/api/onvif/discover"
```

**响应：**
```json
{
  "devices": [
    {
      "uuid": "uuid-12345",
      "name": "Camera 1",
      "xaddrs": ["http://192.168.1.104:80/onvif/device_service"],
      "scopes": ["onvif://www.onvif.org/Profile/Video"],
      "hardware": "Camera Model ABC",
      "endpoint": "http://192.168.1.104:80/onvif/device_service"
    }
  ]
}
```

**端点：** `GET /api/onvif/discover/{ip}`

获取特定 ONVIF 设备的详细信息。

**请求：**
```bash
curl -u username:password \n  "http://localhost:9090/api/onvif/discover/192.168.1.104"
```

**响应：**
```json
{
  "device_info": {
    "manufacturer": "CameraCo",
    "model": "ABC-123",
    "firmware": "1.2.3",
    "serial_number": "CAM123456"
  },
  "profiles": [
    {
      "token": "profile_1",
      "name": "Profile 1",
      "encoding": "H264",
      "width": 1920,
      "height": 1080
    }
  ]
}
```

### 摄像头合并配置

#### 更新摄像头合并配置

**端点：** `PUT /api/cameras/:id/merge-config`

为特定摄像头设置合并配置覆盖。

**请求体：**
```json
{
  "enabled": true,
  "check_interval": "30m",
  "window_size": "1h", 
  "batch_limit": 150,
  "min_segment_age": "5m",
  "min_segments_to_merge": 2
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": false,
    "batch_limit": 50
  }' \
  "http://localhost:9090/api/cameras/front-door/merge-config"
```

**响应：**
```json
{
  "status": "updated"
}
```

#### 删除摄像头合并配置

**端点：** `DELETE /api/cameras/:id/merge-config`

删除每个摄像头的合并覆盖。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/front-door/merge-config"
```

**响应：**
```json
{
  "status": "reset"
}
```

## 录像 API

### 列出录像

**端点：** `GET /api/recordings`

检索带可选过滤的分页录像列表。

**查询参数：**

| 参数 | 类型 | 必需 | 描述 | 示例 |
|------|------|----------|-------------|---------|
| `camera_id` | string | 否 | 按摄像头 ID 过滤 | `front-door` |
| `format` | string | 否 | 按格式过滤 (h264, h265, mjpeg) | `h264` |
| `merged` | boolean | 否 | 按合并状态过滤 | `true` |
| `start` | string | 否 | 开始时间 (RFC3339 格式) | `2024-01-01T00:00:00Z` |
| `end` | string | 否 | 结束时间 (RFC3339 格式) | `2024-01-02T00:00:00Z` |
| `limit` | integer | 否 | 最大结果数 (默认: 50) | `20` |
| `offset` | integer | 否 | 分页结果偏移量 | `0` |

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

### 获取录像

**端点：** `GET /api/recordings/:id`

按 ID 检索特定录像。

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

### 删除录像

**端点：** `DELETE /api/recordings/:id`

按 ID 删除录像。

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

### 下载录像

**端点：** `GET /api/recordings/:id/download`

下载录像文件。

**查询参数：**

| 参数 | 类型 | 必需 | 描述 | 示例 |
|------|------|----------|-------------|---------|
| `frame` | integer | 否 | 对于 MJPEG 格式，下载特定帧 | `150` |

**请求 (H264)：**
```bash
curl -u username:password \
  -o recording.mp4 \
  "http://localhost:9090/api/recordings/1704123456789012345/download"
```

**请求 (MJPEG 带特定帧)：**
```bash
curl -u username:password \
  -o frame_150.jpg \
  "http://localhost:9090/api/recordings/1704123456789012345/download?frame=150"
```

**响应：** 二进制文件内容 (MP4 或 JPEG)

### 列出录像帧 (仅限 MJPEG)

**端点：** `GET /api/recordings/:id/frames`

列出 MJPEG 录像的所有帧。

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

### 批量删除录像

**端点：** `POST /api/recordings/batch-delete`

按 ID 删除多个录像。

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

## 归档 API

### 列出归档

**端点：** `GET /api/archives`

按摄像头列出所有归档组。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/archives"
```

**响应：**
```json
{
  "archives": [
    {
      "camera_id": "front-door",
      "retention_days": 30,
      "recordings_count": 150,
      "total_size_mb": 1024
    }
  ]
}
```

### 列出归档录像

**端点：** `GET /api/archives/{cameraID}/recordings`

列出特定归档的录像。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/archives/front-door/recordings"
```

**响应：**
```json
{
  "recordings": [
    {
      "id": "1704123456789012345",
      "started_at": "2024-01-01T12:34:56.789Z",
      "duration": 10.0,
      "file_size": 1048576
    }
  ],
  "total": 1
}
```

### 删除归档组

**端点：** `DELETE /api/archives/{cameraID}`

删除摄像头的整个归档组。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/archives/front-door"
```

**响应：**
```json
{
  "status": "deleted"
}
```

### 删除归档录像

**端点：** `DELETE /api/archives/{cameraID}/recordings/{recordingID}`

从归档中删除特定录像。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/archives/front-door/recordings/1704123456789012345"
```

**响应：**
```json
{
  "status": "deleted"
}
```

### 设置归档保留期

**端点：** `PUT /api/archives/{cameraID}/retention`

为归档组设置保留期。

**请求体：**
```json
{
  "retention_days": 60
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "retention_days": 60
  }' \
  "http://localhost:9090/api/archives/front-door/retention"
```

**响应：**
```json
{
  "status": "updated"
}
```

## 统计与设置 API

### 系统统计

**端点：** `GET /api/stats`

获取系统统计信息，包括存储使用情况和录像计数。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats"
```

**响应：**
```json
{
  "total_bytes": 1073741824,
  "used_bytes": 536870912,
  "recording_count": 1000,
  "camera_count": 4
}
```

### 统计趋势

**端点：** `GET /api/stats/trends`

获取存储使用趋势随时间变化。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats/trends"
```

**响应：**
```json
{
  "trends": [
    {
      "date": "2024-01-01",
      "total_bytes": 1000000000,
      "used_bytes": 500000000,
      "recording_count": 950
    }
  ]
}
```

### 获取设置

**端点：** `GET /api/settings`

获取当前配置设置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings"
```

**响应：**
```json
{
  "server": {
    "listen": ":9090"
  },
  "storage": {
    "root_dir": "/var/lib/mibee-nvr",
    "segment_duration": "30s"
  },
  "cleanup": {
    "retention_days": 30,
    "check_interval": "1h",
    "disk_threshold_percent": 95
  },
  "auth": {
    "username": "admin"
  }
}
```

### 更新设置

**端点：** `PUT /api/settings`

更新配置设置。

**请求体：**
```json
{
  "cleanup": {
    "retention_days": 60,
    "disk_threshold_percent": 90,
    "check_interval": "30m"
  }
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "cleanup": {
      "retention_days": 60,
      "disk_threshold_percent": 90,
      "check_interval": "30m"
    }
  }' \
  "http://localhost:9090/api/settings"
```

**响应：**
```json
{
  "status": "updated"
}
```

### 获取合并设置

**端点：** `GET /api/settings/merge`

获取全局合并设置配置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings/merge"
```

**响应：**
```json
{
  "enabled": true,
  "check_interval": "1h",
  "window_size": "1h",
  "batch_limit": 200,
  "min_segment_age": "10m",
  "min_segments_to_merge": 3
}
```

### 更新合并设置

**端点：** `PUT /api/settings/merge`

更新全局合并设置。

**请求体：**
```json
{
  "enabled": true,
  "check_interval": "30m",
  "window_size": "2h",
  "batch_limit": 100,
  "min_segment_age": "15m",
  "min_segments_to_merge": 5
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "check_interval": "30m",
    "batch_limit": 100
  }' \
  "http://localhost:9090/api/settings/merge"
```

**响应：**
```json
{
  "status": "updated"
}
```

## ONVIF API

### 发现 ONVIF 设备

**端点：** `POST /api/onvif/discover`

发现网络上的 ONVIF 设备。

**请求体：**
```json
{
  "timeout": 5,
  "target": "192.168.1.0/24"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "timeout": 5
  }' \
  "http://localhost:9090/api/onvif/discover"
```

**响应：**
```json
{
  "devices": [
    {
      "url": "http://192.168.1.104:80/onvif/device_service",
      "name": "Camera 1",
      "model": "ABC-123",
      "location": "Office"
    }
  ]
}
```

### 获取 ONVIF 设备详情

**端点：** `GET /api/onvif/discover/{ip}`

获取特定 ONVIF 设备的详细信息。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/onvif/discover/192.168.1.104"
```

**响应：**
```json
{
  "device": {
    "url": "http://192.168.1.104:80/onvif/device_service",
    "name": "Camera 1",
    "model": "ABC-123",
    "location": "Office",
    "manufacturer": "CameraCo",
    "firmware_version": "1.2.3",
    "serial_number": "CAM123456"
  }
}
```

## 小米 API

### 小米云认证

**端点：** `POST /api/xiaomi/auth`

与小米云服务认证。

**请求体：**
```json
{
  "username": "xiaomi@example.com",
  "password": "password123"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/auth"
```

**响应：**
```json
{
  "user_id": "1234567890",
  "token": "xiaomi_token_123",
  "region": "cn"
}
```

### 获取小米验证码

**端点：** `POST /api/xiaomi/captcha`

获取小米认证的验证码。

**请求体：**
```json
{
  "username": "xiaomi@example.com"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com"
  }' \
  "http://localhost:9090/api/xiaomi/captcha"
```

**响应：**
```json
{
  "captcha_id": "captcha_123",
  "captcha_image": "base64_encoded_image"
}
```

### 验证小米验证码

**端点：** `POST /api/xiaomi/verify`

验证验证码并完成认证。

**请求体：**
```json
{
  "captcha_id": "captcha_123",
  "captcha_code": "ABC123",
  "username": "xiaomi@example.com",
  "password": "password123"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "captcha_id": "captcha_123",
    "captcha_code": "ABC123",
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/verify"
```

**响应：**
```json
{
  "user_id": "1234567890",
  "token": "xiaomi_token_123",
  "region": "cn"
}
```

### 列出小米设备

**端点：** `GET /api/xiaomi/devices`

从云获取小米设备列表。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/xiaomi/devices"
```

**响应：**
```json
{
  "devices": [
    {
      "did": "camera_did_123",
      "name": "Front Door Camera",
      "model": "xiaomi.camera.v2",
      "online": true
    }
  ]
}
```

### 同步小米设备

**端点：** `POST /api/xiaomi/sync`

将小米设备与本地配置同步。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/xiaomi/sync"
```

**响应：**
```json
{
  "synced": 2,
  "added": 1,
  "removed": 0
}
```

## 合并 API

### 获取合并状态

**端点：** `GET /api/merge/status`

获取当前合并管理器状态和统计信息。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/merge/status"
```

**响应：**
```json
{
  "enabled": true,
  "error_count": 0,
  "files_created": 9,
  "last_run_time": "2026-05-11T06:37:41Z",
  "segments_merged": 235
}
```

### 获取待合并计数

**端点：** `GET /api/merge/pending`

获取每个摄像头待合并片段的数量。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/merge/pending"
```

**响应：**
```json
{
  "pending": {
    "front-door": 99,
    "backyard": 145
  }
}
```

## 功能 API

### 获取功能

**端点：** `GET /api/features`

获取启用/禁用的功能标志。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/features"
```

**响应：**
```json
{
  "features": {
    "experimental_hls": false,
    "auto_delete_old": true,
    "webdav_upload": true
  }
}
```

### 更新功能

**端点：** `PUT /api/features`

更新功能标志。

**请求体：**
```json
{
  "experimental_hls": true,
  "auto_delete_old": false
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "experimental_hls": true,
    "auto_delete_old": false
  }' \
  "http://localhost:9090/api/features"
```

**响应：**
```json
{
  "status": "updated"
}
```

## 备份 API

### 创建备份

**端点：** `POST /api/backup`

创建数据库和配置的备份。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/backup"
```

**响应：**
```json
{
  "backup_id": "backup_1704123456",
  "path": "/var/lib/mibee-nvr/backups/backup_1704123456.tar.gz",
  "size_bytes": 1048576,
  "created_at": "2024-01-01T12:34:56Z"
}
```

### 列出备份

**端点：** `GET /api/backups`

列出可用的备份。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/backups"
```

**响应：**
```json
{
  "backups": [
    {
      "backup_id": "backup_1704123456",
      "path": "/var/lib/mibee-nvr/backups/backup_1704123456.tar.gz",
      "size_bytes": 1048576,
      "created_at": "2024-01-01T12:34:56Z"
    }
  ]
}
```

## 错误响应

所有错误响应都遵循以下格式：

```json
{
  "error": "错误消息",
  "code": "ERROR_CODE"
}
```

### 错误代码参考

| 代码 | 描述 | HTTP 状态 |
|------|-------------|-------------|
| `CAMERA_NOT_FOUND` | 指定 ID 的摄像头不存在 | 404 |
| `CAMERA_ALREADY_RUNNING` | 摄像头录制器已激活 | 400 |
| `CAMERA_DISABLED` | 摄像头被禁用，无法启动 | 400 |
| `CAMERA_ALREADY_EXISTS` | 指定 ID 的摄像头已存在 | 409 |
| `RECORDING_NOT_FOUND` | 指定 ID 的录像不存在 | 404 |
| `STORAGE_FULL` | 磁盘空间严重不足 | 507 |
| `AUTH_REQUIRED` | 需要身份验证 | 401 |
| `AUTH_FAILED` | 身份验证凭据被拒绝 | 401 |
| `INVALID_INPUT` | 请求包含无效参数 | 400 |
| `PATH_TRAVERSAL` | 检测到路径遍历尝试 | 400 |
| `HLS_MAX_STREAMS` | 达到最大并发 HLS 流限制 | 503 |
| `HLS_UNSUPPORTED_CODEC` | 摄像头编解码器不支持 HLS 流媒体 | 400 |
| `ONVIF_NOT_CAMERA` | 设备不是 ONVIF 摄像头 | 400 |
| `ONVIF_CONNECTION_FAILED` | 连接到 ONVIF 设备失败 | 500 |
| `ONVIF_NO_PROFILES` | 未找到 ONVIF 摄像头的媒体配置文件 | 400 |
| `INTERNAL` | 内部服务器错误 | 500 |

### 常见错误示例

#### 身份验证失败
```json
{
  "error": "authentication failed: invalid username or password",
  "code": "AUTH_FAILED"
}
```

#### 摄像头未找到
```json
{
  "error": "camera not found: non-existent-camera",
  "code": "CAMERA_NOT_FOUND"
}
```

#### 无效输入
```json
{
  "error": "invalid input: camera URL must be valid",
  "code": "INVALID_INPUT"
}
```

#### 存储空间已满
```json
{
  "error": "storage full: disk space critically low",
  "code": "STORAGE_FULL"
}
```

## HTTP 状态码

| 状态码 | 描述 |
|-------------|-------------|
| 200 | OK - 请求成功 |
| 201 | Created - 资源成功创建 |
| 400 | Bad Request - 无效的请求参数 |
| 401 | Unauthorized - 身份验证失败或需要身份验证 |
| 403 | Forbidden - 资源访问不允许 |
| 404 | Not Found - 资源不存在 |
| 409 | Conflict - 资源冲突（例如，重复的摄像头 ID） |
| 500 | Internal Server Error - 服务器端错误 |
| 503 | Service Unavailable - 服务暂时不可用（例如，达到最大流数） |
| 507 | Insufficient Storage - 磁盘空间不足 |

## 快速开始

### 基本身份验证测试

```bash
# 测试健康端点（不需要身份验证）
curl http://localhost:9090/api/health

# 测试身份验证
curl -u admin:password http://localhost:9090/api/cameras
```

### 常见操作

```bash
# 列出所有录像
curl -u admin:password "http://localhost:9090/api/recordings"

# 添加新摄像头
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Living Room Cam",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.50:554/stream",
    "enabled": true
  }' \
  "http://localhost:9090/api/cameras"

# 下载录像
curl -u admin:password \
  -o recording.mp4 \
  "http://localhost:9090/api/recordings/1704123456789012345/download"

# 更新设置以清理超过 7 天的录像
curl -u admin:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "cleanup": {
      "retention_days": 7
    }
  }' \
  "http://localhost:9090/api/settings"

# 测试摄像头连接
curl -u admin:password \
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

### HLS 流媒体示例

```bash
# 获取 HLS 播放列表
curl -u admin:password \
  "http://localhost:9090/api/cameras/living-room/stream/stream.m3u8"

# 获取 HLS 片段  
curl -u admin:password \
  "http://localhost:9090/api/cameras/living-room/stream/segment_001.ts"
```

### 小米摄像头设置

```bash
# 使用小米云进行身份验证
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/auth"

# 列出小米设备
curl -u admin:password \
  "http://localhost:9090/api/xiaomi/devices"
```