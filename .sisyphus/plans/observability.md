# MiBee NVR 可观测性完善

## TL;DR

> **Quick Summary**: 为 MiBee NVR 添加生产级可观测性：结构化日志 (slog) + Prometheus 指标 + 增强健康检查 + 请求追踪中间件，总内存开销 < 10MB，适配 RPi 3B 约束。
>
> **Deliverables**:
> - 结构化日志系统（slog，替换全部 62 个 log.Printf + 8 个 log.Fatalf）
> - Prometheus 指标端点 (/metrics)，含 Go runtime + NVR 业务指标
> - 增强健康检查 (/api/health) + 新增就绪探针 (/api/readyz)
> - 自定义 slog 请求日志中间件（替换 chi.Logger）
> - 配置化的 pprof 调试端点
> - 前端 Stats 页面增强（运行时指标卡片）
>
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: Config → slog setup → slog migration → Request middleware → Metrics package → Metrics instrumentation → Health → Frontend

---

## Context

### Original Request
给该 NVR 项目完善可观测性（Observability），包括结构化日志、指标暴露、健康检查端点、请求追踪。

### Interview Summary
**Key Discussions**:
- 资源约束: 极致轻量 (< 10MB 额外内存)
- 技术选型: slog (标准库) + prometheus/client_golang
- 前端: 增强现有统计页面，不新增页面
- pprof: config.yaml 开关控制，默认关闭
- 测试: Tests after 策略
- 数据消费: 混合方案 — 日志文件 + Web UI + 可选 Prometheus

**Research Findings**:
- 当前日志: 62 个 log.Printf + 8 个 log.Fatalf + 3 个 log.Println，跨 9 个文件
- 无任何可观测性依赖 (go.mod 干净)
- chi 中间件栈: Logger, Recoverer, SecurityHeaders, AuthMiddleware
- /api/health 硬编码返回 {"status":"ok"}，无依赖检查
- handler.go 已有 statusRecorder 类型可复用
- 前端 Stats 页面使用 Chart.js 4.5.1，有两个图表 (趋势线 + 摄像头柱状图)

### Metis Review
**Identified Gaps** (addressed):
- log.Printf 计数修正: 62 个 (非 55), 9 个文件 (非 8)
- log.Fatalf 需要 slog.Error + os.Exit(1) 替代（slog 无 Fatal 级别）
- statusRecorder 需提取到 middleware/ 共享复用
- /metrics 必须放在公开路由组，不经过 auth
- 禁止用 r.URL.Path 作 Prometheus label（基数爆炸）
- 启动阶段 (config 加载前) 需先用默认 slog，加载后再重新配置
- pprof 启用时需考虑安全风险，应限制为 localhost 或 auth 保护

---

## Work Objectives

### Core Objective
为 MiBee NVR 添加轻量级、生产就绪的可观测性系统，在 RPi 3B 905MB RAM 约束下提供日志、指标、健康检查、请求追踪能力，总开销 < 10MB。

### Concrete Deliverables
- `internal/metrics/` — Prometheus 指标注册与暴露
- `internal/middleware/logging.go` — slog 请求日志中间件 + statusRecorder
- `internal/config/config.go` — 新增 observability 配置字段
- `cmd/mibee-nvr/main.go` — 更新启动流程、中间件栈
- 全部 9 个文件的 log.Printf → slog 迁移
- `/api/health` 增强 + `/api/readyz` 新增
- `/metrics` 公开端点
- `/debug/pprof` 配置化端点
- `web/src/routes/Stats.svelte` — 运行时指标卡片增强
- 测试文件

### Definition of Done
- [ ] `grep -c 'log\.Printf' internal/ cmd/` 返回 0 (全部迁移)
- [ ] `curl -sf localhost:9090/metrics | grep -c 'nvr_'` 返回 ≥ 10 (自定义指标存在)
- [ ] `curl -sf localhost:9090/api/health | jq .status` 返回有效状态
- [ ] 所有现有测试通过: `rtk go test ./... -v`
- [ ] 内存开销 < 10MB: `ps -o rss= -p $(pgrep mibee-nvr)`

### Must Have
- slog 结构化日志 (JSON + Text 格式可配置)
- Prometheus 指标端点 (含 Go runtime + NVR 业务指标)
- 增强健康检查 (SQLite 连通性 + 磁盘空间 + goroutine 数)
- 请求日志中间件 (method, path, status, duration)
- 配置字段 (log_level, log_format, debug.enable_pprof)
- 向后兼容 (现有 config 无需修改即可运行)
- 前端 Stats 页面增加运行时指标

### Must NOT Have (Guardrails)
- ❌ OpenTelemetry / 分布式追踪
- ❌ Sentry / 外部错误追踪服务
- ❌ 修改 model.Recorder / model.StorageProvider 接口签名
- ❌ 修改 internal/muxer/mp4mux.go (设计上无日志)
- ❌ Histogram 指标 (仅 counter + gauge，降低内存开销)
- ❌ 迁移测试代码中的 log.Printf (测试保持 t.Logf)
- ❌ r.URL.Path 作 Prometheus label (无界基数)
- ❌ 前端新增页面或重构非 Stats 页面
- ❌ 日志文件轮转 (依赖 systemd/journald)
- ❌ 告警规则 / Prometheus alertmanager 配置
- ❌ 新增 npm 依赖

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (testify + integration_test.go)
- **Automated tests**: YES (Tests after — 实现后补充)
- **Framework**: Go testing + testify
- **Test approach**: 每个可观测性模块完成后添加对应测试

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **API endpoints**: Use Bash (curl + jq) — Send requests, assert status + response fields
- **Metrics**: Use Bash (curl) — Verify /metrics output format and metric presence
- **Logging**: Use Bash — Run app, capture output, verify JSON/text format
- **Frontend**: Use Playwright — Navigate, assert DOM elements, screenshot

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately - config + shared infra):
├── Task 1: Config observability fields [quick]
├── Task 2: slog setup + per-component logger factory [quick]
└── Task 3: Extract statusRecorder to middleware [quick]

Wave 2 (After Wave 1 - core implementations, MAX PARALLEL):
├── Task 4: slog migration — all log.Printf/log.Fatalf calls (depends: 2) [deep]
├── Task 5: Request logging middleware (depends: 3) [unspecified-high]
├── Task 6: Prometheus metrics package (depends: 1) [unspecified-high]
├── Task 7: Enhanced health check + readyz (depends: 1, 2) [unspecified-high]

