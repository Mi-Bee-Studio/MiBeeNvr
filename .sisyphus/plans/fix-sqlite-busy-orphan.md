# Fix SQLITE_BUSY & Orphaned Recordings

## TL;DR

> **Quick Summary**: 修复 SQLITE_BUSY 数据库锁竞争导致 63% 录像元数据丢失、合并管理器事务效率低、以及磁盘上 1832 个孤立文件无法在 Web UI 中显示的三个关联问题。
> 
> **Deliverables**:
> - `InsertRecordingWithRetry()` 方法（3 次重试 + 退避），替换 4 个录像器的 `InsertRecording` 调用
> - `MergeAndReplaceRecordings()` 原子事务方法，将 3 次独立 DB 操作合并为 1 次
> - `ReconcileOrphanedFiles()` 启动时孤立文件回收，将 1832 个未注册文件导入数据库
> - 补充测试覆盖：重试逻辑、事务原子性、回收正确性
> 
> **Estimated Effort**: Medium
> **Parallel Execution**: YES - 2 waves + test wave
> **Critical Path**: Task 1 (retry) → Task 4 (tests for retry) | Task 2 (merge tx) → Task 5 (tests for merge) | Task 3 (reconcile) → Task 6 (tests for reconcile) → Task 7 (integration)

---

## Context

### Original Request
小米摄像头很久没有录制成功视频了。排查发现摄像头实际在录制（MP4 文件写入磁盘），但 63% 的文件在数据库中无记录，导致 Web UI 看不到录像。根因是 SQLITE_BUSY 锁竞争。

### Interview Summary
**Key Discussions**:
- 根因确认：InsertRecording 无重试机制，MergeManager 60 次/轮独立事务竞争锁
- 修复方案：三层修复（重试 + 事务合并 + 孤立回收）
- 测试策略：Tests after（先修复再补测试）

**Research Findings**:
- 361 次 SQLITE_BUSY/24h，5 个录像器每 30 秒各写一次 DB
- MergeManager 每轮合并 200-300 个片段，每个合并组 3 次独立事务
- 1832 个孤立文件（磁盘存在但 DB 无记录）
- SQLite WAL 模式，busy_timeout=5s，无并发访问测试

### Metis Review
**Identified Gaps** (addressed):
- 重试不应加到 WebDAV/FTP/HTTP 上传处理器（同步请求，会阻塞）→ 创建独立的 `InsertRecordingWithRetry` 方法
- 回收需跳过不存在的 camera_id 目录（foreign_keys 未启用）→ 交叉验证 cameras 表
- SetMerged(true) 调用冗余 → 合并事务中直接 INSERT with merged=true
- 回收需处理 ID 碰撞 → 使用 INSERT OR IGNORE
- 回收需要批量事务（1832 条记录逐条提交太慢）→ 单事务批量插入

---

## Work Objectives

### Core Objective
消除 SQLITE_BUSY 导致的录像元数据丢失，回收已存在的孤立文件，优化合并事务效率。

### Concrete Deliverables
- `internal/storage/db.go`: 新增 `InsertRecordingWithRetry()` 方法
- `internal/storage/db.go`: 新增 `MergeAndReplaceRecordings()` 原子事务方法
- `internal/storage/db.go`: 新增 `GetRecordingsByPathSet()` 批量路径查询
- `internal/storage/manager.go`: 新增 `ReconcileOrphanedFiles()` 启动回收
- `internal/recorder/h264.go`, `h265.go`, `mjpeg.go`, `http_jpeg.go`: 替换 InsertRecording 调用
- `plugins/xiaomi/recorder.go`: 替换 InsertRecording 调用
- `internal/merge/manager.go`: 替换合并组 DB 操作为原子事务
- `cmd/mibee-nvr/main.go`: 启动时调用 ReconcileOrphanedFiles
- 测试文件覆盖所有新增功能

### Definition of Done
- [ ] `rtk go test ./internal/storage/... -v` → ALL PASS
- [ ] `rtk go test ./internal/merge/... -v` → ALL PASS
- [ ] `rtk go vet ./...` → 无错误
- [ ] `rtk make build` → 成功编译
- [ ] 部署后 `journalctl -u mibee-nvr | grep SQLITE_BUSY` 频率降至 <10/24h

