# 存储优化指南

> 目标平台: Raspberry Pi 3B (1GB RAM, SD 卡根分区, USB HDD 数据分区)  
> 数据库引擎: 基于 modernc.org/sqlite 的纯 Go SQLite 驱动 (CGO_ENABLED=0)  
> 模块路径: `github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage`

---

## 1. 架构概览

MiBee NVR 的所有元数据存储在一个启用了 **WAL (Write-Ahead Log，预写式日志)** 模式的 SQLite 数据库中，使用纯 Go 驱动 (`modernc.org/sqlite`) — 无需 CGO，无交叉编译问题。MP4 段文件操作由 `Manager` 结构体管理，采用原子性的 `临时文件 → 重命名` 语义。

### 组件分层

```text
┌─────────────────────────────────────────┐
│  API 请求处理层                           │
├─────────────────────────────────────────┤
│  存储层 (internal/storage/)              │
│  ┌─────────────┐  ┌──────────────────┐   │
│  │ DB 结构体    │  │ Manager 结构体    │   │
│  │ (SQLite CRUD)│  │ (文件操作)        │   │
│  └─────────────┘  └──────────────────┘   │
├─────────────────────────────────────────┤
│  modernc.org/sqlite (纯 Go)              │
│  WAL 模式, mmap_size=256MB              │
└─────────────────────────────────────────┘
```

### DSN 配置

DSN 在 `internal/storage/db.go:New()` 中构造。使用 DSN 级别的 `_pragma` 而非 `PRAGMA` 语句，确保连接池中的每个连接都继承这些设置 — 这对 `busy_timeout` 在多个 goroutine 间正常工作至关重要。

```go
func New(dbPath string) (*DB, error) {
    dsn := dbPath + "?_pragma=journal_mode(WAL)" +
        "&_pragma=synchronous(NORMAL)" +
        "&_pragma=busy_timeout(15000)" +
        "&_pragma=cache_size(-20000)" +
        "&_pragma=mmap_size(268435456)"
    db, err := sql.Open("sqlite", dsn)
    if err != nil { return nil, err }
    return &DB{path: dbPath, db: db}, nil
}
```

### 连接生命周期

1. `New(dbPath)` — 以 DSN pragma 打开单个 `*sql.DB`
2. `Init(ctx)` — 创建表、运行迁移、创建索引
3. `Close()` — 关闭底层数据库
4. `Backup(ctx, destPath)` — 使用 `VACUUM INTO` 实现在线备份

### 关键约束 (RPi 3B)

- 同时最多 4 个 HLS 流，段文件时长 ≤ 30s，进程内存 ≤ 512MB
- SD 卡根文件系统 → NORMAL 同步模式 (WAL 减轻 fsync 开销)
- Busy 超时 15s → 容纳 SD 卡延迟峰值

---

## 2. DSN/PRAGMA 调优参考

| PRAGMA | 值 | 理由 | RPi 约束 |
|--------|------|----------|----------------|
| `journal_mode` | `WAL` | 写入期间允许并发读取；无读取者阻塞写入者 | WAL 检查点比 DELETE 模式减少 3 倍以上 fsync |
| `synchronous` | `NORMAL` | WAL 在 NORMAL 模式下已是崩溃安全的 | NORMAL 比 FULL 减少 40-60% SD 卡磨损 |
| `busy_timeout` | `15000` (15s) | 在 SQLITE_BUSY 时自动重试 | SD 卡延迟尖峰可达 5-10s |
| `cache_size` | `-20000` (20MB) | 负值表示 KiB。查询计划和索引查找缓存 | 总内存 1GB → 20MB 很保守 |
| `mmap_size` | `268435456` (256MB) | 读取查询使用内存映射 | 内存预算的 50%；最大支持约 10GB DB |
| `temp_store` | `MEMORY` | 临时表和索引保留在 RAM 中 | 避免临时数据写入 SD 卡 |
| `analysis_limit` | `1000` | ANALYZE 使用采样行而非全表扫描 | 对约 10 万行表足够 |

### 完整 DSN 字符串

```text
file:/mnt/data/nvr/mibee-nvr.db?_pragma=journal_mode(WAL)&
  _pragma=synchronous(NORMAL)&_pragma=busy_timeout(15000)&
  _pragma=cache_size(-20000)&_pragma=mmap_size(268435456)
```

### 初始化后优化

迁移完成后，`Init()` 执行 `PRAGMA optimize` 以触发 SQLite 的自动调优（统计信息刷新、索引分析）。

---

## 3. 索引策略

