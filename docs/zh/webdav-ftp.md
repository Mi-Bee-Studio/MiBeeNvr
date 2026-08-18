# WebDAV / FTP 存储

> 适用于 MiBeeNvr v0.11.0

MiBee NVR 内置 WebDAV 和 FTP 服务器，支持只读或读写模式访问录像文件。

## 功能对比

| 特性 | WebDAV | FTP |
|------|--------|-----|
| 协议 | HTTP/HTTPS | FTP |
| 加密 | ✅ 支持 TLS | ✅ 支持 FTPS |
| 浏览器访问 | ✅ | ❌ |
| 命令行工具 | ✅ rclone / curl | ✅ ftp / lftp |
| 文件管理器 | ✅ 原生支持 | ✅ 需要 FTP 客户端 |
| 传输效率 | 中等 | 高（二进制模式） |

## WebDAV 配置

### 启用 WebDAV

```yaml
webdav:
  enabled: true
  read_write: false               # false=只读，true=读写
  port: 9090                      # 默认与 HTTP 共享端口
  path: "/dav"                    # WebDAV 路径
```

### 只读模式（默认）

只读模式下，WebDAV 只能浏览和下载录像，无法删除：

```yaml
webdav:
  enabled: true
  read_write: false
```

### 读写模式

启用读写模式后，可以通过 WebDAV 管理录像文件：

```yaml
webdav:
  enabled: true
  read_write: true
```

> **警告**：读写模式允许删除录像文件。请谨慎使用。

### 访问 WebDAV

```bash
# 浏览器访问
# 打开 https://你的NVR地址:9090/dav/

# rclone 挂载
rclone mount nvr:/ /mnt/nvr --vfs-cache-mode full

# curl 下载
curl -u admin:password \
  "http://192.168.1.50:9090/dav/recordings/front-door/2026-08-18/00-00-00.mp4" \
  -o recording.mp4
```

### rclone 配置

```ini
# ~/.config/rclone/rclone.conf
[nvr]
type = webdav
url = http://192.168.1.50:9090/dav/
vendor = other
user = admin
pass = your-password
```

### 文件管理器挂载

| 操作系统 | 方法 |
|----------|------|
| Windows | 映射网络驱动器 → `\\192.168.1.50@9090\dav` |
| macOS | Finder → 前往 → 连接服务器 → `http://192.168.1.50:9090/dav/` |
| Linux | 文件管理器 → 位置 → 其他位置 → `davs://192.168.1.50:9090/dav/` |

## FTP 配置

### 启用 FTP

```yaml
ftp:
  enabled: true
  port: 2121
  read_write: false               # false=只读，true=读写
  max_connections: 10             # 最大并发连接
```

### 只读模式（默认）

```yaml
ftp:
  enabled: true
  port: 2121
  read_write: false
```

### 读写模式

```yaml
ftp:
  enabled: true
  port: 2121
  read_write: true
```

### 访问 FTP

```bash
# 命令行 FTP
ftp 192.168.1.50 2121
# 用户名：admin
# 密码：your-password

# lftp（支持递归下载）
lftp -u admin,password 192.168.1.50:2121
mirror --reverse /recordings/front-door/ ./local-folder/

# WinSCP / FileZilla
# 主机：192.168.1.50
# 端口：2121
# 用户名：admin
# 密码：your-password
```

## 目录结构

WebDAV 和 FTP 访问的目录结构：

```
/
├── recordings/                   # 录像文件
│   ├── front-door/               # 摄像头 ID
│   │   ├── 2026-08-18/           # 日期目录
│   │   │   ├── 00-00-00.mp4     # 录像片段
│   │   │   ├── 00-01-00.mp4
│   │   │   └── ...
│   │   └── 2026-08-17/
│   │       └── ...
│   └── driveway/
│       └── ...
├── snapshots/                    # 快照图片（如果启用）
└── timelapse/                    # 延时摄影视频（如果启用）
```

## 安全配置

### TLS 加密

WebDAV 支持 HTTPS：

```yaml
webdav:
  enabled: true
  tls:
    enabled: true
    cert_file: "/path/to/cert.pem"
    key_file: "/path/to/key.pem"
```

### FTPS 加密

FTP 支持 FTPS（FTP over TLS）：

```yaml
ftp:
  enabled: true
  tls:
    enabled: true
    cert_file: "/path/to/cert.pem"
    key_file: "/path/to/key.pem"
```

### 访问控制

限制特定 IP 访问：

```yaml
webdav:
  enabled: true
  allowed_ips:
    - "192.168.1.0/24"            # 只允许局域网访问
    - "10.0.0.0/8"

ftp:
  enabled: true
  allowed_ips:
    - "192.168.1.0/24"
```

## 常见问题

### WebDAV 无法连接

1. **认证失败**：确认用户名和密码正确
2. **路径错误**：WebDAV 路径通常是 `/dav/`（注意末尾斜杠）
3. **TLS 证书**：如果使用自签名证书，客户端需要信任该证书
4. **防火墙**：确保端口 9090 已开放

### FTP 无法连接

1. **被动模式**：某些防火墙需要 FTP 被动模式
2. **端口**：默认 FTP 端口是 2121（不是 21）
3. **认证失败**：确认用户名和密码正确
4. **防火墙**：确保端口 2121 已开放

### 传输速度慢

- **WebDAV**：HTTP 开销较大，适合小文件
- **FTP**：二进制模式传输效率更高
- **建议**：大文件传输使用 FTP，小文件或浏览使用 WebDAV

### 文件删除后录像消失

- 如果 `read_write: true`，WebDAV 和 FTP 可以删除录像
- 建议保持只读模式（`read_write: false`）
- 如需删除录像，请通过 Web UI 操作

## 性能优化

### 并发连接

```yaml
webdav:
  max_connections: 20

ftp:
  max_connections: 10
```

### 缓存配置

```yaml
webdav:
  cache:
    enabled: true
    max_size: "1G"                 # 缓存大小
    ttl: "5m"                      # 缓存过期时间
```

## 下一步

- [ONVIF 自动发现](onvif-discovery.md) — 摄像头零配置接入
- [SRT / RTMP 推流接入](srt-rtmp.md) — 推流摄像头配置
- [录制与回放](recording-playback.md) — 录像管理
