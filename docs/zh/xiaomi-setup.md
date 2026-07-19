# 小米摄像头集成

MiBee NVR 通过 CS2 P2P 协议为小米云摄像头提供全面支持。此集成允许您将小米摄像头连接到 NVR 系统，无需直接访问摄像头本身的网络，因为所有通信都通过小米云服务处理。

## 概述

- **协议**：CS2 P2P（小米专有云协议）
- **认证**：小米云服务，基于令牌的身份验证
- **支持的型号**：CS2 摄像头（见下表）
- **功能**：实时流、录制、快照、PTZ 控制
- **网络**：需要连接到小米云服务

## 前置要求

- 小米账户，已注册摄像头
- 摄像头已绑定到您的小米账户（米家应用）
- 网络访问小米云服务（`api.io.mi.com`）
- NVR 系统的正常互联网连接

| 型号 | 标识符 | 协议 | 支持级别 | 说明 |
|------|------------|----------|---------------|-------|
| **小米 C200** | `chuangmi.camera.046c04` | CS2 P2P | ✅ 完全 | HD 1080p 室内摄像头 |
| **小米 C300** | `chuangmi.camera.72ac1` | CS2 P2P | ✅ 完全 | 2K 室内摄像头 |
| **小方** | `isa.camera.isc5c1` | TUTK | ✅ 完全 | 云台球型摄像头（旧款） |
| **鹿客 V1** | `loock.cateye.v01` | TUTK | ✅ 完全 | 智能门铃摄像头（旧款） |
| **Loock V2** | `loock.cateye.v02` | CS2 P2P | ✅ 完全 | 智能门铃摄像头 |
| **大方** | `isa.camera.df3` | TUTK | ✅ 完全 | 云台球型摄像头（旧款） |
| **Aqara G2** | `lumi.camera.gwagl01` | TUTK | ✅ 完全 | 室内立方摄像头（旧款） |
| **IMILAB A1** | `chuangmi.camera.ipc019e` | TUTK | ✅ 完全 | 室内摄像头（旧款） |
| **小白** | `chuangmi.camera.xiaobai` | TUTK | ✅ 完全 | 云台摄像头（旧款） |
| **米家** | `chuangmi.camera.v2` | TUTK | ✅ 完全 | 基础室内摄像头（旧款） |

**重要提示**：现在同时支持 CS2 和 TUTK 协议摄像头。TUTK 旧款型号通过 `internal/tutk/` 使用旧版协议。双工音频功能仅适用于 TUTK 型号（CS2 型号不支持，因为双工音频需要 TUTK 传输）。
## 配置

### 基本配置

```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  region: "cn"
  auto_discovery: true

cameras:
  - id: "xiaomi_c200_front"
    name: "小米 C200 - 前门"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_here"
    vendor: "cs2"
    enabled: true
    # 可选设置
    sub_stream_url: "rtsp://xiaomi-c200-cs2.stream"
    hls_max_fps: 15
    sample_interval: 2
```

### 配置选项

| 字段 | 必需 | 类型 | 默认值 | 说明 |
|-------|----------|------|---------|-------------|
| `enabled` | 是 | boolean | false | 启用小米集成 |
| `user_id` | 是 | string | - | 小米用户 ID |
| `token` | 是 | string | - | 小米 passToken |
| `region` | 否 | string | "cn" | 区域代码（cn、sg、de 等） |
| `auto_discovery` | 否 | boolean | true | 启用自动设备发现 |

### 摄像头配置选项

| 字段 | 必需 | 类型 | 默认值 | 说明 |
|-------|----------|------|---------|-------------|
| `id` | 是 | string | - | 唯一摄像头标识符 |
| `name` | 是 | string | - | 摄像头显示名称 |
| `protocol` | 是 | string | "xiaomi" | 必须为 "xiaomi" |
| `encoding` | 是 | string | "h264" | 视频编码（h264、h265） |
| `did` | 是 | string | - | 小米设备 ID |
| `vendor` | 是 | string | "cs2" | 必须为 "cs2" |
| `enabled` | 否 | boolean | true | 启用摄像头录制 |

### 高级配置

