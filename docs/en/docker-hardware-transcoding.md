# Docker Hardware Transcoding Guide

## Overview

Hardware passthrough is essential for optimal transcoding performance in Docker containers. MiBee NVR supports multiple hardware acceleration paths: VAAPI for Intel/AMD GPUs, NVENC for NVIDIA GPUs, and V4L2 M2M for Raspberry Pi. This guide explains how to configure Docker to expose hardware devices to the container for maximum transcoding efficiency.

> **⚠️ NAS / host-network deployments, read this first**: the compose examples
> in this guide use generic bridge networking for standalone Docker hosts. NAS
> deployments (Synology/QNAP/fnOS/Zspace etc.) **must use `network_mode: host`**:
> ONVIF camera auto-discovery relies on UDP multicast (`239.255.255.250:3702`),
> which Docker bridge networking blocks — and under host mode `ports:` mappings
> are ignored (Synology DSM even rejects a compose that declares both host and
> ports). Copying these examples onto a NAS breaks auto-discovery or fails
> outright — use the [NAS / host-network template](#nas--host-network-hardware-transcoding)
> below instead.

> **RK3588 note**: the transcoding backend supports software / V4L2 M2M /
> VAAPI / NVENC only — **not RKMPP (Rockchip NPU)**. The RK3588 NPU cannot be
> used; transcoding falls back to software encoding.

## Raspberry Pi V4L2 M2M

V4L2 Memory-to-Memory (M2M) acceleration provides hardware-accelerated H.264/H.265 encoding on Raspberry Pi devices, significantly improving performance compared to software encoding.

### Device Mapping

Raspberry Pi cameras or USB capture cards typically appear as `/dev/video10`, `/dev/video11`, `/dev/video12` in the host system. Your device numbers may vary.

#### Check Available Devices

```bash
# List video devices on the host
ls -la /dev/video*

# Check device capabilities
v4l2-ctl -d /dev/video10 --list-formats-ext
```

#### Docker Compose Configuration

Add device mapping to your `docker-compose.yml`:

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    container_name: mibee-nvr
    restart: unless-stopped
    
    ports:
      - "9090:9090"   # Web UI / API
      - "2121:2121"   # FTP
      - "2122-2140:2122-2140"  # FTP passive ports
    
    # Map video devices for hardware acceleration
    devices:
      - /dev/video10:/dev/video10
      - /dev/video11:/dev/video11
      - /dev/video12:/dev/video12
    
    volumes:
      - ./data:/data
    environment:
      - NVR_DATA_DIR=/data
    healthcheck:
      test: ["CMD", "mibee-nvr", "health"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

### Verification

Check if devices are accessible inside the container:

```bash
# Enter container shell (if available)
docker exec -it mibee-nvr sh

# List video devices inside container
ls -la /dev/video*

# Test V4L2 capabilities
v4l2-ctl -d /dev/video10 --list-formats-ext
```

### Known Issues

- **Kernel 6.6.63+**: There's a V4L2 M2M bug in newer kernels that may cause encoding failures. Consider downgrading to kernel 6.6.62 or earlier if you encounter issues.
- **Device Permissions**: Ensure your user has access to video devices. Add yourself to the `video` group: `sudo usermod -a -G video $USER`
- **Device Conflicts**: Avoid using the same video device for both Docker host and other applications simultaneously.

## Intel/AMD VAAPI

Video Acceleration API (VAAPI) provides hardware acceleration for Intel and AMD GPUs, enabling efficient H.264/H.265 transcoding.

### Device Mapping

VAAPI requires access to the entire `/dev/dri` device group which includes render and card devices.

#### Check Available Devices

```bash
# List DRI devices
ls -la /dev/dri/*

# Check GPU capabilities
 vainfo
```

#### Docker Compose Configuration

Add DRI device mapping:

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    container_name: mibee-nvr
    restart: unless-stopped
    
    ports:
      - "9090:9090"   # Web UI / API
      - "2121:2121"   # FTP
      - "2122-2140:2122-2140"  # FTP passive ports
    
    # Map DRI devices for VAAPI acceleration
    devices:
      - /dev/dri/renderD128:/dev/dri/renderD128
      - /dev/dri/card0:/dev/dri/card0
    
    volumes:
      - ./data:/data
    environment:
      - NVR_DATA_DIR=/data
      - LIBVA_DRIVER_NAME=i965  # For Intel GPUs
      # - LIBVA_DRIVER_NAME=radeonsi  # For AMD GPUs
    healthcheck:
      test: ["CMD", "mibee-nvr", "health"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

### Verification

Test VAAPI functionality inside the container:

```bash
# Enter container
docker exec -it mibee-nvr sh

# Check VAAPI info
vainfo
```

You should see output listing available VAAPI drivers and formats if hardware acceleration is working correctly.

## NVIDIA

NVIDIA GPUs provide NVENC hardware acceleration for H.264/H.265 encoding with excellent performance and quality.

### Prerequisites

Install the NVIDIA Container Toolkit:

```bash
# Add the NVIDIA Container Toolkit repository
distribution=$(. /etc/os-release;echo $ID$VERSION_ID)
curl -s -L https://nvidia.github.io/nvidia-docker/gpgkey | sudo apt-key add -
curl -s -L https://nvidia.github.io/nvidia-docker/$distribution/nvidia-docker.list | sudo tee /etc/apt/sources.list.d/nvidia-docker.list

# Install the toolkit
sudo apt-get update
sudo apt-get install -y nvidia-container-toolkit

# Restart Docker
sudo systemctl restart docker
```

#### Docker Compose Configuration

Use NVIDIA runtime for GPU access:

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    container_name: mibee-nvr
    restart: unless-stopped
    
    ports:
      - "9090:9090"   # Web UI / API
      - "2121:2121"   # FTP
      - "2122-2140:2122-2140"  # FTP passive ports
    
    # Use NVIDIA runtime for GPU access
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
    
    volumes:
      - ./data:/data
    environment:
      - NVR_DATA_DIR=/data
    healthcheck:
      test: ["CMD", "mibee-nvr", "health"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

#### Alternative Device Mapping

If you prefer device mapping over the deploy configuration:

```yaml
services:
  mibee-nvr:
    # ... other configuration ...
    
    runtime: nvidia
    devices:
      - /dev/nvidia0:/dev/nvidia0
      - /dev/nvidia1:/dev/nvidia1
      - /dev/nvidiactl:/dev/nvidiactl
      - /dev/nvidia-uvm:/dev/nvidia-uvm
      - /dev/nvidia-uvm-tools:/dev/nvidia-uvm-tools
    
    # ... rest of configuration ...
```

### Verification

Check if the GPU is accessible:

```bash
# Check GPU access in container
docker exec mibee-nvr nvidia-smi

# Test NVENC capability
docker exec mibee-nvr ffmpeg -encoders | grep nvenc
```

## Software-only Fallback

When hardware acceleration is not available or desired, MiBee NVR falls back to software encoding using libx264/libx265.

### Configuration

No device mapping is required:

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    container_name: mibee-nvr
    restart: unless-stopped
    
    ports:
      - "9090:9090"   # Web UI / API
      - "2121:2121"   # FTP
      - "2122-2140:2122-2140"  # FTP passive ports
    
    volumes:
      - ./data:/data
    environment:
      - NVR_DATA_DIR=/data
    healthcheck:
      test: ["CMD", "mibee-nvr", "health"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

### Performance Expectations

| Hardware | Encoding | Performance Notes |
|----------|----------|------------------|
| Raspberry Pi 3B | Software H.264 | ~1-2 FPS per stream, CPU intensive |
| Raspberry Pi 4 | Software H.264 | ~3-5 FPS per stream, moderate CPU usage |
| Intel NUC | Software H.264 | ~15-30 FPS per stream, moderate CPU usage |
| Desktop CPU | Software H.264 | ~30-60 FPS per stream, CPU intensive |

### When to Use Software vs Hardware

**Use hardware acceleration when:**
- You need higher frame rates (>5 FPS)
- CPU resources are limited
- You have many concurrent streams
- Power efficiency is important

**Use software encoding when:**
- Hardware devices are not available
- You need maximum compatibility
- Quality settings need fine-tuning
- Development/testing without special hardware

## FFmpeg Inside Docker

MiBee NVR's Docker image **bundles FFmpeg** (and `ffprobe`) via the Alpine `ffmpeg` package.

> **Note**: FFmpeg is an **optional dependency**. All core NVR features (recording,
> playback, live streaming, relay, timelapse, merge) are pure Go and **do not need
> FFmpeg**. The Docker image bundles it only for out-of-the-box transcoding.
> Transcoding and live transcode work out of the box — no manual download required.

### Verify Bundled FFmpeg

```bash
# FFmpeg is on PATH at /usr/bin/ffmpeg
docker exec mibee-nvr ffmpeg -version

# List available encoders (software libx264/libx265 are always present)
docker exec mibee-nvr ffmpeg -encoders | grep -E "(h264|h265|nvenc|vaapi|v4l2)"
```

### Resolution Order

The NVR resolves the FFmpeg binary in this order (see `internal/transcoding/downloader.go:GetFFmpegStatus`):

1. **`exec.LookPath("ffmpeg")`** — finds the bundled `/usr/bin/ffmpeg` first.
2. **`{data_dir}/tools/ffmpeg`** — a user-supplied custom build (only used if the bundled one is absent).
3. **In-app downloader** — fetches a static build from johnvansickle.com into `{data_dir}/tools/` (requires the `xz` tool, which is also bundled; serves as an upgrade/fallback path).

Because the bundled FFmpeg wins on PATH, the downloader is effectively a no-op unless you explicitly remove Alpine's FFmpeg or want a newer/different build.

### Custom FFmpeg Build

To override the bundled FFmpeg with a custom build (e.g. a newer version with extra codecs), remove the bundled one and place your binary in the tools directory:

```bash
# In a derived Dockerfile:
#   RUN apk del ffmpeg && mkdir -p /data/tools
#   COPY my-ffmpeg /data/tools/ffmpeg

# Or at runtime via a mounted volume:
docker run -v $(pwd)/my-ffmpeg:/data/tools/ffmpeg ...
```

## NAS / host-network hardware transcoding

All NAS deployment templates mandate `network_mode: host` (see
`deploy/compose/docker-compose.host.yml` and the various `deployment-*.md`
guides). Under host mode **do not declare `ports:`** (ignored, and some NAS
platforms reject it); resolve port conflicts with the `NVR_LISTEN_PORT` env
var or `server.listen` (see `deployment-faq.md` Q2). Merge the `devices:` /
`deploy:` block from the relevant section above into this template:

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    container_name: mibee-nvr
    restart: unless-stopped
    network_mode: host          # required on NAS: ONVIF multicast reaches the LAN
    # ports: do not declare! ignored under host mode; DSM rejects it

    volumes:
      - /path/to/data:/data
    environment:
      - NVR_DATA_DIR=/data

    # ↓↓↓ pick ONE accelerator block from the sections above ↓↓↓
    # Intel/AMD VAAPI:
    #   devices:
    #     - /dev/dri/renderD128:/dev/dri/renderD128
    #     - /dev/dri/card0:/dev/dri/card0
    #   add to environment: LIBVA_DRIVER_NAME=i965 (Intel) / radeonsi (AMD)
    #
    # NVIDIA NVENC (host needs the NVIDIA Container Toolkit):
    #   deploy:
    #     resources:
    #       reservations:
    #         devices:
    #           - driver: nvidia
    #             count: all
    #             capabilities: [gpu]
    #
    # Raspberry Pi V4L2 M2M:
    #   devices:
    #     - /dev/video10:/dev/video10
    #     - /dev/video11:/dev/video11
    #     - /dev/video12:/dev/video12

    healthcheck:
      test: ["CMD", "mibee-nvr", "health"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

FTP ports likewise need no mapping under host mode; change `ftp.port` / the
passive port range on conflict.

## Troubleshooting Checklist

### Device Access Issues

**Problem**: `Permission denied` when accessing devices
```bash
# Fix device permissions
sudo usermod -a -G video $USER
newgrp video

# For Docker-specific permissions
sudo chown -R 65534:65534 ./data
```

**Problem**: Device not found in container
```bash
# Check host devices
ls -la /dev/video*

# Verify device mapping
docker exec mibee-nvr ls -la /dev/video*
```

### Performance Issues

**Problem**: High CPU usage with software encoding
- Add hardware acceleration if available
- Reduce segment duration to 30s
- Limit concurrent streams based on CPU cores

**Problem**: Poor transcoding quality
- Check encoder settings in configuration
- Consider using appropriate bitrates for your resolution
- Monitor hardware utilization

### VAAPI Issues

**Problem**: `vainfo` shows no drivers
- Verify GPU is installed and working on host
- Check LIBVA_DRIVER_NAME environment variable
- Ensure correct DRI devices are mapped

**Problem**: Black video output
- Check GPU drivers are up to date
- Verify render device permissions
- Test with simple FFmpeg command first

### NVIDIA Issues

**Problem**: `nvidia-smi` not found in container
- Install NVIDIA Container Toolkit
- Verify Docker runtime is set to `nvidia`
- Check GPU device mapping

**Problem**: NVENC encoder not available
- Verify GPU supports NVENC
- Check NVIDIA drivers are up to date
- Test with FFmpeg: `ffmpeg -encoders | grep nvenc`

### General Docker Issues

**Problem**: Container restarts continuously
- Check Docker logs: `docker compose logs mibee-nvr`
- Verify configuration syntax
- Check disk space and permissions

**Problem**: Health check fails
- Test manual health check: `docker exec mibee-nvr mibee-nvr health`
- Check if binary exists and is executable
- Verify data directory permissions

### Debug Commands

```bash
# View container logs
docker compose logs -f mibee-nvr

# Check resource usage
docker stats mibee-nvr

# Test health check
docker exec mibee-nvr mibee-nvr health

# Check FFmpeg availability
docker exec mibee-nvr /data/tools/ffmpeg -version

# List available encoders
docker exec mibee-nvr /data/tools/ffmpeg -encoders | grep -E "(h264|h265|nvenc|vaapi|v4l2)"
```

## Additional Tips

1. **Start Simple**: Begin with software-only configuration to verify basic functionality
2. **Test Incrementally**: Add one hardware component at a time
3. **Monitor Resources**: Use `docker stats` to monitor CPU, memory, and I/O
4. **Update Regularly**: Keep Docker images and drivers up to date
5. **Back Up Config**: Always backup your `docker-compose.yml` before major changes