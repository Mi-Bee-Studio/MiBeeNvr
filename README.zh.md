# MiBee NVR

轻量级网络视频录像机，使用 Go 编写。支持 RTSP (H.264/MJPEG) 和 HTTP JPEG 摄像头，内置 Web 管理界面、WebDAV、FTP 和 MQTT 集成。编译为单文件静态二进制，内嵌前端页面，无需外部依赖。

[**English**](README.md)

<!-- TODO: add screenshots -->

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
