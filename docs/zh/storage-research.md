# 存储层研究报告 — MiBee NVR

> 本文档涵盖 SQLite 存储架构、查询模式、N+1 问题、增长预测及最佳实践的技术研究。

**版本：** 1.0  
**背景：** v0.9.0 存储层重构规划  
**交叉引用：** [docs/en/storage-research.md](../en/storage-research.md)

---

## 1. 执行摘要

MiBee NVR 存储层使用 SQLite 数据库，采用 `modernc.org/sqlite` 纯 Go 驱动，运行于 WAL 模式。生产环境（Banana Pi M5，`192.168.1.10`）的数据库包含 **67,000+ 条录制记录**，数据库文件 **86 MB**，每天新增录制数据约 **90 GB**（文件数据，非数据库大小——数据库仅存储元数据）。

**关键发现：**

| 发现项 | 严重程度 | 影响 |
|--------|----------|------|
| 47% 合并失败率 | 严重 | 约半数段合并失败，留下孤立的待处理录制记录 |
| 8 个 N+1 查询模式确认 | 高 | 清理、合并、API 和统计模块均存在 N+1 循环 |
| 冗余/重叠索引 | 中 | `idx_recordings_merged` 和 `idx_recordings_camera` 与 v22 新增的复合索引重叠 |
| 无 ANALYZE 调度 | 中 | 查询规划器可能在会话中选择次优计划；`PRAGMA optimize` 仅在启动时运行 |
| GetRecordingTrends 将所有行加载到 Go 内存 | 中 | 没有使用 SQL 级 GROUP BY；每次统计请求扫描 67K+ 行到内存 |

**建议：**

1. 修复 N+1 模式（批量查询、在 SQL 中聚合而非 Go）
2. 定期调度 `ANALYZE` 和 WAL 检查点操作
3. 为三个最昂贵的查询模式添加覆盖索引
4. 实现使用 `IN` 子句的批量 DELETE（`DeleteRecordingsBatch` 已部分实现）
5. 监控增长阈值并准备分区或 PostgreSQL 迁移方案

---

## 2. 当前架构分析

### 2.1 模式概览

数据库有 3 个核心表和 8 个辅助表，所有表通过 `Init()` 中的迁移创建（`internal/storage/db.go:50`）：

**核心表：**

- **`cameras`** — 20+ 列：`id`(TEXT 主键), `name`, `protocol`, `encoding`, `url`, `username`, `password`, `enabled`, `description`, `location`, `brand`, `model`, `serial_number`, `retention_days`, `onvif_endpoint`, `profile_token`, `stream_encoding`, `archived`, `archived_at`, `archive_retention_days`, `merge_enabled`, `merge_check_interval`, `merge_window_size`, `merge_batch_limit`, `merge_min_segment_age`, `merge_min_segments_to_merge`, `merge_duration`, `stream_key`, `srt_passphrase`, `srt_stream_id`, `created_at`

- **`recordings`** — 25+ 列：`id`(TEXT 主键), `camera_id`(外键→cameras), `file_path`, `format`, `started_at`, `ended_at`, `duration`, `file_size`, `frame_count`, `merged`, `merge_status`, `merge_path`, `merge_tier`, `merge_progress`, `merge_error`, `retry_count`, `archived`, `ai_status`, `ai_processed_at`, `ai_error`, `created_at`

- **`schema_meta`** — 模式版本追踪（`key`/`value`）

**辅助表：**

`feature_flags`, `camera_health_events`, `transcoding_tasks`, `ai_events`，以及若干与合并相关的表（`merged_recordings`, `merge_queue`, `pending_ai_recordings`, `storage_stats`）。

### 2.2 迁移历史（22+ 次迁移）

迁移嵌入在 `Init()` 中（`internal/storage/db.go:50–379`），每个迁移使用 `IF NOT EXISTS` / 列存在性检查来保证幂等性：

