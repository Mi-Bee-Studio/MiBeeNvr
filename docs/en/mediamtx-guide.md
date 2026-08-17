# MediaMTX Integration Guide

MediaMTX (formerly rtsp-simple-server) is a powerful media server that can serve as an RTSP proxy server for MiBee NVR. It provides RTSP to WebRTC, HLS, and other protocol conversion capabilities, making it particularly suitable for integration with MiBee NVR.

MediaMTX main advantages:
- Supports multiple camera protocols (RTSP, RTMP, HTTP-FLV, WebRTC)
- Provides real-time stream conversion
- Supports load balancing and failover
- Easy to configure and deploy
- Perfect integration with MiBee NVR

## MediaMTX Overview

### What is MediaMTX

MediaMTX (formerly RTSP-simple-server) is an open-source media server focused on:
- RTSP/RTMP/WebRTC client/server functionality
- Real-time stream media processing
- Protocol conversion and forwarding
- Media publishing and subscription

### Key Features

- **Protocol Support**: RTSP, RTMP, WebRTC, HLS, HTTP-FLV
- **Stream Management**: Real-time recording, forwarding, conversion
- **Load Balancing**: Multi-camera stream distribution
- **Authentication**: Basic authentication, custom authentication
- **Monitoring**: Statistics and logging

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

### From Source

```bash
# Install dependencies
sudo apt-get install -y golang git

# Clone repository
git clone https://github.com/bluenviron/mediamtx.git
cd mediamtx

# Build
go build -mod=readonly -o mediamtx ./cmd/mediamtx
```

### Using Docker

```bash
# Pull image
docker pull bluenviron/mediamtx:latest

# Run container
docker run -d --name mediamtx \
  -p 8554:8554 -p 8000:8000 -p 1935:1935 \
  -v /path/to/config.yml:/etc/mediamtx.yml \
  bluenviron/mediamtx:latest
```

## Basic Configuration

Create `/etc/mediamtx/mediamtx.yml`:

```yaml
# MediaMTX Basic Configuration

# RTSP Configuration
paths:
  all:
    # Enable recording (optional)
    record: true
    # Recording path
    recordPath: /path/to/recordings
    # Recording format
    recordFormat: fmp4
    # Max recording duration
    recordMaxDuration: 1h

# Server Configuration
protocols:
  # RTSP Protocol
  - protocol: rtsp
    listen: :8554
    # RTSP-over-TCP
    rtspOverTcp: true
    # Authentication
    authMethod: static
    authUsers:
      user: password

  # RTMP Protocol (optional)
  - protocol: rtmp
    listen: :1935

  # WebRTC Protocol (optional)
  - protocol: webrtc
    listen: :8889
    # WebRTC Server Key
    webrtcServerKey: mediamtx

# Logging Configuration
logging:
  level: info
  format: json
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
    protocol: "rtsp"
    encoding: "h264"
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
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://localhost:8554/front_door"
    enabled: true
  - id: "backyard"
    name: "Backyard"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://localhost:8554/backyard"
    enabled: true
  - id: "garage"
    name: "Garage"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://localhost:8554/garage"
    enabled: true
  - id: "csi-cam"
    name: "CSI Camera"
    protocol: "rtsp"
    encoding: "h264"
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

## Troubleshooting

### Connection Issues

#### Camera Cannot Connect to MediaMTX

```bash
# Test RTSP connection
ffmpeg -rtsp_transport tcp -i "rtsp://admin:password123@localhost:8554/cam1" -t 5 -f null -

# Check MediaMTX logs
mediamtx --config mediamtx.yml --log-level debug

# Check port usage
netstat -tlnp | grep :8554
```

#### MediaMTX Cannot Connect to Camera

```bash
# Direct camera test
ffmpeg -rtsp_transport tcp -i "rtsp://192.168.1.100:554/stream" -t 5 -f null -