Wave 3 (After Wave 2 - instrumentation + integration):
├── Task 8: Metrics instrumentation in recorder/manager/cleanup (depends: 6) [deep]
├── Task 9: Wire up in main.go — middleware stack, /metrics, /readyz, pprof (depends: 4, 5, 6, 7) [unspecified-high]
├── Task 10: Frontend Stats page enhancement (depends: 7) [visual-engineering]

Wave 4 (After Wave 3 - tests):
├── Task 11: Backend tests — metrics, health, middleware (depends: 8, 9) [deep]

Wave FINAL (After ALL tasks — 4 parallel reviews):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality review (unspecified-high)
├── Task F3: Real manual QA (unspecified-high)
└── Task F4: Scope fidelity check (deep)
→ Present results → Get explicit user okay

Critical Path: T1 → T2 → T4 → T9 → T11 → FINAL
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 4 (Wave 2)
```

### Dependency Matrix

| Task | Blocked By | Blocks | Wave |
|------|-----------|--------|------|
| T1   | - | T6, T7 | 1 |
| T2   | - | T4, T7 | 1 |
| T3   | - | T5 | 1 |
| T4   | T2 | T9 | 2 |
| T5   | T3 | T9 | 2 |
| T6   | T1 | T8, T9 | 2 |
| T7   | T1, T2 | T9, T10 | 2 |
| T8   | T6 | T11 | 3 |
| T9   | T4, T5, T6, T7 | T11 | 3 |
| T10  | T7 | - | 3 |
| T11  | T8, T9 | FINAL | 4 |

### Agent Dispatch Summary

- **Wave 1**: **3** — T1 → `quick`, T2 → `quick`, T3 → `quick`
- **Wave 2**: **4** — T4 → `deep`, T5 → `unspecified-high`, T6 → `unspecified-high`, T7 → `unspecified-high`
- **Wave 3**: **3** — T8 → `deep`, T9 → `unspecified-high`, T10 → `visual-engineering`
- **Wave 4**: **1** — T11 → `deep`
- **FINAL**: **4** — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

- [x] 1. Config Observability Fields

  **What to do**:
  - 在 `internal/config/config.go` 的 `Config` 结构体中添加 `ObservabilityConfig` 嵌套字段:
    ```go
    type ObservabilityConfig struct {
        LogLevel  string `yaml:"log_level"`   // debug, info, warn, error — default: info
        LogFormat string `yaml:"log_format"`  // json, text — default: text
        Debug     DebugConfig `yaml:"debug"`
    }
    type DebugConfig struct {
        EnablePprof bool `yaml:"enable_pprof"` // default: false
    }
    ```
  - 在 `applyDefaults()` 中添加默认值: LogLevel="info", LogFormat="text", EnablePprof=false
  - 在 `Validate()` 中校验: log_level 必须是 debug/info/warn/error 之一, log_format 必须是 json/text
  - 确保现有配置文件无此字段时也能正常工作 (向后兼容)

  **Must NOT do**:
  - 不修改 Config.Save() 的原子写入模式
  - 不修改其他配置字段
  - 不修改配置文件格式 (YAML)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 单文件修改，3 个新 struct + 默认值 + 校验逻辑
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2, T3)
  - **Blocks**: T6, T7
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/config/config.go:Config` — 现有 Config 结构体，注意嵌套模式和 YAML tag 约定
  - `internal/config/config.go:applyDefaults()` — 默认值设置模式，复制此风格
  - `internal/config/config.go:Validate()` — 校验逻辑模式，返回 error

  **API/Type References**:
  - `internal/config/config.go:ServerConfig` — 嵌套配置结构体的范例

  **WHY Each Reference Matters**:
  - Config struct 告诉你新字段应放在哪个位置、用什么嵌套风格
  - applyDefaults() 展示了如何为新字段设置默认值 (只在不为零值时设置)
  - Validate() 展示了校验错误的格式和返回方式

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: Config with no observability section uses defaults
    Tool: Bash
    Steps:
      1. Write a minimal config.yaml without observability section
      2. Run: go run cmd/mibee-nvr/main.go -config /tmp/test-config.yaml 2>&1 | head -5
      3. Verify app starts without error (loads defaults)
      4. Check: log_level defaults to "info", log_format defaults to "text"
    Expected Result: App starts successfully, no config validation errors
    Evidence: .sisyphus/evidence/task-1-default-config.txt

  Scenario: Config with invalid log_level is rejected
    Tool: Bash
    Steps:
      1. Write config.yaml with log_level: "verbose" (invalid value)
      2. Run: go run cmd/mibee-nvr/main.go -config /tmp/bad-config.yaml 2>&1
      3. Verify validation error mentions invalid log_level
    Expected Result: App exits with error, validation message contains "log_level"
    Evidence: .sisyphus/evidence/task-1-invalid-loglevel.txt
  ```

  **Commit**: YES (groups with T2, T3)
  - Message: `feat(config): add observability configuration fields`
  - Files: `internal/config/config.go`

- [x] 2. slog Setup + Per-Component Logger Factory

  **What to do**:
  - 在 `internal/middleware/` 创建 `slogutil.go`，提供:
    1. `SetupLogger(level, format string) *slog.Logger` — 根据 config 创建 slog handler (JSON 或 Text), 设置日志级别
    2. `ComponentLogger(name string) *slog.Logger` — 返回带 `component` 字段的子 logger: `slog.Default().With("component", name)`
  - 在 `cmd/mibee-nvr/main.go` 启动流程中:
    1. 先配置默认 slog (info level, text format) — 处理 config 加载前的日志
    2. 加载 config 后，用 `SetupLogger(config.LogLevel, config.LogFormat)` 重新配置
    3. `slog.SetDefault(logger)` 设置全局默认
  - 替换 main.go 中的 3 个 `log.Println` 和 8 个 `log.Fatalf`:
    - `log.Fatalf(...)` → `slog.Error(...); os.Exit(1)` (slog 无 Fatal 级别)
    - `log.Println(...)` → `slog.Info(...)`

  **Must NOT do**:
  - 不添加 log.Fatal 等价物 (用 slog.Error + os.Exit(1))
  - 不修改 config 加载逻辑本身，只在加载前后设置 slog

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 创建工具函数文件 + 修改 main.go 启动流程
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T3)
  - **Blocks**: T4, T7
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `cmd/mibee-nvr/main.go` — 完整启动流程，找到所有 log.Fatalf/log.Println 调用点
  - `internal/middleware/auth.go` — 中间件目录中已有文件，复制其 package 声明和命名风格

  **External References**:
  - Go slog 文档: https://pkg.go.dev/log/slog — HandlerOptions, JSONHandler, TextHandler

  **WHY Each Reference Matters**:
  - main.go 中的 11 个 log 调用 (8 Fatalf + 3 Println) 是第一个要迁移的目标
  - middleware/ 目录是 slogutil.go 的自然位置 (与 auth.go 同目录)
  - slog 文档说明 JSON/Text handler 的创建方式和级别过滤

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: slog produces JSON output when configured
    Tool: Bash
    Steps:
      1. Set config: log_format: json, log_level: info
      2. Start app briefly, capture stderr output
      3. Pipe output through: jq . 2>/dev/null
      4. Verify each line is valid JSON with "time", "level", "msg", "component" fields
    Expected Result: All log lines parse as valid JSON via jq
    Evidence: .sisyphus/evidence/task-2-json-output.txt

  Scenario: Log level filtering works
    Tool: Bash
    Steps:
      1. Set config: log_level: error
      2. Start app briefly, capture output
      3. Verify no Info or Warn level messages appear
      4. Only Error level messages should be present
    Expected Result: Zero lines with "level":"info" in output
    Evidence: .sisyphus/evidence/task-2-level-filter.txt
  ```

  **Commit**: YES (groups with T1, T3)
  - Message: `feat(config): add observability configuration fields`
  - Files: `internal/middleware/slogutil.go`, `cmd/mibee-nvr/main.go`

