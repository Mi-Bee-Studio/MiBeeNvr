# NVR 多功能升级计划

## TL;DR

> **Quick Summary**: 为 MiBee NVR 添加 6 项功能升级：清理提交、录像统计图可筛选重设计、per-camera 保存周期、HLS 实时播放、录像页默认简化、摄像头时间状态。最后部署到 RPi 并浏览器验收。
> 
> **Deliverables**:
> - main 分支清理提交 + feature 分支
> - 可筛选/可折叠的 Chart.js 统计图
> - per-camera retention_days (DB + API + UI)
> - RTSP→HLS 实时播放 (gohlslib + 前端播放页)
> - 录像页去掉快速选择，默认1小时
> - 摄像头状态改为时间+阈值展示
> - 部署到 192.168.63.31 + 浏览器验收通过
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 5 waves
> **Critical Path**: T1(cleanup) → T2(branch) → T3-T7(backend) → T8-T12(frontend+HLS) → T13(build+deploy) → F1-F4(verify)

---

## Context

### Original Request
用户要求先清理多余文件提交代码后，fork 新分支开发 6 项新功能：录像统计图重新设计（可折叠/可筛选）、per-camera 保存视频周期、摄像头实时播放（HLS）、录像页去掉快速选择默认1小时、摄像头状态改为基于最近采集时间判断。测试部署环境 ssh mickey@192.168.63.31，用浏览器工具验收。

### Interview Summary
**Key Discussions**:
- 统计图重设计：用户选择可折叠/可筛选方案，保留图表形式但增加摄像头筛选功能
- 实时播放方案：用户选择 HLS（非 WebRTC），使用 gohlslib 纯 Go 实现
- 清理范围：所有多余文件（旧构建产物等）
- 摄像头状态：显示时间+阈值方案，5分钟无录像=警告，30分钟=异常
- Per-camera retention：在摄像头编辑表单中设置，0=使用全局默认
- 实时播放 UI：摄像头列表增加按钮 + 独立全屏播放页面

**Research Findings**:
- gohlslib (bluenviron/gohlslib/v2) 是最佳选择，与 gortsplib 同一作者，纯 Go
- RPi 3B 最多支持 1-2 路并发 HLS 流
- gohlslib 只支持 H.264/H.265，MJPEG/HTTP JPEG 摄像头无法 HLS
- DB 已有 migration pattern (schema_meta version tracking)，当前 v2
- retention_days 应作为 DB-only metadata 字段，不进 CameraConfig YAML
- 前端使用 Svelte 5 $state runes, Chart.js 4.5.1, hash-based routing

### Metis Review
**Identified Gaps** (addressed):
- MJPEG 不支持 HLS → 这些摄像头不显示实时按钮
- HLS 资源管理 → on-demand lifecycle + idle timeout + 磁盘存储 segments
- 摄像头状态数据源 → 混合方案：recorder lastFrameTime + DB fallback
- 磁盘阈值清理 → 保持全局策略不区分 per-camera
- Migration pattern → 遵循 v1→v2 模式，retention_days 走 UpdateCameraMetadata()

---

## Work Objectives

### Core Objective
为 NVR 系统添加 per-camera 配置、HLS 实时播放、统计图优化、状态改进等功能，使其更适合多摄像头生产环境使用。

### Concrete Deliverables
- `main` 分支清理提交 + `feat/multi-upgrade` 功能分支
- `internal/storage/db.go` migration v2→v3 + retention_days 查询
- `internal/cleanup/cleanup.go` per-camera retention 逻辑
- `internal/hls/` 新模块 (gohlslib 集成 + on-demand lifecycle)
- `internal/api/handler.go` 新 endpoints (/stream, /cameras status enrichment)
- `internal/recorder/h264.go` + `h265.go` 分支 RTP→HLS
- `web/src/routes/Stats.svelte` 可筛选统计图
- `web/src/routes/Cameras.svelte` 时间状态 + 实时按钮
- `web/src/routes/LiveView.svelte` 新页面 (HLS 播放)
- `web/src/routes/Recordings.svelte` 简化时间选择
- 更新 `en.json` + `zh.json` i18n keys
- 部署到 192.168.63.31 + 浏览器验收

### Definition of Done
- [ ] `git log main -1` 显示清理提交
- [ ] `git branch --show-current` 显示功能分支
- [ ] `rtk go vet ./...` 零错误
- [ ] 前端 `npm run build` 成功
- [ ] 跨平台编译 `rtk make cross` 成功
- [ ] 部署到 192.168.63.31 可访问
- [ ] 浏览器验收所有功能通过

### Must Have
- Per-camera retention_days 完整流程 (DB→API→UI→Cleanup)
- HLS 实时播放 (仅 H.264/H.265, on-demand)
- 统计图可筛选/可折叠
- 摄像头时间状态 (5min/30min 阈值)
- 录像页默认1小时
- 浏览器验收通过

### Must NOT Have (Guardrails)
- 不为 MJPEG/HTTP JPEG 摄像头提供 HLS 实时播放
- 不创建第二个 RTSP 连接给 HLS — 复用 recorder 的连接
- 不修改现有 MP4 录制管线
- 不让 HLS muxer 常驻所有摄像头 — 必须按需启动
- 不将 retention_days 加入 CameraConfig YAML — 它是 DB-only 字段
- 不将 retention_days:0 视为"永久保留" — 0 = 使用全局默认
- 不修改 FTP/WebDAV/MQTT 模块
- 不区分磁盘阈值清理按 per-camera retention
- 不添加 console.log 调试代码到生产代码
- 不过度注释或添加不必要的 JSDoc

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (tests/integration_test.go, Go testing)
- **Automated tests**: Tests-after (为 DB migration 和 cleanup 逻辑添加测试)
- **Framework**: Go testing + testify/require
- **Primary verification**: Agent-executed QA scenarios + 浏览器自动化

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Frontend/UI**: Playwright browser automation - Navigate, interact, assert DOM, screenshot
- **Backend API**: Bash (curl) - Send requests, assert status + response fields
- **Build**: Bash - Compile, lint, test
- **Deployment**: Bash (ssh) - Cross-compile, scp, restart service

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 0 (Prep - cleanup + branch):
├── Task 1: 清理多余文件 + 提交到 main [quick]
└── Task 2: 创建功能分支 [quick]

Wave 1 (Backend foundation - MAX PARALLEL):
├── Task 3: DB migration v2→v3 (retention_days) [quick]
├── Task 4: Per-camera retention cleanup 逻辑 [deep]
├── Task 5: 摄像头状态 API enrichment (last_seen) [quick]
├── Task 6: gohlslib 集成 + HLS muxer 模块 [deep]
├── Task 7: API HLS streaming endpoint [unspecified-high]
└── Task 8: Recorder RTP 分支到 HLS muxer [deep]

