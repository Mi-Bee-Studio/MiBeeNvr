# 摄像头品牌兼容指南

MiBee NVR 通过多种协议支持广泛的 IP 摄像头，包括 RTSP（H.264/H.265/MJPEG）、HTTP JPEG 和 ONVIF。本指南提供主流摄像头品牌的全面兼容性信息，包括支持的协议、配置示例和故障排除技巧。

**ONVIF 集成**：有关全面的 ONVIF 摄像头支持、发现方法、PTZ 控制和故障排除，请参阅 [ONVIF 指南](./onvif-guide.md)。

## 快速入门（Top 3 品牌）

### Hikvision

```yaml
cameras:
  - id: "hikvision_front_door"
    name: "Front Door - Hikvision"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password123@192.168.1.100:554/Streaming/Channels/101"
    enabled: true
```

1. **访问摄像头**：通过 Hikvision iVMS-4200 或 Web 界面查找摄像头 IP 地址
2. **启用 RTSP**：确保摄像头 Web 界面中已启用 RTSP 流（通常在 网络 → 高级设置 下）
3. **配置**：使用上面的 URL，替换为你的摄像头 IP 和凭据

### Dahua

```yaml
cameras:
  - id: "dahua_driveway"
    name: "Driveway - Dahua"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:admin@192.168.1.101:554/cam/realmonitor?channel=1&subtype=0"
    enabled: true
```

1. **访问摄像头**：通过 Dahua SmartPSS 或 Web 界面查找摄像头 IP 地址
2. **启用 RTSP**：在摄像头设置中启用 RTSP 流（通常在 配置 → 网络 → 媒体流 下）
3. **配置**：使用上面的 URL，替换为你的摄像头 IP 和凭据

### Uniview

```yaml
cameras:
  - id: "uniview_parking"
    name: "Parking - Uniview"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:123456@192.168.1.102:554/unicast/c1/s0/live"
    enabled: true
```

1. **访问摄像头**：通过 Uniview iViewer 或 Web 界面查找摄像头 IP 地址
2. **启用 RTSP**：在摄像头设置中启用 RTSP 流（通常在 网络 → 媒体流 下）
3. **配置**：使用上面的 URL，替换为你的摄像头 IP 和凭据

## 兼容性概览

| 层级 | 支持级别 | 协议 | 品牌 | 说明 |
|------|---------------|----------|--------|-------|
| **完全支持** | ✅ 自动检测 + PTZ | RTSP + ONVIF | Hikvision, Dahua, Uniview, Axis, Bosch, Vivotek, Hanwha, Amcrest, Reolink | 最佳体验，功能完整 |
| **手动配置** | ⚠️ 仅 RTSP，ONVIF 有限 | 仅 RTSP | TP-Link VIGI, EZVIZ, ANNKE, Lorex, Swann, Speco | 需要手动配置 |
| **有限/特殊** | 🔧 需要特殊处理 | 多种 | Xiaomi, BESDER, Generic, Wyze | 自定义设置或存在限制 |

## 完全支持（ONVIF + RTSP）

### 1. Hikvision

**型号**：DS-2CD2T42WDG1-I, DS-2CD2143G0-I, DS-2CD2343G0-I

#### RTSP URL：
- 主码流：`rtsp://user:pass@ip:554/Streaming/Channels/101`
- 子码流：`rtsp://user:pass@ip:554/Streaming/Channels/102`
- 音频通道：`rtsp://user:pass@ip:554/Streaming/Channels/101_1`

#### 配置：
```yaml
cameras:
  - id: "hikvision_main"
    name: "Hikvision Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password123@192.168.1.100:554/Streaming/Channels/101"
    enabled: true
```

#### 默认凭据：
- 用户名：`admin`
- 密码：`password123`（默认，可能因型号而异）

#### 已知问题：
- 部分型号需要在 Web 界面中先启用 RTSP
- 固件版本低于 V5.3.x 时 ONVIF 可能无法正常工作
- 音频流有时与视频分开传输

### 2. Dahua

**型号**：IPC-HFW1237S-Z, IPC-HDW2831R-ZS, IPC-HFW5442E-Z

