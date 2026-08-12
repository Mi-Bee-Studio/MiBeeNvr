# MiBee NVR GB/T 28181 指南

本指南介绍 MiBee NVR 的 GB/T 28181（中国视频监控国家标准）集成，包括 SIP 平台配置、设备接入、PTZ 控制、回放和故障排除。

## 什么是 GB/T 28181？

GB/T 28181 是中国视频监控网络系统的国家标准，定义了 IP 摄像机、NVR 和平台通过 SIP 和 RTP/PS 通信的方式。MiBee NVR 实现**平台角色**（UAS — 应答方），这意味着：

- 设备通过 SIP（UDP 端口 5060，默认）**REGISTER** 到 NVR
- 设备发送 **Keepalive** 消息维持在线状态
- NVR **INVITE** 通道拉取 RTP/PS 媒体流（拉流模式）
- NVR 将 MPEG-PS 解复用为 H.264/H.265 NALU，并通过 StreamHub 分发

支持的摄像机品牌包括海康威视、大华、宇视和其他 GB28181 兼容制造商。

## 快速开始

### 步骤 1：启用 GB28181 服务器

打开 `mibee-nvr.yaml` 并添加 GB28181 服务器配置：

```yaml
gb28181:
  enabled: true
  sip_listen: ":5060"
  server_id: "34020000002000000001"
  realm: "3402000000"
  password: "yourpassword"
  port_range: "30000-30050"
  heartbeat_interval: "60s"
  catalog_interval: "30m"
  tcp_mode: false
  tcp_framing: "auto"
  allowed_device_ids: []
```

**关键参数**：
- `server_id`：您的 NVR 的 20 位 GB/T 28181 序列号（格式：`34020000002000000001`）
- `realm`： SIP 摘要认证域（通常是您的 10 位区域码，例如 `3402000000`）
- `password`：SIP 摘要认证密钥（通过 `mibee-nvr encrypt-config` 加密）
- `port_range`：RTP 媒体端口池，格式 `"start-end"`（默认 `"30000-30050"`）
- `tcp_mode`：强制 TCP 被动模式用于 NAT 后设备（默认 `false`，UDP）
- `tcp_framing`：`tcp_mode=true` 时的 TCP 组帧 — `"rfc4571"`、`"0x24"` 或 `"auto"`

### 步骤 2：配置摄像机

在您的 GB28181 摄像机（海康、大华等）上，配置 SIP 平台：

**海康威视示例**（通过 Web UI）：
- 导航至 **网络 → 高级平台接入**
- 设置 **服务器地址** 为您的 NVR IP 地址
- 设置 **服务器端口** 为 `5060`
- 设置 **设备 ID** 为您的摄像机 20 位编码（例如 `34020000001320000001`）
- 设置 **密码** 匹配您的 NVR GB28181 密码
- 启用 **平台接入**

**大华示例**（通过 Web UI）：
- 导航至 **网络 → TCP/IP → 28181**
- 设置 **服务器 IP** 为您的 NVR IP 地址
- 设置 **服务器端口** 为 `5060`
- 设置 **设备 ID** 为您的摄像机 20 位编码
- 设置 **设备域** 匹配您的 NVR realm（例如 `3402000000`）
- 设置 **密码** 匹配您的 NVR GB28181 密码
- 启用 **28181**

### 步骤 3：启动 NVR

```bash
./mibee-nvr -config mibee-nvr.yaml
```

GB28181 服务器将在 UDP 端口 5060 上监听。您的摄像机应在几秒内 REGISTER。

### 步骤 4：查看已注册设备

打开 MiBee NVR Web UI 并在侧边栏导航至 **GB28181**。您应该看到：

- **设备**：已注册的 GB28181 设备及在线/离线状态
- **通道**：每个设备的视频通道（通常通道 1 = 主码流，通道 2 = 子码流）
- **PTZ**：PTZ 控制面板（如果通道支持 PTZ）

### 步骤 5：添加摄像机进行录像

在您的配置中创建一个映射到 GB28181 通道的摄像机条目：

```yaml
cameras:
  - id: "hikvision-front-door"
    name: "海康前门"
    protocol: "gb28181"
    gb28181:
      device_id: "34020000001320000001"
      channel_id: "34020000001320000001"
      manufacturer: "Hikvision"
    recording_enabled: true
    enabled: true
```