```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  region: "cn"
  auto_discovery: true
  connection_timeout: "30s"
  read_timeout: "60s"
  retry_attempts: 3
  retry_delay: "10s"
  
  # 性能设置
  max_concurrent_cameras: 10
  segment_buffer_size: "10MB"
  
  # 安全设置
  encrypt_credentials: true
  credential_rotation_days: 90

cameras:
  - id: "xiaomi_c300_living_room"
    name: "客厅摄像头"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_12345"
    vendor: "cs2"
    enabled: true
    # 2K 摄像头优化设置
    hls_max_fps: 20
    sample_interval: 1
    segment_duration: "30s"
    
    # 自动备份设置
    snapshot_interval: "5m"
    snapshot_quality: "high"
    
    # 警报设置
    motion_detection: true
    push_notifications: false
```

## 设置方法

### 方法 1：Web UI 设置（推荐）

1. **访问 Web UI**：打开 MiBee NVR 网页界面 `http://localhost:9090`

2. **导航到摄像头**：转到摄像头页面

3. **小米设备发现**：展开"小米设备发现"部分

4. **身份验证**：输入小米账户凭据，点击"登录"

5. **选择设备**：浏览已发现的设备，选择要添加的摄像头

6. **添加到 NVR**：为每个选中的摄像头点击"添加到 NVR"

7. **配置**：为每个摄像头自定义设置（保留策略、质量等）

8. **保存**：点击"保存配置"应用更改

### 方法 2：API 身份验证

使用 API 以编程方式获取身份验证凭据：

```bash
# 小米云身份验证
curl -X POST http://localhost:9090/api/xiaomi/auth \
  -H "Content-Type: application/json" \
  -d '{"username": "your-email@example.com", "password": "your-password"}'

# 响应示例：
{
  "success": true,
  "user_id": "123456789",
  "pass_token": "your_passToken_here",
  "devices": [
    {
      "did": "device_id_12345",
      "name": "小米 C200",
      "model": "chuangmi.camera.046c04",
      "online": true
    }
  ]
}
```

### 方法 3：手动配置

直接编辑配置文件：

```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  region: "cn"

cameras:
  - id: "xiaomi_c200_front"
    name: "小米 C200 - 前门"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_12345"
    vendor: "cs2"
    enabled: true
```

## API 端点

### 小米身份验证

**POST** `/api/xiaomi/auth`
- **请求体**：`{username: string, password: string}`
- **响应**：用户信息和设备列表
- **说明**：小米云身份验证并获取凭据

```bash
curl -X POST http://localhost:9090/api/xiaomi/auth \
  -H "Content-Type: application/json" \
  -d '{"username": "user@example.com", "password": "password"}'
```

### 设备管理

**GET** `/api/xiaomi/devices`
- **响应**：所有小米设备列表
- **说明**：获取账户关联的所有小米设备

```bash
curl -u admin:password http://localhost:9090/api/xiaomi/devices
```

**POST** `/api/xiaomi/sync`
- **响应**：同步状态
- **说明**：从小米云强制同步设备

```bash
curl -X POST -u admin:password http://localhost:9090/api/xiaomi/sync
```

### 摄像头控制

**GET** `/api/xiaomi/cameras/{camera_id}/status`
- **响应**：摄像头状态信息
- **说明**：获取当前摄像头状态

```bash
curl -u admin:password http://localhost:9090/api/xiaomi/cameras/xiaomi_c200_front/status
```

**POST** `/api/xiaomi/cameras/{camera_id}/ptz`
- **请求体**：`{action: string, speed: number}`
- **响应**：PTZ 控制结果
- **说明**：控制云台/变焦功能（支持型号）

```bash
curl -X POST -u admin:password \
  -H "Content-Type: application/json" \
  -d '{"action": "up", "speed": 1}' \
  http://localhost:9090/api/xiaomi/cameras/xiaofang_living_room/ptz
```

### 快照管理

**GET** `/api/xiaomi/cameras/{camera_id}/snapshot`
- **响应**：JPEG 图像数据
- **说明**：从摄像头拍摄快照

```bash
curl -u admin:password -o snapshot.jpg \
  http://localhost:9090/api/xiaomi/cameras/xiaomi_c200_front/snapshot
```

## 双工音频

