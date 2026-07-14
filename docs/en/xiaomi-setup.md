# Xiaomi Camera Integration

MiBee NVR provides comprehensive support for Xiaomi cloud cameras through the CS2 P2P protocol. This integration allows you to connect Xiaomi cameras to your NVR system without requiring direct network access to the cameras themselves, as all communication is handled through Xiaomi's cloud services.

## Overview

- **Protocol**: CS2 P2P (Xiaomi's proprietary cloud protocol)
- **Authentication**: Xiaomi cloud services with token-based auth
- **Supported Models**: CS2-based cameras (see table below)
- **Features**: Live streaming, recording, snapshots, PTZ control
- **Network**: Requires connectivity to Xiaomi cloud services

## Prerequisites

- Xiaomi account with registered cameras
- Cameras bound to your Xiaomi account in Mi Home app
- Network access to Xiaomi cloud services (`openapi.io.mi.com`)
- Working internet connection for NVR system

| Model | Identifier | Protocol | Support Level | Notes |
|-------|------------|----------|---------------|-------|
| **Xiaomi C200** | `chuangmi.camera.046c04` | CS2 P2P | ✅ Full | HD 1080p indoor camera |
| **Xiaomi C300** | `chuangmi.camera.72ac1` | CS2 P2P | ✅ Full | 2K indoor camera |
| **Xiaofang** | `isa.camera.isc5c1` | TUTK | ✅ Full | Pan/tilt dome camera (legacy) |
| **Loock V1** | `lumi.camera.v1` | TUTK | ✅ Full | Smart doorbell camera (legacy) |
| **Loock V2** | `loock.cateye.v02` | CS2 P2P | ✅ Full | Smart doorbell camera |
| **Dafang** | `isa.camera.df3` | TUTK | ✅ Full | Pan/tilt dome camera (legacy) |
| **Aqara G2** | `isa.camera.g2h` | TUTK | ✅ Full | Indoor cube camera (legacy) |
| **IMILAB A1** | `isa.camera.a1` | TUTK | ✅ Full | Indoor camera (legacy) |
| **Xiaobai** | `isa.camera.xf` | TUTK | ✅ Full | Pan/tilt camera (legacy) |
| **Mijia** | `chuangmi.camera.v2` | TUTK | ✅ Full | Basic indoor camera (legacy) |

**Important**: Both CS2 and TUTK protocol cameras are now supported. TUTK models use the legacy protocol via `internal/tutk/`. Two-way audio is available for CS2 models only.
## Configuration

### Basic Configuration

```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  region: "cn"
  auto_discovery: true

cameras:
  - id: "xiaomi_c200_front"
    name: "Xiaomi C200 - Front"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_here"
    vendor: "cs2"
    enabled: true
    # Optional settings
    sub_stream_url: "rtsp://xiaomi-c200-cs2.stream"
    hls_max_fps: 15
    sample_interval: 2
```

### Configuration Options

| Field | Required | Type | Default | Description |
|-------|----------|------|---------|-------------|
| `enabled` | Yes | boolean | false | Enable Xiaomi integration |
| `user_id` | Yes | string | - | Xiaomi user ID |
| `token` | Yes | string | - | Xiaomi passToken |
| `region` | No | string | "cn" | Region code (cn, sg, de, etc.) |
| `auto_discovery` | No | boolean | true | Enable automatic device discovery |

### Camera Configuration Options

| Field | Required | Type | Default | Description |
|-------|----------|------|---------|-------------|
| `id` | Yes | string | - | Unique camera identifier |
| `name` | Yes | string | - | Display name for camera |
| `protocol` | Yes | string | "xiaomi" | Must be "xiaomi" |
| `encoding` | Yes | string | "h264" | Video encoding (h264, h265) |
| `did` | Yes | string | - | Xiaomi device ID |
| `vendor` | Yes | string | "cs2" | Must be "cs2" |
| `enabled` | No | boolean | true | Enable camera recording |

### Advanced Configuration

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
  
  # Performance settings
  max_concurrent_cameras: 10
  segment_buffer_size: "10MB"
  
  # Security settings
  encrypt_credentials: true
  credential_rotation_days: 90

cameras:
  - id: "xiaomi_c300_living_room"
    name: "Living Room Camera"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_12345"
    vendor: "cs2"
    enabled: true
    # Optimized settings for 2K cameras
    hls_max_fps: 20
    sample_interval: 1
    segment_duration: "30s"
    
    # Auto backup settings
    snapshot_interval: "5m"
    snapshot_quality: "high"
    
    # Alert settings
    motion_detection: true
    push_notifications: false
```

## Setup Methods

### Method 1: Web UI Setup (Recommended)

1. **Access Web UI**: Open MiBee NVR Web Interface at `http://localhost:9090`

2. **Navigate to Cameras**: Go to the Cameras page

3. **Xiaomi Discovery**: Expand the "Xiaomi Device Discovery" section

4. **Authenticate**: Enter your Xiaomi account credentials and click "Sign In"

5. **Select Devices**: Browse the discovered devices and select cameras you want to add

6. **Add to NVR**: Click "Add to NVR" for each selected camera

7. **Configure**: Customize settings for each camera (retention, quality, etc.)

8. **Save**: Click "Save Configuration" to apply changes

### Method 2: API Authentication

Use the API to get authentication credentials programmatically:

```bash
# Authenticate with Xiaomi cloud
curl -X POST http://localhost:9090/api/xiaomi/auth \
  -H "Content-Type: application/json" \
  -d '{"username": "your-email@example.com", "password": "your-password"}'

# Response example:
{
  "success": true,
  "user_id": "123456789",
  "pass_token": "your_passToken_here",
  "devices": [
    {
      "did": "device_id_12345",
      "name": "Xiaomi C200",
      "model": "chuangmi.camera.046c04",
      "online": true
    }
  ]
}
```

### Method 3: Manual Configuration

Edit the configuration file directly:

```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  region: "cn"

cameras:
  - id: "xiaomi_c200_front"
    name: "Xiaomi C200 - Front"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_12345"
    vendor: "cs2"
    enabled: true
```

## API Endpoints

### Xiaomi Authentication

**POST** `/api/xiaomi/auth`
- **Body**: `{username: string, password: string}`
- **Response**: User info and device list
- **Description**: Authenticate with Xiaomi cloud and get credentials

```bash
curl -X POST http://localhost:9090/api/xiaomi/auth \
  -H "Content-Type: application/json" \
  -d '{"username": "user@example.com", "password": "password"}'
```

### Device Management

**GET** `/api/xiaomi/devices`
- **Response**: List of all Xiaomi devices
- **Description**: Get all Xiaomi devices associated with the account

```bash
curl -u admin:password http://localhost:9090/api/xiaomi/devices
```

**POST** `/api/xiaomi/sync`
- **Response**: Sync status
- **Description**: Force sync devices from Xiaomi cloud

```bash
curl -X POST -u admin:password http://localhost:9090/api/xiaomi/sync
```

### Camera Control

**GET** `/api/xiaomi/cameras/{camera_id}/status`
- **Response**: Camera status information
- **Description**: Get current camera status

```bash
curl -u admin:password http://localhost:9090/api/xiaomi/cameras/xiaomi_c200_front/status
```

**POST** `/api/xiaomi/cameras/{camera_id}/ptz`
- **Body**: `{action: string, speed: number}`
- **Response**: PTZ control result
- **Description**: Control pan/tilt/zoom functions (for supported models)

```bash
curl -X POST -u admin:password \
  -H "Content-Type: application/json" \
  -d '{"action": "up", "speed": 1}' \
  http://localhost:9090/api/xiaomi/cameras/xiaofang_living_room/ptz
```

### Snapshot Management

**GET** `/api/xiaomi/cameras/{camera_id}/snapshot`
- **Response**: JPEG image data
- **Description**: Take a snapshot from the camera

```bash
curl -u admin:password -o snapshot.jpg \
  http://localhost:9090/api/xiaomi/cameras/xiaomi_c200_front/snapshot
```

## Two-Way Audio

Two-way audio allows you to communicate through supported Xiaomi cameras. This feature is available for CS2 models only (TUTK models are blocked due to a protocol limitation).

### Prerequisites

- Browser with AudioWorklet support (Chrome, Firefox, Edge modern versions)
- Camera with two-way audio capability
- Network latency under 200ms for best experience

### Configuration

Enable two-way audio in your camera configuration:

```yaml
cameras:
  - id: "xiaomi_c200_front"
    name: "Xiaomi C200 - Front"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_here"
    vendor: "cs2"
    enabled: true
    two_way_audio_enabled: true  # Enable two-way audio
```

### Usage

1. **Access Live View**: Navigate to the camera's live view page
2. **Hold to Talk**: Click and hold the microphone button to speak
3. **Release to Listen**: Release the button to hear audio from the camera

### Technical Details

- Audio codec: G.711 μ-law/A-law (8kHz sample rate)
- Protocol: MISS protocol via CS2 P2P
- Latency: ~100-200ms (depends on network)
- Browser API: AudioContext, AudioWorklet for encoding

### Troubleshooting

- No audio: Check browser permissions for microphone access
- Echo: Use headphones instead of speakers
- Latency: Check network connectivity to Xiaomi cloud

## PTZ Control

Pan-tilt-zoom (PTZ) control is available for Xiaomi cameras with motor support. This includes most dome cameras (Xiaofang, Dafang, Xiaobai) and some indoor cameras.

### Supported Actions

- `up`, `down`, `left`, `right` — Directional pan/tilt
- `zoom_in`, `zoom_out` — Zoom control (if supported)
- `stop` — Stop movement

### API Usage

**POST** `/api/xiaomi/cameras/{camera_id}/ptz`
- **Body**: `{action: string, speed: number}`
- **Actions**: "up", "down", "left", "right", "zoom_in", "zoom_out", "stop"
- **Speed**: 1-10 (1 = slowest, 10 = fastest)

```bash
# Move camera up
curl -X POST -u admin:password \
  -H "Content-Type: application/json" \
  -d '{"action": "up", "speed": 5}' \
  http://localhost:9090/api/xiaomi/cameras/xiaofang_living_room/ptz

# Zoom in
curl -X POST -u admin:password \
  -H "Content-Type: application/json" \
  -d '{"action": "zoom_in", "speed": 3}' \
  http://localhost:9090/api/xiaomi/cameras/xiaofang_living_room/ptz

# Stop movement
curl -X POST -u admin:password \
  -H "Content-Type: application/json" \
  -d '{"action": "stop", "speed": 0}' \
  http://localhost:9090/api/xiaomi/cameras/xiaofang_living_room/ptz
```

### Frontend Integration

The web UI provides on-screen PTZ controls for supported cameras. The controls automatically appear when:

- Camera model is recognized as PTZ-capable
- Camera reports motor support via GetDeviceInfo

## Device Info

You can query device information including firmware and hardware versions for Xiaomi cameras.

### API Usage

**GET** `/api/xiaomi/cameras/{camera_id}/device-info`
- **Response**: Device information JSON

```bash
curl -u admin:password http://localhost:9090/api/xiaomi/cameras/xiaomi_c200_front/device-info
```

**Response Example**:

```json
{
  "model": "chuangmi.camera.046c04",
  "name": "Xiaomi C200",
  "firmware_version": "4.2.0_20231201",
  "hardware_version": "2.0",
  "mac_address": "AA:BB:CC:DD:EE:FF",
  "ip_address": "192.168.1.100",
  "online": true,
  "last_seen": "2024-01-15T10:30:00Z"
}
```

## Integration Examples

### Home Assistant Integration

```yaml
# Home Assistant configuration
homeassistant:
  # Xiaomi camera integration
  xiaomi:
    username: !secret xiaomi_username
    password: !secret xiaomi_password
    region: cn

# Camera entity in Home Assistant
camera:
  - platform: mjpeg
    name: "Xiaomi C200 Front"
    mjpeg_url: !secret xiaomi_c200_stream
    authentication: !secret xiaomi_auth

# Automation for motion detection
- id: "xiaomi_motion_alert"
  alias: "Xiaomi Camera Motion Detected"
  trigger:
    - platform: mqtt
      topic: "xiaomi/motion/camera_200/front"
      payload: "ON"
  action:
    - service: notify.mobile_app_iphone
      data:
        title: "Motion Detected"
        message: "Motion detected at front door"
```

### Node-RED Integration

```json
// Node-RED Flow - Xiaomi Camera Motion Detection
[
  {"id": "1", "type": "inject", "z": "flow", "name": "Test Motion", "payload":"ON", "payloadType":"str", "repeat": "", "crontab": "", "once": false, "onceDelay": 0.1, "topic": "", "x": 120, "y": 140},
  {"id": "2", "type": "switch", "z": "flow", "name": "Motion Detected", "property": "payload", "propertyType":"msg", "rules":[{"t":"eq","v":"ON"}],"checkall":"true,"outputs":1,"x": 320, "y": 140},
  {"id": "3", "type": "function", "name": "Build Message", "func": "msg.topic = \"xiaomi/motion/camera_200/front\";\nmsg.payload = {\"action\": \"record\", \"duration\": 60};\nreturn msg;", "outputs": 1, "x": 500, "y": 140},
  {"id": "4", "type": "mqtt out", "z": "flow", "name": "Trigger Recording", "topic": "xiaomi/trigger/+", "qos": "0", "retain": "false", "x": 680, "y": 140}
]
```

### Python Automation Script

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
        """Authenticate with Xiaomi cloud"""
        url = f"{self.base_url}/api/xiaomi/auth"
        data = {
            "username": xiaomi_username,
            "password": xiaomi_password
        }
        
        response = self.session.post(url, json=data)
        response.raise_for_status()
        return response.json()
    
    def get_devices(self):
        """Get all Xiaomi devices"""
        url = f"{self.base_url}/api/xiaomi/devices"
        response = self.session.get(url, auth=self.auth)
        response.raise_for_status()
        return response.json()
    
    def take_snapshot(self, camera_id):
        """Take snapshot from camera"""
        url = f"{self.base_url}/api/xiaomi/cameras/{camera_id}/snapshot"
        response = self.session.get(url, auth=self.auth)
        response.raise_for_status()
        return response.content
    
    def get_camera_status(self, camera_id):
        """Get camera status"""
        url = f"{self.base_url}/api/xiaomi/cameras/{camera_id}/status"
        response = self.session.get(url, auth=self.auth)
        response.raise_for_status()
        return response.json()
    
    def trigger_recording(self, camera_id, duration=60):
        """Trigger recording on camera"""
        url = f"{self.base_url}/api/xiaomi/cameras/{camera_id}/trigger"
        data = {
            "action": "record",
            "duration": duration
        }
        
        response = self.session.post(url, json=data, auth=self.auth)
        response.raise_for_status()
        return response.json()

def main():
    # Configuration
    NVR_URL = "http://localhost:9090"
    NVR_USERNAME = "admin"
    NVR_PASSWORD = "password"
    XIAOMI_USERNAME = "user@example.com"
    XIAOMI_PASSWORD = "xiaomi_password"
    
    # Create client
    client = XiaomiCameraClient(NVR_URL, NVR_USERNAME, NVR_PASSWORD)
    
    try:
        # Authenticate with Xiaomi
        print("Authenticating with Xiaomi...")
        auth_result = client.authenticate(XIAOMI_USERNAME, XIAOMI_PASSWORD)
        print(f"User ID: {auth_result['user_id']}")
        print(f"Devices found: {len(auth_result['devices'])}")
        
        # List devices
        print("\nAvailable devices:")
        for device in auth_result['devices']:
            print(f"- {device['name']} (DID: {device['did']})")
        
        # Monitor a specific camera
        camera_id = "xiaomi_c200_front"  # Replace with your camera ID
        
        # Get camera status
        status = client.get_camera_status(camera_id)
        print(f"\nCamera {camera_id} status:")
        print(json.dumps(status, indent=2))
        
        # Take snapshot
        print(f"\nTaking snapshot from {camera_id}...")
        snapshot_data = client.take_snapshot(camera_id)
        
        # Save snapshot
        timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
        filename = f"xiaomi_snapshot_{timestamp}.jpg"
        with open(filename, 'wb') as f:
            f.write(snapshot_data)
        print(f"Snapshot saved as {filename}")
        
    except Exception as e:
        print(f"Error: {e}")

if __name__ == "__main__":
    main()
```

### Shell Script for Monitoring

```bash
#!/bin/bash
# Xiaomi Camera Monitoring Script

NVR_URL="http://localhost:9090"
NVR_USER="admin"
NVR_PASS="password"
LOG_FILE="/var/log/xiaomi_monitor.log"

log_message() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') - $1" >> "$LOG_FILE"
}

