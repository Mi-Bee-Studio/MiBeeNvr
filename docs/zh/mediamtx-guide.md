# MiBee NVR MediaMTX 集成指南

## 概述

MediaMTX 是一个功能强大的媒体服务器，可以作为 MiBee NVR 的 RTSP 代理服务器。它提供了 RTSP 到 WebRTC、HLS、WebRTC 等协议的转换功能，特别适合与 MiBee NVR 集成使用。

MediaMTX 的主要优势：
- 支持多种摄像头协议（RTSP、RTMP、HTTP-FLV、WebRTC）
- 提供实时流转换功能
- 支持负载均衡和故障转移
- 易于配置和部署
- 与 MiBee NVR 完美集成

## MediaMTX 简介

### 什么是 MediaMTX

MediaMTX (原 RTSP-simple-server) 是一个开源的媒体服务器，专注于：
- RTSP/RTMP/WebRTC 客户端/服务器
- 实时流媒体处理
- 协议转换和转发
- 媒体发布和订阅

### 主要功能

- **协议支持**: RTSP, RTMP, WebRTC, HLS, HTTP-FLV
- **流管理**: 实时录制、转发、转换
- **负载均衡**: 多摄像头流分发
- **认证**: 基本认证、自定义认证
- **监控**: 统计和日志

## 安装 MediaMTX

### 从官方 releases 安装

```bash
# 下载最新版本
wget https://github.com/bluenviron/mediamtx/releases/latest/download/mediamtx-linux-amd64

# 赋予执行权限
chmod +x mediamtx-linux-amd64

# 移动到系统路径
sudo mv mediamtx-linux-amd64 /usr/local/bin/mediamtx
```

### 从源码编译

```bash
# 安装依赖
sudo apt-get install -y golang git

# 克隆仓库
git clone https://github.com/bluenviron/mediamtx.git
cd mediamtx

# 编译
go build -mod=readonly -o mediamtx ./cmd/mediamtx
```

### 使用 Docker

```bash
# 拉取镜像
docker pull bluenviron/mediamtx:latest

# 运行容器
docker run -d --name mediamtx \
  -p 8554:8554 -p 8000:8000 -p 1935:1935 \
  -v /path/to/config.yml:/etc/mediamtx.yml \
  bluenviron/mediamtx:latest
```

## 基本配置

### 创建配置文件

```bash
# 复制示例配置
cp mediamtx.yml.example mediamtx.yml
```

### 基本配置示例

```yaml
# MediaMTX 基本配置

# RTSP 配置
paths:
  all:
    # 启用录制
    record: true
    # 录制路径
    recordPath: /path/to/recordings
    # 录制格式
    recordFormat: fmp4
    # 最大录制时长
    recordMaxDuration: 1h

# 服务器配置
protocols:
  # RTSP 协议
  - protocol: rtsp
    listen: :8554
    # RTSP-over-TCP
    rtspOverTcp: true
    # 认证
    authMethod: static
    authUsers:
      user: password

  # RTMP 协议
  - protocol: rtmp
    listen: :1935

  # WebRTC 协议
  - protocol: webrtc
    listen: :8889
    # WebRTC 验证密钥
    webrtcServerKey: mediamtx

# 日志配置
logging:
  level: info
  format: json
```

## 与 MiBee NVR 集成

### 集成架构

```text
摄像头 → MediaMTX → MiBee NVR
       ↓    ↓         ↓
    RTSP  WebRTC     HTTP JPEG
    RTMP  HLS        MP4 录像
```

### 推荐配置

#### 1. MediaMTX 配置

```yaml
# mediamtx.yml - MediaMTX 配置

paths:
  # 默认路径配置
  all:
    # 启用录制（可选）
    record: false
    # 启用发布
    publishUser: admin
    publishPass: password123
    # 启用订阅
    playUser: admin
    playPass: password123
    # 转换为 JPEG
    overridePublisher:
      videoCodec: jpeg
      videoBitrate: 1000

# RTSP 服务器配置
protocols:
  - protocol: rtsp
    listen: :8554
    # 启用 TCP 模式（更稳定）
    rtspOverTcp: true
    # 认证
    authMethod: static
    authUsers:
      admin: password123

# RTMP 服务器配置（可选）
protocols:
  - protocol: rtmp
    listen: :1935
    authMethod: static
    authUsers:
      admin: password123

# WebRTC 配置（可选）
protocols:
  - protocol: webrtc
    listen: :8889
    webrtcServerKey: mediamtx-key
```

#### 2. MiBee NVR 配置

使用 MediaMTX 作为 RTSP 源：

