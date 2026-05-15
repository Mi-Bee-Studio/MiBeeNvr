# HLS 四路实时预览优化 + ONVIF 完整实现

## TL;DR

> **Quick Summary**: 重构监控大屏支持四路 HLS 实时预览（当前仅 1 路 HLS + 3 路快照），优化 RPi 3B 内存占用（600MB 上限）。完整实现 ONVIF 设备发现、摄像头添加、PTZ 控制（当前 100% 代码桩）。
> 
> **Deliverables**:
> - Dashboard 4 路 HLS 同时播放（2×2 网格）
> - HLS 缓冲优化（适配 RPi 3B 600MB 内存预算）
> - ONVIF WS-Discovery 设备扫描
> - ONVIF Client（设备信息、Profile、流 URI）
> - ONVIFRecorder（实现 model.Recorder 接口）
> - ONVIF PTZ 控制（连续/绝对/相对移动）
> - 完整 TDD 测试覆盖
> 
> **Estimated Effort**: XL
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: T3 → T10 → T12 → T13 → F1-F4

---

## Context

### Original Request
监控大屏经常播放不了完整的四个实时预览，一直 loading。ONVIF 设备扫描一直用不了。

### Interview Summary
**Key Discussions**:
- 当前 Dashboard 设计为 1 个 HLS + 3 个 JPEG 快照，用户期望四路都是实时 HLS
- ONVIA 是 100% 代码桩，onvif-go v1.1.4 在 go.mod 但未使用
- 用户有物理 ONVIF 设备但不知道 IP，需要自动扫描发现
- 用户选择独立 ONVIFRecorder（不复用 RTSP recorder）
- 内存上限 600MB，远程浏览器访问

**Research Findings**:
- HLS Manager: `defaultMaxStreams=4`, `writeBufSize=120`, `SegmentMaxSize=50MB` — 需大幅缩减
- `ErrMaxStreamsReached` 定义了但从未返回（静默驱逐 bug）
- ONVIF 前端 UI 已完整（扫描按钮、设备列表、添加表单）
- ONVIF API 端点已存在但都是 stub
- `onvif-go` 库的 WS-Discovery 支持需验证
- RPi 3B: 905MB RAM, 当前 ~300MB 稳定运行

### Metis Review
**Identified Gaps** (addressed):
- HLS 帧传递是 pull-on-demand（通过 `OnHLSFrame` callback），非 recorder 驱动 → 需确保多 callback 支持
- DB schema 缺少 ONVIF 字段（onvif_endpoint, profile_token）→ 需 migration
- ONVIF 摄像头当前返回 nil recorder → 需创建 ONVIFRecorder
- 子网问题：RPi 192.168.63.x, 摄像头 192.168.62.x — 多播可能跨不过路由 → 需支持单 IP probe
- PTZ 并发控制 → 需 mutex

---

## Work Objectives

### Core Objective
将监控大屏从"1 个 HLS + 3 个快照"升级为"4 路 HLS 同时实时播放"，并完整实现 ONVIF 设备发现、添加、录制和 PTZ 控制。

### Concrete Deliverables
- `internal/hls/manager.go` — 缓冲参数优化 + 驱逐策略修复
- `internal/hls/errors.go` — 修复 ErrMaxStreamsReached 未返回的 bug
- `internal/onvif/discovery.go` — WS-Discovery 实现（UDP 多播 + 单 IP probe）
- `internal/onvif/client.go` — ONVIF Client 完整实现
- `internal/onvif/ptz.go` — PTZ 操作完整实现
- `internal/recorder/onvif.go` — ONVIFRecorder 新建
- `internal/storage/db.go` — DB migration（onvif_endpoint, profile_token 列）
- `internal/camera/manager.go` — ONVIF 摄像头支持
- `internal/api/handler.go` — 替换所有 ONVIF/PTZ stub
- `web/src/routes/Dashboard.svelte` — 四路 HLS 2×2 网格
- `web/src/lib/hls-config.ts` — 共享 HLS.js 配置模块

### Definition of Done
- [x] Dashboard 四路摄像头同时播放 HLS 实时视频，无 loading 卡顿
- [x] 4 路 HLS + 5 个录制器进程 RSS ≤ 600MB
- [x] ONVIF 扫描能发现局域网内设备（含跨子网手动 IP probe）
- [x] ONVIF 摄像头添加后自动开始录制
- [x] PTZ 控制响应正常（连续/绝对移动、停止）
- [x] 所有新增代码有 TDD 测试，`go test ./...` 全部通过
- [x] `rtk go vet ./...` 无警告

### Must Have
- 四路 HLS 同时播放（H264/H265）
- HLS 缓冲优化（适配 RPi 3B 内存预算）
- 修复 ErrMaxStreamsReached 静默驱逐 bug
- ONVIF WS-Discovery 设备发现
- ONVIF 摄像头添加（独立 ONVIFRecorder）
- ONVIF PTZ 控制
- hls.js 缓冲参数优化
- 前端优雅降级（HLS 失败→快照回退）
- 完整 TDD 测试

### Must NOT Have (Guardrails)
- ❌ 不改 recorder 录制管线帧路径（录制质量/行为不受影响）
- ❌ 不增加 SegmentMaxSize（50MB→10MB 方向）
- ❌ 不改 defaultMaxStreams=4（RPi 无法承受更多）
- ❌ 不加 C 依赖（CGO_ENABLED=0 硬约束）
- ❌ 不实现 ONVIF Events/Analytics/Replay/Imaging（超出范围）
- ❌ 不重新设计 PtzControl.svelte（已有组件，只需接线）
- ❌ 不加 WebSocket PTZ（保持 HTTP POST）
- ❌ 不加 HLS 自适应码率（RPi 是服务端不是 CDN）
- ❌ 不自动修改 ONVIF 摄像头设置（只读发现 + PTZ 写入）
- ❌ 不管理 ONVIF 设备用户（假设已有凭据）

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES
- **Automated tests**: YES (TDD)
- **Framework**: Go testing + testify/require
- **TDD workflow**: Each task follows RED (failing test) → GREEN (minimal impl) → REFACTOR

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Frontend/UI**: Use Playwright (playwright skill) — Navigate, interact, assert DOM, screenshot
- **API/Backend**: Use Bash (curl) — Send requests, assert status + response fields
- **Go tests**: Use Bash (`go test`) — Run unit/integration tests
- **Resource monitoring**: Use Bash (SSH + ps) — Check memory/goroutines

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — foundation + config, 6 tasks):
├── T1: HLS buffer optimization [quick]
├── T2: HLS fix ErrMaxStreamsReached bug [quick]
├── T3: ONVIF testable interfaces + mocks [quick]
├── T4: ONVIF DB schema migration [quick]
├── T5: Frontend shared HLS config module [quick]
└── T6: ONVIF validate library + import setup [quick]

Wave 2 (After Wave 1 — core implementation, 6 tasks MAX PARALLEL):
├── T7: HLS multi-stream backend + tests (depends: T1, T2) [deep]
├── T8: Frontend Dashboard 4-HLS grid (depends: T5) [visual-engineering]
├── T9: ONVIF WS-Discovery impl + tests (depends: T3, T6) [deep]
├── T10: ONVIF Client operations + tests (depends: T3, T6) [deep]
├── T11: ONVIF PTZ operations + tests (depends: T3, T6) [deep]
└── T12: ONVIFRecorder impl + tests (depends: T3, T4, T10) [deep]

Wave 3 (After Wave 2 — integration + polish, 4 tasks):
├── T13: ONVIF camera manager + API handlers (depends: T9-T12) [unspecified-high]
├── T14: ONVIF frontend scan→add→PTZ flow (depends: T13) [visual-engineering]
├── T15: HLS frontend error recovery + degradation (depends: T8) [unspecified-high]
└── T16: HLS + ONVIF end-to-end integration (depends: T7, T13, T14, T15) [deep]

Wave FINAL (After ALL — 4 parallel reviews):
├── F1: Plan compliance audit (oracle)
├── F2: Code quality review (unspecified-high)
├── F3: Real manual QA (unspecified-high + playwright)
└── F4: Scope fidelity check (deep)
→ Present results → Get explicit user okay