# Check Xiaomi devices status
check_devices() {
    response=$(curl -s -u "$NVR_USER:$NVR_PASS" "$NVR_URL/api/xiaomi/devices" 2>/dev/null)
    
    if [[ $? -eq 0 && $response != *"error"* ]]; then
        online_count=$(echo "$response" | grep -o '"online":true' | wc -l)
        total_count=$(echo "$response" | grep -o '"did"' | wc -l)
        
        log_message "Xiaomi devices status: $online_count/$total_count online"
        
        # Check if any device is offline
        if [[ $online_count -lt $total_count ]]; then
            log_message "WARNING: Some Xiaomi devices are offline"
            # Send notification
            # curl -X POST -d "message: Some Xiaomi devices offline" "your-webhook-url"
        fi
    else
        log_message "ERROR: Failed to get Xiaomi devices status"
    fi
}

# Take periodic snapshots
take_snapshots() {
    cameras=$(curl -s -u "$NVR_USER:$NVR_PASS" "$NVR_URL/api/xiaomi/devices" | grep -o '"did":"[^"]*"' | cut -d'"' -f4)
    
    for camera in $cameras; do
        # Skip if camera ID is empty
        [[ -z "$camera" ]] && continue
        
        # Take snapshot
        response=$(curl -s -u "$NVR_USER:$NVR_PASS" -o "/tmp/snapshot_${camera}.jpg" \
                  "$NVR_URL/api/xiaomi/cameras/${camera}/snapshot" 2>/dev/null)
        
        if [[ $? -eq 0 && -f "/tmp/snapshot_${camera}.jpg" ]]; then
            file_size=$(stat -c%s "/tmp/snapshot_${camera}.jpg")
            log_message "Snapshot taken for $camera: ${file_size} bytes"
            
            # Archive snapshot
            timestamp=$(date '+%Y%m%d_%H%M%S')
            cp "/tmp/snapshot_${camera}.jpg" "/var/backups/xiaomi/snapshot_${camera}_${timestamp}.jpg"
        fi
    done
}