### 当前索引（schema v22）

| 索引 | 列 | 用途 | 创建时机 |
|------|--------|---------|-------------|
| `idx_recordings_camera` | `camera_id` | **遗留** — 已被复合索引替代 | Init |
| `idx_recordings_time` | `started_at` | 无 camera_id 过滤的时间范围扫描 | Init |
| `idx_recordings_merged` | `merged` | **遗留** — 已被替代 | 迁移 v4 |
| `idx_recordings_archived` | `archived` | **遗留** — 已被替代 | 迁移 v8 |
| `idx_recordings_camera_time` | `camera_id, started_at, ended_at, archived` | 主要：ListRecordings | 迁移 v9 |
| `idx_recordings_archived_time` | `archived, started_at DESC` | 主导列表模式 (WHERE archived=0 ORDER BY started_at DESC) | 迁移 v19 |
| `idx_recordings_camera_ended` | `camera_id, ended_at DESC` | 覆盖索引：GetAllLastRecordingTimes | 迁移 v19 |
| `idx_recordings_merge_status` | `merge_status` | 合并管线查询 | 迁移 v13 |

### 冗余分析

三个索引已被复合索引**替代**：
1. **`idx_recordings_camera`** — 被 `idx_recordings_camera_time` 通过最左前缀覆盖。
2. **`idx_recordings_merged`** — 被 `idx_recordings_merge_status`（更丰富的 TEXT 状态）替代。
3. **`idx_recordings_archived`** — 被 `idx_recordings_archived_time` 覆盖。

### 为何保留 `idx_recordings_time`

覆盖无 `camera_id` 过滤的时间范围扫描，被 `GetRecordingTrends` 使用：

```sql
SELECT * FROM recordings WHERE started_at >= '2026-01-01'
  AND started_at < '2026-02-01' ORDER BY started_at;
```

### 索引维护计划

```sql
REINDEX idx_recordings_camera_time;
REINDEX idx_recordings_archived_time;
```

[Pending T4] 迁移 v23 将添加 `idx_recordings_camera_merge_status`，列为 `(camera_id, started_at, merge_status)`。

---

## 4. 查询优化指南

### 示例 1: ListRecordings（分页）

使用 `idx_recordings_archived_time` 覆盖索引。动态查询构建器按条件追加过滤：

```go
func (d *DB) ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
    where, args := []string{}, []any{}
    if filter.CameraID != "" { where = append(where, "camera_id=?"); args = append(args, filter.CameraID) }
    if !filter.StartTime.IsZero() { where = append(where, "started_at>=?"); args = append(args, formatTime(filter.StartTime)) }
    sqlstr := "SELECT ... FROM recordings" + buildWhere(where) + " ORDER BY " + sortBy + " " + sortOrder
}
```

### 示例 2: GetAllLastRecordingTimes — 曾是最严重性能瓶颈

**优化前（v19 之前）：** 每次 `/api/cameras` 请求全表扫描（7.1 万+条记录）：

```sql
SELECT camera_id, MAX(ended_at) FROM recordings WHERE ended_at IS NOT NULL GROUP BY camera_id;
```

**优化后（v19）：** `idx_recordings_camera_ended` 覆盖索引 → O(摄像头数)，非 O(总记录数)：

```go
_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_recordings_camera_ended ON recordings(camera_id, ended_at DESC)")
```

### 示例 3: ListSingletonPendingRecordings（CTE 重写）

当前使用关联子查询。**优化方案** — 改写为 CTE：

```sql
WITH hourly_counts AS (
    SELECT id, camera_id, format, strftime('%Y-%m-%d %H', started_at) AS hour_bucket,
           COUNT(*) OVER (PARTITION BY camera_id, format, strftime('%Y-%m-%d %H', started_at)) AS cnt
    FROM recordings WHERE camera_id = ? AND merge_status = 'pending'
      AND ended_at IS NOT NULL AND ended_at < ?
)
SELECT * FROM hourly_counts WHERE cnt = 1;
```

[Pending T4] 与 `incompatible` 状态迁移一同部署。

### 示例 4: ListExpiredRecordings

在约 10 万行时为亚毫秒级，得益于 `idx_recordings_camera_time(ended_at)`：

```sql
SELECT id, camera_id, file_path, format, started_at, ended_at,
       duration, file_size, frame_count, merged, merge_status, archived
FROM recordings WHERE ended_at IS NOT NULL AND archived = 0
  AND camera_id = ? AND ended_at < datetime('now', '-' || ? || ' days')
ORDER BY ended_at ASC;
```