```yaml
# config.yaml - MiBee NVR 配置

cameras:
  - id: "cam1"
    name: "前门摄像头"
    protocol: "rtsp"
    encoding: "h264"
    # 使用 MediaMTX 作为中间代理
    url: "rtsp://admin:password123@localhost:8554/cam1"
    enabled: true
    recording_enabled: true

  - id: "cam2"
    name: "后院摄像头"
    protocol: "rtsp"
    encoding: "mjpeg"
    url: "rtsp://admin:password123@localhost:8554/cam2"
    enabled: true
    recording_enabled: true
```

### MediaMTX 路径配置

为每个摄像头创建专用路径：

```yaml
# mediamtx.yml - 多摄像头路径配置

paths:
  # 摄像头 1
  cam1:
    source: rtsp://192.168.1.100:554/stream
    record: true
    recordPath: /mnt/data/nvr/recordings/cam1
    recordFormat: fmp4
    recordMaxDuration: 30m
    publishUser: admin
    publishPass: password123
    
  # 摄像头 2
  cam2:
    source: rtsp://192.168.1.101:554/live
    record: false  # 不录制，由 MiBee NVR 录制
    publishUser: admin
    publishPass: password123
    
  # 默认路径
  all:
    publishUser: admin
    publishPass: password123
    overridePublisher:
      videoCodec: jpeg
      videoBitrate: 1000
```

## CSI 摄像头流水线

### 使用 CSI 摄像头

MediaMTX 可以与树莓派 CSI 摄像头集成，提供实时流处理。

#### 1. 安装摄像头支持

```bash
# 安装 raspicam
sudo apt-get install -y raspistill raspivid

# 创建流水线脚本
cat > /usr/local/bin/csi-stream.sh << 'EOF'
#!/bin/bash
# CSI 摄像头到 MediaMTX 的流水线

raspivid -t 0 -rot 0 -w 1920 -h 1080 -b 4000000 -fps 30 \
  -o - | ffmpeg -re -i pipe:0 \
  -c:v copy \
  -f rtsp -rtsp_transport tcp rtsp://localhost:8554/camera1
EOF

chmod +x /usr/local/bin/csi-stream.sh
```

#### 2. MediaMTX 配置

```yaml
# mediamtx.yml - CSI 摄像头配置

paths:
  camera1:
    source: rtsp://localhost:8554/camera1
    record: true
    recordPath: /mnt/data/nvr/recordings/camera1
    recordFormat: fmp4
    publishUser: admin
    publishPass: password123
```

#### 3. 启动 CSI 流水线

```bash
# 启动 CSI 流水线
sudo systemctl start csi-stream
```

### SystemD 服务配置

```bash
# 创建服务文件
sudo tee /etc/systemd/system/csi-stream.service << 'EOF'
[Unit]
Description=CSI Camera Stream to MediaMTX
After=network.target mediamtx.service

[Service]
Type=simple
User=mibee
ExecStart=/usr/local/bin/csi-stream.sh
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# 启用服务
sudo systemctl enable csi-stream
sudo systemctl start csi-stream
```

## 多摄像头配置

### 负载均衡配置

```yaml
# mediamtx.yml - 负载均衡配置

paths:
  # 主摄像头
  main-camera:
    source: rtsp://192.168.1.100:554/stream
    record: true
    recordPath: /mnt/data/nvr/recordings/main
    fallback:
      - rtsp://192.168.1.101:554/stream
      
  # 辅助摄像头
  backup-camera:
    source: rtsp://192.168.1.101:554/stream
    record: true
    recordPath: /mnt/data/nvr/recordings/backup

# 负载均衡路径
paths:
  camera-lb:
    source:
      - rtsp://192.168.1.100:554/stream
      - rtsp://192.168.1.101:554/stream
    loadBalance: random
    record: true
    recordPath: /mnt/data/nvr/recordings/lb
```

### 集群配置

```yaml
# mediamtx.yml - 集群配置

# 主服务器
protocols:
  - protocol: rtsp
    listen: :8554
    cluster:
      mode: node
      nodes:
        - address: mediamtx1:8554
        - address: mediamtx2:8554
        - address: mediamtx3:8554
```

## 常见问题排查

### 1. 连接问题

#### 摄像头无法连接到 MediaMTX

```bash
# 测试 RTSP 连接
ffmpeg -rtsp_transport tcp -i "rtsp://admin:password123@localhost:8554/cam1" -t 5 -f null -

# 查看 MediaMTX 日志
mediamtx --config mediamtx.yml --log-level debug

# 检查端口占用
netstat -tlnp | grep :8554
```

