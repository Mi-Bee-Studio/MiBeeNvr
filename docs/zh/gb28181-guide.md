# MiBee NVR GB/T 28181 指南

本指南介绍 MiBee NVR 的 GB/T 28181（中国视频监控国家标准）集成：SIP 平台配置、设备接入、音频、语音对讲、订阅推送、PTZ、设备录像回放和故障排除。

## 什么是 GB/T 28181？

GB/T 28181 是中国视频监控网络系统的国家标准，定义了 IP 摄像机、NVR 和平台通过 SIP 和 RTP/PS 通信的方式。MiBee NVR 实现**平台角色**（UAS — 应答方），这意味着：

- 设备通过 SIP（UDP 端口 5060，默认）**REGISTER** 到 NVR
- 设备发送 **Keepalive** 心跳维持在线状态
- NVR **INVITE** 通道拉取 RTP/PS 媒体流（拉流模式）
- NVR 将 MPEG-PS 解复用为 H.264/H.265 NALU 与音频帧（G.711/AAC），进入标准录像/直播管线

兼容矩阵：GB/T 28181-2007/2011/2016/2022（见 `internal/gb28181/doc.go`）。支持的摄像机品牌包括海康威视、大华、宇视和其它 GB28181 兼容制造商。

## 快速开始

### 步骤 1：启用 GB28181 服务器

**推荐：Web 设置页**。打开 NVR Web UI → **设置 → GB28181**，打开启用开关并填写参数后保存（密码为只写字段，留空保存表示保持不变）。

![GB28181 设置页](images/settings-gb28181.webp)

等价的 YAML 配置（`mibee-nvr.yaml`）：

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
  media_transport: "udp"        # udp | tcp-passive | tcp-active
  subscribe_catalog: true       # 通道变更实时推送
  subscribe_alarm: true         # 报警订阅
  subscribe_mobile_position: false
  subscribe_expires: "3600s"
  allowed_device_ids: []        # 空 = 允许所有设备注册
```

**关键参数**：
- `server_id`：NVR 的 20 位 GB/T 28181 平台编号（设备端需完全一致）
- `realm`：SIP 摘要认证域（通常为 10 位区域码）
- `password`：SIP 摘要认证密码。**留空 = 不鉴权**（任何能访问 5060 端口的设备都可注册，见"安全考虑"）
- `media_transport`：RTP 媒体传输 —— `udp`（默认）、`tcp-passive`（NVR 监听、设备连接，海康/大华 NAT 默认）、`tcp-active`（NVR 拨向设备应答地址）

### 步骤 2：配置摄像机

在 GB28181 摄像机（海康、大华等）上配置 SIP 平台接入：

**海康威视**（Web UI）：**网络 → 高级平台接入** —— 服务器地址填 NVR IP、服务器端口 `5060`、设备 ID 填 20 位国标码、密码与 NVR 一致、启用平台接入。

**大华**（Web UI）：**网络 → TCP/IP → 28181** —— 服务器 IP/端口/设备 ID/设备域（realm）/密码与 NVR 一致、启用 28181。

设备应在几秒内 REGISTER 成功。

### 步骤 3：自动入册（零手动配置）

**无需手动添加相机。** 设备注册成功后 NVR 自动完成：

1. 记入设备列表并跟踪在线状态（心跳超时判离线）
2. 自动查询目录（Catalog），发现全部真实视频通道（组织节点自动跳过）
3. 每个通道自动创建一台相机（`gb-<通道ID>`），与 ONVIF/RTSP 相机并列出现在相机列表
4. 自动 INVITE 拉流开始录像
5. 自动补全厂商/型号（DeviceInfo），订阅目录变更与报警推送

需要手动绑定特殊通道时，也可以用 YAML 或 Web 表单显式创建：

```yaml
cameras:
  - id: "hikvision-front-door"
    name: "海康前门"
    protocol: "gb28181"
    gb28181:
      device_id: "34020000001320000001"
      channel_id: "34020000001320000001"
    recording_enabled: true
    enabled: true