| 版本 | 变更内容 | 关键代码 |
|------|---------|----------|
| 1→2 | 摄像头元数据列（description, location, brand, model, serial） | `db.go:106` |
| 2→3 | 摄像头 `retention_days` 列 | `db.go:120` |
| 3→4 | `pinned` → `merged` 重命名，`idx_recordings_merged` 索引 | `db.go:125` |
| 4→5 | 摄像头合并配置列（5 列） | `db.go:146` |
| 5→6 | ONVIF 列（`onvif_endpoint`, `profile_token`） | `db.go:163` |
| 6→7 | `stream_encoding` 列 | `db.go:169` |
| 7→8 | 归档列 + 索引（`archived`, `archived_at`, `archive_retention_days`） | `db.go:176` |
| 8→9 | `feature_flags` 表 + `idx_recordings_camera_time` 索引 | `db.go:188` |
| 9→10 | `camera_health_events` 表 + 索引 | `db.go:207` |
| 10→11 | `transcoding_tasks` 表 + 索引 | `db.go:224` |
| 11→12 | 转码索引（4 个覆盖索引） | `db.go:248` |
| 12→13 | `merge_status` 列 + recordings 索引 | `db.go:258` |
| 13→14 | 转码 `framerate` 列 | `db.go:272` |
| 14→15 | recordings 的 `merge_path`, `merge_error` 列 | `db.go:280` |
| 15→16 | `merge_tier` 列 | `db.go:290` |
| 16→17 | 摄像头 `merge_duration` 列 | `db.go:298` |
| 17→18 | `merge_progress` 列 | `db.go:306` |
| 18→19 | 转码 `bitrate`, `crf` 列 | `db.go:314` |
| 19→20 | `ai_events` 表 + 索引 | `db.go:323` |
| 20→21 | `ai_status`, `ai_processed_at`, `ai_error` 列 | `db.go:344` |
| 21→22 | SRT/RTMP 推流列（`stream_key`, `srt_passphrase`, `srt_stream_id`） | `db.go:355` |
| v22+ | `idx_recordings_archived_time` + `idx_recordings_camera_ended` 覆盖索引 | `db.go:367` |

### 2.3 所有索引（recordings 表）

| 索引名称 | 列 | 创建于 | 备注 |
|----------|-----|--------|------|
| `idx_recordings_camera` | `camera_id` | v1 | 被 `idx_recordings_camera_time` 重叠 |
| `idx_recordings_time` | `started_at` | v1 | 对独立时间查询仍有用 |
| `idx_recordings_merged` | `merged` | v4 | 选择率低（约 1/3 merged=0） |
| `idx_recordings_archived` | `archived` | v8 | 零选择性（archived=0 匹配所有行） |
| `idx_recordings_camera_time` | `camera_id, started_at, ended_at, archived` | v9 | 用于 ListRecordings 的 4 列复合索引 |
| `idx_recordings_merge_status` | `merge_status` | v13 | 中等选择性 |
| `idx_recordings_archived_time` | `archived, started_at DESC` | v22 | 用于列表查询的覆盖索引 |
| `idx_recordings_camera_ended` | `camera_id, ended_at DESC` | v22 | 用于 GetAllLastRecordingTimes |

### 2.4 DSN 配置

在 `New()` 中配置（`internal/storage/db.go:42`）：

```text
_pragma=journal_mode(WAL)
_pragma=synchronous(NORMAL)
_pragma=busy_timeout(15000)
_pragma=cache_size(-20000)
_pragma=mmap_size(268435456)
```

关键特性：
- **WAL 模式**：并发读取不阻塞写入；写入事务不阻塞读取
- **NORMAL 同步**：每次 CHECKPOINT 时 FSYNC，而不是每次事务——对元数据的可接受崩溃安全性
- **15 秒 busy_timeout**：RPi 负载下 SQLITE_BUSY 争用的超时时间
- **20MB 缓存（-20000 页）**：约 20MB 页缓存用于 B-tree 查找
- **256MB mmap**：内存映射 I/O 加速 RPi 3B（1GB RAM 约束）的读取

### 2.5 查询接口

存储包暴露约 40 个查询方法，涵盖摄像头和录制记录的 CRUD、合并操作（窗口列表、段列表、单例检测）、清理查询（按摄像头的过期录制、最旧录制、AI 状态）、统计聚合和转码任务管理。文件：`db_recording.go`（586 行）、`db_merge.go`（229 行）、`db_stats.go`（157 行）、`db_transcoding.go`（80+ 行）、`db_ai.go`（50+ 行）。