Critical Path: T3 → T10 → T12 → T13 → T14 → T16 → F1-F4
Parallel Speedup: ~65% faster than sequential
Max Concurrent: 6 (Wave 2)
```

### Dependency Matrix

| Task | Blocked By | Blocks | Wave |
|------|-----------|--------|------|
| T1 | - | T7 | 1 |
| T2 | - | T7 | 1 |
| T3 | - | T9, T10, T11, T12 | 1 |
| T4 | - | T12 | 1 |
| T5 | - | T8 | 1 |
| T6 | - | T9, T10, T11 | 1 |
| T7 | T1, T2 | T16 | 2 |
| T8 | T5 | T15 | 2 |
| T9 | T3, T6 | T13 | 2 |
| T10 | T3, T6 | T12, T13 | 2 |
| T11 | T3, T6 | T13 | 2 |
| T12 | T3, T4, T10 | T13 | 2 |
| T13 | T9-T12 | T14 | 3 |
| T14 | T13 | T16 | 3 |
| T15 | T8 | T16 | 3 |
| T16 | T7, T13, T14, T15 | F1-F4 | 3 |
| F1-F4 | T16 | - | FINAL |

### Agent Dispatch Summary

- **Wave 1**: **6** — T1-T6 → all `quick`
- **Wave 2**: **6** — T7, T12 → `deep`; T8 → `visual-engineering`; T9-T11 → `deep`
- **Wave 3**: **4** — T13 → `unspecified-high`; T14 → `visual-engineering`; T15 → `unspecified-high`; T16 → `deep`
- **FINAL**: **4** — F1 → `oracle`; F2-F3 → `unspecified-high`; F4 → `deep`

---

## TODOs

- [x] 1. HLS Buffer Configuration Optimization + Tests

- [x] 2. Fix ErrMaxStreamsReached Silent Eviction Bug + Tests

- [x] 3. ONVIF Testable Interfaces + Mock Types

- [x] 4. ONVIF DB Schema Migration + Tests

- [x] 5. Frontend Shared HLS.js Config Module


  **What to do**:
  - TDD: 先写测试验证新缓冲参数在 4 路并发下总内存占用合理
  - 修改 `internal/hls/manager.go`:
    - `writeBufSize`: 120 → 40 (约 2s@20fps，原来 6s)
    - `SegmentMaxSize`: 50MB → 10MB (4路×3段 = 120MB 上限 vs 原来 600MB)
    - `SegmentCount`: 保持 3 不变
    - `SegmentMinDuration`: 保持 2s 不变
  - 添加配置项支持：`hls_write_buffer_size`, `hls_segment_max_size` 到 `config.go`
  - 确保参数变更不影响现有 recorder 管线的帧路径

  **Must NOT do**:
  - 不改 recorder 的帧处理逻辑
  - 不增加任何 SegmentMaxSize
  - 不改 defaultMaxStreams=4

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 纯配置参数调整 + 简单测试，逻辑清晰
  - **Skills**: []
  - **Skills Evaluated but Omitted**:
    - 无需特殊 skill，纯 Go 代码变更

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2-T6)
  - **Blocks**: T7 (HLS multi-stream backend)
  - **Blocked By**: None

  **References**:
  - `internal/hls/manager.go:28-29` — `defaultMaxStreams=4`, `writeBufSize=120`, `SegmentMaxSize=50MB` 定义位置
  - `internal/hls/manager.go:125-140` — SegmentCount/SegmentMinDuration/SegmentMaxSize 在 muxer 配置中的使用
  - `internal/config/config.go:38-50` — Config 结构体，需添加 HLS buffer 配置字段
  - `internal/hls/manager_test.go` — 现有测试模式，遵循 `t.Helper()` + `require` 约定

  **Acceptance Criteria**:
  - [ ] `go test ./internal/hls/... -v` → PASS
  - [ ] writeBufSize 默认值为 40
  - [ ] SegmentMaxSize 默认值为 10MB
  - [ ] 新增 config 字段可覆盖默认值

  **QA Scenarios**:
  ```
  Scenario: Buffer parameters are applied correctly
    Tool: Bash (go test)
    Preconditions: Go test environment
    Steps:
      1. rtk go test ./internal/hls/... -run TestBufferConfig -v
      2. Assert writeBufSize=40, SegmentMaxSize=10485760
    Expected Result: Tests PASS with correct values
    Evidence: .sisyphus/evidence/task-1-buffer-config.txt

  Scenario: Config override works
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/hls/... -run TestBufferConfigOverride -v
      2. Set custom buffer values via config, verify Manager uses them
    Expected Result: Custom values applied, tests PASS
    Evidence: .sisyphus/evidence/task-1-config-override.txt
  ```

  **Commit**: YES
  - Message: `perf(hls): optimize buffer sizes for RPi 3B multi-stream`
  - Files: `internal/hls/manager.go, internal/config/config.go`
  - Pre-commit: `go test ./internal/hls/... ./internal/config/...`

- [x] 2. Fix ErrMaxStreamsReached Silent Eviction Bug + Tests

  **What to do**:
  - TDD: 先写测试验证达到上限时返回 ErrMaxStreamsReached 而非静默驱逐
  - 修改 `internal/hls/manager.go` `startStream()` 方法 (line 87-100):
    - 当 `len(m.streams) >= m.maxStreams` 时，返回 `ErrMaxStreamsReached` 而非自动驱逐
    - 删除静默驱逐逻辑（或移至显式 API）
  - 添加 `EvictStream(cameraID string) error` 方法用于前端主动释放流
  - 确保 `handleHLSStream` (handler.go:1226,1261) 的 `ErrMaxStreamsReached` 检查生效
  - 添加 stream 状态追踪：`GetActiveStreamCount() int`

  **Must NOT do**:
  - 不改 maxStreams 默认值 4
  - 不在 recorder 管线中加锁或阻塞
  - 不增加 goroutine 泄漏风险

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Bug fix，逻辑清晰，影响范围有限
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T3-T6)
  - **Blocks**: T7
  - **Blocked By**: None

  **References**:
  - `internal/hls/manager.go:86-100` — startStream() 中静默驱逐代码，需改为返回错误
  - `internal/hls/errors.go:7` — ErrMaxStreamsReached 定义（已有但未使用）
  - `internal/api/handler.go:1226,1261` — handler 已检查此错误但永远不会收到
  - `internal/hls/manager_test.go` — 现有测试模式

  **Acceptance Criteria**:
  - [ ] `go test ./internal/hls/... -run TestMaxStreams -v` → PASS
  - [ ] startStream() 达到上限时返回 ErrMaxStreamsReached
  - [ ] handler 返回 HTTP 429 Too Many Requests
  - [ ] EvictStream() 方法可正常释放流
  - [ ] GetActiveStreamCount() 返回正确数量

  **QA Scenarios**:
  ```
  Scenario: ErrMaxStreamsReached returned at capacity
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/hls/... -run TestMaxStreams -v
      2. Create 4 streams, attempt 5th → assert ErrMaxStreamsReached returned
    Expected Result: 5th stream returns error, no silent eviction
    Evidence: .sisyphus/evidence/task-2-max-streams.txt

  Scenario: EvictStream releases stream properly
    Tool: Bash (go test)
    Steps:
      1. Create 4 streams, evict stream 1
      2. Assert GetActiveStreamCount() == 3
      3. Create new stream → assert success
    Expected Result: Eviction works, new stream accepted
    Evidence: .sisyphus/evidence/task-2-evict.txt
  ```

  **Commit**: YES
  - Message: `fix(hls): return ErrMaxStreamsReached instead of silent eviction`
  - Files: `internal/hls/manager.go, internal/hls/errors.go`
  - Pre-commit: `go test ./internal/hls/...`

- [x] 3. ONVIF Testable Interfaces + Mock Types

  **What to do**:
  - 在 `internal/onvif/` 下创建接口定义文件，抽取核心接口以便测试 mock:
    - `Discoverer` 接口：`Discover(ctx, timeout) ([]DiscoveredDevice, error)`
    - `DeviceClient` 接口：`Connect/GetDeviceInformation/GetProfiles/GetStreamURI/GetCapabilities`
    - `PTZController` 接口：`ContinuousMove/AbsoluteMove/RelativeMove/Stop/GetStatus`
  - 创建 `internal/onvif/mocks.go` 实现 mock 版本:
    - `MockDiscoverer`：可配置返回设备列表或错误
    - `MockDeviceClient`：可配置设备信息、profiles、stream URI
    - `MockPTZController`：记录 PTZ 命令调用
  - 确保接口与现有 types.go 中的类型兼容
  - 所有 mock 方法需遵循 `t.Helper()` 约定

  **Must NOT do**:
  - 不修改现有 types.go 中的结构体定义
  - 不引入外部 mock 框架（手动 mock）
  - 不破坏现有 stub 实现的测试

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 纯接口定义 + mock 实现，无复杂逻辑
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2, T4-T6)
  - **Blocks**: T9, T10, T11, T12
  - **Blocked By**: None

  **References**:
  - `internal/onvif/types.go` — DiscoveredDevice, DeviceProfile, DeviceInfo, PTZVector 等类型定义
  - `internal/onvif/client.go` — 现有 Client 结构体的方法签名（需抽取为接口）
  - `internal/onvif/discovery.go` — 现有 Discover 函数签名（需抽取为接口）
  - `internal/onvif/ptz.go` — 现有 PTZ 方法签名
  - `internal/model/types.go:Recorder` — Recorder 接口模式参考
  - `internal/onvif/client_test.go` — 现有测试模式

  **Acceptance Criteria**:
  - [ ] `go test ./internal/onvif/... -v` → PASS
  - [ ] Discoverer/DeviceClient/PTZController 接口定义完整
  - [ ] Mock 实现可配置返回值和错误
  - [ ] 接口与现有 types.go 类型兼容

  **QA Scenarios**:
  ```
  Scenario: Interfaces compile and mocks work
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/onvif/... -run TestMocks -v
      2. Verify MockDiscoverer returns configured devices
      3. Verify MockDeviceClient returns configured profiles
    Expected Result: All mock tests PASS
    Evidence: .sisyphus/evidence/task-3-mocks.txt
  ```

  **Commit**: YES
  - Message: `feat(onvif): add testable interfaces and mock types`
  - Files: `internal/onvif/interfaces.go, internal/onvif/mocks.go`
  - Pre-commit: `go test ./internal/onvif/...`

- [x] 4. ONVIF DB Schema Migration + Tests

  **What to do**:
  - TDD: 先写测试验证新字段的 CRUD 操作
  - 修改 `internal/storage/db.go`:
    - `Init()` 中 cameras 表添加列: `onvif_endpoint TEXT DEFAULT ''`, `profile_token TEXT DEFAULT ''`
    - 使用 `ALTER TABLE ADD COLUMN` (SQLite 兼容)
    - 更新 `CameraRow` 结构体添加 `ONVIFEndpoint string` 和 `ProfileToken string`
    - 更新 `UpsertCamera()` 签名接受 onvif_endpoint 和 profile_token
    - 更新所有 scan 查询包含新字段
  - 确保向后兼容（空字符串默认值）

  **Must NOT do**:
  - 不删表重建（必须用 ALTER TABLE 迁移）
  - 不改现有 camera 的 ID/name/protocol 字段
  - 不破坏现有测试

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 纯 DB schema 变更 + CRUD 测试
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1-T3, T5, T6)
  - **Blocks**: T12
  - **Blocked By**: None

  **References**:
  - `internal/storage/db.go:54-68` — cameras 表 CREATE TABLE 语句
  - `internal/storage/db.go:493-516` — CameraRow 结构体定义
  - `internal/storage/db.go:556` — UpsertCamera() 签名和实现
  - `internal/storage/db_test.go` — 现有 DB 测试模式
  - `internal/config/config.go:43` — ONVIFEndpoint 字段在 Config 中的定义

  **Acceptance Criteria**:
  - [ ] `go test ./internal/storage/... -v` → PASS
  - [ ] ALTER TABLE 迁移不破坏现有数据
  - [ ] UpsertCamera 保存 onvif_endpoint 和 profile_token
  - [ ] 向后兼容：旧数据新字段为空字符串

  **QA Scenarios**:
  ```
  Scenario: Migration preserves existing data
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/storage/... -run TestMigration -v
      2. Create DB with old schema, migrate, verify data intact
    Expected Result: Existing cameras preserved, new columns empty
    Evidence: .sisyphus/evidence/task-4-migration.txt

  Scenario: ONVIF fields CRUD works
    Tool: Bash (go test)
    Steps:
      1. UpsertCamera with onvif_endpoint and profile_token
      2. Query camera, assert fields match
    Expected Result: Fields persisted and retrieved correctly
    Evidence: .sisyphus/evidence/task-4-onvif-crud.txt
  ```

  **Commit**: YES
  - Message: `feat(storage): add ONVIF fields to cameras DB schema`
  - Files: `internal/storage/db.go`
  - Pre-commit: `go test ./internal/storage/...`

- [x] 5. Frontend Shared HLS.js Config Module

  **What to do**:
  - 创建 `web/src/lib/hls-config.ts` — 共享 HLS.js 配置工厂:
    - `createHlsConfig(cameraId: string): Hls.Config` 函数
    - RPi 优化参数:
      ```typescript
      maxBufferLength: 5,          // 5s 缓冲（默认 30s 太大）
      maxMaxBufferLength: 10,      // 最大 10s
      maxBufferSize: 10 * 1024 * 1024, // 10MB 上限
      backBufferLength: 2,         // 2s 回退缓冲
      enableWorker: false,         // RPi 浏览器兼容
      ```
    - Auth header 注入（复用现有 xhrSetup 模式）
    - 错误处理配置
  - 更新 Dashboard.svelte 和 LiveView.svelte 使用共享配置

  **Must NOT do**:
  - 不加自适应码率 (ABR)
  - 不加 WebSocket 支持
  - 不移除 enableWorker: false（RPi 兼容性需要）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 小型 TypeScript 模块，纯配置
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1-T4, T6)
  - **Blocks**: T8
  - **Blocked By**: None

  **References**:
  - `web/src/routes/Dashboard.svelte:194-205` — 现有 hls.js 配置（enableWorker:false, xhrSetup）
  - `web/src/routes/LiveView.svelte` — 另一处 hls.js 配置（需统一）
  - `web/src/lib/api.ts` — getAuthHeader() 用于 auth 注入
  - https://github.com/video-dev/hls.js/blob/master/docs/API.md — hls.js 配置文档

  **Acceptance Criteria**:
  - [ ] `cd web && npm run build` → success
  - [ ] Dashboard 和 LiveView 均使用共享配置
  - [ ] hls.js 配置含 maxBufferLength:5, maxMaxBufferLength:10

  **QA Scenarios**:
  ```
  Scenario: Shared config produces valid Hls instance
    Tool: Bash
    Steps:
      1. cd web && npm run build
      2. Assert no TypeScript errors
    Expected Result: Build success
    Evidence: .sisyphus/evidence/task-5-hls-config.txt
  ```

  **Commit**: YES
  - Message: `feat(web): add shared HLS.js config module`
  - Files: `web/src/lib/hls-config.ts, web/src/routes/Dashboard.svelte, web/src/routes/LiveView.svelte`
  - Pre-commit: `cd web && npm run build`

- [x] 6. ONVIF Library Validation + Import Setup

  **What to do**:
  - 验证 `github.com/0x524a/onvif-go v1.1.4` 的 discovery 包:
    - 写一个简单的 `_test.go` 验证 `discovery.Discover()` 可编译和调用
    - 验证 `onvif.NewClient()` 可编译和调用
    - 验证 PTZ service 可编译
  - 将 onvif-go 从 indirect 改为 direct dependency:
    - `go mod tidy` 清理依赖
  - 创建 `internal/onvif/onvifgo.go` 封装层:
    - 将 onvif-go 的 Device/Client 类型映射到项目接口
    - 方便后续 T9/T10/T11 使用
  - 验证无 CGO 依赖（`CGO_ENABLED=0 go build`）

  **Must NOT do**:
  - 不引入 C 依赖
  - 不修改现有 stub 代码（由后续任务替换）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 依赖验证 + 简单封装层
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1-T5)
  - **Blocks**: T9, T10, T11
  - **Blocked By**: None

  **References**:
  - `go.mod:23` — `github.com/0x524a/onvif-go v1.1.4 // indirect` 需改为 direct
  - `github.com/0x524a/onvif-go/discovery` — WS-Discovery API: `Discover(ctx, timeout)`, `DiscoverWithOptions()`
  - `github.com/0x524a/onvif-go` — Client API: `NewClient(endpoint, WithCredentials(user, pass))`
  - `internal/onvif/types.go` — 项目内部类型定义（需映射 onvif-go 类型）

  **Acceptance Criteria**:
  - [ ] `go build ./internal/onvif/...` → success (CGO_ENABLED=0)
  - [ ] onvif-go 在 go.mod 中为 direct dependency
  - [ ] 封装层编译通过
  - [ ] `go test ./internal/onvif/... -v` → PASS

  **QA Scenarios**:
  ```
  Scenario: Library compiles without CGO
    Tool: Bash
    Steps:
      1. CGO_ENABLED=0 rtk go build ./internal/onvif/...
      2. Assert success (no CGO errors)
    Expected Result: Build success
    Evidence: .sisyphus/evidence/task-6-cgo-check.txt

  Scenario: Dependency is direct in go.mod
    Tool: Bash
    Steps:
      1. grep 'onvif-go' go.mod
      2. Assert no '// indirect' suffix
    Expected Result: Direct dependency, no indirect tag
    Evidence: .sisyphus/evidence/task-6-deps.txt
  ```

  **Commit**: YES
  - Message: `feat(onvif): setup onvif-go library and wrapper layer`
  - Files: `go.mod, go.sum, internal/onvif/onvifgo.go`
  - Pre-commit: `CGO_ENABLED=0 go build ./...`