```

### 步骤 4：开启音频（按相机）

音频默认关闭。在相机编辑页打开 **音频** 开关（或 API `PUT /api/cameras/{id}` `{"audio_enabled": true}`）后，PS 流中的音频轨（G.711 A-law/μ-law、AAC）会写入 MP4 音轨并进入直播音频。

## 功能总览

| 能力 | 状态 | 说明 |
|---|---|---|
| REGISTER + Digest 鉴权 / 白名单 | ✅ | 401 挑战、周期重注册、注销、IP 变化自动重建会话 |
| 自动入册 | ✅ | 注册 → 目录 → 通道建卡 → 自动拉流，零手动配置 |
| 实时点播（INVITE） | ✅ | UDP / TCP 被动 / TCP 主动（0x24、rfc4571、auto 组帧） |
| 录像 | ✅ | H.264/H.265，30s 分片、IDR 对齐、滚动合并 |
| 直播 | ✅ | 经 StreamHub 进 HLS/WebRTC/FLV/WS 全部协议 |
| **音频** | ✅ | G.711 A-law/μ-law + AAC → MP4 音轨 + 直播音频；无 PSM 流自动兜底 |
| **语音对讲** | ✅ | 浏览器麦克风 → 设备扬声器（LiveView 一键对讲） |
| 设备录像检索/回放 | ✅ | RecordInfo + 回放取流进录像库（Recordings 页）+ 倍速/拖动 |
| PTZ + 预置位 | ✅ | 方向/变倍/预置位/巡航（FICommand） |
| 目录订阅 | ✅ | 通道变更实时 NOTIFY，替代纯轮询 |
| 报警订阅 | ✅ | → 事件总线/SSE/REST 环形缓存 |
| 移动位置订阅 | ✅ | 车载/布控球场景，REST 查询轨迹 |
| 时钟同步 | ✅ | 设备发 Query，平台应答标准时间 |
| 设备信息/状态自动化 | ✅ | 厂商/型号自动补全，keepalive 不丢元数据 |
| **多级级联（下级角色）** | ✅ | 向上级平台注册、聚合目录、INVITE 转发（见"级联上联"） |
| GB35114 国密 | ⏸ | 按需评估（见 #341） |

## 配置参考

### 服务器配置

| 参数 | 类型 | 默认值 | 描述 |
|-----------|------|---------|-------------|
| `enabled` | bool | `false` | 启用 GB28181 服务器 |
| `sip_listen` | string | `":5060"` | SIP UDP 监听地址 |
| `server_id` | string | (必填) | 平台 20 位国标编号 |
| `realm` | string | (必填) | SIP 摘要认证域 |
| `password` | string | (建议设置) | SIP 摘要认证密码；留空 = 不鉴权 |
| `port_range` | string | `"30000-30050"` | RTP 媒体端口池（`"start-end"`） |
| `heartbeat_interval` | string | `"60s"` | 期望的设备心跳间隔 |
| `catalog_interval` | string | `"30m"` | 目录轮询间隔（订阅开启时仅作兜底） |
| `media_transport` | string | `"udp"` | 媒体传输：`udp` / `tcp-passive` / `tcp-active` |
| `sip_transport` | string | `"udp"` | 信令传输：`udp`（可叠加 `tcp` 监听） |
| `tcp_framing` | string | `"auto"` | TCP 组帧：`"rfc4571"`、`"0x24"`、`"auto"` |
| `subscribe_catalog` | bool | `true` | 目录订阅（通道变更实时推送） |
| `subscribe_alarm` | bool | `true` | 报警订阅 |
| `subscribe_mobile_position` | bool | `false` | 移动位置订阅（静止相机无需开启） |
| `subscribe_expires` | string | `"3600s"` | 订阅有效期（80% 时自动续订） |
| `allowed_device_ids` | `[]string` | `[]` | 注册白名单（空 = 允许所有） |

> `tcp_mode`（bool）是 `media_transport` 的旧别名，保留仅为 YAML 兼容。

### 相机配置

| 参数 | 类型 | 描述 |
|-----------|------|-------------|
| `protocol` | string | 必须为 `"gb28181"` |
| `gb28181.device_id` | string | 设备 20 位国标编码 |
| `gb28181.channel_id` | string | 通道 20 位国标编码 |
| `audio_enabled` | bool | 音频开关（默认 false，开启后录 MP4 音轨 + 直播音频） |

## 音频

- **编解码**：G.711 A-law / μ-law（8kHz）、AAC（ADTS 封装自动转 ASC）
- **开关**：按相机 `audio_enabled`，关闭时丢弃解复用出的音频帧
- **无 PSM 流兜底**：部分固件把音频复用进 PS 流但不发 PSM（或 PSM 不声明音频）。NVR 按优先级判定编解码：**PSM 声明 → INVITE 应答 SDP `a=rtpmap` → 字节启发式**（ADTS 同步字 → AAC；μ/A-law 按静音簇投票，4KB 内静音样本 μ-law 聚 0xFF/0x7F、A-law 聚 0x55/0xD5）。判定时记 INFO 日志 `codec inferred`
- **无 PTS 音频**：按样本数推进时间线，兼容省略音频 PTS 的固件
- **设备端接入要求**（推荐做法）：音频复用进同一路 PS/RTP 流（同端口同 SSRC），PSM 声明音频（stream_type 0x90=μ-law / 0x91=A-law / 0x0F=AAC，stream_id 0xC0-0xDF），G.711 发 8kHz 单声道裸字节

## 语音对讲

LiveView 页国标相机有 **对讲** 按钮：浏览器麦克风 → AudioWorklet 降采样到 8kHz → A-law 编码 → WebSocket 上行 → NVR 发起 audio-only INVITE（`m=audio ... RTP/AVP 8`，`a=sendrecv`）→ 设备接收地址收 RTP。半双工：直播音频与对讲下行并存。

API：`GET /api/cameras/{id}/gb28181/talk`（WebSocket，二进制帧 = A-law 字节）、`GET /api/cameras/{id}/gb28181/talk/status`。

## 级联上联（下级角色）

NVR 可作为**下级平台**向上级 GB/T 28181 平台上联：本机相机聚合为国标目录上报，上级 INVITE 任意通道时以 RTP/PS 转发媒体（纯封装不转码）。上级无需特殊支持——任何标准平台（包括另一台 MiBee NVR 的平台角色）即可对接。

```yaml
gb28181_cascade:
  enabled: true
  server_domain: "34020000002000000001"   # 上级平台 20 位编号
  server_addr: "192.168.1.10:5060"       # 上级 SIP 地址
  local_device_id: "34020000001320000099" # 本 NVR 作为下级设备的编号
  realm: "3402000000"
  password: "..."                          # encrypt-config 加密
  sip_listen: ":5061"                      # 级联信令端口（与平台角色 5060 并存）
  heartbeat_interval: "60s"
  register_expires: 3600
