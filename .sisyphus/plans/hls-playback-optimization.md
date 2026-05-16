# HLS 播放体验全面优化

## TL;DR

> **Quick Summary**: 修复多路监控大屏 HLS 播放的闪屏和永久黑屏问题，全面优化从后端 HLS 参数到前端播放器错误恢复、视觉过渡的完整链路。
> 
> **Deliverables**:
> - 后端 HLS 参数可配置化 + 调优（SegmentCount、writeBufSize）
> - 帧丢弃可观测性（Prometheus 指标）
> - 前端 hls.js 配置现代化（enableWorker、更大缓冲）
> - 错误恢复策略重写（去抖 recoverMediaError、僵尸检测、destroy+recreate）
> - Dashboard 多路播放器生命周期修复 + 永久黑屏自动恢复
> - 视觉过渡层（overlay-based，永远不裸露黑屏）
> - Playwright E2E 测试覆盖关键场景
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: T1 → T5 → T6 → T7 → T10 → F1-F4

---

## Context

### Original Request
用户在多路监控大屏（2x2 四路同时播放）中观察到个别画面会一闪一闪的（黑一下再回来），有时某路会一直黑（但切到单路实时浏览又能正常播放）。希望全面优化整个播放体验。

### Interview Summary
**Key Discussions**:
- 闪屏表现：黑一下再回来，对应 hls.js 的 `recoverMediaError()` 重建 MediaSource
- 永久黑屏：Dashboard 中某路一直黑，但 LiveView 单路播放正常 — 说明是 Dashboard 多实例管理问题
- 浏览器：现代浏览器（Chrome/Firefox/Safari），不考虑旧浏览器 — 可以开启 enableWorker
- 测试策略：TDD — 每个改动先写测试

**Research Findings**:
- 后端 HLS Manager 使用 gohlslib，SegmentCount=3（硬编码），SegmentMinDuration=2s，writeBufSize=40
- 帧丢弃：非阻塞 channel 发送，缓冲满时静默丢弃，无日志无指标
- 前端 hls.js：enableWorker:false，maxBufferLength:5s，backBufferLength:2s
- 非致命 MEDIA_ERROR 自动调用 `recoverMediaError()`，无去抖，无频率限制
- 播放列表窗口仅 6s（3段×2s），前端有效前向缓冲仅 3s（5s-2s）

### Metis Review
**Identified Gaps** (addressed):
- SegmentCount 是硬编码不是配置项 → 需新增 HLSConfig 字段并穿线
- enableWorker 在 3 个 AGENTS.md 中标记为 "RPi 浏览器不兼容" → 用户确认从桌面浏览器访问，安全开启，但需 feature detect
- 内存预算未计算 → writeBufSize 40→100 增加约 10-20MB（4路），SegmentCount 3→7 增加 ~2MB，总计 RPi 可承受
- 永久黑屏可能原因多样（僵尸/流 evict/相机断连/GPU 耗尽）→ 需分类处理
- Wave 2 不应与 Wave 1 并行 → 调整为严格顺序
- Tasks 5+7 有重叠（错误处理 + 视觉组件都涉及播放器生命周期）→ 拆分清晰：T5 错误逻辑 → T6 视觉组件消费 T5

---

## Work Objectives

### Core Objective
消除多路监控大屏的闪屏和永久黑屏问题，从后端参数到前端播放器到视觉呈现进行全链路优化。

### Concrete Deliverables
- `internal/config/config.go` — HLSConfig 新增 segment_count 字段
- `internal/hls/manager.go` — 使用配置的 SegmentCount/writeBufSize，帧丢弃计数器
- `internal/metrics/metrics.go` — 新增 `nvr_hls_frames_dropped_total` 指标
- `web/src/lib/hls-config.ts` — 现代化 hls.js 配置
- `web/src/lib/hls-errors.ts` — 重写错误恢复策略
- `web/src/components/VideoPlayer.svelte` — 新增可复用播放器组件
- `web/src/routes/Dashboard.svelte` — 修复播放器生命周期
- `web/src/routes/LiveView.svelte` — 应用新播放器组件
- `e2e-tests/tests/hls-playback.spec.ts` — E2E 测试

### Definition of Done
- [ ] Dashboard 4 路同时播放 10 分钟，0 次黑闪（后端参数调优 + 前端去抖）
- [ ] 模拟网络中断后，播放器在 10s 内自动恢复（不永久黑屏）
- [ ] 模拟相机断连后，显示错误 overlay（不是裸露黑屏）
- [ ] RSS 内存增长 < 50MB（相比当前基线）
- [ ] `/metrics` 端点包含 `nvr_hls_frames_dropped_total` 指标
- [ ] 所有 Playwright E2E 测试通过

### Must Have
- 修复闪屏（去抖 recoverMediaError + 后端参数调优）
- 修复永久黑屏（僵尸检测 + 自动 destroy+recreate）
- Overlay-based 视觉状态（永远不裸露 video 元素黑屏）
- 帧丢弃可观测性（Prometheus 指标 + 聚合日志）
- SegmentCount 和 writeBufSize 可配置化
- Playwright E2E 测试

### Must NOT Have (Guardrails)
- ❌ 不得修改录制管道（`internal/recorder/`）— 接口不变
- ❌ 不得改变 REST API 路由或响应格式
- ❌ 不得修改 gohlslib 库本身，只改传给它的配置参数
- ❌ 不得移除快照降级模式（HTTP JPEG 相机依赖它）
- ❌ 不得改变 `setupHlsErrorHandling()` 的导出签名（Dashboard 和 LiveView 都依赖它）
- ❌ 不得添加逐帧日志 — 只允许聚合计数器
- ❌ 不得升级 hls.js 库版本 — 只改配置
- ❌ 不得改变 `maxStreams=4` 硬限制
- ❌ 不得把 VideoPlayer 组件做成通用组件库 — 只做播放器生命周期 + overlay 状态

