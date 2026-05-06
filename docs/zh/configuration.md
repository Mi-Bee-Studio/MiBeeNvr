# MiBee NVR 配置参考文档

## 配置文件结构

MiBee NVR 使用 YAML 格式的配置文件，文件名为 `config.yaml`。配置文件包含多个章节，每个章节负责不同的功能模块。

### 基本格式

```yaml
# 注释以 # 开头
server:
  listen: ":9090"
storage:
  root_dir: "/mnt/data/nvr"
```

## 完整配置参考

### server 服务器配置

服务器配置控制 MiBee NVR 的网络监听设置。

```yaml
server:
  listen: ":9090"          # 监听地址和端口，格式 "host:port"
  read_timeout: 30s        # HTTP 读取超时时间
  write_timeout: 30s       # HTTP 写入超时时间
  idle_timeout: 60s       # 空闲连接超时时间
  max_upload_size: 100MB   # 文件上传大小限制
```

**默认值**:
- `listen: ":9090"`
- `read_timeout: "30s"`
- `write_timeout: "30s"`  
- `idle_timeout: "60s"`
- `max_upload_size: "100MB"`

### storage 存储配置

存储配置控制录像文件的存储位置和分段设置。

```yaml
storage:
  root_dir: "/mnt/data/nvr"      # 录像存储根目录
  segment_duration: "10m"        # 录像分段时长
  max_segments: 1000             # 每个摄像头的最大分段数
  temp_dir: "/tmp/mibee-nvr"     # 临时文件目录
  cleanup_interval: "1h"        # 清理检查间隔
```

**默认值**:
- `root_dir: "./recordings"`
- `segment_duration: "10m"`
- `max_segments: 1000`
- `temp_dir: "/tmp/mibee-nvr"`
- `cleanup_interval: "1h"`

**重要提示**:
- 低内存设备建议 `segment_duration` 设置为 `"30s"`
- 每个分段在关闭前会保存在内存中，30秒分段约占用 15-20MB 内存
- `root_dir` 必须存在且有写权限

### auth 认证配置

认证配置控制 Web 界面的访问权限。

```yaml
auth:
  username: "admin"              # 用户名
  password_hash: ""             # bcrypt 加密的密码哈希
  session_timeout: "24h"         # 会话超时时间
  enable_https: false            # 是否启用 HTTPS
  cert_file: ""                 # SSL 证书文件路径
  key_file: ""                  # SSL 私钥文件路径
```

**默认值**:
- `username: "admin"`
- `password_hash: ""`
- `session_timeout: "24h"`
- `enable_https: false`
- `cert_file: ""`
- `key_file: ""`

**密码哈希**:

使用 bcrypt 生成密码哈希（注意：此命令尚未实现）：

```bash
# 生成密码哈希（此命令尚未在版本中实现）
mibee-nvr hash-password your-password
```

或者使用其他工具生成：

```bash
echo "your-password" | htpasswd -n -B admin
```

### cameras 摄像头配置

摄像头配置是系统的核心，定义了所有连接的摄像头。

#### 基本摄像头配置

```yaml
cameras:
  - id: "cam1"                  # 摄像头唯一标识符
    name: "前门摄像头"          # 显示名称
    protocol: "rtsp_h264"       # 协议类型
    url: "rtsp://192.168.1.100:554/h264/main"
    enabled: true               # 是否启用
    recording: true             # 是否录制
    username: "admin"           # 认证用户名（可选）
    password: "password123"     # 认证密码（可选）
    segment_prefix: "cam1"      # 分段文件前缀（可选，默认为 id）
```

#### 支持的协议类型

1. **RTSP H.264** (`rtsp_h264`)
   ```yaml
   - id: "cam1"
     name: "前门摄像头"
     protocol: "rtsp_h264"
     url: "rtsp://192.168.1.100:554/h264/main"
     enabled: true
   ```

2. **RTSP H.265/HEVC** (`rtsp_h265`)
   ```yaml
   - id: "cam1"
     name: "H.265 摄像头"
     protocol: "rtsp_h265"
     url: "rtsp://192.168.1.100:554/h265/main"
     username: "admin"
     password: "password123"
     enabled: true
   ```
   注意：H.265/HEVC 提供比 H.264 更好的压缩率，但需要更多的 CPU 处理能力。

