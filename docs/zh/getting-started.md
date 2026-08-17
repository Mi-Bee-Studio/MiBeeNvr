# MiBee NVR 快速入门

## MiBee NVR 是什么

MiBee NVR 是一个用 Go 语言编写的轻量级网络视频录像机。它将 IP 摄像头的视频流录制为 MP4 片段并保存到磁盘，提供 Web 界面用于查看录像、管理摄像头和访问录制内容。

**主要功能：**

- 支持 RTSP (H.264、H.265、MJPEG)、HTTP JPEG、ONVIF、Xiaomi (CS2/TUTK P2P)、GB/T 28181 国标平台、SRT/RTMP 推流接入和 Timelapse 摄像头录制为 MP4 片段
- Web 管理界面，支持深色/浅色主题、多协议实时预览（HLS、WebRTC、HTTP-FLV、WebSocket）和 Chart.js 统计图表
- 录像连续播放：双缓冲无缝换段 + 按需 fMP4 分片的全天 VOD 时间轴（跨录像、跨空隙拖动）
- 局域网自动发现：mDNS `_mibee-nvr._tcp` 通告 + UDP 49090 广播应答，`/api/health` 暴露稳定 `device_id`
- WebDAV（可配置只读/读写）和 FTP 访问录像文件
- MQTT 集成，支持事件触发录制
- 片段合并，减少文件数量
- 单一静态二进制文件，内嵌 Web 界面，无外部依赖

## 快速开始（5 分钟）

### 方式一：下载预编译二进制文件（推荐）

从 [GitHub Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases) 下载对应架构的最新版本：

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
./mibee-nvr-amd64 init --password 你的密码
./mibee-nvr-amd64 -config mibee-nvr.yaml
```

在浏览器打开 http://localhost:9090。

### 方式二：Docker

```bash
docker compose --project-directory . -f deploy/docker/docker-compose.yml up -d
```

在浏览器打开 http://localhost:9090。

> **注意**：无需准备配置文件！MiBee NVR 在没有配置文件的情况下启动时会自动初始化。

> **在 NAS 上运行（群晖 / 威联通 / unRAID / 飞牛 / iStoreOS / 极空间）？** 请参阅[部署指南](deployment.md#在-nas-os-上部署)中的各平台指南——涵盖 host 网络（ONVIF 摄像头自动发现所需）、数据卷路径与端口冲突。



#### 修改录像存储位置

默认情况下，录像存储在宿主机的 `./data` 目录（映射到容器内的 `/data`）。
如果要将录像存储到外部硬盘或其他目录，请修改 `docker-compose.yml`：

```yaml
    volumes:
      - /mnt/external/nvr:/data    # ← 改为你的宿主机路径
    environment:
      - NVR_DATA_DIR=/data          # 必须与卷挂载的右侧一致
      # - NVR_UID=1000               # 与宿主机目录所有者的 UID 一致
      # - NVR_GID=1000               # 与宿主机目录所有者的 GID 一致
```

> **重要**：卷挂载的右侧（`:data`）和 `NVR_DATA_DIR` 必须始终一致。
> 如果容器无法启动或不断重启，请检查宿主机目录是否存在，以及配置的 UID/GID（默认 1000:1000）是否有写入权限。

### 方式三：一键安装脚本

```bash
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | sudo bash
```

此脚本会自动下载二进制文件、创建系统用户（`nvr`）、生成配置、安装 systemd 服务并启动。数据目录：`/var/lib/mibee-nvr`。

卸载（保留录像数据）：

```bash
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | sudo bash -s -- --uninstall
```

### 方式四：从源码编译

需要 Go 1.26+ 和 Node.js（编译前端）：

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr
make build
./mibee-nvr init --password 你的密码
./mibee-nvr -config mibee-nvr.yaml
```

交叉编译到 ARM64（如树莓派）：

```bash
make cross
```

## 首次配置

### 使用 `mibee-nvr init`

`init` 子命令创建带有安全默认值的配置文件：

```bash
./mibee-nvr init --password 你的密码
```

