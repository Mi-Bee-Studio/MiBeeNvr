# MQTT Integration

MiBee NVR supports MQTT-based recording triggers for smart home automation and event-driven recording. When an MQTT message is received, the system can start recording on a specific camera for a configurable duration.

## Overview

- **Protocol**: MQTT (Message Queuing Telemetry Transport)
- **Topic Pattern**: `{prefix}/trigger/{camera_id}`
- **Payload**: JSON with `action` field
- **Actions**: `record`, `stop`, `snapshot`
- **Auto-reconnect**: Built-in with exponential backoff

## Configuration

### Basic Setup

```yaml
mqtt:
  broker_url: "tcp://192.168.1.100:1883"
  client_id: "mibee-nvr"
  topic_prefix: "mibee"
  username: "mqtt_user"
  password: "mqtt_password"
```

### Configuration Options

| Field | Required | Type | Default | Description |
|-------|----------|------|---------|-------------|
| `broker_url` | Yes | string | - | MQTT broker address (e.g., `tcp://192.168.1.100:1883`) |
| `client_id` | Yes | string | - | Unique client ID for MQTT connection |
| `topic_prefix` | Yes | string | - | Prefix for trigger topics (e.g., `mibee`) |
| `username` | No | string | - | MQTT username (if broker requires auth) |
| `password` | No | string | - | MQTT password (if broker requires auth) |

### Example Configuration

```yaml
mqtt:
  broker_url: "tcp://mqtt.example.com:1883"
  client_id: "mibee-nvr-home"
  topic_prefix: "home/security"
  username: "smart_home_user"
  password: "secure_password_123"
```

## Usage

### MQTT Messages

#### Trigger Recording

Start recording on a specific camera for a set duration:

```json
{
  "action": "record"
}
```

**Topic**: `home/security/trigger/front-door`  
**Message**: `{"action": "record"}`

**Topic**: `home/security/trigger/backyard`  
**Message**: `{"action": "record"}`

#### Stop Recording

Stop recording on a specific camera:

```json
{
  "action": "stop"
}
```

**Topic**: `home/security/trigger/front-door`  
**Message**: `{"action": "stop"}`

#### Trigger Snapshot

Take a snapshot from a specific camera:

```json
{
  "action": "snapshot"
}
```

**Topic**: `home/security/trigger/front-door`  
**Message**: `{"action": "snapshot"}`

### Integration Examples

#### Home Assistant

```yaml
# Home Assistant MQTT Automation
- alias: Motion Detection - Front Door
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
// Node-RED Flow Example
[
  {"id": "1", "type": "mqtt in", "topic": "zigbee2mqtt/occupancy/+", "z": "2"},
  {"id": "2", "type": "switch", "name": "Motion Detection", "property": "payload", "propertyType": "msg", "rules": [{"t": "eq", "v": "ON"}], "checkall": "true", "outputs": 1},
  {"id": "3", "type": "function", "name": "Get Camera ID", "func": "const camera = msg.topic.split('/')[1]; msg.camera = camera; return msg;", "outputs": 1},
  {"id": "4", "type": "function", "name": "Build MQTT Message", "func": "msg.topic = `home/security/trigger/${msg.camera}`; msg.payload = '{\"action\": \"record\"}'; return msg;", "outputs": 1},
  {"id": "5", "type": "mqtt out", "topic": "home/security/trigger/+", "qos": "0", "retain": "false"}
]
```

#### ESP8266/ESP32

```cpp
// ESP8266 Motion Detection Example
#include <PubSubClient.h>
#include <WiFi.h>

const char* mqtt_server = "192.168.1.100";
const char* topic_prefix = "home/security";

void setup() {
  pinMode(D1, INPUT); // PIR sensor pin
  WiFi.begin("your_ssid", "password");
  
  client.setServer(mqtt_server, 1883);
}

void loop() {
  if (digitalRead(D1) == HIGH) {
    // Motion detected, trigger recording
    String payload = "{\"action\": \"record\"}";
    String topic = String(topic_prefix) + "/trigger/front-door";
    client.publish(topic.c_str(), payload.c_str());
    delay(30000); // Wait 30 seconds before next trigger
  }
  delay(1000);
}
```

#### Python Script