3. **RTSP MJPEG** (`rtsp_mjpeg`)
   ```yaml
   - id: "cam2"
     name: "后院摄像头"
     protocol: "rtsp_mjpeg"
     url: "rtsp://192.168.1.101:554/mjpeg"
     enabled: true
   ```

4. **HTTP JPEG** (`http_jpeg`)
   ```yaml
   - id: "cam3"
     name: "室内摄像头"
     protocol: "http_jpeg"
     url: "http://192.168.1.102:8080/snapshot"
     enabled: true
   ```

#### 完整摄像头配置示例

```yaml
cameras:
  - id: "front-door"
    name: "前门"
    protocol: "rtsp_h264"
    url: "rtsp://192.168.1.100:554/stream"
    username: "admin"
    password: "password123"
    enabled: true
    
  - id: "back-yard"
    name: "后院"
    protocol: "rtsp_mjpeg"
    url: "rtsp://192.168.1.101:554/live"
    enabled: true
    
  - id: "garage"
    name: "车库"
    protocol: "http_jpeg"
    url: "http://192.168.1.102/capture"
    username: "admin"
    password: "garage123"
    enabled: true
    
  - id: "h265-camera"
    name: "H.265 摄像头"
    protocol: "rtsp_h265"
    url: "rtsp://192.168.1.103:554/stream"
    username: "admin"
    password: "password123"
    enabled: true
```

### cleanup 清理配置

清理配置控制录像文件的自动清理策略。

```yaml
cleanup:
  retention_days: 30            # 录像保留天数（0=未配置，默认30天）
  check_interval: "1h"         # 清理检查间隔
  disk_threshold_percent: 95    # 磁盘使用率阈值（百分比）
```

**默认值**:
- `retention_days: 30`
- `check_interval: "1h"
- `disk_threshold_percent: 95`

**重要提示**:
- `retention_days: 0` 会被视为"未配置"，系统会使用默认值 30 天
- **按摄像头保留天数**：每个摄像头可以通过 Web 界面或 API 设置自己的 `retention_days` 来覆盖全局设置
- 清理策略包括：按时间清理和磁盘空间清理两种方式

### FTP 服务器配置

FTP 服务器配置控制 FTP 访问功能。

```yaml
ftp:
  enabled: true                 # 是否启用 FTP 服务器
  port: 2121                   # FTP 端口
  passive_port_range: "2122-2140"  # 被动模式端口范围
  max_connections: 10          # 最大连接数
  timeout: "30s"               # 连接超时时间
```

**默认值**:
- `enabled: true`
- `port: 2121`
- `passive_port_range: "2122-2140"`
- `max_connections: 10`
- `timeout: "30s"`

**匿名访问**: FTP 服务器拒绝所有匿名访问，必须使用配置文件中的认证凭据。

### MQTT 客户端配置

MQTT 客户端配置支持远程触发和通知功能。

```yaml
mqtt:
  enabled: false                # 是否启用 MQTT
  broker: "tcp://localhost:1883" # MQTT 服务器地址
  topic: "mibeenr/trigger"     # 触发主题
  client_id: "mibee-nvr"       # 客户端 ID
  username: ""                 # MQTT 用户名（可选）
  password: ""                 # MQTT 密码（可选）
  qos: 1                       # QoS 级别（0,1,2）
  retain: false                # 是否保留消息
```

**默认值**:
- `enabled: false`
- `broker: "tcp://localhost:1883"`
- `topic: "mibeenr/trigger"`
- `client_id: "mibee-nvr"`
- `username: ""`
- `password: ""`
- `qos: 1`
- `retain: false`

### WebDAV 服务器配置

WebDAV 服务器配置提供只读文件访问功能。

```yaml
webdav:
  enabled: true                 # 是否启用 WebDAV
  path_prefix: "/dav"         # WebDAV 路径前缀
  read_write: false             # 是否启用读写模式（默认只读）
```

**默认值**:
- `enabled: true`
- `path_prefix: "/dav"`
- `read_write: false`

**重要提示**:
- WebDAV 服务器默认为**只读**模式，所有写入操作都会返回 403 状态码
- 启用 `read_write: true` 后，可以通过 WebDAV PUT 请求自动注册新摄像头
- 访问路径格式：`http://server:9090/dav/recordings/`
- 启用读写模式前请考虑安全影响