- [x] 7. HLS Multi-Stream Backend Support + Tests

  **What to do**:
  - TDD: 先写测试验证多路并发流的创建、管理和释放
  - 修改 `internal/hls/manager.go`:
    - 确保 4 路 OnHLSFrame callback 可同时活跃（每路 camera 有独立 recorder → 独立 HLS muxer）
    - 优化 StartStream/StopStream 的并发安全性（检查 writeLock 粒度）
    - 添加 `GetStreamStatus(cameraID) (active bool, viewerCount int)` 方法
  - 修改 `internal/api/handler.go` `handleHLSStream()`:
    - 确保并发请求不会死锁（多个 goroutine 同时调用 startStream）
    - 优化错误响应：返回清晰的 JSON 错误而非空响应
  - 添加 handler 测试：验证 4 路同时请求的处理

  **Must NOT do**:
  - 不改 recorder 的帧路径（OnHLSFrame callback 是非阻塞的）
  - 不增加 SegmentMaxSize
  - 不改 defaultMaxStreams

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 并发安全逻辑复杂，需深入理解锁机制和 goroutine 交互
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T8-T12)
  - **Parallel Group**: Wave 2
  - **Blocks**: T16
  - **Blocked By**: T1, T2

  **References**:
  - `internal/hls/manager.go:60-100` — startStream() 的锁和流管理逻辑
  - `internal/hls/manager.go:349-435` — writeLoop goroutine 和 OnHLSFrame callback
  - `internal/hls/manager.go:472-495` — idleWatchdog goroutine
  - `internal/api/handler.go:1175-1295` — handleHLSStream 完整流程
  - `internal/api/handler.go:TestHandler()` — 测试工厂方法

  **Acceptance Criteria**:
  - [ ] `go test ./internal/hls/... -run TestConcurrent -v` → PASS
  - [ ] 4 路流可同时创建和释放
  - [ ] GetStreamStatus() 返回正确状态
  - [ ] 并发 startStream 不死锁
  - [ ] `go test ./internal/api/... -v` → PASS

  **QA Scenarios**:
  ```
  Scenario: 4 concurrent streams start without deadlock
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/hls/... -run TestConcurrent -v
      2. Start 4 streams in parallel goroutines
      3. Assert all 4 created within 5s
    Expected Result: 4 streams active, no deadlock
    Evidence: .sisyphus/evidence/task-7-concurrent.txt

  Scenario: Stream status tracking works
    Tool: Bash (go test)
    Steps:
      1. Create 2 streams, check status of both
      2. Stop stream 1, verify status updated
    Expected Result: Active/inactive status correct
    Evidence: .sisyphus/evidence/task-7-status.txt
  ```

  **Commit**: YES
  - Message: `feat(hls): support concurrent multi-stream management`
  - Files: `internal/hls/manager.go, internal/api/handler.go`
  - Pre-commit: `go test ./internal/hls/... ./internal/api/...`

