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

进程内上限是主机制；unit 限额是安全网（`deploy/mibee-nvr.service` 默认启用）：

```ini
MemoryHigh=450M   # 软限：内核回收而非杀死
MemoryMax=550M    # 硬限兜底
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

观测：`nvr_iobudget_wait_seconds_total{consumer}`（后台等待时长）、
`nvr_iobudget_bytes_charged_total{consumer}` /
`nvr_iobudget_unlinks_charged_total{consumer}`（计费量），
`consumer` ∈ {merge, cleanup, repair, timelapse}。

`mibee-nvr repair delete-by-format` CLI 读取同一 `io:` 配置节——对在线
服务器手工批量删除时，让路行为与服务端自身清理完全一致。
