# Docker 硬件转码指南

## 概述

硬件直通对于 Docker 容器中的最佳转码性能至关重要。MiBee NVR 支持多种硬件加速路径：Intel/AMD GPU 的 VAAPI、NVIDIA GPU 的 NVENC，以及树莓派的 V4L2 M2M。本指南介绍如何配置 Docker 以将硬件设备暴露给容器，以实现最大转码效率。

## 树莓派 V4L2 M2M

V4L2 内存到内存 (M2M) 加速为树莓派设备提供硬件加速的 H.264/H.265 编码，相比软件编码显著提升性能。

### 设备映射

树莓派摄像头或 USB 采集卡通常在主机系统中显示为 `/dev/video10`、`/dev/video11`、`/dev/video12`。您的设备编号可能有所不同。

#### 检查可用设备

```bash
# 列出主机上的视频设备
ls -la /dev/video*

# 检查设备功能
v4l2-ctl -d /dev/video10 --list-formats-ext
```

#### Docker Compose 配置

向您的 `docker-compose.yml` 添加设备映射：

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    container_name: mibee-nvr
    restart: unless-stopped
    
    ports:
      - "9090:9090"   # Web UI / API
      - "2121:2121"   # FTP
      - "2122-2140:2122-2140"  # FTP 被动模式端口
    
    # 映射视频设备以进行硬件加速
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

### 验证

检查设备在容器内是否可访问：

```bash
# 进入容器 shell（如果可用）
docker exec -it mibee-nvr sh

# 列出容器内的视频设备
ls -la /dev/video*

# 测试 V4L2 功能
v4l2-ctl -d /dev/video10 --list-formats-ext
```

### 已知问题

- **内核 6.6.63+**: 新内核中存在 V4L2 M2M 错误，可能导致编码失败。如果遇到问题，请考虑降级到内核 6.6.62 或更早版本。
- **设备权限**: 确保您的用户有权访问视频设备。将您添加到 `video` 组：`sudo usermod -a -G video $USER`
- **设备冲突**: 避免同时将同一视频设备用于 Docker 主机和其他应用程序。

## Intel/AMD VAAPI

视频加速 API (VAAPI) 为 Intel 和 AMD GPU 提供硬件加速，实现高效的 H.264/H.265 转码。

### 设备映射

VAAPI 需要访问整个 `/dev/dri` 设备组，包括渲染和卡设备。

#### 检查可用设备

```bash
# 列出 DRI 设备
ls -la /dev/dri/*

# 检查 GPU 功能
vainfo
```

#### Docker Compose 配置