# Main monitoring loop
while true; do
    check_devices
    take_snapshots
    
    # Wait 5 minutes before next check
    sleep 300
done
```

## Security Considerations

### Authentication Security

**Token Storage**:
```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  encrypt_credentials: true  # Enable if available in your version
```

**File Permissions**:
```bash
# Secure configuration file
chmod 600 mibee-nvr.yaml
chown nvr:nvr mibee-nvr.yaml
```

### Network Security

**Firewall Configuration**:
```bash
# Allow access to Xiaomi cloud services
ufw allow to openapi.io.mi.com port 443 proto tcp

# Restrict NVR access
ufw allow from 192.168.1.0/24 to any port 9090 proto tcp
```

### Network Requirements

**Required Access**:
- `api.io.mi.com:443` - Xiaomi cloud API (device list, MISS URL resolution)
- `openapi.io.mi.com:443` - Xiaomi cloud API (alternate endpoint)
- `https://xiaomioa.com` - Xiaomi authentication

**Network Troubleshooting**:
```bash
# Test connectivity to Xiaomi services
curl -v https://openapi.io.mi.com

# Test DNS resolution
nslookup openapi.io.mi.com
```

## Performance Optimization

### Camera-Specific Settings

**For High-Resolution Cameras (C300)**:
```yaml
cameras:
  - id: "xiaomi_c300"
    name: "Xiaomi C300 - Living Room"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_12345"
    vendor: "cs2"
    enabled: true
    hls_max_fps: 20        # Higher frame rate for 2K
    sample_interval: 1      # More frequent sampling
    segment_duration: "30s"  # Standard segment duration
    snapshot_interval: "5m" # Regular snapshots
```