#### RTSP URL：
- 通道 1：`rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`
- 通道 2：`rtsp://user:pass@ip:554/cam/realmonitor?channel=2&subtype=0`
- 音频流：`rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=1`

#### 配置：
```yaml
cameras:
  - id: "dahua_main"
    name: "Dahua Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:admin@192.168.1.101:554/cam/realmonitor?channel=1&subtype=0"
    enabled: true
```

#### 默认凭据：
- 用户名：`admin`
- 密码：`admin`（默认）

#### 已知问题：
- 部分型号使用不同的子码流 URL（子码流使用 `subtype=1`）
- 音频可能需要单独配置
- ONVIF 发现可能比直接使用 RTSP 耗时更长

>### 3. Uniview
>
>**型号**：U-AI1208LBF, U-AI1208LBFZ, U-CV3208ERBU
>
>#### RTSP URL：
>- 主码流：`rtsp://user:pass@ip:554/unicast/c1/s0/live`
>- 子码流：`rtsp://user:pass@ip:554/unicast/c1/s1/live`
>- 音频流：`rtsp://user:pass@ip:554/unicast/c1/s0/live`
>
>#### 配置：
>```yaml
>cameras:
>  - id: "uniview_main"
>    name: "Uniview Main Stream"
>    protocol: "rtsp"
>    encoding: "h264"
>    url: "rtsp://admin:123456@192.168.1.102:554/unicast/c1/s0/live"
>    enabled: true
>```
>
>#### 默认凭据：
>- 用户名：`admin`
>- 密码：`123456`（默认）
>
>#### 已知问题：
>- 部分型号需要在摄像头设置中启用 RTSP
>- 音频流可能与视频分开传输
>- ONVIF 端口可能不同（通常为 80，但请检查摄像头设置）
>
>### 4. Axis
>
>**型号**：M3045-V, M3054-V, M3065-V
>
>#### RTSP URL：
>- 主码流：`rtsp://root:pass@ip:554/axis-media/media.amp`
>- 高质量：`rtsp://root:pass@ip:554/axis-media/media.amp?videoType=1`
>- 音频流：`rtsp://root:pass@ip:554/axis-media/media.amp?videoType=3`
>
>#### 配置：
>```yaml
>cameras:
>  - id: "axis_main"
>    name: "Axis Main Stream"
>    protocol: "rtsp"
>    encoding: "h264"
>    url: "rtsp://root:password@192.168.1.103:554/axis-media/media.amp"
>    enabled: true
>```
>
>#### 默认凭据：
>- 用户名：`root`
>- 密码：`pass`（默认）
>
>#### 已知问题：
>- 部分型号需要特定的认证方式
>- 音频流可能需要单独配置
>- 摄像头固件更新可能改变 RTSP 端点
>
>### 5. Bosch
>
>**型号**：FLEXIDOME 5000I, MIC IP, DINION 6000I
>
>#### RTSP URL：
>- 视频流 1：`rtsp://user:pass@ip:554/rtp_video1`
>- 视频流 2：`rtsp://user:pass@ip:554/rtp_video2`
>- 音频流：`rtsp://user:pass@ip:554/rtp_audio1`
>
>#### 配置：
>```yaml
>cameras:
>  - id: "bosch_main"
>    name: "Bosch Main Stream"
>    protocol: "rtsp"
>    encoding: "h264"
>    url: "rtsp://admin:12345@192.168.1.104:554/rtp_video1"
>    enabled: true
>```
>
>#### 默认凭据：
>- 用户名：`admin`
>- 密码：`12345`（默认）
>
>#### 已知问题：
>- 部分型号使用不同的 RTSP 端口范围
>- 音频可能需要单独配置
>- ONVIF 配置可能较为复杂
>
>### 6. Vivotek
>
>**型号**：IB8369, IP8332-H, FD8166
>
>#### RTSP URL：
>- 实时流：`rtsp://user:pass@ip:554/live.sdp`
>- 主码流：`rtsp://user:pass@ip:554/h264/ch1/main/av_stream`
>- 子码流：`rtsp://user:pass@ip:554/h264/ch1/sub/av_stream`
>
>#### 配置：
>```yaml
>cameras:
>  - id: "vivotek_main"
>    name: "Vivotek Main Stream"
>    protocol: "rtsp"
>    encoding: "h264"
>    url: "rtsp://root:123456@192.168.1.105:554/live.sdp"
>    enabled: true
>```
>
>#### 默认凭据：
>- 用户名：`root`
>- 密码：`123456`（默认）
>
>#### 已知问题：
>- 部分型号需要特定的 RTSP 路径
>- 音频流可能不适用于所有型号
>- ONVIF 发现可能需要手动配置
>
>### 7. Hanwha
>
>**型号**：XNV-6081R, XND-6081R, XNV-8081R
>
>#### RTSP URL：
>- Profile 1：`rtsp://user:pass@ip:554/profile1/media.smp`
>- Profile 2：`rtsp://user:pass@ip:554/profile2/media.smp`
>- 音频流：`rtsp://user:pass@ip:554/profile1/audio`
>
>#### 配置：
>```yaml
>cameras:
>  - id: "hanwha_main"
>    name: "Hanwha Main Stream"
>    protocol: "rtsp"
>    encoding: "h264"
>    url: "rtsp://admin:123456@192.168.1.106:554/profile1/media.smp"
>    enabled: true
>```
>
>#### 默认凭据：
>- 用户名：`admin`
>- 密码：`123456`（默认）
>
>#### 已知问题：
>- 部分型号使用不同的 Profile 编号
>- 音频流可能需要单独配置
>- ONVIF 端口因型号而异（请检查摄像头 Web 界面）
>
>### 8. Amcrest
>
>**型号**：IP8M-T1179EW, IP5M-T1177EW, IP2M-841EB
>
>#### RTSP URL：
>- 通道 1：`rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`
>- 通道 2：`rtsp://user:pass@ip:554/cam/realmonitor?channel=2&subtype=0`
>- 音频流：`rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=1`
>
>#### 配置：
>```yaml
>cameras:
>  - id: "amcrest_main"
>    name: "Amcrest Main Stream"
>    protocol: "rtsp"
>    encoding: "h264"
>    url: "rtsp://admin:admin@192.168.1.107:554/cam/realmonitor?channel=1&subtype=0"
>    enabled: true
>```
### 8. Amcrest