NVR 将：
- 从 PS 流自动检测编解码器（H.264 或 H.265）
- 当摄像机开始录像时 INVITE 通道
- 将 MPEG-PS 流解复用为 NALU
- 将视频送入 StreamHub 进行录像和直播（HLS、WebRTC、FLV 等）

## 配置参考

### 服务器配置

| 参数 | 类型 | 默认值 | 描述 |
|-----------|------|---------|-------------|
| `enabled` | bool | `false` | 启用 GB28181 服务器 |
| `sip_listen` | string | `":5060"` | SIP UDP/TCP 监听地址 |
| `server_id` | string | (必需) | NVR 的 20 位 GB/T 28181 序列号 |
| `realm` | string | (必需) | SIP 摘要认证域（10 位区域码） |
| `password` | string | (必需) | SIP 摘要认证密钥（已加密） |
| `port_range` | string | `"30000-30050"` | RTP 媒体端口池（`"start-end"`） |
| `heartbeat_interval` | string | `"60s"` | 设备心跳间隔 |
| `catalog_interval` | string | `"30m"` | 目录刷新间隔 |
| `tcp_mode` | bool | `false` | 强制 TCP 被动模式用于 NAT 穿透 |
| `tcp_framing` | string | `"auto"` | TCP 组帧：`"rfc4571"`、`"0x24"` 或 `"auto"` |
| `allowed_device_ids` | `[]string` | `[]` | 限制注册到特定设备 ID（空 = 允许所有） |

### 摄像机配置

| 参数 | 类型 | 描述 |
|-----------|------|-------------|
| `protocol` | string | 必须为 `"gb28181"` |
| `gb28181.device_id` | string | 摄像机的 20 位 GB/T 28181 设备编码 |
| `gb28181.channel_id` | string | 摄像机的 20 位 GB/T 28181 通道编码 |
| `gb28181.manufacturer` | string | 可选制造商名称（例如 `"Hikvision"`） |

## PTZ 控制

GB28181 PTZ 控制通过带 MANSCDP DeviceControl 命令的 SIP MESSAGE 实现。

### 支持的方向

- `up`、`down`、`left`、`right` — 云台/倾斜移动
- `up-left`、`up-right`、`down-left`、`down-right` — 对角移动
- `zoom-in`、`zoom-out` — 变倍（需要 `PTZType=2`）
- `stop` — 停止所有移动

### PTZ 类型

通道从目录报告 `PTZType`：
- `0` — 无 PTZ 支持
- `1` — 仅云台/倾斜
- `2` — 云台/倾斜 + 变倍

### Web UI 控制

在 GB28181 设备页面，点击 `PTZType > 0` 的通道的 PTZ 面板：
- 按住方向按钮以速度 128 移动
- 释放以停止
- 点击中心停止按钮停止移动
- `PTZType=2` 时显示变倍 in/out 按钮

### API 控制

```bash
curl -X POST http://localhost:9090/api/gb28181/channels/34020000001320000001/ptz \
  -H "Content-Type: application/json" \
  -H "Authorization: Basic base64(username:password)" \
  -d '{
    "direction": "up",
    "speed": 128
  }'
```

响应：
```json
{
  "status": "ptz_sent",
  "channel_id": "34020000001320000001",
  "direction": "up",
  "speed": 128
}
```

## 回放

GB28181 通过带 `PlayMode=PlayBack` 和 `StartTime`/`EndTime` 的 INVITE 支持历史回放。此功能尚未在 MiBee NVR Web UI 中实现，但您可以通过 API 触发：

```bash
curl -X POST http://localhost:9090/api/gb28181/channels/34020000001320000001/invite \
  -H "Content-Type: application/json" \
  -H "Authorization: Basic base64(username:password)" \
  -d '{
    "playback": {
      "start_time": "2026-01-01T00:00:00Z",
      "end_time": "2026-01-01T01:00:00Z"
    }
  }'
```

这是未来 UI 工作的占位符。底层的 INVITE/BYE 基础设施已支持回放 SDP 协商。

## 故障排除

### 设备未注册

**症状**：设备未出现在 GB28181 设备列表中。

**解决方案**：
1. **检查 SIP 端口**：确保 `sip_listen` 正在监听 `:5060`（或您配置的端口）：
   ```bash
   sudo netstat -ulnp | grep 5060
   sudo ss -ulnp | grep 5060
   ```

