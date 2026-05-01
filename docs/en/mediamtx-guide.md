# MediaMTX Integration Guide

MediaMTX (formerly rtsp-simple-server) is a zero-dependency real-time media server that acts as a "media router" between publishers and readers. MiBee NVR uses it to ingest camera streams — especially for CSI cameras that need a streaming pipeline.

**Official docs**: <https://mediamtx.org>  
**GitHub**: <https://github.com/bluenviron/mediamtx>  
**Recommended version**: v1.18.0+

## Installation

### Binary (Recommended)

```bash
# Download for ARM64
wget https://github.com/bluenviron/mediamtx/releases/download/v1.18.0/mediamtx_v1.18.0_linux_arm64.tar.gz
tar -xzf mediamtx_v1.18.0_linux_arm64.tar.gz

# Install
sudo cp mediamtx /usr/local/bin/
sudo chmod +x /usr/local/bin/mediamtx
sudo mkdir -p /etc/mediamtx
```

### Verify

```bash
mediamtx --version
```

## Basic Configuration

Create `/etc/mediamtx/mediamtx.yml`:

```yaml
# Global
logLevel: info
logDestinations: [stdout]

# RTSP server
rtsp: yes
rtspAddress: :8554
rtspTransports: [udp, multicast, tcp]

# Paths (camera streams)
paths:
  cam_front:
    source: rtsp://admin:password@192.168.1.10:554/stream1
    rtspTransport: tcp
    sourceOnDemand: yes
```

Start:

```bash
mediamtx /etc/mediamtx/mediamtx.yml
```

The stream is now available at `rtsp://localhost:8554/cam_front`.

## Integrating with MiBee NVR

Configure MiBee NVR to pull from MediaMTX:

```yaml
# mibee-nvr.yaml
cameras:
  - id: "front-door"
    name: "Front Door"
    protocol: "rtsp_h264"
    url: "rtsp://localhost:8554/cam_front"
    enabled: true
```

MiBee NVR will connect to MediaMTX's RTSP port and record the stream.

## CSI Camera Pipeline

For devices with a CSI camera (e.g., Raspberry Pi Camera Module), you need a pipeline to convert the raw H.264 output into an RTSP stream via MediaMTX.

### Option 1: UDP Pipeline (rpicam-vid + ffmpeg)

```bash
# Capture H.264 from CSI camera → ffmpeg → UDP
rpicam-vid -n --codec h264 --width 1280 --height 720 --framerate 15 -t 0 -o - \
  | ffmpeg -fflags +genpts -i pipe:0 -c:v copy -f mpegts -flush_packets 0 \
    'udp://127.0.0.1:8555?pkt_size=188'
```

MediaMTX config to receive the UDP stream:

```yaml
paths:
  rpi_cam:
    source: udp://127.0.0.1:8555
```

> **Note**: On Debian Bookworm and later, `libcamera-*` commands have been renamed to `rpicam-*`.

### Option 2: Built-in rpiCamera Source (MediaMTX v1.14+)

MediaMTX can directly access the Raspberry Pi camera without ffmpeg:

```yaml
paths:
  rpi_cam:
    source: rpiCamera
    rpiCameraCamID: 0
    rpiCameraWidth: 1280
    rpiCameraHeight: 720
    rpiCameraFPS: 15
```

This is simpler but only works on devices with a compatible CSI camera.

### Systemd Service

Create `/etc/systemd/system/rpicam-stream.service`:

```ini
[Unit]
Description=CSI Camera Streaming Pipeline
Wants=mediamtx.service
After=mediamtx.service

[Service]
Type=simple
User=nvr
ExecStart=/bin/bash -c 'rpicam-vid -n --codec h264 --width 1280 --height 720 --framerate 15 -t 0 -o - | ffmpeg -fflags +genpts -i pipe:0 -c:v copy -f mpegts -flush_packets 0 "udp://127.0.0.1:8555?pkt_size=188"'
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## Multi-Camera Configuration

```yaml
paths:
  front_door:
    source: rtsp://admin:pass@192.168.1.10:554/stream1
    rtspTransport: tcp
    sourceOnDemand: yes

  backyard:
    source: rtsp://admin:pass@192.168.1.11:554/stream1
    rtspTransport: tcp
    sourceOnDemand: yes

  garage:
    source: rtsp://admin:pass@192.168.1.12:554/stream1
    rtspTransport: tcp
    sourceOnDemand: yes

  rpi_csi:
    source: udp://127.0.0.1:8555
```

Then in MiBee NVR config:

```yaml
cameras:
  - id: "front"
    name: "Front Door"
    protocol: "rtsp_h264"
    url: "rtsp://localhost:8554/front_door"
    enabled: true
  - id: "backyard"
    name: "Backyard"
    protocol: "rtsp_h264"
    url: "rtsp://localhost:8554/backyard"
    enabled: true
  - id: "garage"
    name: "Garage"
    protocol: "rtsp_h264"
    url: "rtsp://localhost:8554/garage"
    enabled: true
  - id: "csi-cam"
    name: "CSI Camera"
    protocol: "rtsp_h264"
    url: "rtsp://localhost:8554/rpi_csi"
    enabled: true
```

## Authentication

### Internal Users

```yaml
authMethod: internal
authInternalUsers:
  # Admin (full access)
  - user: admin
    pass: secret123
    permissions:
      - action: publish
      - action: read
      - action: api

  # Read-only (for NVR)
  - user: nvr
    pass: nvrcode
    ips: ["127.0.0.1"]  # Restrict to localhost
    permissions:
      - action: read
```

### Hashed Passwords

```bash
# Generate SHA256 hash
echo -n "mypassword" | openssl dgst -binary -sha256 | openssl base64
```

```yaml
authInternalUsers:
  - user: sha256:<hash_value>
    pass: sha256:<hash_value>
    permissions:
      - action: read
```

## Logging and Debugging

### Log Levels

```yaml
logLevel: info  # error, warn, info, debug
```

### Log to File

```yaml
logDestinations: [file]
logFile: /var/log/mediamtx/mediamtx.log
```

### Structured Logging (JSONL)

```yaml
logStructured: true
```

### Debugging Packet Issues

```yaml
logLevel: debug
dumpPackets: true  # Dump packets to disk for analysis
```

## Common Issues

### "invalid FU-A packet (non-starting)"

This error appears when using the UDP pipeline (rpicam-vid → ffmpeg → UDP). It is **non-critical** and does not affect recording. You can safely ignore it.

### Stream Not Available

1. Check MediaMTX is running: `systemctl status mediamtx`
2. Check logs: `journalctl -u mediamtx -f`
3. Verify camera is accessible: `ffplay rtsp://camera-ip:554/stream`
4. Test MediaMTX path: `ffplay rtsp://localhost:8554/cam_name`

### High Memory Usage

- Use `sourceOnDemand: yes` to only pull streams when readers connect
- Reduce `writeQueueSize: 256` for memory-constrained devices
- Use TCP transport instead of UDP for reliability

### TCP vs UDP Transport

```yaml
# More reliable (works through NAT)
rtspTransport: tcp

# Lower latency (default)
rtspTransport: udp
```

For home networks, TCP is recommended for reliability.