**型号**: IP2M-841, IP3M-954, IP4M-1051, IP8M-2796

#### RTSP URL:
- 主流: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`
- 子流: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=1`
- 音频流: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`（部分型号支持）

#### 配置：
```yaml
cameras:
  - id: "amcrest_main"
    name: "Amcrest Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:admin@192.168.1.107:554/cam/realmonitor?channel=1&subtype=0"
    enabled: true
```

#### 默认凭据：
- 用户名：`admin`
- 密码：`admin`（默认）

#### 已知问题：
- 与大华摄像头类似，但固件不同
- 部分型号对子类型的使用有所不同
- ONVIF 发现功能可用但可能较慢

### 9. Reolink

**型号**: RLC-810A, RLC-510A, RLC-823A

#### RTSP URL：
- 主流: `rtsp://user:pass@ip:554/h264Preview_01_main`
- 子流: `rtsp://user:pass@ip:554/h264Preview_01_sub`
- 音频流: `rtsp://user:pass@ip:554/h264Preview_01_main`

#### 配置：
```yaml
cameras:
  - id: "reolink_main"
    name: "Reolink Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:123456@192.168.1.108:554/h264Preview_01_main"
    enabled: true
```

#### 默认凭据：
- 用户名：`admin`
- 密码：`123456`（默认）

#### 已知问题：
- 部分型号需要 Reolink 专有认证方式
- 音频流可能需要单独配置
- ONVIF 发现功能可用但端口可能不同（8000）

## 手动配置（仅 RTSP）

### 10. TP-Link VIGI

**型号**: TC-VR303, TC-VR307, TC-V304GS