选项：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--password` | （交互输入） | Web UI 管理员密码 |
| `--username` | `admin` | 管理员用户名 |
| `--data-dir` | `/var/lib/mibee-nvr` | 录像和数据库的数据目录 |
| `--listen` | `:9090` | HTTP 监听地址 |
| `--config` | `mibee-nvr.yaml` | 配置文件路径 |
| `--force` | false | 覆盖已有配置文件 |

### 密码设置

设置管理员密码有三种方式：

1. **`mibee-nvr init --password <密码>`** — 初始化时直接设置哈希密码（推荐）
2. **配置文件明文** — 在 YAML 中设置 `auth.password`，首次启动时自动转换为 `password_hash`
3. **手动生成哈希** — 使用 `mibee-nvr hash-password <密码>` 生成，粘贴到 `auth.password_hash`

### 默认路径

| 路径 | 说明 |
|------|------|
| `/var/lib/mibee-nvr/` | 数据目录（录像、数据库） |
| `/var/lib/mibee-nvr/mibee-nvr.db` | SQLite 数据库 |
| `mibee-nvr.yaml` | 配置文件 |

## 添加第一个摄像头

MiBee NVR 使用**独立的传输协议 + 编码格式**配置摄像头：

- **传输协议**：`rtsp`、`http`、`onvif`、`xiaomi`、`timelapse`
- **编码格式**：`h264`、`h265`、`mjpeg`、`jpeg`

> 旧版组合格式（`rtsp_h264`、`rtsp_h265`、`rtsp_mjpeg`、`http_jpeg`）在 **0.10.0+ 已被拒绝** —— `Validate()` 与摄像头创建处理器会返回错误，要求拆分为独立的 `protocol` 和 `encoding` 字段。

### RTSP H.264 摄像头

```yaml
cameras:
  - id: "front-door"
    name: "前门"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
```

### RTSP H.265 摄像头

```yaml
cameras:
  - id: "driveway"
    name: "车道"
    protocol: "rtsp"
    encoding: "h265"
    url: "rtsp://192.168.1.103:554/stream"
    enabled: true
```

### HTTP JPEG 摄像头

```yaml
cameras:
  - id: "garage"
    name: "车库"
    protocol: "http"
    encoding: "jpeg"
    url: "http://192.168.1.102:8080/snapshot"
    enabled: true
```

### ONVIF 摄像头

```yaml
cameras:
  - id: "lobby"
    name: "大厅"
    protocol: "onvif"
    url: "http://192.168.1.104:80/onvif/device_service"
    username: "admin"
    password: "camera123"
    enabled: true
```

> ONVIF 会自动检测编码格式，可以省略 `encoding` 字段。

### 延时摄影摄像头

```yaml
cameras:
  - id: "construction"
    name: "施工现场"
    protocol: "timelapse"
    enabled: true
```

> 延时摄影摄像头按周期拍摄快照，不产生连续视频流。

### 组合格式已弃用

> ⚠️ **0.10.0+ 拒绝组合格式**（如 `rtsp_h264`）。如你从旧版升级且配置里仍用组合格式，必须拆分为独立的 `protocol` + `encoding` 字段，否则 NVR 启动时会校验失败。

**错误（旧）**：`protocol: "rtsp_h264"`
**正确（新）**：

```yaml
cameras:
  - id: "cam1"
    name: "摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
```

编辑配置后重启 MiBee NVR，或通过 Web UI 在运行时添加摄像头。

## 访问 MiBee NVR

### Web 管理界面

在浏览器打开 http://你的服务器地址:9090，使用配置的凭据登录。通过 Web UI 可以：

- 查看摄像头实时画面（HLS、WebRTC、HTTP-FLV、WebSocket）
- 回放和下载录像
- 添加、编辑和删除摄像头
- 查看存储统计和趋势
- 配置系统设置

### WebDAV

WebDAV 默认启用（只读模式），访问路径为 `/dav/`：

```bash
curl -u admin:密码 http://你的服务器地址:9090/dav/
```

在文件管理器中挂载：`davs://你的服务器地址:9090/dav/`

要启用读写访问，在配置中设置 `webdav.read_write: true`。

### FTP

FTP 默认启用，端口为 2121：

```bash
ftp 你的服务器地址 2121
# 用户名：admin
# 密码：（你设置的密码）
```

## 常见问题

### 服务无法启动

- 检查配置文件语法：`cat mibee-nvr.yaml`
- 确认数据目录存在且可写：`ls -la /var/lib/mibee-nvr/`
- 查看日志：`journalctl -u mibee-nvr -f`

### 权限错误

- `install.sh` 脚本创建了 `nvr` 系统用户。确保数据目录的所有者正确：
  `sudo chown -R nvr:nvr /var/lib/mibee-nvr/`

### 端口冲突

- 默认端口为 9090。如果被占用，在配置中修改 `server.listen`（如 `":8080"`）。也可不改配置而通过以下方式覆盖监听端口：环境变量 `NVR_LISTEN_PORT`（如 `NVR_LISTEN_PORT=8080`）、`install.sh --port <端口>` 参数、或 Web UI 设置页。

### 无法连接摄像头

- 使用 VLC 或 ffplay 验证摄像头 URL：`ffplay rtsp://192.168.1.100:554/stream`
- 检查网络连通性：`ping 192.168.1.100`
- 确认摄像头用户名和密码正确
- 部分 H.265 摄像头可能需要使用特定的子码流 URL

### 树莓派内存占用过高

- 将 `segment_duration` 设为 `30s`（默认值）。更长的时长会在内存中缓存更多数据。
- 树莓派 3B 约 900MB 内存，4 个摄像头 30 秒片段时稳定运行约占 300MB。