---

## 3. 生产数据库健康报告

### 3.1 当前状态

| 指标 | 数值 | 来源 |
|------|------|------|
| 总录制记录数 | ~67,000 行 | `CountRecordings()` |
| 数据库文件大小 | ~86 MB | 磁盘测量 |
| 合并失败率 | ~47% | 生产环境观察（合并尝试 vs 成功） |
| 每日录制增长 | ~90 GB/天（文件数据） | 根据段时长 × 摄像头数估算 |
| SQLite 页大小 | 4096 字节（默认） | `PRAGMA page_size` |
| WAL 文件大小 | 可变（5–20 MB） | 取决于检查点频率 |
| 缓存命中率 | 未知 | 未收集 `PRAGMA stats` |

### 3.2 增长率

以 67K 记录和约 30 天保留窗口计算：
- **每日新记录：** ~2,200 行/天
- **每日数据库增长：** ~2.8 MB/天（仅元数据，约 1.3 KB/行）
- **录制文件数据：** ~90 GB/天（所有摄像头合计）
- **90GB/天**的视频文件意味着数据库元数据增长约为存储的 0.003%——数据库本身不是瓶颈。

### 3.3 WAL 和碎片化

- WAL 模式意味着 WAL 文件累积写入直到检查点发生
- 如果没有明确的检查点调度，WAL 在高录制期间可能变大
- 没有 VACUUM 调度；删除的行在数据库文件中留下空闲页面
- `PRAGMA optimize` 只在启动时运行（`db.go:377`），不是定期运行

---

## 4. SQLite 扩展阈值

### 4.1 理论限制

| 参数 | SQLite 限制 | 对 NVR 相关？ |
|------|-------------|--------------|
| 最大数据库大小 | 281 TB | 否（当前 86 MB） |
| 每表最大行数 | ~2^64 | 否（当前 67K） |
| 每表最大列数 | 2000 | 接近（recordings 表 25+ 列） |
| 最大附加数据库数 | 125 | 未使用 |
| 最大查询参数数 | 32766 | 对批量 IN 子句相关 |

### 4.2 时间序列元数据的实际阈值

对于录制日志等时间序列元数据，SQLite 在 **10–50 GB** 之前是实用的：

- **B-tree 深度**：86 MB / ~67K 行时，深度约 2-3 级。10 GB / ~8M 行时，深度达 4-5 级。
- **INSERT 吞吐量**：RPi 3B SD 卡上约 50-200 写入/秒（WAL 模式）
- **查询性能**：与索引大小 + WAL 文件大小成比例下降
- **CHECKPOINT 开销**：在大 WAL 刷新期间可能导致 RPi 3B 上的延迟峰值

### 4.3 RPi 3B 约束

| 资源 | 限制 | 对 SQLite 的影响 |
|------|------|-----------------|
| 内存 | 1 GB | mmap_size=256MB（内存的 25%），剩余 768MB 给应用 + 操作系统 |
| SD 卡 I/O | ~10-20 MB/s 顺序 | WAL 检查点和 ANALYZE 可能导致写入延迟 |
| CPU | 4× Cortex-A53 @ 1.2 GHz | 查询编译开销最小；瓶颈是 I/O |
| 进程内存 | 512 MB 预算 | cache_size 为 20MB 合理；不要增加 |

**关键关注点**：SD 卡写入寿命有限。每次额外的索引更新、ANALYZE 或 VACUUM 都会写入日志/WAL。尽量减少写入放大。

---

## 5. NVR 行业对比

### 5.1 Frigate

- **数据库**：PostgreSQL（通过 `psycopg2` + `asyncpg`）
- **模式**：以事件为中心（events 表包含 camera, label, zone, thumbnail, snapshot）
- **优势**：适当的关系连接、窗口函数、并发读写、丰富查询
- **权衡**：部署较重（数据库服务器），内存消耗更多
- **相关性**：PostgreSQL 是多摄像头 NVR 元数据的黄金标准

### 5.2 Shinobi

