# ONVIF API

## ONVIF 摄像头控制

### 获取 ONVIF 媒体配置文件

**端点：** `GET /api/cameras/{id}/onvif/profiles`

获取 ONVIF 摄像头的可用媒体配置文件。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/onvif/profiles"
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

### 获取 ONVIF 能力信息

**端点：** `GET /api/cameras/{id}/onvif/capabilities`

获取 ONVIF 摄像头的详细设备能力信息。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/onvif/capabilities"
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

### PTZ 移动

**端点：** `POST /api/cameras/{id}/ptz/move`

使用绝对/相对定位移动 PTZ 摄像头。

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
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "mode": "absolute",
    "pan": 0.5,
    "tilt": 0.3,
    "zoom": 1.0
  }' \
  "http://localhost:9090/api/cameras/lobby/ptz/move"
```

**响应：**
```json
{
  "status": "moving"
}
```

### 停止 PTZ

**端点：** `POST /api/cameras/{id}/ptz/stop`

停止 PTZ 移动。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/lobby/ptz/stop"
```

**响应：**
```json
{
  "status": "stopped"
}
```

### 获取 PTZ 状态

**端点：** `GET /api/cameras/{id}/ptz/status`

获取当前 PTZ 位置和移动状态。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/ptz/status"
```

**响应：**
```json
{
  "pan": 0.5,
  "tilt": 0.3,
  "zoom": 1.0,
  "moving": false
}
```

### PTZ 预置位

**端点：** `GET /api/cameras/{id}/ptz/presets`

获取 ONVIF 摄像头保存的 PTZ 预置位。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/ptz/presets"
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

### 创建 PTZ 预置位

**端点：** `POST /api/cameras/{id}/ptz/presets`

创建新的 PTZ 预置位。

**请求体：**
```json
{
  "name": "Home Position"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Home Position"
  }' \
  "http://localhost:9090/api/cameras/lobby/ptz/presets"
```

**响应：**
```json
{
  "token": "preset_123"
}
```

### 转到 PTZ 预置位

**端点：** `POST /api/cameras/{id}/ptz/presets/{token}/goto`

将摄像头移动到保存的 PTZ 预置位。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/lobby/ptz/presets/preset_123/goto"
```

**响应：**
```json
{
  "status": "ok"
}
```

### 删除 PTZ 预置位

**端点：** `DELETE /api/cameras/{id}/ptz/presets/{token}`

删除一个 PTZ 预置位。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/lobby/ptz/presets/preset_123"
```

**响应：**
```json
{
  "status": "ok"
}
```

### 快照 URI

**端点：** `GET /api/cameras/{id}/snapshot/uri`

获取 ONVIF 摄像头的快照 URI。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/snapshot/uri"
```

**响应：**
```json
{
  "uri": "http://192.168.1.100:8080/snapshot.jpg"
}
```

## ONVIF 摄像头管理

### 图像设置（GET）

**端点：** `GET /api/cameras/{id}/imaging/settings`

获取 ONVIF 摄像头的当前图像设置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/imaging/settings"
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

### 设置图像参数

**端点：** `PUT /api/cameras/{id}/imaging/settings`

更新 ONVIF 摄像头的图像参数。

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
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "brightness": 0.6,
    "contrast": 0.8,
    "exposure": {
      "mode": "manual",
      "exposure_time": 0.02
    }
  }' \
  "http://localhost:9090/api/cameras/lobby/imaging/settings"
```

**响应：**
```json
{
  "status": "ok"
}
```

### 获取图像选项

**端点：** `GET /api/cameras/{id}/imaging/options`

获取 ONVIF 摄像头支持的图像参数范围。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/imaging/options"
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

### 重启摄像头

**端点：** `POST /api/cameras/{id}/onvif/reboot`

重启 ONVIF 摄像头。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/lobby/onvif/reboot"
```

**响应：**
```json
{
  "status": "ok"
}
```

**注意：** 部分摄像头可能不支持此操作，将返回 `501 Not Implemented`。

### 网络配置（GET）

**端点：** `GET /api/cameras/{id}/onvif/network`

获取 ONVIF 摄像头的网络接口配置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/onvif/network"
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

### 网络配置（PUT）

**端点：** `PUT /api/cameras/{id}/onvif/network`

配置 ONVIF 摄像头上的网络接口。

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
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
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
  }' \
  "http://localhost:9090/api/cameras/lobby/onvif/network"
```

**响应：**
```json
{
  "status": "ok"
}
```

**注意：** 网络更改可能需要重启摄像头才能生效。部分摄像头可能不支持此操作。

### 用户管理（GET）

**端点：** `GET /api/cameras/{id}/onvif/users`

获取 ONVIF 摄像头上配置的用户列表。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/onvif/users"
```

**响应：**
```json
{
  "users": [
    {
      "username": "admin",
      "user_level": "Administrator"
    }
  ]
}
```

### 用户管理（POST）

**端点：** `POST /api/cameras/{id}/onvif/users`

在 ONVIF 摄像头上创建新用户。

**请求体：**
```json
{
  "username": "operator",
  "password": "securepass",
  "user_level": "Operator"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "operator",
    "password": "securepass",
    "user_level": "Operator"
  }' \
  "http://localhost:9090/api/cameras/lobby/onvif/users"
```

**响应：**
```json
{
  "status": "ok"
}
```

### 用户管理（DELETE）

**端点：** `DELETE /api/cameras/{id}/onvif/users`

从 ONVIF 摄像头删除用户。

**请求体：**
```json
{
  "username": "operator"
}
```

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  -H "Content-Type: application/json" \
  -d '{
    "username": "operator"
  }' \
  "http://localhost:9090/api/cameras/lobby/onvif/users"
```

**响应：**
```json
{
  "status": "ok"
}
```

### 用户管理（PUT）

**端点：** `PUT /api/cameras/{id}/onvif/users/{username}`

更新 ONVIF 摄像头上的指定用户。

**请求体：**
```json
{
  "password": "newpassword",
  "user_level": "Operator"
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "password": "newpassword",
    "user_level": "Operator"
  }' \
  "http://localhost:9090/api/cameras/lobby/onvif/users/operator"
```

**响应：**
```json
{
  "status": "ok"
}
```

## ONVIF 设备发现

### 发现 ONVIF 设备

**端点：** `POST /api/onvif/discover`

使用 WS-Discovery 发现网络上的 ONVIF 设备。

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

### 获取 ONVIF 设备详情

**端点：** `GET /api/onvif/discover/{ip}`

获取指定 ONVIF 设备的详细信息。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/onvif/discover/192.168.1.104"
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

### 探测 ONVIF 设备

**端点：** `POST /api/onvif/probe`

通过直接向指定主机和端口发送 HTTP POST 请求来探测单个 ONVIF 设备（无需组播）。

**请求体：**
```json
{
  "host": "192.168.1.104",
  "port": 80,
  "timeout": 5
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "host": "192.168.1.104",
    "port": 80,
    "timeout": 5
  }' \
  "http://localhost:9090/api/onvif/probe"
```

**响应：**
```json
{
  "device": {
    "manufacturer": "CameraCo",
    "model": "ABC-123",
    "firmware": "1.2.3",
    "serial_number": "CAM123456",
    "endpoint": "http://192.168.1.104:80/onvif/device_service"
  }
}
```
