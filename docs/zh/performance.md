# 性能调优

> 范围：资源受限主机上的进程内存自律与后台 I/O 让路。  
> 模块：`internal/memlimit`、`internal/iobudget`。

---

## 内存分层

NVR 主机上有三类内存消费者，它们的关系理解错了，就是大多数"莫名其妙变慢"的根源：

| 层 | 是什么 | 谁管理 | 被挤压时会发生什么 |
|----|--------|--------|--------------------|
| Go 堆 | 录像环形缓冲、合并样本元数据、API JSON | Go 运行时（GC） | GC 变热，录像延迟上升 |
| 页缓存 | 内核对录像文件的缓存 | 内核 | 每次读写都打到磁盘 → I/O 放大 |
| tmpfs | `/tmp` 提取临时目录（#746 教训） | 内核（RAM 支撑） | 占用堆以为属于自己的内存 |

Go 默认行为（GOGC=100、无内存上限）会激进增长堆：生产 4GB 主机上
NVR 进程峰值 1.6GB，把录像 I/O 依赖的页缓存挤掉——多出来的磁盘读表现为
I/O 停顿，看着像存储问题，实际是内存问题。

### 自动 GOMEMLIMIT

自 #756 起，服务端启动路径在任何 manager 开始分配之前安装一个保守的
**软**内存上限（`debug.SetMemoryLimit`）：

```text
limit = min( 物理内存 × 45%, 1 GiB )              # 物理档
limit = min( limit, cgroup 内存上限 × 80% )        # 存在上限时
下限   = 64 MiB                                     # 不再更低
```

优先级——先命中先生效：

1. `GOMEMLIMIT` 环境变量（运行时在 `main` 之前原生生效，NVR 检测到后不再干预）；
2. `mibee-nvr.yaml` 的 `memory.soft_limit_bytes`（显式指定值）；
3. 上面的自动启发式。

关闭自动启发式：

```yaml
memory:
  disable_auto_limit: true   # 保持 Go 运行时默认
```

生效值在启动日志中输出（`process memory soft limit set`），并以
`nvr_memlimit_bytes` 指标发布（0 = 未设置）。

软上限本身不会导致 OOM——接近上限时 GC 只是更努力工作。GOGC 保持默认；
若观察到 GC 抖动（`go_memstats_gc_cpu_fraction` 上升），调高
`memory.soft_limit_bytes`。

### systemd / Docker 外层限额

进程内上限是主机制；unit 限额是可选安全网（`deploy/mibee-nvr.service` 中默认**注释**——512MiB 基准取值直接开在更大内存的板上会 OOM，按板取消注释并调值）：

```ini
# MemoryHigh=450M   # 软限：内核回收而非杀死（1GB 板示例）
# MemoryMax=550M    # 硬限兜底（4GB 板建议 1500M / 2G）
```

数值按 512MiB 设计基准（RPi-3B 级）取值；4GB 板建议 `High=1500M` /
`Max=2G`。Docker 见 `deploy/docker/docker-compose.yml` 中注释掉的
`mem_limit` 示例——容器内的 cgroup 上限也会被自动启发式识别（80% 档）。

---

## 后台 I/O 预算

合并、清理删除、repair、延时摄影提取是**后台**工作；录像写入、API 文件
服务、SQLite 是**前台**工作。它们共享一条内核 I/O 队列，否则平权竞争——
生产实测在 timelapse 合并运行期间 PSI io-some 约 60%、API 出现秒级停顿
（#748/#751）。

启用共享字节率预算，让后台工作让路：

```yaml
io:
  budget_bytes_per_sec: 16777216   # 16 MiB/s ≈ 慢速 SD 卡顺序写吞吐的 25%
  # budget_burst_bytes: 33554432   # 可选；默认 = 1 秒速率
  # delete_unlinks_per_sec: 200    # 帧目录 unlink 护栏（默认 200）
```

- **默认关闭**——`budget_bytes_per_sec: 0`（或不写该节）行为与历史版本完全一致；
- 建议取存储介质顺序写吞吐的 ~25%；介质忙时后台任务自动拉长，前台保持延迟；
- unlink 护栏额外把递归帧目录删除限制在 200 文件/秒（可配），防止
  ext4 日志（jbd2）饱和在删除进程退出后自持（#748 教训）。

### 租户全景

所有共享同一令牌桶的 I/O 租户（即指标里的 `consumer` 标签）：

| 租户 | 计费内容 | 状态 |
|------|----------|------|
| `merge` | 滚动 / 批量合并的读写 | 既有 |
| `cleanup` | 保留期 / 磁盘水位清理删除 | 既有 |
| `repair` | 修复性批量删除 | 既有 |
| `timelapse` | 延时摄影帧提取 | 既有 |
| `transcode` | 转码输入读 + 输出写，按输入大小 ×2 估价计费（#848） | 既有 |
| `offload` | S3 对象存储冷备上传（#874） | v0.13 |
| `recording` | 录像段样本写，按 NALU 字节计费 | v0.13 灰度，默认关（#886） |
| `playback` | API 媒体服务读，按读块计费 | v0.13 灰度，默认关（#886） |