```python
# Python MQTT Publisher Example
import paho.mqtt.client as mqtt
import json
import time

def on_connect(client, userdata, flags, rc):
    print("Connected to MQTT broker")

client = mqtt.Client("mibee-nvr-trigger")
client.username_pw_set("mqtt_user", "mqtt_password")
client.connect("192.168.1.100", 1883)

# Trigger recording from Python
def trigger_recording(camera_id, action="record"):
    topic = f"home/security/trigger/{camera_id}"
    payload = json.dumps({"action": action})
    client.publish(topic, payload)
    print(f"Triggered {action} for {camera_id}")

# Example usage
trigger_recording("front-door", "record")
time.sleep(30)  # Record for 30 seconds
trigger_recording("front-door", "stop")
```

### Monitoring

#### System Logs

Check MQTT connection status and messages:

```bash
# View system logs
journalctl -u mibee-nvr -f | grep mqtt

# Docker logs
docker logs -f mibee-nvr | grep mqtt
```

#### Health Check

Verify MQTT client status:

```bash
curl -u admin:password http://localhost:9090/api/system/health
```

Look for `"mqtt": {"status": "ok"}` in the response.

### Error Handling

#### Common Issues

**Connection Failed**
```
WARN: mqtt connection failed: connection refused
```
- **Solution**: Check broker URL, ensure broker is running
- **Debug**: Test with `mosquitto_pub -h 192.168.1.100 -t test -m hello`

**Authentication Failed**
```
WARN: mqtt authentication failed
```
- **Solution**: Verify username and password in configuration
- **Debug**: Test with credentials: `mosquitto_pub -u user -P pass -h broker -t test -m hello`

**Message Not Processed**
```
WARN: invalid mqtt message payload format
```
- **Solution**: Ensure JSON payload contains `action` field
- **Debug**: Validate payload with JSON validator

### Advanced Configuration

#### Multiple Topics

```yaml
mqtt:
  broker_url: "tcp://192.168.1.100:1883"
  client_id: "mibee-nvr"
  topic_prefix: "security"
```

Multiple cameras can be triggered on the same broker:

```
security/trigger/camera1
security/trigger/camera2
security/trigger/camera3
```

#### Broker Security

```yaml
mqtt:
  broker_url: "ssl://mqtt.example.com:8883"
  client_id: "mibee-nvr"
  topic_prefix: "home/security"
  username: "secure_user"
  password: "complex_password"
  # For SSL, certificates must be properly configured
  ca_cert: "/path/to/ca.crt"
  client_cert: "/path/to/client.crt"
  client_key: "/path/to/client.key"
```

### Integration with Other Systems

#### Zigbee2MQTT

```yaml
# Zigbee2MQTT Integration
mqtt:
  broker_url: "tcp://192.168.1.100:1883"
  client_id: "mibee-nvr"
  topic_prefix: "zigbee2mqtt"

# Camera trigger from motion sensor
```

#### Home Assistant Automation

```yaml
# Home Assistant automation to record when someone rings doorbell
- alias: Doorbell Recording
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

### Best Practices

1. **Topic Organization**: Use meaningful topic prefixes to organize different areas
2. **Payload Validation**: Always include the `action` field in JSON payloads
3. **Connection Reliability**: Configure broker for auto-reconnect
4. **Error Handling**: Implement retry logic in client applications
5. **Message Retention**: Use appropriate message retention policies on the broker
6. **Security**: Use strong passwords and SSL/TLS for production deployments

### Troubleshooting

#### Connection Issues

1. Verify broker is reachable:
   ```bash
   ping 192.168.1.100
   nc -zv 192.168.1.100 1883
   ```

2. Test MQTT connection manually:
   ```bash
   mosquitto_sub -h 192.168.1.100 -t "test" -u user -P pass
   mosquitto_pub -h 192.168.1.100 -t "test" -m "hello" -u user -P pass
   ```

#### Message Delivery Issues

1. Check logs for processing errors:
   ```bash
   journalctl -u mibee-nvr -f | grep mqtt
   ```

2. Verify topic matching:
   ```bash
   # Test topic matching
   echo '{"action": "record"}' | mosquitto_pub -h 192.168.1.100 -t "security/trigger/camera1" -u user -P pass
   ```

#### Configuration Validation

Validate configuration file:
```bash
./mibee-nvr -config mibee-nvr.yaml --validate
```

#### Debug Mode

Enable debug logging:
```yaml
mqtt:
  broker_url: "tcp://192.168.1.100:1883"
  client_id: "mibee-nvr"
  topic_prefix: "home/security"

observability:
  log_level: "debug"
```