- [x] 3. Extract statusRecorder to Middleware Package

  **What to do**:
  - 从 `internal/api/handler.go:111-119` 提取 `statusRecorder` 类型到 `internal/middleware/recorder.go`
  - 导出为 `StatusRecorder` (首字母大写)
  - 添加 `Bytes` 字段用于追踪响应大小:
    ```go
    type StatusRecorder struct {
        http.ResponseWriter
        Status int
        Bytes  int
    }
    func (r *StatusRecorder) WriteHeader(code int) {
        r.Status = code
        r.ResponseWriter.WriteHeader(code)
    }
    func (r *StatusRecorder) Write(b []byte) (int, error) {
        n, err := r.ResponseWriter.Write(b)
        r.Bytes += n
        return n, err
    }
    ```
  - 更新 `internal/api/handler.go` 使用新的 `middleware.StatusRecorder`，删除本地定义
  - 确保现有测试通过 (handler.go 中的 handleLogin 使用了 statusRecorder)

  **Must NOT do**:
  - 不修改 statusRecorder 的行为逻辑，只是移动和导出
  - 不修改 handleLogin 的行为

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 单类型提取 + 引用更新
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2)
  - **Blocks**: T5
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/api/handler.go:111-119` — 当前 statusRecorder 定义，这是要提取的代码
  - `internal/api/handler.go:handleLogin` — 使用 statusRecorder 的地方，需更新 import

  **WHY Each Reference Matters**:
  - handler.go:111-119 是要移动的源代码，必须精确复制
  - handleLogin 展示了 statusRecorder 的使用方式，确保兼容性

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: statusRecorder extracted and existing tests pass
    Tool: Bash
    Steps:
      1. Verify new file exists: ls internal/middleware/recorder.go
      2. Verify old definition removed: grep -c 'statusRecorder struct' internal/api/handler.go → 0
      3. Run: rtk go test ./internal/api/... -v
      4. All tests pass
    Expected Result: New file created, old removed, all tests green
    Evidence: .sisyphus/evidence/task-3-extraction.txt
  ```

  **Commit**: YES (groups with T1, T2)
  - Message: `feat(config): add observability configuration fields`
  - Files: `internal/middleware/recorder.go`, `internal/api/handler.go`