#### RTSP URL：
- 流 1：`rtsp://user:pass@ip:554/stream1`
- 流 2：`rtsp://user:pass@ip:554/stream2`
- 子流：`rtsp://user:pass@ip:554/stream1_sub`

#### 配置：
```yaml
cameras:
  - id: "tplink_vigi"
    name: "TP-Link VIGI"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:admin@192.168.1.109:554/stream1"
    enabled: true
```

#### 默认凭据：
- 用户名：`admin`
- 密码：`admin`（默认）

#### 已知问题：
- ONVIF 支持有限（可能无法自动发现）
- 部分型号需要手动配置 URL
- 音频流并非始终可用

### 11. EZVIZ

**型号**: CS-CV248, CS-CV228, CS-CV208

#### RTSP URL：
- 通道 1：`rtsp://user:pass@ip:554/h264/ch1/main/av_stream`
- 通道 2：`rtsp://user:pass@ip:554/h264/ch2/main/av_stream`
- 音频流：`rtsp://user:pass@ip:554/h264/ch1/main/audio`

#### 配置：
```yaml
cameras:
  - id: "ezviz_main"
    name: "EZVIZ Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:verification_code@192.168.1.110:554/h264/ch1/main/av_stream"
    enabled: true
```

#### 默认凭据：
- 用户名：`admin`
- 密码：摄像头标签上的验证码

#### 已知问题：
- 大多数型号不支持 ONVIF
- 密码频繁变更（基于标签）
- 部分型号需要固件更新才能支持 RTSP

### 12. ANNKE

**型号**: 8CH H.265, 4CH H.264, 16CH NVR

#### RTSP URL：
- 通道 1：`rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`
- 通道 2：`rtsp://user:pass@ip:554/cam/realmonitor?channel=2&subtype=0`
- 音频流：`rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=1`

#### 配置：
```yaml
cameras:
  - id: "annke_main"
    name: "ANNKE Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:admin123@192.168.1.111:554/cam/realmonitor?channel=1&subtype=0"
    enabled: true
```

#### 默认凭据：
- 用户名：`admin`
- 密码：`admin123`（默认）

#### 已知问题：
- 大华 OEM，URL 格式与大华类似
- ONVIF 支持因型号而异
- 部分型号需要特定固件版本

### 13. Lorex

**型号**: LNB4432, LNC22852B, LNE8832

#### RTSP URL：
- 主流: `rtsp://user:pass@ip:554/stream`
- 子流: `rtsp://user:pass@ip:554/stream_sub`
- 音频流: `rtsp://user:pass@ip:554/stream_audio`

#### 配置：
```yaml
cameras:
  - id: "lorex_main"
    name: "Lorex Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:000000@192.168.1.112:554/stream"
    enabled: true
```

#### 默认凭据：
- 用户名：`admin`
- 密码：`000000` 或 `123456`（因型号而异）

#### 已知问题：
- 大华/FLIR OEM，RTSP 格式因型号而异
- ONVIF 支持在不同型号间不一致
- 部分型号需要特定配置步骤

### 14. Swann

**型号**: SWPRO-890MS, SWPRO-882MS, SWPRO-848MS

#### RTSP URL：
- 主流: `rtsp://user:pass@ip:554/stream`
- 子流: `rtsp://user:pass@ip:554/stream_sub`
- 音频流: `rtsp://user:pass@ip:554/stream_audio`

#### 配置：
```yaml
cameras:
  - id: "swann_main"
    name: "Swann Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:123456@192.168.1.113:554/stream"
    enabled: true
```

#### 默认凭据：
- 用户名：`admin`
- 密码：`123456`（默认）

#### 已知问题：
- 与 Lorex 类似，因型号而异
- ONVIF 支持不一致
- 部分型号需要固件更新

### 15. Speco Technologies

**型号**: C128-HV, C828-HV, C168-HV

#### RTSP URL：
- 主流: `rtsp://user:pass@ip:554/stream`
- 子流: `rtsp://user:pass@ip:554/stream_sub`
- 音频流: `rtsp://user:pass@ip:554/stream_audio`

#### 配置：
```yaml
cameras:
  - id: "speco_main"
    name: "Speco Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:1234@192.168.1.114:554/stream"
    enabled: true
```