添加 DRI 设备映射：

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    container_name: mibee-nvr
    restart: unless-stopped
    
    ports:
      - "9090:9090"   # Web UI / API
      - "2121:2121"   # FTP
      - "2122-2140:2122-2140"  # FTP 被动模式端口
    
    # 映射 DRI 设备以进行 VAAPI 加速
    devices:
      - /dev/dri/renderD128:/dev/dri/renderD128
      - /dev/dri/card0:/dev/dri/card0
    
    volumes:
      - ./data:/data
    environment:
      - NVR_DATA_DIR=/data
      - LIBVA_DRIVER_NAME=i965  # 对于 Intel GPU
      # - LIBVA_DRIVER_NAME=radeonsi  # 对于 AMD GPU
    healthcheck:
      test: ["CMD", "mibee-nvr", "health"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

### 验证

在容器内测试 VAAPI 功能：

```bash
# 进入容器
docker exec -it mibee-nvr sh

# 检查 VAAPI 信息
vainfo
```

如果硬件加速正常工作，您应该看到列出可用 VAAPI 驱动程序和格式的输出。

## NVIDIA

NVIDIA GPU 提供 NVENC 硬件加速用于 H.264/H.265 编码，具有出色的性能和质量。

### 先决条件

安装 NVIDIA Container Toolkit：

```bash
# 添加 NVIDIA Container Toolkit 仓库
distribution=$(. /etc/os-release;echo $ID$VERSION_ID)
curl -s -L https://nvidia.github.io/nvidia-docker/gpgkey | sudo apt-key add -
curl -s -L https://nvidia.github.io/nvidia-docker/$distribution/nvidia-docker.list | sudo tee /etc/apt/sources.list.d/nvidia-docker.list

# 安装工具包
sudo apt-get update
sudo apt-get install -y nvidia-container-toolkit

# 重启 Docker
sudo systemctl restart docker
```

#### Docker Compose 配置

使用 NVIDIA 运行时进行 GPU 访问：

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    container_name: mibee-nvr
    restart: unless-stopped
    
    ports:
      - "9090:9090"   # Web UI / API
      - "2121:2121"   # FTP
      - "2122-2140:2122-2140"  # FTP 被动模式端口
    
    # 使用 NVIDIA 运行时进行 GPU 访问
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

#### 替代设备映射

如果您更喜欢设备映射而不是部署配置：

```yaml
services:
  mibee-nvr:
    # ... 其他配置 ...
    
    runtime: nvidia
    devices:
      - /dev/nvidia0:/dev/nvidia0
      - /dev/nvidia1:/dev/nvidia1
      - /dev/nvidiactl:/dev/nvidiactl
      - /dev/nvidia-uvm:/dev/nvidia-uvm
      - /dev/nvidia-uvm-tools:/dev/nvidia-uvm-tools
    
    # ... 其余配置 ...
```

### 验证

检查 GPU 是否可访问：

```bash
# 检查容器内的 GPU 访问
docker exec mibee-nvr nvidia-smi

# 测试 NVENC 功能
docker exec mibee-nvr ffmpeg -encoders | grep nvenc
```

## 仅软件回退

当硬件加速不可用或不想要时，MiBee NVR 回退到使用 libx264/libx265 的软件编码。

### 配置

不需要设备映射：

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    container_name: mibee-nvr
    restart: unless-stopped
    
    ports:
      - "9090:9090"   # Web UI / API
      - "2121:2121"   # FTP
      - "2122-2140:2122-2140"  # FTP 被动模式端口
    
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

### 性能预期

| 硬件 | 编码 | 性能说明 |
|------|------|----------|
| 树莓派 3B | 软件 H.264 | 每流约 1-2 FPS，CPU 密集型 |
| 树莓派 4 | 软件 H.264 | 每流约 3-5 FPS，中等 CPU 使用率 |
| Intel NUC | 软件 H.264 | 每流约 15-30 FPS，中等 CPU 使用率 |
| 桌面 CPU | 软件 H.264 | 每流约 30-60 FPS，CPU 密集型 |

### 何时使用软件与硬件

**使用硬件加速时：**
- 您需要更高的帧率（>5 FPS）
- CPU 资源有限
- 您有多个并发流
- 能效很重要

**使用软件编码时：**
- 硬件设备不可用
- 需要最大兼容性
- 需要精细调整质量设置
- 没有特殊硬件的开发/测试

## Docker 内的 FFmpeg

MiBee NVR 的 Docker 镜像**内置了 FFmpeg**（以及 `ffprobe`），通过 Alpine 的 `ffmpeg` 包提供。

> **注意**：FFmpeg 是**可选依赖**。NVR 的所有核心功能（录制、回放、直播、推流、延时摄影、合并）都是纯 Go 实现，**不需要 FFmpeg**。Docker 镜像内置它只是为了开箱即用的转码体验。转码、实时转码开箱即用——无需手动下载。

### 验证内置的 FFmpeg

```bash
# FFmpeg 位于 PATH 上的 /usr/bin/ffmpeg
docker exec mibee-nvr ffmpeg -version

# 列出可用的编码器（软件编码 libx264/libx265 始终存在）
docker exec mibee-nvr ffmpeg -encoders | grep -E "(h264|h265|nvenc|vaapi|v4l2)"
```

### 解析顺序

NVR 按以下顺序解析 FFmpeg 二进制文件（见 `internal/transcoding/downloader.go:GetFFmpegStatus`）：

1. **`exec.LookPath("ffmpeg")`** —— 首先找到内置的 `/usr/bin/ffmpeg`。
2. **`{data_dir}/tools/ffmpeg`** —— 用户提供的自定义构建（仅在内置版本不存在时使用）。
3. **应用内下载器** —— 从 johnvansickle.com 获取静态构建到 `{data_dir}/tools/`（需要 `xz` 工具，已一同内置；作为升级/后备路径）。

由于内置的 FFmpeg 在 PATH 上优先，下载器实际上是空操作，除非您明确移除 Alpine 的 FFmpeg 或需要更新/不同的构建。

### 自定义 FFmpeg 构建

要用自定义构建覆盖内置的 FFmpeg（例如需要额外编解码器的更新版本），请移除内置版本并将二进制文件放在 tools 目录中：

```bash
# 在派生的 Dockerfile 中：
#   RUN apk del ffmpeg && mkdir -p /data/tools
#   COPY my-ffmpeg /data/tools/ffmpeg

# 或通过挂载卷在运行时提供：
docker run -v $(pwd)/my-ffmpeg:/data/tools/ffmpeg ...
```

## 故障排除清单

### 设备访问问题

**问题**：访问设备时出现 `Permission denied`
```bash
# 修复设备权限
sudo usermod -a -G video $USER
newgrp video

# 针对 Docker 特定权限
sudo chown -R 65534:65534 ./data
```

**问题**：在容器中找不到设备
```bash
# 检查主机设备
ls -la /dev/video*

# 验证设备映射
docker exec mibee-nvr ls -la /dev/video*
```

### 性能问题

**问题**：软件编码时 CPU 使用率高
- 如果可用，添加硬件加速
- 将段持续时间减少到 30 秒
- 根据 CPU 核心限制并发流数

**问题**：转码质量差
- 检查配置中的编码器设置
- 考虑为您的分辨率使用适当的比特率
- 监控硬件利用率

### VAAPI 问题

**问题**：`vainfo` 显示无驱动程序
- 验证 GPU 在主机上已安装并正常工作
- 检查 LIBVA_DRIVER_NAME 环境变量
- 确保正确映射了 DRI 设备

**问题**：黑色视频输出
- 检查 GPU 驱动程序是否为最新版本
- 验证渲染设备权限
- 首先用简单的 FFmpeg 命令测试

### NVIDIA 问题

**问题**：容器中找不到 `nvidia-smi`
- 安装 NVIDIA Container Toolkit
- 验证 Docker 运行时设置为 `nvidia`
- 检查 GPU 设备映射

**问题**：NVENC 编码器不可用
- 验证 GPU 支持 NVENC
- 检查 NVIDIA 驱动程序是否为最新版本
- 使用 FFmpeg 测试：`ffmpeg -encoders | grep nvenc`

### 常见 Docker 问题

**问题**：容器持续重启
- 检查 Docker 日志：`docker compose logs mibee-nvr`
- 验证配置语法
- 检查磁盘空间和权限

**问题**：健康检查失败
- 测试手动健康检查：`docker exec mibee-nvr mibee-nvr health`
- 检查二进制文件是否存在且可执行
- 验证数据目录权限

### 调试命令

```bash
# 查看容器日志
docker compose logs -f mibee-nvr

# 检查资源使用情况
docker stats mibee-nvr

# 测试健康检查
docker exec mibee-nvr mibee-nvr health

# 检查 FFmpeg 可用性
docker exec mibee-nvr /data/tools/ffmpeg -version

# 列出可用编码器
docker exec mibee-nvr /data/tools/ffmpeg -encoders | grep -E "(h264|h265|nvenc|vaapi|v4l2)"
```

## 其他提示

1. **从简单开始**：首先使用仅软件配置验证基本功能
2. **增量测试**：一次添加一个硬件组件
3. **监控资源**：使用 `docker stats` 监控 CPU、内存和 I/O
4. **定期更新**：保持 Docker 镜像和驱动程序为最新版本
5. **备份配置**：在进行重大更改之前始终备份您的 `docker-compose.yml`