# MiBee NVR

[![GitHub Release](https://img.shields.io/github/v/release/Mi-Bee-Studio/MiBeeNvr?style=flat&label=Release)](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/Mi-Bee-Studio/MiBeeNvr/ci.yml?style=flat&label=CI)](https://github.com/Mi-Bee-Studio/MiBeeNvr/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev/)
[![Svelte](https://img.shields.io/badge/Svelte-FF3E00?style=flat&logo=svelte&logoColor=white)](https://svelte.dev/)
[![SQLite](https://img.shields.io/badge/SQLite-003B57?style=flat&logo=sqlite&logoColor=white)](https://www.sqlite.org/)
[![Docker](https://img.shields.io/badge/Docker-2496ED?style=flat&logo=docker&logoColor=white)](https://www.docker.com/)
[![Raspberry Pi](https://img.shields.io/badge/Raspberry_Pi-A22846?style=flat&logo=raspberrypi&logoColor=white)](https://www.raspberrypi.com/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow?style=flat)](LICENSE)

轻量级、易上手的网络视频录像机，单文件部署，零配置烦恼——下载即用。

专为树莓派及低功耗设备打造。支持主流协议：**RTSP**（H.264/H.265/MJPEG）、**HTTP JPEG**、**HLS** 直播流、**ONVIF** 设备发现、**WebRTC**（WHEP）、**HTTP-FLV**、**RTMP** 接入、**SRT** 接收器。

[**English**](README.md)

## 截图

![登录页](docs/images/login-light.png)
![仪表盘](docs/images/dashboard-light.png)
![设置页](docs/images/settings-light.png)

## 核心功能

- **摄像头协议**：RTSP（H.264/H.265/MJPEG）、HTTP JPEG、ONVIF 设备发现与管理
- **视频录像**：自动 MP4 切片、多摄像头并发、按摄像头设置保留天数
- **实时直播**：HLS / WebRTC（WHEP）/ HTTP-FLV 多协议直播，RTMP 接入 + SRT 接收器
- **片段合并**：自动/手动合并，全局 + 按摄像头策略
- **Web 界面**：深色/浅色主题、响应式、中英文切换、Chart.js 图表
- **智能家居**：MQTT 触发录像、WebDAV/FTP 文件访问
- **单文件部署**：零依赖、内嵌前端、`CGO_ENABLED=0`
- **小米摄像头**：CS2 P2P 协议、云端认证（社区驱动，非核心功能）

## 开发路线

| 状态 | 协议 / 功能 | 说明 |
|------|------------|------|
| ✅ 已完成 | RTSP（H.264/H.265/MJPEG） | 核心流媒体协议 |
| ✅ 已完成 | HTTP JPEG | IP 摄像头快照流 |
| ✅ 已完成 | HLS | 按需直播流 |
| ✅ 已完成 | ONVIF | 设备发现、云台控制、流地址获取 |
| ✅ 已完成 | 小米（CS2 P2P） | 云端认证、H.264/H.265 — 社区支持 |
| ✅ 已完成 | RTMP | 推拉流 |
| ✅ 已完成 | SRT | 低延迟传输 |
| ✅ 已完成 | HTTP-FLV | 浏览器友好的直播流 |
| ✅ 已完成 | WebRTC | 亚秒级延迟实时预览 |
## 快速开始

### 方式 1：预编译二进制（推荐）

从 [GitHub Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases) 下载最新二进制文件：

```bash
# AMD64（大多数 PC/服务器）
wget https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/latest/download/mibee-nvr-amd64
chmod +x mibee-nvr-amd64

# ARM64（树莓派等）
wget https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/latest/download/mibee-nvr-arm64
chmod +x mibee-nvr-arm64
```

初始化配置并启动：

```bash
./mibee-nvr-amd64 init --password yourpassword
./mibee-nvr-amd64 -config mibee-nvr.yaml
```

打开 `http://localhost:9090` 即可访问管理界面。

### 方式 2：Docker

```bash
mkdir -p data
cp config.example.yaml data/mibee-nvr.yaml
# 编辑 data/mibee-nvr.yaml — 设置密码、添加摄像头
docker compose up -d
```

打开 `http://localhost:9090` 即可访问管理界面。详见 [`docker-compose.yml`](docker-compose.yml)。

### 方式 3：一键安装脚本

```bash
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | sudo bash
```

自动下载二进制文件、创建系统用户（`nvr`）、生成配置、安装 systemd 服务并启动。数据目录：`/var/lib/mibee-nvr`。

### 方式 4：源码编译

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr
make build
./mibee-nvr init --password yourpassword
./mibee-nvr -config mibee-nvr.yaml
```

详细设置请参考 [快速入门](docs/zh/getting-started.md)。

## 文档

| 文档 | 说明 |
|------|------|
| [快速入门](docs/zh/getting-started.md) | 安装、添加第一个摄像头 |
| [配置说明](docs/zh/configuration.md) | 完整配置参考 |
| [API 文档](docs/zh/api-reference.md) | REST API 接口文档 |
| [MediaMTX 指南](docs/zh/mediamtx-guide.md) | MediaMTX CSI 摄像头集成 |
| [部署指南](docs/zh/deployment.md) | systemd、反向代理、交叉编译 |

```bash
make build              # 本机编译（当前架构）
make cross              # 交叉编译 ARM64 二进制
make test               # 运行测试
make lint               # 代码检查
```

## Docker 容器镜像

快速部署请参考 [`docker-compose.yml`](docker-compose.yml)：

```bash
docker compose up -d
```

支持两种构建方式：

- **多阶段构建**（`Dockerfile`）：在容器内完成前端+后端编译，需要网络拉取基础镜像
- **交叉编译构建**（`Dockerfile.arm64`）：在宿主机交叉编译后打包，无需 QEMU，使用 `scratch` 基础镜像

```bash
# 构建 amd64 镜像（多阶段构建）
make docker-build

# 构建 arm64 镜像（宿主交叉编译 + scratch 打包）
make docker-build-arm64

# 构建全部架构
make docker-build-all

# 推送到镜像仓库（需先 docker/podman login）
make docker-push              # 推送 amd64
make docker-push-arm64        # 推送 arm64
make docker-push-all          # 推送全部

# 一键构建并推送
make docker-release
```

镜像在打版本标签时自动发布到 GitHub Container Registry：

| 镜像 | 架构 |
|------|------|
| `ghcr.io/mi-bee-studio/mibeenvr:<tag>` | amd64, arm64 |

可用标签：`latest`、`v1.2.3`（semver）、`sha-abc1234`

## 项目结构

```
cmd/mibee-nvr/       # 程序入口
internal/            # 核心模块
  api/               # REST API
  camera/            # 摄像头管理
  recorder/          # H.264/H.265/MJPEG 录像引擎
  hls/               # HLS 直播管理器
  storage/           # SQLite 数据库 + 文件管理
  config/            # YAML 配置
  middleware/        # 认证中间件
  muxer/             # MP4 封装器
  ftp/               # FTP 服务
  webdav/            # WebDAV 服务（可配置只读/读写）
  mqtt/              # MQTT 客户端
  ui/                # 内嵌 Web UI
web/                 # Svelte 5 前端
deploy/              # systemd 服务文件
docs/                # 文档（中文/英文）
```

## 贡献

1. 提交前运行 `make lint`
2. 新功能附带测试
3. 清晰的提交信息

## 许可证

[MIT License](LICENSE) © Mi&Bee Studio