# Check camera network connectivity
ping 192.168.1.100
nmap -p 554 192.168.1.100
```

### Performance Issues

#### High CPU Usage

```yaml
# Optimization configuration
paths:
  all:
    videoBitrate: 1000        # Reduce bitrate
    fps: 15                   # Reduce frame rate
    gopSize: 30              # Increase GOP size
```

#### High Memory Usage

```yaml
# Memory optimization
paths:
  all:
    recordMaxDuration: 10m   # Reduce recording duration
    bufferType: ring         # Use ring buffer
    bufferTimeMs: 1000       # Reduce buffer time
```

### Authentication Issues

#### Authentication Failure

```yaml
# Check authentication configuration
paths:
  all:
    authMethod: static
    authUsers:
      admin: password123      # Ensure correct password

# Test authenticated access
ffmpeg -rtsp_transport tcp -i "rtsp://admin:password123@localhost:8554/cam1" -t 5 -f null -
```

### Recording Issues

#### Recording Failure

```bash
# Check storage permissions
ls -la /mnt/data/nvr/recordings/
sudo chown -R mibee:mibee /mnt/data/nvr/recordings/

# Check disk space
df -h /mnt/data/nvr

# Check MediaMTX recording logs
mediamtx --config mediamtx.yml --log-level debug
```

## Monitoring and Logging

### Log Configuration

```yaml
# mediamtx.yml - Log Configuration

logging:
  level: info
  format: json
  files:
    - path: /var/log/mediamtx.log
      maxSize: 100MB
      maxBackups: 5
      compress: true
```

### Statistics

```bash
# Check MediaMTX statistics
curl http://localhost:8889/stats

# Check MiBee NVR statistics
curl http://localhost:9090/api/stats
```

### Prometheus Monitoring

```yaml
# mediamtx.yml - Prometheus Configuration

metrics:
  enabled: true
  address: :9998
  path: /metrics
```

## Performance Optimization

### Network Optimization

```yaml
# Network optimization configuration
protocols:
  - protocol: rtsp
    listen: :8554
    rtspOverTcp: true        # Use TCP for stability
    rtspReadTimeout: 10s     # Read timeout
    rtspWriteTimeout: 10s    # Write timeout
```

### Encoding Optimization

```yaml
# Encoding optimization configuration
paths:
  all:
    videoCodec: h264         # Use H.264
    videoBitrate: 2000       # 2 Mbps
    audioCodec: aac          # Use AAC
    audioBitrate: 128        # 128 kbps
    fps: 25                  # 25 FPS
    gopSize: 50              # 2 second GOP
```

### Buffer Optimization

```yaml
# Buffer optimization configuration
paths:
  all:
    bufferType: ring         # Ring buffer
    bufferTimeMs: 2000       # 2 second buffer
    ringSize: 1000           # Buffer size
```

## Best Practices

### Deployment Architecture

```
camera → MediaMTX (load balancing) → MiBee NVR
         ↓
      monitoring node
         ↓
      storage cluster
```

### Configuration Management

```bash
# Use configuration management tools
ansible-playbook -i inventory mediamtx.yml
```

### Backup Strategy

```bash
# Regular configuration backup
*/5 * * * * cp /etc/mediamtx.yml /backup/mediamtx-$(date +\%Y\%m\%d).yml
```

### Update Strategy

```bash
# Rolling update script
cat > update-mediamtx.sh << 'EOF'
#!/bin/bash
# Stop service
sudo systemctl stop mediamtx

# Backup configuration
sudo cp /etc/mediamtx.yml /etc/mediamtx.yml.bak

# Update binary
sudo wget -O /usr/local/bin/mediamtx https://github.com/bluenviron/mediamtx/releases/latest/download/mediamtx-linux-amd64
sudo chmod +x /usr/local/bin/mediamtx

# Start service
sudo systemctl start mediamtx
EOF

chmod +x update-mediamtx.sh
```

## Summary

MediaMTX provides powerful media processing capabilities for MiBee NVR, enabling:

- More stable camera connections
- More protocol support
- Better performance
- More flexible deployment options

It's recommended to choose appropriate configuration based on actual needs and regularly monitor system status.