```

行为要点：

- 通道编号按相机首次入目录顺序分配（`<编号前10位>132+7位序号`），持久化防重启变号——上级的绑定始终有效
- REGISTER + Digest、Expires 80% 续注、Keepalive；上级重启（keepalive 403）立即重注册
- 目录应答聚合全部非 MJPEG 相机；H.264/H.265 由首帧 NAL 嗅探决定 PSM 类型
- 上级 INVITE → 200 OK(sendonly) → 从 StreamHub 取流 → PS 复用 → RTP 推送；BYE/错误清理
- v1 限制：**仅视频**（hub 音频暂不区分 G.711 A/μ 律）；单上级；不推送目录变更 NOTIFY（上级轮询兜底）

## 订阅与事件

- **目录订阅**：设备通道变更通过 NOTIFY 实时推送（合并进通道表并自动建卡）
- **报警订阅**：`GET /api/gb28181/devices/{id}/alarms` 查询环形缓存（最近 200 条），同时进事件总线（SSE `gb28181.alarm`）
- **移动位置订阅**：`GET /api/gb28181/devices/{id}/positions` 查询位置轨迹
- 三个开关与有效期见设置页 / 上表；到期前 80% 自动续订

## 设备录像检索与回放

Recordings 页可按国标通道检索**设备侧存储的录像**（RecordInfo）并取回：取回片段经正常录像管线落盘（分段、记录行），在 Recordings 页直接播放，支持倍速/拖动（INFO 控制）。

API：
- `GET /api/gb28181/channels/{id}/records?start=&end=` — 检索设备录像列表
- `POST /api/gb28181/channels/{id}/playback` — 开始取回（body 含 start/end）
- `GET /api/gb28181/channels/{id}/playback` — 取回进度
- `DELETE /api/gb28181/channels/{id}/playback` — 停止
- `POST /api/gb28181/channels/{id}/playback/control` — 倍速/拖动/暂停

## PTZ 控制

PTZ 经 MANSCDP DeviceControl 下发。支持方向（含对角）、变倍（需 `PTZType=2`）、预置位/巡航（FICommand）。通道的 `PTZType` 来自目录报告（0=无、1=云台、2=云台+变倍）。

Web UI：LiveView 相机页 PTZ 面板（按住移动、松开停止、预置位调用/设置）。

```bash
curl -X POST http://localhost:9090/api/gb28181/channels/34020000001320000001/ptz \
  -H "Content-Type: application/json" \
  -H "Authorization: Basic base64(username:password)" \
  -d '{"direction": "up", "speed": 128}'
