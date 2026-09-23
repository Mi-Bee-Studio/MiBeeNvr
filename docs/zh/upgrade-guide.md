# 升级指南

本指南覆盖 MiBee NVR 各版本间的升级路径与破坏性变更。**升级前务必备份数据库与配置文件。**

## 升级路径速查表

| 起始 → 目标 | 状态 | 需要的操作 |
|------------|------|----------|
| **0.12.x → 0.13.0** | 🟡 **需阅读安全收紧与默认值变化** | 若干端点移入鉴权组、`?token=` 透传移除；数据库 schema v40（不可降级）；HLS/MJPEG/合并默认值变化。详见 [0.12.x → 0.13.0](#012x--0130)。 |
| **0.11.x → 0.12.0** | 🟢 **透明升级（一处注意）** | 无破坏性 API 变更；数据库增量迁移自动完成；国标默认媒体传输改 TCP 被动。详见 [0.11.x → 0.12.0](#011x--0120)。 |
| **0.10.x → 0.11.0** | 🟡 **需阅读许可与 API 变更** | 许可证变更（AGPL-3.0）；一个 API 响应字段改名；纯 HTTP AAC 直播音频降级。详见 [0.10.x → 0.11.0](#010x--0110)。 |
| **v0.9.1 → v0.10.0** | 🟡 **需要操作** | 拆分组合 `protocol` 字符串；可选的磁盘回收。详见 [v0.9.1 → v0.10.0](#v091--v0100)。 |
| v0.9.0 → v0.9.1 | 🟢 透明升级 | 无需操作。 |
| v0.8.x → v0.9.x | 🟡 先备份 | 大型存储层重构。备份数据库后升级。 |
| v0.8.x → 0.10.0 | 🟡 两步走 | 先升级到 v0.9.x，再升级到 0.10.0。 |
| **< v0.9.x → v0.10.0** | 🔴 **不支持（直升级）** | **必须**先升级到 0.9.x —— 原因见 [下文](#低于-v09x--v0100不支持直升级)。 |

---

## 0.12.x → 0.13.0

0.13.0 是一次大版本发布：**对象存储冷备**（S3 兼容异步上传）、**分层录制 + 像素域门控**（智能编码器相机全链路方案）、**Windows / macOS 桌面版**、**裸机自动更新**（签名校验 + 失败自动回滚）、**I/O 预算调度**与**录像写卡顿根除**。数据库增量迁移自动完成，但以下行为变更需要知晓：

### 🔴 安全收紧（#879）

- **下载与回放端点移入鉴权路由组**：`/api/recordings/{id}/download`、`/api/recordings/{id}/merged`、延时合并下载、回放分段（`/api/cameras/{id}/playback/*`）、`/api/events`、`/api/health/cameras` 在 v0.12.x 可匿名访问，现在要求与其它 API 相同的凭证（Basic Auth / API Key / 会话 cookie / Bearer token 均可）。**用脚本或第三方工具匿名拉取这些端点的集成需要补上凭证**
- **移除 base64 旧版 `?token=` 透传**——请改用 `Authorization: Bearer` 会话 token、URL 签名 token 或 API Key
- `local_bypass` 增加跨站请求拒绝（`Sec-Fetch-Site: cross-site` 的浏览器请求不再免鉴权）；CSP `script-src` 收紧为内联哈希——仅影响向页面注入自定义脚本的魔改部署
- **无凭据首启**：未设置密码直接启动时，终端会打印一次性 setup 校验码，首跑向导需输入它完成初始化（本机环回访问豁免）

### 🔴 数据库 schema v33 → v40（不可降级）

首次启动自动增量迁移：新增相机分组（v36–v38）、`motion_confidence`（v34）、AI 事件来源（v35）、分层录制数据回填（v39）、`offload_outbox`（v40）。**迁移后无法回退到 v0.12.x 二进制**——升级前务必备份 `mibee-nvr.db` 与配置文件。

### 🟡 存储与合并默认值变化

- **HLS `low_latency` 默认改为开启**：未显式配置即低延迟 HLS（实际部署自该功能上线起就运行在 LL 模式，键此前是死的）；显式 `low_latency: false` 回到经典分段播放列表
- **MJPEG 录像段默认改为单文件 AVI 容器**（消除 SD/eMMC 每帧文件的元数据抖动）：存量目录形段仍可正常播放；相机级 `http_jpeg_avi` 键废弃（旧配置不报错），新键 `mjpeg_form: dir` 可显式回退；存量段可用 `mibee-nvr repair mjpeg-containerize` 一次性迁移
- **闪断碎片批量折叠默认开启**（`merge.rolling_fragment_hold_s` 未配置 = 300s）：闪断相机的碎片按批折叠而非逐个重写整桶——IO 负载显著下降属预期；显式设 `0` 关闭
- `merge.rolling_bucket_retain` 默认 `2`：画质摆动相机（如小米 HD/SD 切换）会各保留一个桶，磁盘占用略增；显式设 `1` 恢复旧单桶行为
- `merge.transcode_grace` 默认 `90s`：带转码任务相机的新鲜段延迟至多 90 秒才折叠（修复转码/合并竞态；设 `"0s"` 恢复立即折叠）
- **`cleanup` 校验收紧**：`disk_threshold_percent` 合法域 50–99、`retention_days` 1–3650，且 Web 设置与启动加载同源校验——v0.12.x 设置页存得进去的越界值（如 20%）升级后会被拒绝，另新增 last-good 配置备份启动恢复（这正是 v0.12 崩溃循环 bug 的修复）

### 🟡 其它行为变更

- `POST /api/auth/password` 不再要求旧密码——本机环回请求（桌面版菜单栏/托盘配套）可直接改密；远程仍需登录会话
- 第三方库 stdlib 日志限流默认每 10 秒 1 条（`observability.stdlog_throttle`），修复 journald 被刷爆；设 `off` 关闭
- FTP 未配置专用凭据时回退管理员账号并打印启动警告（明文协议建议配置独立弱权限凭据 `ftp.username` / `ftp.password`）
- 桌面版（Windows/macOS）默认只监听 `127.0.0.1` 且本机免密登录，不对局域网暴露；开放局域网需在托盘/菜单栏显式修改监听地址

### 🟢 新特性速览

- **对象存储冷备**：合并完成的录像异步上传到 S3 兼容存储（AWS S3 / MinIO / Cloudflare R2 / B2 / OSS / COS），上传校验后可安全逐出本地副本——见[对象存储冷备](storage-offload.md)
- **分层录制 + 像素域门控**：智能编码器相机（静态 ~0.5fps / 5MP VBR）子码流 24/7 连续基线 + 主码流按需、约 1fps 采样的经典 CV 细门控，不依赖相机侧配合——见[自适应录制](adaptive-recording.md)
- **Windows / macOS 桌面版**：单文件安装器（Setup.exe / DMG）、系统托盘与 macOS 菜单栏、本机免密——见[桌面版](desktop.md)
- **裸机自动更新**：release 产物 ed25519 签名校验、健康门失败自动回滚、Web 设置页一键升级与升级历史（`update.auto_apply` 默认关闭）
- **I/O 预算调度**：后台任务（合并 / 清理 / 修复 / 延时 / 转码 / offload）共享令牌桶，不再与前台平等争抢磁盘 IO；`io.recording_writes_budgeted` / `io.playback_reads_budgeted` 两个灰度开关默认关闭——见[性能调优](performance.md)
- **录像写卡顿根除**：MP4 段增量落盘（消除 Close 时全文件突发写）；每段独立写锁，相机间写盘不再互相串行化
- **内存自适应**：按物理内存 / cgroup 配额自动设置 GOMEMLIMIT，小内存板子不再 OOM（`memory:` 配置块）
- **全局录像默认开关** `recording.default_enabled`：纯直播部署一键关闭录像
- **相机分组**：Web UI 拖拽分组 / 排序 / 重命名（`/api/cameras/groups`）
- **RTSP 输出支持 MJPEG/JPEG**（RTP-JPEG）；H.264/H.265 最新帧截图（可选 FFmpeg）与 MQTT 快照触发
- **HMAC 签名 webhook 触发端点**：`POST /api/trigger/webhook/{camera_id}`，Stripe 风格签名 + 双向防重放——见 [Webhook 集成](webhook-integration.md)
- **MQTT 事件总线转发**（`mqtt.status_events`）与定时强制录像窗口（`{"action":"record","duration":"60s"}`）
- **ONVIF 相机侧移动侦测触发**（`motion_source: camera:onvif`）——见 [ONVIF 指南](onvif-guide.md)
- **Home Assistant 官方定制集成随包发布**（HACS 可装，mDNS 自动发现）——见[接入 Home Assistant](home-assistant.md)
- **Vision 多实例路由** + 推送熔断自动退避；AI 事件快照端点
- **延时摄影 MJPEG 双模可用** + `timelapse-merge` CLI 批量转换 + 合并历史播放——见[延时摄影](timelapse.md)
- **GB35114 A 级安全注册试点**（SM2 证书，需 `-tags gb35114` 构建）与 GB28181 级联按需主码流激活
- **`validate-config` CLI**、深层孤儿目录扫描、启动配置防呆（崩溃循环保险丝）

完整变更列表见 [GitHub Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases) 的 v0.13.0 说明。

---

## 0.11.x → 0.12.0

v0.12.0 带来**子码流体系**（按需拉流 + 全协议画质切换 + RTSP 输出）、**自适应录制**（动静感知 + 音频触发 + 活动评分）、**按相机存储与热迁移**与**流媒体全链路观测**（链路诊断树 / 延迟徽章 / 帧追踪）。对已有部署几乎是透明升级：

- **无破坏性 API 变更**；`recordings` 表新增 `motion_score` / `activity_flags` / `timeline_map` 三列，**首次启动自动增量迁移**，无需手动操作
- ⚠️ **GB28181 默认媒体传输改为 TCP 被动**（`tcp-passive`）——实测 UDP 约 16% 丢帧。已在 YAML 显式配置 `media_transport` 的部署不受影响；未配置的国标相机升级后改用 TCP 取流（海康/大华等主流设备均支持）
- 仅**从源码自行构建前端**需要 Node 24；直接使用发布产物的用户不受影响
- 新特性按相机显式开启（如 `recording_mode: adaptive`），不开则行为与 0.11.x 一致

完整变更列表见 [GitHub Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases) 的 v0.12.0 说明。

---

## 0.10.x → 0.11.0

0.11.0 新增 GB/T 28181 国标平台接入（默认关闭）、录像连续播放/全天时间轴，并变更了项目许可证。数据库层面**无需任何迁移**，升级透明。

### ⚖️ 许可证变更（请阅读）

v0.11.0 起 MiBee NVR 的许可证从 MIT 变更为 **AGPL-3.0-only**（v0.10.1 及更早版本永久保持 MIT）。用大白话说：

- **只是使用 MiBee NVR**（运行、录像、看流——包括商用场景）：零义务，对你没有任何变化。
- **分发修改版**：修改版必须以 AGPL-3.0 开源发布。
- **基于 `pkg/` 扩展接口构建自己的程序**：受[链接例外](../../LICENSE.pkg-linking-exception)保护，你的程序许可证由你决定。
- **独立进程通过 HTTP/WebSocket API 调用运行中的 NVR**：完全不受许可证影响。

详见 [LICENSE](../../LICENSE)、[NOTICE](../../NOTICE) 与 [CONTRIBUTING.md](../../CONTRIBUTING.md)。

### 🔴 破坏性：`GET /api/cameras/{id}/protocols` 字段改名

响应条目的 JSON 字段统一为 snake_case，与其它端点一致（#332）。消费此端点的脚本需更新：

| 0.10.x | 0.11.0 |
|--------|--------|
| `Protocol` | `protocol` |
| `Available` | `available` |
| `Reason` | `reason` |

### 🟡 纯 HTTP 下 AAC 直播音频降级

出于许可证兼容，AAC 直播预览音频的 FAAD2（GPL-2.0）WASM 回退已移除（#319）。AAC 直播音频现在需要 WebCodecs（HTTPS 或 localhost）。纯 HTTP 局域网部署下 AAC 直播预览降级并提示——与 Opus 的既有行为一致。**不受影响**：G.711 直播音频（全场景可用）、录像中的 AAC 音频（浏览器 MP4 原生解码）。

### 🟢 GB/T 28181 国标平台接入（可选，默认关闭）

v0.11.0 新增国标平台角色：支持 SIP 的摄像头（海康、大华、宇视等）REGISTER 到 NVR，以普通相机形式出现——支持 PTZ、语音对讲、设备侧录像检索回放、报警/目录/移动位置订阅，以及作为下级平台级联上联。全部功能**默认关闭**，不开启则无任何变化。配置方法见 [GB/T 28181 指南](gb28181-guide.md)。

新增配置段：`gb28181:`（平台角色）与 `gb28181_cascade:`（下级角色）。新增 DB 表（`gb28181_devices`、`gb28181_channels`、`camera_ptz_presets`、`cascade_channels`、`gb28181_fingerprints`）以 `CREATE TABLE IF NOT EXISTS` 在首次启动时创建——无迁移。

### 🟢 局域网身份与发现

- `server.device_id`：首次启动自动生成稳定 UUID 并持久化到配置；`GET /api/health` 现携带 `device_id`/`device_name`，客户端可锚定身份而非 IP。
- mDNS/DNS-SD 服务通告（`_mibee-nvr._tcp`，默认开启）+ UDP 发现应答器（49090 端口，默认开启；应答 `MIBEE-NVR-DISCv1?` 探测，覆盖组播受限网络）。两者绑定失败仅记日志不阻塞启动——可在 `server.discovery.*` 配置。

### 🟢 iOS/AVPlayer 的 HLS 播放修复

携带会话 token 的播放列表请求现在会下发作用域受限的 `mbs_session` cookie，修复 iOS AVPlayer 等无法设置逐请求头的播放器在媒体分段上的 401。无需操作。

---

## v0.9.1 → v0.10.0

0.10.0 是一次大版本发布（H.265 WASM 直播、Timelapse v3、无状态签名 token、MJPEG 走 WebSocket、AI 模型完整性加固、0.10.0 架构清理）。其中包含几项**破坏性变更**，需要一次性的配置修改或部署后处理。

### 🔴 第 1 步 —— 拆分组合协议字符串（必做，否则无法启动）

0.10.0 在配置校验阶段**拒绝**组合协议字符串（如 `"rtsp_h264"`）。这是**硬错误**——不修复就无法启动。

**检查你的配置：**

```bash
grep -nE 'protocol:\s*".*_(h264|h265|mjpeg|jpeg)"' /path/to/mibee-nvr.yaml
```

**如有命中，拆成两个字段：**

```yaml
# ❌ 升级前（0.9.x 接受，0.10.0 拒绝）
- id: "front-door"
  protocol: "rtsp_h264"
  url: "rtsp://..."

# ✅ 升级后
- id: "front-door"
  protocol: "rtsp"
  encoding: "h264"
  url: "rtsp://..."
```

漏改时启动会报：

```text
camera[0].protocol "rtsp_h264": combined format is no longer supported in
0.10.0+; split into separate protocol ("rtsp") and encoding fields
```

适用于所有 `protocol` 含下划线的相机（`rtsp_h264`、`rtsp_h265`、`rtsp_mjpeg`、`http_jpeg`、`onvif_jpeg` 等）。

### 🔴 第 2 步 —— 备份数据库（强烈建议）

v28 → v29 的 schema 迁移会**自动**通过 `VACUUM INTO` 在删除旧的 `recordings.merged` 列之前备份到 `<db>.pre-v29-backup`。手动备份是多一层保险：

```bash
cp /var/lib/mibee-nvr/nvr.db /var/lib/mibee-nvr/nvr.db.pre-upgrade
```

迁移流程：
1. 备份数据库到 `<db>.pre-v29-backup`（尽力而为；失败只记 warning）。
2. 为旧 `merged=1` 标志的行更新 `merge_status='merged'`（安全网）。
3. 删除 `merged` 列（`merge_status` 现在是唯一真相源）。

对 v0.9.x 用户**透明**。0.10.0 的 DB schema 基线为 **v29**。

### 🟡 第 3 步 —— 回收泄漏的合并 MP4 文件（部署后，可选但推荐）

0.10.0 修复了一个 bug（#117/#119）：在该修复之前，通过 Web UI 删除录像时**不会**删除磁盘上的合并产物 MP4。修复覆盖未来的删除，但**升级前已泄漏的文件仍在磁盘**。用 repair CLI 回收：

```bash
# 1. 先 dry-run 看能回收多少空间
./mibee-nvr repair reclaim-orphan-merges --dry-run

# 2. 执行（删除间隔默认 20ms，对 USB HDD 友好）
./mibee-nvr repair reclaim-orphan-merges --execute

# 可选：限定单个相机、限制数量
./mibee-nvr repair reclaim-orphan-merges --execute --camera front-door --limit 1000
```

只删除**没有任何录像行引用**（既不在 `file_path` 也不在 `merge_path`）的 `.mp4` 文件。原始帧目录和原始分段不会被触碰。`--dry-run` 是默认行为。

### 🟡 第 4 步 —— 注意默认值变化（仅影响留空的配置项）

| 配置项 | v0.9.1 默认 | 0.10.0 默认 | 说明 |
|--------|------------|------------|------|
| `cleanup.disk_threshold_percent` | 95 | **85** | 更早触发清理，避免 HDD 90%+ 满时的性能悬崖。仅当配置留空（`0`）时生效——显式设置的值会被保留。 |
| `cameras[].timelapse.merge_duration` | `"1h"` | **`"natural-day"`** | 单相机 timelapse 默认改为按配置时区对齐到午夜的 24h 窗口。之前的 1h 硬上限已移除；支持 `8h`/`12h`/`24h`/`7d`/`30d`/`natural-day` 以及任意 ≤ 30d 的时长。注意：**rolling-window 合并**（`merge.rolling_window`）仍硬限制 1h。 |

### 🟢 移除的 API 端点（仅影响外部脚本）

两个 timelapse 端点被移除（底层的 gallery 功能被 Timelapse v3 周期合并取代）：

| 移除（0.10.0 返回 404） | 替代 |
|------------------------|------|
| `GET /api/timelapse/{id}/thumbnail` | `GET /api/timelapse/merges` + `GET /api/timelapse/merges/{id}` |
| `GET /api/timelapse/{id}/preview` | `GET /api/timelapse/merges/{id}/download`（响应带 `X-Timelapse-Codec` 头，前端据此选择 `<video>` 播放器或 JPEG 轮播） |

如有外部脚本或书签指向旧端点，请迁移。NVR 自身的前端已使用新端点。

### 🟢 `streaming.default_protocol` 字段移除（静默——旧 YAML key 会被忽略）

全局 `streaming.default_protocol` 配置字段已移除。前端 Player Orchestrator 现在按相机自动选择最佳协议（探测 `/api/cameras/{id}/protocols`、结合 codec + 浏览器能力、运行时根据健康状态降级/升级）。全局默认值只增加用户的配置认知负担。

- **无需任何操作。** 旧 YAML 里的 `default_protocol:` key 会被**静默忽略**（YAML 解码不严格校验未知字段），不会报错。
- 每相机的覆盖仍可通过各相机 LiveView 页的协议切换器设置。
- **行为变更（针对未设覆盖的 H.264 相机）：** 初始协议偏好改为延迟最优顺序（`webrtc > flv > ll-hls > hls > mjpeg`），而非全局默认（通常是 `hls`）。Orchestrator 仍会运行时自适应——这仅改变首选。

### 🟢 新增配置字段（全部向后兼容，默认值安全）

| 字段 | 默认值 | 用途 |
|------|--------|------|
| `merge.rolling_backfill_concurrency` | `0`（自动：≤2GB RAM 用 1，否则 3） | 限制滚动合并回填时的并发相机数。 |
| `streaming.webrtc.ice_servers` | `[]`（仅局域网） | 跨网 WebRTC 的 STUN/TURN 服务器。空 = 仅局域网（旧行为）。 |
| `cameras[].timelapse.retain_intermediate_mp4` | `false` | 周期合并折叠后是否保留滚动合并的中间 MP4 文件（默认清理以节省磁盘——约 1.5GB/天/相机）。 |

### 升级前清单

```bash
# 1. 拆分组合协议字符串（必做——否则无法启动）
grep -nE 'protocol:\s*".*_(h264|h265|mjpeg|jpeg)"' mibee-nvr.yaml
#   → 每个命中项拆为 protocol + encoding

# 2. 备份数据库（在自动 v29 备份基础上多一层保险）
cp /var/lib/mibee-nvr/nvr.db /var/lib/mibee-nvr/nvr.db.pre-upgrade

# 3. 部署新二进制，启动服务，确认健康
curl http://localhost:9090/api/health

# 4. 部署后：回收修复前泄漏的合并 MP4（一次性）
./mibee-nvr repair reclaim-orphan-merges --dry-run     # 检查
./mibee-nvr repair reclaim-orphan-merges --execute     # 执行
```

### 自行构建者须知（非 release 二进制）

如果你自己构建二进制而非使用 release 产物：

- **Go 二进制构建前必须先构建前端。** 嵌入的 SPA（`internal/ui/static/`）必须用 `cd web && npm run build`（或 `make build`，它已包含此步）重新生成。过期的嵌入 UI bundle 可能仍引用已移除的端点，导致浏览器报 404。
- Release 二进制是用全新前端构建的；这只影响本地/自定义构建。

---

## 低于 v0.9.x → v0.10.0：不支持直升级

**不能**从低于 v0.9.x 的版本直接升级到 0.10.0。代码里没有启动时的版本 guard 来中断进程，但你会遇到以下失败之一：

- **DB 时间戳解析失败。** 0.10.0 移除了对旧 Go `time.Time.String()` 格式的支持——monotonic clock 后缀（`m=+...`）和时区缩写（`CST`）。含这些格式的行会变成零时间。v0.9.x 已把这些重写为规范的 SQLite 时间戳；必须先跑 v0.9.x 完成规范化。
- **DB schema 太旧。** v28→v29 迁移假设 v28 schema（由 v0.9.x 产生）。更早的 schema 缺少迁移所需的列。

**正确路径：** 先升级到最新的 v0.9.x，让它跑一次以规范化数据库，再升级到 0.10.0。

### 无法回退版本时的手工 schema 修复

如果你已经直升到 0.10.0、录像数据都还在磁盘上、但页面报错用不了，又不想/无法回退到 v0.9.x 中转，可以用下面的脚本**手工把数据库 schema 补齐到 0.10.0**。录像文件不会丢，只是补上缺失的列。

> ⚠️ **先备份。** 这一步直接改库，出错只能靠备份恢复。

**对号入座 —— 哪些症状说明你中招了：**

- 录像页报 `failed to list recordings`，但硬盘里 `.mp4` 录像片段明明都在；
- 新增/编辑摄像头报错或加不上（`no such column: stable_id`）；
- 从 0.3.0 ~ 0.8.0 任一版本直升 0.10.0 后出现的“数据库相关”报错。

**第 1 步：停服 + 备份数据库。**

裸机（systemd）：

```bash
systemctl stop mibee-nvr
cp /var/lib/mibee-nvr/nvr.db /var/lib/mibee-nvr/nvr.db.pre-fix   # 路径以你的 Storage.root_dir 为准
```

Docker：

```bash
docker compose stop mibee-nvr
cp ./data/nvr.db ./data/nvr.db.pre-fix          # 默认卷映射是 ./data:/data，DB 文件名见 Storage.root_dir
```

**第 2 步：把下面的修复脚本存成 `fix-schema.sql`**（完整复制）：

```sql
-- ===== MiBeeNvr 手工 schema 修复(0.3.0-0.8.0 → 0.10.0) =====
-- 每条 ADD COLUMN 独立执行;某列已存在会报 "duplicate column name" —— 该报错可忽略,
-- 其余语句照常生效。重复执行幂等(不会损坏数据)。

-- ----- recordings -----
ALTER TABLE recordings ADD COLUMN merge_status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE recordings ADD COLUMN merge_path TEXT DEFAULT '';
ALTER TABLE recordings ADD COLUMN merge_error TEXT DEFAULT '';
ALTER TABLE recordings ADD COLUMN merge_tier TEXT DEFAULT '';
ALTER TABLE recordings ADD COLUMN merge_progress INTEGER DEFAULT 0;
ALTER TABLE recordings ADD COLUMN merge_quality TEXT DEFAULT 'complete';
ALTER TABLE recordings ADD COLUMN archived INTEGER DEFAULT 0;
ALTER TABLE recordings ADD COLUMN ai_status TEXT DEFAULT NULL;
ALTER TABLE recordings ADD COLUMN ai_processed_at TEXT DEFAULT NULL;
ALTER TABLE recordings ADD COLUMN ai_error TEXT DEFAULT NULL;
-- 把旧的 merged=1 行迁移到 merge_status(仅当 merged 列还在时生效;已被取代)
UPDATE recordings SET merge_status='merged' WHERE merged=1 AND merge_status='pending';

-- ----- cameras -----
ALTER TABLE cameras ADD COLUMN merge_enabled INTEGER;
ALTER TABLE cameras ADD COLUMN merge_check_interval TEXT;
ALTER TABLE cameras ADD COLUMN merge_window_size TEXT;
ALTER TABLE cameras ADD COLUMN merge_batch_limit INTEGER;
ALTER TABLE cameras ADD COLUMN merge_min_segment_age TEXT;
ALTER TABLE cameras ADD COLUMN merge_min_segments_to_merge INTEGER;
ALTER TABLE cameras ADD COLUMN onvif_endpoint TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN profile_token TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN stream_encoding TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN archived INTEGER DEFAULT 0;
ALTER TABLE cameras ADD COLUMN archived_at DATETIME DEFAULT NULL;
ALTER TABLE cameras ADD COLUMN archive_retention_days INTEGER DEFAULT 0;
ALTER TABLE cameras ADD COLUMN merge_duration TEXT DEFAULT 'natural-day';
ALTER TABLE cameras ADD COLUMN stream_key TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN srt_passphrase TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN srt_stream_id TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN activation_state TEXT DEFAULT 'active';
ALTER TABLE cameras ADD COLUMN stable_id TEXT DEFAULT '';

-- ----- schema_meta / feature_flags 收尾 -----
INSERT OR IGNORE INTO schema_meta(key,value) VALUES('schema_version','29');
UPDATE schema_meta SET value='29' WHERE key='schema_version';
INSERT OR IGNORE INTO feature_flags(key,value) VALUES('protocol.srt',1);
INSERT OR IGNORE INTO feature_flags(key,value) VALUES('protocol.rtmp',1);
```

**第 3 步：执行修复脚本。** NVR 的 Docker 运行镜像（基于 Alpine）**不含** `sqlite3`，所以用哪种方式取决于你的环境：

- **方式 A — 宿主机已装 sqlite3**（裸机用户首选）：

  ```bash
  sqlite3 /var/lib/mibee-nvr/nvr.db < fix-schema.sql
  ```
  出现若干 `duplicate column name: ...` 是正常的（说明该列已存在），其余报错才需要处理。

- **方式 B — 用一次性 Alpine 容器**（Docker 用户推荐，无需在宿主机装任何东西）：

  ```bash
  docker run --rm -v "$PWD/data:/data" -v "$PWD/fix-schema.sql:/fix-schema.sql:ro" alpine \
    sh -c "apk add --no-cache sqlite >/dev/null && sqlite3 /data/nvr.db < /fix-schema.sql"
  ```
  `$PWD/data` 换成你实际的卷映射目录（默认 `./data:/data`）。同样，`duplicate column name` 报错可忽略。

**第 4 步：验证修复成功**（用同一个 sqlite3 会话）：

```bash
# 方式 A
sqlite3 /var/lib/mibee-nvr/nvr.db "SELECT merge_quality FROM recordings LIMIT 1;"
sqlite3 /var/lib/mibee-nvr/nvr.db "SELECT stable_id FROM cameras LIMIT 1;"

# 方式 B(Docker)
docker run --rm -v "$PWD/data:/data" alpine \
  sh -c "apk add --no-cache sqlite >/dev/null && sqlite3 /data/nvr.db 'SELECT merge_quality FROM recordings LIMIT 1;'"
```

两条都不再报 `no such column`（第一条返回 `complete`，第二条返回空串或序列号）即修复成功。

**第 5 步：重启服务，刷新页面确认录像列表正常加载。**

```bash
# 裸机
systemctl start mibee-nvr
# Docker
docker compose start mibee-nvr
```

> 这套脚本是**幂等**的：在已经是 0.10.0 schema 的库上重跑只会报一堆 `duplicate column name`，不会损坏任何数据。所以不确定自己缺哪些列时，直接全量跑一遍最省事。

---

## 通用升级最佳实践

1. **始终备份** `nvr.db` 和 `mibee-nvr.yaml`。
2. **阅读目标版本的 release notes**——本指南覆盖结构性变更；release notes 覆盖功能与修复。
3. **替换二进制前先停服务**（`systemctl stop mibee-nvr`），替换后再启动。
4. **部署后检查健康**：`curl http://localhost:9090/api/health` 应返回 `{"status":"ok",...}`。
5. **头几分钟观察日志**：`journalctl -u mibee-nvr -f`。
6. **必要时回滚**：`make rollback RPi_HOST=user@host` 恢复上一个二进制。DB 备份让你在需要时能恢复数据。
