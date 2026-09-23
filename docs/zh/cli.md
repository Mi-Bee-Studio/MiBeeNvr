# CLI 用户手册

> 适用于 MiBeeNvr v0.13.0 · 命令名 `mibee-nvr`（预编译包可能带架构后缀，如 `mibee-nvr-amd64`）

MiBee NVR 是「单二进制 + 子命令」形态：**不带子命令直接运行即启动服务器**，带子命令则执行对应的管理工具后退出。

```bash
mibee-nvr              # 启动 NVR 服务器（长驻进程）
mibee-nvr <子命令>      # 执行管理工具，完成后退出
mibee-nvr -version     # 打印版本
```

## 启动服务器

```bash
mibee-nvr -config mibee-nvr.yaml
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-config` | `mibee-nvr.yaml` | 配置文件路径 |
| `-version` | — | 打印版本后退出 |

> 没有配置文件时启动会**自动初始化**，浏览器打开后进入[初始化向导](wizard.md)。

## 子命令总览

| 子命令 | 用途 |
|--------|------|
| [`init`](#init-生成配置) | 交互式生成配置文件和管理员账户 |
| [`hash-password`](#hash-password-生成密码哈希) | 生成密码哈希 |
| [`health`](#health-健康检查) | HTTP 健康探测（Docker HEALTHCHECK 用） |
| [`encrypt-config`](#encrypt-config-加密敏感字段) | 加密配置中的明文密码 |
| [`validate-config`](#validate-config-校验配置文件) | 部署前校验配置文件（退出码 0 = 可启动） |
| [`download-model`](#download-model-下载-ai-模型) | 下载浏览器端 AI 检测模型 |
| [`merge-cameras`](#merge-cameras-合并摄像头) | 合并两个重复的摄像头条目 |
| [`timelapse-merge`](#timelapse-merge-录像转延时合并) | 把任意时段的录像批量转成延时合并产物 |
| [`eval-replay`](#eval-replay-离线回放评估) | 离线回放活动评分 / 自适应门控，调参前后对照 |
| [`repair`](#repair-数据修复) | 数据修复工具集（9 个子命令） |
| [`cleanup`](#cleanup-录像清理) | 按日期 / 孤儿文件清理录像 |
| [`offload`](#offload-对象存储冷备队列) | S3 冷备队列计数与本地副本逐出 |
| [`update`](#update-版本检查与裸机升级) | 版本检查与裸机升级执行 |
| [`gen-gb35114-certs`](#gen-gb35114-certs-签发-gb35114-试点证书) | 签发 GB35114 A 级试点证书（仅 `-tags gb35114` 构建） |

---

## init — 生成配置

生成配置文件并设置管理员账户：

```bash
mibee-nvr init --password 你的密码
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--password` | （交互输入） | 管理员密码，**至少 8 位**；未提供时在终端提示输入 |
| `--username` | `admin` | 管理员用户名 |
| `--data-dir` | `/var/lib/mibee-nvr` | 数据目录（录像 + 数据库） |
| `--listen` | `:9090` | HTTP 监听地址 |
| `--config` | `mibee-nvr.yaml` | 配置文件输出路径 |
| `--force` | — | 覆盖已存在的配置文件（否则报错退出） |

生成的配置默认值：片段时长 `30s`、保留 30 天、FTP 2121、WebDAV `/dav`。

> 也可以完全跳过 `init`：无配置启动 → Web 向导完成（参阅[初始化向导](wizard.md)）。

## hash-password — 生成密码哈希

```bash
mibee-nvr hash-password '你的密码'
# 输出: $2a$10$...
```

把输出粘贴到配置文件的 `auth.password_hash` 字段，适合在脚本中批量初始化。

## health — 健康检查

对本地服务做 HTTP 探测（`GET /api/health`），成功退出码 0，失败退出码 1 —— 供 Docker `HEALTHCHECK` / systemd watchdog 使用：

```bash
mibee-nvr health                 # 探测 :9090
mibee-nvr health --addr :9191    # 显式指定地址
mibee-nvr health --config /data/mibee-nvr.yaml   # 从配置读取 server.listen
```

地址解析优先级：`--addr` > `--config` 中的 `server.listen` > Docker 自动探测（读 `NVR_DATA_DIR` 数据目录下的配置）> 默认 `:9090`。**host 网络模式下改过监听端口时无需加参数**，命令会自动找到真实端口。

## encrypt-config — 加密敏感字段

把配置文件中的**明文敏感字段**（摄像头密码等）就地加密：

```bash
mibee-nvr encrypt-config --config mibee-nvr.yaml
```

输出加密了哪些字段；已是密文或为空的字段会跳过。加密后服务照常读取，人工无法直接看到密码明文。

## validate-config — 校验配置文件

手工编辑 YAML 后的**部署前冒烟检查**：走与服务器启动完全相同的 Load → Validate 流水线，但不启动任何东西。**退出码 0 = NVR 用这份配置能正常启动**，1 = 会启动失败（在 systemd 里就是崩溃重启循环）：

```bash
mibee-nvr validate-config
mibee-nvr validate-config --config /data/mibee-nvr.yaml
# OK /data/mibee-nvr.yaml — the NVR would boot on this config
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--config <path>` | `mibee-nvr.yaml` | 要校验的配置文件路径（与裸 `mibee-nvr` 启动时的默认路径一致） |

校验覆盖取值区间、格式与缺失必填项等**非法值**，并暴露加载阶段的**悬空引用**问题 —— 2026-09-11 M5 事故正是手改 YAML 删掉 vision 实例后摄像头仍引用它，服务重启循环了约两分钟。改完配置先跑一遍再重启服务。

## download-model — 下载 AI 模型

下载浏览器端 AI 检测所需的 ONNX 模型（YOLOv11-nano，约 5.4MB，来自 Ultralytics 官方发布）到 Web 静态目录：

```bash
mibee-nvr download-model --config mibee-nvr.yaml
```

内置 5 次指数退避重试和大小 / 完整性校验，适合离线环境预下载后随包分发。模型用于[浏览器端 AI 检测](ai-detection-tuning.md)。

## merge-cameras — 合并摄像头

把两个重复的摄像头条目**端到端合并**（例如同一台设备被 ONVIF 发现和小米接入各加了一次）：

```bash
# 先预览（默认 dry-run）
mibee-nvr merge-cameras --source cam-old --target cam-new

# 确认无误后执行
mibee-nvr merge-cameras --source cam-old --target cam-new --execute
```

执行步骤：备份数据库 → 改写录像 / 事件的摄像头归属与文件路径 → 移动录像文件 → 从配置中移除源摄像头 → 删除源摄像头数据库行。**任一步失败自动回滚**。

| 参数 | 说明 |
|------|------|
| `--source <id>` | 源摄像头 ID（数据搬离方，合并后删除） |
| `--target <id>` | 目标摄像头 ID（数据并入方，保留） |
| `--execute` | 真正执行（默认 dry-run 仅预览） |
| `--force` | 存在孤儿记录时仍然继续 |
| `--config <path>` | 配置文件路径（默认 `mibee-nvr.yaml`） |

## timelapse-merge — 录像转延时合并

把**任意时段、任意摄像头**的既有录像（H264 / H265 / AVI / MJPEG）批量转成周期延时合并产物 —— `POST /api/timelapse/{id}/merge` 的 CLI 版，进程内直连数据库执行。采样间隔、输出帧率、源录像删除都是**本次运行覆盖值，不改摄像头配置**：

```bash
# 预览：所有 JPEG 摄像头、2026-08-26 起的全部录像
mibee-nvr timelapse-merge --camera all --encoding jpeg --start 2026-08-26

# 执行：1 秒采样 + 合并成功后删除源录像
mibee-nvr timelapse-merge --camera all --encoding jpeg --start 2026-08-26 \
  --interval 1s --delete-sources --execute
```

行为要点：

- 按窗口（默认 `natural-day`，即自然日）枚举日期范围，逐窗口执行；已完成的窗口自动跳过（可安全重跑续传），尚未闭合的窗口跳过。
- `--delete-sources` 仅在对应窗口合并**成功后**删除源录像（DB 行 + 文件），MiBeeVision 处理中的录像始终跳过。
- 命令可在 NVR 运行中执行（WAL 并发模型，与 cleanup/repair 一致）；但 timelapse 已启用的摄像头会被拒绝 —— 其窗口归服务器合并调度器所有，需 `--force` 或停服后运行。
- **执行时自动自降级**（nice 19 + IO best-effort 最低档，`--no-throttle` 关闭）：大窗口合并与在线录像平权竞争会拖垮录像（2026-09-12 事故：load 8-13 持续 4.5h，录像 17→10 台），事后 renice 无法挽回。
- **目录形源删除自动限速**（每 200 个文件暂停 `--delete-throttle`）：MJPEG/延时帧目录的百万级小文件 unlink 会让 ext4 日志（jbd2）饱和数十分钟，拖慢所有磁盘 IO。
- 合并中间产物（帧提取/复制目录）写在**存储根** `<root>/periodic-merge/tmp/` 下，不再用系统 `/tmp`（1s 采样全天窗口约需 3.5-7GB，小根分区会 ENOSPC）；合并输出采用流式生成（#747），内存占用与窗口大小无关。中断（Ctrl-C/崩溃）遗留的中间目录由 NVR 服务端启动清扫回收（宽限期 `storage.periodic_temp_grace_s`，默认 24h，见 [配置](configuration.md#storageperiodic_temp_grace_s)）。

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--camera <ids\|all>` | （必填） | 逗号分隔摄像头 ID 或 `all` |
| `--encoding <enc>` | — | 配合 `--camera all` 按编码过滤（如 `jpeg`） |
| `--start <YYYY-MM-DD>` | （必填） | 起始窗口日期（配置时区） |
| `--end <YYYY-MM-DD>` | 昨天 | 结束窗口日期（含） |
| `--duration <label>` | `natural-day` | 窗口大小：`8h` / `12h` / `24h` / `7d` / `30d` 等 |
| `--interval <dur>` | 摄像头 `timelapse.interval`，缺省 30s | 帧采样间隔（如 `1s`） |
| `--fps <n>` | 摄像头 `merge_output_fps`，缺省 10 | 输出播放帧率 |
| `--delete-sources` / `--no-delete-sources` | 摄像头 `delete_recordings_after_merge` | 覆盖本次运行的源录像删除开关 |
| `--delete-throttle <dur>` | `200ms` | 删除目录形源（MJPEG/延时帧目录）时的分块暂停，`0` 关闭 |
| `--execute` | dry-run | 真正执行 |
| `--force` | — | NVR 运行中仍处理 timelapse 已启用的摄像头 |
| `--no-throttle` | — | 跳过启动时自动自降级（nice 19 + IO best-effort） |
| `--config <path>` | `mibee-nvr.yaml` | 配置文件路径 |

## eval-replay — 离线回放评估

把**离线活动评分器**或**自适应录像门控**在一组已完成录像的黄金语料（corpus）上重放，输出逐文件明细与按标签聚合的均值 —— 自适应录像（`recording_mode: adaptive`）调参先离线拿到调参前/后对照表，再上真实机器。详见[自适应录像](adaptive-recording.md)。

```bash
# 评分器回放（默认）
mibee-nvr eval-replay --corpus corpus.json

# 门控回放（默认参数）
mibee-nvr eval-replay --corpus corpus.json --gate

# 门控回放 + 候选参数对照（候选与默认并排两列）
mibee-nvr eval-replay --corpus corpus.json --gate --videoexit=false
```

corpus 是 JSON 数组，路径可写绝对路径或相对清单文件；标签自由（框架约定 `rain` / `lowbitrate` / `static` / `active`）：

```json
[{"path": "/mnt/data/nvr/cam-yard/seg.mp4", "camera": "cam-yard", "label": "rain"}]
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--corpus <path>` | （必填） | 语料清单 JSON 路径 |
| `--gate` | 评分器 | 回放自适应门控而非评分器 |
| `--fps <n>` | 按文件推算 | 门控回放帧率（0 = frames/duration 推算） |
| `--spike <f>` | — | 候选门控：`spike_factor` |
| `--noisefloor-bytes <n>` | — | 候选门控：显式 `noise_floor_bytes` |
| `--autonoise true\|false` | — | 候选门控：`auto_noise_floor` |
| `--videoexit true\|false` | — | 候选门控：`video_exit` |

## repair — 数据修复

针对运行期数据问题的一组修复工具，**直接操作数据库**。建议优先在服务停止时运行（运行中也安全 —— WAL 模式支持并发读，但大修停服更稳）。

```bash
mibee-nvr repair <子命令> [--dry-run | --execute] [--config mibee-nvr.yaml]
```

所有子命令**默认 dry-run**（只报告将改动什么），加 `--execute` 才真正落库。

| 子命令 | 用途 |
|--------|------|
| `duration` | 修复 duration=0 的录像：重新探测视频文件恢复真实时长（`--prune` 顺带删除无法修复的记录） |
| `merge-status` | 重置「已合并」标记 —— 合并产物文件丢失时回退为未合并 |
| `fragments` | 清理合并引擎放弃的碎片段（不兼容 / 失败） |
| `delete-by-format` | 按格式批量删除某摄像头的录像，保留指定格式（如只留延时摄影段） |
| `prune-intermediate-mp4` | 清理已并入周期合并产物（8h/24h/7d/30d）的滚动合并中间 .mp4 |
| `reclaim-orphan-merges` | 回收 Web UI 删除录像后遗留的孤儿合并 .mp4（只动无引用产物，不碰源段） |
| `normalize-endpoints` | 规范化 ONVIF endpoint（省略默认端口 / 小写 / 去尾斜杠），修复去重查询不匹配 |
| `mjpeg-containerize` | 把旧版目录形态 MJPEG 段（每帧一个 JPEG 文件）转成单文件 AVI 容器（#761）；逐段「转换 → 校验 → 落库 → 删源」，校验失败则行不动 |
| `timelapse-mjpeg` | 重写样本带「双重 JPEG 头」的 MJPEG 周期合并产物（不合规范的 RTSP 发送方把完整 JPEG 塞进 RFC 2435 载荷、解包器又前置了合成头，浏览器拒收）：无损抽取每个样本内层完整 JPEG 重封装，并刷新 DB 行的帧数 / 大小；可抢救帧数不足 25%（源数据在流尾截断）时跳过留给运维决断 |

示例：

```bash
# 预览将修复多少条 duration=0 的录像
mibee-nvr repair duration

# 执行修复，并删除探测失败的坏文件
mibee-nvr repair duration --execute --prune
```

`mjpeg-containerize` 专用参数：

| 参数 | 默认 | 说明 |
|------|------|------|
| `--camera <id>` | 全部 | 只转换该摄像头的段 |
| `--limit N` | 全部 | 最多转换 N 段 |
| `--keep-old` | 关 | 转换后保留源帧目录 |
| `--busy-retries N` | `3` | DB 行翻转遇 `SQLITE_BUSY` 的重试次数（`0` = 单次尝试，不重试） |
| `--busy-wait <dur>` | `2s` | BUSY 重试的线性退避基数（Go 时长格式，如 `500ms`、`5s`） |

CLI 与运行中的 NVR 共享 WAL 库——磁盘饱和时服务端合并事务可能拖过 busy_timeout，此时调大这两个值；快盘上默认 3 次的最坏等待是纯浪费，可调小（照 `timelapse-merge --delete-throttle` 先例）。

```bash
# 预览（默认 dry-run）
mibee-nvr repair mjpeg-containerize --camera yard-esp32

# 执行；磁盘饱和时放宽 BUSY 重试
mibee-nvr repair mjpeg-containerize --execute --busy-retries 5 --busy-wait 5s
```

## cleanup — 录像清理

绕过保留策略的手动清理工具，**同时删除文件、数据库行和孤儿 AI 事件**：

```bash
# 预览删除某日期之前的录像
mibee-nvr cleanup --before 2026-08-01 --dry-run

# 执行
mibee-nvr cleanup --before 2026-08-01

# 清理孤儿文件（磁盘上有视频文件但数据库无记录）
mibee-nvr cleanup --orphans --dry-run
mibee-nvr cleanup --orphans
```

| 参数 | 说明 |
|------|------|
| `--before YYYY-MM-DD` | 删除此日期之前的录像（文件 + DB 行 + AI 事件） |
| `--orphans` | 扫描磁盘删除数据库无记录的视频文件（.mp4/.mkv/.avi/.dav/.flv） |
| `--dry-run` | 只统计不删除（强烈建议先跑一遍） |
| `--config <path>` | 配置文件路径（默认 `mibee-nvr.yaml`，用于定位存储根目录和数据库） |

> 日常清理请优先使用[保留策略](recording-playback.md)（`cleanup.retention_days`）；本命令适合迁移后瘦身、异常善后等场景。

## offload — 对象存储冷备队列

[`storage.remote`](storage-offload.md)（S3 兼容对象存储冷备）的运维面。两个子命令都要求配置里 `storage.remote.enabled: true`，否则直接报错退出：

```bash
# 上传队列各状态计数 + 积压（pending+uploading）
mibee-nvr offload status

# 逐出已确认上传录像的本地副本（默认 dry-run 仅报告）
mibee-nvr offload evict --all-uploaded
mibee-nvr offload evict --camera front-door

# 真正执行
mibee-nvr offload evict --all-uploaded --execute
```

行为要点：

- `evict` 只删**已确认上传**（`uploaded`）录像的本地文件；`--execute` 删除前对每个对象重新做一次远端 `HeadObject` 复核，复核失败该项 REFUSED、什么也不删。**远端对象永不删除**（远端清理交给 bucket lifecycle 策略）。
- 默认按 `storage.remote.evict.after_days` 计算可逐出窗口；`after_days: 0`（仅上传模式）必须显式加 `--all-uploaded` 才会逐出。
- 可对**运行中的服务**执行（WAL 并发读）；大批量建议选空闲时段。

| 参数 | 说明 |
|------|------|
| `--all-uploaded` | 无视 `evict.after_days`，纳入所有已确认上传 |
| `--camera <id>` | 只处理该摄像头 |
| `--execute` | 真正删除本地文件（默认 dry-run 报告） |
| `--config <path>` | 配置文件路径（默认 `mibee-nvr.yaml`） |

整体管线（outbox 状态机、上传宽限、backlog 上限）与配置见[对象存储冷备](storage-offload.md)。

## update — 版本检查与裸机升级

裸机（bare-metal systemd）部署的**升级执行层**（#647），配合 `mibee-nvr-update.service` root helper（polkit 授权）使用。`--check` 只读感知层，升级必须 root：

```bash
# 只看当前/最新版本与部署形态，不改任何东西
mibee-nvr update --check

# 手动升级到最新稳定版（需要 sudo）
sudo mibee-nvr update
sudo mibee-nvr update --version v0.13.0

# root helper 入口（update 服务的 ExecStart；请求文件由应用写入、只消费一次，任意结果都会被删除）
mibee-nvr update --apply-request /var/lib/mibee-nvr/update-request.json
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--check` | — | 只打印 current / latest / available / deployment，不执行升级 |
| `--version <tag>` | 最新稳定版 | 目标 release tag |
| `--apply-request <file>` | — | 消费应用写好的升级请求文件（helper 入口） |
| `--config <path>` | `mibee-nvr.yaml` | 配置文件路径 |

- 升级制品经 sha256 + ed25519 校验，升级后过**健康门**（对配置的 `server.listen` 做本地健康探测），失败自动回滚到旧二进制。
- **仅限 Linux 裸机**：Docker 部署永久禁用（容器不可变，用 Watchtower / `docker compose pull`），Windows / macOS 桌面构建同样拒绝（自更新会错拿到 linux 二进制）。
- `update.download_mirror` 可把制品下载切到镜像源（版本检查仍走 GitHub API）。

自动升级开关（`update.auto_apply`）、服务与 polkit 安装见[自动升级](deployment-autoupdate.md)。

## gen-gb35114-certs — 签发 GB35114 试点证书

仅存在于 `-tags gb35114` 构建（默认构建运行会提示重建方式）。为 GB35114 A 级安全注册签发自签试点材料：SM2 平台身份 + 由平台签发的设备身份，输出布局直接对应 `gb28181.security35114` 的三个路径键。详见 [GB28181 指南 — GB35114 安全增强](gb28181-guide.md)。

```bash
mibee-nvr gen-gb35114-certs --platform-id 34020000002000000001 \
  --device-id 34020000001320000001,34020000001320000002 \
  --out-dir gb35114-certs [--days 3650]
```

## 环境变量速查

| 变量 | 说明 |
|------|------|
| `NVR_PASSWORD` | 首次启动设置管理员密码（无密码时 API 返回 503） |
| `NVR_LISTEN_PORT` | 覆盖监听端口 |
| `NVR_DATA_DIR` | Docker 数据目录（`health` 子命令自动探测用） |
| `NVR_UID` / `NVR_GID` | 容器内运行用户（对齐宿主目录权限） |

## 下一步

- [配置参考](config.md) — YAML 顶层键速查
- [初始化向导](wizard.md) — Web 端首次配置
- [升级指南](upgrade-guide.md) — 版本升级与数据迁移
