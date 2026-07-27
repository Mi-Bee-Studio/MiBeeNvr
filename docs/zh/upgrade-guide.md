# 升级指南

本指南覆盖 MiBee NVR 各版本间的升级路径与破坏性变更。**升级前务必备份数据库与配置文件。**

## 升级路径速查表

| 起始 → 目标 | 状态 | 需要的操作 |
|------------|------|----------|
| **v0.9.1 → 0.10.0** | 🟡 **需要操作** | 拆分组合 `protocol` 字符串；可选的磁盘回收。详见 [v0.9.1 → 0.10.0](#v091--v0100)。 |
| v0.9.0 → v0.9.1 | 🟢 透明升级 | 无需操作。 |
| v0.8.x → v0.9.x | 🟡 先备份 | 大型存储层重构。备份数据库后升级。 |
| v0.8.x → 0.10.0 | 🟡 两步走 | 先升级到 v0.9.x，再升级到 0.10.0。 |
| **< v0.9.x → 0.10.0** | 🔴 **不支持（直升级）** | **必须**先升级到 0.9.x —— 原因见 [下文](#低于-v09x--v0100-不支持直升级)。 |

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

```
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

---

## 通用升级最佳实践

1. **始终备份** `nvr.db` 和 `mibee-nvr.yaml`。
2. **阅读目标版本的 release notes**——本指南覆盖结构性变更；release notes 覆盖功能与修复。
3. **替换二进制前先停服务**（`systemctl stop mibee-nvr`），替换后再启动。
4. **部署后检查健康**：`curl http://localhost:9090/api/health` 应返回 `{"status":"ok",...}`。
5. **头几分钟观察日志**：`journalctl -u mibee-nvr -f`。
6. **必要时回滚**：`make rollback RPi_HOST=user@host` 恢复上一个二进制。DB 备份让你在需要时能恢复数据。