### GetRecordingTrends — 未来优化 [Pending T6]

将聚合推送到 SQL 端，避免传输原始行到 Go：

```sql
SELECT date(r.started_at) as day, COUNT(*) as recordings,
       SUM(r.file_size) as total_size, r.camera_id
FROM recordings r WHERE r.started_at >= ?
GROUP BY date(r.started_at), r.camera_id ORDER BY day;
```

---

## 5. 批量操作模式

### 批量 DELETE

清理模块分批删除录制记录。批量 API 每次最多接受 100 个 ID：

```go
func (d *DB) DeleteRecordingsBatch(ctx context.Context, ids []string) ([]string, error) {
    placeholders, args := make([]string, len(ids)), make([]interface{}, len(ids))
    for i, id := range ids { placeholders[i] = "?"; args[i] = id }
    q := "DELETE FROM recordings WHERE id IN (" + strings.Join(placeholders, ",") + ")"
    res, err := d.db.ExecContext(ctx, q, args...)
}
```

### 批量 INSERT（孤立录制恢复）

每 500 条记录在一个事务中插入：

```go
const orphanBatchSize = 500
func (d *DB) InsertOrphanRecordings(ctx context.Context, recordings []*model.Recording) (int, error) {
    for i := 0; i < len(recordings); i += orphanBatchSize {
        tx, err := d.db.BeginTx(ctx, nil)
        for _, r := range recordings[i:min(i+orphanBatchSize, len(recordings))] {
            tx.ExecContext(ctx, q, r.ID, r.CameraID, ...)
        }
        if err := tx.Commit(); err != nil { return totalInserted, err }
    }
}
```

### 事务性合并替换

`MergeAndReplaceRecordings` 在单条事务中组合 INSERT + DELETE。**在 DB 事务提交前绝不删除源文件：**

```go
if err := storage.RetryOnBusy(ctx, func() error {
    return m.db.MergeAndReplaceRecordings(ctx, mergedRec, ids)
}); err != nil {
    os.Remove(finalPath); continue
}
for _, r := range recordings { m.store.DeleteFile(r.FilePath) }
```

### 删除时的事件发布

发布事件以便 MiBeeVision 取消正在进行的 AI 处理。跳过 `ai_status = "processing"` 的录制：

```go
func (cm *CleanupManager) deleteRecording(ctx context.Context, rec *model.Recording) error {
    if status, _ := cm.db.GetRecordingAIStatus(ctx, rec.ID); status == "processing" { return nil }
    if err := cm.db.DeleteRecording(ctx, rec.ID); err != nil { return err }
    // 尽力删除文件
    if err := cm.store.DeleteFile(rec.FilePath); err != nil {
        logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
    }
    cm.eventBus.Publish(ctx, event.TopicSegmentDeleted, event.SegmentDeleted{
        RecordingID: rec.ID, CameraID: rec.CameraID, FilePath: rec.FilePath, Reason: "retention_expired",
    })
    return nil
}
```

### SQLITE_BUSY 重试模式

所有合并 DB 操作都使用 `RetryOnBusy` 包装（指数退避：100ms → 200ms → 400ms，最多 3 次重试）。[Pending T5] 批量 DELETE 中的自适应休眠将根据近期 BUSY 错误频率滑动窗口实现。

---

## 6. 数据库维护计划

### ANALYZE 节奏 (PRAGMA optimize)

`PRAGMA optimize` 在**每次启动**时迁移后以及**每次清理周期**中运行（默认每小时）。对于 >50 万行的数据库，在低活动时段（凌晨 3 点）安排每周完整 `ANALYZE`。`analysis_limit=1000` 使其快速完成。

### WAL 检查点触发策略

默认：约 1MB 自动检查点 (wal_autocheckpoint=1000)。当 WAL 超过 50MB 时，先尝试 PASSIVE。如连续 3 次报告 `busy > 0`，则切换到 TRUNCATE：

```sql
PRAGMA wal_checkpoint(PASSIVE);
PRAGMA wal_checkpoint(TRUNCATE);  -- 短时写锁，<10ms
```

### incremental_vacuum（绝不使用完整 VACUUM）

完整 `VACUUM` 会重写整个数据库文件，临时加倍存储占用，在 RPi 3B 上存在 OOM 风险。使用增量回收：

```sql
PRAGMA auto_vacuum = INCREMENTAL;
PRAGMA incremental_vacuum(256);  -- 每周期约 2MB
```

[Pending T9] 清理周期在批量删除后调用 `IncrementalVacuum`。

---

## 7. 合并管线设计

### 流程图