Wave 2 (Frontend - MAX PARALLEL after backend):
├── Task 9: Stats.svelte 可筛选统计图 [visual-engineering]
├── Task 10: Cameras.svelte 时间状态 + 实时按钮 [visual-engineering]
├── Task 11: LiveView.svelte 实时播放页面 [visual-engineering]
├── Task 12: Recordings.svelte 简化时间选择 [quick]
├── Task 13: i18n 更新 (en.json + zh.json) [quick]
└── Task 14: 前端 build + Go embed [quick]

Wave 3 (Integration + Deploy):
└── Task 15: 跨平台编译 + 部署到 RPi + 浏览器验收 [deep]

Wave FINAL (Verification - 4 parallel):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality review (unspecified-high)
├── Task F3: Real manual QA with browser (unspecified-high)
└── Task F4: Scope fidelity check (deep)
```

### Dependency Matrix

| Task | Blocked By | Blocks | Wave |
|------|-----------|--------|------|
| 1 | - | 2 | 0 |
| 2 | 1 | 3-14 | 0 |
| 3 | 2 | 4, 10 | 1 |
| 4 | 3 | 15 | 1 |
| 5 | 2 | 10, 15 | 1 |
| 6 | 2 | 7, 8 | 1 |
| 7 | 6 | 11, 15 | 1 |
| 8 | 6 | 11, 15 | 1 |
| 9 | 2 | 14, 15 | 2 |
| 10 | 3, 5 | 14, 15 | 2 |
| 11 | 7, 8 | 14, 15 | 2 |
| 12 | 2 | 14, 15 | 2 |
| 13 | 9, 10, 11, 12 | 14 | 2 |
| 14 | 13 | 15 | 2 |
| 15 | 4, 5, 7, 8, 14 | F1-F4 | 3 |

### Agent Dispatch Summary

- **Wave 0**: **2** - T1 → `quick`, T2 → `quick`
- **Wave 1**: **6** - T3 → `quick`, T4 → `deep`, T5 → `quick`, T6 → `deep`, T7 → `unspecified-high`, T8 → `deep`
- **Wave 2**: **6** - T9 → `visual-engineering`, T10 → `visual-engineering`, T11 → `visual-engineering`, T12 → `quick`, T13 → `quick`, T14 → `quick`
- **Wave 3**: **1** - T15 → `deep`
- **FINAL**: **4** - F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

---

- [x] 1. 清理多余文件 + 提交到 main

  **What to do**:
  - 检查 `internal/ui/static/assets/` 目录，删除旧构建产物 `index-BcyvPyiv.js`（当前版本是 `index-BwAVNV2-.js`）
  - 确认 `internal/ui/static/assets/` 只包含 `index.html` 中引用的文件
  - 确认 untracked 文件 `internal/recorder/h265.go` 是有意义的新代码（H.265 recorder），应被提交
  - 确认 untracked 文件 `internal/ui/static/assets/index-BwAVNV2-.js` 是当前版本的构建产物
  - 将所有 12 个 modified files + 2 个 untracked files 提交到 main 分支
  - 提交信息: `feat: H.265 support, WebDAV read-write, UI fixes`

  **Must NOT do**:
  - 不要修改任何代码内容，只做 git add + commit
  - 不要删除当前版本的构建产物

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 简单的文件清理和 git 操作
  - **Skills**: [`git-master`]
    - `git-master`: git 操作专用技能

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 0
  - **Blocks**: Task 2
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/ui/static/index.html` - 确认当前引用的 JS/CSS 文件名
  - `internal/ui/static/assets/` - 查看所有文件，识别旧版本

  **Acceptance Criteria**:
  - [ ] `ls internal/ui/static/assets/` 只包含 index.html 中引用的文件
  - [ ] `git status` 显示 clean 状态
  - [ ] `git log -1 --oneline` 显示提交信息

  **QA Scenarios:**
  ```
  Scenario: 旧构建产物已清理
    Tool: Bash
    Steps:
      1. ls internal/ui/static/assets/ 列出文件
      2. grep -o 'index-[^.]*\.js' internal/ui/static/index.html 提取引用的 JS
      3. 比对：assets 目录中的 JS 文件应只包含 index.html 引用的那个
    Expected Result: assets 目录中只有一个 JS 文件且与 index.html 引用一致
    Evidence: .sisyphus/evidence/task-1-cleanup.txt

  Scenario: git 工作区干净
    Tool: Bash
    Steps:
      1. git status --porcelain
    Expected Result: 无输出 (working tree clean)
    Evidence: .sisyphus/evidence/task-1-git-clean.txt
  ```

  **Commit**: YES
  - Message: `feat: H.265 support, WebDAV read-write, UI fixes`
  - Files: all modified + untracked files
  - Pre-commit: `rtk go vet ./...`