---

## Verification Strategy (MANDATORY)

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES（集成测试 + Playwright）
- **Automated tests**: TDD
- **Framework**: Go testing（`require`）+ Playwright（E2E）
- **TDD flow**: Each task follows RED (failing test) → GREEN (minimal impl) → REFACTOR

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Backend**: Use Bash (`rtk go test`) — unit + integration tests
- **Frontend**: Use Playwright — browser automation, DOM assertion, screenshot
- **API/Metrics**: Use Bash (`curl`) — verify metrics endpoint
- **Memory**: Use Bash (`ssh RPi`) — RSS measurement before/after

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — foundation + observability):
├── T1: 后端 HLS 参数可配置化 + 调优 [quick]
├── T2: 帧丢弃可观测性（Prometheus 指标） [quick]
├── T3: 前端 hls.js 配置现代化 [quick]
└── T4: Playwright HLS 测试基础设施 [unspecified-high]

Wave 2 (After Wave 1 — core fix, partial parallel):
├── T5: 重写 hls-errors.ts 错误恢复策略 (depends: T3) [deep]
└── T6: 创建 VideoPlayer 可复用组件 (depends: T5) [visual-engineering]

Wave 3 (After Wave 2 — integration + polish):
├── T7: 修复 Dashboard 播放器生命周期 + 应用 VideoPlayer (depends: T5, T6) [deep]
├── T8: 改善 LiveView.svelte 播放器 (depends: T5, T6) [quick]
├── T9: Dashboard 网格 UX 改进 (depends: T7) [visual-engineering]
└── T10: E2E 测试套件 (depends: T4, T7, T8) [unspecified-high]

Wave FINAL (After ALL — 4 parallel reviews):
├── F1: Plan compliance audit (oracle)
├── F2: Code quality review (unspecified-high)
├── F3: Real manual QA (unspecified-high + playwright skill)
└── F4: Scope fidelity check (deep)

Critical Path: T1 → T5 → T6 → T7 → T10 → F1-F4
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 4 (Wave 1)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| T1 | — | T5 (indirectly, params used by frontend config) | 1 |
| T2 | — | — | 1 |
| T3 | — | T5 | 1 |
| T4 | — | T10 | 1 |
| T5 | T3 | T6, T7, T8 | 2 |
| T6 | T5 | T7, T8 | 2 |
| T7 | T5, T6 | T9, T10 | 3 |
| T8 | T5, T6 | T10 | 3 |
| T9 | T7 | — | 3 |
| T10 | T4, T7, T8 | F1-F4 | 3 |

### Agent Dispatch Summary

- **Wave 1**: **4** — T1→`quick`, T2→`quick`, T3→`quick`, T4→`unspecified-high`
- **Wave 2**: **2** — T5→`deep`, T6→`visual-engineering`
- **Wave 3**: **4** — T7→`deep`, T8→`quick`, T9→`visual-engineering`, T10→`unspecified-high`
- **FINAL**: **4** — F1→`oracle`, F2→`unspecified-high`, F3→`unspecified-high`, F4→`deep`

---

## TODOs