### Must Have
- InsertRecordingWithRetry 在 4 个录像器 + 1 个插件中使用
- MergeAndReplaceRecordings 替换 merge/manager.go 中的 3 次独立 DB 调用
- ReconcileOrphanedFiles 在 main.go 中 camMgr.Start() 之前同步执行
- 所有孤立文件在启动时导入数据库（跳过不存在的 camera_id）
- 合并事务中直接 INSERT with merged=true，消除 SetMerged 调用
- 文件删除移至事务提交之后

### Must NOT Have (Guardrails)
- ❌ 不要修改 DB schema（无新表、新列、migration）
- ❌ 不要给 WebDAV/FTP/HTTP 上传处理器加重试（会阻塞同步请求）
- ❌ 不要增加 busy_timeout 超过 10s
- ❌ 不要在回收时读取文件内容（仅 stat + 文件名解析）
- ❌ 不要添加定期回收（仅启动时执行）
- ❌ 不要在录像器 InsertRecording 失败时删除已写入的文件
- ❌ 不要修改 ListCameraMergeWindows 或 ListMergeableSegments 查询
- ❌ 不要添加 Prometheus 指标（后续优化）

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES
- **Automated tests**: Tests after
- **Framework**: Go testing + testify/require
- **Pattern**: Real SQLite temp files, no mocks

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Go packages**: Use Bash (go test) - Run tests, check exit code, verify output
- **Build**: Use Bash (make build) - Verify compilation succeeds
- **Integration**: Use Bash (go vet) - Check for code issues

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Core implementations - 3 parallel tasks):
├── Task 1: InsertRecordingWithRetry [quick]
├── Task 2: MergeAndReplaceRecordings atomic transaction [deep]
└── Task 3: ReconcileOrphanedFiles startup recovery [deep]

Wave 2 (Caller updates - 2 parallel tasks, depend on Wave 1):
├── Task 4: Update recorders to use InsertRecordingWithRetry (depends: 1) [quick]
└── Task 5: Update merge manager to use MergeAndReplaceRecordings (depends: 2) [quick]

Wave 3 (Integration + tests - sequential):
├── Task 6: Wire ReconcileOrphanedFiles in main.go (depends: 3) [quick]
└── Task 7: Test suite for all new functionality (depends: 4, 5, 6) [unspecified-high]