#### MediaMTX 无法连接到摄像头

```bash
# 直接测试摄像头
ffmpeg -rtsp_transport tcp -i "rtsp://192.168.1.100:554/stream" -t 5 -f null -

# 检查摄像头网络连通性
ping 192.168.1.100
nmap -p 554 192.168.1.100
```

### 2. 性能问题

#### 高 CPU 使用率

```yaml
# 优化配置
paths:
  all:
    videoBitrate: 1000        # 降低比特率
    fps: 15                   # 降低帧率
    gopSize: 30              # 增加 GOP 大小
```

#### 内存使用过高

```yaml
# 内存优化
paths:
  all:
    recordMaxDuration: 10m   # 减少录制时长
    bufferType: ring         # 使用环形缓冲区
    bufferTimeMs: 1000       # 减少缓冲时间
```

### 3. 认证问题

#### 认证失败

```yaml
# 检查认证配置
paths:
  all:
    authMethod: static
    authUsers:
      admin: password123      # 确保密码正确

# 测试认证访问
ffmpeg -rtsp_transport tcp -i "rtsp://admin:password123@localhost:8554/cam1" -t 5 -f null -
```

### 4. 录制问题

#### 录制失败

```bash
# 检查存储权限
ls -la /mnt/data/nvr/recordings/
sudo chown -R mibee:mibee /mnt/data/nvr/recordings/

# 检查磁盘空间
df -h /mnt/data/nvr

# 查看 MediaMTX 录制日志
mediamtx --config mediamtx.yml --log-level debug
```

## 监控和日志

### 日志配置

```yaml
# mediamtx.yml - 日志配置

logging:
  level: info
  format: json
  files:
    - path: /var/log/mediamtx.log
      maxSize: 100MB
      maxBackups: 5
      compress: true
```

### 统计信息

```bash
# 查看 MediaMTX 统计信息
curl http://localhost:8889/stats

# 查看 MiBee NVR 统计信息
curl http://localhost:9090/api/stats
```

### Prometheus 监控

```yaml
# mediamtx.yml - Prometheus 配置

metrics:
  enabled: true
  address: :9998
  path: /metrics
```

## 性能优化

### 网络优化

```yaml
# 网络优化配置
protocols:
  - protocol: rtsp
    listen: :8554
    rtspOverTcp: true        # 使用 TCP 更稳定
    rtspReadTimeout: 10s     # 读取超时
    rtspWriteTimeout: 10s    # 写入超时
```

### 编码优化

```yaml
# 编码优化配置
paths:
  all:
    videoCodec: h264         # 使用 H.264
    videoBitrate: 2000       # 2 Mbps
    audioCodec: aac          # 使用 AAC
    audioBitrate: 128       # 128 kbps
    fps: 25                 # 25 FPS
    gopSize: 50             # 2秒 GOP
```

### 缓冲优化

```yaml
# 缓冲优化配置
paths:
  all:
    bufferType: ring         # 环形缓冲区
    bufferTimeMs: 2000      # 2秒缓冲
    ringSize: 1000          # 缓冲区大小
```

## 最佳实践

### 1. 部署架构

```text
摄像头 → MediaMTX (负载均衡) → MiBee NVR
         ↓
      监控节点
         ↓
      存储集群
```

### 2. 配置管理

```bash
# 使用配置管理工具
ansible-playbook -i inventory mediamtx.yml
```

### 3. 备份策略

```bash
# 定期备份配置
*/5 * * * * cp /etc/mediamtx.yml /backup/mediamtx-$(date +\%Y\%m\%d).yml
```

### 4. 更新策略

```bash
# 滚动更新脚本
cat > update-mediamtx.sh << 'EOF'
#!/bin/bash
# 停止服务
sudo systemctl stop mediamtx

# 备份配置
sudo cp /etc/mediamtx.yml /etc/mediamtx.yml.bak

# 更新二进制文件
sudo wget -O /usr/local/bin/mediamtx https://github.com/bluenviron/mediamtx/releases/latest/download/mediamtx-linux-amd64
sudo chmod +x /usr/local/bin/mediamtx

# 启动服务
sudo systemctl start mediamtx
EOF

chmod +x update-mediamtx.sh
```

## 总结

MediaMTX 为 MiBee NVR 提供了强大的媒体处理能力，通过合理的配置可以实现：

- 更稳定的摄像头连接
- 更多的协议支持
- 更好的性能表现
- 更灵活的部署方式

建议根据实际需求选择合适的配置方案，并定期监控系统运行状态。