- [x] 8. Frontend Dashboard 4-HLS Grid Layout

  **What to do**:
  - 重构 `web/src/routes/Dashboard.svelte`:
    - 修改 `getCameraMode()`: 所有 H264/H265 摄像头返回 `'hls'` 而非仅 expanded
    - 移除 `'snapshot'` 模式作为默认，改为 HLS 失败时的回退
    - 实现 2×2 网格布局：每个格子一个 HLS 播放器
    - 使用 `web/src/lib/hls-config.ts` 的共享配置初始化所有播放器
    - 每个格子显示摄像头名称、状态指示器、全屏按钮
  - 生命周期管理:
    - 摄像头列表变化时，为新摄像头创建 HLS 实例
    - 摄像头移除时销毁对应 HLS 实例
    - `onDestroy()` 清理所有 HLS 实例
  - 展开模式保留：点击某个格子可放大为全屏（复用 LiveView）
  - ONVIF 摄像头也支持 HLS（如果发现 RTSP URL）

  **Must NOT do**:
  - 不重新设计 LiveView.svelte（保持单摄像头全屏不变）
  - 不加自适应码率
  - 不使用 WebSocket

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 前端 UI 布局重构，涉及响应式设计和播放器集成
  - **Skills**: [`/frontend-ui-ux`]
    - `/frontend-ui-ux`: 前端 UI 布局设计，2×2 网格 + 播放器交互

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T7, T9-T12)
  - **Parallel Group**: Wave 2
  - **Blocks**: T15
  - **Blocked By**: T5

  **References**:
  - `web/src/routes/Dashboard.svelte:113` — getCameraMode() 当前逻辑（expandedCameraId 判断）
  - `web/src/routes/Dashboard.svelte:35-36` — snapshot 刷新逻辑（3s 间隔）
  - `web/src/routes/Dashboard.svelte:362-367` — HLS 播放器初始化逻辑
  - `web/src/routes/Dashboard.svelte:194-248` — hls.js 创建/错误处理/销毁
  - `web/src/lib/hls-config.ts` — T5 创建的共享配置模块
  - `web/src/lib/api.ts:531-557` — ONVIF API 函数

  **Acceptance Criteria**:
  - [ ] `cd web && npm run build` → success
  - [ ] Dashboard 同时显示 4 个 HLS 播放器
  - [ ] 点击格子可放大为全屏
  - [ ] 摄像头移除时 HLS 实例正确销毁
  - [ ] 使用 hls-config.ts 共享配置

  **QA Scenarios**:
  ```
  Scenario: 4 HLS players render simultaneously
    Tool: Playwright
    Preconditions: NVR running with 4+ cameras configured
    Steps:
      1. Navigate to http://192.168.63.31:9090
      2. Login with admin/admin
      3. Assert 4 video elements present within 10s
      4. Assert each video has a playing source
    Expected Result: 4 video elements present and loading/playing
    Evidence: .sisyphus/evidence/task-8-4-hls-grid.png

  Scenario: Camera removal cleans up HLS instance
    Tool: Playwright
    Steps:
      1. Navigate to Dashboard with 4 cameras
      2. Remove one camera
      3. Assert only 3 video elements remain
    Expected Result: Correct cleanup, no memory leak
    Evidence: .sisyphus/evidence/task-8-cleanup.png
  ```

  **Commit**: YES
  - Message: `feat(web): dashboard 4-HLS simultaneous grid layout`
  - Files: `web/src/routes/Dashboard.svelte`
  - Pre-commit: `cd web && npm run build`