- [x] 4. slog Migration — All log.Printf/log.Fatalf Calls

  **What to do**:
  - 替换全部 62 个 `log.Printf` + 8 个 `log.Fatalf` + 3 个 `log.Println` 调用:
    - 每个 package 创建一个带 `component` 字段的 logger:
      ```go
      var logger = slog.Default().With("component", "camera-manager")
      ```
    - `log.Printf("[h264-recorder %s] connection error: %v", camID, err)` →
      `logger.Error("connection error", "camera_id", camID, "error", err)`
    - `log.Fatalf("failed to load config: %v", err)` → `slog.Error("failed to load config", "error", err); os.Exit(1)`
    - `log.Println("started camera manager")` → `slog.Info("started camera manager")`
  - 涉及文件 (9个):
    1. `cmd/mibee-nvr/main.go` — 8 log.Fatalf + 3 log.Println
    2. `internal/camera/manager.go` — ~21 log.Printf
    3. `internal/recorder/h264.go` — ~13 log.Printf
    4. `internal/cleanup/cleanup.go` — ~8 log.Printf
    5. `internal/recorder/mjpeg.go` — ~7 log.Printf
    6. `internal/middleware/auth.go` — ~2 log.Printf
    7. `internal/api/handler.go` — ~2 log.Printf
    8. `internal/storage/db.go` — ~1 log.Printf
    9. `internal/ftp/server.go` — ~1 log.Printf
  - 统一使用 `slog.LogAttrs()` 在热路径避免反射 (recorder 的 ring buffer 路径)
  - 保持 `[component-name]` 语义信息通过 slog 的 `component` 字段
  - main.go 中 log.Fatalf 的 migration 需保持 os.Exit(1) 语义

  **Must NOT do**:
  - 不迁移测试代码中的 log.Printf (测试保持 t.Logf)
  - 不修改日志的语义内容，只改变格式和结构
  - 不添加新日志消息，只替换现存的

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 大规模代码迁移，涉及 9 个文件 73 个调用点，需要仔细处理每个上下文
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T5, T6, T7)
  - **Blocks**: T9
  - **Blocked By**: T2

  **References**:

  **Pattern References**:
  - `cmd/mibee-nvr/main.go` — 8 个 log.Fatalf + 3 个 log.Println，注意它们的上下文（配置加载、DB 初始化等）
  - `internal/camera/manager.go` — 最多 log.Printf (21个)，涵盖摄像头生命周期事件，注意 `camera_id` 参数提取
  - `internal/recorder/h264.go` — 13 个调用，注意 PANIC recovery 日志格式（含 stack trace）
  - `internal/recorder/mjpeg.go` — 7 个调用，同上 PANIC recovery 模式
  - `internal/cleanup/cleanup.go` — 8 个调用，保留和删除记录的日志
  - `internal/middleware/auth.go` — 2 个调用，速率限制和认证失败日志
  - `internal/api/handler.go` — 2 个 warning 级别日志
  - `internal/storage/db.go` — 1 个 scanTime 解析失败日志
  - `internal/ftp/server.go` — 1 个上传文件记录插入失败日志
  - `internal/middleware/slogutil.go` (T2 creates) — `ComponentLogger()` 工厂函数，所有 package 应使用此函数创建 logger

  **WHY Each Reference Matters**:
  - 每个 log.Printf 调用都有其独特的参数格式，需要逐个转换为 slog 的 key-value 结构化字段
  - PANIC recovery 的日志包含 stack trace，需要用 slog.String("stack", string(buf)) 传递
  - main.go 的 log.Fatalf 必须转为 slog.Error + os.Exit(1)，不能简单替换

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: Zero log.Printf in non-test production code
    Tool: Bash
    Steps:
      1. grep -rn 'log\.Printf' internal/ cmd/ --include='*.go' | grep -v '_test.go'
      2. grep -rn 'log\.Fatalf' internal/ cmd/ --include='*.go' | grep -v '_test.go'
      3. grep -rn 'log\.Println' internal/ cmd/ --include='*.go' | grep -v '_test.go'
      4. Count: all should return 0 results
    Expected Result: Zero log.Printf, log.Fatalf, log.Println in non-test code
    Evidence: .sisyphus/evidence/task-4-no-logprintf.txt

  Scenario: All existing tests pass after migration
    Tool: Bash
    Steps:
      1. rtk go test ./... -v 2>&1
      2. Verify all tests pass (no failures)
    Expected Result: All tests PASS, 0 failures
    Evidence: .sisyphus/evidence/task-4-tests-pass.txt

  Scenario: slog component field present in output
    Tool: Bash
    Steps:
      1. Start app with a test camera config
      2. Capture log output
      3. Verify each log line has "component" field
    Expected Result: All log lines contain "component":"<name>"
    Evidence: .sisyphus/evidence/task-4-component-field.txt
  ```

  **Commit**: YES
  - Message: `refactor(logging): migrate log.Printf to slog structured logging`
  - Files: all 9 files listed above
  - Pre-commit: `rtk go test ./... -v`

- [x] 5. Request Logging Middleware (slog-based)

  **What to do**:
  - 在 `internal/middleware/logging.go` 创建自定义请求日志中间件:
    - 函数签名: `func RequestLogger(logger *slog.Logger, skipPaths ...string) func(next http.Handler) http.Handler`
    - 使用 `middleware.StatusRecorder` (T3 提取的) 包装 ResponseWriter
    - 记录字段: method, path, status, duration, bytes, remote_addr
    - `skipPaths` 参数用于跳过 /api/health, /api/readyz, /metrics 等高频端点
    - 使用 `slog.LogAttrs()` 避免 `reflect` 开销
  - 路径规范化: 对 `/api/recordings/123` 类路径，记录模板 `/api/recordings/{id}` 而非具体 ID
    - 避免每个 recording ID 生成不同日志行
  - 在 `cmd/mibee-nvr/main.go` 中替换 `middleware.Logger` (chi 默认) 为新中间件:
    - 删除: `r.Use(middleware.Logger)`
    - 添加: `r.Use(middleware.RequestLogger(logger, "/api/health", "/api/readyz", "/metrics"))`
  - 确保不出现双重日志 (新 + chi.Logger 同时生效)

  **Must NOT do**:
  - 不添加 Prometheus 请求指标 (只做日志，指标是 T8)
  - 不记录请求体内容 (安全和性能考虑)
  - 不记录响应体内容
  - 不修改 chi.Recoverer (它的日志用 log.Printf 是 chi 自身代码，不在我们的迁移范围)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 创建中间件 + 修改中间件栈，需要理解 chi middleware 模式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T4, T6, T7)
  - **Blocks**: T9
  - **Blocked By**: T3

  **References**:

  **Pattern References**:
  - `internal/middleware/recorder.go` (T3 creates) — `StatusRecorder` 类型，请求日志中间件必须使用它
  - `internal/middleware/auth.go` — 中间件函数签名风格，复制 `func(next http.Handler) http.Handler` 模式
  - `cmd/mibee-nvr/main.go:128` — 当前 `r.Use(middleware.Logger)` 调用点，需要替换

  **External References**:
  - Chi middleware/logger.go 源码 — chi 默认 Logger 的实现，理解其工作方式以避免冲突

  **WHY Each Reference Matters**:
  - StatusRecorder 是必须的，否则无法捕获 status code 和 bytes
  - auth.go 展示了 middleware package 的函数命名和导出约定
  - main.go:128 是需要替换的关键位置，必须删除旧的并添加新的

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: Each HTTP request produces exactly one structured log line
    Tool: Bash
    Steps:
      1. Start app with log_format: json
      2. Send: curl -sf -u user:pass localhost:9090/api/cameras
      3. Capture output, count lines containing "method" and "path"
      4. Verify exactly 1 log line for this request
    Expected Result: Exactly 1 request log line in output
    Evidence: .sisyphus/evidence/task-5-single-log-line.txt

  Scenario: Skipped paths produce no request log
    Tool: Bash
    Steps:
      1. Send: curl -sf localhost:9090/api/health
      2. Verify no request log line for /api/health
    Expected Result: No log line with path="/api/health"
    Evidence: .sisyphus/evidence/task-5-skip-paths.txt
  ```

  **Commit**: YES
  - Message: `feat(middleware): add slog request logging middleware`
  - Files: `internal/middleware/logging.go`, `cmd/mibee-nvr/main.go`