#### 默认凭据：
- 用户名：`admin`
- 密码：`1234`（默认）

#### 已知问题：
- ONVIF 支持良好，但 RTSP 格式各异
- 部分型号需要手动配置
- 文档可能已过时

## 有限支持 / 特殊情况

### 16. Xiaomi

**型号**: C200, C300, Xiaofang, Dafang

#### 特殊配置：
- 使用 MiBee NVR 内建的 Xiaomi 集成方式，而非直接使用 RTSP
- 通过 Web UI → Xiaomi Device Discovery 配置
- 通过小米云服务进行认证

#### 配置：
```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  region: "cn"

cameras:
  - id: "xiaomi_c200"
    name: "Xiaomi C200"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_here"
    vendor: "cs2"
    enabled: true
```

#### 已知问题：
- 不兼容直接 RTSP 连接
- 需要连接小米云服务
- 仅支持 P2P 协议，无法通过 IP 直接访问

### 17. BESDER

**型号**: BCS-SR-505, BCS-SR-608, BCS-SR-702

#### RTSP URL：
- 主流: `rtsp://user:pass@ip:554/stream`
- 子流: `rtsp://user:pass@ip:554/sub`
- 音频流: `rtsp://user:pass@ip:554/audio`

#### 配置：
```yaml
cameras:
  - id: "besder_main"
    name: "BESDER Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password@192.168.1.115:554/stream"
    enabled: true
```

#### 已知问题：
- 通常兼容 ONVIF，但质量参差不齐
- RTSP 格式在不同型号间缺乏统一标准
- 部分型号通过 RTSP 传输的视频质量较差
### 18. 通用 RTSP

**型号**: 各种国产品牌、白标摄像头

#### RTSP URL：

- 常见格式：`rtsp://user:pass@ip:554/stream`
- 替代格式：`rtsp://user:pass@ip:554/live`
- 另一种格式：`rtsp://user:pass@ip:554/h264`

#### 配置：

```yaml
cameras:
  - id: "generic_rtsp"
    name: "Generic RTSP Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password@192.168.1.116:554/stream"
    enabled: true
```

#### 已知问题：

- RTSP 格式因制造商而异
- URL 格式需要试错调整
- ONVIF 发现功能可能无法使用

### 19. 通用 ONVIF

**型号**: 各种支持 ONVIF 的摄像头

#### 配置：

```yaml
cameras:
  - id: "generic_onvif"
    name: "Generic ONVIF Camera"
    protocol: "onvif"
    url: "http://192.168.1.117:80/onvif/device_service"
    username: "admin"
    password: "password"
    enabled: true
```

#### 已知问题：

- 使用 MiBee 的 ONVIF 扫描功能进行自动发现
- 如果自动发现失败，可能需要手动配置
- ONVIF 配置文件可能因型号而异

### 20. Wyze

**型号**: Wyze Cam v3, Wyze Cam Outdoor, Wyze Cam Pan

#### 特殊设置：

- 通过自定义固件提供有限的 RTSP 支持
- 需要 Wyze RTSP 分支或等效方案
- 主要基于云服务，不适合 NVR

#### 配置：

```yaml
cameras:
  - id: "wyze_rtsp"
    name: "Wyze RTSP Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password@192.168.1.118:554/live"
    enabled: true
```

#### 已知问题：

- 未获得 Wyze 官方支持
- 需要特定的固件修改
- 使用自定义固件时性能可能较差

## 通用配置技巧

### RTSP URL 测试

在添加到 MiBee NVR 之前，使用 VLC 或 ffplay 测试 RTSP URL：

```bash
# 测试 RTSP 连接
ffplay rtsp://admin:password@192.168.1.100:554/stream

# 测试 HTTP JPEG
curl http://admin:password@192.168.1.101:8080/snapshot

# 检查摄像头网络连通性
ping 192.168.1.100

# 测试 ONVIF 发现
nmap -p 80 192.168.1.102
```

### 性能优化

对于 Raspberry Pi 用户，针对有限资源优化设置：