- [x] 9. ONVIF WS-Discovery Implementation + Tests

  **What to do**:
  - TDD: 先写测试（使用 MockDiscoverer）验证发现逻辑
  - 重写 `internal/onvif/discovery.go`:
    - 使用 `github.com/0x524a/onvif-go/discovery` 包:
      ```go
      devices, err := discovery.Discover(ctx, timeout)
      ```
    - 将 onvif-go 的 `discovery.Device` 映射到项目 `DiscoveredDevice` 类型
    - 提取设备端点、名称、位置
  - 添加单 IP probe 支持（解决跨子网问题）:
    - 新增 `ProbeDevice(ctx, ip, port, timeout) (*DiscoveredDevice, error)`
    - 直接向指定 IP:port 发送 ONVIF GetDeviceInformation
  - 保持 Discoverer 接口兼容（T3 定义的接口）

  **Must NOT do**:
  - 不阻塞主 goroutine（WS-Discovery 是 UDP 多播+超时）
  - 不要求网络多播可用（单 IP probe 作为回退）
  - 不实现 ONVIF Events/Analytics

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 网络协议实现，需处理 UDP 多播、超时、跨子网等边界情况
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T7-T8, T10-T12)
  - **Parallel Group**: Wave 2
  - **Blocks**: T13
  - **Blocked By**: T3, T6

  **References**:
  - `internal/onvif/discovery.go` — 现有 stub，需完全重写
  - `internal/onvif/types.go` — DiscoveredDevice 结构体定义
  - `internal/onvif/interfaces.go` — T3 创建的 Discoverer 接口
  - `github.com/0x524a/onvif-go/discovery` — onvif-go discovery 包 API:
    - `Discover(ctx, timeout) ([]*Device, error)`
    - `DiscoverWithOptions(ctx, timeout, opts)` — 支持网卡选择
    - `Device.GetDeviceEndpoint()`, `Device.GetName()`
  - `internal/onvif/discovery_test.go` — 现有测试模式
  - `internal/api/handler.go:957-993` — handleONVIFDiscover 调用 Discover()

  **Acceptance Criteria**:
  - [ ] `go test ./internal/onvif/... -run TestDiscover -v` → PASS
  - [ ] Discover() 调用 onvif-go discovery 包
  - [ ] ProbeDevice() 可探测指定 IP 的 ONVIF 设备
  - [ ] 无网络时返回空列表而非 panic
  - [ ] Discoverer 接口实现正确

  **QA Scenarios**:
  ```
  Scenario: Discovery returns device list format
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/onvif/... -run TestDiscover -v
      2. Mock discoverer returns 2 devices
      3. Assert correct mapping to DiscoveredDevice type
    Expected Result: Tests PASS with correct type mapping
    Evidence: .sisyphus/evidence/task-9-discovery.txt

  Scenario: ProbeDevice handles unreachable IP
    Tool: Bash (go test)
    Steps:
      1. Test ProbeDevice with unreachable IP (timeout)
      2. Assert returns error, not panic
    Expected Result: Graceful timeout error
    Evidence: .sisyphus/evidence/task-9-probe-error.txt
  ```

  **Commit**: YES
  - Message: `feat(onvif): implement WS-Discovery device discovery`
  - Files: `internal/onvif/discovery.go`
  - Pre-commit: `go test ./internal/onvif/...`

- [x] 10. ONVIF Client Operations + Tests

  **What to do**:
  - TDD: 先写测试（使用 MockDeviceClient）验证所有操作
  - 重写 `internal/onvif/client.go`:
    - `Connect(ctx) error` — 使用 onvif-go 初始化客户端，验证连接
    - `GetDeviceInformation(ctx) (*DeviceInfo, error)` — 设备厂商/型号/固件版本
    - `GetProfiles(ctx) ([]DeviceProfile, error)` — 获取 Media profiles（主流+子流）
    - `GetStreamURI(ctx, profileToken) (*StreamInfo, error)` — 获取 RTSP 流地址
    - `GetCapabilities(ctx) (*DeviceCapabilities, error)` — 检查 PTZ/流媒体支持
  - 处理 ONVIF 认证（WS-UsernameToken）:
    - 通过 onvif-go `WithCredentials(username, password)` 传入
  - 实现 DeviceClient 接口（T3 定义）

  **Must NOT do**:
  - 不创建长连接（每次请求独立）
  - 不实现 ONVIF Replay/Recording 服务
  - 不自动修改摄像头设置

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: ONVIF 协议集成，需处理多种响应格式和错误码
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T7-T9, T11-T12)
  - **Parallel Group**: Wave 2
  - **Blocks**: T12, T13
  - **Blocked By**: T3, T6

  **References**:
  - `internal/onvif/client.go` — 现有 stub（Connect/GetDeviceInformation/GetProfiles/GetStreamURI/GetCapabilities）
  - `internal/onvif/types.go` — DeviceInfo, DeviceProfile, StreamInfo, DeviceCapabilities 类型定义
  - `internal/onvif/interfaces.go` — T3 创建的 DeviceClient 接口
  - `github.com/0x524a/onvif-go` — onvif-go 客户端 API:
    - `onvif.NewClient(endpoint, onvif.WithCredentials(user, pass))`
    - client.Device.GetDeviceInformation()
    - client.Media.GetProfiles()
    - client.Media.GetStreamUri()
  - `internal/onvif/client_test.go` — 现有测试模式

  **Acceptance Criteria**:
  - [ ] `go test ./internal/onvif/... -run TestClient -v` → PASS
  - [ ] Connect() 使用 onvif-go 初始化客户端
  - [ ] GetProfiles() 返回主流+子流 profile
  - [ ] GetStreamURI() 返回 RTSP URL
  - [ ] 认证失败返回清晰错误

  **QA Scenarios**:
  ```
  Scenario: Client operations with mock server
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/onvif/... -run TestClient -v
      2. MockDeviceClient returns profiles with H.264 and H.265
      3. GetStreamURI returns rtsp:// URLs
    Expected Result: All client methods work with mock
    Evidence: .sisyphus/evidence/task-10-client.txt

  Scenario: Auth failure handled correctly
    Tool: Bash (go test)
    Steps:
      1. Test Connect with wrong credentials
      2. Assert clear error message returned
    Expected Result: Error with auth failure details
    Evidence: .sisyphus/evidence/task-10-auth-error.txt
  ```

  **Commit**: YES
  - Message: `feat(onvif): implement ONVIF client operations`
  - Files: `internal/onvif/client.go`
  - Pre-commit: `go test ./internal/onvif/...`