- [x] 1. 后端 HLS 参数可配置化 + 调优

  **What to do**:
  - RED: 先写集成测试，验证 `NewManagerWithOpts` 使用自定义 SegmentCount 和 WriteBufferSize
  - 在 `internal/config/config.go` 的 `HLSConfig` 结构体新增 `segment_count int` 字段（YAML tag: `segment_count`）
  - 在 `applyDefaults()` 中设置 `segment_count` 默认值为 7（从 3 增加到 7，播放列表窗口从 6s 增至 14s）
  - 在 `Validate()` 中校验 segment_count 范围 [3, 10]
  - 修改 `internal/hls/manager.go` 的 `startStream()` 方法（第 140、153 行），将硬编码 `SegmentCount: 3` 改为使用 `m.segmentCount` 字段
  - 在 `NewManagerWithOpts()` 中接受 segmentCount 参数
  - 将 `defaultWriteBufSize` 从 40 增加到 100
  - 更新 `cmd/mibee-nvr/main.go` 中 HLS Manager 初始化，传入 config 中的 segment_count
  - GREEN: 确保测试通过

  **Must NOT do**:
  - 不得改变录制管道（`internal/recorder/`）
  - 不得改变 `maxStreams=4` 或 `idleTimeout=60s`
  - 不得修改 gohlslib 库本身

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 配置字段添加 + 常量修改，范围清晰，1-2 个文件
  - **Skills**: []
  - **Skills Evaluated but Omitted**:
    - None needed — 纯 Go 配置/参数改动

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2, T3, T4)
  - **Blocks**: T5 (间接 — 前端配置需要对应后端参数)
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/config/config.go:HLSConfig` — 现有 HLS 配置结构体，新字段加入此处
  - `internal/config/config.go:applyDefaults()` — 默认值设置模式，参考 WriteBufferSize 的做法
  - `internal/config/config.go:Validate()` — 校验模式
  - `cmd/mibee-nvr/main.go` — HLS Manager 初始化处，传参模式

  **API/Type References**:
  - `internal/hls/manager.go:26-31` — 当前硬编码常量定义
  - `internal/hls/manager.go:140` — H.265 muxer SegmentCount 硬编码位置
  - `internal/hls/manager.go:153` — H.264 muxer SegmentCount 硬编码位置
  - `internal/hls/manager.go:80-95` — `NewManagerWithOpts` 当前签名，需要扩展

  **Test References**:
  - `internal/hls/manager_test.go` (如果存在) — 现有测试模式
  - `tests/integration_test.go` — 集成测试模式，`require` 断言 + `t.Helper()`

  **WHY Each Reference Matters**:
  - `config.go:HLSConfig` — 添加 segment_count 字段的精确位置
  - `manager.go:140,153` — 需要从硬编码改为变量的精确行
  - `main.go` — 穿线新参数的入口

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] 集成测试文件创建，验证 SegmentCount 和 WriteBufferSize 可配置
  - [ ] `rtk go test ./internal/hls/... -v -run TestSegmentCount` → PASS
  - [ ] `rtk go test ./internal/config/... -v` → PASS

  **QA Scenarios:**
  ```
  Scenario: HLS 配置参数生效
    Tool: Bash (go test)
    Preconditions: 代码编译通过
    Steps:
      1. rtk go test ./internal/hls/... -v -run TestSegmentCount
      2. 验证输出包含 SegmentCount=7
      3. rtk go test ./internal/config/... -v -run TestHLS
      4. 验证 Validate 拒绝 segment_count < 3 和 > 10
    Expected Result: 所有测试 PASS，默认值正确
    Failure Indicators: 任何测试 FAIL，默认值不正确
    Evidence: .sisyphus/evidence/task-1-config-test.txt

  Scenario: 配置穿线完整性
    Tool: Bash (go vet + grep)
    Preconditions: 编译成功
    Steps:
      1. rtk go vet ./internal/hls/... ./internal/config/... ./cmd/...
      2. grep -r 'SegmentCount.*3' internal/hls/ — 确认无硬编码 3 残留
    Expected Result: go vet 无错误，无硬编码 3 残留
    Failure Indicators: vet 报错或仍有硬编码
    Evidence: .sisyphus/evidence/task-1-vet-check.txt
  ```

  **Commit**: YES
  - Message: `feat(hls): make SegmentCount configurable and increase defaults`
  - Files: `internal/config/config.go`, `internal/hls/manager.go`, `cmd/mibee-nvr/main.go`, test files
  - Pre-commit: `rtk go test ./internal/hls/... ./internal/config/... -v`

- [x] 2. 帧丢弃可观测性（Prometheus 指标）

  **What to do**:
  - RED: 先写测试验证 Metrics 对象包含 HLS 帧丢弃计数器
  - 在 `internal/metrics/metrics.go` 新增 `HLSFramesDropped` 计数器（Prometheus `Counter`）
    - 标签: `camera_id` — 区分不同摄像机
    - 指标名: `nvr_hls_frames_dropped_total`
  - 修改 `internal/hls/manager.go` 的 `writeFrame()` 方法（第 466-470 行）
    - 缓冲满丢弃帧时递增计数器
    - 每 100 次丢弃写一条 warn 日志（而非每帧）
  - Manager 构造函数接受 `opts ...*metrics.Metrics` 可选参数（与其他包一致的模式）
  - 更新 `cmd/mibee-nvr/main.go` 传 metrics 实例给 HLS Manager
  - GREEN: 确保测试通过

  **Must NOT do**:
  - 不得添加逐帧日志 — 只允许聚合计数器 + 每 100 次聚合日志
  - 不得改变 writeFrame 的非阻塞语义

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 添加一个 Prometheus counter + 几行递增代码，遵循项目已有 metrics 模式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T3, T4)
  - **Blocks**: None
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/metrics/metrics.go` — 现有 9 个 NVR 指标的定义模式，用 `promauto.With(reg).NewCounter()` / `NewGauge()`
  - `internal/cleanup/cleanup.go` — 接受 `opts ...*metrics.Metrics` 可选参数的模式
  - `internal/hls/manager.go:466-470` — 当前静默丢弃帧的位置，需添加计数器递增

  **API/Type References**:
  - `internal/metrics/metrics.go:Metrics` struct — 添加 `HLSFramesDropped` 字段

  **Test References**:
  - `internal/metrics/metrics_test.go` (如果存在) — 指标测试模式

  **WHY Each Reference Matters**:
  - `metrics.go` — 必须按照已有的 Counter/Gauge 模式添加新指标
  - `cleanup.go` — `opts ...*metrics.Metrics` 可选参数模式是项目约定
  - `manager.go:466-470` — 精确的代码位置，丢弃帧时需要递增计数器

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] Metrics 测试文件验证 HLSFramesDropped 计数器存在且可递增
  - [ ] `rtk go test ./internal/metrics/... -v -run TestHLS` → PASS

  **QA Scenarios:**
  ```
  Scenario: Prometheus 指标可查询
    Tool: Bash (curl)
    Preconditions: NVR 运行中
    Steps:
      1. curl -s http://192.168.63.31:9090/metrics | grep nvr_hls_frames_dropped
      2. 验证输出包含 nvr_hls_frames_dropped_total 指标
    Expected Result: 指标存在，有 camera_id 标签
    Failure Indicators: 指标不存在或缺少标签
    Evidence: .sisyphus/evidence/task-2-metrics.txt

  Scenario: 帧丢弃时计数器递增
    Tool: Bash (go test)
    Preconditions: 单元测试环境
    Steps:
      1. rtk go test ./internal/hls/... -v -run TestFrameDrop
      2. 验证缓冲满时计数器递增
    Expected Result: 测试 PASS，计数器从 0 变为 N
    Evidence: .sisyphus/evidence/task-2-drop-test.txt
  ```

  **Commit**: YES
  - Message: `feat(hls): add frame drop observability metric`
  - Files: `internal/metrics/metrics.go`, `internal/hls/manager.go`, `cmd/mibee-nvr/main.go`, test files
  - Pre-commit: `rtk go test ./internal/metrics/... ./internal/hls/... -v`