2. **检查防火墙**：允许 UDP 端口 5060：
   ```bash
   sudo ufw allow 5060/udp
   # 或
   sudo iptables -A INPUT -p udp --dport 5060 -j ACCEPT
   ```

3. **验证摄像机 SIP 设置**：
   - 服务器 IP 匹配您的 NVR IP
   - 服务器端口为 `5060`
   - 设备 ID 为 20 位（GB/T 28181 格式）
   - 密码匹配您的 NVR `gb28181.password`
   - 摄像机上启用了平台接入

4. **检查 NVR 日志**：
   ```bash
   journalctl -u mibee-nvr -f | grep -i gb28181
   ```

5. **测试 SIP 可达性**：
   ```bash
   # 从摄像机（如果有 shell 访问权限）：
   nc -uvz nvr-ip 5060
   ```

### 心跳超时

**症状**：设备注册但几分钟后离线。

**原因**：设备心跳间隔不匹配 NVR 的 `heartbeat_interval`（默认 60 秒）。大多数摄像机每 60 秒发送一次心跳，但有些使用不同的间隔。

**解决方案**：
1. 在您的配置中调整 `heartbeat_interval`：
   ```yaml
   gb28181:
     heartbeat_interval: "90s"  # 或 "30s"，取决于您的摄像机
   ```

2. 检查摄像机 SIP 设置的心跳间隔并匹配它。

### 无视频（黑屏）

**症状**：通道 INVITE 成功但实时预览为黑屏。

**解决方案**：
1. **检查 RTP 端口分配**：确保 `port_range` 未耗尽：
   ```bash
   # 检查 GB28181 设备页面的 "RTP Port" 列
   # 验证端口在配置的范围内
   ```

2. **检查 RTP 端口防火墙**：允许 `port_range` 中的 UDP 端口：
   ```bash
   sudo ufw allow 30000:30050/udp
   ```

3. **验证编解码器**：GB28181 使用 stream_type 96（H.264）或 97（H.265）的 MPEG-PS。NVR 从 PS 流自动检测。如果摄像机发送不支持的编解码器，解复用将失败。

4. **检查 NVR 日志中的解复用错误**：
   ```bash
   journalctl -u mibee-nvr -f | grep -i "psdemux\|demux"
   ```

5. **通过直接 RTP 捕获测试**（高级）：
   ```bash
   # 从摄像机通告的端口捕获 RTP
   sudo tcpdump -i any -w capture.pcap udp port <camera-rtp-port>
   # 用 Wireshark 分析以验证 MPEG-PS 结构
   ```

### NAT 后设备（网络地址转换）

**症状**：设备注册但无法建立 RTP 媒体（视频从未开始）。

**原因**：来自摄像机的 RTP 数据包无法到达 NVR，因为摄像机位于 NAT 后且 SDP 通告端口未转发。

**解决方案**：
1. **在 NVR 上启用 TCP 被动模式**：
   ```yaml
   gb28181:
     tcp_mode: true
     tcp_framing: "auto"
   ```
   TCP 被动模式要求摄像机向 NVR 发起 TCP 连接，这可以通过 NAT 工作。

2. **在路由器上配置端口转发**：
   - 将 UDP 端口 `30000-30050`（或您的 `port_range`）转发到 NVR IP 地址
   - 将 UDP 端口 `5060`（SIP）转发到 NVR IP 地址

3. **使用 VPN**（例如 Tailscale、WireGuard）绕过 NAT：
   - 在 NVR 和摄像机上安装 VPN（如果支持）
   - 在摄像机 SIP 设置中使用 VPN 分配的 IP

4. **检查摄像机 NAT 穿透设置**：
   - 海康：**网络 → 高级平台接入 → NAT 穿透**
   - 大华：**网络 → TCP/IP → 28181 → NAT 模式**

### 字符集问题（GBK vs UTF-8）

**症状**：目录响应的中文设备/通道名称显示为乱码（mojibake）。

**原因**：GB/T 28181 设备通常在 XML prolog（`<?xml ... encoding="GB2312"?>`）中声明编码为 GB2312/GBK，但可能实际发送 UTF-8。