**For Lower Bandwidth Networks**:
```yaml
cameras:
  - id: "xiaomi_c200"
    name: "Xiaomi C200 - Backyard"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_67890"
    vendor: "cs2"
    enabled: true
    hls_max_fps: 10        # Lower frame rate
    sample_interval: 3      # Less frequent sampling
    snapshot_quality: "medium"  # Lower quality snapshots
```

### System-Wide Settings

```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  region: "cn"
  
  # Performance optimizations
  connection_timeout: "20s"    # Faster timeout
  read_timeout: "45s"        # Adjusted for network conditions
  retry_attempts: 2          # Fewer retries
  retry_delay: "5s"          # Faster retry
  
  # Memory management
  max_concurrent_cameras: 5   # Limit concurrent connections
  segment_buffer_size: "5MB" # Smaller buffer for limited RAM
```

## Troubleshooting

### Common Issues

#### "Auth Failed" Error
```
ERROR: xiaomi authentication failed: invalid credentials
```
**Causes**:
- Invalid Xiaomi username/password
- Account requires captcha verification
- Two-factor authentication enabled
- Xiaomi cloud service issues

**Solutions**:
```bash
# Test Xiaomi credentials manually
curl -X POST https://openapi.io.mi.com/login \
  -H "Content-Type: application/json" \
  -d '{"username": "user@example.com", "password": "password"}'

# Check account status in Mi Home app
# Verify no captcha is required
# Try with a fresh Xiaomi account if needed
```