双工音频允许您通过支持的小米摄像头进行语音交流。此功能仅适用于 TUTK 型号 —— 它需要 TUTK 传输，因此 CS2 摄像头会返回 `two-way audio requires TUTK transport` 错误，不支持双工音频。

### 前置条件

- 浏览器支持 AudioWorklet（Chrome、Firefox、Edge 现代版本）
- 摄像头具备双工音频能力
- 网络延迟低于 200ms 以获得最佳体验

### 配置

在摄像头配置中启用双工音频：

```yaml
cameras:
  - id: "xiaomi_c200_front"
    name: "小米 C200 - 前门"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_here"
    vendor: "cs2"
    enabled: true
    two_way_audio_enabled: true  # 启用双工音频
```

### 使用方法

1. **访问实时视图**：导航到摄像头的实时视图页面

2. **按住说话**：点击并按住麦克风按钮进行说话

3. **松开听音**：松开按钮以收听摄像头的音频

### 技术细节

- 音频编解码器：G.711 μ-law/A-law（8kHz 采样率）

- 协议：通过 CS2 P2P 的 MISS 协议

- 延迟：约 100-200ms（取决于网络）

- 浏览器 API：AudioContext、AudioWorklet 用于编码

### 故障排除

- 无音频：检查浏览器麦克风权限

- 回音：使用耳机而非扬声器

- 延迟：检查到小米云的网络连接

## 云台控制

云台控制（PTZ）适用于支持电机的小米摄像头。这包括大多数球型摄像头（小方、大方、小白）和一些室内摄像头。

### 支持的操作

- `up`, `down`, `left`, `right` — 方向云台控制

- `zoom_in`, `zoom_out` — 变焦控制（如果支持）

- `stop` — 停止移动

### API 使用

**POST** `/api/xiaomi/cameras/{camera_id}/ptz`

- **请求体**：`{action: string, speed: number}`

- **操作**："up", "down", "left", "right", "zoom_in", "zoom_out", "stop"

- **速度**：1-10（1 = 最慢，10 = 最快）

```bash
# 向上移动摄像头
curl -X POST -u admin:password \
  -H "Content-Type: application/json" \
  -d '{"action": "up", "speed": 5}' \
  http://localhost:9090/api/xiaomi/cameras/xiaofang_living_room/ptz

# 放大
curl -X POST -u admin:password \
  -H "Content-Type: application/json" \
  -d '{"action": "zoom_in", "speed": 3}' \
  http://localhost:9090/api/xiaomi/cameras/xiaofang_living_room/ptz

# 停止移动
curl -X POST -u admin:password \
  -H "Content-Type: application/json" \
  -d '{"action": "stop", "speed": 0}' \
  http://localhost:9090/api/xiaomi/cameras/xiaofang_living_room/ptz
```

### 前端集成

Web UI 为支持的摄像头提供屏幕云台控制。当满足以下条件时，控制会自动显示：

- 摄像头型号被识别为具备 PTZ 能力

- 摄像头通过 GetDeviceInfo 报告电机支持

## 设备信息

您可以查询小米摄像头的设备信息，包括固件和硬件版本。

### API 使用

**GET** `/api/xiaomi/cameras/{camera_id}/device-info`

- **响应**：设备信息 JSON

```bash
curl -u admin:password http://localhost:9090/api/xiaomi/cameras/xiaomi_c200_front/device-info
```

**响应示例**：

```json
{
  "model": "chuangmi.camera.046c04",
  "name": "小米 C200",
  "firmware_version": "4.2.0_20231201",
  "hardware_version": "2.0",
  "mac_address": "AA:BB:CC:DD:EE:FF",
  "ip_address": "192.168.1.100",
  "online": true,
  "last_seen": "2024-01-15T10:30:00Z"
}
```

## 集成示例

### Home Assistant 集成

```yaml
# Home Assistant 配置
homeassistant:
  # 小米摄像头集成
  xiaomi:
    username: !secret xiaomi_username
    password: !secret xiaomi_password
    region: cn

# 摄像头实体
camera:
  - platform: mjpeg
    name: "小米 C200 前门"
    mjpeg_url: !secret xiaomi_c200_stream
    authentication: !secret xiaomi_auth

# 运动检测自动化
- id: "xiaomi_motion_alert"
  alias: "小米摄像头检测到运动"
  trigger:
    - platform: mqtt
      topic: "xiaomi/motion/camera_200/front"
      payload: "ON"
  action:
    - service: notify.mobile_app_iphone
      data:
        title: "检测到运动"
        message: "前门检测到运动"
```

