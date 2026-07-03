# 故障排除指南

本指南帮助您诊断和解决 MiBee NVR 的常见问题。如果在这里找不到解决方案，请查阅 [配置参考](configuration.md)或在 GitHub 上搜索现有问题。

## 常见问题

### 健康检查失败

#### 数据库连接问题
**症状**: 健康检查显示 `"database": {"status": "error", "message": "database is closed"}`
```json
{
  "status": "error",
  "checks": {
    "database": {"status": "error", "message": "database is closed"}
  }
}
```

**解决方案**:
1. 检查存储目录是否存在且可写：
   ```bash
   ls -la /var/lib/mibee-nvr/
   sudo chown -R nvr:nvr /var/lib/mibee-nvr/
   ```
2. 验证数据库文件是否损坏：
   ```bash
   ls -la /var/lib/mibee-nvr/mibee-nvr.db
   file /var/lib/mibee-nvr/mibee-nvr.db
   ```
3. 尝试重新初始化数据库：
   ```bash
   mv /var/lib/mibee-nvr/mibee-nvr.db /var/lib/mibee-nvr/mibee-nvr.db.backup
   ./mibee-nvr -config mibee-nvr.yaml
   ```

#### 存储空间问题
**症状**: 健康检查显示 `"storage": {"status": "error", "message": "disk space critically low"}`
```json
{
  "status": "error", 
  "checks": {
    "storage": {"status": "error", "message": "disk space critically low"}
  }
}
```

**解决方案**:
1. 检查磁盘使用情况：
   ```bash
   df -h
   du -sh /var/lib/mibee-nvr/recordings/
   ```
2. 清理旧录像：
   ```bash
   find /var/lib/mibee-nvr/recordings/ -type f -mtime +30 -delete
   ```
3. 调整配置中的保留天数：
   ```yaml
   cleanup:
     retention_days: 7  # 从 30 天减少到 7 天
     disk_threshold_percent: 90  # 从 95 降低到 90
   ```

## FFmpeg / 转码

### FFmpeg 是必须安装的吗？

**不是。** FFmpeg 是**可选依赖**，仅用于 H.265↔H.264 转码（存储优化 + 实时推流转码）。
所有其他功能（录制、回放、直播、推流、延时摄影、合并）都是纯 Go 实现，**不需要 FFmpeg**。

- **不安装 FFmpeg**：NVR 完整可用，仅转码功能被禁用（启动日志会提示"Transcoding disabled"）
- **安装 FFmpeg**：转码功能自动启用（H.265→H.264 兼容旧设备、H.264→H.265 节省存储）
- Docker 镜像已内置 FFmpeg，开箱即用
- 裸机安装（`install.sh`）会尝试自动安装 FFmpeg，失败不阻断

### 如何手动安装 FFmpeg？

```bash
# Debian/Ubuntu
sudo apt-get install -y ffmpeg

# RHEL/CentOS/Fedora
sudo dnf install -y ffmpeg

# Alpine
sudo apk add ffmpeg
```

或使用 NVR 内置下载器：**设置 → 转码 → 下载 FFmpeg**（下载静态二进制到数据目录）。

## 摄像头问题

### 摄像头未找到

#### 身份验证失败
**症状**: 错误 `"authentication failed: invalid username or password"` 或 `"camera authentication failed"`

**解决方案**:
1. 手动测试摄像头连接：
   ```bash
   # 对于 RTSP
   ffprobe -rtsp_transport tcp "rtsp://用户名:密码@192.168.1.100:554/stream"
   
   # 对于 HTTP JPEG
   curl -I "http://用户名:密码@192.168.1.100/capture"
   ```
2. 验证配置中的摄像头凭据
3. 检查摄像头是否可以从服务器访问：
   ```bash
   ping 192.168.1.100
   nc -zv 192.168.1.100 554
   ```

#### 地址格式问题
**症状**: 错误 `"camera URL has invalid format"` 或连接超时

