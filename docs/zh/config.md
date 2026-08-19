# 配置参考

> 适用于 MiBeeNvr v0.11.0 · 配置文件默认为 `mibee-nvr.yaml`（可用 `-config` 指定）

MiBee NVR 的全部行为由一个 YAML 文件驱动。本页是**顶层键速查**；每个键的完整字段说明见仓库中的[完整配置参考](https://github.com/Mi-Bee-Studio/MiBeeNvr/blob/main/docs/zh/configuration.md)。

## 修改方式

配置有**两个入口**，Web UI 是推荐方式（改完即存盘、少数字段需重启生效）：

| 入口 | 适用 | 说明 |
|------|------|------|
| Web UI → 设置 | 大部分运行参数 | 存储路径、直播、GB28181、AI 检测、录像与处理等分页 |
| 编辑 YAML | 批量 / 初始化 | 摄像头列表、部署脚本等；改完需重启 |

> 用 `mibee-nvr encrypt-config` 可把配置里的明文密码就地加密（见 [CLI 手册](cli.md#encrypt-config-加密敏感字段)）。

## 顶层键速查

```yaml
server:
  listen: ":9090"            # HTTP 监听地址（Web/API/直播）

storage:
  root_dir: "/var/lib/mibee-nvr"  # 存储根目录：录像 + 数据库
  segment_duration: "30s"          # MP4 片段时长（树莓派建议 ≤30s）

auth:
  username: "admin"
  password_hash: ""          # mibee-nvr hash-password <密码> 生成

cameras: []                  # 摄像头列表（推荐 Web UI 维护，见下）

cleanup:
  retention_days: 30         # 录像保留天数（1–3650），到期自动清理

merge:
  enabled: false             # 周期性片段合并（8h/24h/7d/30d 产物）

ftp:
  enabled: true              # FTP 访问录像（默认端口 2121）

mqtt:
  enabled: false             # MQTT 事件集成，见 MQTT 集成文档

webdav:
  enabled: true              # WebDAV 访问（/dav）

hls:
  write_buffer_size: 40      # 每路 HLS 异步写缓冲帧数

observability:
  log_level: "info"          # debug / info / warn / error

security:
  frame_ancestors: ""        # CSP frame-ancestors（跨域嵌入 Web UI 时配置）
```

## cameras 条目结构

每路摄像头一个条目 —— **协议与编码是两个独立字段**：

```yaml
cameras:
  - id: "front-door"             # 稳定唯一 ID（用于目录名和 API）
    name: "前门"                  # 显示名
    protocol: "rtsp"             # rtsp | http | onvif | xiaomi | srt | rtmp | gb28181 | timelapse
    encoding: "h264"             # h264 | h265 | mjpeg | jpeg（ONVIF 可省略自动检测）
    url: "rtsp://192.168.1.100:554/stream"
    username: "admin"            # 摄像头凭据（按协议可选）
    password: "camera123"
    enabled: true
    recording_enabled: true      # false = 仅直播不落盘
```

各接入协议的完整写法见对应文档：[ONVIF](onvif-discovery.md) · [小米](xiaomi-setup.md) · [SRT/RTMP](srt-rtmp.md) · [树莓派](raspberrypi.md) · [GB28181](gb28181-guide.md)。

## 常见组合示例

### 外部硬盘存储

```yaml
storage:
  root_dir: "/mnt/external/nvr"
  segment_duration: "30s"
```

### Docker 环境变量覆盖

配置值可被环境变量覆盖（容器部署常用）：

```yaml
# docker-compose.yml
environment:
  - NVR_PASSWORD=initial-password   # 首启设置管理员密码
  - NVR_LISTEN_PORT=9090
  - NVR_DATA_DIR=/data
  - NVR_UID=1000
  - NVR_GID=1000
```

## 环境变量 / 端口速查

| 项 | 默认 | 说明 |
|----|------|------|
| HTTP（Web/API/HLS） | 9090 | `server.listen` / `NVR_LISTEN_PORT` |
| FTP | 2121（被动 2122–2140） | `ftp.port` |
| SRT 推流 | 9000/udp | `srt.port` |
| RTMP 推流 | 1935 | `rtmp.port` |
| GB28181 SIP | 5060/udp | `gb28181.sip_listen` |

## 下一步

- [CLI 手册](cli.md) — init / hash-password / encrypt-config 等命令
- [Docker 部署](install-docker.md) — 容器化部署与卷挂载
- [完整配置参考（GitHub）](https://github.com/Mi-Bee-Studio/MiBeeNvr/blob/main/docs/zh/configuration.md)