#### "Device Not Found" Error
```
ERROR: xiaomi device not found: device_id_12345
```
**Causes**:
- Camera not bound to Xiaomi account
- Camera offline
- Incorrect device ID (DID)
- Region mismatch

**Solutions**:
```bash
# List available devices
curl -u admin:password http://localhost:9090/api/xiaomi/devices

# Check device online status
curl -u admin:password http://localhost:9090/api/xiaomi/cameras/device_id_12345/status

# Verify camera is online in Mi Home app
# Check network connectivity
# Try refreshing device list
```

#### "Recording Failed" Error
```
ERROR: xiaomi recording failed: device offline
```
**Causes**:
- Network connectivity issues
- Xiaomi cloud service downtime
- Camera battery dead (for wireless models)
- Authentication token expired

**Solutions**:
```bash
# Test network connectivity to Xiaomi services
ping openapi.io.mi.com
curl -v https://api.io.mi.com

# Check token validity
curl -u admin:password http://localhost:9090/api/xiaomi/auth

# Re-authenticate if needed
curl -X POST -H "Content-Type: application/json" \
  -d '{"username": "user@example.com", "password": "new_password"}' \
  http://localhost:9090/api/xiaomi/auth
```

#### Stream Quality Issues
**Symptoms**: Choppy video, high latency, poor resolution