**解决方案**:
1. 确保地址格式正确：
   ```yaml
   # RTSP - 必须包含端口
   url: "rtsp://192.168.1.100:554/stream"
   
   # HTTP - 必须包含捕获路径
   url: "http://192.168.1.100/capture"
   url: "http://192.168.1.100:8080/cgi-bin/snapshot.cgi"
   ```
2. 为 RTSP 尝试不同的传输方法：
   ```yaml
   # 首先尝试 TCP（更可靠）
   protocol: "rtsp"
   # 如果 TCP 失败，尝试 UDP
   ```

### 摄像头录制问题

#### 摄像头显示"无信号"状态
**症状**: 摄像头已启用但状态显示 `"status": "no_signal"`

**解决方案**:
1. 检查摄像头日志中的连接错误：
   ```bash
   journalctl -u mibee-nvr -f
   ```
2. 手动测试摄像头流：
   ```bash
   # 查看 RTSP 流
   ffplay -rtsp_transport tcp "rtsp://用户名:密码@192.168.1.100:554/stream"
   
   # 测试 HTTP JPEG
   curl -o test.jpg "http://用户名:密码@192.168.1.100/capture"
   file test.jpg
   ```
3. 尝试为有问题的摄像头调整片段时长：
   ```yaml
   storage:
     segment_duration: "15s"  # 不稳定摄像头使用更短片段
   ```

#### 摄像头显示"已禁用"状态
**症状**: 摄像头显示 `"enabled": true` 但状态是 `"disabled"`

**解决方案**:
1. 检查配置验证错误：
   ```bash
   ./mibee-nvr -config mibee-nvr.yaml --validate
   ```
2. 验证所有必需的摄像头字段是否存在：
   ```yaml
   cameras:
     - id: "cam1"
       name: "摄像头 1"
       protocol: "rtsp"
       encoding: "h264"
       url: "rtsp://..."  # rtsp/http 需要此字段
       enabled: true
   ```
3. 检查重复的摄像头 ID：
   ```bash
   grep -r "id:" mibee-nvr.yaml
   ```

### ONVIF 摄像头问题

#### ONVIF 发现失败
**症状**: 无法发现 ONVIF 摄像头或 `"ONVIF not camera"` 错误

**解决方案**:
1. 手动测试 ONVIF 发现：
   ```bash
   onvif-discover
   ```
2. 验证摄像头支持 ONVIF：
   - 查看摄像头制造商文档
   - 确保摄像头上启用了 ONVIF 服务
3. 尝试不同的发现方法：
   ```yaml
   # 使用特定 IP 范围
   onvif:
     discover:
       timeout: 10
       target: "192.168.1.0/24"
   ```

#### ONVIF 配置文件问题
**症状**: `"ONVIF no profiles"` 或 PTZ 控制不工作

**解决方案**:
1. 获取可用配置文件：
   ```bash
   curl -u admin:password http://localhost:9090/api/cameras/cam-id/onvif/profiles
   ```
2. 手动指定配置文件令牌：
   ```yaml
   cameras:
     - id: "onvif-cam"
       protocol: "onvif"
       profile_token: "profile_1"  # 使用特定配置文件
       stream_encoding: "H264"
   ```

### 小米摄像头问题

#### 小米身份验证失败
**症状**: `"xiaomi authentication failed"` 错误

**解决方案**:
1. 手动测试小米身份验证：
   ```bash
   curl -X POST http://localhost:9090/api/xiaomi/auth \
     -H "Content-Type: application/json" \
     -d '{"username": "your-email@example.com", "password": "your-password"}'
   ```
2. 验证小米账户：
   - 检查账户是否有小米设备
   - 确保账户在正确的区域
   - 尝试重新认证
3. 检查小米设备状态：
   ```bash
   curl -u admin:password http://localhost:9090/api/xiaomi/devices
   ```

#### 小米设备未找到
**症状**: 小米摄像头显示 `"online": false` 或无法连接

**解决方案**:
1. 验证小米设备 ID：
   ```bash
   # 列出设备以找到正确的 DID
   curl -u admin:password http://localhost:9090/api/xiaomi/devices
   ```
2. 检查设备兼容性：
   - 仅支持小米 CS2 摄像头
   - 验证设备型号是否支持