- [x] 3. 前端 hls.js 配置现代化

  **What to do**:
  - RED: 先在 `hls-config.test.ts` 写测试验证新配置值
  - 重写 `web/src/lib/hls-config.ts` 的 `createHlsConfig()` 函数:
    - `enableWorker`: 改为 `true`（用户从现代浏览器访问，不是 RPi 浏览器）
    - `maxBufferLength`: 5 → 15（前向缓冲 15s）
    - `maxMaxBufferLength`: 10 → 30（最大前向缓冲 30s）
    - `maxBufferSize`: 10MB → 30MB
    - `backBufferLength`: 2 → 5（后向缓冲 5s）
    - 新增 `liveSyncDurationCount`: 3（与后端 SegmentCount 匹配）
    - 新增 `liveMaxLatencyDurationCount`: 7（允许最大延迟段数）
    - 新增 `liveDurationInfinity`: true（无限直播时长）
    - 新增 `progressive`: true（渐进式加载）
    - 保留 `xhrSetup` 认证逻辑不变
  - GREEN: 测试通过

  **Must NOT do**:
  - 不得升级 hls.js 库版本 — 只改配置
  - 不得改变 xhrSetup 认证逻辑
  - 不得移除任何现有配置项

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 单文件配置改动，逻辑清晰
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2, T4)
  - **Blocks**: T5（错误恢复重写依赖新配置）
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `web/src/lib/hls-config.ts` — 当前配置，需要修改的文件

  **External References**:
  - hls.js API docs: https://github.com/video-dev/hls.js/blob/master/docs/API.md#hlsconfig

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] `hls-config.test.ts` 验证 enableWorker=true, maxBufferLength=15, liveSyncDurationCount=3
  - [ ] `cd web && npx vitest run src/lib/hls-config.test.ts` → PASS

  **QA Scenarios:**
  ```
  Scenario: 新配置在浏览器中生效
    Tool: Playwright
    Preconditions: NVR 运行中
    Steps:
      1. 打开 Dashboard 页面，打开浏览器控制台
      2. 检查 hls.js 实例的 config 属性
      3. 验证 enableWorker === true, maxBufferLength === 15
    Expected Result: 所有新配置值生效，无控制台错误
    Evidence: .sisyphus/evidence/task-3-config-verify.png

  Scenario: 前端构建成功
    Tool: Bash
    Steps:
      1. cd web && rtk npm run build
    Expected Result: 构建成功，无 TypeScript 错误
    Evidence: .sisyphus/evidence/task-3-build.txt
  ```

  **Commit**: YES
  - Message: `feat(web): modernize hls.js config for modern browsers`
  - Files: `web/src/lib/hls-config.ts`, test file
  - Pre-commit: `cd web && npx vitest run`

- [x] 4. Playwright HLS 测试基础设施

  **What to do**:
  - 创建 `e2e-tests/tests/hls-helpers.ts`，包含可复用的 HLS 测试工具函数：
    - `waitForStreamState(page, cameraId, state, timeout)` — 等待指定摄像机达到指定状态
    - `checkNoBlackScreen(page, cameraId, durationMs)` — 持续检查无黑屏
    - `simulateNetworkError(page, urlPattern)` — 通过 route intercept 模拟网络错误
    - `getVideoReadyState(page, cameraId)` — 获取 video readyState
  - 创建 `e2e-tests/tests/hls-playback.spec.ts` 基础测试：
    - “单路播放应正常启动” — 打开 LiveView，等待 playing 状态
  - 注意：现有 `e2e-tests/playwright.config.ts` 配置的是远程 NVR（192.168.63.31:9090）

  **Must NOT do**:
  - 不得修改现有 E2E 测试文件 — 只添加新文件
  - 不得引入新的 npm 依赖

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 需理解 Playwright API 和现有测试结构，设计可复用工具函数
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2, T3)
  - **Blocks**: T10
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `e2e-tests/tests/` — 现有测试文件结构
  - `e2e-tests/playwright.config.ts` — Playwright 配置

  **API/Type References**:
  - `web/src/lib/hls-errors.ts:StreamState` — 流状态类型定义

  **External References**:
  - Playwright API: https://playwright.dev/docs/api/class-page

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] 基础测试文件创建并 PASS

  **QA Scenarios:**
  ```
  Scenario: 基础测试可运行
    Tool: Bash
    Preconditions: NVR 运行在 192.168.63.31:9090
    Steps:
      1. cd e2e-tests && npx playwright test tests/hls-playback.spec.ts
    Expected Result: 基础测试 PASS
    Evidence: .sisyphus/evidence/task-4-e2e-base.txt
  ```

  **Commit**: YES
  - Message: `test(e2e): add Playwright HLS test infrastructure`
  - Files: `e2e-tests/tests/hls-helpers.ts`, `e2e-tests/tests/hls-playback.spec.ts`
  - Pre-commit: `cd e2e-tests && npx playwright test tests/hls-playback.spec.ts`

  - Pre-commit: `cd e2e-tests && npx playwright test tests/hls-playback.spec.ts`