Wave FINAL (Verification):
├── F1: Plan compliance audit (oracle)
├── F2: Code quality review (unspecified-high)
├── F3: Build + test verification (unspecified-high)
└── F4: Scope fidelity check (deep)
```

### Dependency Matrix

| Task | Depends On | Blocks |
|------|-----------|--------|
| 1    | -         | 4, 7   |
| 2    | -         | 5, 7   |
| 3    | -         | 6, 7   |
| 4    | 1         | 7      |
| 5    | 2         | 7      |
| 6    | 3         | 7      |
| 7    | 4, 5, 6  | F1-F4  |

### Agent Dispatch Summary

- **Wave 1**: 3 agents — T1 → `quick`, T2 → `deep`, T3 → `deep`
- **Wave 2**: 2 agents — T4 → `quick`, T5 → `quick`
- **Wave 3**: 2 agents — T6 → `quick`, T7 → `unspecified-high`
- **FINAL**: 4 agents — F1 → `oracle`, F2-F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

- [x] 1. Add `InsertRecordingWithRetry` to storage/db.go

  **What to do**:
  - 在 `internal/storage/db.go` 中添加新方法 `InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error`
  - 内部循环：先调用 `InsertRecording(ctx, r)`，若成功则返回
  - 若失败且错误包含 "database is locked" 或 "SQLITE_BUSY"，等待 backoff 后重试
  - 每次重试前用 `slog.Warn("retrying insert recording", "camera_id", r.CameraID, "attempt", i+1, "max_retries", maxRetries, "error", err)` 记录
  - 重试次数用完后用 `slog.Error("insert recording failed after retries", "camera_id", r.CameraID, "file_path", r.FilePath, "error", err, "attempts", maxRetries)` 记录
  - 返回最终错误（包装为 `fmt.Errorf("insert recording failed after %d attempts: %w", maxRetries, lastErr)`）
  - 不修改原有的 `InsertRecording` 方法（保持 WebDAV/FTP/HTTP 调用路径不变）

  **Must NOT do**:
  - 不要修改原有 `InsertRecording` 方法签名
  - 不要给非 SQLITE_BUSY 错误加重试
  - 不要引入新的依赖包

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3)
  - **Blocks**: Task 4, Task 7
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/storage/db.go:311-314` — 原有 InsertRecording 实现，理解参数和返回值
  - `internal/storage/db.go:41` — busy_timeout=5000 设置

  **API/Type References**:
  - `internal/model/types.go` — model.Recording 结构体定义

  **WHY Each Reference Matters**:
  - `db.go:311-314`: 需要理解 InsertRecording 的签名和调用方式来包装重试
  - `db.go:41`: 了解当前 busy_timeout 配置，确保重试间隔与之配合

  **Acceptance Criteria**:
  - [ ] `InsertRecordingWithRetry` 方法存在于 `internal/storage/db.go`
  - [ ] 方法接受 `maxRetries` 和 `backoff` 参数
  - [ ] 仅对 SQLITE_BUSY 错误重试，其他错误直接返回
  - [ ] 每次重试有 Warn 日志，最终失败有 Error 日志

  **QA Scenarios**:
  ```
  Scenario: Verify method signature and basic behavior
    Tool: Bash (go build)
    Steps:
      1. rtk go build ./internal/storage/...
      2. Verify no compilation errors
    Expected Result: Build succeeds
    Evidence: .sisyphus/evidence/task-1-build-success.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 2. Add `MergeAndReplaceRecordings` atomic transaction to storage/db.go

  **What to do**:
  - 在 `internal/storage/db.go` 中添加新方法 `MergeAndReplaceRecordings(ctx context.Context, merged *model.Recording, oldIDs []string) error`
  - 使用 `d.db.BeginTx(ctx, nil)` 开启事务（参照 `DeleteRecordingsBatch` 在 db.go:447-473 的模式）
  - 在事务中：
    1. `INSERT INTO recordings` 合并后的新记录，**直接设置 `merged=true`**
    2. `DELETE FROM recordings WHERE id=?` 所有 oldIDs
  - `defer tx.Rollback()` 确保出错时回滚
  - 显式 `tx.Commit()` 提交
  - 返回 commit 错误（如果有）

  **Must NOT do**:
  - 不要在事务中包含文件操作
  - 不要修改 `SetMerged` 方法（保留，但 merge manager 不再调用）
  - 不要修改 DB schema

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3)
  - **Blocks**: Task 5, Task 7
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/storage/db.go:447-473` — DeleteRecordingsBatch 事务模式，这是此项目唯一的事务使用范例，必须完全遵循
  - `internal/storage/db.go:311-314` — InsertRecording SQL，理解字段列表
  - `internal/storage/db.go:317-321` — UpdateRecording SQL，理解字段顺序
  - `internal/merge/manager.go:336-373` — 当前合并组的 3 次独立 DB 调用（InsertRecording + SetMerged + DeleteRecordingsBatch），需要理解其逻辑来替换

  **API/Type References**:
  - `internal/model/types.go` — model.Recording 结构体和字段定义

  **WHY Each Reference Matters**:
  - `db.go:447-473`: 事务模板，需要完全复制其 BeginTx + defer Rollback + Commit 模式
  - `db.go:311-314`: 需要理解 INSERT 语句的字段列表和参数顺序
  - `manager.go:336-373`: 需要理解当前合并流程来确定新方法需要替换哪些操作

  **Acceptance Criteria**:
  - [ ] `MergeAndReplaceRecordings` 方法存在于 `internal/storage/db.go`
  - [ ] 单个事务包含 INSERT（merged=true）+ DELETE oldIDs
  - [ ] 事务失败时 oldIDs 记录保留在 DB 中
  - [ ] 遵循 BeginTx + defer Rollback + Commit 模式

  **QA Scenarios**:
  ```
  Scenario: Verify build and method existence
    Tool: Bash (go build)
    Steps:
      1. rtk go build ./internal/storage/...
    Expected Result: Build succeeds
    Evidence: .sisyphus/evidence/task-2-build-success.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 3. Add `ReconcileOrphanedFiles` and `GetRecordingsByPathSet` to storage

  **What to do**:
  - 在 `internal/storage/db.go` 中添加 `GetRecordingsByPathSet(ctx context.Context, paths map[string]struct{}) (map[string]bool, error)` 方法
    - 查询 `SELECT file_path FROM recordings WHERE file_path IN (...)` 返回已存在的路径集合
    - 使用动态构建的 IN 子句（与 ListRecordings 动态查询构建器类似）
  - 在 `internal/storage/manager.go` 中添加 `ReconcileOrphanedFiles(ctx context.Context, db *DB, cameraIDs map[string]bool) (int, error)` 方法
    - 流程：
      1. 遍历 `rootDir` 下的子目录，跳过 `hls`、`recordings`、`logs`、`backups` 等非摄像头目录
      2. 对每个匹配 cameraIDs 的子目录，列出其中的 `.mp4` 文件
      3. 跳过不匹配 `{cameraID}_{YYYYMMDD}_{HHMMSS}_{nanoseconds}.mp4` 模式的文件（用正则或字符串分割）
      4. 跳过零字节文件（`info.Size() == 0`）
      5. 批量收集所有文件路径，调用 `GetRecordingsByPathSet` 获取已注册路径
      6. 对未注册的文件，从文件名解析元数据：cameraID（目录名）、startedAt（YYYYMMDD_HHMMSS 解析为时间）、ID（nanoseconds 部分）、FileSize（stat）
      7. 将 format 默认设为 `model.FormatH264`（无法从文件名区分 h264/h265）
      8. 用 `INSERT OR IGNORE` 批量插入（单事务包装所有插入操作）
      9. 返回插入数量和错误
    - 每 100 个文件用 `slog.Info("reconciliation progress", "scanned", n, "reconciled", reconciled)` 记录进度

  **Must NOT do**:
  - 不要读取文件内容（MP4 header 解析是范围蔓延）
  - 不要处理 MJPEG 目录格式（目录内含 JPEG 文件）——当前孤立文件都是 MP4
  - 不要修改 DB schema
  - 不要添加定期回收（仅启动时）

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2)
  - **Blocks**: Task 6, Task 7
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/storage/db.go:447-473` — DeleteRecordingsBatch 事务模式，回收的批量插入也需要同样的事务包装
  - `internal/storage/manager.go` — Manager 结构体和 rootDir 字段，理解如何遍历存储目录
  - `internal/recorder/h264.go:437-441` — 录像 ID 生成模式 `fmt.Sprintf("%d", time.Now().UnixNano())`
  - `internal/storage/manager.go:63` — CreateSegment 中的文件名生成格式 `{cameraID}_{timestamp}_{uuid}.mp4`
  - `internal/storage/db.go:898` — ListRecordings 动态查询构建器，参照 IN 子句构建方式

  **API/Type References**:
  - `internal/model/types.go` — model.Recording 结构体、FormatH264/FormatH265 常量

  **External References**:
  - Go filepath.Glob 或 os.ReadDir 遍历目录
  - Go regexp 或 strings.Split 解析文件名

  **WHY Each Reference Matters**:
  - `db.go:447-473`: 批量操作的事务模板
  - `manager.go:63`: 文件名格式决定了如何解析 cameraID、时间戳、UUID
  - `h264.go:437-441`: 理解 ID 和文件名中 UUID 的关系（不同 time.Now() 调用，但 UUID 可作为唯一 ID）
  - `db.go:898`: 动态 IN 子句构建模式

  **Acceptance Criteria**:
  - [ ] `GetRecordingsByPathSet` 方法存在于 db.go
  - [ ] `ReconcileOrphanedFiles` 方法存在于 manager.go
  - [ ] 跳过不匹配文件名模式的文件
  - [ ] 跳过零字节文件
  - [ ] 跳过不在 cameras 表中的 camera 目录
  - [ ] 使用单事务批量插入
  - [ ] 使用 INSERT OR IGNORE 确保幂等性

  **QA Scenarios**:
  ```
  Scenario: Verify build after new methods
    Tool: Bash (go build)
    Steps:
      1. rtk go build ./internal/storage/...
    Expected Result: Build succeeds
    Evidence: .sisyphus/evidence/task-3-build-success.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 4. Update recorders to use `InsertRecordingWithRetry`

  **What to do**:
  - 在以下文件中将 `r.cfg.DB.InsertRecording(context.Background(), rec)` 替换为 `r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond)`:
    1. `internal/recorder/h264.go` — closeCurrentSegment() 中约 line 454
    2. `internal/recorder/h265.go` — closeCurrentSegment() 中约 line 454
    3. `internal/recorder/mjpeg.go` — closeCurrentSegment() 中约 line 351
    4. `internal/recorder/http_jpeg.go` — closeCurrentSegment() 中约 line 373
    5. `plugins/xiaomi/recorder.go` — closeCurrentSegment() 中约 line 540
  - 确保每个文件都导入了 `time` 包（用于 `time.Millisecond`）
  - 不修改 WebDAV/FTP/HTTP 上传处理器中的 InsertRecording 调用

  **Must NOT do**:
  - 不要修改 `internal/webdav/server.go`、`internal/ftp/server.go`、`internal/upload/handler.go` 中的调用
  - 不要在 InsertRecordingWithRetry 失败时删除文件
  - 不要改变 closeCurrentSegment() 的其他逻辑

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Task 5)
  - **Blocks**: Task 7
  - **Blocked By**: Task 1

  **References**:
  **Pattern References**:
  - `internal/recorder/h264.go:454` — 当前 InsertRecording 调用点，需要替换
  - `internal/recorder/h265.go:454` — 同上
  - `internal/recorder/mjpeg.go:351` — 同上
  - `internal/recorder/http_jpeg.go:373` — 同上
  - `plugins/xiaomi/recorder.go:540` — 同上

  **WHY Each Reference Matters**:
  - 每个调用点都需要精确替换为新方法，保持其他逻辑不变

  **Acceptance Criteria**:
  - [ ] 5 个文件中 InsertRecording 调用已替换为 InsertRecordingWithRetry
  - [ ] 参数：maxRetries=3, backoff=500ms
  - [ ] `time` 包已导入
  - [ ] WebDAV/FTP/HTTP 上传处理器未修改
  - [ ] `rtk go build ./...` 编译成功

  **QA Scenarios**:
  ```
  Scenario: Verify all recorders updated and build succeeds
    Tool: Bash (go build)
    Steps:
      1. rtk go build ./...
    Expected Result: Full project builds without errors
    Evidence: .sisyphus/evidence/task-4-full-build.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 5. Update merge manager to use `MergeAndReplaceRecordings`

  **What to do**:
  - 在 `internal/merge/manager.go` 的 `mergeFormatGroup()` 方法中：
    1. 替换 lines 336-373 的 3 次独立 DB 操作（InsertRecording + SetMerged + DeleteRecordingsBatch）为单次 `m.db.MergeAndReplaceRecordings(ctx, mergedRec, oldIDs)` 调用
    2. 在 mergedRec 构建时直接设置 `mergedRec.Merged = true`
    3. 将文件删除操作（删除旧段文件）移到 `MergeAndReplaceRecordings` 成功返回后
    4. 如果事务失败，不删除旧文件（确保数据安全），只删除合并后的临时文件
    5. 保留 `slog.Info("merged segments", ...)` 日志记录
  - 删除不再需要的 `SetMerged` 调用

  **Must NOT do**:
  - 不要在事务中包含文件 I/O 操作
  - 不要修改 `mergeFormatGroup` 的签名
  - 不要修改 MP4/MJPEG 合并逻辑本身

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Task 4)
  - **Blocks**: Task 7
  - **Blocked By**: Task 2

  **References**:
  **Pattern References**:
  - `internal/merge/manager.go:336-373` — 当前合并组的 DB 操作代码，需要替换为单次调用
  - `internal/storage/db.go` — 新的 MergeAndReplaceRecordings 方法（Task 2 创建）

  **WHY Each Reference Matters**:
  - `manager.go:336-373`: 需要精确理解当前 3 步操作的逻辑，确保替换后行为一致

  **Acceptance Criteria**:
  - [ ] mergeFormatGroup 中只调用 1 次 DB 方法（MergeAndReplaceRecordings）
  - [ ] 文件删除在 DB 事务成功后执行
  - [ ] 事务失败时不删除旧文件
  - [ ] merged=true 直接在 INSERT 中设置
  - [ ] `rtk go build ./...` 编译成功

  **QA Scenarios**:
  ```
  Scenario: Verify merge manager builds correctly
    Tool: Bash (go build)
    Steps:
      1. rtk go build ./internal/merge/...
    Expected Result: Build succeeds
    Evidence: .sisyphus/evidence/task-5-merge-build.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 6. Wire `ReconcileOrphanedFiles` in main.go

  **What to do**:
  - 在 `cmd/mibee-nvr/main.go` 中：
    1. 找到 `db.CleanupIncomplete()` 调用位置（约 line 317）
    2. 在其后添加孤立文件回收逻辑：
       ```go
       // Reconcile orphaned recording files (exists on disk but not in DB)
       cameraIDs := make(map[string]bool)
       for _, cam := range cfg.Cameras {
           cameraIDs[cam.ID] = true
       }
       reconciled, err := store.ReconcileOrphanedFiles(ctx, db, cameraIDs)
       if err != nil {
           logger.Error("failed to reconcile orphaned files", "error", err)
       } else if reconciled > 0 {
           logger.Info("reconciled orphaned recording files", "count", reconciled)
       }
       ```
    3. 确保此代码在 `camMgr.Start(ctx)` 之前执行（同步阻塞）

  **Must NOT do**:
  - 不要在 camMgr.Start() 之后执行回收（会有并发写入竞争）
  - 不要将回收改为异步（必须在启动时同步完成）

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (sequential, after Task 3)
  - **Blocks**: Task 7
  - **Blocked By**: Task 3

  **References**:
  **Pattern References**:
  - `cmd/mibee-nvr/main.go:314-420` — 启动序列，理解 CleanupTempFiles → CleanupIncomplete → camMgr.Start 的顺序

  **WHY Each Reference Matters**:
  - 需要精确找到插入点：在 CleanupIncomplete 之后、camMgr.Start 之前

  **Acceptance Criteria**:
  - [ ] ReconcileOrphanedFiles 在 main.go 中被调用
  - [ ] 调用位于 db.CleanupIncomplete() 之后、camMgr.Start() 之前
  - [ ] 传入所有 camera IDs
  - [ ] `rtk go build ./...` 编译成功

  **QA Scenarios**:
  ```
  Scenario: Verify main.go compiles with new code
    Tool: Bash (go build)
    Steps:
      1. rtk go build ./cmd/mibee-nvr/...
    Expected Result: Build succeeds
    Evidence: .sisyphus/evidence/task-6-main-build.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 7. Test suite for all new functionality

  **What to do**:
  - **在 `internal/storage/db_test.go` 中添加以下测试**：
    - `TestInsertRecordingWithRetry_Success` — 验证正常插入不重试
    - `TestInsertRecordingWithRetry_RetriesOnBusy` — 模拟 SQLITE_BUSY 后成功（可用 custom wrapper 验证调用次数）
    - `TestInsertRecordingWithRetry_ExhaustsRetries` — 验证 3 次重试后返回错误
    - `TestMergeAndReplaceRecordings` — 插入 5 个源记录，调用方法，验证旧记录删除、新记录存在且 merged=true
    - `TestMergeAndReplaceRecordings_Rollback` — 模拟失败，验证源记录保留
    - `TestGetRecordingsByPathSet` — 插入记录，验证批量路径查询返回正确结果

  - **在 `internal/storage/manager_test.go` 中添加以下测试**：
    - `TestReconcileOrphanedFiles_Basic` — 创建匹配文件名模式的 MP4 文件，运行回收，验证 DB 中存在记录
    - `TestReconcileOrphanedFiles_SkipsUnknownCamera` — 不插入 camera，验证文件被跳过
    - `TestReconcileOrphanedFiles_SkipsNonMatching` — 放置不匹配文件名的文件，验证被跳过
    - `TestReconcileOrphanedFiles_SkipsZeroByte` — 放置零字节文件，验证被跳过
    - `TestReconcileOrphanedFiles_Idempotent` — 运行两次回收，验证结果一致

  - **在 `internal/merge/manager_test.go` 中添加以下测试**（可选，如果现有测试已覆盖合并流程）：
    - 验证合并后旧记录从 DB 中删除
    - 验证合并后的新记录 merged=true

  - 所有测试遵循项目约定：`require` 断言、`t.Helper()` 在辅助函数中、temp file SQLite

  **Must NOT do**:
  - 不要使用 mock（项目约定使用真实 SQLite）
  - 不要使用 `assert` 断言（仅用 `require`）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (sequential, after Tasks 4, 5, 6)
  - **Blocks**: F1-F4
  - **Blocked By**: Task 4, Task 5, Task 6

  **References**:
  **Pattern References**:
  - `internal/storage/db_test.go:41-71` — TestInsertAndGetRecording，测试 Insert+Get 的基本模式
  - `internal/storage/db_test.go:447-473` — 测试中使用的 DeleteRecordingsBatch 模式
  - `internal/merge/manager_test.go:23-38` — newMergeTestEnv 辅助函数，集成测试环境设置

  **Test References**:
  - `internal/storage/db_test.go` — 全部 DB 测试，理解 t.TempDir + New + Init + defer Close 模式
  - `internal/merge/manager_test.go` — 合并测试模式，理解如何创建真实 MP4 段用于测试

  **WHY Each Reference Matters**:
  - `db_test.go:41-71`: 基本 Insert+Get 测试模板，新测试需遵循同样模式
  - `manager_test.go:23-38`: 集成测试环境设置模式，回收测试需要类似的环境

  **Acceptance Criteria**:
  - [ ] 所有新测试文件编译通过
  - [ ] `rtk go test ./internal/storage/... -v -count=1` → ALL PASS
  - [ ] `rtk go test ./internal/merge/... -v -count=1` → ALL PASS
  - [ ] 测试覆盖：重试成功、重试耗尽、事务成功、事务回滚、路径查询、回收基本、回收跳过、回收幂等
  - [ ] 所有辅助函数使用 `t.Helper()`
  - [ ] 仅使用 `require` 断言

  **QA Scenarios**:
  ```
  Scenario: Run all tests and verify pass
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/storage/... -v -count=1 2>&1 | tee /tmp/storage-test-output.txt
      2. rtk go test ./internal/merge/... -v -count=1 2>&1 | tee /tmp/merge-test-output.txt
      3. Check exit codes are 0
      4. grep -c "PASS" /tmp/storage-test-output.txt /tmp/merge-test-output.txt
    Expected Result: All tests pass, no FAIL output
    Evidence: .sisyphus/evidence/task-7-test-results.txt

  Scenario: Verify build and vet
    Tool: Bash (go build + go vet)
    Steps:
      1. rtk go vet ./... 2>&1 | tee /tmp/vet-output.txt
      2. rtk make build 2>&1 | tee /tmp/build-output.txt
    Expected Result: No vet issues, successful build
    Evidence: .sisyphus/evidence/task-7-vet-build.txt
  ```

  **Commit**: YES (single commit for all changes)
  - Message: `fix(storage): add InsertRecording retry, merge atomic tx, and orphan reconciliation`
  - Files: All modified files
  - Pre-commit: `rtk go test ./internal/storage/... ./internal/merge/... -v && rtk go vet ./...`


## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `rtk go vet ./...` + `rtk make build`. Review all changed files for: `as any`/ignored errors, empty catches, `fmt.Println` in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Vet [PASS/FAIL] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Build + Test Verification** — `unspecified-high`
  Run `rtk go test ./internal/storage/... -v -count=1` and `rtk go test ./internal/merge/... -v -count=1`. Verify all tests pass. Check for test output containing "FAIL" or "panic". Run `rtk make build` to confirm static binary builds.
  Output: `Storage Tests [N/N pass] | Merge Tests [N/N pass] | Build [PASS/FAIL] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff. Verify 1:1 — everything in spec was built, nothing beyond spec was built. Check "Must NOT do" compliance. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **Single commit after all tasks**: `fix(storage): add InsertRecording retry, merge atomic tx, and orphan reconciliation`
  Files: `internal/storage/db.go`, `internal/storage/manager.go`, `internal/recorder/*.go`, `plugins/xiaomi/recorder.go`, `internal/merge/manager.go`, `cmd/mibee-nvr/main.go`
  Pre-commit: `rtk go test ./internal/storage/... ./internal/merge/... -v && rtk go vet ./...`

---

## Success Criteria

### Verification Commands
```bash
rtk go test ./internal/storage/... -v -count=1   # Expected: ALL PASS, new tests visible
rtk go test ./internal/merge/... -v -count=1      # Expected: ALL PASS, merge tests pass
rtk go vet ./...                                   # Expected: no issues
rtk make build                                     # Expected: successful build
```

### Final Checklist
- [x] All "Must Have" present
- [x] All "Must NOT Have" absent
- [x] All tests pass
- [x] No schema changes
- [x] WebDAV/FTP/HTTP upload handlers unchanged (no retry added)
- [x] ReconcileOrphanedFiles runs before camMgr.Start in main.go