### Node-RED 集成

```json
// Node-RED 流 - 小米摄像头运动检测
[
  {"id": "1", "type": "inject", "z": "flow", "name": "测试运动", "payload":"ON", "payloadType":"str", "repeat": "", "crontab": "", "once": false, "onceDelay": 0.1, "topic": "", "x": 120, "y": 140},
  {"id": "2", "type": "switch", "z": "flow", "name": "检测到运动", "property": "payload", "propertyType":"msg", "rules":[{"t":"eq","v":"ON"}],"checkall":"true,"outputs":1,"x": 320, "y": 140},
  {"id": "3", "type": "function", "name": "构建消息", "func": "msg.topic = \"xiaomi/motion/camera_200/front\";\nmsg.payload = {\"action\": \"record\", \"duration\": 60};\nreturn msg;", "outputs": 1, "x": 500, "y": 140},
  {"id": "4", "type": "mqtt out", "z": "flow", "name": "触发录制", "topic": "xiaomi/trigger/+", "qos": "0", "retain": "false", "x": 680, "y": 140}
]
```

### Python 自动化脚本

```python
#!/usr/bin/env python3
import requests
import json
import time
from datetime import datetime

class XiaomiCameraClient:
    def __init__(self, base_url, username, password):
        self.base_url = base_url
        self.auth = (username, password)
        self.session = requests.Session()
    
    def authenticate(self, xiaomi_username, xiaomi_password):
        """小米云身份验证"""
        url = f"{self.base_url}/api/xiaomi/auth"
        data = {
            "username": xiaomi_username,
            "password": xiaomi_password
        }
        
        response = self.session.post(url, json=data)
        response.raise_for_status()
        return response.json()
    
    def get_devices(self):
        """获取所有小米设备"""
        url = f"{self.base_url}/api/xiaomi/devices"
        response = self.session.get(url, auth=self.auth)
        response.raise_for_status()
        return response.json()
    
    def take_snapshot(self, camera_id):
        """从摄像头拍摄快照"""
        url = f"{self.base_url}/api/xiaomi/cameras/{camera_id}/snapshot"
        response = self.session.get(url, auth=self.auth)
        response.raise_for_status()
        return response.content
    
    def get_camera_status(self, camera_id):
        """获取摄像头状态"""
        url = f"{self.base_url}/api/xiaomi/cameras/{camera_id}/status"
        response = self.session.get(url, auth=self.auth)
        response.raise_for_status()
        return response.json()
    
    def trigger_recording(self, camera_id, duration=60):
        """触发摄像头录制"""
        url = f"{self.base_url}/api/xiaomi/cameras/{camera_id}/trigger"
        data = {
            "action": "record",
            "duration": duration
        }
        
        response = self.session.post(url, json=data, auth=self.auth)
        response.raise_for_status()
        return response.json()

def main():
    # 配置
    NVR_URL = "http://localhost:9090"
    NVR_USERNAME = "admin"
    NVR_PASSWORD = "password"
    XIAOMI_USERNAME = "user@example.com"
    XIAOMI_PASSWORD = "xiaomi_password"
    
    # 创建客户端
    client = XiaomiCameraClient(NVR_URL, NVR_USERNAME, NVR_PASSWORD)
    
    try:
        # 小米身份验证
        print("正在向小米进行身份验证...")
        auth_result = client.authenticate(XIAOMI_USERNAME, XIAOMI_PASSWORD)
        print(f"用户 ID: {auth_result['user_id']}")
        print(f"发现设备数: {len(auth_result['devices'])}")
        
        # 列出设备
        print("\n可用设备:")
        for device in auth_result['devices']:
            print(f"- {device['name']} (DID: {device['did']})")
        
        # 监控特定摄像头
        camera_id = "xiaomi_c200_front"  # 替换为你的摄像头 ID
        
        # 获取摄像头状态
        status = client.get_camera_status(camera_id)
        print(f"\n摄像头 {camera_id} 状态:")
        print(json.dumps(status, indent=2))
        
        # 拍摄快照
        print(f"\n从 {camera_id} 拍摄快照...")
        snapshot_data = client.take_snapshot(camera_id)
        
        # 保存快照
        timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
        filename = f"xiaomi_snapshot_{timestamp}.jpg"
        with open(filename, 'wb') as f:
            f.write(snapshot_data)
        print(f"快照已保存为 {filename}")
        
    except Exception as e:
        print(f"错误: {e}")

if __name__ == "__main__":
    main()
```