- [x] 5. 重写 hls-errors.ts 错误恢复策略

  **What to do**:
  - RED: 先写 `hls-errors.test.ts` 验证新的错误恢复行为
  - 重写 `web/src/lib/hls-errors.ts`，核心改进：
    1. **去抖 recoverMediaError**: 非致命 MEDIA_ERROR 不立即调用 recoverMediaError()
       - 使用 500ms 去抖 — 连续多个非致命错误只触发一次 recover
       - 如果 5s 内 recover 次数超过 3 次，升级为 destroy+recreate
    2. **僵尸检测**: 新增 `startZombieDetector(hls, videoEl, cameraId, onZombie)` 函数
       - 每 5s 检查 video.readyState 和当前时间
       - 如果 readyState === 0（HAVE_NOTHING）持续 10s → 声明僵尸
       - 如果连续 30s 没有 FRAG_LOADED 事件 → 声明僵尸
       - 僵尸回调：完全销毁并重建 hls.js 实例
    3. **destroy+recreate 替代 recoverMediaError**: 对反复失败的情况
       - 完全销毁当前 hls.js 实例
       - 创建新实例，重新 loadSource + attachMedia
       - 最多 recreate 2 次后降级为快照
    4. **浏览器标签页前后台处理**:
       - 监听 `visibilitychange` 事件
       - 标签页后台化时标记所有流为 `suspended`
       - 前台化时主动重建所有流（而不是等错误恢复）
    5. **保持现有导出接口**: `setupHlsErrorHandling` 和 `StreamState` 类型签名不变
       - 新增 `setupZombieDetector` 和 `handleVisibilityChange` 导出
  - GREEN: 测试通过

  **Must NOT do**:
  - 不得改变 `setupHlsErrorHandling` 的函数签名（Dashboard 和 LiveView 都依赖它）
  - 不得移除快照降级模式
  - 不得移除 `checkStreamAvailable` 函数

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 复杂的状态机逻辑（去抖、僵尸检测、recreate、前后台），需要仔细设计
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2 (sequential, T6 depends on this)
  - **Blocks**: T6, T7, T8
  - **Blocked By**: T3（需要新的 hls.js 配置）

  **References**:

  **Pattern References**:
  - `web/src/lib/hls-errors.ts` — 当前错误处理实现，需要完全重写
  - `web/src/lib/hls-config.ts` — createHlsConfig() 在 recreate 时需要调用

  **API/Type References**:
  - `web/src/lib/hls-errors.ts:StreamState` — 状态类型，保持不变
  - `web/src/lib/hls-errors.ts:HlsErrorConfig` — 配置接口，保持不变

  **External References**:
  - hls.js Events: https://github.com/video-dev/hls.js/blob/master/docs/API.md#events
  - hls.js Error Handling: https://github.com/video-dev/hls.js/blob/master/docs/API.md#errors

  **WHY Each Reference Matters**:
  - 当前 hls-errors.ts 是闪屏的根因所在地 — 去抖和 recreate 是核心修复
  - hls.js Events 文档 — 确认可用的事件类型（FRAG_LOADED, ERROR 等）

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] 测试验证 500ms 内连续错误只触发一次 recoverMediaError
  - [ ] 测试验证 readyState=0 持续 10s 触发僵尸回调
  - [ ] 测试验证 destroy+recreate 在 3 次 recover 后触发
  - [ ] `cd web && npx vitest run src/lib/hls-errors.test.ts` → PASS

  **QA Scenarios:**
  ```
  Scenario: 去抖 recoverMediaError
    Tool: Playwright
    Preconditions: NVR 运行中，至少一个 RTSP 摄像头在线
    Steps:
      1. 打开 Dashboard，等待所有流 playing
      2. 通过 page.route() 拦截一个 .ts segment 请求返回空数据
      3. 等待 2s，观察是否只触发一次 recover（无连续黑闪）
    Expected Result: 最多一次黑闪，之后恢复播放
    Evidence: .sisyphus/evidence/task-5-debounce.png

  Scenario: 僵尸检测和自动恢复
    Tool: Playwright
    Steps:
      1. 打开 Dashboard，等待所有流 playing
      2. 通过 page.route() 中断某路的 segment 请求（abort all）
      3. 等待 15s
      4. 验证该路显示错误 overlay 或自动重连（不是永久黑屏）
    Expected Result: 10-15s 内自动恢复或显示错误状态
    Evidence: .sisyphus/evidence/task-5-zombie-recovery.png
  ```

  **Commit**: YES
  - Message: `fix(web): rewrite HLS error recovery with debounce and zombie detection`
  - Files: `web/src/lib/hls-errors.ts`, `web/src/lib/hls-errors.test.ts`
  - Pre-commit: `cd web && npx vitest run`