**解决方案**：NVR 的 MANSCDP 编解码器自动处理此问题：
- 在解组前剥离 XML 声明
- 验证 UTF-8 并回退到 GB18030 → GBK 解码器
- 如果字符集回退失败则记录警告

如果您仍然看到乱码，请检查摄像机 GB28181 配置中的编码设置。某些旧设备需要显式 UTF-8 声明。

### 时钟同步问题

**症状**：设备即使凭据正确也因认证失败拒绝 REGISTER。

**原因**：GB28181 摘要认证使用 SIP `Date` 头进行 nonce 新鲜度。如果 NVR 和设备时钟未同步，设备可能会拒绝质询。

**解决方案**：
1. **将 NVR 时钟同步到 NTP**：
   ```bash
   sudo timedatectl set-ntp true
   # 或
   sudo ntpdate pool.ntp.org
   ```

2. **将摄像机时钟同步到 NTP**（通过摄像机 Web UI）：
   - 海康：**系统 → 时间配置**
   - 大华：**系统 → 常规 → 时间**

3. **验证时钟漂移**：
   ```bash
   # 在 NVR 上
   date
   # 在摄像机上（如果有 shell 访问）
   date
   ```

### TCP 组帧问题

**症状**：TCP 被动模式（`tcp_mode=true`）显示 "read error" 或 "invalid length"。

**原因**：`tcp_framing` 设置不匹配摄像机的线格式。

**解决方案**：
1. 尝试每个组帧选项：
   ```yaml
   gb28181:
     tcp_mode: true
     tcp_framing: "rfc4571"   # 2 字节大端长度前缀
     # 或
     tcp_framing: "0x24"      # RTSP 交织（$ + 通道 + 2 字节长度）
     # 或
     tcp_framing: "auto"      # 从首字节检测（默认）
   ```

2. 检查 NVR 日志中的组帧检测错误：
   ```bash
   journalctl -u mibee-nvr -f | grep -i "tcp\|framing\|0x24\|rfc4571"
   ```

3. **注意**：GB/T 28181-2016 和 -2022 指定 RFC4571 用于 RTP over TCP。`0x24` 模式模拟供应商扩展（RTSP 交织）。对于混合部署使用 `"auto"`。

### PTZ 不工作

**症状**：PTZ 面板被禁用或 PTZ 命令无效。

**解决方案**：
1. **检查 PTZType**：从目录验证通道的 `PTZType`：
   - `0` = 无 PTZ 支持（设备固件限制）
   - `1` = 仅云台/倾斜
   - `2` = 云台/倾斜 + 变倍

2. **检查设备在线状态**：如果设备离线，PTZ 命令将以 409 失败。

3. **验证制造商支持**：并非所有 GB28181 设备都通过 MANSCDP DeviceControl 支持 PTZ。有些使用专有的 SIP 扩展。

4. **检查 NVR 日志中的 PTZ 错误**：
   ```bash
   journalctl -u mibee-nvr -f | grep -i "ptz\|devicecontrol"
   ```

## API 参考

### 设备和通道端点

- `GET /api/gb28181/devices` — 列出已注册设备（支持 ETag）
- `GET /api/gb28181/channels` — 列出所有设备上的所有通道
- `GET /api/gb28181/channels/{id}` — 获取通道详细信息（包括 PTZType）
- `POST /api/gb28181/catalog/refresh` — 触发目录刷新（存根，202 已接受）

### 媒体会话端点

- `POST /api/gb28181/channels/{id}/invite` — INVITE 通道（存根，202 已接受）
- `POST /api/gb28181/channels/{id}/bye` — 发送 BYE 停止流（调用 SessionManager.Bye）

### PTZ 端点

- `POST /api/gb28181/channels/{id}/ptz` — 发送 PTZ 命令

PTZ 请求体：
```json
{
  "direction": "up|down|left|right|up-left|up-right|down-left|down-right|zoom-in|zoom-out|stop",
  "speed": 0-255
}
```

PTZ 方向：
- `up`、`down`、`left`、`right` — 云台/倾斜
- `up-left`、`up-right`、`down-left`、`down-right` — 对角
- `zoom-in`、`zoom-out` — 变倍（需要 `PTZType=2`）
- `stop` — 停止所有移动

### 错误响应