- [x] 11. ONVIF PTZ Operations + Tests

  **What to do**:
  - TDD: 先写测试（使用 MockPTZController）验证所有 PTZ 操作
  - 重写 `internal/onvif/ptz.go`:
    - `ContinuousMove(ctx, velocity PTZVector) error` — 持续移动（上下左右/缩放）
    - `AbsoluteMove(ctx, position PTZVector) error` — 绝对定位
    - `RelativeMove(ctx, displacement PTZVector) error` — 相对移动
    - `Stop(ctx, stopPanTilt, stopZoom bool) error` — 停止移动
    - `GetStatus(ctx) (*PTZVector, bool, error)` — 获取当前位置和移动状态
  - PTZ 并发控制：使用 sync.Mutex 防止同时发送冲突命令
  - 实现 PTZController 接口（T3 定义）
  - 需要关联 ONVIF Client 和 PTZ token

  **Must NOT do**:
  - 不加 WebSocket 实时 PTZ
  - 不实现 PTZ 预置位（Preset）管理（超出范围）
  - 不修改摄像头 PTZ 配置

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: ONVIF PTZ 协议实现，需处理空间向量和并发控制
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T7-T10, T12)
  - **Parallel Group**: Wave 2
  - **Blocks**: T13
  - **Blocked By**: T3, T6

  **References**:
  - `internal/onvif/ptz.go` — 现有 stub，需完全重写
  - `internal/onvif/types.go` — PTZVector 结构体定义（Pan, Tilt, Zoom float64）
  - `internal/onvif/interfaces.go` — T3 创建的 PTZController 接口
  - `github.com/0x524a/onvif-go` — PTZ 服务 API
  - `internal/api/handler.go:1011-1058` — PTZ handler 端点定义
  - `internal/onvif/ptz_test.go` — 现有测试模式
  - `web/src/routes/Cameras.svelte` — PtzControl 组件使用方式

  **Acceptance Criteria**:
  - [ ] `go test ./internal/onvif/... -run TestPTZ -v` → PASS
  - [ ] ContinuousMove/Stop 配对使用正常
  - [ ] 并发 PTZ 命令由 Mutex 序列化
  - [ ] GetStatus 返回位置和移动状态
  - [ ] PTZController 接口实现正确

  **QA Scenarios**:
  ```
  Scenario: PTZ continuous move + stop
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/onvif/... -run TestPTZ -v
      2. Send ContinuousMove(pan=0.5, tilt=0.0)
      3. Send Stop()
      4. Assert no error in sequence
    Expected Result: Move and stop succeed
    Evidence: .sisyphus/evidence/task-11-ptz.txt

  Scenario: Concurrent PTZ commands serialized
    Tool: Bash (go test)
    Steps:
      1. Send 10 PTZ commands concurrently
      2. Verify all complete without race condition
    Expected Result: No race, all commands processed
    Evidence: .sisyphus/evidence/task-11-ptz-concurrent.txt
  ```

  **Commit**: YES
  - Message: `feat(onvif): implement PTZ control operations`
  - Files: `internal/onvif/ptz.go`
  - Pre-commit: `go test ./internal/onvif/...`

- [x] 12. ONVIFRecorder Implementation + Tests

  **What to do**:
  - TDD: 先写测试验证 ONVIFRecorder 的完整生命周期
  - 创建 `internal/recorder/onvif.go`:
    - 实现 `model.Recorder` 接口:
      - `Start() error` — 通过 ONVIF GetStreamURI 获取 RTSP URL，根据编码类型创建内部 RTSP 连接
      - `Stop() error` — 停止 RTSP 连接和录制
      - `Status() RecorderStatus` — 返回录制状态
    - 内部流程:
      1. 使用 ONVIF DeviceClient 获取 Stream URI（首选 H.264，备选 H.265）
      2. 根据 URI 中的编码类型，内部创建 RTSP 连接（复用 H264Recorder/H265Recorder 的连接逻辑）
      3. 录制到 MP4 段文件（复用现有 segment 生命周期）
    - ONVIF 特殊处理:
      - 自动重连：RTSP 断开后重新通过 ONVIF 获取 URI
      - Profile 切换：子流不可用时回退到主流
      - 凭据传递：ONVIF 认证 → RTSP 连接
  - 注册到 `camera/manager.go` 的 recorder 工厂

  **Must NOT do**:
  - 不实现新的 RTSP 解码逻辑（复用现有 H264/H265 recorder 的 RTP 解码）
  - 不增加 C 依赖
  - 不破坏现有 recorder 接口

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 新 recorder 类型实现，需理解 RTSP/RTP/MP4 管线 + ONVIF 集成
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T7-T11)
  - **Parallel Group**: Wave 2
  - **Blocks**: T13
  - **Blocked By**: T3, T4, T10

  **References**:
  - `internal/model/types.go` — Recorder 接口定义（Start/Stop/Status）
  - `internal/recorder/h264.go` — H264Recorder 结构，RTSP 连接 + RTP 解码 + MP4 录制完整流程
  - `internal/recorder/h265.go` — H265Recorder 结构，HEVC 版本
  - `internal/recorder/http_jpeg.go` — HTTPJPEGRecorder 结构，另一种 recorder 模式参考
  - `internal/onvif/interfaces.go` — T3 创建的 DeviceClient 接口（GetStreamURI）
  - `internal/camera/manager.go:85-130` — createRecorder() 工厂方法，需添加 onvif case
  - `internal/camera/manager.go:105-106` — 现有 onvif case（返回 nil，需替换）
  - `internal/storage/manager.go` — Segment 生命周期（CreateSegment/CloseSegment）

  **Acceptance Criteria**:
  - [ ] `go test ./internal/recorder/... -run TestONVIF -v` → PASS
  - [ ] ONVIFRecorder 实现 model.Recorder 接口
  - [ ] Start() 通过 ONVIF 获取 RTSP URI 并开始录制
  - [ ] Stop() 正确释放资源
  - [ ] RTSP 断开时自动重连

  **QA Scenarios**:
  ```
  Scenario: ONVIFRecorder lifecycle with mock ONVIF client
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/recorder/... -run TestONVIF -v
      2. MockDeviceClient returns rtsp:// mock URI
      3. Start → verify status recording
      4. Stop → verify status stopped
    Expected Result: Full lifecycle works
    Evidence: .sisyphus/evidence/task-12-onvif-recorder.txt

  Scenario: Auto-reconnect on RTSP disconnect
    Tool: Bash (go test)
    Steps:
      1. Start recorder, simulate RTSP disconnect
      2. Verify reconnection attempt via ONVIF GetStreamURI
      3. Assert recording resumes
    Expected Result: Reconnection succeeds
    Evidence: .sisyphus/evidence/task-12-reconnect.txt
  ```

  **Commit**: YES
  - Message: `feat(recorder): implement ONVIFRecorder`
  - Files: `internal/recorder/onvif.go`
  - Pre-commit: `go test ./internal/recorder/...`
  - Files: `internal/recorder/onvif.go`
  - Pre-commit: `go test ./internal/recorder/...`

- [x] 13. ONVIF Camera Manager Integration + API Handlers + Tests

  **What to do**:
  - 修改 `internal/camera/manager.go`:
    - 替换 `case string(model.ProtoONVIF): return nil` (line 105-106)
    - 创建 ONVIFRecorder（调用 T12 的构造函数）
    - 添加 ONVIF endpoint 和 profile_token 的配置处理
    - 确保 ONVIF 摄像头添加后自动开始录制
  - 重写 `internal/api/handler.go` 中的 ONVIF 端点:
    - `handleONVIFDiscover` (line 957-993) — 调用真实 Discover() + ProbeDevice()
    - `handleONVIFDeviceDetail` (line 995) — 调用真实 DeviceClient
    - `handleONVIFCameraProfiles` — 调用真实 GetProfiles()
  - 重写 PTZ handler:
    - `handlePTZMove` (line 1011) — 调用真实 PTZ 命令
    - `handlePTZStop` (line 1036) — 调用真实 PTZStop
    - `handlePTZStatus` (line 1050) — 调用真实 GetStatus
  - 确保 PTZ 只对 ONVIF 摄像头可用（非 ONVIF 返回 400）
  - 添加 API handler 测试

  **Must NOT do**:
  - 不重新设计 PtzControl.svelte 组件
  - 不实现 ONVIF Events/Analytics
  - 不修改非 ONVIF 摄像头的行为

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 多文件集成，将已实现的 ONVIF 组件接线到 camera manager 和 API
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (sequential start)
  - **Blocks**: T14
  - **Blocked By**: T9, T10, T11, T12

  **References**:
  - `internal/camera/manager.go:85-130` — createRecorder() 工厂方法，需添加 onvif case
  - `internal/camera/manager.go:105-106` — 现有 `case ProtoONVIF: return nil` 需替换
  - `internal/camera/manager.go:216-217` — ONVIF 特殊处理日志
  - `internal/api/handler.go:957-993` — handleONVIFDiscover
  - `internal/api/handler.go:995-1010` — handleONVIFDeviceDetail (currently returns 501)
  - `internal/api/handler.go:1011-1058` — PTZ handlers (currently stubs with TODO)
  - `internal/api/handler.go:TestHandler()` — 测试工厂
  - `internal/onvif/interfaces.go` — Discoverer/DeviceClient/PTZController 接口

  **Acceptance Criteria**:
  - [ ] `go test ./internal/camera/... ./internal/api/... -v` → PASS
  - [ ] ONVIF 摄像头添加后 recorder 非空且自动开始录制
  - [ ] `POST /api/onvif/discover` 返回设备列表
  - [ ] `POST /api/cameras/{id}/ptz/move` 发送真实 ONVIF 命令
  - [ ] 非 ONVIF 摄像头 PTZ 返回 400

  **QA Scenarios**:
  ```
  Scenario: ONVIF camera creation starts recording
    Tool: Bash (go test)
    Steps:
      1. rtk go test ./internal/camera/... -run TestONVIFCamera -v
      2. Create ONVIF camera with mock client
      3. Assert recorder starts, status is recording
    Expected Result: Camera records via ONVIF
    Evidence: .sisyphus/evidence/task-13-onvif-camera.txt

  Scenario: PTZ on non-ONVIF camera returns 400
    Tool: Bash (curl)
    Steps:
      1. curl -s -u admin:admin -X POST http://localhost:9090/api/cameras/{non-onvif-id}/ptz/move -d '{"mode":"continuous","pan":0.5}'
      2. Assert HTTP 400
    Expected Result: 400 Bad Request with error message
    Evidence: .sisyphus/evidence/task-13-ptz-non-onvif.txt

  Scenario: Discovery endpoint returns device list
    Tool: Bash (curl)
    Steps:
      1. curl -s -u admin:admin -X POST http://localhost:9090/api/onvif/discover -d '{"timeout": 5}'
      2. Assert JSON response with devices array
    Expected Result: {"devices": [...]} (empty OK if no ONVIF devices)
    Evidence: .sisyphus/evidence/task-13-discover-api.txt
  ```

  **Commit**: YES
  - Message: `feat(onvif): integrate ONVIF into camera manager and API`
  - Files: `internal/camera/manager.go, internal/api/handler.go`
  - Pre-commit: `go test ./internal/camera/... ./internal/api/...`

