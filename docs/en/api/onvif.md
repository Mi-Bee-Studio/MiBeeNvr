# ONVIF API

## ONVIF Camera Control

### Get ONVIF Profiles

**Endpoint:** `GET /api/cameras/{id}/onvif/profiles`

Get available media profiles for an ONVIF camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/onvif/profiles"
```

**Response:**
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

### Get ONVIF Capabilities

**Endpoint:** `GET /api/cameras/{id}/onvif/capabilities`

Get detailed device capabilities for an ONVIF camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/onvif/capabilities"
```

**Response:**
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

### Move PTZ

**Endpoint:** `POST /api/cameras/{id}/ptz/move`

Move PTZ camera with absolute/relative positioning.

**Request Body:**
```json
{
  "mode": "absolute",
  "pan": 0.5,
  "tilt": 0.3,
  "zoom": 1.0,
  "speed": 1.0
}
```

**Request:**
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

**Response:**
```json
{
  "status": "moving"
}
```

### Stop PTZ

**Endpoint:** `POST /api/cameras/{id}/ptz/stop`

Stop PTZ movement.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/lobby/ptz/stop"
```

**Response:**
```json
{
  "status": "stopped"
}
```

### Get PTZ Status

**Endpoint:** `GET /api/cameras/{id}/ptz/status`

Get current PTZ position and movement status.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/ptz/status"
```

**Response:**
```json
{
  "pan": 0.5,
  "tilt": 0.3,
  "zoom": 1.0,
  "moving": false
}
```

### PTZ Presets

**Endpoint:** `GET /api/cameras/{id}/ptz/presets`

Get saved PTZ presets for an ONVIF camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/ptz/presets"
```

**Response:**
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

### Create PTZ Preset

**Endpoint:** `POST /api/cameras/{id}/ptz/presets`

Create a new PTZ preset.

**Request Body:**
```json
{
  "name": "Home Position"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Home Position"
  }' \
  "http://localhost:9090/api/cameras/lobby/ptz/presets"
```

**Response:**
```json
{
  "token": "preset_123"
}
```

### Go to PTZ Preset

**Endpoint:** `POST /api/cameras/{id}/ptz/presets/{token}/goto`

Move camera to a saved PTZ preset.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/lobby/ptz/presets/preset_123/goto"
```

**Response:**
```json
{
  "status": "ok"
}
```

### Delete PTZ Preset

**Endpoint:** `DELETE /api/cameras/{id}/ptz/presets/{token}`

Delete a PTZ preset.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/lobby/ptz/presets/preset_123"
```

**Response:**
```json
{
  "status": "ok"
}
```

### Snapshot URI

**Endpoint:** `GET /api/cameras/{id}/snapshot/uri`

Get the snapshot URI for an ONVIF camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/snapshot/uri"
```

**Response:**
```json
{
  "uri": "http://192.168.1.100:8080/snapshot.jpg"
}
```

## ONVIF Camera Management

### Imaging Settings

**Endpoint:** `GET /api/cameras/{id}/imaging/settings`

Get current imaging settings for an ONVIF camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/imaging/settings"
```

**Response:**
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

### Set Imaging Settings

**Endpoint:** `PUT /api/cameras/{id}/imaging/settings`

Update imaging parameters for an ONVIF camera.

**Request Body:**
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

**Request:**
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

**Response:**
```json
{
  "status": "ok"
}
```

### Get Imaging Options

**Endpoint:** `GET /api/cameras/{id}/imaging/options`

Get supported imaging parameter ranges for an ONVIF camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/imaging/options"
```

**Response:**
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

### Reboot Camera

**Endpoint:** `POST /api/cameras/{id}/onvif/reboot`

Reboot an ONVIF camera.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/lobby/onvif/reboot"
```

**Response:**
```json
{
  "status": "ok"
}
```

**Note:** Some cameras may not support this operation and will return `501 Not Implemented`.

### Network Configuration (GET)

**Endpoint:** `GET /api/cameras/{id}/onvif/network`

Get network interface configuration from an ONVIF camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/onvif/network"
```

**Response:**
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

### Network Configuration (PUT)

**Endpoint:** `PUT /api/cameras/{id}/onvif/network`

Configure network interfaces on an ONVIF camera.

**Request Body:**
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

**Request:**
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

**Response:**
```json
{
  "status": "ok"
}
```

**Note:** Camera restart may be required for network changes to take effect. Some cameras may not support this operation.

### Users (GET)

**Endpoint:** `GET /api/cameras/{id}/onvif/users`

Get users configured on an ONVIF camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/onvif/users"
```

**Response:**
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

### Users (POST)

**Endpoint:** `POST /api/cameras/{id}/onvif/users`

Create a new user on an ONVIF camera.

**Request Body:**
```json
{
  "username": "operator",
  "password": "securepass",
  "user_level": "Operator"
}
```

**Request:**
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

**Response:**
```json
{
  "status": "ok"
}
```

### Users (DELETE)

**Endpoint:** `DELETE /api/cameras/{id}/onvif/users`

Delete a user from an ONVIF camera.

**Request Body:**
```json
{
  "username": "operator"
}
```

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  -H "Content-Type: application/json" \
  -d '{
    "username": "operator"
  }' \
  "http://localhost:9090/api/cameras/lobby/onvif/users"
```

**Response:**
```json
{
  "status": "ok"
}
```

### Users (PUT)

**Endpoint:** `PUT /api/cameras/{id}/onvif/users/{username}`

Update a specific user on an ONVIF camera.

**Request Body:**
```json
{
  "password": "newpassword",
  "user_level": "Operator"
}
```

**Request:**
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

**Response:**
```json
{
  "status": "ok"
}
```

## ONVIF Discovery

### Discover ONVIF Devices

**Endpoint:** `POST /api/onvif/discover`

Discover ONVIF devices on the network using WS-Discovery.

**Request Body:**
```json
{
  "timeout": 5,
  "target": "192.168.1.0/24"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "timeout": 5
  }' \
  "http://localhost:9090/api/onvif/discover"
```

**Response:**
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

### Get ONVIF Device Detail

**Endpoint:** `GET /api/onvif/discover/{ip}`

Get detailed information about a specific ONVIF device.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/onvif/discover/192.168.1.104"
```

**Response:**
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

### Probe ONVIF Device

**Endpoint:** `POST /api/onvif/probe`

Probe a single ONVIF device by sending a WS-Discovery probe via HTTP POST directly to the specified host and port (no multicast needed).

**Request Body:**
```json
{
  "host": "192.168.1.104",
  "port": 80,
  "timeout": 5
}
```

**Request:**
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

**Response:**
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