```text
各录制器创建段文件（状态 = 'pending'）
    │
    ▼
MergeManager.RunOnce()（定时器：可配置，默认 1 小时）
    │
    ├─ ListCameraMergeWindows() → 包含 2+ 段的每小时窗口
    ├─ processCamera() 逐个摄像头处理
    │   ├─ acquireMergeLock()（每个摄像头互斥锁，非阻塞）
    │   ├─ ListMergeableSegments()
    │   ├─ groupByFormat() → 分离 H.264、H.265、MJPEG、AVI
    │   ├─ mergeFormatGroup()
    │   │   ├─ ParseSegment()（MP4 样本表提取）
    │   │   ├─ SHA-256 SPS/PPS 分组
    │   │   ├─ 磁盘空间检查（1.1 倍估算值）
    │   │   ├─ MergeMP4Segments()（流式合并，1MB 缓冲区）
    │   │   ├─ MergeAndReplaceRecordings()（事务性）
    │   │   └─ 删除源文件（事务提交后）
    │   └─ ListSingletonPendingRecordings() → 标记为 'merged'
    │
    └─ 更新指标
```

### 段生命周期状态

| 状态 | 含义 | 转换 |
|--------|---------|--------------|
| `pending` | 等待合并 | → `merged`, `failed`, `incompatible` |
| `merged` | 已合并 | （终态，可被保留策略删除） |
| `failed` | 解析错误或 SPS/PPS 不匹配 | （终态） |
| `incompatible` | [Pending T4] 跨格式/编码，无法合并 | （终态） |

### 失败语义

- **解析失败**：标记为 `failed`，后续跳过
- **磁盘空间不足**：跳过当前摄像头，下一轮重试
- **DB 事务失败**：删除合并输出，保留源文件
- **SPS/PPS 组过小**：标记为 `failed`，避免重复解析

### 回填端点 [Pending T10]

`POST /api/cameras/{id}/merge/backfill` 手动触发历史段合并，包括已标记的段（强制重新处理）。

---

## 8. 连接池设计

### SetMaxOpenConns(1) — 单写入者串行化

SQLite 只有一个写入者。多连接时：A 开始写入 → B 尝试写入 → `SQLITE_BUSY` → 重试。使用 `SetMaxOpenConns(1)` 后，所有写入通过单连接串行化：

```go
d.db.SetMaxOpenConns(1)    // 串行化所有写入
d.db.SetMaxIdleConns(1)    // 保持 1 个连接热备
d.db.SetConnMaxLifetime(0) // 本地 SQLite，无过期连接
```

**权衡：** 长时间查询（GetRecordingTrends）会阻塞写入。缓解措施：WAL 允许并发读取、低活动时运行 `PRAGMA optimize`、支持 context 取消的查询。

### WAL 读取并发

```text
写入者 (WAL)：INSERT/UPDATE/DELETE → 追加到 WAL 文件
读取者 1/2：  SELECT               → 从主 DB + WAL 索引读取，并发
```

### temp_store = MEMORY

将临时表和索引保留在 RAM 中，而非写入 SD 卡。在 512MB 预算内可接受；对 RPi 3B 至关重要，因 SD 卡 I/O 是主要瓶颈。

---

## 9. 可观测性

### Prometheus 指标

所有指标注册在**自定义注册表** (`prometheus.NewRegistry()`) 上，而非全局默认注册表。

#### 存储与数据库指标

| 指标 | 类型 | 标签 | 描述 |
|--------|------|--------|-------------|
| `nvr_recording_count` | Gauge | — | 当前录制记录数 |
| `nvr_storage_used_bytes` | Gauge | — | 录制占用的磁盘空间 |
| `nvr_storage_total_bytes` | Gauge | — | 可用总磁盘空间 |
| `nvr_recording_bytes_total` | CounterVec | camera_id, codec | 每摄像头/编码的写入字节数 |
| `nvr_storage_write_errors_total` | Counter | — | 写入 I/O 错误 |
| `nvr_cleanup_deleted_total` | CounterVec | reason | 按原因分类的删除数 |

#### 合并管线指标

| 指标 | 类型 | 标签 | 描述 |
|--------|------|--------|-------------|
| `nvr_merge_attempts_total` | Counter | — | 合并尝试总数 |
| `nvr_merge_successes_total` | Counter | — | 成功合并数 |
| `nvr_merge_failures_total` | CounterVec | reason | 失败原因 (sps_pps_mismatch, parse_error, db_error, disk_space, timeout, audio_mismatch, io_error) |
| `nvr_merge_duration_seconds` | Histogram | — | 合并耗时 |
| `nvr_merge_size_bytes` | Histogram | — | 输出文件大小 |
| `nvr_merge_pending_segments` | GaugeVec | camera_id | 等待合并的段数 |

