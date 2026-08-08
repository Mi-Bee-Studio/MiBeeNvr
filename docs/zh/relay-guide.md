# Relay Guide — RTMP 直播平台转发

> 将摄像头流转发到直播平台（哗哩哗啦、YouTube、抖音、快手）的自动编码优化实现。默认纯 Go 中继——已兼容所有测试过的严格 FMS 接收端（含斗鱼直播伴侣）；FFmpeg 模式仅作为异构平台的兼底。

## 在 Web UI 中设置转推（推荐，无需编辑配置文件）

大多数用户只需要在网页界面操作。下面以把一个摄像头推到 **哔哩哔哩直播** 为例。

### 第 1 步：拿到目标平台的推流地址（含直播密钥）

在直播平台（如 [哔哩哔哩直播创作中心](https://link.bilibili.com/p/center/index)）开通直播间后，平台会给你一个**推流地址**和一个**直播密钥（stream key）**。两者拼起来才是完整的推流 URL：

- 哔哩哔哩：地址 `rtmp://live-push.bilivideo.com/live-bvc/`，密钥形如 `?streamkey=bvc_live_xxxxxx` → 完整 URL = `rtmp://live-push.bilivideo.com/live-bvc/?streamkey=bvc_live_xxxxxx`
- 抖音：`rtmp://live-push.douyin.com/你的密钥`
- YouTube：`rtmp://a.youtube.com/live2/你的密钥`
- 快手：`rtmp://txyun-push.voipimgs.com/gifshow/你的密钥`

> **关键**：RTMP 的\"直播密钥/key\"就是 URL 路径的最后一段，**填在同一个 URL 框里**，没有单独的 key 输入框。

### 第 2 步：在 NVR 网页里添加转推目标

1. 打开 NVR 网页（默认 `http://<NVR的IP>:9090`）并登录。
2. 进入「**摄像头**」页面，找到要转推的摄像头，点「**编辑**」。
3. 向下滚动找到「**转推输出**」区块并展开，点「**添加目标**」。
4. 填写：
   - **名称**：随便起，如「哔哩哔哩直播」。
   - **协议**：选 `RTMP`（绝大多数直播平台用 RTMP；RTSP 用于推到另一台服务器/NVR）。
   - **平台**：可选「哔哩哔哩 / 抖音 / YouTube / 快手 / 通用」——选了会自动套用该平台的推荐编码参数（分辨率/码率/帧率等），不用手填。
   - **URL**：粘贴第 1 步拼好的**完整推流地址**（含密钥）。下方会实时显示这个地址，可点复制核对。
   - **启用**：勾选才会真正开始推流（先不勾，确认无误再勾上也行）。
5. 点页面底部「**保存**」。提示「已更新」即成功。

### 第 3 步：查看推流状态 / 复制地址

- 保存后回到「摄像头」列表，该摄像头卡片上会显示「**1/1**」转推徽章。点它弹出浮层：
  - 看到每个目标的名称、协议、完整地址、实时状态（`streaming` + 码率 = 推流成功）。
  - 点复制按钮可一键复制推流地址。
- 如果显示 `error`：检查 URL 是否拼对、密钥是否过期、目标平台是否已开播。详见文末「故障排除」。

### 常见疑问

- **\"URL 输入框到底是什么意思？\"** —— 它就是目标平台给你的**完整推流地址**（含协议头 `rtmp://` 或 `rtsp://`、服务器、应用名、直播密钥）。填好后下方的实时预览就是最终用来推流的地址。
- **\"RTMP 的 key 在哪里设？\"** —— 没有单独的 key 输入框，key 就是 URL 最后一段路径。例如 `rtmp://live-push.bilivideo.com/live-bvc/?streamkey=你的key`。
- **H.265 摄像头能推吗？** —— 可以。直播平台基本只收 H.264，NVR 会自动把 H.265 转码成 H.264 再推（需要开启转码策略 `auto`，M5 有硬件转码）。H.264 摄像头则直接转发，零转码开销。
- **推流有声音吗？** —— 有。摄像头提供 AAC 音频时直通；只有 G.711 时 NVR 会自动转成静默或兼容音频，无需设置。

---

## 方法二：直接编辑配置文件（高级）

如果偏好编辑 YAML（如批量配置、脚本化管理、无 Web UI 的部署），下面是手动配置方法。

### 3 步设置：摄像头 → 哔哩哔哩直播

1. **配置摄像头到 `mibee-nvr.yaml`：**
```yaml
cameras:
  - id: "front-door"
    name: "前门摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
```

2. **添加哔哩哔哩转发目标：**
```yaml
cameras:
  - id: "front-door"
    name: "前门摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    push_targets:
      - id: "bilibili-live"
        name: "哔哩哔哩直播"
        protocol: "rtmp"
        url: "rtmp://live-push.bilivideo.com/live-bvc/?streamkey=YOUR_STREAM_KEY"
        enabled: true
        platform: "bilibili"
        transcode_policy: "auto"
```

3. **启动并验证：**
```bash
# 启动 MiBee NVR
./mibee-nvr -config mibee-nvr.yaml

# 检查转发状态
curl -u admin:PASSWORD http://localhost:9090/api/cameras/front-door/push-status

# 在哔哩哔哩直播仪表板验证流
```

## 支持的平台

| 平台 | URL 模式 | 音频编码 | 视频编码 | 备注 |
|------|----------|----------|----------|------|
| **哔哩哔哩** | `rtmp://live-push.bilivideo.com/live-bvc/` | AAC 必须 | 仅 H.264 | 4000kbps, 1920x1080, main profile, 2 B-帧 |
| **抖音** | `rtmp://live-push.douyin.com/` | AAC 必须 | 仅 H.264 | 3500kbps, **1080x1920 (竖屏)**, main profile, 0 B-帧 |
| **YouTube** | `rtmp://a.youtube.com/live2/` | AAC 必须 | 仅 H.264 | 4500kbps, 1920x1080, high profile, 2 B-帧 |
| **快手** | `rtmp://txyun-push.voip.yximgs.com/gifshow/` | AAC 必须 | 仅 H.264 | 4000kbps, 1920x1080, main profile, 2 B-帧 |
| **通用** | 自定义 URL | AAC 或 G.711 | 仅 H.264 | 3000kbps, 1920x1080, main profile, 0 B-帧 |

> **重要提示：**
> - RTMP 目标需要 H.264 源视频
> - 音频：AAC 优先，G.711 μ-law/a-law 作为回退
> - H.265 源自动转码为 H.264

## 平台预设

### 5 个内置预设

系统包含 5 个为主要平台优化的预设：

```go
// 哔哩哔哩：直播平衡质量
{
    Name: "bilibili",
    Description: "哔哩哔哩直播",
    URLHint: "rtmp://live-push.bilivideo.com/live-bvc/",
    GopSeconds: 2,
    VideoBitrateKbps: 4000,
    AudioBitrateKbps: 128,
    Resolution: "1920x1080",
    Framerate: 30,
    Profile: "main",
    Bframes: 2,
    AudioCodecRequired: "aac"
}

// 抖音：竖屏视频格式
{
    Name: "douyin",
    Description: "抖音/TikTok 直播",
    URLHint: "rtmp://live-push.douyin.com/",
    GopSeconds: 2,
    VideoBitrateKbps: 3500,
    AudioBitrateKbps: 128,
    Resolution: "1080x1920", // 竖屏格式
    Framerate: 30,
    Profile: "main",
    Bframes: 0, // 0 B-帧实现最低延迟
    AudioCodecRequired: "aac"
}

// YouTube：高质量 high profile
{
    Name: "youtube",
    Description: "YouTube 直播",
    URLHint: "rtmp://a.youtube.com/live2/",
    GopSeconds: 2,
    VideoBitrateKbps: 4500,
    AudioBitrateKbps: 128,
    Resolution: "1920x1080",
    Framerate: 30,
    Profile: "high", // YouTube high profile
    Bframes: 2,
    AudioCodecRequired: "aac"
}

// 快手：标准质量
{
    Name: "kuaishou",
    Description: "快手直播",
    URLHint: "rtmp://txyun-push.voip.yximgs.com/gifshow/",
    GopSeconds: 2,
    VideoBitrateKbps: 4000,
    AudioBitrateKbps: 128,
    Resolution: "1920x1080",
    Framerate: 30,
    Profile: "main",
    Bframes: 2,
    AudioCodecRequired: "aac"
}

// 通用：未知平台的回退
{
    Name: "generic",
    Description: "通用 RTMP 目标",
    URLHint: "", // 必须在配置中提供
    GopSeconds: 2,
    VideoBitrateKbps: 3000,
    AudioBitrateKbps: 128,
    Resolution: "1920x1080",
    Framerate: 30,
    Profile: "main",
    Bframes: 0, // 保守设置
    AudioCodecRequired: "any" // 接受 AAC 和 G.711
}
```

### 通过 `relay-presets.yaml` 自定义预设

在部署目录创建 `relay-presets.yaml` 来覆盖内置预设或添加新的：

```yaml
# deploy/relay-presets.yaml
presets:
  custom_platform:
    name: custom_platform
    description: "我的自定义直播平台"
    url_hint: "rtmp://live.mycustomplatform.com/"
    gop_seconds: 2
    video_bitrate_kbps: 5000
    audio_bitrate_kbps: 128
    resolution: "1920x1080"
    framerate: 30
    profile: "main"
    bframes: 2
    audio_codec_required: "aac"

  low_latency:
    name: low_latency
    description: "低延迟游戏直播"
    url_hint: "rtmp://gaming-platform.com/live/"
    gop_seconds: 1  # 更短的 GOP 实现低延迟
    video_bitrate_kbps: 3000
    audio_bitrate_kbps: 128
    resolution: "1280x720"
    framerate: 60
    profile: "baseline"  # baseline 模式降低延迟
    bframes: 0
    audio_codec_required: "aac"
```

> **配置加载：** 如果存在，系统会从 `relay-presets.yaml` 加载自定义预设。如果文件丢失、无效或为空，自动回退到 5 个内置预设。

## 音频处理

### AAC 直通

- **最佳质量：** 来自摄像头的 AAC 音频原样直通
- **兼容性：** 所有主要平台（哔哩哔哩、YouTube、抖音、快手）都支持
- **设置：** 摄像头提供 AAC 音频时自动启用

### G.711 直通（有限支持）

- **μ-law/α-law：** 来自某些摄像头（主要是 ONVIF 和小米摄像头）的 G.711 音频
- **尽力而为：** 某些平台可能接受 G.711，其他可能拒绝
- **回退：** 如果被拒绝，生成静默 AAC（见下文）

### 静默 AAC 回退

**目的：** 当摄像头没有音频或 G.711 被平台拒绝时

**工作原理：**
1. **检测：** 系统检测缺失音频或仅 G.711 源
2. **生成：** 创建 48kHz 立体声静默 AAC 帧
3. **自适应速率：** 限制为每秒 5 帧（音频缓冲区容量的 10%）
4. **丢包保护：** 如果发生网络拥堵，自动降低输出

**配置：** 自动 - 无需手动设置

**技术细节：**
- AudioSpecificConfig：标准 48kHz 立体声 AAC-LC
- 帧大小：每静默帧 6 字节
- 采样率：48kHz，声道数：2（立体声）
- 缓冲区管理：非阻塞，防止音频缓冲区溢出

## H.265 转码

### 何时启用转码

当以下条件满足时，系统会自动将 H.265 源转码为 H.264：

1. **源摄像头：** 您的摄像头提供 H.265 视频
2. **转码策略：** `transcode_policy: "auto"` 或 `"force_sw"`
3. **注册表可用：** 已配置平台预设

```yaml
cameras:
  - id: "h265-camera"
    name: "H.265 摄像头"
    protocol: "rtsp"
    encoding: "h265"  # H.265 源
    url: "rtsp://camera/h265-stream"
    push_targets:
      - id: "youtube-transcode"
        protocol: "rtmp"
        url: "rtmp://a.youtube.com/live2/"
        enabled: true
        platform: "youtube"
        transcode_policy: "auto"  # 将 H.265 转码为 H.264
```

### 硬件要求

**香蕉派 M5 生产服务器：**
- **硬件加速：** 使用 v4l2m22 编解码器（供应商内核）
- **CPU 回退：** 在主线内核上回退到 libx264
- **性能：** 1080p@30fps 硬件转码，CPU 使用率 < 10%
- **内存：** 转码约 50MB RAM 开销

**树莓派 3B：**
- **仅软件：** 使用 libx264（无硬件加速）
- **CPU 使用率：** 1080p@30fps 时约 30-40% CPU
- **内存：** 约 100MB RAM 开销
- **建议：** 对于可靠性能，使用 720p 或更低分辨率

### 温度管理

**香蕉派 M5 温度保护：**
- **节流阈值：** 85°C → 自动降级到 480p 分辨率
- **关机阈值：** 95°C → 停止转码，触发重连
- **监控：** 通过 Prometheus 指标实时监控
- **散热：** 确保持续转码时有足够的散热

**配置：**
```yaml
# 系统全局温度限制（可选覆盖）
relay:
  thermal_limit: 85  # 摄氏节流阈值（默认：85）
```

### 转码策略

| 策略 | 描述 | 使用场景 |
|------|------|----------|
| `"auto"` | 硬件可用时使用硬件，否则软件 | 性能和质量的最佳平衡 |
| `"force_sw"` | 始终使用软件转码 | 测试，或硬件有 bug 时 |
| `"off"` | 用永久错误拒绝 H.265 源 | 仅用于仅 H.264 工作流 |

## 手动平台测试

### 分步测试：哔哩哔哩

1. **创建哔哩哔哩直播账号：**
   - 访问 [哔哩哔哩直播创作中心](https://link.bilibili.com/p/center/index)
   - 创建直播房间
   - 复制您的直播密钥（以 "live-bvc-" 开头）

2. **配置 NVR：**
```yaml
cameras:
  - id: "test-camera"
    name: "哔哩哔哩测试摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://your-camera-ip:554/stream"
    enabled: true
    push_targets:
      - id: "bilibili-test"
        name: "哔哩哔哩测试直播"
        protocol: "rtmp"
        url: "rtmp://live-push.bilivideo.com/live-bvc/?streamkey=YOUR_STREAM_KEY"
        enabled: true
        platform: "bilibili"
        transcode_policy: "auto"
```

3. **启动流：**
```bash
# 启动 NVR
./mibee-nvr -config mibee-nvr.yaml

# 等待 30 秒让摄像头连接
```

4. **验证流：**
   - 检查哔哩哔哩仪表板中的实时预览
   - 验证流是否出现在您的直播间
   - 检查视频和音频是否都正常工作

5. **检查分析：**
   - 监控观看人数和互动
   - 在哔哩哔哩上检查流质量指标
   - 监控 NVR 日志中的转发状态

6. **停止测试：**
```bash
# 禁用目标以停止直播
# 编辑 mibee-nvr.yaml 将 bilibili-test 的 enabled: false
# 重启 NVR 或重新加载配置
```

### 验证命令

```bash
# 检查摄像头状态
curl -s http://localhost:9090/api/cameras/test-camera | jq '.status.recording_status'

# 检查转发状态
curl -s http://localhost:9090/api/cameras/test-camera | jq '.push_targets[]'

# 查看 NVR 日志
tail -f /var/log/mibee-nvr.log | grep relay

# 检查流密钥认证（如果适用）
curl -H "Authorization: Bearer YOUR_KEY" -X GET https://api.bilibili.com/x/web-interface/nav
```
```

## FFmpeg 转发模式（兼容性）

原生 Go 中继使用**自定义 RTMP 握手 + 发布层**（`internal/relay/rtmp_client.go`）——而非 `gortmplib.Client`——因为严格的 FMS 兼容接收端（斗鱼直播伴侣、虎牙、哗哩哗啦）会拒绝 `gortmplib` 发出的简单握手、Type 1/2/3 chunk header 以及不完整的 `onMetaData`。该自定义层实现了：复杂握手 HMAC-SHA256 digest、`SetChunkSize=4096`、每条出站消息强制 Type 0 chunk header、从 SPS 解析出的完整 FFmpeg 风格 `onMetaData`（宽/高/帧率）、以及大端序 MessageStreamID。六大根因全部解决——原生 Go 路径现在适用于所有已测试平台（斗鱼/哗哩哗啦/YouTube/SRS）。

`use_ffmpeg` 保留作为异构或未测试平台的可选兼底配置。FFmpeg 经过数十年的 RTMP 兼容性打磨，若未来出现原生层无法满足的接收端，它是稳妥的后备方案。

### 何时使用 FFmpeg 转发

| 平台 | 原生 Go | FFmpeg 转发 | 建议 |
|------|---------|------------|------|
| 哗哩哗啦 | ✅ 可用 | ✅ 可用 | 原生（CPU 更低） |
| YouTube | ✅ 可用 | ✅ 可用 | 原生（CPU 更低） |
| 抖音/TikTok | ⚠️ 未测试 | ✅ 可用 | 原生优先（FFmpeg 兜底） |
| **斗鱼（直播伴侣）** | **✅ 可用** | **✅ 可用** | **原生**（已解决——自定义握手层） |
| 快手 | ⚠️ 未测试 | ✅ 可用 | 原生优先（FFmpeg 兜底） |
| 通用 RTMP | ✅ 可用 | ✅ 可用 | 原生（CPU 更低） |

> **技术说明：** 原生 Go RTMP 路径使用**自定义握手 + 发布层**（`internal/relay/rtmp_client.go`），而非 `gortmplib` 的标准写入器。它对每条消息强制使用 Type 0 chunk header（gortmplib 会发出 Type 1/2/3 优化，严格接收端无法解析），计算 FMS 兼容接收端所需的复杂握手 HMAC-SHA256 digest，并发送完整的 FFmpeg 风格 `onMetaData`（从 SPS 解析出宽/高/帧率）。这正是斗鱼直播伴侣——之前唯一需要 FFmpeg 的平台——现在能原生推流的原因。`use_ffmpeg` 仅作为未来未知接收端的遇险手段保留。

### 配置方法

```yaml
cameras:
  - id: "front-door"
    push_targets:
      - id: "douyu-live"
        name: "斗鱼直播"
        protocol: "rtmp"
        url: "rtmp://192.168.1.10:1935/live/stream_key"
        enabled: true
        use_ffmpeg: true        # ← 启用 FFmpeg 转发
        # source_url: ""        # 可选：覆盖自动解析的源地址
```

启用 `use_ffmpeg: true` 后：
- 转发器启动 `ffmpeg -rtsp_transport tcp -i <摄像头地址> -c copy -f flv <目标地址>`
- 摄像头的 RTSP 地址会**自动从录像器解析**（ONVIF 摄像头使用解析后的 RTSP 地址；
  RTSP 摄像头直接使用配置 URL）
- 可选的 `source_url` 可覆盖自动解析的地址（适用于非 RTSP 摄像头）
- FFmpeg 子进程的生命周期与转发目标绑定（停止/禁用时自动终止）

### 前提条件

- NVR 服务器上必须安装 FFmpeg（`apt install ffmpeg` 或等效命令）
- 通过 API 检查可用性：
```bash
curl -u admin:password http://localhost:9090/api/relay/capabilities
# {"ffmpeg_available": true, ...}
```
- Web 界面仅在检测到 FFmpeg 时显示 `use_ffmpeg` 选项

### 对比

| 方面 | 原生 Go（自定义写入器） | FFmpeg 转发 |
|------|---------------------|------------|
| CPU 占用 | 更低（零拷贝转封装） | 略高（FFmpeg 进程） |
| 内存 | 每目标约 2MB | 每目标约 20-40MB（FFmpeg） |
| 依赖 | 无额外依赖 | 需安装 FFmpeg |
| 兼容性 | 大部分平台 | 所有平台 |
| 音频支持 | 完整（AAC、G.711） | 仅视频（音频由平台混合） |


## 故障排除

### "连接被拒绝" 错误

**问题：** RTMP/RTSP 连接失败

**解决方案：**
```bash
# 1. 检查目标 URL 可访问性
curl -I "rtmp://live-push.bilivideo.com/live-bvc/"

# 2. 本地验证摄像头流
ffplay "rtsp://your-camera-ip:554/stream"

# 3. 检查 NVR 日志中的连接错误
journalctl -u mibee-nvr -f --since="5 minutes ago"

# 4. 验证网络连接
ping live-push.bilivideo.com
netstat -tlnp | grep 1935  # RTMP 端口
```

**配置修复：**
- 确保 URL 方案正确（`rtmp://` 或 `rtsp://`）
- 检查端口号和流密钥格式
- 验证平台预设名称完全匹配

### "编解码器被拒绝" 错误

**问题：** 音频/视频编解码器不被目标支持

**解决方案：**
```bash
# 检查来自摄像头的编解码器信息
curl -s http://localhost:9090/api/cameras/test-camera | jq '.recording_codec_info'

# 如果是 H.265 源且未启用转码：
# 在目标配置中添加 transcode_policy: "auto"
```

**常见问题：**
- **H.265 源未启用转码：** 添加 `transcode_policy: "auto"`
- **G.711 音频被拒绝：** 系统将回退到静默 AAC
- **错误的音频格式：** 确保摄像头提供 AAC 音频

### "温度关机" 错误

**问题：** 香蕉派 M5 转码过热

**解决方案：**
```bash
# 检查当前温度
curl -u admin:PASSWORD -s http://localhost:9090/api/cameras/front-door/push-status | jq '.targets[0].temperature_c'

# 监控温度指标
curl -s http://localhost:9090/api/metrics | grep 'nvr_relay_transcoder_temperature_c'

# 降低转码分辨率或比特率
# 或为系统添加散热
```

**预防措施：**
- 确保适当通风
- 监控环境温度
- 使用 `transcode_policy: "force_sw"` 减少 GPU 负载

### "音视频漂移" 问题

**问题：** 音频和视频失去同步

**症状：**
- 音频领先/滞后视频几秒
- 持续漂移且情况恶化

**解决方案：**
```bash
# 检查漂移监控日志
tail -f /var/log/mibee-nvr.log | grep 'AV drift'

# 如果漂移超过 1 秒持续 5 秒，系统会自动重连
# 通常不需要手动干预
```

**系统行为：**
- 实时监控 A/V 同步
- 如果漂移 > 1 秒持续 > 5 秒，自动重连
- 无需用户配置

### 常见错误消息

| 错误消息 | 解决方案 |
|----------|----------|
| `"source stream not ready"` | 摄像头断开连接或尚未开始流传输 |
| `"H.265 source with transcode_policy=off"` | 添加 `transcode_policy: "auto"` 或切换到 H.264 摄像头 |
| `"preset registry not configured"` | 确保 main.go 中 PresetRegistry 连接到 relay manager |
| `"failed to parse AudioSpecificConfig"` | 摄像头提供无效 AAC 音频，系统将回退到静默 AAC |
| `"thermal limit exceeded"` | 降低分辨率/比特率或改善散热 |

### "timed out after 15s waiting for transcoder SPS/PPS"

**原因：** 转码器（FFmpeg）已启动，但在 15 秒的 SPS/PPS 超时前未收到 H.265 输入帧。这是一个 bug，摄像头 hub 订阅在参数等待之后才设置，而非之前。

**已修复：** 当前版本。hub 订阅现在在转码器启动后立即开始向 FFmpeg 提供 H.265 帧，在等待输出参数之前。

**如果仍然出现：** 确认摄像头正在活跃推流（检查 `GET /api/cameras/{id}` → `status` 应为 `"recording"`）。转码器只有收到输入帧才能产生输出。

### `temperature_c` 始终显示 0

**原因：** 热监控器未接入 relay 引擎。`latestTemperatureC` 字段已声明但从未更新。

**已修复：** 当前版本。现在每个转码器实例启动时同时启动 `ThermalMonitor`，每 30 秒读取 `/sys/class/thermal/thermal_zone*/temp`。

**注意：** 当 RTMP 目标不可达时，转码器每 5-10 秒重启一次（连接重试周期）。由于热检查间隔为 30 秒，在快速重连周期中温度可能不会更新。一旦 RTMP 连接稳定，温度报告正常工作。
## 配置参考

### PushTargetConfig 字段

```go
type PushTargetConfig struct {
    ID                  string                `yaml:"id" json:"id"`                             // 摄像头内唯一目标 ID
    Name                string                `yaml:"name,omitempty" json:"name,omitempty"`    // 显示名称
    Protocol            string                `yaml:"protocol" json:"protocol"`                 // "rtmp" 或 "rtsp"
    URL                 string                `yaml:"url" json:"url"`                           // 目标 URL
    Enabled             bool                  `yaml:"enabled" json:"enabled"`                   // 目标是否激活
    Platform            string                `yaml:"platform,omitempty" json:"platform,omitempty"`                         // 预设名称（bilibili/douyin/youtube/kuaishou/generic）
    TranscodePolicy     string                `yaml:"transcode_policy,omitempty" json:"transcode_policy,omitempty"`         // "auto", "force_sw", "off"
    VideoPresetOverride *VideoPresetOverrides `yaml:"video_preset_override,omitempty" json:"video_preset_override,omitempty"`
}
```

### VideoPresetOverrides 字段

```go
type VideoPresetOverrides struct {
    Resolution       string `yaml:"resolution,omitempty" json:"resolution,omitempty"`       // "1920x1080", "1280x720" 等
    Framerate        int    `yaml:"framerate,omitempty" json:"framerate,omitempty"`        // 1-120 FPS
    VideoBitrateKbps int    `yaml:"video_bitrate_kbps,omitempty" json:"video_bitrate_kbps,omitempty"` // 100-50000 kbps
    GopSeconds       int    `yaml:"gop_seconds,omitempty" json:"gop_seconds,omitempty"`       // 1-10 秒
    Profile          string `yaml:"profile,omitempty" json:"profile,omitempty"`          // "baseline", "main", "high"
    Bframes          int    `yaml:"bframes,omitempty" json:"bframes,omitempty"`          // 0-2 (0=无, 1=1 个 B-帧, 2=2 个 B-帧)
}
```

### 全局配置

```yaml
# mibee-nvr.yaml

relay:
  # 自定义预设文件路径（可选）
  presets_path: "/etc/mibee-nvr/relay-presets.yaml"
  
  # 摄氏节流阈值（可选）
  thermal_limit: 85
```

### 验证规则

- **Platform：** 必须匹配 `^[a-zA-Z0-9_]+$`（字母数字 + 下划线）或为空
- **TranscodePolicy：** 必须为 "auto", "force_sw", "off" 或空
- **Resolution：** 必须为 WxH 格式（如 "1920x1080"）
- **Framerate：** 如果指定，必须为 1-120
- **VideoBitrateKbps：** 如果指定，必须为 100-50000
- **GopSeconds：** 如果指定，必须为 1-10
- **Profile：** 如果指定，必须为 "baseline", "main" 或 "high"
- **Bframes：** 必须为 0-2

### 完整配置示例

```yaml
# mibee-nvr.yaml - 完整转发配置

cameras:
  - id: "front-door"
    name: "前门摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    push_targets:
      # 哔哩哔哩直播流
      - id: "bilibili-stream"
        name: "哔哩哔哩直播"
        protocol: "rtmp"
        url: "rtmp://live-push.bilivideo.com/live-bvc/?streamkey=bvc_live_your_key"
        enabled: true
        platform: "bilibili"
        transcode_policy: "auto"
        video_preset_override:
          resolution: "1920x1080"
          framerate: 30
          video_bitrate_kbps: 4000
          gop_seconds: 2
          profile: "main"
          bframes: 2
      
      # YouTube 备份流
      - id: "youtube-backup"
        name: "YouTube 备份"
        protocol: "rtmp"
        url: "rtmp://a.youtube.com/live2/your_youtube_key"
        enabled: false  # 默认禁用
        platform: "youtube"
        transcode_policy: "auto"

  - id: "backyard-camera"
    name: "后院摄像头"
    protocol: "rtsp"
    encoding: "h265"  # 将自动转码
    url: "rtsp://192.168.1.101:554/stream"
    enabled: true
    push_targets:
      - id: "douyin-stream"
        name: "抖音直播"
        protocol: "rtmp"
        url: "rtmp://live-push.douyin.com/stream_key"
        enabled: true
        platform: "douyin"
        transcode_policy: "auto"
        # 不需要覆盖 - 使用 douyin 预设（1080x1920 竖屏）

# 可选的全局转发配置
relay:
  presets_path: "/etc/mibee-nvr/relay-presets.yaml"
  thermal_limit: 85
```

### 命令行示例

```bash
# 检查所有摄像头的转发状态
curl http://localhost:9090/api/cameras | jq '.[].push_targets[]'

# 检查特定摄像头转发状态
curl http://localhost:9090/api/cameras/front-door | jq '.push_targets[]'

# 监控转发指标
curl http://localhost:9090/api/metrics | grep 'nvr_relay_'

# 强制重启摄像头转发（通过切换 enabled 标志）
# 编辑配置，设置 enabled: false → true，重新加载配置

# 测试到目标平台的连接
ffmpeg -re -i test-video.mp4 -c:v copy -c:a aac -f flv "rtmp://live-push.bilivideo.com/live-bvc/?test_key"
```

### 系统集成

**Prometheus 指标：**
- `nvr_relay_target_status` - 目标流状态
- `nvr_relay_bitrate_kbps` - 当前比特率
- `nvr_relay_reconnects_total` - 重连次数
- `nvr_relay_transcoder_temperature_c` - 转码器温度（香蕉派 M5）
- `nvr_relay_transcoder_restarts_total` - 转码器重启次数
- `nvr_relay_transcoder_thermal_throttles_total` - 温度节流事件

**Systemd 服务：**
- 服务：`mibee-nvr.service`
- 日志：`journalctl -u mibee-nvr`
- 状态：`systemctl status mibee-nvr`

**健康检查：**
```bash
# 系统整体健康
curl http://localhost:9090/api/health

# 摄像头健康包括转发状态
curl http://localhost:9090/api/cameras/front-door/health
```

## 截图

*Web UI 中的转发配置界面*
![转发配置](docs/images/relay-config.png)

*实时转发状态仪表板*
![转发状态](docs/images/relay-dashboard.png)

*平台预设管理*
![预设管理](docs/images/relay-presets.png)

---

**下一步：**
- [配置参考](docs/zh/configuration.md) - 完整配置指南
- [API 参考](docs/zh/api/README.md) - REST API 文档
- [故障排除](docs/zh/troubleshooting.md) - 常见问题和解决方案