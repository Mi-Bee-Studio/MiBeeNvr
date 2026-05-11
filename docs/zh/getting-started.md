# MiBee NVR 快速入门指南

## 简介

MiBee NVR 是一个用 Go 语言编写的轻量级家用网络视频录像机。它支持 RTSP (H.264/MJPEG) 和 HTTP JPEG 摄像头录制，将视频流保存为 MP4 片段。MiBee NVR 提供简洁的 Web 管理界面，支持 WebDAV（只读）、FTP 和 MQTT 集成，可以部署为单个静态二进制文件，内嵌 SPA 前端。

### 主要特性

- 支持多种摄像头协议：RTSP H.264、RTSP H.265、RTSP MJPEG、HTTP JPEG
- 实时录制为 MP4 片段，自动分段管理
- Web 管理界面，支持摄像头配置和录像管理
- WebDAV（可配置只读/读写）访问，支持文件浏览器播放
- FTP 服务器，便于第三方工具访问
- MQTT 集成，支持远程触发
- 轻量级设计，低内存占用
- 单文件部署，无需额外依赖

## 环境要求

### 系统要求

- **操作系统**: Linux (推荐 Ubuntu 20.04+ 或其他主流发行版)
- **架构**: AMD64 或 ARM64
- **内存**: 推荐 1GB 以上，最低 512MB
- **存储**: 足够的磁盘空间用于录像存储

### 软件要求

- **Go 1.26+**: 用于编译和运行
- **存储设备**: 推荐使用 SSD 或高性能 SD 卡

### 硬件要求

- **处理器**: 任何支持 x86_64 或 arm64 的处理器
- **网络**: 有线网络连接（推荐），WiFi 也可用
- **摄像头**: 支持 RTSP 或 HTTP JPEG 的网络摄像头

## 快速开始

### 1. 获取 MiBee NVR

#### 从发布版本下载

访问 [GitHub Releases 页面](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases) 下载预编译的二进制文件：

```bash
# 下载最新版本
wget https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/latest/download/mibee-nvr-linux-amd64
chmod +x mibee-nvr-linux-amd64
```

#### 从源码编译

```bash
# 克隆仓库
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr

# 编译
make build
# 或者交叉编译到 ARM64
make cross
```

### 2. 创建配置文件

复制示例配置文件：

```bash
cp config.example.yaml mibee-nvr.yaml
```

编辑 `config.yaml` 文件，配置服务器、存储和摄像头设置。

### 3. 启动服务

```bash
# 直接运行
./mibee-nvr

# 后台运行
nohup ./mibee-nvr > nvr.log 2>&1 &

# 使用 systemd（推荐）
sudo systemctl start mibee-nvr
```

### 4. 访问 Web 界面

打开浏览器访问 `http://your-server-ip:9090`，默认用户名和密码在配置文件中设置。

## 添加第一个摄像头

### RTSP H.264 摄像头

```yaml
cameras:
  - id: "cam1"
    name: "前门摄像头"
    protocol: "rtsp_h264"
    url: "rtsp://192.168.1.100:554/h264/main"
    username: "admin"
    password: "password123"
    enabled: true
```

### RTSP H.265 摄像头

```yaml
cameras:
  - id: "cam4"
    name: "H.265 摄像头"
    protocol: "rtsp_h265"
    url: "rtsp://192.168.1.103:554/stream"
    enabled: true
```

### RTSP MJPEG 摄像头


```yaml
cameras:
  - id: "cam2"
    name: "后院摄像头"
    protocol: "rtsp_mjpeg"
    url: "rtsp://192.168.1.101:554/mjpeg"
    enabled: true
```

### HTTP JPEG 摄像头

```yaml
cameras:
  - id: "cam3"
    name: "室内摄像头"
    protocol: "http_jpeg"
    url: "http://192.168.1.102:8080/snapshot"
    enabled: true
```

## 访问录像

### Web 管理界面

通过 Web 界面可以：
- 查看实时摄像头画面
- 播放历史录像
- 下载录像文件
- 管理摄像头配置
- 查看存储统计

### WebDAV 只读访问

配置 WebDAV 后，可以通过文件浏览器访问：

```bash
# 在文件管理器中访问
davs://your-server-ip:9090/dav/

# 使用 curl 下载
curl -u admin:password -O "http://your-server-ip:9090/dav/recordings/2024/01/01/cam1_1704110400.mp4"
```

### FTP 访问

启用 FTP 服务器后，可以通过 FTP 客户端访问：

```bash
# 连接参数
主机: your-server-ip
端口: 2121
用户名: admin  
密码: password

# 查看文件
ftp> ls recordings/
```

## 健康检查

检查 MiBee NVR 运行状态：

```bash
curl http://localhost:9090/api/health
```

预期响应：
```json
{
  "status": "ok",
  "uptime": "2h30m",
  "checks": {
    "database": {"status": "ok"},
    "storage": {"status": "ok", "message": "20% used (10737418240 / 53687091200 bytes)"},
    "goroutines": {"status": "ok", "message": "42 goroutines"}
  },
  "version": "0.1.0-dev"
}
```

## 下一步

- 查看 [配置文档](configuration.md) 了解详细的配置选项
- 阅读 [媒体服务器指南](mediamtx-guide.md) 了解如何配置 mediamtx
- 查看 [API 参考文档](api-reference.md) 了解 API 接口
- 阅读 [部署文档](deployment.md) 了解生产环境部署

## 故障排除

如果遇到问题，请检查：

1. **配置文件格式**: 确保 YAML 格式正确
2. **网络连接**: 确保摄像头可以访问
3. **存储权限**: 确保有写入存储目录的权限
4. **端口占用**: 确保配置的端口没有被占用
5. **内存使用**: 监控内存使用，避免过度占用

## 支持

如需帮助，请：
- 查看 [Issues 页面](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues)
- 提交新的 Issue 描述问题
- 查看项目文档和 FAQ