```yaml
cameras:
  - id: "optimized_camera"
    name: "Optimized Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password@192.168.1.100:554/stream"
    enabled: true
    # 针对 Raspberry Pi 优化
    segment_duration: "30s"
    hls_max_fps: 15
    sample_interval: 2
```

### 网络注意事项

- 确保摄像头和 MiBee NVR 在同一网络中
- 考虑使用 VLAN 隔离以提高安全性
- 使用静态 IP 地址确保连接可靠
- 监控多台摄像头的带宽使用情况

### 安全最佳实践

- 修改摄像头默认密码
- 每台摄像头使用不同的凭据
- 启用防火墙规则限制摄像头访问
- 定期更新摄像头固件
- 考虑为摄像头 Web 界面启用 HTTPS

## 故障排除

### 常见 RTSP 问题

**"RTSP 连接失败"**：

```bash
# 检查摄像头是否在线
ping 192.168.1.100

# 手动测试 RTSP
ffplay rtsp://admin:password@192.168.1.100:554/stream

# 检查摄像头端口是否可访问
nc -zv 192.168.1.100 554
```

**"无视频流可用"**：

- 确认摄像头设置中已启用 RTSP
- 检查摄像头是否需要身份验证
- 尝试不同的流格式（主码流/子码流）
- 使用 VLC 测试 URL 确认流是否正常

### ONVIF 发现问题

**摄像头未被 ONVIF 发现**：

```yaml
# 手动 ONVIF 配置
cameras:
  - id: "manual_onvif"
    name: "Manual ONVIF Camera"
    protocol: "onvif"
    url: "http://192.168.1.100:80/onvif/device_service"
    username: "admin"
    password: "password"
    enabled: true
```

**ONVIF 身份验证失败**：

- 检查 ONVIF 端口（通常为 80，但也可能不同）
- 确认摄像头支持 ONVIF
- 尝试不同的身份验证方法
- 检查摄像头固件版本是否支持 ONVIF

### 音频配置

**录制中无音频**：

```yaml
# 在摄像头配置中启用音频
cameras:
  - id: "camera_with_audio"
    name: "Camera with Audio"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password@192.168.1.100:554/stream"
    enabled: true
    # 音频设置
    enable_audio: true
    audio_codec: "aac"  # 或 pcm, g711
    audio_bitrate: "128k"
```

**音频同步问题**：

- 检查音频流是否与视频流分离
- 如有可用选项，调整音频同步设置
- 部分摄像头不支持 RTSP 流中的音频
- 如有需要，考虑单独录制音频

## 支持资源

### 文档

- [MiBee NVR 配置指南](./configuration.md)
- [MiBee NVR API 参考](./api/README.md)
- [MiBee NVR 入门指南](./getting-started.md)

### 社区支持

