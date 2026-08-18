# 初始化向导

> 适用于 MiBeeNvr v0.11.0

首次启动 MiBee NVR 且尚未设置管理员密码时，Web 界面会自动进入**单页初始化向导**，一次完成账号、语言与存储位置配置。

![初始化向导](images/wizard.webp)

## 何时会出现向导

满足以下任一条件时，浏览器打开 Web 界面会直接进入「初始化设置」页：

- 用 Docker 启动且没有挂载已有配置文件（无配置自动初始化）
- 删除了 `mibee-nvr.yaml` 后重启服务

如果已经通过 `mibee-nvr init --password` 或配置文件设置了密码，则跳过向导，直接进入登录页。

## 向导内容

### 1. 管理员账户

- **用户名**：默认 `admin`，可修改
- **密码**：至少 8 个字符
- **确认密码**：再次输入密码

> 密码以哈希形式安全存储，无法找回，请妥善保管。

### 2. 浏览器能力检测

向导会实时检测当前浏览器对四种播放协议的支持情况，并给出**推荐流媒体协议**：

| 检测项 | 说明 |
|--------|------|
| LL-HLS | 低延迟 HLS（WebCodecs） |
| WebRTC | 超低延迟实时播放 |
| HTTP-FLV | HTTP-FLV 流播放 |
| HLS | 兼容性最好的 HLS 播放 |

全部支持时推荐 LL-HLS；不支持 WebCodecs 的旧浏览器会自动回退到 HLS。此检测仅影响默认播放器选择，不影响录制。

### 3. 界面语言

选择 Web 界面语言（**中文 / English**）。该选择会保存在浏览器本地，之后可随时通过页面右上角的语言下拉框切换。

### 4. 存储路径

指定录像文件的存储目录：

- **留空**（推荐）：沿用服务端当前配置（`server.data_dir`）
- **填写路径**：例如外部硬盘挂载点 `/mnt/storage/nvr`，需确保目录存在且有写入权限

### 5. 完成设置

点击「完成设置」，向导会写入管理员凭据并自动登录，进入 Web 界面。此时系统还没有摄像头，可以：

- 在「摄像头」页点击「扫描设备」自动发现 ONVIF / 小米摄像头
- 或点击「+ 添加摄像头」手动添加（参阅 [快速开始](quickstart.md)）

## 重置向导

如需重新运行向导，删除配置文件并重启即可：

```bash
rm mibee-nvr.yaml
# 重启 MiBee NVR，向导会自动出现
```

> ⚠️ 删除配置文件会丢失摄像头列表等配置（录像数据保存在数据目录，不受影响）。

## 高级配置

向导完成后，可以通过 Web UI 或编辑配置文件进行高级配置：

### 录制设置

在配置文件中添加：

```yaml
recording:
  segment_duration: "1m"        # 录像片段时长
  max_days: 30                  # 录像保留天数
  storage_path: "/data"         # 录像存储路径
```

### 推流接入

启用 SRT / RTMP 推流接入：

```yaml
srt:
  enabled: true
  port: 9000

rtmp:
  enabled: true
  port: 1935
```

### MQTT 集成

连接 MQTT 事件总线：

```yaml
mqtt:
  enabled: true
  broker: "mqtt://localhost:1883"
  topic: "mibee/nvr"
```

### 延时摄影

为摄像头启用延时摄影：

```yaml
cameras:
  - id: "construction"
    name: "施工现场"
    protocol: "timelapse"
    enabled: true
    timelapse:
      enabled: true
      merge_duration: "natural-day"
      output_fps: 30
```

## 配置文件结构

完整的配置文件示例：

```yaml
# MiBee NVR 配置文件
server:
  listen: ":9090"
  data_dir: "/var/lib/mibee-nvr"

auth:
  username: "admin"
  password: "your-password"

recording:
  segment_duration: "1m"
  max_days: 30

srt:
  enabled: false
  port: 9000

rtmp:
  enabled: false
  port: 1935

mqtt:
  enabled: false

webdav:
  enabled: true
  read_write: false

ftp:
  enabled: true
  port: 2121

cameras:
  - id: "front-door"
    name: "前门"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:pass@192.168.1.100:554/stream"
    enabled: true
```

## 下一步

- [快速开始](quickstart.md) — 添加第一台摄像头
- [Docker 部署](install-docker.md) — 生产环境容器化部署
- [NAS 部署](install-nas.md) — NAS 系统安装
- [ONVIF 自动发现](onvif-discovery.md) — 摄像头自动发现
