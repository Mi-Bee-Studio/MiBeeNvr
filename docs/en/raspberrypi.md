# Raspberry Pi Camera Integration

> For MiBeeNvr v0.12.0

MiBee NVR supports Raspberry Pi CSI cameras directly via libcamera, as well as network RTSP / ONVIF cameras.

## Supported Cameras

| Camera | Status | Notes |
|--------|--------|-------|
| Raspberry Pi CSI Camera V2 | ✅ Full support | 8 MP |
| Raspberry Pi CSI Camera V3 | ✅ Full support | 12 MP, HDR |
| Raspberry Pi HQ Camera | ✅ Full support | 12 MP |
| USB camera | ✅ Basic support | UVC compatible |

## Local CSI Camera

### Configure libcamera

MiBee NVR uses the libcamera system library to connect to CSI cameras.

```yaml
cameras:
  - id: "rpi-cam"
    name: "Raspberry Pi Camera"
    protocol: "libcamera"
    encoding: "h264"
    device: "0"                     # libcamera device index
    width: 1920                     # Video width
    height: 1080                    # Video height
    fps: 30                         # Frame rate
    enabled: true
```

### Device Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `device` | `"0"` | libcamera device index |
| `width` | `1920` | Video width |
| `height` | `1080` | Video height |
| `fps` | `30` | Frame rate |

### Multiple Cameras

The Raspberry Pi 4B supports up to 2 CSI cameras (with a dual-camera adapter board):

```yaml
cameras:
  - id: "rpi-cam-1"
    name: "Raspberry Pi Camera 1"
    protocol: "libcamera"
    encoding: "h264"
    device: "0"
    width: 1920
    height: 1080
    fps: 30
    enabled: true

  - id: "rpi-cam-2"
    name: "Raspberry Pi Camera 2"
    protocol: "libcamera"
    encoding: "h264"
    device: "1"
    width: 1920
    height: 1080
    fps: 30
    enabled: true
```

## Network Cameras

A Raspberry Pi can also serve as an NVR for network cameras:

### RTSP Camera

```yaml
cameras:
  - id: "network-cam"
    name: "Network Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
```

### ONVIF Camera

```yaml
cameras:
  - id: "onvif-cam"
    name: "ONVIF Camera"
    protocol: "onvif"
    encoding: "h264"
    url: "http://192.168.1.101:80/onvif/device_service"
    username: "admin"
    password: "camera123"
    enabled: true
```

## Raspberry Pi Optimization

### Memory Optimization

```yaml
recording:
  segment_duration: "30s"        # Shorter segments reduce peak memory usage
  max_days: 14                   # Fewer retention days
```

| Configuration | Memory Usage | Notes |
|---------------|-------------|-------|
| 4 cameras / 30s segments | ~300MB | RPi 3B |
| 4 cameras / 1m segments | ~400MB | RPi 3B |
| 8 cameras / 30s segments | ~600MB | RPi 4B |

### CPU Optimization

```yaml
cameras:
  - id: "rpi-cam"
    protocol: "libcamera"
    encoding: "h264"
    device: "0"
    width: 1280                   # Lower resolution
    height: 720
    fps: 15                       # Lower frame rate
    enabled: true
```

### Storage Optimization

- Use an external USB SSD for recording storage
- Avoid using the SD card for recordings (I/O bottleneck)
- Use ext4 filesystem (better performance than NTFS)

```bash
# Mount a USB SSD
sudo mount /dev/sda1 /mnt/storage
sudo chown -R nvr:nvr /mnt/storage
```

## Docker Deployment

Run on a Raspberry Pi with Docker:

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest
    ports:
      - "9090:9090"
    volumes:
      - /mnt/storage:/data
    devices:
      - /dev/video0:/dev/video0   # CSI camera device
      - /dev/vchiq:/dev/vchiq     # CSI driver
    privileged: true              # Required for CSI hardware access
    restart: unless-stopped
```

## USB Camera

Connect a UVC-compatible USB camera:

```yaml
cameras:
  - id: "usb-cam"
    name: "USB Camera"
    protocol: "v4l2"
    encoding: "mjpeg"
    device: "/dev/video0"
    width: 640
    height: 480
    fps: 30
    enabled: true
```

## Troubleshooting

### CSI Camera Not Detected

```bash
# Check camera connection
libcamera-hello --list-cameras

# Check device files
ls -la /dev/video*
```

### Insufficient Memory

- Reduce the number of cameras
- Lower resolution and frame rate
- Shorten recording segment duration
- Use `cgroup` to limit memory usage

### CPU Overload

- Lower the recording resolution
- Reduce the number of simultaneously recording cameras
- Use hardware encoding (Raspberry Pi supports H.264 hardware encoding)

## Next Steps

- [Recording & Playback](recording-playback.md) — Recording management
- [Timelapse](timelapse.md) — Timelapse recording feature
- [WebDAV / FTP Storage](webdav-ftp.md) — Accessing recording files