3. 尝试手动同步：
   ```bash
   curl -X POST -u admin:password http://localhost:9090/api/xiaomi/sync
   ```

#HB|## 实时预览问题
#SY|
#XW|### 仪表板或实时预览显示无限加载
#SY|
#RS|**症状**: 仪表板摄像头网格或实时预览页面一直显示"缓冲中"或"加载中..."。视频从未开始播放。
#PR|
#SY|**根本原因**: HLS.js 1.6+ 默认使用 `fetch` API 而不是 XHR。如果未将身份验证头注入到 fetch 请求中，服务器将返回 401 未授权，流无法加载。
#PR|
#SY|**解决方案**:
#HB|1. 确保您运行的是 MiBee NVR v0.6.0 或更高版本，包含 `fetchSetup` 身份验证修复
#RP|2. 检查浏览器控制台（F12）查看 `.m3u8` 或 `.ts` 请求的 401 错误
#WW|3. 在尝试实时预览之前，验证摄像头正在录制（状态 = "录制中"）
#SR|4. 对于"重新连接"状态的摄像头，等待摄像头重新连接 — HLS 需要活动的录制流
#SY|
#RM|### 流显示"SPS/PPS 不可用"错误 (503)
#SY|
#RS|**症状**: HLS 端点返回 HTTP 503，消息为"SPS/PPS not available yet"
#PR|
#SY|**解决方案**:
#HB|1. 摄像头开始录制后的前几秒是正常的 — 视频编码器需要生成关键帧数据
#RP|2. 前端会自动以指数退避重试（5s、10s、20s、40s）
#WW|3. 如果错误持续超过 60 秒，请检查：
#SR|   - 摄像头实际正在传输视频（使用 `ffprobe` 测试）
#XW|   - 录制处于活动状态（通过 API 检查摄像头状态）
#SY|   ```bash
#HB|   curl -u admin:password http://localhost:9090/api/cameras/{id}/stream/index.m3u8
#WW|   ```
#SY|
#HB|### 实时预览可播放但仪表板不可播放
#SY|
#RS|**症状**: 单个摄像头实时预览正常，但仪表板网格显示所有摄像头卡在"缓冲中"
#PR|
#SY|**解决方案**:
#HB|1. 仪表板同时加载多个流 — 检查 `hls.max_streams` 设置（默认：4）
#RP|2. 减少仪表板摄像头数量（最多 4 个，在 RPi 上越少越好）
#WW|3. 使用子流 URL 减少仪表板带宽：
#SR|   ```yaml
#HB|   cameras:
#XW|     - id: "cam1"
#SY|       sub_stream_url: "rtsp://192.168.1.100:554/sub"  # 较低分辨率流
#WW|       hls_max_fps: 10  # 限制仪表板帧率
#SR|   ```
#WW|4. 在低功耗设备（RPi 3B）上，限制仪表板最多 2 个 HLS 流
#SY|
#RM|### 达到最大并发流数
#SY|
#RS|**症状**: 流启动失败，错误为"maximum concurrent HLS streams reached"
#PR|
#SY|**解决方案**:
#HB|1. 关闭未使用的仪表板或实时预览标签页
#RP|2. 如果您的硬件能够处理，增加 `max_streams`：
#WW|   ```yaml
#SR|   hls:
#HB|     max_streams: 6  # 从默认 4 增加到 6
#WW|   ```
#SR|3. 对某些摄像头使用快照缩略图而不是实时流：
#SY|   ```yaml
#HB|   cameras:
#XW|     - id: "cam-low-priority"
#SY|       snapshot_url: "http://192.168.1.100/snapshot"
#WW|   ```
#PR|
#JB|
#NX|## 录制问题
#SY|
## 录制问题

### 未创建录像

#### 无磁盘空间
**症状**: 录像目录为空但摄像头处于活动状态

**解决方案**:
1. 检查磁盘空间：
   ```bash
   df -h
   du -sh /var/lib/mibee-nvr/
   ```