- **数据库**：MySQL（MariaDB）或 SQLite（可配置）
- **模式**：Monitors、recordings、logs 分表；recordings 可以很大
- **优势**：MySQL 处理并发访问良好；引擎可配置
- **权衡**：SQLite 模式在规模上与 MiBee NVR 面临相同限制
- **相关性**：验证 SQLite 选择对中小型部署是可接受的

### 5.3 ZoneMinder

- **数据库**：MySQL（MariaDB）
- **模式**：Events、Frames、Stats — 高度规范化，包括帧级细节
- **优势**：成熟的模式设计，适当的索引；处理数千个事件
- **权衡**：复杂模式增加查询开销；需要 MySQL 调优
- **相关性**：展示了视频元数据过度规范化的成本

### 5.4 Kerberos.io

- **数据库**：SQLite（每个实例单个文件）
- **模式**：最小化 — recordings, cameras, snapshots
- **优势**：简单、可移植、易于备份
- **权衡**：不支持多进程访问；仅限于单实例使用
- **相关性**：类似于 MiBee NVR 的架构；验证了单文件方法

### 5.5 MotionEye

- **数据库**：SQLite
- **模式**：非常小 — events, movies, images
- **优势**：极其简单，零配置
- **权衡**：无查询能力；平面文件映射
- **相关性**：展示了 NVR 存储的最小可行模式

### 5.6 对比总结

| 特性 | MiBee NVR | Frigate | Shinobi | ZoneMinder | Kerberos.io | MotionEye |
|------|-----------|---------|---------|------------|-------------|-----------|
| 引擎 | SQLite | PostgreSQL | MySQL/SQLite | MySQL | SQLite | SQLite |
| 纯 Go | 是 | 否 | 否 | 否 | 否 | 否 |
| 模式大小 | ~25 列 | ~15 列 | ~20 列 | ~40+ 列 | ~10 列 | ~8 列 |
| 增长到 10M 行 | 担忧 | 良好 | 良好（MySQL）| 良好 | 担忧 | 担忧 |
| 并发访问 | 单进程 | 多进程 | 多进程 | 多进程 | 单进程 | 单进程 |

**关键洞察**：对于 500K 以下录制记录的单进程嵌入式 NVR，SQLite 是正确的选择。MiBee NVR 当前的 67K 行在范围内。问题不在于 SQLite 的限制，而在于**查询模式**（N+1 循环）和**合并可靠性**（47% 失败率）。

---

## 6. N+1 查询模式分析

在代码库中确认了八个 N+1 模式：

### 模式 1：清理 — 按摄像头循环

**位置：** `internal/cleanup/cleanup.go:131`（`timeBasedCleanup`）

```go
for _, cam := range cameras {                          // 1 次查询 (ListCameras)
    recordings, err := cm.db.ListExpiredRecordingsByCamera(ctx, cam.ID, retentionDays)  // N 次查询
    for _, rec := range recordings {
        cm.deleteRecording(ctx, &rec)                  // M 次查询 (内部 N+1)
    }
}
```

**查询数：** 1 + N（摄像头数）×（1 + M 录制数）。对于 10 个摄像头，每个约 200 条过期记录：**1 + 10 + 2,000 = 2,011 次查询**。

### 模式 2：清理 — 每条记录检查 AI 状态

**位置：** `internal/cleanup/cleanup.go:242`（`deleteRecording` → `GetRecordingAIStatus`）

```go
func (cm *CleanupManager) deleteRecording(ctx context.Context, rec *model.Recording) error {
    if status, err := cm.db.GetRecordingAIStatus(ctx, rec.ID); err == nil && status == "processing" {
        // N+1：每条记录一次 SELECT
    }
}
```

**查询数：** 每条记录 1 次 SELECT。通过在初始的 `ListExpired*` 查询中通过 JOIN 或子查询一次性获取 AI 状态来修复。

### 模式 3：清理 — 逐行 DeleteRecording

**位置：** `internal/cleanup/cleanup.go:154-158`（`timeBasedCleanup` 内部）

```go
for _, rec := range recordings {
    if err := cm.deleteRecording(ctx, &rec); err != nil { ... }
}
```

每次 `deleteRecording` 调用执行 `DeleteRecording(ctx, rec.ID)`——单行 DELETE。对于 200 条记录：**200 个 DELETE 语句**。应使用 `DeleteRecordingsBatch`（`internal/storage/db_recording.go:354`），它发出单个 `DELETE ... WHERE id IN (...)`。