**Solutions**:
```yaml
# Adjust camera settings
cameras:
  - id: "xiaomi_camera"
    hls_max_fps: 15        # Reduce frame rate
    sample_interval: 2      # Increase sampling interval
    segment_duration: "15s" # Shorter segments
```

### Debug Mode

Enable detailed logging for troubleshooting:

```yaml
observability:
  log_level: "debug"

xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  debug: true  # Enable detailed Xiaomi logging
```

**Check Logs**:
```bash
# System logs
journalctl -u mibee-nvr -f | grep xiaomi

# Docker logs
docker logs -f mibee-nvr | grep xiaomi

# Configuration validation
./mibee-nvr -config mibee-nvr.yaml --validate
```

### Performance Monitoring

Monitor Xiaomi camera performance:

```bash
#!/bin/bash
# Xiaomi Performance Monitor

LOG_FILE="/var/log/xiaomi_performance.log"
NVR_URL="http://localhost:9090"
NVR_USER="admin"
NVR_PASS="password"

# Monitor response times
response_time=$(curl -o /dev/null -s -w '%{time_total}' -u "$NVR_USER:$NVR_PASS" "$NVR_URL/api/xiaomi/devices")
echo "$(date) - Xiaomi API response time: ${response_time}s" >> "$LOG_FILE"

# Monitor camera status
status=$(curl -s -u "$NVR_USER:$NVR_PASS" "$NVR_URL/api/xiaomi/devices")
online_count=$(echo "$status" | grep -o '"online":true' | wc -l)
echo "$(date) - Online cameras: $online_count" >> "$LOG_FILE"
```