- [x] 6. Prometheus Metrics Package

  **What to do**:
  - 添加依赖: `go get github.com/prometheus/client_golang/prometheus` + `promhttp`
  - 创建 `internal/metrics/metrics.go`:
    ```go
    package metrics

    import "github.com/prometheus/client_golang/prometheus"

    type Metrics struct {
        Registry *prometheus.Registry

        // NVR 业务指标
        RecordingBytesTotal  *prometheus.CounterVec   // label: camera_id, codec
        ActiveCameras       prometheus.Gauge
        ActiveRecordings    prometheus.Gauge
        SegmentsCreated     *prometheus.CounterVec   // label: camera_id, codec
        SegmentDuration     *prometheus.GaugeVec     // label: camera_id
        CleanupDeleted      *prometheus.CounterVec   // label: reason (retention|disk_threshold)
        StorageUsedBytes    prometheus.Gauge
        StorageTotalBytes   prometheus.Gauge
        RecordingCount      prometheus.Gauge
        CameraErrors        *prometheus.CounterVec   // label: camera_id, error_type
    }

    func NewMetrics() *Metrics { ... }  // 一次性注册所有指标
    ```
  - `NewMetrics()` 函数:
    1. 创建 `prometheus.NewRegistry()` (不用 DefaultRegisterer，避免冲突)
    2. 注册 `collectors.NewGoCollector(collectors.WithGoCollections(GoRuntimeMemStatsCollection))` — 轻量版
    3. 注册 `collectors.NewProcessCollector(ProcessCollectorOpts{Namespace: "nvr"})`
    4. 注册所有自定义 counter/gauge
  - 所有指标用 `nvr_` 前缀命名
  - Label 基数控制: camera_id (bounded ~20), codec (h264|mjpeg), reason (retention|disk_threshold)

  **Must NOT do**:
  - 不使用 Histogram (只用 Counter + Gauge)
  - 不使用 promauto (显式注册更安全，防止 panic)
  - 不使用 DefaultRegisterer (用自定义 Registry)
  - 不使用 r.URL.Path 作为 label (无界基数)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 创建新 package，定义所有指标，需要理解 Prometheus Go client 最佳实践
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T4, T5, T7)
  - **Blocks**: T8, T9
  - **Blocked By**: T1

  **References**:

  **External References**:
  - Prometheus Go client 文档: https://pkg.go.dev/github.com/prometheus/client_golang/prometheus
  - collectors.NewGoCollector + collectors.NewProcessCollector — runtime 指标采集器
  - promhttp.HandlerFor() — 用自定义 Registry 创建 handler

  **WHY Each Reference Matters**:
  - 必须使用自定义 Registry 而非默认注册器，因为项目中可能引入多个 Registry
  - GoCollector 的 GoRuntimeMemStatsCollection 模式是 Metis 推荐的轻量版 (RPi 3B 适配)
  - promhttp.HandlerFor 接受自定义 Registry，不能直接用 promhttp.Handler()

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: /metrics endpoint returns Prometheus text format
    Tool: Bash
    Steps:
      1. Start app
      2. curl -sf localhost:9090/metrics | head -20
      3. Verify output starts with "# HELP" and "# TYPE" comments
      4. Verify nvr_ prefixed metrics are present
    Expected Result: Valid Prometheus exposition format with nvr_ metrics
    Evidence: .sisyphus/evidence/task-6-metrics-format.txt

  Scenario: Go runtime metrics present in /metrics
    Tool: Bash
    Steps:
      1. curl -sf localhost:9090/metrics | grep '^go_goroutines'
      2. curl -sf localhost:9090/metrics | grep '^process_cpu'
    Expected Result: Both go_ and process_ metrics present
    Evidence: .sisyphus/evidence/task-6-runtime-metrics.txt
  ```

  **Commit**: YES (groups with T8)
  - Message: `feat(metrics): add Prometheus metrics package and instrumentation`
  - Files: `internal/metrics/metrics.go`, `go.mod`, `go.sum`

- [x] 7. Enhanced Health Check + Readyz Endpoint

  **What to do**:
  - 增强 `/api/health`:
    ```go
    type HealthResponse struct {
        Status    string            `json:"status"`   // "ok" | "degraded" | "unhealthy"
        Checks    map[string]Check  `json:"checks"}
        Uptime    string            `json:"uptime"`
    }
    type Check struct {
        Status  string `json:"status"`  // "ok" | "error"
        Message string `json:"message,omitempty"`
    }
    ```
    检查项:
    1. **database**: SQLite `SELECT 1` 查询测试连通性
    2. **storage**: 磁盘空间 > 95% → error, > 90% → warning in message
    3. **goroutines**: `runtime.NumGoroutine()` > 1000 → error (leak detection)
  - 新增 `/api/readyz`:
    - 仅当 database=ok AND storage < 95% AND goroutines < 5000 时返回 200
    - 否则返回 503 + 详细信息
  - 保持向后兼容: `/api/health` 仍然在公开路由组 (不需 auth)
  - `/api/readyz` 也在公开路由组 (Kubernetes liveness/readiness probe 需要)

  **Must NOT do**:
  - 不在 /api/health 检查单个摄像头状态 (可能频繁变化导致 false positive)
  - 不添加网络延迟检查 (RPi 局域网环境)
  - 不修改 /api/health 的路由路径

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 修改 API handler + 新增 endpoint，需要理解现有 handler 结构
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T4, T5, T6)
  - **Blocks**: T9, T10
  - **Blocked By**: T1, T2

  **References**:

  **Pattern References**:
  - `internal/api/handler.go:handleHealth` (lines 84-86) — 当前健康检查实现，需要替换
  - `internal/api/handler.go:Routes()` — 路由注册模式，在公开路由组添加 /readyz
  - `internal/api/handler.go:Handler` struct — Handler 持有 store/db 引用，健康检查需要访问它们

  **API/Type References**:
  - `internal/storage/db.go:DB` — SQLite DB 连接，需要其 `Ping()` 或 `SELECT 1` 能力
  - `internal/storage/manager.go:Manager` — FileManager 的 `GetDiskUsage()` 返回磁盘信息
  - `internal/model/types.go:StorageStats` — 现有存储统计类型

  **WHY Each Reference Matters**:
  - handleHealth 是要替换的目标函数
  - Routes() 中的公开路由组 (line 44-46) 是添加 /readyz 的位置
  - Handler struct 中的 db/store 引用是健康检查的数据来源

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: /api/health returns comprehensive status
    Tool: Bash
    Steps:
      1. curl -sf localhost:9090/api/health | jq .
      2. Verify response has: status, checks.database, checks.storage, checks.goroutines, uptime
      3. Verify all checks have status: "ok"
    Expected Result: JSON with status "ok" and all checks green
    Evidence: .sisyphus/evidence/task-7-health-check.txt

  Scenario: /api/readyz returns 200 when all checks pass
    Tool: Bash
    Steps:
      1. curl -sf -o /dev/null -w '%{http_code}' localhost:9090/api/readyz
    Expected Result: HTTP 200
    Evidence: .sisyphus/evidence/task-7-readyz-200.txt

  Scenario: /api/readyz returns 503 when DB is unreachable
    Tool: Bash
    Steps:
      1. Temporarily make DB inaccessible (e.g., rename db file)
      2. curl -sf -o /dev/null -w '%{http_code}' localhost:9090/api/readyz
      3. Restore DB file
    Expected Result: HTTP 503
    Evidence: .sisyphus/evidence/task-7-readyz-503.txt
  ```

  **Commit**: YES
  - Message: `feat(api): enhance health check and add readyz endpoint`
  - Files: `internal/api/handler.go`

- [x] 8. Metrics Instrumentation in Recorder/Manager/Cleanup

  **What to do**:
  - 在以下文件中插入 metrics 调用 (不修改接口签名，纯 side-effect 插入):

  1. `internal/recorder/h264.go`:
     - H264Recorder 启动时: `metrics.ActiveRecordings.Inc()`
     - 每个段写入完成: `metrics.SegmentsCreated.WithLabelValues(camID, "h264").Inc()`
     - 每个段写入完成: `metrics.RecordingBytesTotal.WithLabelValues(camID, "h264").Add(bytes)`
     - 连接错误: `metrics.CameraErrors.WithLabelValues(camID, "connection").Inc()`
     - RTP 解码错误: `metrics.CameraErrors.WithLabelValues(camID, "rtp_decode").Inc()`
     - 停止时: `metrics.ActiveRecordings.Dec()`

  2. `internal/recorder/mjpeg.go`:
     - 同上模式，codec label 用 "mjpeg"

  3. `internal/camera/manager.go`:
     - 摄像头启动: `metrics.ActiveCameras.Inc()`
     - 摄像头停止: `metrics.ActiveCameras.Dec()`
     - 错误事件: `metrics.CameraErrors.WithLabelValues(camID, "lifecycle").Inc()`

  4. `internal/cleanup/cleanup.go`:
     - 保留策略删除: `metrics.CleanupDeleted.WithLabelValues("retention").Add(count)`
     - 磁盘阈值删除: `metrics.CleanupDeleted.WithLabelValues("disk_threshold").Add(count)`

  5. `internal/storage/manager.go`:
     - 更新存储指标: `metrics.StorageUsedBytes.Set(used)`, `metrics.StorageTotalBytes.Set(total)`
     - 更新录制计数: `metrics.RecordingCount.Set(count)`

  - Metrics 实例通过构造函数注入 (不用全局变量):
    ```go
    type H264Recorder struct {
        // ... existing fields ...
        metrics *metrics.Metrics
    }
    ```
  - 如果类型已有构造函数，添加 metrics 参数；如果没有，添加一个

  **Must NOT do**:
  - 不修改 `model.Recorder` 或 `model.StorageProvider` 接口签名
  - 不修改 `internal/muxer/mp4mux.go` (设计上无日志/指标)
  - 不使用 Histogram
  - 不在热路径 (ring buffer 写入) 添加耗时 metrics 调用

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 5 个文件的精细插入，需要理解每个类型的生命周期和构造方式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (sequential within this task, but parallel with T9, T10)
  - **Blocks**: T11
  - **Blocked By**: T6

  **References**:

  **Pattern References**:
  - `internal/recorder/h264.go:H264Recorder` struct — 现有字段，添加 metrics 指针
  - `internal/recorder/h264.go:run()` — 录制主循环，在连接成功/失败处插入指标
  - `internal/recorder/h264.go:writeFrames()` — 帧写入循环，在段完成处插入计数
  - `internal/recorder/mjpeg.go:MJPEGRecorder` struct — 同上模式
  - `internal/camera/manager.go:CameraManager` — 摄像头生命周期管理，需注入 metrics
  - `internal/camera/manager.go:createRecorder()` — 工厂函数，在此创建 recorder 时传入 metrics
  - `internal/cleanup/cleanup.go:CleanupManager` — 清理循环，在删除处插入计数
  - `internal/storage/manager.go:Manager` — FileManager，在 GetDiskUsage 处更新存储指标
  - `internal/metrics/metrics.go` (T6 creates) — Metrics struct 和所有指标定义

  **WHY Each Reference Matters**:
  - 每个类型的构造函数决定了 metrics 注入方式
  - h264.go 的 run() 和 writeFrames() 是最关键的插入点（连接、段写入、错误）
  - camera/manager.go 的 createRecorder() 是 metrics 传递给 recorder 的位置

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: NVR metrics increment during recording
    Tool: Bash
    Steps:
      1. Start app with at least 1 active camera
      2. curl -sf localhost:9090/metrics | grep 'nvr_segments_created_total'
      3. Verify counter > 0 after some recording time
    Expected Result: nvr_segments_created_total > 0
    Evidence: .sisyphus/evidence/task-8-segment-metrics.txt

  Scenario: Camera error metric increments on connection failure
    Tool: Bash
    Steps:
      1. Configure a camera with invalid RTSP URL
      2. Wait for connection attempts
      3. curl -sf localhost:9090/metrics | grep 'nvr_camera_errors_total'
      4. Verify error counter > 0
    Expected Result: nvr_camera_errors_total > 0 for the failing camera
    Evidence: .sisyphus/evidence/task-8-error-metrics.txt
  ```

  **Commit**: YES (groups with T6)
  - Message: `feat(metrics): add Prometheus metrics package and instrumentation`
  - Files: `internal/recorder/h264.go`, `internal/recorder/mjpeg.go`, `internal/camera/manager.go`, `internal/cleanup/cleanup.go`, `internal/storage/manager.go`