- GitHub Issues：[MiBee NVR Issues](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues)
- 讨论区：[MiBee NVR Discussions](https://github.com/Mi-Bee-Studio/MiBeeNvr/discussions)
- Discord：加入我们的社区服务器获取帮助

### 专业支持

- 为企业用户提供商业支持
- 联系我们进行自定义摄像头集成
- 针对大型部署提供现场咨询

## 摄像头发现工具

### ONVIF Device Manager

下载并安装 [ONVIF Device Manager](https://www.onvif.org/)，可用于：

- 发现网络中的 ONVIF 摄像头
- 获取详细的摄像头信息
- 测试 ONVIF 合规性
- 下载摄像头能力信息

### iSpy

[iSpy](https://www.ispyconnect.com/) 可帮助发现摄像头并生成 RTSP URL：

- 自动发现摄像头
- 生成 RTSP URL
- 测试流兼容性
- 摄像头配置管理

### FFMPEG RTSP URL 测试工具

创建简单的脚本来测试 RTSP URL：

```bash
#!/bin/bash

# 测试 RTSP URL 函数
test_rtsp_url() {
    local url="$1"
    local timeout=10
    
    echo "正在测试 RTSP URL: $url"
    
    # 使用超时进行测试
    timeout $timeout ffmpeg -i "$url" -t 1 -f null - 2>/dev/null && {
        echo "✅ RTSP URL 可用: $url"
        return 0
    } || {
        echo "❌ RTSP URL 失败: $url"
        return 1
    }
}

# 使用示例
test_rtsp_url "rtsp://admin:password@192.168.1.100:554/stream"

# 批量测试多个 URL
for ip in 192.168.1.{100..120}; do
    test_rtsp_url "rtsp://admin:password@$ip:554/stream"
done
```

### 网络扫描工具

使用这些工具查找网络中的摄像头：

```bash

# 查找 ONVIF 摄像头
nmap -p 80 --open 192.168.1.0/24 | grep -v "Nmap scan"

# 查找 RTSP 服务器
nmap -p 554 --open 192.168.1.0/24 | grep -v "Nmap scan"

# 查找 HTTP 摄像头
nmap -p 80,8080 --open 192.168.1.0/24 | grep -v "Nmap scan"

# 快速发现摄像头
sudo nmap -sV -p 554,80,8080 --open 192.168.1.0/24
```

## 推流接入 (SRT / RTMP 收推) — 跨网络接入

与上面的拉流协议（RTSP/ONVIF/HTTP，NVR 主动拨出连接摄像头）不同，**推流接入**是反过来的：远端发布者（ffmpeg、OBS、手机、另一台 NVR）把流**推入** NVR。这让你能录制**不同网络**（另一个局域网、跨互联网、NAT 后）的摄像头，无需 VPN —— 只要发布者能连到 NVR 的公网 IP（或做了端口转发的端口）。

这是连接 NVR 无法直接拨出的摄像头的推荐方式。

详细架构设计请参见[架构文档](./architecture.md)。

### 前置条件

- NVR 的 SRT（UDP `9000`）和/或 RTMP（TCP `1935`）端口必须可从发布者网络访问。如果 NVR 在 NAT/路由器后，需做**端口转发**。
- 发布者需要 ffmpeg、OBS、GStreamer 或任何能推 RTMP/SRT 的工具。

### 启用接入服务

在 `mibee-nvr.yaml`（或通过设置页面）：

```yaml
srt:
  enabled: true
  port: 9000

rtmp:
  enabled: true
  port: 1935
```

### 添加 RTMP 推流摄像头

```yaml
cameras:
  - id: "remote-shop"
    name: "远程店铺摄像头"
    protocol: "rtmp"
    encoding: "h264"
    stream_key: "remote-shop"   # 推流地址的最后一段
    push_retention_days: 7      # nil=跟随全局, 0=仅直播, N=保留N天
    enabled: true
```

从远端推流（ffmpeg 示例，替换 `NVR_IP`）：

```bash
ffmpeg -rtsp_transport tcp -i "rtsp://admin:pass@192.168.1.50:554/stream" \
  -c copy -f flv "rtmp://NVR_IP:1935/live/remote-shop"
```

### 添加 SRT 推流摄像头

```yaml
cameras:
  - id: "garage-cam"
    name: "车库摄像头"
    protocol: "srt"
    encoding: "h264"
    srt_stream_id: "garage-cam"
    srt_passphrase: "my-secret"   # 可选 AES 加密
    enabled: true
```

```bash
ffmpeg -rtsp_transport tcp -i "rtsp://admin:pass@192.168.1.60:554/stream" \
  -c copy -f mpegts "srt://NVR_IP:9000?streamid=garage-cam&passphrase=my-secret"
```

SRT 使用 UDP，部分家宽运营商会封锁或难以转发。如果 SRT 连不上，改用 RTMP（TCP），它穿透家宽 NAT/防火墙更可靠。

### 限制说明

- **SRT/RTMP 当前仅支持 H.264**。SRT 的 MPEG-TS 解复用器和经典 RTMP 都只承载 H.264。
- **发布者连接前摄像头显示离线** —— 推流摄像头的预期行为。
- **需要端口转发/公网 IP**。推流接入本身不穿透 NAT。如两端都不可达，用 overlay 网络（Tailscale/WireGuard）或 MediaMTX 做中转。
- **音频**：RTMP/SRT 推流当前仅录制视频。

## 推流转发 (Push-Out Relay) — 把摄像头转发到远端

推流转发（relay）是推流接入的反向：NVR 把某个摄像头的实时流**转发出去**到远端目标 —— 另一台 NVR 的 RTMP/SRT 接入、直播平台、备份服务器。**默认使用内置 Go 中继**（可选 FFmpeg 兼容模式） —— 转发使用现有的 `gortsplib`/`gortmplib` 库进程内完成。

适用于：把摄像头画面送到跨互联网的远端 NVR、镜像到备份站点、发布到直播平台 —— 全部不需要外部进程。

### 工作原理

每个转发目标订阅摄像头已有的 `StreamHub`（录像和直播共用的同一帧总线）。帧被重新封装（不解码、不重编码）并通过专用的 RTMP 或 RTSP 连接写入目标。每个目标在独立 goroutine 中运行，有独立重连和码率统计。

- **零拷贝源**：不重新拉流、不解码 —— 添加一个转发目标只增加一个 goroutine + 一个出站连接（RPi 3B 上约 5-10MB）。
- **H.265 自动转码**：RTMP 目标要求 H.264 源（RTMP 不承载 H.265）。`transcode_policy` 为 `auto` 时，H.265 源会自动转码为 H.264 再转发（见《转发指南》relay-guide.md）；H.264 源直接转发，零转码开销。
- **每摄像头多目标**：一个摄像头可同时推到多个目标。

### 配置转发目标

给**任意摄像头**（RTSP、ONVIF、小米、甚至推流接入的摄像头）添加 `push_targets`：

```yaml
cameras:
  - id: "front-door"
    protocol: "rtsp"
    url: "rtsp://admin:pass@192.168.1.50/stream"
    push_targets:
      - id: "backup-nvr"
        name: "备份 NVR"
        protocol: "rtmp"
        url: "rtmp://backup.example.com:1935/live/front-door"
        enabled: true
      - id: "live-platform"
        name: "直播平台"
        protocol: "rtsp"
        url: "rtsp://live.example.com:8554/front-door"
        enabled: false
    enabled: true
```

或通过 Web UI：编辑摄像头 → 展开「转推输出」区域 → 添加目标（名称、协议、URL、启用开关）。

### 监控转发状态

摄像头卡片显示活跃/总数目标徽标（如 `↑ 2/2`）。编辑表单中每个目标显示实时状态药丸（推流中 + kbps / 连接中 / 重连中 / 错误）。API 端点 `GET /api/cameras/{id}/push-status` 返回每个目标的运行时状态。

### 跨网络 NVR 互传示例

最常见的场景 —— 把摄像头从一台 NVR 送到另一台，跨互联网，**无 FFmpeg**：

```
[摄像头] ──RTSP──▶ [NVR-A] ──转推 (RTMP)──▶ [NVR-B (推流接入)] ──▶ 录像 + 直播
```

1. 在 **NVR-B**（接收方）：启用 RTMP 接入，添加一个推流摄像头，设好 stream key（如 `front-door-relay`）。
2. 在 **NVR-A**（发送方）：给源摄像头添加 `push_target`，指向 `rtmp://NVR-B-IP:1935/live/front-door-relay`。
3. NVR-A 的转发引擎连接到 NVR-B 并转发帧。NVR-B 像本地摄像头一样录像和提供直播。

### 限制说明

- **RTMP 目标要求 H.264 源**。`transcode_policy` 为 `auto` 时 H.265 源会自动转码为 H.264（见《转发指南》relay-guide.md），H.264 源直接转发。
- **音频转发**：AAC 音频直通；仅有 G.711 时按目标平台要求处理（见 relay-guide.md）。
- **目标必须可达**：转发是拨出连接，目标的 RTMP/RTSP 端口必须可访问（NAT 后需端口转发）。
- **弹性重连**：源摄像头或目标连接断开时，转发以指数退避自动重试并恢复。

通过这份全面的摄像头品牌兼容性指南，您可以成功地将各种 IP 摄像头与 MiBee NVR 集成，确保监控系统获得最佳的录制性能和可靠性。