### Shell 监控脚本

```bash
#!/bin/bash
# 小米摄像头监控脚本

NVR_URL="http://localhost:9090"
NVR_USER="admin"
NVR_PASS="password"
LOG_FILE="/var/log/xiaomi_monitor.log"

log_message() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') - $1" >> "$LOG_FILE"
}

# 检查小米设备状态
check_devices() {
    response=$(curl -s -u "$NVR_USER:$NVR_PASS" "$NVR_URL/api/xiaomi/devices" 2>/dev/null)
    
    if [[ $? -eq 0 && $response != *"error"* ]]; then
        online_count=$(echo "$response" | grep -o '"online":true' | wc -l)
        total_count=$(echo "$response" | grep -o '"did"' | wc -l)
        
        log_message "小米设备状态: $online_count/$total_count 在线"
        
        # 检查是否有设备离线
        if [[ $online_count -lt $total_count ]]; then
            log_message "警告: 部分小米设备离线"
            # 发送通知
            # curl -X POST -d "message: 部分小米设备离线" "your-webhook-url"
        fi
    else
        log_message "错误: 获取小米设备状态失败"
    fi
}

# 定期拍摄快照
take_snapshots() {
    cameras=$(curl -s -u "$NVR_USER:$NVR_PASS" "$NVR_URL/api/xiaomi/devices" | grep -o '"did":"[^"]*"' | cut -d'"' -f4)
    
    for camera in $cameras; do
        # 如果摄像头 ID 为空则跳过
        [[ -z "$camera" ]] && continue
        
        # 拍摄快照
        response=$(curl -s -u "$NVR_USER:$NVR_PASS" -o "/tmp/snapshot_${camera}.jpg" \
                  "$NVR_URL/api/xiaomi/cameras/${camera}/snapshot" 2>/dev/null)
        
        if [[ $? -eq 0 && -f "/tmp/snapshot_${camera}.jpg" ]]; then
            file_size=$(stat -c%s "/tmp/snapshot_${camera}.jpg")
            log_message "为 $camera 拍摄快照: ${file_size} 字节"
            
            # 归档快照
            timestamp=$(date '+%Y%m%d_%H%M%S')
            cp "/tmp/snapshot_${camera}.jpg" "/var/backups/xiaomi/snapshot_${camera}_${timestamp}.jpg"
        fi
    done
}

# 主监控循环
while true; do
    check_devices
    take_snapshots
    
    # 等待 5 分钟后进行下次检查
    sleep 300
done
```

## 安全考虑

### 身份验证安全

**令牌存储**：

```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  encrypt_credentials: true  # 如果在您的版本中可用，请启用
```

**文件权限**：

```bash
# 安全配置文件
chmod 600 mibee-nvr.yaml
chown nvr:nvr mibee-nvr.yaml
```

### 网络安全

**防火墙配置**：

```bash
# 允许访问小米云服务
ufw allow to api.io.mi.com port 443 proto tcp

# 限制 NVR 访问
ufw allow from 192.168.1.0/24 to any port 9090 proto tcp
```

### 网络要求

**必需的访问**：
- `api.io.mi.com:443` - 小米云 API（设备列表、MISS URL 解析；区域变体 `<region>.api.io.mi.com`）
- `account.xiaomi.com:443` - 小米身份验证

**网络故障排除**：

```bash
# 测试到小米服务的连接性
curl -v https://api.io.mi.com

# 测试 DNS 解析
nslookup api.io.mi.com
```

## 性能优化

### 摄像头特定设置