## Migration and Updates

### Token Rotation

Regularly rotate Xiaomi credentials for security:

```bash
#!/bin/bash
# Token rotation script

NVR_URL="http://localhost:9090"
NVR_USER="admin"
NVR_PASS="password"
NEW_PASSWORD="new_xiaomi_password"

# Get new authentication
curl -X POST -H "Content-Type: application/json" \
  -d "{\"username\": \"user@example.com\", \"password\": \"$NEW_PASSWORD\"}" \
  "$NVR_URL/api/xiaomi/auth" | jq '.user_id, .pass_token'

# Update configuration with new token
# (Manual update required in config file)
```

### Version Compatibility

**Check compatibility**:
```bash
# Check current version
./mibee-nvr --version

# Check for updates
curl -s https://api.github.com/repos/Mi-Bee-Studio/MiBeeNvr/releases/latest | grep 'tag_name'
```

### Backup Configuration

Regular backup of Xiaomi configuration:

```bash
#!/bin/bash
# Xiaomi Configuration Backup

BACKUP_DIR="/var/backups/xiaomi"
DATE=$(date '+%Y%m%d_%H%M%S')
CONFIG_FILE="/var/lib/mibee-nvr/mibee-nvr.yaml"

# Create backup directory
mkdir -p "$BACKUP_DIR"

# Backup configuration with sensitive data removed
grep -v 'token:' "$CONFIG_FILE" > "$BACKUP_DIR/mibee-nvr_config_${DATE}.yaml"

# Backup device list
curl -s -u "admin:password" "http://localhost:9090/api/xiaomi/devices" \
  > "$BACKUP_DIR/xiaomi_devices_${DATE}.json"

echo "Backup completed: $BACKUP_DIR"
```

## Best Practices

1. **Regular Updates**: Keep MiBee NVR updated for latest Xiaomi protocol support
2. **Network Monitoring**: Ensure reliable connectivity to Xiaomi cloud services
3. **Token Management**: Rotate Xiaomi credentials periodically
4. **Backup Strategy**: Regular backups of configuration and device lists
5. **Monitoring**: Set up monitoring for camera availability and performance
6. **Security**: Use strong passwords and restrict network access
7. **Documentation**: Keep documentation updated with camera configurations
8. **Testing**: Regular testing of camera functionality and alerts

### Monitoring Dashboard

Create a simple monitoring dashboard:

```python
#!/usr/bin/env python3
import requests
import json
from datetime import datetime

def get_xiaomi_status():
    """Get comprehensive Xiaomi status"""
    try:
        # Get devices
        devices = requests.get("http://localhost:9090/api/xiaomi/devices", 
                            auth=("admin", "password")).json()
        
        online_count = sum(1 for d in devices if d.get('online', False))
        total_count = len(devices)
        
        # Get recent recordings
        recent = requests.get("http://localhost:9090/api/recordings?limit=10",
                            auth=("admin", "password")).json()
        
        # Create status report
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

Through comprehensive Xiaomi camera integration, MiBee NVR enables seamless cloud-based camera recording with rich automation capabilities, making it easy to integrate Xiaomi cameras into your surveillance and smart home ecosystem.