#### 提议指标 [Pending T9]

| 指标 | 类型 | 用途 |
|--------|------|---------|
| `nvr_db_wal_size_bytes` | Gauge | WAL 文件大小 — >100MB 告警 |
| `nvr_db_fragmentation_ratio` | Gauge | 碎片化率 — >20% 告警 |
| `nvr_db_query_duration_seconds` | Histogram | 查询延迟（前 5 模式） |
| `nvr_db_connection_busy_errors_total` | Counter | SQLITE_BUSY 错误率 |
| `nvr_cleanup_duration_seconds` | Histogram | 清理周期耗时 |

### 告警阈值

| 条件 | 严重级别 | 阈值 | 操作 |
|-----------|----------|---------|--------|
| WAL 文件大小 | 警告 | >50MB | TRUNCATE 检查点 |
| WAL 文件大小 | 严重 | >200MB | 分析读取模式 |
| 数据库碎片化 | 警告 | >20% | incremental_vacuum |
| 查询耗时 (p99) | 警告 | >500ms | ANALYZE，检查索引 |
| 合并失败率 | 警告 | 1h 内 >10% | 检查损坏 |
| SQLITE_BUSY 率 | 警告 | >100/小时 | 减少并发写入者 |

---

## 10. 未来扩展手册

### 决策树

```text
当前数据库大小？
│
├─ < 100MB → 无需操作。当前配置已足够。
│
├─ 100MB – 500MB → 每周 ANALYZE，启动时 PRAGMA optimize
│   └─ 确保存在 idx_recordings_archived_time（v19+）
│
├─ 500MB – 10GB → 按时间分区（按月分表）
│   └─ [Pending T8] recordings_2026_01, recordings_2026_02 等
│       UNION ALL 视图实现透明查询
│
└─ > 10GB → 迁移到 PostgreSQL（外部进程）
    └─ [Pending T12] 步骤：pgx/v5 驱动, pg_cron, 流式复制
```

### 何时分区（50 万行以上）

迹象：`ListRecordings` 带时间过滤 >200ms，`CountRecordingsWithFilter` >500ms，`GetRecordingTrends`（7 天）>1s，WAL 检查点 >5s。

**提议方案 [Pending T8]：**

```sql
CREATE TABLE recordings_2026_01 (
    CHECK (started_at >= '2026-01-01' AND started_at < '2026-02-01')
) INHERITS (recordings);
-- 或 SQLite 方案：ATTACH 'recordings_2026_01.db' AS jan;
```

### 何时迁移到 PostgreSQL（10GB+ 数据库）

迹象：DB >10GB，VACUUM >30min，WAL >500MB，需要并发写入，需要 PITR/复制。

**前提条件 [Pending T12]：** PostgreSQL 16+，pgx/v5 驱动，Schema 转换 (DATETIME→TIMESTAMPTZ, INTEGER→SERIAL/BIGSERIAL, REAL→DOUBLE PRECISION)。

### 何时不迁移

保持使用 SQLite 如果：录制数 <50 万，DB <5GB，单 NVR 实例，RAM <1GB，SD 卡为主要存储。

### 长期策略

```text
阶段 1 (当前)：SQLite 单文件，WAL 模式，优化索引
阶段 2 (T8)：  基于时间的分区表或独立 DB 文件
阶段 3 (T12)： 迁移到 PostgreSQL + pgx 驱动
阶段 4 (未来)： ClickHouse 用于分析查询（GetRecordingTrends、长期统计）
```

---

> **参考文件：**
> - `internal/storage/db.go` — DSN、Init、迁移、时间处理
> - `internal/storage/db_recording.go` — 录制 CRUD、批量操作
> - `internal/storage/db_stats.go` — CountRecordings、GetRecordingTrends
> - `internal/storage/db_merge.go` — MergeAndReplaceRecordings、ListMergeableSegments
> - `internal/storage/retry.go` — RetryOnBusy、IsBusyError
> - `internal/cleanup/cleanup.go` — CleanupManager、基于时间/磁盘阈值的清理
> - `internal/merge/manager.go` — MergeManager、processCamera、mergeFormatGroup
> - `internal/metrics/metrics.go` — 合并指标、存储指标
> - `internal/storage/AGENTS.md` — 存储约定
> - `docs/zh/metrics.md` — 完整指标参考