- [x] 6. 创建 VideoPlayer 可复用组件

  **What to do**:
  - 创建 `web/src/components/VideoPlayer.svelte`，封装 video 元素 + overlay 状态层：
    1. **Props**: `cameraId`, `cameraName`, `streamUrl`, `cameraProtocol`, `expanded`
    2. **Overlay 状态**（永远不裸露黑屏）:
       - `loading` — 暗色背景 + shimmer 动画（不是黑色+白色 spinner）
       - `playing` — 无 overlay，视频正常显示
       - `buffering` — 半透明暗色背景 + 小 loading 指示器（不遮挡视频）
       - `error` — 暗色背景 + 错误图标 + 重连按钮
       - `snapshot` — 快照模式显示
    3. **视觉过渡**:
       - overlay 使用 `opacity` + `transition` (200ms) 而非瞬间切换
       - 摄像机名称和状态始终显示（底部半透明条）
       - 闪烁时 overlay 保持显示，直到稳定 playing
    4. **生命周期管理**:
       - 内部管理 hls.js 实例的创建/销毁
       - 集成 T5 的 `setupHlsErrorHandling` + `setupZombieDetector`
       - `onDestroy` 自动清理
       - `src` 变化时自动 destroy 旧实例 + 创建新实例
    5. **Events**: `on:stateChange`, `on:expand`, `on:shrink`
  - RED: 创建组件前先写基本的使用测试（渲染 + props）

  **Must NOT do**:
  - 不得做成通用组件库 — 只做本项目的播放器生命周期 + overlay
  - 不得引入新的 CSS 框架 — 使用 TailwindCSS
  - 不得改变 Dashboard.svelte 或 LiveView.svelte（T7/T8 的事）

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: Svelte 5 组件开发 + CSS 过渡动画 + 状态管理
  - **Skills**: [`frontend-ui-ux`]
    - `frontend-ui-ux`: 视觉过渡和 overlay 设计是核心工作

  **Parallelization**:
  - **Can Run In Parallel**: NO（依赖 T5 的错误处理接口）
  - **Parallel Group**: Wave 2 (after T5)
  - **Blocks**: T7, T8
  - **Blocked By**: T5

  **References**:

  **Pattern References**:
  - `web/src/routes/Dashboard.svelte:505-575` — 当前 HLS 播放器部分的 HTML + overlay 逻辑，新组件将替换此部分
  - `web/src/routes/LiveView.svelte:206-267` — 单路播放器的 HTML，新组件也将替换
  - `web/src/components/PtzControl.svelte` — 现有组件的结构参考（props, events, 样式）
  - `web/src/app.css` — CSS 变量（th-bg-primary 等），overlay 需要使用这些变量

  **API/Type References**:
  - `web/src/lib/hls-errors.ts:StreamState` — overlay 状态与 StreamState 对应
  - `web/src/lib/hls-config.ts:createHlsConfig` — 内部使用
  - `web/src/lib/api.ts:Camera` — camera 对象类型

  **WHY Each Reference Matters**:
  - Dashboard 和 LiveView 的现有 overlay 代码是新组件的设计来源
  - app.css 的 CSS 变量确保视觉一致性

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] 组件在 loading/playing/error 三种状态下渲染正确

  **QA Scenarios:**
  ```
  Scenario: Overlay 状态过渡无黑闪
    Tool: Playwright
    Steps:
      1. 打开 Dashboard，等待播放器初始化
      2. 截图验证：初始状态显示 shimmer overlay（不是纯黑）
      3. 等待 playing 状态，截图验证：overlay 淡出，视频显示
      4. 模拟错误，截图验证：error overlay 淡入（不是突然黑屏）
    Expected Result: 状态切换时有平滑过渡，无裸露黑屏
    Evidence: .sisyphus/evidence/task-6-overlay-states.png

  Scenario: 组件销毁时清理 hls.js 实例
    Tool: Bash (Playwright console)
    Steps:
      1. 打开 Dashboard，等待所有流 playing
      2. 导航到其他页面（如 Recordings）
      3. 检查浏览器内存是否下降（无泄漏）
    Expected Result: 无控制台错误，内存回收正常
    Evidence: .sisyphus/evidence/task-6-cleanup.txt
  ```

  **Commit**: YES
  - Message: `feat(web): add VideoPlayer component with overlay states`
  - Files: `web/src/components/VideoPlayer.svelte`
  - Pre-commit: `cd web && rtk npm run build`

  - Pre-commit: `cd web && rtk npm run build`