| 状态 | 错误 | 描述 |
|--------|-------|-------------|
| 400 | Invalid body | 缺少或格式错误的 PTZ body |
| 404 | Channel not found | `ErrChannelNotFound` |
| 409 | Device offline | `ErrDeviceOffline` |
| 400 | PTZ unsupported | `ErrPTZUnsupported`（PTZType=0） |
| 400 | Zoom unsupported | `ErrZoomUnsupported`（PTZType=1） |
| 500 | Internal error | 无法编码 MANSCDP 或发送 MESSAGE |

## 支持的协议和功能

### SIP（会话发起协议）
- **REGISTER**：带摘要认证的设备注册
- **Keepalive**：心跳消息用于在线/离线跟踪
- **INVITE**：拉取直播或回放媒体流
- **BYE**：拆除媒体会话
- **MESSAGE**：发送 MANSCDP 命令（目录请求、设备控制）

### 媒体（RTP/PS）
- **RTP over UDP**：默认传输（端口范围可配置）
- **RTP over TCP**：可选 TCP 被动模式用于 NAT 穿透
  - 组帧：RFC4571（标准）、0x24（供应商扩展）、自动检测
- **MPEG-PS 解复用**：提取 H.264（stream_type 96）或 H.265（stream_type 97）NALU
- **StreamHub 集成**：非阻塞帧分发到 HLS、WebRTC、FLV 等

### MANSCDP（管理和控制）
- **Catalog**：设备/通道列表，包含 PTZType、制造商、型号
- **DeviceInfo**：设备固件/硬件详细信息
- **DeviceControl**：PTZ 命令（方向、速度）

## 限制和已知问题

### 回放 UI
回放 INVITE 在会话管理器中受支持，但尚未在 Web UI 中公开。使用 API 端点 `POST /api/gb28181/channels/{id}/invite` 并附带 `playback.start_time` 和 `playback.end_time` 作为临时解决方案。

### TCP 0x24 模式
`0x24` TCP 组帧模式模拟供应商扩展（RTSP 交织）。GB/T 28181-2016 和 -2022 指定 RFC4571 为标准。对于混合部署使用 `"auto"`；对于严格合规使用 `"rfc4571"`。

### 设备特定行为
- **海康威视**：支持完整目录、PTZ 和回放。某些旧型号可能需要 GBK 字符集回退。
- **大华**：支持完整目录、PTZ 和回放。NAT 穿透设置因固件而异。
- **宇视**：与海康类似，但 PTZ 命令语义可能不同。
- **其他品牌**：支持情况各异。最小设备仅实现 REGISTER/keepalive/INVITE。

### RPi 3B 上的端口耗尽
默认 `port_range` 为 `"30000-30050"`（51 个端口）。如果您有许多并发的 GB28181 流，请扩展范围：
```yaml
gb28181:
  port_range: "30000-30100"  # 101 个端口
```

在 RPi 3B 上，保持池低于 200 个端口以避免临时端口耗尽。

## 安全考虑

- **摘要认证**：GB28181 使用 SIP 摘要认证（类似 HTTP Basic 但使用 nonce 哈希）。当设置 `NVR_ENCRYPTION_KEY` 时，`password` 字段会被加密。
- **设备 ID 限制**：使用 `allowed_device_ids` 白名单特定的 20 位设备编码。空列表 = 允许所有。
- **网络暴露**：GB28181 默认在 UDP 端口 5060 上运行。如果您的 NVR 暴露在互联网上，请使用防火墙或 VPN 限制访问。
- **无商业内容**：此实现是开源且免费的。不包含或引用任何 Pro/P2P 功能。

## 支持资源

### 文档
- [MiBee NVR 快速开始](./getting-started.md)
- [MiBee NVR 配置指南](./configuration.md)
- [MiBee NVR API 参考](./api/README.md)

### GB/T 28181 参考资料
- GB/T 28181-2016：信息技术 — 视频监控联网系统技术要求
- GB/T 28181-2022：最新版本（增加 TCP 被动、改进目录）

### 社区支持
- GitHub Issues: [MiBee NVR Issues](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues)
- Discussions: [MiBee NVR Discussions](https://github.com/Mi-Bee-Studio/MiBeeNvr/discussions)

---

本指南提供了 MiBee NVR GB/T 28181 集成的全面覆盖。对于特定摄像机型号，请查阅制造商文档以了解 GB28181 功能和限制。