- [x] 14. ONVIF Frontend Scan → Add → PTZ Flow

  **What to do**:
  - 修改 `web/src/routes/Cameras.svelte`:
    - 验证 scanONVIF() 函数与新后端 API 的兼容性
    - 增强 ONVIF 发现 UI:
      - 显示设备详细信息（厂商、型号、固件）
      - 支持 Profile 选择（主流/子流）
      - 添加 ONVIF 认证输入（用户名/密码）
    - 添加发现结果的“单 IP 探测”入口
  - 添加 ONVIF 摄像头创建表单:
    - ONVIF endpoint 字段
    - 认证凭据（用户名/密码）
    - Profile 选择下拉
  - 确保 PtzControl 组件连接到真实后端:
    - PTZ 按钮调用真实 API
    - PTZ 状态显示实时位置

  **Must NOT do**:
  - 不重新设计 PtzControl.svelte 组件
  - 不加 WebSocket PTZ
  - 不实现 PTZ 预置位管理

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 前端 UI 流程串联，涉及表单设计和交互
  - **Skills**: [`/frontend-ui-ux`]
    - `/frontend-ui-ux`: 前端 UI 交互设计，ONVIF 扫描和添加流程

  **Parallelization**:
  - **Can Run In Parallel**: NO (needs T13 backend ready)
  - **Parallel Group**: Wave 3 (after T13)
  - **Blocks**: T16
  - **Blocked By**: T13

  **References**:
  - `web/src/routes/Cameras.svelte:263-300` — scanONVIF() 和 addDiscoveredDevice() 函数
  - `web/src/routes/Cameras.svelte:314-416` — ONVIF 发现面板 UI
  - `web/src/lib/api.ts:531-557` — ONVIF API 函数（discoverONVIFDevices, addONVIFCamera 等）
  - `web/src/lib/i18n/en.json` — 英文翻译（含 ONVIF 相关）
  - `web/src/lib/i18n/zh.json` — 中文翻译
  - `internal/api/handler.go:957-1058` — T13 更新后的 API 端点

  **Acceptance Criteria**:
  - [ ] `cd web && npm run build` → success
  - [ ] 扫描按钮触发真实 WS-Discovery
  - [ ] 发现设备可添加为摄像头
  - [ ] ONVIF 认证字段可输入
  - [ ] PTZ 控件连接真实 API

  **QA Scenarios**:
  ```
  Scenario: ONVIF scan and device display
    Tool: Playwright
    Steps:
      1. Navigate to Cameras page
      2. Click 'Scan ONVIF Devices' button
      3. Wait for scan result (timeout 6s)
      4. Assert device list or 'no devices found' message
    Expected Result: Scan completes, results displayed
    Evidence: .sisyphus/evidence/task-14-scan-ui.png

  Scenario: PTZ control buttons work
    Tool: Playwright
    Preconditions: ONVIF camera added with PTZ support
    Steps:
      1. Navigate to Cameras page
      2. Find ONVIF camera, click PTZ control
      3. Click direction button (right)
      4. Assert API call made to /ptz/move
    Expected Result: PTZ command sent
    Evidence: .sisyphus/evidence/task-14-ptz-ui.png
  ```

  **Commit**: YES
  - Message: `feat(web): ONVIF scan→add camera→PTZ frontend flow`
  - Files: `web/src/routes/Cameras.svelte, web/src/lib/api.ts`
  - Pre-commit: `cd web && npm run build`

- [x] 15. HLS Frontend Error Recovery + Graceful Degradation

  **What to do**:
  - 在 `Dashboard.svelte` 中添加 HLS 错误恢复逻辑:
    - ErrMaxStreamsReached (HTTP 429): 显示提示“已达最大流数”，回退到快照模式
    - 网络错误: 自动重试 3 次（指数退避 2s/4s/8s）
    - 媒体错误: `hls.recoverMediaError()` 恢复
    - 致命错误: 回退到 JPEG 快照 + 错误提示
  - 添加流状态指示器:
    - 🟢 正在播放
    - 🟡 缓冲中
    - 🔴 错误（显示错误原因）
    - 📷 快照模式（HLS 不可用）
  - 确保 LiveView.svelte 兼容:
    - 单摄像头全屏模式仍然正常工作
    - 错误处理与 Dashboard 共享
  - 添加 `web/src/lib/hls-errors.ts` 共享错误处理模块

  **Must NOT do**:
  - 不重新设计 LiveView 的布局
  - 不加 WebSocket 实时通知
  - 不修改后端代码（纯前端变更）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 错误处理逻辑涉及多种边界情况，需要健壮的状态管理
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T13, T14)
  - **Parallel Group**: Wave 3
  - **Blocks**: T16
  - **Blocked By**: T8

  **References**:
  - `web/src/routes/Dashboard.svelte:218-248` — 现有 HLS 错误处理（MANIFEST_ERROR/MEDIA_ERROR 处理）
  - `web/src/routes/Dashboard.svelte:331-339` — destroyPlayer() 逻辑
  - `web/src/routes/LiveView.svelte` — 单摄像头错误处理（需统一）
  - `web/src/lib/hls-config.ts` — T5 创建的共享配置模块
  - https://github.com/video-dev/hls.js/blob/master/docs/API.md — hls.js 错误类型文档

  **Acceptance Criteria**:
  - [ ] `cd web && npm run build` → success
  - [ ] 429 错误自动回退到快照模式
  - [ ] 致命错误 3 次重试后显示快照
  - [ ] LiveView 错误处理与 Dashboard 共享
  - [ ] 流状态指示器显示正确

  **QA Scenarios**:
  ```
  Scenario: HLS 429 falls back to snapshot
    Tool: Playwright
    Steps:
      1. Start 4 HLS streams (all slots used)
      2. Navigate to Dashboard
      3. Add 5th camera → assert snapshot fallback with indicator
    Expected Result: 5th camera shows snapshot with degraded indicator
    Evidence: .sisyphus/evidence/task-15-429-fallback.png

  Scenario: Fatal error retries then shows snapshot
    Tool: Playwright
    Steps:
      1. Simulate HLS fatal error (disconnect camera RTSP)
      2. Wait for retry sequence (3 retries)
      3. Assert fallback to snapshot mode
    Expected Result: Retries attempted, then snapshot shown
    Evidence: .sisyphus/evidence/task-15-fatal-retry.png
  ```

  **Commit**: YES
  - Message: `fix(web): HLS error recovery and graceful degradation`
  - Files: `web/src/routes/Dashboard.svelte, web/src/routes/LiveView.svelte, web/src/lib/hls-errors.ts`
  - Pre-commit: `cd web && npm run build`

