# 树莓派摄像头接入

> 适用于 MiBeeNvr v0.11.0

MiBee NVR 支持通过 libcamera 直接连接树莓派 CSI 摄像头，也支持网络 RTSP / ONVIF 摄像头。

## 支持的摄像头

| 摄像头 | 状态 | 说明 |
|--------|------|------|
| 树莓派 CSI 摄像头 V2 | ✅ 完整支持 | 800 万像素 |
| 树莓派 CSI 摄像头 V3 | ✅ 完整支持 | 1200 万像素，HDR |
| 树莓派 HQ 摄像头 | ✅ 完整支持 | 1200 万像素 |
| USB 摄像头 | ✅ 基础支持 | UVC 兼容 |

## 本地 CSI 摄像头

### 配置 libcamera

MiBee NVR 使用 libcamera 系统库连接 CSI 摄像头。

```yaml
cameras:
  - id: "rpi-cam"
    name: "树莓派摄像头"
    protocol: "libcamera"
    encoding: "h264"
    device: "0"                     # libcamera 设备索引
    width: 1920                     # 分辨率宽
    height: 1080                    # 分辨率高
    fps: 30                         # 帧率
    enabled: true
```

### 设备配置

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `device` | `"0"` | libcamera 设备索引 |
| `width` | `1920` | 视频宽度 |
| `height` | `1080` | 视频高度 |
| `fps` | `30` | 帧率 |

### 多摄像头

树莓派 4B 支持最多 2 个 CSI 摄像头（使用双摄像头适配板）：

```yaml
cameras:
  - id: "rpi-cam-1"
    name: "树莓派摄像头 1"
    protocol: "libcamera"
    encoding: "h264"
    device: "0"
    width: 1920
    height: 1080
    fps: 30
    enabled: true

  - id: "rpi-cam-2"
    name: "树莓派摄像头 2"
    protocol: "libcamera"
    encoding: "h264"
    device: "1"
    width: 1920
    height: 1080
    fps: 30
    enabled: true
```

## 网络摄像头

树莓派也可以作为 NVR 录制网络摄像头：

### RTSP 摄像头

```yaml
cameras:
  - id: "network-cam"
    name: "网络摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
```

### ONVIF 摄像头

```yaml
cameras:
  - id: "onvif-cam"
    name: "ONVIF 摄像头"
    protocol: "onvif"
    encoding: "h264"
    url: "http://192.168.1.101:80/onvif/device_service"
    username: "admin"
    password: "camera123"
    enabled: true
```

## 树莓派优化

### 内存优化

```yaml
recording:
  segment_duration: "30s"        # 缩短片段时长，降低内存峰值
  max_days: 14                   # 减少保留天数
```

| 配置 | 内存占用 | 说明 |
|------|----------|------|
| 4 个摄像头 / 30s 片段 | ~300MB | RPi 3B |
| 4 个摄像头 / 1m 片段 | ~400MB | RPi 3B |
| 8 个摄像头 / 30s 片段 | ~600MB | RPi 4B |

### CPU 优化

```yaml
cameras:
  - id: "rpi-cam"
    protocol: "libcamera"
    encoding: "h264"
    device: "0"
    width: 1280                   # 降低分辨率
    height: 720
    fps: 15                       # 降低帧率
    enabled: true
```

### 存储优化

- 使用外接 USB SSD 作为录像存储
- 避免使用 SD 卡存储录像（I/O 瓶颈）
- 使用 ext4 文件系统（比 NTFS 性能好）

```bash
# 挂载 USB SSD
sudo mount /dev/sda1 /mnt/storage
sudo chown -R nvr:nvr /mnt/storage
```

## Docker 部署

在树莓派上使用 Docker：

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest
    ports:
      - "9090:9090"
    volumes:
      - /mnt/storage:/data
    devices:
      - /dev/video0:/dev/video0   # CSI 摄像头设备
      - /dev/vchiq:/dev/vchiq     # CSI 驱动
    privileged: true              # 需要访问 CSI 硬件
    restart: unless-stopped
```

## USB 摄像头

连接 UVC 兼容的 USB 摄像头：

```yaml
cameras:
  - id: "usb-cam"
    name: "USB 摄像头"
    protocol: "v4l2"
    encoding: "mjpeg"
    device: "/dev/video0"
    width: 640
    height: 480
    fps: 30
    enabled: true
```

## 常见问题

### CSI 摄像头未检测到

```bash
# 检查摄像头连接
libcamera-hello --list-cameras

# 检查设备文件
ls -la /dev/video*
```

### 内存不足

- 减少摄像头数量
- 降低分辨率和帧率
- 缩短录制片段时长
- 使用 `cgroup` 限制内存

### CPU 过载

- 降低录制分辨率
- 减少同时录制的摄像头数量
- 使用硬件编码（树莓派支持 H.264 硬件编码）

## 下一步

- [录制与回放](recording-playback.md) — 录像管理
- [延时摄影](timelapse.md) — 延时摄影功能
- [WebDAV / FTP 存储](webdav-ftp.md) — 访问录像文件