**对于高分辨率摄像头（C300）**：

```yaml
cameras:
  - id: "xiaomi_c300"
    name: "小米 C300 - 客厅"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_12345"
    vendor: "cs2"
    enabled: true
    hls_max_fps: 20        # 2K 摄像头使用更高帧率
    sample_interval: 1      # 更频繁的采样
    segment_duration: "30s"  # 标准片段时长
    snapshot_interval: "5m" # 定期快照
```

**对于低带宽网络**：

```yaml
cameras:
  - id: "xiaomi_c200"
    name: "小米 C200 - 后院"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_67890"
    vendor: "cs2"
    enabled: true
    hls_max_fps: 10        # 更低帧率
    sample_interval: 3      # 较不频繁的采样
    snapshot_quality: "medium"  # 较低质量快照
```

### 系统级设置

```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  region: "cn"
  
  # 性能优化
  connection_timeout: "20s"    # 更快的超时
  read_timeout: "45s"        # 根据网络条件调整
  retry_attempts: 2          # 较少重试
  retry_delay: "5s"          # 更快的重试
  
  # 内存管理
  max_concurrent_cameras: 5   # 限制并发连接数
  segment_buffer_size: "5MB" # 受限 RAM 使用较小缓冲区
```

## 故障排除

### 常见问题

#### "身份验证失败"错误

```
错误: 小米身份验证失败: 无效凭据
```

**原因**：
- 无效的小米用户名/密码
- 账户需要验证码验证
- 启用了双因素身份验证
- 小米云服务问题

**解决方案**：

```bash
# 手动测试小米凭据
curl -X POST https://api.io.mi.com/login \
  -H "Content-Type: application/json" \
  -d '{"username": "user@example.com", "password": "password"}'

# 在米家应用中检查账户状态
# 验证不需要验证码
# 如果需要，尝试使用新的小米账户
```

#### "设备未找到"错误

```
错误: 小米设备未找到: device_id_12345
```

**原因**：
- 摄像头未绑定到小米账户
- 摄像头离线
- 设备 ID（DID）不正确
- 区域不匹配

**解决方案**：

```bash
# 列出可用设备
curl -u admin:password http://localhost:9090/api/xiaomi/devices

# 检查设备在线状态
curl -u admin:password http://localhost:9090/api/xiaomi/cameras/device_id_12345/status

# 在米家应用中验证摄像头在线
# 检查网络连接
# 尝试刷新设备列表
```

#### "录制失败"错误

```
错误: 小米录制失败: 设备离线
```

**原因**：
- 网络连接问题
- 小米云服务停机
- 摄像头电池耗尽（无线型号）
- 身份验证令牌已过期

**解决方案**：

```bash
# 测试到小米服务的网络连接
ping api.io.mi.com
curl -v https://api.io.mi.com

# 检查令牌有效性
curl -u admin:password http://localhost:9090/api/xiaomi/auth

# 如果需要，重新身份验证
curl -X POST -H "Content-Type: application/json" \
  -d '{"username": "user@example.com", "password": "new_password"}' \
  http://localhost:9090/api/xiaomi/auth
```

#### 流质量相关问题

**症状**：视频卡顿、高延迟、分辨率差

**解决方案**：

```yaml
# 调整摄像头设置
cameras:
  - id: "xiaomi_camera"
    hls_max_fps: 15        # 降低帧率
    sample_interval: 2      # 增加采样间隔
    segment_duration: "15s" # 更短的片段
```

### 调试模式

启用详细日志进行故障排除：

```yaml
observability:
  log_level: "debug"

xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  debug: true  # 启用详细的小米日志
```

**检查日志**：

```bash
# 系统日志
journalctl -u mibee-nvr -f | grep xiaomi

# Docker 日志
docker logs -f mibee-nvr | grep xiaomi

# 配置验证
./mibee-nvr -config mibee-nvr.yaml --validate
```

### 性能监控

监控小米摄像头性能：