```

方向：`up/down/left/right/up-left/up-right/down-left/down-right/zoom-in/zoom-out/stop`。

## API 参考

### 设备与通道
- `GET /api/gb28181/devices` — 已注册设备（支持 ETag）
- `GET /api/gb28181/devices/{id}/channels` — 设备通道列表
- `POST /api/gb28181/devices/{id}/catalog-refresh` — 触发目录刷新（202）
- `GET /api/gb28181/devices/{id}/alarms` — 报警环形缓存
- `GET /api/gb28181/devices/{id}/positions` — 移动位置轨迹

### 媒体会话
- `POST /api/gb28181/channels/{id}/invite` / `bye` — 手动拉流/停流
- `POST /api/gb28181/channels/{id}/ptz` — PTZ 控制

### 录像检索/回放（设备侧）
- 见上节

### 语音对讲
- `GET /api/cameras/{id}/gb28181/talk`（WebSocket）、`GET .../talk/status`

### 错误响应

| 状态 | 场景 |
|--------|-------|
| 400 | 请求体缺失/畸形；不支持的 PTZ |
| 404 | 通道不存在 |
| 409 | 设备离线 |
| 500 | 内部错误（MANSCDP 编码/发送失败） |

## 故障排除

### 设备未注册

1. 确认 `sip_listen` 监听（`ss -ulnp | grep 5060`）与防火墙放行 UDP 5060
2. 核对设备端五要素：服务器 IP/端口 5060/服务器 ID（须与 `server_id` 完全一致）/域（realm）/密码
3. 看日志：`journalctl -u mibee-nvr -f | grep -i gb28181` —— `REGISTER auth failed` = 密码不匹配；`not in allowlist` = 白名单
4. 改过 NVR 密码后：设备下一次重注册（≤1 个注册周期）会 403 掉线，设备端同步修改后自动恢复，无需重新入册

### 视频黑屏 / 无流

1. 检查 `port_range` 未耗尽、防火墙放行该 UDP 段
2. NAT 场景切换 `media_transport: tcp-passive`
3. TCP 组帧报错时依次尝试 `tcp_framing: rfc4571 / 0x24 / auto`
4. 编码：H.264/H.265 自动探测，无需配置

### 音频无声

1. 确认相机 **音频开关** 已开启（`audio_enabled`，默认关）
2. 看日志 `codec inferred` —— 无 PSM 设备会自动判定编解码；若始终无此日志且无音轨，抓包确认设备确实把音频复用进了 PS 流
3. 设备端推荐做法见"音频"节；无 PSM 也能出声（自动兜底），但其它平台（ZLMediaKit 等）会丢，建议设备侧补 PSM 声明

### 会话周期性回收重邀

日志出现 `recycling stale session reason=no keyframe` 且录像每隔约一分多钟有空窗：设备关键帧间隔（GOP）超过 NVR 的 75 秒看门狗。属正常自愈行为（重邀强制出新 IDR），但建议把设备 GOP 收敛到 **2-4 秒**。

### REGISTER 源端口轮换

部分 SIP 栈每个 REGISTER 换源端口。NVR 仅按 **IP** 判断地址变化，端口轮换不会回收会话，无需处理。

### 心跳超时掉线

设备心跳间隔与 NVR `heartbeat_interval`（默认 60s）不匹配时调整任一侧。

### 字符集乱码（GBK vs UTF-8）

MANSCDP 编解码器自动处理：剥离 XML 声明、UTF-8 校验、GB18030/GBK 回退。仍乱码时检查设备端编码设置。

### 时钟同步

设备因时钟漂移鉴权异常或录像检索时间不对时：NVR 自动应答设备的 TimeSync 查询；两侧建议均同步 NTP。

## 限制与已知问题

- **设备自注册伪通道**：NVR 重启后，多通道目录设备会短暂出现一台"设备自身通道"的相机（不推流）。已知问题，重启后手动归档即可，跟踪于 #352
- **对讲真机覆盖**：语音对讲目前经模拟器 E2E 验证，真机回环验证跟踪于 #353
- **TCP 0x24 组帧**：非标准扩展（GB/T 28181-2016/2022 指定 RFC4571），混合部署用 `auto`
- **多级级联 / GB35114**：未实现，按需评估（#341）
- **报警联动点播**：报警事件已进总线，联动自动拉流跟踪于 #355

## 安全考虑

- **鉴权**：务必设置 `password`（留空 = 任何可达设备都能注册）。配合 `allowed_device_ids` 白名单双层防护
- **传输**：SIP/RTP 为明文（GB35114 国密/TLS 未实现）。公网部署必须用防火墙/VPN 收敛暴露面，勿直接暴露 5060 与端口池
- **端口池**：默认 51 路，按并发需求扩展（RPi 3B 建议 <200）

## 支持资源

- [快速开始](./getting-started.md) · [配置指南](./configuration.md) · [API 参考](./api/README.md)
- GB/T 28181-2016 / 2022 国家标准文本
- [GitHub Issues](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues) · [Discussions](https://github.com/Mi-Bee-Studio/MiBeeNvr/discussions)