2. 清理磁盘空间：
   ```bash
   # 查找并删除旧录像
   find /var/lib/mibee-nvr/recordings/ -type f -mtime +30 -delete
   
   # 清理快照
   find /var/lib/mibee-nvr/snapshots/ -type f -mtime +7 -delete
   ```
3. 降低磁盘阈值：
   ```yaml
   cleanup:
     disk_threshold_percent: 90  # 从 95 降低
   ```

#### 权限问题
**症状**: 录像未写入磁盘

**解决方案**:
1. 检查目录权限：
   ```bash
   ls -la /var/lib/mibee-nvr/
   ls -la /var/lib/mibee-nvr/recordings/
   ```
2. 修复权限：
   ```bash
   sudo chown -R nvr:nvr /var/lib/mibee-nvr/
   sudo chmod -R 755 /var/lib/mibee-nvr/
   ```
3. 检查磁盘是否正确挂载：
   ```bash
   mount | grep mibee-nvr
   df -h /var/lib/mibee-nvr/
   ```

### 录像损坏

#### MP4 文件无法播放
**症状**: 录像已创建但无法用媒体播放器播放

**解决方案**:
1. 检查文件完整性：
   ```bash
   file /var/lib/mibee-nvr/recordings/h264/cam_1704123456789012345.mp4
   ffprobe -v quiet -show_format -show_streams /var/lib/mibee-nvr/recordings/h264/cam_1704123456789012345.mp4
   ```
2. 调整片段时长：
   ```yaml
   storage:
     segment_duration: "30s"  # 使用标准时长
   ```
3. 检查片段合并问题：
   ```bash
   # 检查合并状态
   curl -u admin:password http://localhost:9090/api/merge/status
   
   # 检查待处理片段
   curl -u admin:password http://localhost:9090/api/merge/pending
   ```

### 内存使用过高

#### 摄像头消耗太多 RAM
**症状**: 系统无响应或 OOM 激活器激活

**解决方案**:
1. 检查内存使用情况：
   ```bash
   free -h
   ps aux | grep mibee-nvr
   ```
2. 减少片段时长：
   ```yaml
   storage:
     segment_duration: "15s"  # 较短片段 = 较少 RAM 使用
   ```
3. 为实时预览启用子流：
   ```yaml
   cameras:
     - id: "cam1"
       sub_stream_url: "rtsp://192.168.1.100:554/low"  # 较低带宽流
   ```
4. 限制 MJPEG 摄像头的帧率：
   ```yaml
   cameras:
     - id: "mjpeg-cam"
       sample_interval: 2  # 每 2 秒采样一次
       hls_max_fps: 15      # 限制为 15 FPS
   ```

## 网络问题

### 端口冲突

#### 端口已被占用
**症状**: 无法启动服务器，端口已被绑定

**解决方案**:
1. 检查哪个进程在使用该端口：
   ```bash
   sudo netstat -tulpn | grep :9090
   sudo lsof -i :9090
   ```
2. 更改配置中的端口：
   ```yaml
   server:
     listen: ":8080"  # 使用不同端口
   ```
3. 终止冲突进程：
   ```bash
   sudo kill -9 <PID>
   ```

### 防火墙问题

#### 无法访问 Web UI
**症状**: 外部连接到 Web UI 失败

**解决方案**:
1. 检查防火墙状态：
   ```bash
   sudo ufw status
   sudo iptables -L -n
   ```
2. 开放所需端口：
   ```bash
   # 对于 Ubuntu/Debian
   sudo ufw allow 9090/tcp
   sudo ufw allow 2121/tcp  # FTP
   sudo ufw allow 5005/tcp  # WebDAV（如果启用）
   
   # 对于 CentOS/RHEL
   sudo firewall-cmd --permanent --add-port=9090/tcp
   sudo firewall-cmd --reload
   ```
3. 检查反向代理配置（如果使用）：
   ```nginx
   # Caddy 示例
   reverse_proxy localhost:9090
   ```

## 性能问题

### CPU 使用率过高

#### 摄像头太多
**症状**: 高 CPU 使用率影响系统性能

**解决方案**:
1. 监控 CPU 使用率：
   ```bash
   top -p $(pgrep mibee-nvr)
   htop -p $(pgrep mibee-nvr)
   ```