- [x] 16. HLS + ONVIF End-to-End Integration Tests

  **What to do**:
  - 添加集成测试到 `tests/integration_test.go`:
    - TestMultiStreamHLS: 4 路同时请求 HLS 流，验证全部成功
    - TestONVIFDiscoveryWithMock: 使用 mock 验证完整发现流程
    - TestONVIFCameraCreation: 发现→添加→验证录制开始
    - TestPTZLifecycle: PTZ 移动→停止→状态检查
    - TestHLSWithONVIFCamera: ONVIF 摄像头添加后 HLS 预览可用
  - 添加前端 E2E 测试到 `e2e-tests/`:
    - `dashboard-4-hls.spec.ts`: 四路 HLS 同时播放
    - `onvif-scan.spec.ts`: ONVIF 扫描和添加流程
  - 在 RPi 上执行实际测试（部署后）:
    - 内存验证：4 路 HLS + 5 录制器 RSS ≤ 600MB
    - 性能验证：4 路同时启动 ≤ 10s

  **Must NOT do**:
  - 不要求物理 ONVIF 设备跑测试（mock 所有 ONVIF 操作）
  - 不破坏现有集成测试

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 跨模块集成测试设计，需理解 HLS + ONVIF + Camera 全链路
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (需要 T7/T13/T14/T15 全部完成)
  - **Parallel Group**: Wave 3 (final)
  - **Blocks**: F1-F4
  - **Blocked By**: T7, T13, T14, T15

  **References**:
  - `tests/integration_test.go` — 现有 7 个集成测试场景
  - `e2e-tests/tests/` — 现有 Playwright E2E 测试
  - `e2e-tests/playwright.config.ts` — Playwright 配置
  - `internal/api/handler.go:TestHandler()` — 测试工厂（共享 handler 实例）

  **Acceptance Criteria**:
  - [ ] `go test ./tests/... -v` → PASS
  - [ ] `cd e2e-tests && npm test` → PASS
  - [ ] 4 路 HLS 内存 ≤ 600MB
  - [ ] 4 路启动时间 ≤ 10s

  **QA Scenarios**:
  ```
  Scenario: 4-stream HLS memory check on RPi
    Tool: Bash (SSH)
    Preconditions: NVR deployed on RPi with 4+ cameras
    Steps:
      1. ssh mickey@192.168.63.31 'sudo systemctl restart mibee-nvr'
      2. Start 4 HLS streams via curl
      3. sleep 30 && ssh mickey@192.168.63.31 "ps aux | grep mibee-nvr | grep -v grep | awk '{print \$6}'"
      4. Assert RSS ≤ 600000 (600MB)
    Expected Result: RSS under 600MB limit
    Evidence: .sisyphus/evidence/task-16-memory.txt

  Scenario: All integration tests pass
    Tool: Bash
    Steps:
      1. rtk go test ./tests/... -v
      2. Assert all new tests PASS
    Expected Result: 0 failures
    Evidence: .sisyphus/evidence/task-16-integration.txt
  ```

  **Commit**: YES
  - Message: `test: HLS + ONVIF end-to-end integration tests`
  - Files: `tests/integration_test.go, e2e-tests/tests/dashboard-4-hls.spec.ts, e2e-tests/tests/onvif-scan.spec.ts`
  - Pre-commit: `go test ./... && cd e2e-tests && npm test`
## Final Verification Wave

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `rtk go vet ./...` + `rtk go test ./...` + check `web/` build. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high` (+ `playwright` skill if UI)
  Start from clean state (restart service on RPi). Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration (HLS + ONVIF together). Test edge cases: empty state, invalid input, rapid actions. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff (git log/diff). Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

| Wave | Commit Message | Key Files | Pre-commit |
|------|---------------|-----------|------------|
| 1 | `perf(hls): optimize buffer sizes for RPi 3B multi-stream` | manager.go | `go test ./internal/hls/...` |
| 1 | `fix(hls): return ErrMaxStreamsReached instead of silent eviction` | manager.go, errors.go | `go test ./internal/hls/...` |
| 1 | `feat(onvif): add testable interfaces and mock types` | onvif/interfaces.go, onvif/mocks.go | `go test ./internal/onvif/...` |
| 1 | `feat(storage): add ONVIF fields to cameras DB schema` | db.go | `go test ./internal/storage/...` |
| 1 | `feat(web): add shared HLS.js config module` | hls-config.ts | `cd web && npm run build` |
| 2 | `feat(hls): support concurrent multi-stream management` | manager.go, handler.go | `go test ./internal/hls/... ./internal/api/...` |
| 2 | `feat(web): dashboard 4-HLS simultaneous grid layout` | Dashboard.svelte | `cd web && npm run build` |
| 2 | `feat(onvif): implement WS-Discovery device discovery` | discovery.go | `go test ./internal/onvif/...` |
| 2 | `feat(onvif): implement ONVIF client operations` | client.go | `go test ./internal/onvif/...` |
| 2 | `feat(onvif): implement PTZ control operations` | ptz.go | `go test ./internal/onvif/...` |
| 2 | `feat(recorder): implement ONVIFRecorder` | onvif.go | `go test ./internal/recorder/...` |
| 3 | `feat(onvif): integrate ONVIF into camera manager and API` | manager.go, handler.go | `go test ./internal/camera/... ./internal/api/...` |
| 3 | `feat(web): ONVIF scan→add camera→PTZ frontend flow` | Cameras.svelte, api.ts | `cd web && npm run build` |
| 3 | `fix(web): HLS error recovery and graceful degradation` | Dashboard.svelte | `cd web && npm run build` |
| 3 | `test: HLS + ONVIF end-to-end integration tests` | integration_test.go | `go test ./...` |

---

## Success Criteria

### Verification Commands
```bash
# Go tests
rtk go test ./internal/hls/... -v        # Expected: All PASS
rtk go test ./internal/onvif/... -v       # Expected: All PASS
rtk go test ./internal/recorder/... -v    # Expected: All PASS
rtk go test ./internal/camera/... -v      # Expected: All PASS
rtk go test ./internal/api/... -v         # Expected: All PASS
rtk go vet ./...                          # Expected: no warnings

# Frontend build
cd web && npm run build                   # Expected: success

# Memory check on RPi
ssh mickey@192.168.63.31 "ps aux | grep mibee-nvr | grep -v grep | awk '{print \$6}'"
# Expected: RSS ≤ 600000 (KB, = 600MB)

# HLS stream check (4 concurrent)
curl -s -u admin:admin http://192.168.63.31:9090/api/cameras | jq -r '.[].id' | head -4 | \
  xargs -P4 -I{} curl -s -u admin:admin "http://192.168.63.31:9090/api/cameras/{}/stream/index.m3u8" | head -1
# Expected: #EXTM3U for each

# ONVIF discovery
curl -s -u admin:admin -X POST http://192.168.63.31:9090/api/onvif/discover -d '{"timeout": 5}'
# Expected: {"devices": [...]} (empty array OK if no ONVIF devices)
```

### Final Checklist
- [x] All "Must Have" present
- [x] All "Must NOT Have" absent
- [x] All tests pass (`rtk go test ./...`)
- [x] Frontend builds clean (`cd web && npm run build`)
- [x] Process RSS ≤ 600MB on RPi with 4 HLS streams active
- [x] Dashboard shows 4 simultaneous HLS players
- [x] ONVIF scan returns device list (or empty array gracefully)
- [x] ONVIF camera creation starts recording
- [x] PTZ commands reach ONVIF device