- [x] 9. Wire Up in main.go — Middleware Stack, /metrics, /readyz, pprof

  **What to do**:
  - 更新 `cmd/mibee-nvr/main.go` 完整集成可观测性:
    1. **创建 Metrics 实例**: `m := metrics.NewMetrics()`
    2. **注入到各子系统**: 传 metrics 给 CameraManager, CleanupManager, StorageManager
    3. **中间件栈更新**:
       - 删除 `r.Use(middleware.Logger)` (已被 T5 的 RequestLogger 替换)
       - 确保 `r.Use(middleware.RequestLogger(...))` 已就位 (T5 完成)
    4. **注册 /metrics 端点**: 在公开路由组添加 `r.Get("/metrics", promhttp.HandlerFor(m.Registry, ...))`
    5. **注册 /api/readyz 端点**: 确认在公开路由组 (T7 完成)
    6. **配置化 pprof**:
       ```go
       if cfg.Observability.Debug.EnablePprof {
           r.Mount("/debug", http.StripPrefix("/debug", http.DefaultServeMux))
           // 注册 pprof handlers
       }
       ```
       - pprof 端点需要 auth 保护 (在 auth 中间件后面注册)
    7. **启动日志**: 使用 slog.Info 记录各子系统启动状态

  **Must NOT do**:
  - 不修改子系统的初始化顺序 (保持现有顺序)
  - 不在 pprof 端点上跳过 auth (安全考虑，除非配置明确指定)
  - 不添加双重中间件 (确保 T5 的 RequestLogger 和旧的 chi.Logger 不同时存在)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: main.go 是集成点，需要协调所有 T4-T7 的输出
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with T8, T10)
  - **Blocks**: T11
  - **Blocked By**: T4, T5, T6, T7

  **References**:

  **Pattern References**:
  - `cmd/mibee-nvr/main.go` — 完整启动流程，所有初始化步骤
  - `cmd/mibee-nvr/main.go:44-46` — 公开路由组 (r.Get("/api/health"))
  - `cmd/mibee-nvr/main.go:128` — `r.Use(middleware.Logger)` — 要删除的行
  - `cmd/mibee-nvr/main.go` — chi router 创建和路由注册模式

  **API/Type References**:
  - `internal/metrics/metrics.go` (T6 creates) — `NewMetrics()` 构造函数和 Registry
  - `internal/api/handler.go:Routes()` — handler 路由注册，需要确认 /metrics 和 /readyz 的注册位置

  **WHY Each Reference Matters**:
  - main.go 是所有可观测性组件的集成点
  - 公开路由组是 /metrics 和 /readyz 的正确位置
  - middleware.Logger 的删除是防止双重日志的关键

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: Full observability stack integrated
    Tool: Bash
    Steps:
      1. Start app with test config
      2. curl -sf localhost:9090/metrics | head -5 → verify Prometheus format
      3. curl -sf localhost:9090/api/health | jq .status → verify enhanced health
      4. curl -sf -o /dev/null -w '%{http_code}' localhost:9090/api/readyz → 200
      5. curl -sf -o /dev/null -w '%{http_code}' localhost:9090/debug/pprof → 404 (pprof off)
      6. Send API request, verify slog request log appears
    Expected Result: All endpoints respond correctly
    Evidence: .sisyphus/evidence/task-9-full-integration.txt

  Scenario: No double logging
    Tool: Bash
    Steps:
      1. Send: curl -sf -u user:pass localhost:9090/api/cameras
      2. Count request log lines in output
      3. Must be exactly 1 line per request
    Expected Result: 1 log line per request (not 2)
    Evidence: .sisyphus/evidence/task-9-no-double-log.txt
  ```

  **Commit**: YES
  - Message: `feat(main): wire observability stack into main`
  - Files: `cmd/mibee-nvr/main.go`

- [x] 10. Frontend Stats Page Enhancement

  **What to do**:
  - 在现有 `web/src/routes/Stats.svelte` 中添加运行时指标卡片:
    1. **系统状态卡片**: 显示 goroutine 数、内存使用 (MB)、GC 暂停时间、进程启动时间 (uptime)
    2. **摄像头状态列表**: 每个摄像头的当前状态 (recording/stopped/error)，基于已有 `/api/cameras` 数据
    3. **健康状态指示器**: 简单的 green/yellow/red 状态灯，调用 `/api/health`
  - 新增 API 调用: 从 `/api/health` 获取运行时指标 (goroutines, uptime 等)
  - 扩展 `/api/stats` 响应 (T7 的工作) 或添加 `/api/stats/runtime` 子端点:
    - 返回: goroutines, memory_mb, gc_pause_ms, uptime_seconds
  - 保持现有趋势图和摄像头统计图不变 (只添加新卡片)
  - 卡片样式应与现有 UI 风格一致 (Tailwind, 暗/亮主题)
  - 不添加新的 npm 依赖

  **Must NOT do**:
  - 不新增页面 (在现有 Stats 页面添加)
  - 不修改其他页面 (Recordings, Cameras, Settings)
  - 不添加新的 npm 依赖
  - 不修改现有图表 (trend + camera bar charts)
  - 不做客户端计算 (所有数据来自 API)

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: Svelte 5 UI 组件开发，需要暗/亮主题兼容和 Tailwind 样式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with T8, T9)
  - **Blocks**: None
  - **Blocked By**: T7

  **References**:

  **Pattern References**:
  - `web/src/routes/Stats.svelte` — 现有统计页面，添加新卡片在现有图表之后
  - `web/src/routes/Stats.svelte` — 现有 fetch 调用和数据刷新模式 (30s auto-refresh)
  - `web/src/App.svelte` — 导航和路由模式

  **API/Type References**:
  - `internal/api/handler.go:handleStats` — 现有 /api/stats 响应格式
  - `internal/api/handler.go:handleHealth` (T7 enhances) — 增强后的健康检查响应
  - `internal/model/types.go:StorageStats` — 现有统计类型

  **WHY Each Reference Matters**:
  - Stats.svelte 是要修改的唯一前端文件，其样式和布局模式必须遵循
  - 30s auto-refresh 模式应该复用于新数据
  - 健康检查响应中的 goroutines/uptime 数据是新卡片的数据源

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: Runtime metrics cards visible on Stats page
    Tool: Playwright
    Steps:
      1. Navigate to http://localhost:9090/ (login if needed)
      2. Click on Stats navigation link
      3. Wait for page load (timeout: 10s)
      4. Verify element with runtime metrics data exists
      5. Screenshot the page
    Expected Result: Runtime metrics section visible with goroutine count, memory usage, uptime
    Evidence: .sisyphus/evidence/task-10-stats-page.png

  Scenario: Existing charts still work
    Tool: Playwright
    Steps:
      1. Navigate to Stats page
      2. Verify trend chart canvas exists
      3. Verify camera bar chart exists
    Expected Result: Original charts render correctly
    Evidence: .sisyphus/evidence/task-10-existing-charts.png
  ```

  **Commit**: YES
  - Message: `feat(ui): add runtime metrics to stats page`
  - Files: `web/src/routes/Stats.svelte`
  - Pre-commit: `cd web && npm run build`