## 配置文件示例

### 完整配置示例

```yaml
# MiBee NVR 完整配置文件示例

server:
  listen: ":9090"
  read_timeout: "30s"
  write_timeout: "30s"
  idle_timeout: "60s"
  max_upload_size: "100MB"

storage:
  root_dir: "/mnt/data/nvr"
  segment_duration: "10m"
  max_segments: 1000
  temp_dir: "/tmp/mibee-nvr"
  cleanup_interval: "1h"

auth:
  username: "admin"
  password_hash: "$2a$10$N9qo8uLOickgx2ZMRZoMy..."
  session_timeout: "24h"
  enable_https: false
  cert_file: ""
  key_file: ""

cameras:
  - id: "front-door"
    name: "前门"
    protocol: "rtsp_h264"
    url: "rtsp://192.168.1.100:554/stream"
    username: "admin"
    password: "password123"
    enabled: true
    recording: true
    segment_prefix: "front_door"
    
  - id: "back-yard"
    name: "后院"
    protocol: "rtsp_mjpeg"
    url: "rtsp://192.168.1.101:554/live"
    enabled: true
    recording: true
    
  - id: "garage"
    name: "车库"
    protocol: "http_jpeg"
    url: "http://192.168.1.102/capture"
    username: "admin"
    password: "garage123"
    enabled: true
    recording: true

cleanup:
  retention_days: 30
  check_interval: "1h"
  disk_threshold_percent: 95
  max_files_per_camera: 10000
  delete_interval: "6h"

ftp:
  enabled: true
  port: 2121
  passive_port_range: "2122-2140"
  max_connections: 10
  timeout: "30s"

mqtt:
  enabled: false
  broker: "tcp://localhost:1883"
  topic: "mibeenr/trigger"
  client_id: "mibee-nvr"
  username: ""
  password: ""
  qos: 1
  retain: false

webdav:
  enabled: true
  path_prefix: "/dav"
  max_upload_size: "1GB"
  cache_control: "max-age=3600"
```

### 最小配置示例

```yaml
server:
  listen: ":9090"

storage:
  root_dir: "/mnt/data/nvr"

auth:
  username: "admin"
  password_hash: "$2a$10$N9qo8uLOickgx2ZMRZoMy..."

cameras:
  - id: "cam1"
    name: "摄像头1"
    protocol: "http_jpeg"
    url: "http://192.168.1.100/capture"
    enabled: true
    recording: true
```

## 配置验证

启动时 MiBee NVR 会验证配置文件：

```bash
# 检查配置文件语法
./mibee-nvr --config config.yaml --validate

# 启动并显示配置
./mibee-nvr --config config.yaml --dry-run
```

## 配置热重载

修改配置文件后，可以通过以下方式重载：

```bash
# 向进程发送 SIGHUP 信号
kill -HUP $(pgrep mibee-nvr)
```

## 配置文件权限

确保配置文件权限适当：

```bash
chmod 600 config.yaml  # 仅所有者可读写
chown mibee:nvr config.yaml  # 设置合适的所有权
```

## 故障排除

### 常见配置错误

1. **YAML 语法错误**
   - 检查缩进（使用空格，不要用制表符）
   - 检查冒号和引号的使用

2. **端口被占用**
   ```bash
   netstat -tlnp | grep :9090
   ```

3. **存储权限问题**
   ```bash
   ls -la /mnt/data/nvr
   touch /mnt/data/nvr/test.txt
   ```

4. **摄像头连接失败**
   - 使用 `ffmpeg` 测试连接：
   ```bash
   ffmpeg -rtsp_transport tcp -i "rtsp://192.168.1.100:554/stream" -t 5 -f null -
   ```

### 日志配置

配置文件中可以通过 `logging` 部分控制日志输出：

```yaml
logging:
  level: "info"                 # 日志级别：debug, info, warn, error
  file: "/var/log/mibee-nvr.log"  # 日志文件路径
  max_size: "100MB"            # 最大文件大小
  max_backups: 5              # 最大备份文件数
  compress: true               # 是否压缩旧日志
```

默认日志级别为 `info`，调试时可以设置为 `debug`。