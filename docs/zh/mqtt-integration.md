# MQTT 集成

MiBee NVR 支持 MQTT 触发录制与状态发布，用于智能家居自动化和事件驱动录制。接收 MQTT 消息可以启动/停止摄像头录制；开启状态发布后，健康告警与录像事件也会推送到 MQTT。

## 概述

- **协议**: MQTT (Message Queuing Telemetry Transport)
- **触发（订阅）**: `{prefix}/trigger/{camera_id}`
- **状态（发布）**: `{prefix}/health/{camera_id}`、`{prefix}/event/{topic}`（见[状态发布](#状态发布)）
- **触发负载**: 包含 `action` 字段的 JSON
- **触发操作**: `record`, `stop`（`snapshot` 尚未实现，收到后仅记录日志）
- **自动重连**: 内置指数退避重连

## 配置

### 基本设置

```yaml
mqtt:
  enabled: true
  broker: "tcp://192.168.1.100:1883"
  client_id: "mibee-nvr"
  topic: "mibee"
  username: "mqtt_user"
  password: "mqtt_password"
```

### 配置选项

| 字段 | 必需 | 类型 | 默认值 | 描述 |
|------|------|------|--------|------|
| `enabled` | 是 | bool | `false` | 启用 MQTT 客户端 |
| `broker` | 是 | string | - | MQTT 代理地址（如 `tcp://192.168.1.100:1883`） |
| `client_id` | 是 | string | - | MQTT 连接的唯一客户端 ID |
| `topic` | 是 | string | - | 主题前缀（如 `mibee`），实际订阅 `{topic}/trigger/+` |
| `username` | 否 | string | - | MQTT 用户名（如果代理需要认证） |
| `password` | 否 | string | - | MQTT 密码（可经 `mibee-nvr encrypt-config` 加密存储） |
| `status_events` | 否 | bool | `false` | 将录像/摄像头事件转发到 `{topic}/event/<topic>`（见状态发布） |

### 完整配置示例

```yaml
mqtt:
  enabled: true
  broker: "tcp://mqtt.example.com:1883"
  client_id: "mibee-nvr-home"
  topic: "home/security"
  username: "smart_home_user"
  password: "secure_password_123"
  status_events: true
```

## 使用方法

### MQTT 消息

#### 触发录制

开始录制特定摄像头一段时间：

```json
{
  "action": "record"
}
```

**主题**: `home/security/trigger/front-door`  
**消息**: `{"action": "record"}`

**主题**: `home/security/trigger/backyard`  
**消息**: `{"action": "record"}`

#### 停止录制

停止录制特定摄像头：

```json
{
  "action": "stop"
}
```

**主题**: `home/security/trigger/front-door`  
**消息**: `{"action": "stop"}`

#### 触发快照

从特定摄像头拍摄快照（**尚未实现**：当前收到 `snapshot` 只会记录日志，不执行任何操作，快照落盘能力规划中，见 #656）：

```json
{
  "action": "snapshot"
}
```

**主题**: `home/security/trigger/front-door`  
**消息**: `{"action": "snapshot"}`

### 状态发布

NVR 会将以下状态发布到 MQTT（均需 `mqtt.enabled: true`）：

#### 健康告警 — `{prefix}/health/{camera_id}`

门控：`mqtt.enabled: true` **且** `health.alerts.mqtt: true`。摄像头断连、码流冻结、连接恢复等健康事件会发布到该主题，负载为健康事件 JSON：

```json
{
  "id": "evt-123",
  "camera_id": "front-door",
  "event_type": "connection_lost",
  "status": "warning",
  "message": "no frames for 30s",
  "created_at": "2026-09-01T12:00:00Z"
}
```

同一事件的重复告警受 `health.alerts.cooldown`（默认 5m）抑制；持续异常会升级为 `error`，恢复类事件（`connection_restored`、`freeze_recovered`）永不升级。

#### 事件转发 — `{prefix}/event/{topic}`

门控：`mqtt.enabled: true` **且** `mqtt.status_events: true`。以下事件总线主题会被原样转发（负载为对应事件的 JSON）：

| 事件主题 | MQTT 主题 | 负载要点 |
|----------|-----------|----------|
| `segment.completed` | `{prefix}/event/segment.completed` | `camera_id`、`file_path`、`format`、`encoding`、`started_at`、`ended_at`、`file_size`、`recording_id` |
| `camera.added` | `{prefix}/event/camera.added` | `camera_id`、`name`、`endpoint`、`source` |
| `camera.quality` | `{prefix}/event/camera.quality` | `camera_id`、`from`、`to`、`reason` |
| `storage.health.changed` | `{prefix}/event/storage.health.changed` | `camera_id`、`previous_state`、`current_state`、`message` |

高频主题（如 AI 检测）刻意不在转发白名单内。消息 QoS 1、不保留（retain=false）；代理缓慢时事件会被丢弃（事件总线满即丢，不阻塞录制）。

### 集成示例

#### Home Assistant

```yaml
# Home Assistant MQTT 自动化
- alias: 检测到运动 - 前门
  trigger:
    - platform: mqtt
      topic: "zigbee2mqtt/front-door-motion/occupancy"
      payload: "ON"
  action:
    - service: mqtt.publish
      data:
        topic: "home/security/trigger/front-door"
        payload: '{"action": "record"}'
        retain: false
```

#### Node-RED

```json
// Node-RED 流示例
[
  {"id": "1", "type": "mqtt in", "topic": "zigbee2mqtt/occupancy/+", "z": "2"},
  {"id": "2", "type": "switch", "name": "运动检测", "property": "payload", "propertyType": "msg", "rules": [{"t": "eq", "v": "ON"}], "checkall": "true", "outputs": 1},
  {"id": "3", "type": "function", "name": "获取摄像头 ID", "func": "const camera = msg.topic.split('/')[1]; msg.camera = camera; return msg;", "outputs": 1},
  {"id": "4", "type": "function", "name": "构建 MQTT 消息", "func": "msg.topic = `home/security/trigger/${msg.camera}`; msg.payload = '{\"action\": \"record\"}'; return msg;", "outputs": 1},
  {"id": "5", "type": "mqtt out", "topic": "home/security/trigger/+", "qos": "0", "retain": "false"}
]
```

#### ESP8266/ESP32

```cpp
// ESP8266 运动检测示例
#include <PubSubClient.h>
#include <WiFi.h>

const char* mqtt_server = "192.168.1.100";
const char* topic_prefix = "home/security";

void setup() {
  pinMode(D1, INPUT); // PIR 传感器引脚
  WiFi.begin("your_ssid", "password");
  
  client.setServer(mqtt_server, 1883);
}

void loop() {
  if (digitalRead(D1) == HIGH) {
    // 检测到运动，触发录制
    String payload = "{\"action\": \"record\"}";
    String topic = String(topic_prefix) + "/trigger/front-door";
    client.publish(topic.c_str(), payload.c_str());
    delay(30000); // 等待 30 秒后下次触发
  }
  delay(1000);
}
```

#### Python 脚本

```python
# Python MQTT 发布器示例
import paho.mqtt.client as mqtt
import json
import time

def on_connect(client, userdata, flags, rc):
    print("已连接到 MQTT 代理")

client = mqtt.Client("mibee-nvr-trigger")
client.username_pw_set("mqtt_user", "mqtt_password")
client.connect("192.168.1.100", 1883)

# 从 Python 触发录制
def trigger_recording(camera_id, action="record"):
    topic = f"home/security/trigger/{camera_id}"
    payload = json.dumps({"action": action})
    client.publish(topic, payload)
    print(f"触发 {camera_id} 的 {action}")

# 使用示例
trigger_recording("front-door", "record")
time.sleep(30)  # 录制 30 秒
trigger_recording("front-door", "stop")
```

### 监控

#### 系统日志

检查 MQTT 连接状态和消息：

```bash
# 查看系统日志
journalctl -u mibee-nvr -f | grep mqtt

# Docker 日志
docker logs -f mibee-nvr | grep mqtt
```

#### 健康检查

验证 MQTT 客户端状态：

```bash
curl -u admin:password http://localhost:9090/api/system/health
```

在响应中查找 `"mqtt": {"status": "ok"}`。

### 错误处理

#### 常见问题

**连接失败**
```text
WARN: mqtt connection failed: connection refused
```
**解决方案**：检查代理 URL，确保代理正在运行
**调试**：使用 `mosquitto_pub -h 192.168.1.100 -t test -m hello` 进行测试

**认证失败**
```text
WARN: mqtt authentication failed
```
**解决方案**：验证配置中的用户名和密码
**调试**：使用凭据测试：`mosquitto_pub -u user -P pass -h broker -t test -m hello`

**消息未处理**
```text
WARN: invalid mqtt message payload format
```
**解决方案**：确保 JSON 负载包含 `action` 字段
**调试**：使用 JSON 验证器验证负载

### 高级配置

#### 多个主题

```yaml
mqtt:
  broker_url: "tcp://192.168.1.100:1883"
  client_id: "mibee-nvr"
  topic_prefix: "security"
```

多个摄像头可以在同一个代理上被触发：

```text
security/trigger/camera1
security/trigger/camera2
security/trigger/camera3
```

#### 代理安全

```yaml
mqtt:
  broker_url: "ssl://mqtt.example.com:8883"
  client_id: "mibee-nvr"
  topic_prefix: "home/security"
  username: "secure_user"
  password: "complex_password"
  # SSL 需要 正确配置证书
  ca_cert: "/path/to/ca.crt"
  client_cert: "/path/to/client.crt"
  client_key: "/path/to/client.key"
```

### 与其他系统集成

#### Zigbee2MQTT

```yaml
# Zigbee2MQTT 集成
mqtt:
  broker_url: "tcp://192.168.1.100:1883"
  client_id: "mibee-nvr"
  topic_prefix: "zigbee2mqtt"

# 从运动传感器触发摄像头
```

#### Home Assistant 自动化

```yaml
# 门铃触发的 Home Assistant 自动化录制
- alias: 门铃录制
  trigger:
    - platform: mqtt
      topic: "zigbee2mqtt/doorbell/bell_pressed"
      payload: "ON"
  action:
    - service: mqtt.publish
      data:
        topic: "zigbee2mqtt/trigger/doorbell"
        payload: '{"action": "record", "duration": 60}'
        retain: false
```

### 最佳实践

1. **主题组织**：使用有意义的前缀来组织不同区域
2. **负载验证**：在 JSON 负载中始终包含 `action` 字段
3. **连接可靠性**：配置代理用于自动重连
4. **错误处理**：在客户端应用程序中实现重试逻辑
5. **消息保留**：在代理上使用适当的消息保留策略
6. **安全性**：为生产部署使用强密码和 SSL/TLS

### 故障排除

#### 连接问题

1. 验证代理是否可达：
   ```bash
   ping 192.168.1.100
   nc -zv 192.168.1.100 1883
   ```

2. 手动测试 MQTT 连接：
   ```bash
   mosquitto_sub -h 192.168.1.100 -t "test" -u user -P pass
   mosquitto_pub -h 192.168.1.100 -t "test" -m "hello" -u user -P pass
   ```

#### 消息传递问题

1. 检查日志中的处理错误：
   ```bash
   journalctl -u mibee-nvr -f | grep mqtt
   ```

2. 验证主题匹配：
   ```bash
   # 测试主题匹配
   echo '{"action": "record"}' | mosquitto_pub -h 192.168.1.100 -t "security/trigger/camera1" -u user -P pass
   ```

#### 配置验证

验证配置文件：
```bash
./mibee-nvr -config mibee-nvr.yaml --validate
```

#### 调试模式

启用调试日志：
```yaml
mqtt:
  broker_url: "tcp://192.168.1.100:1883"
  client_id: "mibee-nvr"
  topic_prefix: "home/security"

observability:
  log_level: "debug"
```