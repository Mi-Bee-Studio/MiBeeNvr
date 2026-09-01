# MQTT Integration

MiBee NVR supports MQTT recording triggers and status publishing for smart home automation and event-driven recording. Incoming MQTT messages can start/stop camera recording; with status publishing enabled, health alerts and recording events are also pushed to MQTT.

## Overview

- **Protocol**: MQTT (Message Queuing Telemetry Transport)
- **Trigger (subscribe)**: `{prefix}/trigger/{camera_id}`
- **Status (publish)**: `{prefix}/health/{camera_id}`, `{prefix}/event/{topic}` (see [Status Publishing](#status-publishing))
- **Trigger payload**: JSON with `action` field
- **Actions**: `record`, `stop` (`snapshot` is not implemented yet — logged and ignored)
- **Auto-reconnect**: Built-in with exponential backoff

## Configuration

### Basic Setup

```yaml
mqtt:
  enabled: true
  broker: "tcp://192.168.1.100:1883"
  client_id: "mibee-nvr"
  topic: "mibee"
  username: "mqtt_user"
  password: "mqtt_password"
```

### Configuration Options

| Field | Required | Type | Default | Description |
|-------|----------|------|---------|-------------|
| `enabled` | Yes | bool | `false` | Enable the MQTT client |
| `broker` | Yes | string | - | MQTT broker address (e.g., `tcp://192.168.1.100:1883`) |
| `client_id` | Yes | string | - | Unique client ID for MQTT connection |
| `topic` | Yes | string | - | Topic prefix (e.g., `mibee`); the client subscribes to `{topic}/trigger/+` |
| `username` | No | string | - | MQTT username (if broker requires auth) |
| `password` | No | string | - | MQTT password (can be encrypted via `mibee-nvr encrypt-config`) |
| `status_events` | No | bool | `false` | Forward recording/camera events to `{topic}/event/<topic>` (see Status Publishing) |

### Example Configuration

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

Take a snapshot from a specific camera (**not implemented yet**: a `snapshot` action is currently only logged, nothing is executed; snapshot persistence is planned, see #656):

```json
{
  "action": "snapshot"
}
```

**Topic**: `home/security/trigger/front-door`  
**Message**: `{"action": "snapshot"}`

### Status Publishing

The NVR publishes the following status to MQTT (all require `mqtt.enabled: true`):

#### Health alerts — `{prefix}/health/{camera_id}`

Gating: `mqtt.enabled: true` **and** `health.alerts.mqtt: true`. Camera health events (connection lost, stream freeze, connection restored, etc.) are published to this topic with the health-event JSON as payload:

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

Repeated alerts for the same event are suppressed by `health.alerts.cooldown` (default 5m); persistent issues escalate to `error`, while positive events (`connection_restored`, `freeze_recovered`) never escalate.

#### Event forwarding — `{prefix}/event/{topic}`

Gating: `mqtt.enabled: true` **and** `mqtt.status_events: true`. The following event-bus topics are forwarded as-is (payload is the event's JSON):

| Event topic | MQTT topic | Payload highlights |
|-------------|------------|--------------------|
| `segment.completed` | `{prefix}/event/segment.completed` | `camera_id`, `file_path`, `format`, `encoding`, `started_at`, `ended_at`, `file_size`, `recording_id` |
| `camera.added` | `{prefix}/event/camera.added` | `camera_id`, `name`, `endpoint`, `source` |
| `camera.quality` | `{prefix}/event/camera.quality` | `camera_id`, `from`, `to`, `reason` |
| `storage.health.changed` | `{prefix}/event/storage.health.changed` | `camera_id`, `previous_state`, `current_state`, `message` |

High-frequency topics (e.g. AI detections) are deliberately excluded from the whitelist. Messages use QoS 1 and retain=false; events are dropped when the broker is slow (the event bus drops on overflow and never blocks recording).

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
```text
WARN: mqtt connection failed: connection refused
```
- **Solution**: Check broker URL, ensure broker is running
- **Debug**: Test with `mosquitto_pub -h 192.168.1.100 -t test -m hello`

**Authentication Failed**
```text
WARN: mqtt authentication failed
```
- **Solution**: Verify username and password in configuration
- **Debug**: Test with credentials: `mosquitto_pub -u user -P pass -h broker -t test -m hello`

**Message Not Processed**
```text
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

```text
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