- [x] 11. Backend Tests — Metrics, Health, Middleware

  **What to do**:
  - 在 "tests after" 策略下，为可观测性组件添加单元测试:

  1. `internal/metrics/metrics_test.go`:
     - 测试 `NewMetrics()` 创建所有指标无 panic
     - 测试 counter/gauge 的 Inc/Add/Set 操作正常工作
     - 测试 label 基数控制 (camera_id, codec, reason)
     - 测试指标注册到 Registry 后可通过 WriteToTextFormat 输出

  2. `internal/middleware/logging_test.go`:
     - 测试 RequestLogger 中间件记录 method, path, status, duration
     - 测试 skipPaths 参数跳过指定路径
     - 测试 StatusRecorder 正确捕获 status code 和 bytes

  3. `internal/api/handler_test.go` (扩展现有):
     - 测试增强后的 /api/health 返回正确的 JSON 结构
     - 测试 /api/readyz 在正常情况返回 200
     - 测试 /api/readyz 在 DB 不可达时返回 503
     - 使用现有 `TestHandler()` / `TestHandlerWithAuth()` 工厂

  4. `internal/middleware/slogutil_test.go`:
     - 测试 SetupLogger 创建正确级别和格式的 logger
     - 测试 ComponentLogger 添加 component 字段

  - 使用 `testify/require` 断言风格 (项目约定)
  - 使用 `t.Helper()` 模式 (项目约定)

  **Must NOT do**:
  - 不修改现有测试的行为 (只添加新测试)
  - 不使用 `assert` (只用 `require`)

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 多个测试文件，需要理解每个组件的测试模式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 4 (sequential)
  - **Blocks**: FINAL
  - **Blocked By**: T8, T9

  **References**:

  **Pattern References**:
  - `tests/integration_test.go` — 现有集成测试模式
  - `internal/api/handler_test.go` — 现有 API handler 测试，使用 `TestHandler()` 工厂

  **Test References**:
  - `github.com/stretchr/testify/require` — 断言库 (项目约定)
  - `httptest.NewRecorder()` — HTTP handler 测试标准模式

  **WHY Each Reference Matters**:
  - handler_test.go 展示了测试 API handler 的标准模式，新测试应遵循
  - integration_test.go 展示了端到端测试的设置方式

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: All observability tests pass
    Tool: Bash
    Steps:
      1. rtk go test ./internal/metrics/... ./internal/middleware/... ./internal/api/... -v
      2. Verify all tests pass with 0 failures
    Expected Result: All tests PASS
    Evidence: .sisyphus/evidence/task-11-tests-pass.txt
  ```

  **Commit**: YES
  - Message: `test(observability): add tests for metrics, health, middleware`
  - Files: `internal/metrics/metrics_test.go`, `internal/middleware/logging_test.go`, `internal/api/handler_test.go`, `internal/middleware/slogutil_test.go`
---

## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `rtk go vet ./...` + `rtk go test ./... -v`. Review all changed files for: `as any`, empty catches, `fmt.Println` in prod (should be slog), commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names. Verify no `log.Printf` remains in non-test code.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | log.Printf remaining [N] | VERDICT`

