# MiBee NVR

**[English](#english) | [中文](#中文)**

---

<a id="english"></a>

## MiBee NVR

A lightweight Network Video Recorder (NVR) written in Go. Supports RTSP (H.264/MJPEG) and HTTP JPEG cameras, with a built-in Web UI, WebDAV, FTP, and MQTT integration. Compiles to a single static binary with an embedded SPA frontend.

<!-- TODO: add screenshots -->

## Features

- RTSP (H.264/MJPEG) and HTTP JPEG camera support
- Automatic MP4 segment recording
- Web UI for camera management and recording playback
- WebDAV (read-only) and FTP file access
- MQTT trigger-based recording for smart home integration
- Multi-camera concurrent recording
- Automatic cleanup with retention and disk threshold policies
- SQLite metadata storage
- Single static binary, no external dependencies (`CGO_ENABLED=0`)

## Quick Start

```bash
# Build
make build

# Create config
cp config.example.yaml mibee-nvr.yaml

# Run
./mibee-nvr -config mibee-nvr.yaml
```

Open `http://localhost:9090` to access the Web UI.

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](docs/en/getting-started.md) | Installation, first camera setup |
| [Configuration](docs/en/configuration.md) | Full config reference |
| [API Reference](docs/en/api-reference.md) | REST API documentation |
| [MediaMTX Guide](docs/en/mediamtx-guide.md) | MediaMTX integration for CSI cameras |
| [Deployment](docs/en/deployment.md) | systemd, reverse proxy, cross-compile |

## Build

```bash
make build        # Local build
make cross        # Cross-compile for ARM64
make test         # Run tests
make lint         # Run linter
```

## Project Structure

```
cmd/mibee-nvr/       # Entry point
internal/            # Core packages
  api/               # REST API handlers
  camera/            # Camera manager
  recorder/          # H.264/MJPEG recording engines
  storage/           # SQLite DB + file manager
  config/            # YAML config
  middleware/        # Auth middleware
  muxer/             # MP4 muxer
  ftp/               # FTP server
  webdav/            # WebDAV server (read-only)
  mqtt/              # MQTT client
  ui/                # Embedded web UI
web/                 # Svelte 5 frontend
deploy/              # systemd services
docs/                # Documentation (EN/ZH)
```

## Contributing

1. Run `make lint` before submitting
2. Add tests for new features
3. Write clear commit messages

## License

[MIT License](LICENSE) © Mi&Bee Studio

---

<a id="中文"></a>

## MiBee NVR

轻量级网络视频录像机，使用 Go 编写。支持 RTSP (H.264/MJPEG) 和 HTTP JPEG 摄像头，内置 Web 管理界面、WebDAV、FTP 和 MQTT 集成。编译为单文件静态二进制，内嵌前端页面，无需外部依赖。

## 功能特性

- 支持 RTSP (H.264/MJPEG) 和 HTTP JPEG 摄像头
- 自动将视频流封装为 MP4 片段存储
- Web 管理界面，查看摄像头状态和录像回放
- WebDAV（只读）和 FTP 文件访问
- MQTT 消息触发录像，灵活集成智能家居
- 多摄像头同时录像
- 自动清理过期录像，支持磁盘空间阈值
- SQLite 存储元数据
- 单文件部署，无外部依赖 (`CGO_ENABLED=0`)

## 快速开始

```bash
# 编译
make build

# 创建配置文件
cp config.example.yaml mibee-nvr.yaml

# 运行
./mibee-nvr -config mibee-nvr.yaml
```

打开 `http://localhost:9090` 即可访问管理界面。

## 文档

| 文档 | 说明 |
|------|------|
| [快速入门](docs/zh/getting-started.md) | 安装、添加第一个摄像头 |
| [配置说明](docs/zh/configuration.md) | 完整配置参考 |
| [API 文档](docs/zh/api-reference.md) | REST API 接口文档 |
| [MediaMTX 指南](docs/zh/mediamtx-guide.md) | MediaMTX CSI 摄像头集成 |
| [部署指南](docs/zh/deployment.md) | systemd、反向代理、交叉编译 |

## 编译

```bash
make build        # 本机编译
make cross        # 交叉编译 ARM64
make test         # 运行测试
make lint         # 代码检查
```

## 项目结构

```
cmd/mibee-nvr/       # 程序入口
internal/            # 核心模块
  api/               # REST API
  camera/            # 摄像头管理
  recorder/          # H.264/MJPEG 录像引擎
  storage/           # SQLite 数据库 + 文件管理
  config/            # YAML 配置
  middleware/        # 认证中间件
  muxer/             # MP4 封装器
  ftp/               # FTP 服务
  webdav/            # WebDAV 服务（只读）
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