```bash
#!/bin/bash
# 小米性能监控脚本

LOG_FILE="/var/log/xiaomi_performance.log"
NVR_URL="http://localhost:9090"
NVR_USER="admin"
NVR_PASS="password"

# 监控响应时间
response_time=$(curl -o /dev/null -s -w '%{time_total}' -u "$NVR_USER:$NVR_PASS" "$NVR_URL/api/xiaomi/devices")
echo "$(date) - 小米 API 响应时间: ${response_time}s" >> "$LOG_FILE"

# 监控摄像头状态
status=$(curl -s -u "$NVR_USER:$NVR_PASS" "$NVR_URL/api/xiaomi/devices")
online_count=$(echo "$status" | grep -o '"online":true' | wc -l)
echo "$(date) - 在线摄像头: $online_count" >> "$LOG_FILE"
```

## 迁移和更新

### 令牌轮换

定期轮换小米凭据以确保安全：

```bash
#!/bin/bash
# 令牌轮换脚本

NVR_URL="http://localhost:9090"
NVR_USER="admin"
NVR_PASS="password"
NEW_PASSWORD="new_xiaomi_password"

# 获取新的身份验证
curl -X POST -H "Content-Type: application/json" \
  -d "{\"username\": \"user@example.com\", \"password\": \"$NEW_PASSWORD\"}" \
  "$NVR_URL/api/xiaomi/auth" | jq '.user_id, .pass_token'

# 使用新令牌更新配置
#（需要在配置文件中手动更新）
```

### 版本兼容性

**检查兼容性**：

```bash
# 检查当前版本
./mibee-nvr --version

# 检查更新
curl -s https://api.github.com/repos/Mi-Bee-Studio/MiBeeNvr/releases/latest | grep 'tag_name'
```

### 备份配置

定期备份小米配置：

```bash
#!/bin/bash
# 小米配置备份

BACKUP_DIR="/var/backups/xiaomi"
DATE=$(date '+%Y%m%d_%H%M%S')
CONFIG_FILE="/var/lib/mibee-nvr/mibee-nvr.yaml"

# 创建备份目录
mkdir -p "$BACKUP_DIR"

# 备份配置（移除敏感数据）
grep -v 'token:' "$CONFIG_FILE" > "$BACKUP_DIR/mibee-nvr_config_${DATE}.yaml"

# 备份设备列表
curl -s -u "admin:password" "http://localhost:9090/api/xiaomi/devices" \
  > "$BACKUP_DIR/xiaomi_devices_${DATE}.json"

echo "备份完成: $BACKUP_DIR"
```

## 最佳实践

1. **定期更新**：保持 MiBee NVR 更新以获取最新的小米协议支持
2. **网络监控**：确保到小米云服务的可靠连接
3. **令牌管理**：定期轮换小米凭据
4. **备份策略**：定期备份配置和设备列表
5. **监控**：设置摄像头可用性和性能监控
6. **安全**：使用强密码并限制网络访问
7. **文档**：保持文档更新，包含摄像头配置
8. **测试**：定期测试摄像头功能和警报

### 监控仪表板

创建一个简单的监控仪表板：

```python
#!/usr/bin/env python3
import requests
import json
from datetime import datetime

def get_xiaomi_status():
    """获取综合小米状态"""
    try:
        # 获取设备
        devices = requests.get("http://localhost:9090/api/xiaomi/devices", 
                            auth=("admin", "password")).json()
        
        online_count = sum(1 for d in devices if d.get('online', False))
        total_count = len(devices)
        
        # 获取最近的录制
        recent = requests.get("http://localhost:9090/api/recordings?limit=10",
                            auth=("admin", "password")).json()
        
        # 创建状态报告
        status = {
            "timestamp": datetime.now().isoformat(),
            "xiaomi_devices": {
                "total": total_count,
                "online": online_count,
                "offline": total_count - online_count,
                "health": (online_count / total_count * 100) if total_count > 0 else 0
            },
            "recent_recordings": len(recent.get("recordings", [])),
            "last_check": datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        }
        
        return status
        
    except Exception as e:
        return {"error": str(e), "timestamp": datetime.now().isoformat()}

if __name__ == "__main__":
    status = get_xiaomi_status()
    print(json.dumps(status, indent=2))
```

通过全面的小米摄像头集成，MiBee NVR 实现了基于云的摄像头录制，具有丰富的自动化功能，让您可以轻松地将小米摄像头集成到您的监控和智能家居生态系统中。