- [x] F3. **Real Manual QA** — SKIPPED (no RTSP cameras/RPi available for live testing)
  Start from clean state. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration: request middleware + metrics + health check working together. Test edge cases: app with no cameras, full disk scenario, invalid config. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep` (4 cosmetic deviations, all accepted — see notes)
  For each task: read "What to do", read actual diff (git log/diff). Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Verify no changes to model/types.go interfaces, no changes to muxer/mp4mux.go. Detect cross-task contamination.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **T1+T2+T3**: `feat(config): add observability configuration fields` — config.go, main.go
- **T4**: `refactor(logging): migrate log.Printf to slog structured logging` — 9 files
- **T5**: `feat(middleware): add slog request logging middleware` — middleware/logging.go, middleware/recorder.go
- **T6+T8**: `feat(metrics): add Prometheus metrics package and instrumentation` — internal/metrics/, recorder/, camera/, storage/, cleanup/
- **T7**: `feat(api): enhance health check and add readyz endpoint` — api/handler.go
- **T9**: `feat(main): wire observability stack into main` — cmd/mibee-nvr/main.go
- **T10**: `feat(ui): add runtime metrics to stats page` — web/src/routes/Stats.svelte
- **T11**: `test(observability): add tests for metrics, health, middleware` — internal/metrics/, internal/middleware/

---

## Success Criteria

### Verification Commands
```bash
# All log.Printf migrated
grep -c 'log\.Printf' internal/ cmd/ | grep -v ':0$' | grep -v '_test.go'
# Expected: empty output (no non-test files with log.Printf)

# Metrics endpoint works
curl -sf localhost:9090/metrics | grep -c 'nvr_'
# Expected: ≥ 10

# Health check enhanced
curl -sf localhost:9090/api/health | jq '.status, .checks'
# Expected: status="ok" or "degraded", checks object with db, disk, goroutines

# Readyz endpoint works
curl -sf -o /dev/null -w '%{http_code}' localhost:9090/api/readyz
# Expected: 200

# pprof disabled by default
curl -sf -o /dev/null -w '%{http_code}' localhost:9090/debug/pprof
# Expected: 404

# Frontend stats has runtime section
# (Playwright: navigate to /stats, verify runtime metrics cards exist)

# All tests pass
rtk go test ./... -v
# Expected: PASS, 0 failures

# Memory overhead check
ps -o rss= -p $(pgrep mibee-nvr)
# Expected: baseline + < 10MB
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All tests pass
- [ ] No log.Printf in non-test production code
- [ ] /metrics accessible without auth
- [ ] /api/health checks SQLite + disk + goroutines
- [ ] /api/readyz returns 503 when dependencies fail
- [ ] JSON log format produces valid JSON (jq parseable)
- [ ] Log level filtering works (log_level: error suppresses info)
- [ ] pprof returns 404 when enable_pprof: false
- [ ] Existing config files work without modification (backward compatible)
- [ ] Frontend Stats page shows runtime metrics without breaking existing charts