### 模式 4：合并 — 按摄像头 processCamera

**位置：** `internal/merge/manager.go:238`（`processCamera`）

```go
func (m *MergeManager) processCamera(ctx context.Context, cameraID string, ...) (...) {
    windows, err := m.db.ListCameraMergeWindows(ctx, cameraID, minAge)     // 1 次查询
    for _, win := range windows {
        recs, err := m.db.ListMergeableSegments(ctx, cameraID, win.StartTime, win.EndTime)  // N 次查询
    }
    singletons, err = m.db.ListSingletonPendingRecordings(ctx, cameraID, minAge)  // 1 次查询（相关子查询）
}
```

对于每个合并窗口（每摄像头每天通常 24 个），`ListMergeableSegments` 被单独调用。**查询数：** 1 + N_窗口 + 1。

### 模式 5：API — handleBatchDeleteRecordings

**位置：** `internal/api/handlers_recording.go:301`（`handleBatchDeleteRecordings`）

```go
for _, id := range body.IDs {
    rec, err := h.db.GetRecording(ctx, id)  // N 次查询：每个 ID 一次
    if err == nil && rec != nil && rec.FilePath != "" {
        filePaths[id] = rec.FilePath
    }
}
```

对于 100 个 ID：**100 次 SELECT 查询**，然后才进行批量 DELETE。通过使用单个 `SELECT file_path FROM recordings WHERE id IN (...)` 一次性获取所有文件路径来修复。

### 模式 6：API — handleTranscodingBackfill

**位置：** `internal/api/handlers_transcode.go:596`（`handleTranscodingBackfill`）

```go
for _, rec := range recordings {
    task := &storage.TranscodeTask{ ... }
    if err := h.db.EnqueueTask(r.Context(), task); err != nil {  // N 次查询：每条记录一次 INSERT
    }
}
```

对于 500 条未转码的记录：**500 次 INSERT 语句**。通过批量 INSERT 修复。

### 模式 7：统计 — GetRecordingTrends 将所有行加载到内存

**位置：** `internal/storage/db_stats.go:27-120`（`GetRecordingTrends`）

```go
query := `SELECT r.started_at, r.file_size, r.camera_id, COALESCE(c.name, r.camera_id) as camera_name
    FROM recordings r LEFT JOIN cameras c ON r.camera_id = c.id
    WHERE r.started_at >= ?
    ORDER BY r.started_at`
```

将日期范围内的**所有原始行**加载到 Go 内存中并在 Go 代码中聚合。对于 30 天 ~2,200 行/天：**扫描 66,000 行**，全部加载到 RAM，然后用 Go map 聚合。通过使用 SQL 级 `GROUP BY strftime(...)` 修复。

### 模式 8：合并 — ListSingletonPendingRecordings 相关子查询

**位置：** `internal/storage/db_merge.go:194-212`（`ListSingletonPendingRecordings`）

```go
SELECT r.id, ... FROM recordings r
WHERE r.camera_id = ?
  AND ...
  AND (
    SELECT COUNT(*) FROM recordings r2
    WHERE r2.camera_id = r.camera_id
      AND strftime('%Y-%m-%d %H', r2.started_at) = strftime('%Y-%m-%d %H', r.started_at)
  ) = 1;
```

相关子查询为每个候选行执行一次。对于 N 条待处理记录，这是 **1 个外部查询 + N 次子查询执行**。如果没有 `strftime()` 上的索引，每个子查询都会进行全表扫描。此模式目前在每个合并周期运行。

---

## 7. 增长预测

### 7.1 录制记录行增长

基于每天约 2,200 条新记录（67K 总数 ÷ 约 30 天）：

| 周期 | 行数估算 | 数据库大小（元数据） | 备注 |
|------|---------|-------------------|------|
| 当前 | ~67,000 | ~86 MB | 生产基线 |
| 30 天 | ~8,000 新增 | ~10 MB 增量 | ~3 个新摄像头 × 365 条记录/天 |
| 90 天 | ~24,000 新增 | ~31 MB 增量 | 典型部署周期 |
| 1 年 | ~159,000 新增 | ~205 MB 增量 | 当前摄像头数量下 |
| 5 年 | ~795,000 新增 | ~1 GB 增量 | 在保留策略不变的情况下 |