2. 减少并发摄像头处理：
   - 禁用不必要的摄像头
   - 为实时查看使用子流
   - 增加 MJPEG 摄像头的采样间隔
3. 优化片段合并：
   ```yaml
   merge:
     batch_limit: 100  # 从 200 减少
     check_interval: "2h"  # 较少检查频率
   ```

#### 并发流太多
**症状**: 实时查看期间 CPU 过高

**解决方案**:
1. 限制 HLS 流：
   ```yaml
   hls:
     max_streams: 2  # 从 4 减少
   ```
2. 使用快照缩略图而不是实时流：
   ```yaml
   cameras:
     - id: "cam1"
       snapshot_url: "http://192.168.1.100/snapshot"  # 为缩略图使用快照
   ```

### 网络使用过高

#### 带宽饱和
**症状**: 网络接口饱和，影响其他服务

**解决方案**:
1. 监控网络使用情况：
   ```bash
   iftop -i eth0 -t
   nethogs
   ```
2. 优化摄像头流：
   ```yaml
   cameras:
     - id: "cam1"
       sub_stream_url: "rtsp://192.168.1.100:554/sub"  # 较低带宽子流
       hls_max_fps: 15  # 限制帧率
   ```
3. 启用快照缓存：
   ```yaml
   cameras:
     - id: "cam1"
       snapshot_url: "http://192.168.1.100/snapshot"  # 快照使用较少带宽
   ```

## Docker 问题

### 容器无法启动
**症状**: Docker 容器立即退出

**解决方案**:
1. 检查容器日志：
   ```bash
   docker compose logs mibee-nvr
   docker logs mibee-nvr-container-id
   ```
2. 验证配置文件已挂载：
   ```yaml
   # docker-compose.yml
   volumes:
     - ./mibee-nvr.yaml:/mibee-nvr.yaml:ro
   ```
3. 检查容器内的文件权限：
   ```bash
   docker exec -it mibee-nvr-container ls -la /mibee-nvr.yaml
   ```

### 卷权限问题
**症状**: 无法将录像写入挂载的卷

**解决方案**:
1. 设置正确的所有权：
   ```bash
   sudo chown -R 1000:1000 ./data  # mibee-nvr 以 UID 1000 运行
   ```
2. 在 Docker 中使用正确的用户：
   ```yaml
   # docker-compose.yml
   user: "1000:1000"
   volumes:
     - ./data:/var/lib/mibee-nvr
   ```

## 错误消息和解决方案

### 常见错误代码

| 错误代码 | 描述 | 解决方案 |
|----------|------|----------|
| `CAMERA_NOT_FOUND` | 摄像头 ID 不存在 | 检查摄像头 ID 拼写，验证摄像头在配置中存在 |
| `CAMERA_ALREADY_EXISTS` | 摄像头 ID 已使用 | 选择唯一的摄像头 ID |
| `RECORDING_NOT_FOUND` | 录像文件丢失 | 检查存储目录，验证文件存在 |
| `STORAGE_FULL` | 磁盘空间已满 | 清理录像，增加磁盘空间，降低保留期 |
| `AUTH_REQUIRED` | 需要身份验证 | 为请求添加有效凭据 |
| `AUTH_FAILED` | 无效凭据 | 检查用户名/密码，验证哈希生成 |
| `INVALID_INPUT` | 无效参数 | 检查 API 请求格式，验证配置 |
| `PATH_TRAVERSAL` | 安全违规 | 修复文件路径，删除可疑字符 |
| `HLS_MAX_STREAMS` | 并发流太多 | 减少并发观看者，增加 `max_streams` |
| `ONVIF_CONNECTION_FAILED` | 无法连接到 ONVIF 设备 | 检查网络，验证 ONVIF 服务正在运行 |

### 日志分析

#### 调试模式
启用调试日志进行详细故障排除：
```yaml
observability:
  log_level: "debug"
```

#### 日志位置
**Systemd 服务**:
```bash
journalctl -u mibee-nvr -f
```

**Docker 容器**:
```bash
docker logs -f mibee-nvr-container
```