观测：`nvr_iobudget_wait_seconds_total{consumer}`（等待时长）、
`nvr_iobudget_bytes_charged_total{consumer}` /
`nvr_iobudget_unlinks_charged_total{consumer}`（计费量）。

### 前台灰度开关（v0.13，#886）

预算默认只约束后台任务；v0.13 新增两个灰度开关，把**前台** I/O 也纳入同一预算：

```yaml
io:
  budget_bytes_per_sec: 16777216    # 前提：预算本身已启用
  recording_writes_budgeted: false  # 录像段写按 NALU 字节计入 "recording" 租户
  playback_reads_budgeted: false    # API 媒体服务读按读块计入 "playback" 租户
```

- **默认 false——关闭态零行为变化**，与历史版本完全一致；两者都要求 `budget_bytes_per_sec > 0`；
- `recording_writes_budgeted`：段样本写在 muxer 写入前计费，桶枯竭时阻塞让路（录像器环形缓冲吸收延迟，仅当停顿超出缓冲容量才丢帧）。只在"无界录像写本身就是延迟问题"的介质上开启——例如 SD 卡同时承压合并与回放下载。代码：`internal/recorder/iobudget.go`；
- `playback_reads_budgeted`：回放 / 下载的文件读按 `http.ServeContent` 读块计费，桶枯竭时给传输限速（Range / 协商语义不变）。代码：`internal/api/playback_budget.go`；
- **开启前先观察 iobudget 指标**：确认 `nvr_iobudget_wait_seconds_total` 余量充足，开启后再复查前台等待没有失控。

`mibee-nvr repair delete-by-format` CLI 读取同一 `io:` 配置节——对在线
服务器手工批量删除时，让路行为与服务端自身清理完全一致。

### 碎段攒批折叠（#852）

闪断相机（Wi-Fi/电源不稳、CS2 EOF、级联抖动）每次重连产出一个 ~7s 碎段；滚动合并的每次
折卷都是全桶整读整写（`MergeMP4Segments([bucket, 段])` 流式重写），发病期单台相机的折卷率
可达 36 次/分钟，PSI io some 冲 86%（#851 实测）。碎段攒批把 <30s 的 MP4 段先攒进持有队列，
到期后**一次**折卷整批——全桶重写次数降为约 1/N。

```yaml
merge:
  rolling_fragment_hold_s: 300   # 默认；0 = 关闭（逐段折卷）；范围 0-3600
```

- 默认开启（纯元数据持有，最坏内存 <1MB，RPi 3B 无影响）；可在 Web
  **设置 → 录像合并**卡片直接调整或关闭；
- 冲刷条件任一满足即折：最老碎段年龄到期 / 深度 8 段 / 累计 64MB / 小时窗翻转 /
  同相机健康段（≥30s）到达搭车；10 分钟回填扫描对窗口内年轻碎段让路（防中途收走）；
- 持有期间碎段仍是可独立播放的录像行——只是合并产物晚至多一个窗口出现。

### 顺序追加桶（#853，实验性，默认关闭）

#852 把折卷**次数**降为 1/N；顺序追加桶把单次**代价**从 O(桶) 降为 O(段)：桶创建时在
样本表预留容量槽位，折卷只在 mdat 尾部追加新段字节并原地补丁表条目，绝不重写已有字节。
启动自检发现未提交尾部（崩溃残留）按 mdat 声明截断；容量耗尽自动回落经典全量重写。

```yaml
merge:
  rolling_append_bucket: false  # 默认关闭；实验性，Web 设置 → 录像合并可开
```

**⚠️ RPi 3B 基线**：每活跃相机常驻约 1–2MB 内存镜像（72k 样本/小时 × 12–16B/条目）；
12 路全开 ≈ 12–24MB。1GB 设备谨慎；默认关闭由用户按实际开启。

---

## 0.13 录像写路径优化

v0.13 对录像写盘路径做了三项改进，消除段写入制造的 I/O 尖峰（效果可用
`nvr_segment_write_duration_seconds` 指标观测）：

- **MP4 增量落盘**：媒体字节在录制期间增量写出，段关闭只剩一个小 moov 头
  待补——消除 Close 时刻的全文件突发写；
- **每段独立写锁**：相机之间的段写盘不再互相串行化，单相机慢盘不拖累全局；
- **NALU 缓冲池化**：Annex-B 组帧缓冲按录像器池化复用（#875），
  消除每 NALU 一次的堆分配与 GC 压力。