- [x] 2. 创建功能分支

  **What to do**:
  - 从 main 分支创建新功能分支 `feat/multi-upgrade`
  - 切换到新分支
  - 确认分支基于最新的 main

  **Must NOT do**:
  - 不要在 main 上做功能开发
  - 不要推送到 remote

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 简单的 git 分支操作
  - **Skills**: [`git-master`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 0 (after Task 1)
  - **Blocks**: Tasks 3-14
  - **Blocked By**: Task 1

  **References**:
  **Pattern References**:
  - 无特殊引用，标准 git 操作

  **Acceptance Criteria**:
  - [ ] `git branch --show-current` 显示 `feat/multi-upgrade`
  - [ ] `git log main..HEAD` 显示无提交 (基于 main 创建)

  **QA Scenarios:**
  ```
  Scenario: 功能分支已创建
    Tool: Bash
    Steps:
      1. git branch --show-current
      2. git merge-base main HEAD 检查基准
    Expected Result: 当前分支为 feat/multi-upgrade，基于 main 最新提交
    Evidence: .sisyphus/evidence/task-2-branch.txt
  ```

  **Commit**: NO

## Final Verification Wave
- [x] 3. DB migration v2→v3 (per-camera retention_days)

  **What to do**:
  - 在 `internal/storage/db.go` 中新增 migration v2→v3:
    - ALTER TABLE cameras ADD COLUMN retention_days INTEGER DEFAULT 0
    - 更新 schema_meta version 为 "3"
  - 遵循现有 migration v1→v2 的模式 (check version → ALTER TABLE → update version, errors ignored for idempotency)
  - 修改 `scanCamera()` 函数增加 retention_days 字段读取
  - 修改 `UpdateCameraMetadata()` SQL 增加 retention_days
  - 修改 `GetCamera()` 和 `ListCameras()` 返回的 CameraRow 包含 retention_days
  - 添加测试: migration 升级正确, 默认值为 0, metadata 更新包含 retention_days

  **Must NOT do**:
  - 不要将 retention_days 加入 UpsertCamera() — 它是 DB-only metadata 字段
  - 不要将 retention_days:0 视为“永久保留” — 0 = 使用全局默认
  - 不要修改现有的 v1→v2 migration 逻辑

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 遵循现有模式，变更范围明确
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1
  - **Blocks**: Task 4, Task 10
  - **Blocked By**: Task 2

  **References**:
  **Pattern References**:
  - `internal/storage/db.go:100-114` - 现有 migration v1→v2 模式 (schema_meta check + ALTER TABLE + update version)
  - `internal/storage/db.go:UpdateCameraMetadata()` - metadata 更新 SQL 模式
  - `internal/storage/db.go:scanCamera()` - CameraRow Scan() 模式，需要增加 retention_days

  **API/Type References**:
  - `internal/model/types.go:CameraRow` - camera 数据结构，需要增加 RetentionDays int 字段

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/storage/... -v` 全部通过
  - [ ] migration v3 正确添加 retention_days 列
  - [ ] scanCamera 包含 retention_days
  - [ ] UpdateCameraMetadata 包含 retention_days

  **QA Scenarios:**
  ```
  Scenario: Migration v2→v3 正确执行
    Tool: Bash
    Steps:
      1. rtk go test ./internal/storage/... -v -run TestMigration
      2. 确认测试中验证 schema_meta version = "3"
      3. 确认测试中验证 cameras 表有 retention_days 列
    Expected Result: 测试通过，migration 幂等 (重复执行不报错)
    Evidence: .sisyphus/evidence/task-3-migration.txt

  Scenario: UpdateCameraMetadata 包含 retention_days
    Tool: Bash
      1. rtk go test ./internal/storage/... -v -run TestCameraMetadata
      2. 确认 retention_days 可被正确更新和读取
    Expected Result: 更新 retention_days=7 后读取值为 7, 更新 retention_days=0 后读取值为 0
    Evidence: .sisyphus/evidence/task-3-metadata.txt
  ```

  **Commit**: YES
  - Message: `feat(storage): add per-camera retention_days migration v2→v3`
  - Files: `internal/storage/db.go`, `internal/model/types.go`
  - Pre-commit: `rtk go test ./internal/storage/... -v`

- [x] 4. Per-camera retention cleanup 逻辑

  **What to do**:
  - 修改 `internal/cleanup/cleanup.go` 的 `timeBasedCleanup()` 方法:
    - 获取所有摄像头的 retention_days 设置
    - 对每个摄像头使用其自身的 retention_days (0 时 fallback 到全局 CleanupConfig.RetentionDays)
    - 按 camera_id 分组删除过期录像
  - 在 `internal/storage/db.go` 添加新方法:
    - `ListExpiredRecordingsByCamera(cameraID string, retentionDays int)` — 按摄像头查询过期录像
    - 或修改 `ListExpiredRecordings` 支持按 camera_id + retention_days 参数化
  - 添加测试: per-camera retention 正确生效, 0 fallback 到全局, 不同摄像头不同周期

  **Must NOT do**:
  - 不要修改磁盘阈值清理逻辑 — 保持全局策略
  - 不要删除现有 ListExpiredRecordings — 可能被其他代码引用

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 需要理解现有 cleanup 逻辑并修改核心算法
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (after Task 3)
  - **Blocks**: Task 15
  - **Blocked By**: Task 3

  **References**:
  **Pattern References**:
  - `internal/cleanup/cleanup.go:timeBasedCleanup()` - 现有全局 retention 清理逻辑
  - `internal/storage/db.go:ListExpiredRecordings()` - 过期录像查询 SQL

  **API/Type References**:
  - `internal/model/types.go:CameraRow` - 包含 RetentionDays 字段
  - `internal/config/config.go:CleanupConfig` - 全局 retention_days 默认值

  **Acceptance Criteria**:
  - [ ] `rtk go vet ./...` 零错误
  - [ ] `rtk go test ./internal/cleanup/... -v` 全部通过 (如有测试)
  - [ ] cleanup 逻辑按摄像头分别计算过期时间
  - [ ] retention_days=0 的摄像头 fallback 到全局设置

  **QA Scenarios:**
  ```
  Scenario: Per-camera retention 正确工作
    Tool: Bash
    Steps:
      1. rtk go test ./internal/cleanup/... -v
      2. 验证: camera A retention=7天, camera B retention=30天, 全局=14天
      3. 验证: camera A 的 10天前录像被删除, camera B 的 10天前录像保留
      4. 验证: camera C (retention=0) 使用全局 14天
    Expected Result: 每个摄像头按各自周期清理，0 用全局值
    Evidence: .sisyphus/evidence/task-4-cleanup.txt
  ```

  **Commit**: YES
  - Message: `feat(cleanup): per-camera retention in cleanup logic`
  - Files: `internal/cleanup/cleanup.go`, `internal/storage/db.go`
  - Pre-commit: `rtk go vet ./...`

- [ ] 5. 摄像头状态 API enrichment (last_seen)

  **What to do**:
  - 在 `internal/storage/db.go` 添加方法 `GetLastRecordingTime(cameraID string) (time.Time, error)`:
    - `SELECT MAX(ended_at) FROM recordings WHERE camera_id=?`
  - 在 `internal/camera/manager.go` 的 camera 列表/status API 响应中添加 `last_seen` 字段:
    - 方案: 在 API handler 层查询每个摄像头的最后录像时间
    - 混合方案: recorder 运行中暴露 lastFrameTime, 否则 fallback DB 查询
  - 修改 `internal/model/types.go` 的 CameraRow 增加 `LastSeen *time.Time` 字段
  - 修改 API `/api/cameras` 响应包含 `last_seen` 字段

  **Must NOT do**:
  - 不要修改 recorder 接口 — 只在 API 层做 enrichment
  - 不要在 CameraManager 中缓存状态 — 每次查询实时获取

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 添加 DB 查询 + API 字段，范围明确
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1
  - **Blocks**: Task 10, Task 15
  - **Blocked By**: Task 2

  **References**:
  **Pattern References**:
  - `internal/api/handler.go:listCameras()` - 现有摄像头列表 API，需要在此添加 last_seen
  - `internal/camera/manager.go:Status()` - 现有状态查询模式

  **API/Type References**:
  - `internal/model/types.go:CameraRow` - 需要增加 LastSeen 字段
  - `internal/storage/db.go` - 需要添加 GetLastRecordingTime 方法

  **Acceptance Criteria**:
  - [ ] `/api/cameras` 响应中每个摄像头包含 `last_seen` 字段 (ISO timestamp 或 null)
  - [ ] 从未有录像的摄像头 `last_seen` 为 null
  - [ ] `rtk go vet ./...` 零错误

  **QA Scenarios:**
  ```
  Scenario: API 返回 last_seen 字段
    Tool: Bash (curl)
    Steps:
      1. curl -s -u admin:admin http://192.168.63.31:9090/api/cameras
      2. 解析 JSON, 检查每个摄像头是否有 last_seen 字段
    Expected Result: 每个摄像头对象包含 last_seen (有值或 null)
    Evidence: .sisyphus/evidence/task-5-last-seen.txt
  ```

  **Commit**: YES
  - Message: `feat(api): camera last_seen status enrichment`
  - Files: `internal/api/handler.go`, `internal/storage/db.go`, `internal/model/types.go`
  - [x] 5. 摄像头状态 API enrichment (last_seen)
- [x] 6. gohlslib 集成 + HLS muxer 模块

  **What to do**:
  - 添加依赖: `go get github.com/bluenviron/gohlslib/v2@latest`
  - 创建新包 `internal/hls/` 目录:
    - `manager.go` — HLS Manager 结构体:
      - 管理 per-camera 的 on-demand HLS muxer 实例
      - `StartStream(cameraID string, rtspURL string) (string, error)` — 启动 HLS muxer, 返回 m3u8 URL
      - `StopStream(cameraID string)` — 停止指定摄像头的 HLS muxer
      - `GetHandler(cameraID string) http.Handler` — 获取 gohlslib Muxer.Handle 的 HTTP handler
      - `IsActive(cameraID string) bool` — 检查 muxer 是否活跃
      - 内部 idle timeout (如 60s 无请求自动停止 muxer)
      - 线程安全 (sync.RWMutex)
    - `muxer.go` — 单个 camera 的 HLS muxer 封装:
      - gohlslib.Muxer 配置: disk-based segments, segment duration 2s, segment count 3
      - 临时目录: `{data_dir}/hls/{camera_id}/`
      - 启动/停止/写入帧的接口
  - 在 `cmd/mibee-nvr/main.go` 中初始化 HLS Manager 并传递给 API handler

  **Must NOT do**:
  - 不要让 muxer 常驻 — 必须 on-demand
  - 不要在内存中保存 segments — 用磁盘
  - 不要支持超过 2 路并发流 (RPi 3B 限制)
  - 不要为 MJPEG/HTTP JPEG 摄像头创建 HLS muxer

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 新模块设计，需要理解 gohlslib API 和资源管理
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1
  - **Blocks**: Task 7, Task 8
  - **Blocked By**: Task 2

  **References**:
  **Pattern References**:
  - `internal/camera/manager.go` — CameraManager 的 RWMutex + map 管理模式可参考
  - `internal/recorder/h264.go:258-270` — RTP decode 后的帧数据获取点

  **External References**:
  - gohlslib: `github.com/bluenviron/gohlslib/v2` — HLS muxer 库
  - gohlslib.Muxer 结构体 — SegmentDuration, SegmentCount, Directory 配置
  - gohlslib.Muxer.Handle — 内置 HTTP handler 服务 .m3u8 + .ts

  **Acceptance Criteria**:
  - [ ] `internal/hls/manager.go` 和 `internal/hls/muxer.go` 存在
  - [ ] `rtk go vet ./internal/hls/...` 零错误
  - [ ] `go get github.com/bluenviron/gohlslib/v2` 成功添加到 go.mod

  **QA Scenarios:**
  ```
  Scenario: HLS Manager 创建和销毁
    Tool: Bash
    Steps:
      1. rtk go vet ./internal/hls/...
      2. 确认 Manager 有 StartStream/StopStream/GetHandler/IsActive 方法
    Expected Result: 编译通过，模块结构正确
    Evidence: .sisyphus/evidence/task-6-hls-module.txt
  ```

  **Commit**: YES
  - Message: `feat(hls): add gohlslib integration with on-demand lifecycle`
  - Files: `internal/hls/manager.go`, `internal/hls/muxer.go`, `go.mod`, `go.sum`
  - Pre-commit: `rtk go vet ./...`

- [x] 7. API HLS streaming endpoint

  **What to do**:
  - 在 `internal/api/handler.go` 添加新的路由和 handler:
    - `GET /api/cameras/{id}/stream/` — HLS playlist + segments 代理
      - 认证 (复用现有 auth middleware)
      - 检查摄像头协议 (仅 rtsp_h264/rtsp_h265 支持)
      - 调用 HLS Manager StartStream (on-demand)
      - 使用 gohlslib.Muxer.Handle 作为 HTTP handler
      - 管理响应: Content-Type for .m3u8 和 .ts
    - `DELETE /api/cameras/{id}/stream/` — 主动停止 HLS 流
  - 在 `cmd/mibee-nvr/main.go` 的路由注册中添加新路由
  - 仅 H.264/H.265 协议摄像头可访问，其他返回 400 Bad Request

  **Must NOT do**:
  - 不要为 MJPEG/HTTP JPEG 摄像头创建流 endpoint — 返回 400
  - 不要跳过认证 — 必须经过 auth middleware

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: API endpoint 涉及路由、认证、错误处理等多个关注点
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (after Task 6)
  - **Blocks**: Task 11, Task 15
  - **Blocked By**: Task 6

  **References**:
  **Pattern References**:
  - `internal/api/handler.go:Routes()` — 现有路由注册模式
  - `internal/api/handler.go:downloadRecording()` — 文件服务 + Content-Type 设置模式

  **API/Type References**:
  - `internal/hls/manager.go` — HLS Manager 接口 (Task 6 产出)
  - `internal/model/types.go` — ProtocolRTSPH264, ProtocolRTSPH265 常量

  **Acceptance Criteria**:
  - [ ] `GET /api/cameras/{h264_id}/stream/` 返回 m3u8 playlist
  - [ ] `GET /api/cameras/{mjpeg_id}/stream/` 返回 400 Bad Request
  - [ ] 未认证访问返回 401
  - [ ] `rtk go vet ./...` 零错误

  **QA Scenarios:**
  ```
  Scenario: H.264 摄像头 HLS 端点可用
    Tool: Bash (curl)
    Steps:
      1. curl -s -u admin:admin http://192.168.63.31:9090/api/cameras 获取 H.264 摄像头 ID
      2. curl -s -u admin:admin http://192.168.63.31:9090/api/cameras/{id}/stream/
      3. 检查 Content-Type 包含 application/vnd.apple.mpegurl
    Expected Result: 返回 .m3u8 内容, Content-Type 正确
    Evidence: .sisyphus/evidence/task-7-hls-endpoint.txt

  Scenario: MJPEG 摄像头 HLS 端点拒绝
    Tool: Bash (curl)
    Steps:
      1. curl -s -u admin:admin http://192.168.63.31:9090/api/cameras 获取 MJPEG 摄像头 ID
      2. curl -s -o /dev/null -w "%{http_code}" -u admin:admin http://192.168.63.31:9090/api/cameras/{mjpeg_id}/stream/
    Expected Result: HTTP 400 Bad Request
    Evidence: .sisyphus/evidence/task-7-mjpeg-reject.txt
  ```

  **Commit**: YES
  - Message: `feat(api): HLS streaming endpoint`
  - Files: `internal/api/handler.go`, `cmd/mibee-nvr/main.go`
  - Pre-commit: `rtk go vet ./...`

- [ ] 8. Recorder RTP 分支到 HLS muxer
- [x] 8. Recorder RTP 分支到 HLS muxer
  **What to do**:
  - 修改 `internal/recorder/h264.go`:
    - 添加可选的 HLS 帧回调接口: `OnHLSFrame func(nalUnit [][]byte, pts time.Duration)`
    - 在 `writeFrames()` 中 RTP decode 后分支调用 HLS 回调
    - 不影响现有 MP4 录制管线
  - 修改 `internal/recorder/h265.go` (同理):
    - 添加相同的 OnHLSFrame 回调
  - 在 HLS Manager 中连接回调:
    - StartStream 时设置 recorder 的 OnHLSFrame 回调
    - 回调将帧数据写入 gohlslib.Muxer
    - StopStream 时清除回调
  - 注意: 只在 H264Recorder/H265Recorder 运行中才能分支
    如果 recorder 未运行, HLS Manager 需要自行启动临时 RTSP 连接或返回错误

  **Must NOT do**:
  - 不要创建第二个 RTSP 连接 — 复用 recorder 现有连接的帧数据
  - 不要修改现有 MP4 录制管线
  - 不要阻塞录制管线等待 HLS 写入

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 需要理解 recorder 内部管线并安全地分支数据流
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (after Task 6)
  - **Blocks**: Task 11, Task 15
  - **Blocked By**: Task 6

  **References**:
  **Pattern References**:
  - `internal/recorder/h264.go:writeFrames()` — RTP decode 后写入 MP4 的位置，在此分支
  - `internal/recorder/h264.go:H264Recorder` 结构体 — 需要添加 OnHLSFrame 回调字段
  - `internal/recorder/h265.go` — H.265 recorder, 同样模式

  **API/Type References**:
  - `internal/hls/muxer.go` — HLS muxer 的 WriteH264/WriteH265 接口 (Task 6 产出)
  - `internal/model/types.go:Recorder` — recorder 接口，不需要修改但需理解

  **Acceptance Criteria**:
  - [ ] H264Recorder 和 H265Recorder 有 OnHLSFrame 回调字段
  - [ ] 现有录制功能不受影响
  - [ ] `rtk go vet ./...` 零错误
  - [ ] `rtk go test ./internal/recorder/... -v` 通过 (如有测试)

  **QA Scenarios:**
  ```
  Scenario: 录制功能不受影响
    Tool: Bash
    Steps:
      1. rtk go vet ./internal/recorder/...
      2. rtk go test ./internal/recorder/... -v
    Expected Result: 编译通过，现有测试仍然通过
    Evidence: .sisyphus/evidence/task-8-recorder.txt
  ```

  **Commit**: YES
  - Message: `feat(recorder): branch RTP to HLS muxer`
  - Files: `internal/recorder/h264.go`, `internal/recorder/h265.go`, `internal/hls/manager.go`
  - Pre-commit: `rtk go vet ./...`


- [x] 9. Stats.svelte 可筛选统计图

  **What to do**:
  - 重构 `web/src/routes/Stats.svelte` 的摄像头统计图 (cameraChart):
    - 在图表上方添加摄像头筛选/切换区域:
      - 显示所有摄像头的小标签 (tag/chip)，每个可点击切换选中/取消
      - 默认全部选中
      - 取消选中的摄像头从图表中移除
    - 图表动态更新: 选中变化时销毁并重建 chart 实例 (Chart.js 必须先 destroy 再 new)
    - 增加可折叠面板: 点击标题可展开/收起整个图表区域
  - 保持现有存储趋势图 (trendChart) 不变
  - 保持现有主题支持 (dark/light) 和自动刷新 (30s)
  - 视觉风格: 使用现有的设计系统 (app.css 变量 + TailwindCSS)

  **Must NOT do**:
  - 不要修改后端 API — 使用现有 getStatsTrends 数据
  - 不要引入新的 chart 库 — 继续使用 Chart.js
  - 不要移除存储趋势图
  - 不要改变现有 i18n key 的值 — 只添加新 key

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 前端 UI 交互设计，涉及图表动态更新和动画
  - **Skills**: [`/frontend-ui-ux`]
    - `/frontend-ui-ux`: UI/UX 设计实现

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 13
  - **Blocked By**: Task 2

  **References**:
  **Pattern References**:
  - `web/src/routes/Stats.svelte:154-252` — 现有图表创建逻辑, createCharts() 函数
  - `web/src/routes/Stats.svelte:216-225` — 摄像头颜色调色板 (8 色)
  - `web/src/routes/Stats.svelte:265-282` — 自动刷新和主题监听模式
  - `web/src/app.css` — 设计系统 CSS 变量 (颜色、间距、圆角等)

  **Acceptance Criteria**:
  - [ ] 摄像头标签区域显示所有摄像头
  - [ ] 点击标签可切换选中/取消
  - [ ] 图表动态更新反映选中摄像头
  - [ ] 图表区域可折叠
  - [ ] `cd web && npm run build` 成功

  **QA Scenarios:**
  ```
  Scenario: 摄像头筛选功能正常
    Tool: Playwright (browser automation)
    Steps:
      1. 导航到 http://192.168.63.31:9090/#/stats
      2. 等待图表加载完成 (timeout: 10s)
      3. 查找摄像头标签元素
      4. 点击第二个摄像头标签取消选中
      5. 等待图表更新 (timeout: 2s)
      6. 截图
      7. 再次点击同一标签恢复选中
      8. 截图
    Expected Result: 取消选中后图表移除该摄像头数据，恢复后重新显示
    Evidence: .sisyphus/evidence/task-9-chart-filter.png

  Scenario: 折叠面板功能正常
    Tool: Playwright
    Steps:
      1. 在统计页找到折叠按钮
      2. 点击折叠
      3. 确认图表区域隐藏
      4. 点击展开
      5. 确认图表区域显示
    Expected Result: 折叠后图表区域隐藏，展开后恢复
    Evidence: .sisyphus/evidence/task-9-collapse.png
  ```

  **Commit**: YES
  - Message: `feat(web): filterable stats chart`
  - Files: `web/src/routes/Stats.svelte`
  - Pre-commit: `cd web && npm run build`

- [x] 10. Cameras.svelte 时间状态 + 实时按钮

  **What to do**:
  - 修改 `web/src/routes/Cameras.svelte`:
    - **摄像头状态改进**:
      - 替换现有的 recording/stopped/error/reconnecting 状态 badge
      - 使用 API 返回的 `last_seen` 时间字段计算时间差:
        - last_seen < 5分钟: 绿色 “活跃” (X分钟前)
        - last_seen 5-30分钟: 黄色警告 “X分钟前”
        - last_seen > 30分钟: 红色异常 “X分钟前”
        - last_seen 为 null: 灰色 “从未录制”
      - 保留 recorder_status (recording/error/reconnecting) 作为补充信息
    - **实时播放按钮**:
      - 仅在摄像头协议为 rtsp_h264 或 rtsp_h265 时显示 “实时” 按钮
      - 点击跳转到 `#/live/{camera_id}`
      - 使用 lucide-svelte 的 Video 或 Eye 图标
    - **Per-camera retention 编辑**:
      - 在摄像头编辑/创建表单中添加 "保存天数" (retention_days) 字段
      - 数字输入框，0 = 使用全局设置
      - 提示文字: “0 = 使用全局设置 (当前: {global_retention_days}天)”
  - 修改 `web/src/lib/api.ts` 的 updateCamera 类型定义包含 retention_days

  **Must NOT do**:
  - 不要修改后端 API — 使用 Task 5 产出的 last_seen 字段
  - 不要为 MJPEG/HTTP JPEG 摄像头显示实时按钮
  - 不要移除摄像头 CRUD 功能

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: UI 状态展示改进 + 新按钮 + 表单字段
  - **Skills**: [`/frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 13
  - **Blocked By**: Task 3 (retention_days), Task 5 (last_seen)

  **References**:
  **Pattern References**:
  - `web/src/routes/Cameras.svelte:448-456` — 现有状态 badge 渲染逻辑
  - `web/src/routes/Cameras.svelte` — 摄像头编辑表单结构
  - `web/src/routes/Cameras.svelte` — 现有 CRUD 操作模式

  **API/Type References**:
  - `web/src/lib/api.ts` — API client, 需要在 Camera 类型中添加 retention_days 和 last_seen
  - `web/src/lib/format.ts` — 可能有时间格式化工具可复用

  **Acceptance Criteria**:
  - [ ] 状态显示时间 (如 “3分钟前活跃”)
  - [ ] 超过 5 分钟显示黄色警告
  - [ ] 超过 30 分钟显示红色异常
  - [ ] H.264/H.265 摄像头显示实时按钮
  - [ ] MJPEG 摄像头不显示实时按钮
  - [ ] 编辑表单包含 retention_days 字段
  - [ ] `cd web && npm run build` 成功

  **QA Scenarios:**
  ```
  Scenario: 摄像头状态显示时间
    Tool: Playwright
    Steps:
      1. 导航到 http://192.168.63.31:9090/#/cameras
      2. 查找摄像头列表中的状态 badge
      3. 确认显示时间文字 (如 "X分钟前活跃")
      4. 截图
    Expected Result: 状态 badge 显示时间信息而非简单的 recording/stopped
    Evidence: .sisyphus/evidence/task-10-status-time.png

  Scenario: H.264 摄像头显示实时按钮
    Tool: Playwright
    Steps:
      1. 在摄像头列表查找协议为 rtsp_h264 的摄像头行
      2. 确认存在实时/播放按钮
      3. 截图
    Expected Result: H.264 摄像头行有实时按钮
    Evidence: .sisyphus/evidence/task-10-live-btn.png

  Scenario: MJPEG 摄像头无实时按钮
    Tool: Playwright
    Steps:
      1. 在摄像头列表查找协议为 rtsp_mjpeg 或 http_jpeg 的摄像头行
      2. 确认不存在实时/播放按钮
    Expected Result: MJPEG/HTTP JPEG 摄像头行无实时按钮
    Evidence: .sisyphus/evidence/task-10-no-live-btn.png
  ```

  **Commit**: YES
  - Message: `feat(web): camera time-based status + live button`
  - Files: `web/src/routes/Cameras.svelte`, `web/src/lib/api.ts`
  - Pre-commit: `cd web && npm run build`

- [x] 11. LiveView.svelte 实时播放页面

  **What to do**:
  - 创建新组件 `web/src/routes/LiveView.svelte`:
    - 从 URL hash 获取 camera_id: `#/live/{camera_id}`
    - 查询摄像头信息 (getCamera API)
    - 如果摄像头不是 H.264/H.265 协议，显示 "不支持实时播放" 提示
    - 播放器区域:
      - 使用 HLS.js 库 (npm install hls.js) 或浏览器原生 HLS 支持 (Safari)
      - 视频源: `/api/cameras/{id}/stream/` (m3u8 playlist)
      - 需要在请求中带上 Basic Auth header (XHR 拦截或 hls.js xhrSetup)
    - 全屏播放控制: 播放/暂停、全屏切换
    - 返回按钮: 返回摄像头列表
    - 摄像头名称显示
    - 加载状态和错误处理
  - 修改 `web/src/App.svelte`:
    - 在 parseRoute() 中添加 `#/live/{id}` 路由识别
    - 在渲染块中添加 LiveView 组件
  - 修改 `web/src/components/Header.svelte`:
    - 不需要添加导航项 (LiveView 是从摄像头列表跳转进入)
    - 但需要处理 active state (live 路由时高亮 cameras 导航项)
  - 添加 hls.js 依赖: `cd web && npm install hls.js`

  **Must NOT do**:
  - 不要用 `<video src=...>` 直接设置 HLS URL — 需要通过 hls.js 带 Auth header
  - 不要在 Header 导航栏添加 LiveView 导航项
  - 不要在 URL 中嵌入凭证

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 视频播放器 UI + 路由集成
  - **Skills**: [`/frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 13
  - **Blocked By**: Task 7 (HLS endpoint), Task 8 (recorder 分支)

  **References**:
  **Pattern References**:
  - `web/src/routes/RecordingDetail.svelte:513-557` — 现有视频播放器模式
  - `web/src/routes/RecordingDetail.svelte:359` — XHR 带 auth header 的视频加载模式
  - `web/src/App.svelte:parseRoute()` — 现有路由解析模式
  - `web/src/App.svelte` — 渲染块中的组件切换模式

  **External References**:
  - hls.js: `https://github.com/video-dev/hls.js` — HLS 播放器库
  - hls.js xhrSetup: 配置自定义 HTTP headers (用于 Basic Auth)

  **Acceptance Criteria**:
  - [ ] `#/live/{h264_camera_id}` 页面加载 HLS 播放器
  - [ ] 视频画面显示实时画面
  - [ ] `#/live/{mjpeg_camera_id}` 显示 "不支持" 提示
  - [ ] 播放器有全屏切换功能
  - [ ] `cd web && npm run build` 成功

  **QA Scenarios:**
  ```
  Scenario: H.264 摄像头实时播放
    Tool: Playwright
    Steps:
      1. 导航到 http://192.168.63.31:9090/#/live/{h264_camera_id}
      2. 等待播放器加载 (timeout: 15s)
      3. 确认 <video> 元素存在且正在播放
      4. 截图
    Expected Result: 视频播放器显示实时画面
    Evidence: .sisyphus/evidence/task-11-live-play.png

  Scenario: MJPEG 摄像头不支持提示
    Tool: Playwright
    Steps:
      1. 导航到 http://192.168.63.31:9090/#/live/{mjpeg_camera_id}
      2. 确认显示 "不支持实时播放" 或类似提示
      3. 确认无 <video> 元素
    Expected Result: 显示不支持提示，无播放器
    Evidence: .sisyphus/evidence/task-11-unsupported.png
  ```

  **Commit**: YES
  - Message: `feat(web): live view page with HLS player`
  - Files: `web/src/routes/LiveView.svelte`, `web/src/App.svelte`, `web/package.json`
  - Pre-commit: `cd web && npm run build`

- [x] 12. Recordings.svelte 简化时间选择

  **What to do**:
  - 修改 `web/src/routes/Recordings.svelte`:
    - 移除快速选择下拉框 (preset dropdown with 1h/24h/7d/30d options)
    - 页面加载时默认设置时间范围为最近1小时
    - 保留手动日期时间选择器 (datetime-local inputs) 以供自定义范围
    - 修改 `onMount` 或初始化逻辑，设置默认 start/end 时间为 now-1h/now
  - 删除 `setPresetRange()` 函数和相关 preset 变量

  **Must NOT do**:
  - 不要移除手动日期时间选择器 — 用户仍可自定义范围
  - 不要改变其他筛选功能 (camera filter, format filter, search)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 简单的 UI 简化，移除组件 + 调整默认值
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 13
  - **Blocked By**: Task 2

  **References**:
  **Pattern References**:
  - `web/src/routes/Recordings.svelte:166-173` — setPresetRange() 函数 (需删除)
  - `web/src/routes/Recordings.svelte:304-312` — 快速选择 UI (需删除)
  - `web/src/routes/Recordings.svelte` — onMount 初始化逻辑 (需修改默认值)

  **Acceptance Criteria**:
  - [ ] 页面默认加载最近1小时的录像
  - [ ] 无快速选择下拉框
  - [ ] 手动日期时间选择器仍可用
  - [ ] `cd web && npm run build` 成功

  **QA Scenarios:**
  ```
  Scenario: 录像页默认显示最近1小时
    Tool: Playwright
    Steps:
      1. 导航到 http://192.168.63.31:9090/#/recordings
      2. 等待录像列表加载
      3. 检查时间范围输入框的值
      4. 确认开始时间为约1小时前
      5. 确认无快速选择下拉框
      6. 截图
    Expected Result: 默认加载1小时范围，无快速选择下拉框
    Evidence: .sisyphus/evidence/task-12-recordings-default.png
  ```

  **Commit**: YES
  - Message: `fix(web): simplify recordings time selection, default 1h`
  - Files: `web/src/routes/Recordings.svelte`
  - Pre-commit: `cd web && npm run build`

- [x] 13. i18n 更新 (en.json + zh.json)

  **What to do**:
  - 更新 `web/src/lib/i18n/en.json` 添加新 key:
    - 统计图筛选相关: "stats.filterCameras", "stats.allCameras", "stats.selectedCameras"
    - 摄像头状态相关: "cameras.lastSeen", "cameras.active", "cameras.inactive", "cameras.neverRecorded", "cameras.minutesAgo", "cameras.hoursAgo", "cameras.daysAgo"
    - 实时播放相关: "cameras.live", "live.title", "live.notSupported", "live.loading", "live.error", "live.backToCameras", "live.fullscreen"
    - Retention 相关: "cameras.retentionDays", "cameras.retentionDaysHint", "cameras.useGlobal"
    - 录像页简化: 移除不再需要的 preset 相关 key (如 "recordings.last1h", "recordings.last24h" 等)
  - 更新 `web/src/lib/i18n/zh.json` 添加对应中文翻译
  - 确保两个文件 key 完全一致

  **Must NOT do**:
  - 不要修改现有 key 的值 — 只添加新 key 和删除不再使用的 key
  - 不要遗漏任一语言文件

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 纯文本 JSON 文件更新
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2 (last, after Tasks 9-12)
  - **Blocks**: Task 14
  - **Blocked By**: Tasks 9, 10, 11, 12

  **References**:
  **Pattern References**:
  - `web/src/lib/i18n/en.json` — 现有英文翻译 key 结构
  - `web/src/lib/i18n/zh.json` — 现有中文翻译 key 结构

  **Acceptance Criteria**:
  - [ ] en.json 和 zh.json 包含所有新 key
  - [ ] 两个文件 key 集合完全一致
  - [ ] `cd web && npm run build` 成功

  **QA Scenarios:**
  ```
  Scenario: i18n key 完整性
    Tool: Bash
    Steps:
      1. 提取 en.json 和 zh.json 的所有 key
      2. 比较 key 集合差异
    Expected Result: 两个文件 key 完全一致
    Evidence: .sisyphus/evidence/task-13-i18n.txt
  ```

  **Commit**: YES
  - Message: `chore(web): update i18n strings`
  - Files: `web/src/lib/i18n/en.json`, `web/src/lib/i18n/zh.json`
  - Pre-commit: `cd web && npm run build`

- [x] 14. 前端 build + Go embed

  **What to do**:
  - 运行 `cd web && npm run build` 构建前端 SPA
  - 将构建产物复制到 Go embed 目录: `cp -r web/dist/* internal/ui/static/`
  - 确认 `internal/ui/static/index.html` 引用的 JS/CSS 文件与 dist 一致
  - 清理旧的构建产物 (确保无多余文件)
  - 运行 `rtk make build` 或 `rtk make cross` 验证 Go 编译成功

  **Must NOT do**:
  - 不要修改任何代码 — 只做构建和复制
  - 不要遗留旧版本构建产物

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 纯构建操作
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2 (after Task 13)
  - **Blocks**: Task 15
  - **Blocked By**: Task 13

  **References**:
  **Pattern References**:
  - `web/dist/` — 前端构建输出目录
  - `internal/ui/static/` — Go embed 输入目录
  - `internal/ui/embed.go` — go:embed 指令

  **Acceptance Criteria**:
  - [ ] `cd web && npm run build` 成功
  - [ ] `internal/ui/static/index.html` 引用的资源文件存在
  - [ ] `rtk make build` 或 `rtk make cross` 成功
  - [ ] `internal/ui/static/assets/` 无多余文件

  **QA Scenarios:**
  ```
  Scenario: 前端构建并嵌入 Go 二进制
    Tool: Bash
    Steps:
      1. cd web && npm run build
      2. cp -r web/dist/* internal/ui/static/
      3. ls internal/ui/static/assets/
      4. rtk make build
    Expected Result: 构建成功, Go 编译成功, 无多余资源文件
    Evidence: .sisyphus/evidence/task-14-build.txt
  ```

  **Commit**: YES
  - Message: `chore: rebuild SPA and embed`
  - Files: `internal/ui/static/*`
  - Pre-commit: `rtk make build`


- [x] 15. 跨平台编译 + 部署到 RPi + 浏览器验收

  **What to do**:
  - 跨平台编译: `GOOS=linux GOARCH=arm64 go build -o mibee-nvr-arm64 ./cmd/mibee-nvr/`
  - 部署到 RPi:
    - `scp mibee-nvr-arm64 mickey@192.168.63.31:/tmp/mibee-nvr`
    - `ssh mickey@192.168.63.31 "sudo systemctl stop mibee-nvr && sudo cp /tmp/mibee-nvr /mnt/data/nvr/bin/mibee-nvr && sudo systemctl start mibee-nvr"`
    - 等待服务启动 (sleep 5s)
  - 验证部署:
    - `curl -s -u admin:admin http://192.168.63.31:9090/api/health` 确认服务正常
  - 浏览器验收 (使用 Playwright):
    - 导航到 http://192.168.63.31:9090
    - 登录 (如需)
    - 验证所有页面加载正常
    - 验证统计页筛选功能
    - 验证摄像头列表时间状态
    - 验证录像页默认1小时
    - 验证 H.264 摄像头实时播放
    - 拍摄验收截图

  **Must NOT do**:
  - 不要直接在 RPi 上编译 — 使用跨平台编译
  - 不要跳过服务重启 — 确保新版本加载

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 涉及编译、远程部署、多场景验收测试
  - **Skills**: [`/playwright`]
    - `/playwright`: 浏览器自动化验收测试

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3
  - **Blocks**: F1-F4
  - **Blocked By**: Tasks 4, 5, 7, 8, 14

  **References**:
  **Pattern References**:
  - `Makefile` — cross 编译目标 (`rtk make cross`)
  - `deploy/` — systemd service 文件

  **Acceptance Criteria**:
  - [ ] 跨平台编译成功
  - [ ] 服务在 RPi 上运行正常
  - [ ] 所有 API 端点响应正常
  - [ ] 浏览器访问 Web UI 正常
  - [ ] 验收截图已保存

  **QA Scenarios:**
  ```
  Scenario: 服务部署成功
    Tool: Bash (ssh + curl)
    Steps:
      1. GOOS=linux GOARCH=arm64 go build -o mibee-nvr-arm64 ./cmd/mibee-nvr/
      2. scp mibee-nvr-arm64 mickey@192.168.63.31:/tmp/mibee-nvr
      3. ssh mickey@192.168.63.31 "sudo systemctl stop mibee-nvr && sudo cp /tmp/mibee-nvr /mnt/data/nvr/bin/mibee-nvr && sudo systemctl start mibee-nvr"
      4. sleep 5
      5. curl -s http://192.168.63.31:9090/api/health
    Expected Result: health endpoint 返回 {"status":"ok"}
    Evidence: .sisyphus/evidence/task-15-deploy.txt

  Scenario: 统计页筛选功能验收
    Tool: Playwright
    Steps:
      1. 导航到 http://192.168.63.31:9090/#/stats
      2. 等待页面加载
      3. 确认摄像头筛选标签存在
      4. 点击标签切换
      5. 截图
    Expected Result: 筛选标签可点击，图表动态更新
    Evidence: .sisyphus/evidence/task-15-stats-filter.png

  Scenario: 摄像头状态时间显示验收
    Tool: Playwright
    Steps:
      1. 导航到 http://192.168.63.31:9090/#/cameras
      2. 等待列表加载
      3. 检查状态列显示时间 (如 "X分钟前活跃")
      4. 截图
    Expected Result: 状态列显示时间信息
    Evidence: .sisyphus/evidence/task-15-cameras-status.png

  Scenario: 录像页默认1小时验收
    Tool: Playwright
    Steps:
      1. 导航到 http://192.168.63.31:9090/#/recordings
      2. 等待加载
      3. 确认无快速选择下拉框
      4. 确认默认时间范围约1小时
      5. 截图
    Expected Result: 默认加载1小时范围，无快速选择
    Evidence: .sisyphus/evidence/task-15-recordings.png

  Scenario: 实时播放功能验收
    Tool: Playwright
    Steps:
      1. 从摄像头列表点击 H.264 摄像头的实时按钮
      2. 等待跳转到播放页
      3. 等待视频加载 (timeout: 15s)
      4. 确认 <video> 元素存在且播放中
      5. 截图
    Expected Result: 实时播放页面显示摄像头实时画面
    Evidence: .sisyphus/evidence/task-15-live-view.png
  ```

  **Commit**: NO (部署任务不提交代码)

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `rtk go vet ./...` + `rtk go test ./... -v`. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high` (+ playwright skill)
  Start from clean state on 192.168.63.31. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration. Test edge cases: MJPEG camera no live button, camera with 0 retention uses global, HLS idle timeout. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff (git log/diff). Verify 1:1 — everything in spec was built, nothing beyond spec was built. Check "Must NOT do" compliance. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

| Commit | Message | Files | Pre-commit Check |
|--------|---------|-------|------------------|
| 1 (main) | `feat: H.265 support, WebDAV read-write, UI fixes` | 12 modified + 2 untracked | `rtk go vet ./...` |
| 2 | `feat(storage): add per-camera retention_days migration v2→v3` | db.go | `rtk go test ./internal/storage/... -v` |
| 3 | `feat(cleanup): per-camera retention in cleanup logic` | cleanup.go | `rtk go vet ./...` |
| 4 | `feat(api): camera last_seen status enrichment` | handler.go, manager.go | `rtk go vet ./...` |
| 5 | `feat(hls): add gohlslib integration with on-demand lifecycle` | internal/hls/*.go | `rtk go vet ./...` |
| 6 | `feat(api): HLS streaming endpoint` | handler.go | `rtk go vet ./...` |
| 7 | `feat(recorder): branch RTP to HLS muxer` | h264.go, h265.go | `rtk go vet ./...` |
| 8 | `feat(web): filterable stats chart` | Stats.svelte | `cd web && npm run build` |
| 9 | `feat(web): camera time-based status + live button` | Cameras.svelte | `cd web && npm run build` |
| 10 | `feat(web): live view page with HLS player` | LiveView.svelte, App.svelte, Header.svelte | `cd web && npm run build` |
| 11 | `fix(web): simplify recordings time selection, default 1h` | Recordings.svelte | `cd web && npm run build` |
| 12 | `chore(web): update i18n strings` | en.json, zh.json | `cd web && npm run build` |
| 13 | `chore: rebuild SPA and embed` | internal/ui/static/* | `cd web && npm run build && cp -r dist/* ../internal/ui/static/` |

---

## Success Criteria

### Verification Commands
```bash
# Backend
rtk go vet ./...              # Expected: zero errors
rtk go test ./... -v          # Expected: all pass

# Frontend
cd web && npm run build       # Expected: successful build

# Cross-compile
rtk make cross                # Expected: ./mibee-nvr-arm64 binary

# Deploy
ssh mickey@192.168.63.31 "systemctl status mibee-nvr"  # Expected: active (running)

# API verification
curl -s -u admin:admin http://192.168.63.31:9090/api/health  # Expected: {"status":"ok"}
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All Go tests pass
- [ ] Frontend builds successfully
- [ ] Binary deployed and running on RPi
- [ ] Browser verification all features work
- [ ] HLS live view functional for H.264/H.265 cameras
- [ ] MJPEG cameras correctly show no live button
- [ ] Per-camera retention configurable via camera edit form
- [ ] Stats chart filterable by camera
- [ ] Recordings page defaults to last 1 hour
- [ ] Camera status shows time-based indicators with thresholds