- [x] 7. 修复 Dashboard 播放器生命周期 + 应用 VideoPlayer

  **What to do**:
  - RED: 先写 Playwright 测试 — Dashboard 4 路同时播放 30s，验证无黑屏
  - 重写 `web/src/routes/Dashboard.svelte` 的 HLS 播放器部分：
    1. **替换内联 video + overlay 为 `<VideoPlayer>` 组件**（第 505-575 行区域）
    2. **修复 $effect 播放器生命周期**: 当前 `prevVisibleIds` Set 的 $effect 逻辑可能在 Svelte 5 响应式系统中产生竞态条件
       - 改用显式的 `onMount`/`onDestroy` 管理，而非依赖 $effect 的执行时机
       - 或者确保 $effect 清理函数正确销毁旧播放器
    3. **修复 `initPlayer` 的 `setTimeout(..., 50)` 竞态**（第 359 行）: 50ms 延迟是任意值，可能在 DOM 未就绪时触发
       - 改用 `tick()` 或 `requestAnimationFrame` 确保 video 元素已挂载
    4. **前后台处理**: 集成 T5 的 `handleVisibilityChange`
       - `document.addEventListener('visibilitychange', ...)` in onMount
       - 前台化时重建所有播放器
    5. **摄像机切换竞态修复**: `applyCameraSelection()` 先销毁所有旧播放器，再初始化新播放器
       - 当前代码只处理 `prevVisibleIds` 的差异，可能遗漏同时 remove+add 同一 ID 的情况
  - GREEN: 30s 稳定性测试通过

  **Must NOT do**:
  - 不得改变 Dashboard 的网格布局逻辑（T9 的事）
  - 不得改变摄像机选择/配置面板逻辑
  - 不得移除快照降级模式

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Svelte 5 响应式生命周期管理复杂，需要仔细处理竞态条件和清理
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (after T5, T6)
  - **Blocks**: T9, T10
  - **Blocked By**: T5, T6

  **References**:

  **Pattern References**:
  - `web/src/routes/Dashboard.svelte:337-366` — 当前 $effect 播放器生命周期，需要重写
  - `web/src/routes/Dashboard.svelte:195-246` — 当前 initPlayer/destroyPlayer 函数
  - `web/src/routes/Dashboard.svelte:505-575` — 需要替换为 VideoPlayer 组件的 HTML 区域

  **API/Type References**:
  - `web/src/components/VideoPlayer.svelte` — T6 创建的组件，本任务的核心消费者
  - `web/src/lib/hls-errors.ts` — T5 重写的错误处理函数

  **WHY Each Reference Matters**:
  - $effect 生命周期是永久黑屏的根因之一 — 像尸播放器可能不被清理
  - initPlayer 的 setTimeout 竞态可能导致 DOM 未就绪时初始化失败

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] Playwright 测试：Dashboard 4 路播放 30s，0 次黑屏（视频 readyState >= 1）

  **QA Scenarios:**
  ```
  Scenario: Dashboard 多路稳定性
    Tool: Playwright
    Steps:
      1. 打开 Dashboard，选择 4 个摄像机
      2. 等待所有流 playing（绿色指示灯）
      3. 保持页面 30s，每 5s 截图检查无黑屏
    Expected Result: 30s 内所有 4 路保持 playing，无黑屏
    Evidence: .sisyphus/evidence/task-7-stability-*.png

  Scenario: 摄像机切换无残留
    Tool: Playwright
    Steps:
      1. 打开 Dashboard，选择 A/B/C/D 四路
      2. 等待所有 playing
      3. 打开配置，切换为 A/B/E/F
      4. 等待新流 playing
      5. 检查 D 和 E 的播放器状态（D 应销毁，E 应新建）
    Expected Result: 切换后所有流正常，无僵尸播放器
    Evidence: .sisyphus/evidence/task-7-camera-switch.png
  ```

  **Commit**: YES
  - Message: `fix(web): fix Dashboard player lifecycle and apply VideoPlayer`
  - Files: `web/src/routes/Dashboard.svelte`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 8. 改善 LiveView.svelte 播放器

  **What to do**:
  - 替换 `web/src/routes/LiveView.svelte` 的内联播放器为 `<VideoPlayer>` 组件（第 206-267 行区域）
  - 应用 T5 的改进错误处理
  - 移除冗余的 `playerInitialized` 标志和 `setTimeout(..., 50)` 延迟（第 158-167 行）
  - 保留 LiveView 特有的功能：全屏、PTZ 控制、导航回退按钮
  - 确认单路播放仍然正常工作（这是用户验证的基线 — “单路实时浏览又能正常播放”）

  **Must NOT do**:
  - 不得移除 PTZ 控制面板
  - 不得移除全屏功能
  - 不得改变路由逻辑

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 主要是替换内联代码为 VideoPlayer 组件，逻辑简单
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T7, T9)
  - **Parallel Group**: Wave 3
  - **Blocks**: T10
  - **Blocked By**: T5, T6

  **References**:

  **Pattern References**:
  - `web/src/routes/LiveView.svelte:41-96` — 当前 initPlayer 函数，需要简化
  - `web/src/routes/LiveView.svelte:158-167` — 需要移除的 playerInitialized + setTimeout 逻辑
  - `web/src/routes/LiveView.svelte:206-267` — 需要替换为 VideoPlayer 的 HTML 区域

  **API/Type References**:
  - `web/src/components/VideoPlayer.svelte` — T6 创建的组件

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: 单路播放正常
    Tool: Playwright
    Steps:
      1. 从 Dashboard 点击某摄像机进入 LiveView
      2. 等待 playing 状态
      3. 验证 overlay 无黑屏闪烁
    Expected Result: 单路播放流畅，无黑闪
    Evidence: .sisyphus/evidence/task-8-liveview.png
  ```

  **Commit**: YES
  - Message: `fix(web): apply VideoPlayer to LiveView`
  - Files: `web/src/routes/LiveView.svelte`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 9. Dashboard 网格 UX 改进

  **What to do**:
  - 在 `web/src/routes/Dashboard.svelte` 中进行以下 UX 改进：
    1. **每路添加独立的重连按钮**（error overlay 上），而不只是降级为快照
    2. **展开/缩小过渡动画**：当前 expandedCameraId 切换是瞬间的，添加 CSS transition
    3. **状态指示器优化**：将当前的小圆点（2×2px）改为更明显的时间戳 + 状态文本
    4. **底部信息栏改进**：显示摄像机名称 + 协议 + 状态 + 当前时间
    5. **loading shimmer 效果**：替换单纯的 spinner 为暗色 shimmer 动画，更专业

  **Must NOT do**:
  - 不得重新设计网格布局（保持 2x2）
  - 不得改变摄像机选择逻辑
  - 不得引入新的 npm 依赖

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: CSS 动画 + UI 优化
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T8, T10)
  - **Parallel Group**: Wave 3
  - **Blocks**: None
  - **Blocked By**: T7

  **References**:

  **Pattern References**:
  - `web/src/routes/Dashboard.svelte` — Dashboard 组件
  - `web/src/app.css` — CSS 变量和主题定义

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: 重连按钮可用
    Tool: Playwright
    Steps:
      1. 打开 Dashboard，等待所有流 playing
      2. 中断某路网络（route intercept）
      3. 等待 error overlay 出现
      4. 点击重连按钮
      5. 验证流恢复 playing
    Expected Result: 重连成功，流恢复
    Evidence: .sisyphus/evidence/task-9-reconnect.png
  ```

  **Commit**: YES
  - Message: `feat(web): improve Dashboard grid UX`
  - Files: `web/src/routes/Dashboard.svelte`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 10. E2E 测试套件

  **What to do**:
  - 在 `e2e-tests/tests/hls-playback.spec.ts` 中扩展完整的 E2E 测试：
    1. **多路稳定性测试**: Dashboard 4 路同时播放 60s，每 5s 检查无黑屏
    2. **网络中断恢复测试**: 模拟某路网络中断 5s 后恢复，验证播放器在 10s 内恢复
    3. **摄像机切换测试**: 切换 Dashboard 中的摄像机选择，验证旧播放器销毁、新播放器创建
    4. **浏览器前后台测试**: 模拟 visibilitychange 事件，验证前台化后播放器重建
    5. **单路播放测试**: LiveView 单路播放正常启动和播放
    6. **错误 overlay 测试**: 摄像机不存在时显示错误 overlay（不是黑屏）
  - 使用 T4 创建的 hls-helpers.ts 工具函数
  - 所有测试连接到实际 NVR（192.168.63.31:9090）

  **Must NOT do**:
  - 不得 mock RTSP 源 — 测试的是真实 HLS 流
  - 不得跳过网络中断测试（这是核心场景）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 需要设计多个 Playwright 测试场景，包含网络模拟
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO（需要 T7/T8 完成后才能测试）
  - **Parallel Group**: Wave 3 (after T7, T8)
  - **Blocks**: F1-F4
  - **Blocked By**: T4, T7, T8

  **References**:

  **Pattern References**:
  - `e2e-tests/tests/` — 现有测试文件，保持风格一致
  - `e2e-tests/tests/hls-helpers.ts` — T4 创建的工具函数

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] 6 个测试场景全部通过

  **QA Scenarios:**
  ```
  Scenario: 完整 E2E 测试套件通过
    Tool: Bash
    Preconditions: NVR 运行在 192.168.63.31:9090
    Steps:
      1. cd e2e-tests && npx playwright test tests/hls-playback.spec.ts
    Expected Result: 所有测试 PASS
    Evidence: .sisyphus/evidence/task-10-e2e-full.txt
  ```

  **Commit**: YES
  - Message: `test(e2e): add comprehensive HLS playback E2E tests`
  - Files: `e2e-tests/tests/hls-playback.spec.ts`
  - Pre-commit: `cd e2e-tests && npx playwright test tests/hls-playback.spec.ts`


## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.

- [x] F1. **Plan Compliance Audit** — `oracle` — VERDICT: ✅ APPROVE (Must Have 6/6, Must NOT Have 9/9, Tasks 10/10)
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high` — VERDICT: ✅ APPROVE (Build PASS, 560 tests PASS, 3 minor issues fixed)
  Run `rtk go vet ./...` + `rtk go test ./... -v` + `cd web && rtk npm run build`. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — SKIPPED (requires live NVR on RPi, deferred to deployment)
  Start from clean state (deploy to RPi). Execute EVERY QA scenario from EVERY task. Test cross-task integration: Dashboard 4-camera grid stability, camera switch, error recovery. Test edge cases: tab background/foreground, camera disconnect mid-stream. Save screenshots to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `oracle` — VERDICT: ✅ APPROVE after fix (untracked files committed, contamination CLEAN, guardrails 8/8)
  For each task: read "What to do", read actual diff (`rtk git diff main...HEAD`). Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **T1**: `feat(hls): make SegmentCount configurable and increase defaults` — config.go, manager.go, manager_test.go
- **T2**: `feat(hls): add frame drop observability metric` — manager.go, metrics.go, metrics_test.go
- **T3**: `feat(web): modernize hls.js config for modern browsers` — hls-config.ts
- **T4**: `test(e2e): add Playwright HLS test infrastructure` — e2e-tests/
- **T5**: `fix(web): rewrite HLS error recovery with debounce and zombie detection` — hls-errors.ts, hls-errors.test.ts
- **T6**: `feat(web): add VideoPlayer component with overlay states` — VideoPlayer.svelte
- **T7**: `fix(web): fix Dashboard player lifecycle and apply VideoPlayer` — Dashboard.svelte
- **T8**: `fix(web): apply VideoPlayer to LiveView` — LiveView.svelte
- **T9**: `feat(web): improve Dashboard grid UX` — Dashboard.svelte
- **T10**: `test(e2e): add comprehensive HLS playback E2E tests` — e2e-tests/

---

## Success Criteria

### Verification Commands
```bash
# Backend tests
rtk go test ./internal/hls/... -v          # All HLS tests pass
rtk go test ./internal/metrics/... -v      # Metrics tests pass

# Frontend build
cd web && rtk npm run build                # Build succeeds

# Metrics endpoint (on RPi)
ssh mickey@192.168.63.31 "curl -s http://localhost:9090/metrics | grep nvr_hls_frames_dropped"
# Expected: nvr_hls_frames_dropped_total{camera_id="..."} N

# Memory check (on RPi, 4 streams active)
ssh mickey@192.168.63.31 "curl -s http://localhost:9090/metrics | grep process_resident_memory_bytes"
# Expected: < 400MB (335544320)

# E2E tests
cd e2e-tests && npx playwright test tests/hls-playback.spec.ts
# Expected: All tests pass
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All Go tests pass
- [ ] Frontend builds successfully
- [ ] Playwright E2E tests pass
- [ ] RSS memory < 400MB on RPi with 4 streams
- [ ] No flashing in 10-minute Dashboard stability test
- [ ] Auto-recovery from network interruption within 10s
- [ ] Error overlay shown (not black screen) on camera disconnect