**二进制文件直接运行**:
```bash
./mibee-nvr -config mibee-nvr.yaml 2>&1 | tee mibee-nvr.log
```

#### 常见日志模式

**摄像头连接问题**:
```
WARN: camera connection failed: rtsp://...: connection refused
WARN: camera authentication failed for camera_id
ERROR: camera stream error: read timeout
```

**存储问题**:
```
WARN: storage directory not writable: /var/lib/mibee-nvr
ERROR: cannot write recording file: no space left on device
```

**配置问题**:
```
ERROR: validation failed: camera[].url has invalid format
ERROR: validation failed: cleanup.retention_days must be between 1 and 3650
```

## 性能优化

### 针对树莓派 3B
```yaml
# 针对 RPi 3B 约束优化
storage:
  segment_duration: "15s"  # 较短片段 = 较少 RAM
hls:
  max_streams: 2          # RPi 限制：最多 4，但 2 更安全
  segment_count: 5        # 较少片段 = 较少 I/O
cleanup:
  check_interval: "30m"   # 较少检查频率
  retention_days: 7        # 较短保留期
merge:
  enabled: false          # 在 RPi 3B 上禁用合并
```

### 针对高性能系统
```yaml
# 针对性能优化
storage:
  segment_duration: "60s"  # 较长片段 = 较少文件
hls:
  max_streams: 10          # 允许多并发流
  segment_count: 10        # 更多片段用于更流畅播放
merge:
  enabled: true
  batch_limit: 500        # 更大批量以提高效率
cleanup:
  check_interval: "15m"    # 更频繁清理
  retention_days: 90       # 较长保留期
```

## 获取帮助

### 报告问题前
1. 查阅本故障排除指南
2. 查看 [配置参考](configuration.md)
3. 搜索现有 GitHub 问题
4. 检查日志中的错误消息

### 创建错误报告
创建 GitHub issue 时，包含：

1. **系统信息**:
   ```bash
   uname -a
   lsb_release -a
   ```

2. **MiBee NVR 版本**:
   ```bash
   ./mibee-nvr --version
   ```

3. **配置**（删除敏感数据）:
   ```bash
   grep -v password mibee-nvr.yaml
   ```

4. **日志**（最后 50 行）:
   ```bash
   journalctl -u mibee-nvr --since "1 hour ago"
   ```

5. **重现步骤**:
   - 您尝试做什么
   - 实际发生了什么
   - 预期行为

### 社区支持
- 加入我们的 Discord 社区获取实时帮助
- 查看 wiki 获取其他指南
- 查看已关闭的问题以查找类似问题

## 紧急程序

### 系统无响应
1. 停止服务：
   ```bash
   sudo systemctl stop mibee-nvr
   ```
2. 终止任何剩余进程：
   ```bash
   sudo pkill -f mibee-nvr
   ```
3. 检查系统资源：
   ```bash
   free -h
   df -h
   top
   ```
4. 使用减少的配置重新启动：
   ```bash
   # 使用最小配置
   cp mibee-nvr.yaml mibee-nvr.yaml.backup
   # 编辑以仅启用基本摄像头
   sudo systemctl start mibee-nvr
   ```

### 配置损坏
1. 从备份恢复：
   ```bash
   cp mibee-nvr.yaml.backup mibee-nvr.yaml
   ```
2. 或创建最小配置：
   ```yaml
   server:
     listen: ":9090"
   storage:
     root_dir: "/var/lib/mibee-nvr"
     segment_duration: "30s"
   auth:
     username: "admin"
     password: "临时密码"
   ```
3. 重新启动服务并重新配置

### 数据库损坏
1. 备份数据库：
   ```bash
   cp /var/lib/mibee-nvr/mibee-nvr.db /var/lib/mibee-nvr/mibee-nvr.db.backup
   ```
2. 删除损坏的数据库：
   ```bash
   rm /var/lib/mibee-nvr/mibee-nvr.db
   ```
3. 重新启动服务（数据库将被重新创建）：
   ```bash
   sudo systemctl restart mibee-nvr
   ```
4. 重新配置所有摄像头