### 7.2 文件数据增长（视频文件）

| 周期 | 所需存储（90 GB/天） |
|------|---------------------|
| 1 天 | 90 GB |
| 7 天 | 630 GB |
| 30 天 | 2.7 TB |
| 90 天 | 8.1 TB |

*注：实际保留由磁盘阈值清理和按摄像头 retention_days 管理。90 GB/天的估计很大程度上取决于分辨率、编解码器和摄像头数量。*

### 7.3 SQLite 何时成为问题

在当前增长率下：

- **数据库元数据达到 1 GB**：约 2-3 年（SQLite 可接受）
- **录制记录超过 500K**：约 6-12 个月（SQLite 元数据的实际舒适区）
- **常见模式查询时间超过 500ms**：约 3-6 个月（未优化；N+1 修复将推后这一时间点）
- **WAL 检查点延迟超过 1 秒**：负载下的 RPi 3B SD 卡上已可能发生

---

## 8. 最佳实践目录

### 8.1 ANALYZE 调度

SQLite 的查询规划器使用 `ANALYZE` 收集的表/索引统计信息。如果没有最新的统计信息，规划器可能选择次优的查询计划：

- **当前状态：** `PRAGMA optimize` 在启动时运行一次（`db.go:377`）——这执行增量 ANALYZE，但仅针对当前连接会话
- **建议：** 通过定时器调度 `ANALYZE`（每小时或在每 1000 次 INSERT/DELETE 操作后）
- **SQL：** `PRAGMA optimize`（增量）或 `ANALYZE`（完全重建）
- **参考：** [SQLite — ANALYZE](https://www.sqlite.org/lang_analyze.html)

### 8.2 WAL 检查点策略

WAL 文件随写入活动增长。定期检查点保持 WAL 大小可控：

- **当前状态：** 默认自动检查点（触发于 1000 页 ≈ 4MB）
- **建议：** 每 5 分钟或在批量操作后定期显式检查点 `PRAGMA wal_checkpoint(TRUNCATE)`
- **权衡：** 频繁检查点增加磁盘写入（SD 卡磨损）但减少崩溃时的 WAL 恢复时间
- **参考：** [SQLite — WAL 模式](https://www.sqlite.org/wal.html)

### 8.3 批量 DELETE 模式

- **反模式：** 循环中逐行 `DELETE FROM recordings WHERE id=?`（当前 cleanup.go 中的 `deleteRecording`）
- **推荐：** 每批最多 100 个 ID 的单一 `DELETE FROM recordings WHERE id IN (?, ?, ...)`（如 `DeleteRecordingsBatch` 中实现，`db_recording.go:354`）
- **事务边界：** 在单个事务中包装批量 DELETE + 批量文件删除

### 8.4 覆盖索引

- **反模式：** 从 recordings 读取 12+ 列但仅按 `camera_id` 和 `started_at` 过滤的 `SELECT *` 查询
- **推荐：** 设计包含所有选中列的覆盖索引以避免 B-tree 查找
- **示例：** `idx_recordings_camera_time (camera_id, started_at, ended_at, archived)` 覆盖了常见的 ListRecordings 查询而无需访问表
- **v22 新增：** `idx_recordings_archived_time (archived, started_at DESC)` 避免了 `idx_recordings_archived` 的零选择性问题

### 8.5 连接池

- **当前状态：** 使用 `sql.Open()` 和 `modernc.org/sqlite` 默认值
- **SQLite 行为：** 池中的每个连接独立操作；WAL 模式下池大小 > 1 对读并发是安全的
- **建议：** 设置 `SetMaxOpenConns(1)` 或根据具体工作负载调整。`modernc.org/sqlite` 与 `mattn/go-sqlite3` 相比连接池支持有限
- **注意：** DSN 级别的 pragma 确保池中每个连接使用相同设置（`busy_timeout`, `cache_size` 等）——这在 `New()` 中已正确实现（`db.go:42`）

### 8.6 temp_store

- **当前状态：** `temp_store` 默认为 FILE（为临时表和排序结果使用磁盘）
- **建议：** 对于 RPi 3B，`PRAGMA temp_store=MEMORY`（录制元数据查询有足够内存）
- **参考：** [SQLite — temp_store](https://www.sqlite.org/pragma.html#pragma_temp_store)
- **权衡：** 内存 temp_store 在非常大的排序操作中存在 `SQLITE_NOMEM` 风险。鉴于元数据大小（< 100 MB），此风险是可接受的。

### 8.7 其他最佳实践

| 实践 | 当前 | 目标 | 优先级 |
|------|------|------|--------|
| 外键 | 未强制 | `PRAGMA foreign_keys=ON` | 低 |
| 增量 BLOB I/O | 未使用 | 对大 `metadata` 字段相关 | 低 |
| `PRAGMA mmap_size` | 256 MB | 256 MB（1GB RAM 正确） | 当前可接受 |
| VACUUM 调度 | 从不 | 批量删除后执行 `VACUUM` | 中 |
| `PRAGMA page_size` | 4096（默认） | 8192 用于元数据密集型工作负载 | 低 |

---

## 9. 未来可扩展路径

### 9.1 分区模式（已文档化，未实现）

对于超过 500K 行的基于 SQLite 的扩展，考虑**按时间手动分区**：

```text
recordings_2025_01
recordings_2025_02
recordings_2025_03
...
```

每个分区是一个独立的表；查询通过视图或应用程序逻辑路由。这使每个分区保持在 50K 行以下，避免了 SQLite 单表大小限制。

**分区的触发条件：**
- recordigs 表超过 500K 行
- 尽管有覆盖索引，ListRecordings 的查询延迟仍超过 2 秒
- 数据库文件超过 1 GB

**要避免的反模式：** 过早分区。在 67K 行时，分区增加复杂性而没有收益。

### 9.2 PostgreSQL 迁移触发器

以下事件将触发从 SQLite 到 PostgreSQL 的迁移：

| 触发器 | 阈值 | 操作 |
|--------|------|------|
| 数据库文件大小 | > 2 GB | 评估 PostgreSQL 迁移 |
| 录制记录数 | > 200 万行 | 为查询性能迁移 |
| 并发读取者 | > 5 个同时连接（超出现有单进程模式用于 P2P）| 迁移到客户端-服务器数据库 |
| 写入吞吐量 | > 500 写入/秒持续 | PostgreSQL 处理更高的 TPS |
| 多实例需求 | P2P 或分布式部署 | PostgreSQL 或只读副本 |

**迁移策略**（触发时）：
1. 运行 `VACUUM INTO`（已在 `db.go:418` 中实现）以获取干净的 SQLite 备份
2. 通过 `.dump` 导出或使用 `pgloader` 进行批量传输
3. 在过渡期间实现双写（同时写入 SQLite + PostgreSQL）
4. 验证延迟后将读取切换到 PostgreSQL

### 9.3 只读副本（PostgreSQL 迁移后）

如果发生 PostgreSQL 迁移，架构支持：

- **主库：** 写入到单个 PostgreSQL 主库
- **副本：** 只读查询（回放、统计、摄像头列表）到副本
- **缓存：** 前端缓存（API 响应缓存）减少元数据存储的读取负载
- **分离：** 录制文件路径保留在数据库中；实际视频文件始终留在本地/网络存储上

---

## 参考

- SQLite 文档：https://www.sqlite.org/docs.html
- `internal/storage/db.go` — 模式、迁移、DSN、`Init()`、`Backup()`
- `internal/storage/db_recording.go` — 录制 CRUD、查询方法
- `internal/storage/db_merge.go` — 合并窗口/段查询
- `internal/storage/db_stats.go` — 统计聚合（`GetRecordingTrends`）
- `internal/cleanup/cleanup.go` — `timeBasedCleanup()`, `deleteRecording()`, `diskThresholdCleanup()`
- `internal/merge/manager.go` — `processCamera()`, `RunOnce()`
- `internal/api/handlers_recording.go` — `handleBatchDeleteRecordings()`
- `internal/api/handlers_transcode.go` — `handleTranscodingBackfill()`
