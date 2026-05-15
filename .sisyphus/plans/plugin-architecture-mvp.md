# Plugin Architecture Implementation — MVP

## TL;DR

> **Quick Summary**: 实现基于 gRPC 的插件进程隔离架构，将 Xiaomi 插件迁移为独立进程，并构建完整的插件管理 UI。主进程通过 gRPC 流式接收 NAL 帧、控制 Muxer 和 Segment 生命周期。内置记录器保留在主进程，双模式共存。
> 
> **Deliverables**:
> - Protocol Buffers SDK（NAL 帧流式传输接口）
> - gRPC 插件框架 + PluginManager（生命周期、健康检查、自动重启）
> - FrameReceiver 服务（主进程接收帧 + Muxer + Segment 管理）
> - gRPCRecorderAdapter（实现 model.Recorder 接口）
> - Xiaomi 插件独立进程重构
> - Camera manager 双模式分派
> - Config 扩展 + API 扩展（插件 CRUD、状态、能力查询）
> - 完整插件管理 UI（动态协议、状态页、发现面板抽象）
> - 双二进制构建系统
> - 架构文档更新
> 
> **Estimated Effort**: XL (5-6 waves, ~25 tasks)
> **Parallel Execution**: YES - 6 waves
> **Critical Path**: Verification → Proto SDK → FrameReceiver + PluginManager → Xiaomi Refactor → Camera Manager → API → UI

---

## Context

### Original Request
将 `docs/private/plugin-architecture.md` 的设计方案落地为最小可行方案，fork `feature/plugin-architecture` 分支实现。包含 Web UI 侧的完整插件管理界面。

### Interview Summary
**Key Discussions**:
- **范围**: 最小可行方案 — SDK + Xiaomi 迁移 + 完整 UI，不含内置记录器迁移和高级功能
- **数据流**: gRPC 流式传输（IDR 帧分组 NAL 单元），主进程控制 Muxer 和 Segment
- **内置记录器**: 保留在主进程，双模式共存
- **前端**: 坚持完整插件管理 UI（动态协议选择器 + 状态页 + 发现面板抽象）
- **gRPC 方案**: 先验证 HashiCorp go-plugin CGO_ENABLED=0，不行回退 raw gRPC
- **测试**: TDD

**Research Findings**:
- Xiaomi 插件导入 6 个 internal 包（config, metrics, model, plugin, storage, muxer）
- MP4Muxer 是最紧耦合点 — 4 个 recorder + Xiaomi 直接使用
- Xiaomi 协议层（miss.go, cs2.go, crypto.go）零内部依赖
- 前端有 180+ 处硬编码协议逻辑
- Plugin API 已存在但未使用
- `api/handler.go` 和 `cmd/mibee-nvr/main.go` 直接 import `plugins/xiaomi`

### Metis Review
**Identified Gaps** (addressed):
- Proto 定义与实际需求不符：从头发设计，不沿用文档 proto
- 构建系统需要双二进制：加入 Wave 1
- 前端动态协议选择器工作量大：坚持完整 UI 但合理分任务
- HashiCorp go-plugin CGO 兼容性未验证：加验证任务
- Xiaomi cloud auth 边界问题：主进程代理认证，插件只接收凭证
- Config 向后兼容：自动迁移 `xiaomi:` section 到 `plugins.xiaomi`

---

## Work Objectives

### Core Objective
实现 gRPC 进程隔离插件架构，完成 Xiaomi 插件迁移并验证端到端流程，同时构建完整的前端插件管理界面。

### Concrete Deliverables
- `plugin/proto/*.proto` — Protocol Buffers 定义
- `plugin/proto/gen/*.go` — 生成的 Go 代码
- `internal/plugin/grpc_manager.go` — PluginManager（进程生命周期）
- `internal/plugin/frame_receiver.go` — FrameReceiver 服务
- `internal/plugin/grpc_adapter.go` — gRPCRecorderAdapter
- `plugins/xiaomi/cmd/xiaomi-plugin/main.go` — 插件入口
- `plugins/xiaomi/grpc_server.go` — gRPC 服务端实现
- `plugins/xiaomi/grpc_recorder.go` — 重构后的 recorder（gRPC 流式发送）
- `web/src/routes/Plugins.svelte` — 插件管理页面
- 更新 `Cameras.svelte`, `Dashboard.svelte`, `LiveView.svelte`, `Settings.svelte`
- 更新 `Makefile`, `Dockerfile`, `deploy/`
- 更新 `docs/private/plugin-architecture.md`

### Definition of Done
- [ ] `make build` 产出 2 个二进制：`mibee-nvr` + `plugins/xiaomi/xiaomi-plugin`
- [ ] `make cross` 交叉编译 2 个 ARM64 二进制
- [ ] `go test ./...` 全部通过
- [ ] Xiaomi 相机通过 gRPC 插件正常录制
- [ ] 插件进程 kill -9 后主进程不崩溃，自动重启
- [ ] 前端插件管理页面可查看插件状态
- [ ] 前端协议选择器动态加载
- [ ] 现有 xiaomi: 配置自动兼容

### Must Have
- gRPC over Unix Domain Socket
- 插件崩溃隔离 + 自动重启
- NAL 帧流式传输（IDR 分组）
- 主进程控制 Segment 生命周期
- 完整的前端插件管理 UI
- 向后兼容现有配置
- Mock plugin 用于集成测试
- 双二进制构建系统

### Must NOT Have (Guardrails)
- ❌ 不迁移内置记录器（RTSP/HTTP/ONVIF）
- ❌ 不实现热重载
- ❌ 不实现 cgroups 资源限制
- ❌ 不实现插件签名验证
- ❌ 不改变数据库 schema
- ❌ 不改变现有 `model.Recorder` 接口
- ❌ 不改变现有 `init()`/`plugin.Register()` 机制
- ❌ 不用 `context.TODO()` 或 `time.Sleep()` 做插件就绪等待
- ❌ 不创建独立 Go module 做 plugin SDK（保持 internal）
- ❌ 不在 proto 里添加 config_schema RPC（MVP 不需要）
- ❌ 不滥用抽象层（一个 adapter 够了）

---

## Verification Strategy (MANDATORY)

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (go test)
- **Automated tests**: TDD
- **Framework**: go test
- **TDD**: Each task follows RED → GREEN → REFACTOR

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Go 服务**: Use Bash (go test) — Unit + integration tests
- **API**: Use Bash (curl) — Request/response verification
- **Frontend**: Use Playwright — Navigate, interact, assert DOM, screenshot
- **Build**: Use Bash (make) — Build verification, binary checks

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 0 (Start Immediately - validation spike):
└── Task 1: Validate gRPC approach on CGO_ENABLED=0 [deep]

Wave 1 (After Wave 0 - foundation):
├── Task 2: Proto SDK definition [deep]
├── Task 3: Build system updates (dual binary) [quick]
└── Task 4: Config schema extension [quick]

Wave 2 (After Wave 1 - core framework):
├── Task 5: FrameReceiver service (depends: 2) [deep]
├── Task 6: PluginManager lifecycle (depends: 2, 3) [deep]
├── Task 7: gRPCRecorderAdapter (depends: 2, 5) [unspecified-high]
└── Task 8: Mock plugin for testing (depends: 2) [quick]

Wave 3 (After Wave 2 - Xiaomi migration):
├── Task 9: Xiaomi plugin gRPC server (depends: 2, 8) [deep]
├── Task 10: Xiaomi recorder refactor to streaming (depends: 5, 9) [deep]
├── Task 11: Xiaomi cloud auth boundary (depends: 6) [unspecified-high]
└── Task 12: Camera manager dual-mode dispatch (depends: 7) [deep]

Wave 4 (After Wave 3 - API + integration):
├── Task 13: Plugin API endpoints (depends: 6, 12) [unspecified-high]
├── Task 14: Plugin management UI page (depends: 13) [visual-engineering]
├── Task 15: Dynamic protocol selector + encoding (depends: 13) [visual-engineering]
├── Task 16: Plugin discovery panel abstraction (depends: 13) [visual-engineering]
└── Task 17: Architecture doc update (depends: all above) [writing]

Wave FINAL (After ALL tasks):
├── F1: Plan compliance audit (oracle)
├── F2: Code quality review (unspecified-high)
├── F3: Real manual QA (unspecified-high)
└── F4: Scope fidelity check (deep)

Critical Path: T1 → T2 → T5 → T9 → T10 → T12 → T13 → T14/T15/T16
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 5 (Wave 4)
```

### Dependency Matrix

| Task | Blocked By | Blocks |
|------|-----------|--------|
| 1 | - | 2 |
| 2 | 1 | 5, 6, 7, 8, 9 |
| 3 | 1 | 6 |
| 4 | 1 | 6 |
| 5 | 2 | 7, 10 |
| 6 | 2, 3, 4 | 11, 12, 13 |
| 7 | 2, 5 | 12 |
| 8 | 2 | 9 |
| 9 | 2, 8 | 10 |
| 10 | 5, 9 | 12 |
| 11 | 6 | 12 |
| 12 | 7, 10, 11 | 13 |
| 13 | 6, 12 | 14, 15, 16 |
| 14 | 13 | F1-F4 |
| 15 | 13 | F1-F4 |
| 16 | 13 | F1-F4 |
| 17 | all above | F1-F4 |

### Agent Dispatch Summary

- **Wave 0**: 1 — T1 → `deep`
- **Wave 1**: 3 — T2 → `deep`, T3 → `quick`, T4 → `quick`
- **Wave 2**: 4 — T5 → `deep`, T6 → `deep`, T7 → `unspecified-high`, T8 → `quick`
- **Wave 3**: 4 — T9 → `deep`, T10 → `deep`, T11 → `unspecified-high`, T12 → `deep`
- **Wave 4**: 5 — T13 → `unspecified-high`, T14-T16 → `visual-engineering`, T17 → `writing`
- **FINAL**: 4 — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

- [x] 1. **Validate gRPC approach for CGO_ENABLED=0**

  **What to do**:
  - 创建一个临时 spike 项目验证 HashiCorp go-plugin 在 CGO_ENABLED=0 下是否正常工作
  - 验证内容：gRPC 模式启动、Unix Domain Socket 通信、进程崩溃检测、日志转发
  - 验证 `github.com/hashicorp/go-plugin` + `google.golang.org/grpc` 不需要 CGO
  - 检查二进制大小影响（当前 ~20-30MB，gRPC 预期增加 ~10MB）
  - 在 ARM64 交叉编译环境下验证
  - 如果 HashiCorp go-plugin 不行，验证 raw gRPC + 手动进程管理的方案
  - **结论记录到** `.sisyphus/drafts/grpc-validation.md`

  **Must NOT do**:
  - 不写生产代码，这是纯验证 spike
  - 不改变任何现有代码

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 需要深入调研依赖链和编译约束，评估两种方案的优劣
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (blocks everything)
  - **Parallel Group**: Wave 0 (sequential)
  - **Blocks**: 2, 3, 4
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `go.mod` — 当前依赖列表，检查是否已有 gRPC 相关依赖
  - `Makefile` — 当前构建命令，验证 `CGO_ENABLED=0` 设置

  **External References**:
  - HashiCorp go-plugin 文档: https://github.com/hashicorp/go-plugin#go-plugin
  - 验证其 go.mod 是否包含 CGO 依赖
  - gRPC Go 文档: https://grpc.io/docs/languages/go/

  **Acceptance Criteria**:
  - [ ] Spike 项目成功编译并运行 (CGO_ENABLED=0)
  - [ ] gRPC over UDS 通信正常
  - [ ] 进程崩溃检测正常
  - [ ] 二进制大小增长 < 15MB
  - [ ] ARM64 交叉编译成功
  - [ ] 结论文档写入 `.sisyphus/drafts/grpc-validation.md`

  **QA Scenarios:**
  ```
  Scenario: HashiCorp go-plugin CGO_ENABLED=0 validation
    Tool: Bash
    Steps:
      1. Create spike project with hashicorp/go-plugin + gRPC
      2. CGO_ENABLED=0 go build -o /tmp/spike-plugin ./...
      3. Run binary, verify gRPC handshake works
      4. Kill plugin process, verify host detects crash
    Expected Result: All steps succeed without CGO
    Evidence: .sisyphus/evidence/task-1-grpc-validation.txt
  ```

  **Commit**: YES
  - Message: `feat(plugin): validate gRPC approach for CGO_ENABLED=0`
  - Files: spike code (can be in tmp/ or a separate branch commit)

---

- [x] 2. **Define Protocol Buffers SDK**

  **What to do**:
  - 创建 `plugin/proto/nvr.proto` 定义 gRPC 服务接口
  - **从头设计**（不沿用架构文档的错误 proto）
  - 核心服务定义:
    - `GetPluginInfo(Empty) → PluginInfo` — 插件元信息（name, version, protocols, capabilities）
    - `StartRecorder(RecorderConfig) → stream Frame` — 客户端流：插件发送 NAL 帧
    - `StopRecorder(StopRequest) → StopResponse` — 停止录制
    - `GetRecorderStatus(StatusRequest) → RecorderStatus` — 获取状态
    - `HealthCheck(Empty) → HealthCheckResponse` — 健康检查
  - 消息定义:
    - `Frame` — 包含: bytes, pts_ns, is_idr, codec(h264/h265/mjpeg), access_unit_data
    - `PluginInfo` — name, version, protocols[], capabilities{hls, ptz, snapshot, discovery}
    - `RecorderConfig` — camera_id, url, username, password, segment_duration, options{}
    - `RecorderStatus` — status enum, error_msg, bytes_recorded, segments_created
  - 配置 protoc 生成 Go 代码到 `plugin/proto/gen/`
  - 编写 `plugin/proto/gen.go` 的 go:generate 指令
  - TDD: 编写 proto 兼容性测试（消息序列化/反序列化）

  **Must NOT do**:
  - 不沿用架构文档的 StreamRecordings proto（它发 segment 元数据，不是 NAL 帧）
  - 不添加 config_schema RPC（MVP 不需要）
  - 不创建独立 Go module

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Proto 定义是整个架构的契约，需要深入理解数据流设计
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 3, 4)
  - **Parallel Group**: Wave 1
  - **Blocks**: 5, 6, 7, 8, 9
  - **Blocked By**: 1

  **References**:
  **Pattern References**:
  - `internal/model/types.go` — Recorder 接口、Recording 结构、Protocol/Format 类型
  - `internal/recorder/h264.go:processH264NALU()` — 理解 NAL 帧处理流程
  - `plugins/xiaomi/recorder.go:closeCurrentSegment()` — 理解 IDR 检测和 segment 生命周期

  **API/Type References**:
  - `internal/config/config.go:CameraConfig` — 需要映射到 RecorderConfig proto
  - `internal/metrics/metrics.go:Metrics` — 理解指标字段用于 status

  **External References**:
  - gRPC Go streaming: https://grpc.io/docs/languages/go/basics/#streaming-rpcs
  - protoc-gen-go: https://grpc.io/docs/languages/go/quickstart/

  **Acceptance Criteria**:
  - [ ] `plugin/proto/nvr.proto` 完整定义
  - [ ] `go generate ./plugin/proto/...` 成功生成 Go 代码
  - [ ] `go test ./plugin/proto/...` — 序列化/反序列化测试通过
  - [ ] Proto 包含 Frame 消息（含 bytes, pts_ns, is_idr, codec 字段）
  - [ ] Proto 包含 capabilities 子消息（hls, ptz, snapshot, discovery）

  **QA Scenarios:**
  ```
  Scenario: Proto generation and serialization
    Tool: Bash
    Steps:
      1. go generate ./plugin/proto/...
      2. Verify plugin/proto/gen/*.go files exist
      3. go test ./plugin/proto/... -v
      4. Verify Frame message serializes with 500KB payload
    Expected Result: Generation succeeds, all tests pass
    Evidence: .sisyphus/evidence/task-2-proto-gen.txt

  Scenario: Proto backward compatibility check
    Tool: Bash
    Steps:
      1. Add a new optional field to Frame message
      2. Verify old serialized data still deserializes
    Expected Result: No breaking changes
    Evidence: .sisyphus/evidence/task-2-proto-compat.txt
  ```

  **Commit**: YES
  - Message: `feat(plugin): define Protocol Buffers SDK for NAL frame streaming`
  - Files: plugin/proto/
  - Pre-commit: `go generate ./plugin/proto/... && go test ./plugin/proto/...`

---

- [x] 3. **Build system updates (dual binary)**

  **What to do**:
  - 更新 `Makefile` 添加插件构建目标:
    - `make plugin-xiaomi` — 构建 Xiaomi 插件二进制
    - `make plugins` — 构建所有插件
    - `make build` — 同时构建主程序和插件
    - `make cross` — 交叉编译主程序 + 所有插件 for ARM64
  - 添加 protoc 生成步骤:
    - `make proto` — 从 .proto 生成 Go 代码
    - 集成到 `make build` 流程
  - 更新 `Dockerfile` 和 `Dockerfile.arm64`:
    - 构建阶段包含 protoc + proto 生成
    - 最终镜像包含主程序 + plugins/ 目录
  - 更新 `deploy/` 脚本:
    - `make deploy` 部署主程序 + 插件
    - 插件部署到 `/mnt/data/nvr/plugins/xiaomi/`
  - 确保所有目标支持 `CGO_ENABLED=0`
  - TDD: `make build && ls -la mibee-nvr plugins/xiaomi/xiaomi-plugin`

  **Must NOT do**:
  - 不改变现有 `make build` 行为（主程序仍然正常构建）
  - 不引入复杂构建工具（如 bazel）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Makefile/Dockerfile 更新是明确的配置任务
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 2, 4)
  - **Parallel Group**: Wave 1
  - **Blocks**: 6
  - **Blocked By**: 1

  **References**:
  **Pattern References**:
  - `Makefile` — 当前构建目标，理解 $(GOARGS), $(LDFLAGS), cross-compile 设置
  - `Dockerfile` — 当前 Docker 构建流程
  - `Dockerfile.arm64` — 交叉编译 Docker 构建
  - `deploy/` — 当前部署脚本

  **Acceptance Criteria**:
  - [ ] `make build` 产出 `mibee-nvr` + `plugins/xiaomi/xiaomi-plugin`
  - [ ] `make cross` 交叉编译 ARM64 双二进制
  - [ ] `make proto` 生成 proto Go 代码
  - [ ] `file plugins/xiaomi/xiaomi-plugin` 显示正确的架构
  - [ ] Docker build 包含插件二进制

  **QA Scenarios:**
  ```
  Scenario: Dual binary build
    Tool: Bash
    Steps:
      1. make build
      2. ls -la mibee-nvr plugins/xiaomi/xiaomi-plugin
      3. file mibee-nvr plugins/xiaomi/xiaomi-plugin
    Expected Result: Two binaries, correct architecture, CGO_ENABLED=0
    Evidence: .sisyphus/evidence/task-3-dual-build.txt

  Scenario: ARM64 cross-compilation
    Tool: Bash
    Steps:
      1. make cross
      2. file mibee-nvr-arm64 plugins/xiaomi/xiaomi-plugin-arm64
    Expected Result: ELF 64-bit LSB executable, ARM aarch64
    Evidence: .sisyphus/evidence/task-3-cross-build.txt
  ```

  **Commit**: YES
  - Message: `build(plugin): dual binary build system (main + plugin)`
  - Files: Makefile, Dockerfile, Dockerfile.arm64, deploy/

---

- [x] 4. **Config schema extension**

  **What to do**:
  - 扩展 `internal/config/config.go` 添加 `PluginsConfig`:
    ```go
    type PluginsConfig struct {
      Directory string                     `yaml:"directory"`
      Plugins  map[string]PluginEntryConfig `yaml:"plugins"`
    }
    type PluginEntryConfig struct {
      Enabled bool                   `yaml:"enabled"`
      Path    string                 `yaml:"path"`
      Config  map[string]interface{} `yaml:"config"`
    }
    ```
  - 添加到 `Config` struct: `Plugins PluginsConfig yaml:"plugins"`
  - 默认值: `directory: "./plugins"`, plugins map 为空时自动发现
  - **向后兼容**: 如果 `XiaomiConfig` 存在但 `plugins.xiaomi` 不存在，自动生成虚拟插件配置
  - TDD: 测试配置解析、默认值、向后兼容

  **Must NOT do**:
  - 不删除现有 `XiaomiConfig` 结构
  - 不改变现有配置文件格式

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 明确的配置结构扩展
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 2, 3)
  - **Parallel Group**: Wave 1
  - **Blocks**: 6
  - **Blocked By**: 1

  **References**:
  **Pattern References**:
  - `internal/config/config.go:Config` — 根配置结构
  - `internal/config/config.go:XiaomiConfig` — 现有小米配置，需要向后兼容
  - `internal/config/config.go:Load()` — 配置加载逻辑

  **Acceptance Criteria**:
  - [ ] `PluginsConfig` 结构定义完整
  - [ ] 配置解析测试通过
  - [ ] 向后兼容测试：只有 `xiaomi:` section 时自动创建插件配置
  - [ ] 默认值正确（directory: ./plugins）

  **QA Scenarios:**
  ```
  Scenario: Config with new plugins section
    Tool: Bash
    Steps:
      1. Create test YAML with plugins: section
      2. Parse with config.Load()
      3. Verify Plugins.Plugins["xiaomi"].Enabled == true
    Expected Result: Config parsed correctly
    Evidence: .sisyphus/evidence/task-4-config-new.txt

  Scenario: Backward compatible config (xiaomi: only)
    Tool: Bash
    Steps:
      1. Create test YAML with only xiaomi: section (no plugins:)
      2. Parse with config.Load()
      3. Verify auto-generated plugins.xiaomi config exists
    Expected Result: Virtual plugin config created from xiaomi: section
    Evidence: .sisyphus/evidence/task-4-config-compat.txt
  ```

  **Commit**: YES
  - Message: `feat(config): add plugins section to YAML config schema`
  - Files: internal/config/config.go, internal/config/config_test.go

---

- [x] 5. **FrameReceiver service**

  **What to do**:
  - 创建 `internal/plugin/frame_receiver.go` — 主进程侧的帧接收服务
  - 核心职责:
    1. 接收 gRPC 流式 NAL 帧（来自插件进程）
    2. Codec 探测（H264/H265 检测，解析 SPS/PPS/VPS）
    3. IDR 帧边界检测（新 segment 触发点）
    4. MP4Muxer 生命周期管理（创建 track、write sample、close）
    5. Segment 生命周期（CreateSegment → write → CloseSegment → DB insert）
    6. 指标上报（SegmentsCreated, RecordingBytesTotal, ActiveRecordings）
  - 接口设计:
    ```go
    type FrameReceiver struct {
      store      SegmentStore      // 复用现有接口
      db         RecordingDB       // 复用现有接口
      metrics    *metrics.Metrics
      segDur     time.Duration
      cameraID   string
      // internal state: currentMuxer, currentSegment, codec info
    }
    func (r *FrameReceiver) HandleFrame(ctx context.Context, frame *proto.Frame) error
    ```
  - HandleFrame 逻辑:
    - 如果 frame.is_idr && 当前有 segment → 关闭当前 segment，创建新的
    - 如果无 muxer && 有 codec info → 创建 track + muxer
    - 写入 frame.bytes 到 muxer.WriteSample()
  - 复用现有 recorder 中定义的 `SegmentStore` 和 `RecordingDB` 接口
  - TDD: 先写测试用 mock 数据验证帧处理流程

  **Must NOT do**:
  - 不修改 `internal/muxer/` 包
  - 不修改 `internal/storage/` 包
  - 不改变 segment 文件命名/存储结构

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 这是整个架构的核心——帧接收+segment管理+Muxer集成，需要深入理解 NAL 帧处理
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 6, 7, 8)
  - **Parallel Group**: Wave 2
  - **Blocks**: 7, 10
  - **Blocked By**: 2

  **References**:
  **Pattern References**:
  - `internal/recorder/h264.go:processH264NALU()` — H264 NAL 处理逻辑，IDR 检测，segment 创建流程
  - `internal/recorder/h265.go:processH265NALU()` — H265 对等逻辑
  - `plugins/xiaomi/recorder.go:closeCurrentSegment()` — Segment 关闭+DB insert 模式
  - `plugins/xiaomi/recorder.go:SegmentStore` — 现有 SegmentStore 接口定义
  - `plugins/xiaomi/recorder.go:RecordingDB` — 现有 RecordingDB 接口定义

  **API/Type References**:
  - `internal/muxer/mp4mux.go:MP4Muxer` — Muxer API (AddH264Track, AddH265Track, WriteSample, Close)
  - `internal/model/types.go:Recording` — 录制元数据结构

  **Acceptance Criteria**:
  - [ ] FrameReceiver 处理 H264 NAL 帧序列（SPS → PPS → IDR → P → IDR → ...）
  - [ ] IDR 帧触发 segment 切割
  - [ ] Muxer 正确创建 H264/H265 track
  - [ ] Segment 文件正确创建、原子重命名、DB 插入
  - [ ] go test ./internal/plugin/... PASS

  **QA Scenarios:**
  ```
  Scenario: H264 frame sequence processing
    Tool: Bash (go test)
    Steps:
      1. Create FrameReceiver with mock store/db
      2. Send SPS + PPS + IDR frames
      3. Verify muxer created with H264 track
      4. Send 30 P-frames
      5. Send IDR frame (triggers segment close)
      6. Verify segment closed, new segment started
    Expected Result: 1 segment created, muxer tracks correct
    Evidence: .sisyphus/evidence/task-5-frame-processing.txt

  Scenario: Missing codec info handling
    Tool: Bash (go test)
    Steps:
      1. Send P-frame without prior SPS/PPS/IDR
    Expected Result: Frame buffered or discarded, no crash
    Evidence: .sisyphus/evidence/task-5-missing-codec.txt
  ```

  **Commit**: YES
  - Message: `feat(plugin): implement FrameReceiver service for NAL frame handling`
  - Files: internal/plugin/frame_receiver.go, internal/plugin/frame_receiver_test.go

---

- [x] 6. **PluginManager lifecycle**

  **What to do**:
  - 创建 `internal/plugin/grpc_manager.go` — 插件进程生命周期管理
  - 核心职责:
    1. 插件发现（扫描 plugins directory）
    2. 插件进程启动（exec.Command + gRPC 客户端连接）
    3. 健康检查（定期 HealthCheck RPC）
    4. 自动重启（崩溃检测 + 指数退避重启）
    5. 优雅停止（graceful shutdown）
    6. 插件注册表（name → PluginClient mapping）
  - 接口设计:
    ```go
    type PluginManager struct {
      config   *config.PluginsConfig
      plugins  map[string]*ManagedPlugin
      mu       sync.RWMutex
      logger   *slog.Logger
    }
    type ManagedPlugin struct {
      Name     string
      Client   proto.PluginServiceClient
      Cmd      *exec.Cmd
      Info      *proto.PluginInfo
      Status   string  // running, stopped, error
      StartedAt time.Time
    }
    ```
  - 启动流程:
    1. 扫描 plugin directory
    2. 对每个 enabled plugin: exec.Command 启动进程
    3. 连接 UDS → 创建 gRPC client
    4. HealthCheck → 确认插件就绪
    5. GetPluginInfo → 获取元信息（protocols, capabilities）
    6. 注册到 plugins map
  - 崩溃重启: 监听 cmd.Wait(), 指数退避 (1s → 2s → 4s → max 60s), 最大重启次数 10
  - TDD: 测试启动/停止/重启/健康检查流程

  **Must NOT do**:
  - 不实现热重载（启动时加载，崩溃后重启）
  - 不实现 cgroups 资源限制
  - 不使用 `time.Sleep()` 等待插件就绪（用带 timeout 的 health check）

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 进程生命周期管理涉及并发、错误恢复、gRPC 连接管理
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 5, 7, 8)
  - **Parallel Group**: Wave 2
  - **Blocks**: 11, 12, 13
  - **Blocked By**: 2, 3, 4

  **References**:
  **Pattern References**:
  - `internal/camera/manager.go` — 现有的 manager 生命周期模式（Start/Stop goroutine）
  - `internal/recorder/h264.go:run()` — 重连模式（指数退避），参考其 backoff 策略

  **External References**:
  - HashiCorp go-plugin (或 raw gRPC) — 取决于 Task 1 验证结果

  **Acceptance Criteria**:
  - [ ] PluginManager 启动/停止插件进程
  - [ ] 健康检查定时运行
  - [ ] 插件崩溃后自动重启（指数退避）
  - [ ] 优雅停止（context cancellation）
  - [ ] go test ./internal/plugin/... PASS

  **QA Scenarios:**
  ```
  Scenario: Plugin lifecycle management
    Tool: Bash (go test)
    Steps:
      1. Create PluginManager with test config
      2. Start mock plugin process
      3. Verify health check passes
      4. Stop plugin
      5. Verify process exited cleanly
    Expected Result: Plugin started, health checked, stopped cleanly
    Evidence: .sisyphus/evidence/task-6-lifecycle.txt

  Scenario: Plugin crash and auto-restart
    Tool: Bash (go test)
    Steps:
      1. Start plugin via PluginManager
      2. Kill plugin process (kill -9)
      3. Wait for auto-restart
      4. Verify plugin running again
    Expected Result: Plugin restarted within 30 seconds
    Evidence: .sisyphus/evidence/task-6-crash-restart.txt
  ```

  **Commit**: YES
  - Message: `feat(plugin): implement PluginManager with lifecycle management`
  - Files: internal/plugin/grpc_manager.go, internal/plugin/grpc_manager_test.go

---

- [x] 7. **gRPCRecorderAdapter**

  **What to do**:
  - 创建 `internal/plugin/grpc_adapter.go` — 将 gRPC 插件客户端适配为 `model.Recorder` 接口
  - 实现 `model.Recorder` 接口:
    ```go
    type gRPCRecorderAdapter struct {
      client    proto.PluginServiceClient
      config    config.CameraConfig
      cancel    context.CancelFunc
      status    model.RecorderStatus
      mu        sync.Mutex
    }
    func (a *gRPCRecorderAdapter) Start(ctx context.Context) error
    func (a *gRPCRecorderAdapter) Stop()
    func (a *gRPCRecorderAdapter) Status() model.RecorderStatus
    ```
  - Start() 流程:
    1. 调用 `client.StartRecorder(config)` 获取帧流
    2. 启动 goroutine 持续接收帧，转发给 `FrameReceiver.HandleFrame()`
    3. 处理流结束/错误
  - 是 CameraManager 和 gRPC 插件之间的桥梁
  - CameraManager 不感知 gRPC 细节，只看到 `model.Recorder`
  - TDD: 用 mock gRPC client 测试适配逻辑

  **Must NOT do**:
  - 不修改 `model.Recorder` 接口
  - 不过度抽象（一层 adapter 足够）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Adapter 模式需要理解两套接口（model.Recorder + gRPC client）
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 5, 6, 8)
  - **Parallel Group**: Wave 2
  - **Blocks**: 12
  - **Blocked By**: 2, 5

  **References**:
  **Pattern References**:
  - `internal/model/types.go:Recorder` — 必须实现的接口 (Start/Stop/Status)
  - `internal/recorder/h264.go:H264Recorder` — 参考 Start/Stop/Status 实现模式
  - `internal/camera/manager.go:createRecorder()` — 理解 recorder 如何被创建和使用

  **Acceptance Criteria**:
  - [ ] gRPCRecorderAdapter 实现 model.Recorder 接口
  - [ ] Start() 正确建立 gRPC 流并转发帧
  - [ ] Stop() 正确取消 context 并关闭流
  - [ ] Status() 返回正确的状态
  - [ ] go test ./internal/plugin/... PASS

  **QA Scenarios:**
  ```
  Scenario: Adapter implements Recorder interface
    Tool: Bash (go test)
    Steps:
      1. Create adapter with mock gRPC client
      2. Call Start() — verify stream opened
      3. Send mock frames — verify forwarded to FrameReceiver
      4. Call Stop() — verify stream closed
      5. Check Status() returns correct state
    Expected Result: Adapter correctly proxies to gRPC client
    Evidence: .sisyphus/evidence/task-7-adapter.txt
  ```

  **Commit**: YES
  - Message: `feat(plugin): implement gRPCRecorderAdapter for model.Recorder`
  - Files: internal/plugin/grpc_adapter.go, internal/plugin/grpc_adapter_test.go

---

- [x] 8. **Mock plugin for testing**

  **What to do**:
  - 创建 `tests/mock_plugin/` — 用于集成测试的模拟插件
  - 实现完整的 gRPC PluginService 服务端:
    - GetPluginInfo() — 返回 {name: "mock", protocols: ["mock"], capabilities: {hls: false}}
    - StartRecorder() — 发送合成 H.264 NAL 帧流:
      1. 发送 SPS (7 bytes) + PPS (4 bytes)
      2. 发送 IDR 帧 (~10KB 合成数据)
      3. 以 30fps 发送 P 帧 (~5KB, pts 递增 33ms)
      4. 每 N 帧发送 IDR（模拟 segment 切割点）
    - StopRecorder() — 停止发送
    - GetRecorderStatus() — 返回状态
    - HealthCheck() — 返回 healthy
  - 合成帧数据: 不需要真实视频，用随机 bytes + 正确 NAL header 即可
  - 构建为独立可执行文件: `go build -o tests/mock_plugin/mock-plugin`
  - TDD: 这是测试基础设施本身，用于其他任务的集成测试

  **Must NOT do**:
  - 不依赖真实摄像头硬件
  - 不生成可播放视频（只需要正确 NAL 结构）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 明确的测试工具实现，不涉及复杂业务逻辑
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 5, 6, 7)
  - **Parallel Group**: Wave 2
  - **Blocks**: 9
  - **Blocked By**: 2

  **References**:
  **Pattern References**:
  - `plugin/proto/nvr.proto` — 必须实现的服务接口（Task 2 产出）
  - `plugins/xiaomi/recorder.go:processH264NALU()` — 参考 NAL 帧结构

  **Acceptance Criteria**:
  - [ ] Mock plugin 可编译为独立二进制
  - [ ] gRPC 服务启动正常
  - [ ] StartRecorder 发送合成 NAL 帧（SPS + PPS + IDR + P* + IDR + ...）
  - [ ] 帧序列可以被 FrameReceiver 正确处理

  **QA Scenarios:**
  ```
  Scenario: Mock plugin produces processable frames
    Tool: Bash
    Steps:
      1. Build mock-plugin binary
      2. Start it on test UDS
      3. Connect with gRPC client, call StartRecorder
      4. Receive 100 frames
      5. Verify: SPS, PPS, IDR frames present, pts increasing
    Expected Result: Valid frame sequence received
    Evidence: .sisyphus/evidence/task-8-mock-plugin.txt
  ```

  **Commit**: YES
  - Message: `test(plugin): add mock plugin for integration testing`
  - Files: tests/mock_plugin/

---

- [x] 9. **Xiaomi plugin gRPC server**

  **What to do**:
  - 创建 `plugins/xiaomi/cmd/xiaomi-plugin/main.go` — 插件进程入口
  - 创建 `plugins/xiaomi/grpc_server.go` — 实现 proto PluginService 服务端
  - gRPC server 实现:
    - GetPluginInfo() → {name: "xiaomi", version, protocols: ["xiaomi"], capabilities: {hls: false, discovery: true}}
    - StartRecorder(config) → 启动 Xiaomi 录制器，将帧流式发送到客户端
    - StopRecorder() → 停止录制器
    - GetRecorderStatus() → 返回状态
    - HealthCheck() → 返回 healthy
  - 入口 main.go:
    1. 解析命令行参数（UDS path）
    2. 创建 gRPC server
    3. 注册 PluginService
    4. 监听 UDS
    5. 优雅停止（signal handling）
  - **不修改**: miss.go, cs2.go, crypto.go, cloud.go（保持不变）
  - TDD: 测试 gRPC server 各个 RPC 方法

  **Must NOT do**:
  - 不修改 MISS/CS2/crypto/cloud 协议层代码
  - 不添加 storage/muxer/metrics 依赖

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 需要理解 Xiaomi 协议层和 gRPC 服务端的集成
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (depends on mock plugin patterns)
  - **Parallel Group**: Wave 3
  - **Blocks**: 10
  - **Blocked By**: 2, 8

  **References**:
  **Pattern References**:
  - `tests/mock_plugin/` — Task 8 产出的 mock plugin，参考其 gRPC server 实现
  - `plugins/xiaomi/plugin.go` — 现有 XiaomiPlugin 结构和 NewRecorder()
  - `plugins/xiaomi/miss.go` — MISS 协议客户端（保持不变）
  - `plugins/xiaomi/cs2.go` — CS2 P2P 连接（保持不变）
  - `plugins/xiaomi/cloud.go` — 云端认证（保持不变）

  **Acceptance Criteria**:
  - [ ] `plugins/xiaomi/cmd/xiaomi-plugin/` 可编译为独立二进制
  - [ ] gRPC server 启动并监听 UDS
  - [ ] GetPluginInfo 返回正确信息
  - [ ] HealthCheck 返回 healthy
  - [ ] go test ./plugins/xiaomi/... PASS

  **QA Scenarios:**
  ```
  Scenario: Xiaomi plugin server startup
    Tool: Bash
    Steps:
      1. go build -o /tmp/xiaomi-plugin ./plugins/xiaomi/cmd/xiaomi-plugin/
      2. Start plugin with --socket /tmp/test-xiaomi.sock
      3. Connect with gRPC client, call GetPluginInfo
      4. Verify response: name="xiaomi", protocols=["xiaomi"]
    Expected Result: Plugin server responds correctly
    Evidence: .sisyphus/evidence/task-9-xiaomi-server.txt
  ```

  **Commit**: YES
  - Message: `refactor(xiaomi): implement gRPC server for Xiaomi plugin process`
  - Files: plugins/xiaomi/cmd/xiaomi-plugin/main.go, plugins/xiaomi/grpc_server.go

---

- [x] 10. **Xiaomi recorder refactor to streaming**

  **What to do**:
  - 创建 `plugins/xiaomi/grpc_recorder.go` — 重构后的 Xiaomi 录制器
  - 核心变更: 将 muxer/storage 依赖替换为 gRPC 帧流发送
  - 新流程:
    ```
    旧: processNALU → muxer.WriteSample → storage.CreateSegment/CloseSegment → db.Insert
    新: processNALU → proto.Frame{bytes, pts, is_idr, codec} → gRPC stream.Send
    ```
  - 保留的现有逻辑:
    - run() 重连循环（指数退避）
    - connectAndRecord() MISS 协议连接
    - processH264NALU() / processH265NALU() — 修改为发送帧而非写 muxer
    - SPS/PPS/VPS 解析（用于 codec 检测）
  - 移除的依赖:
    - `internal/muxer` → 不再需要
    - `internal/storage` → 不再需要
    - `internal/metrics` → 不再需要（主进程负责指标）
    - SegmentStore / RecordingDB → 不再需要
  - IDR 帧检测简化: 检测到 IDR 后在 Frame 消息中设置 is_idr=true
  - PTS 计算: 在插件侧计算 pts（纳秒），通过 Frame.pts_ns 发送
  - TDD: 测试帧序列生成正确性

  **Must NOT do**:
  - 不修改 miss.go / cs2.go / crypto.go / cloud.go
  - 不添加新的 internal 包依赖

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 核心录制逻辑重构，需要理解 NAL 帧处理和 segment 边界
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 11, 12)
  - **Parallel Group**: Wave 3
  - **Blocks**: 12
  - **Blocked By**: 5, 9

  **References**:
  **Pattern References**:
  - `plugins/xiaomi/recorder.go` — 现有录制器，理解 processH264NALU/processH265NALU 逻辑
  - `internal/plugin/frame_receiver.go` — Task 5 产出，理解 Frame 消息格式
  - `plugin/proto/nvr.proto` — Frame 消息定义

  **Acceptance Criteria**:
  - [ ] 新录制器零 internal 包依赖（仅 proto + miss + cs2 + cloud）
  - [ ] NAL 帧正确序列化为 proto.Frame
  - [ ] IDR 帧正确标记 is_idr=true
  - [ ] PTS 正确计算（纳秒精度）
  - [ ] go test ./plugins/xiaomi/... PASS

  **QA Scenarios:**
  ```
  Scenario: Frame sequence generation
    Tool: Bash (go test)
    Steps:
      1. Create mock MISS connection returning test NAL data
      2. Run recorder, collect Frame messages
      3. Verify: SPS + PPS + IDR(is_idr=true) + P(is_idr=false) + ... + IDR(is_idr=true)
    Expected Result: Correct frame sequence with IDR markers
    Evidence: .sisyphus/evidence/task-10-frame-sequence.txt
  ```

  **Commit**: YES
  - Message: `refactor(xiaomi): refactor recorder to stream NAL units over gRPC`
  - Files: plugins/xiaomi/grpc_recorder.go, plugins/xiaomi/grpc_recorder_test.go

---

- [x] 11. **Xiaomi cloud auth boundary**

  **What to do**:
  - 解决 Xiaomi 云认证的进程边界问题
  - 当前状态: `api/handler.go` 直接 import `plugins/xiaomi` 调用 SetCloudConfig()
  - 新架构:
    - 主进程: 保留 `/api/xiaomi/auth`, `/api/xiaomi/captcha`, `/api/xiaomi/verify`, `/api/xiaomi/devices` 端点
    - 主进程: 通过 gRPC 调用插件的 `SetCloudConfig()` 等价方法
    - 插件进程: 保留 cloud.go 的云认证逻辑，通过 gRPC 接收凭证
  - 在 proto 中添加:
    - `SetCloudConfig(CloudConfig) → CloudConfigResponse`
    - `CloudConfig` 消息: service_token, user_id, etc.
  - 从 `cmd/mibee-nvr/main.go` 移除 `import _ "plugins/xiaomi"`
  - 从 `api/handler.go` 移除 `"plugins/xiaomi"` 直接 import
  - TDD: 测试认证流程的 gRPC 代理

  **Must NOT do**:
  - 不改变现有 Xiaomi 认证流程（用户视角不变）
  - 不修改前端认证页面

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 涉及 API 层和插件层的边界划分
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 9, 10)
  - **Parallel Group**: Wave 3
  - **Blocks**: 12
  - **Blocked By**: 6

  **References**:
  **Pattern References**:
  - `api/handler.go` (xiaomi routes) — 现有小米认证 API 端点
  - `cmd/mibee-nvr/main.go` (xiaomi import) — 现有 import _ "plugins/xiaomi"
  - `plugins/xiaomi/cloud.go` — 云认证逻辑（保持不变，在插件进程中运行）
  - `plugins/xiaomi/plugin.go` — SetCloudConfig() 函数

  **Acceptance Criteria**:
  - [ ] `api/handler.go` 不再 import `plugins/xiaomi`
  - [ ] `cmd/mibee-nvr/main.go` 不再 import `plugins/xiaomi`
  - [ ] 云认证 API 端点仍然工作
  - [ ] 凭证通过 gRPC 传递到插件
  - [ ] go test ./internal/api/... PASS

  **QA Scenarios:**
  ```
  Scenario: Cloud auth proxy via gRPC
    Tool: Bash (go test)
    Steps:
      1. Call SetCloudConfig via gRPC to plugin
      2. Call cloud auth API endpoints
      3. Verify credentials stored in plugin process
    Expected Result: Auth flow works via gRPC proxy
    Evidence: .sisyphus/evidence/task-11-auth-boundary.txt
  ```

  **Commit**: YES
  - Message: `refactor(xiaomi): extract cloud auth to main process proxy`
  - Files: api/handler.go, cmd/mibee-nvr/main.go, plugin/proto/nvr.proto (updated)

---

- [x] 12. **Camera manager dual-mode dispatch**

  **What to do**:
  - 修改 `internal/camera/manager.go` 支持双模式分派
  - 现有流程: plugin.LookupProtocol() → built-in switch
  - 新流程:
    ```
    1. 检查 gRPC PluginManager 是否有对应协议的插件
    2. 如果有 → 创建 gRPCRecorderAdapter（封装 gRPC 客户端 + FrameReceiver）
    3. 如果没有 → 走现有的 init()/plugin.Register() + built-in switch
    ```
  - 添加 PluginManager 引用到 CameraManager:
    ```go
    type CameraManager struct {
      pluginMgr *plugin.PluginManager  // 新增
      // ... 现有字段
    }
    ```
  - 更新 `createRecorder()` 方法为三分派
  - 更新 `cmd/mibee-nvr/main.go` 初始化流程: 创建 PluginManager → 传入 CameraManager
  - TDD: 测试三种路径的分派逻辑

  **Must NOT do**:
  - 不修改现有 init()/plugin.Register() 机制
  - 不改变现有内置记录器的创建流程
  - 不修改 model.Recorder 接口

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: CameraManager 是核心组件，需要确保双模式不破坏现有流程
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (integrates 7, 10, 11)
  - **Parallel Group**: Wave 3 (sequential after 7, 10, 11)
  - **Blocks**: 13
  - **Blocked By**: 7, 10, 11

  **References**:
  **Pattern References**:
  - `internal/camera/manager.go:createRecorder()` — 现有分派逻辑（lines 68-145）
  - `internal/camera/manager.go:CameraManager` — 现有结构体
  - `internal/plugin/plugin.go:LookupProtocol()` — 现有插件查找
  - `cmd/mibee-nvr/main.go` — 初始化流程

  **Acceptance Criteria**:
  - [ ] Xiaomi 相机通过 gRPC 插件正常录制
  - [ ] RTSP/HTTP/ONVIF 相机通过内置记录器正常录制
  - [ ] 内置记录器流程零变化
  - [ ] go test ./internal/camera/... PASS
  - [ ] Integration test: mock plugin 端到端录制成功

  **QA Scenarios:**
  ```
  Scenario: Dual-mode camera dispatch
    Tool: Bash (go test)
    Steps:
      1. Add camera with protocol="mock" (gRPC plugin)
      2. Add camera with protocol="rtsp" (built-in)
      3. Verify: mock → gRPCRecorderAdapter, rtsp → H264Recorder
    Expected Result: Both cameras create correct recorder type
    Evidence: .sisyphus/evidence/task-12-dual-dispatch.txt
  ```

  **Commit**: YES
  - Message: `feat(camera): dual-mode dispatch (built-in + gRPC plugin)`
  - Files: internal/camera/manager.go, cmd/mibee-nvr/main.go

---

- [x] 13. **Plugin management REST API endpoints**

  **What to do**:
  - 扩展 `internal/api/handler.go` 添加插件管理 API:
    - `GET /api/plugins` — 列出所有插件（名称、版本、状态、PID、协议、能力、运行时间）
    - `GET /api/plugins/{name}` — 单个插件详情
    - `POST /api/plugins/{name}/restart` — 重启插件
    - `GET /api/plugins/{name}/capabilities` — 插件能力查询（protocols, encodings, features）
  - 响应格式:
    ```json
    {
      "plugins": [{
        "name": "xiaomi",
        "version": "1.0.0",
        "status": "running",
        "pid": 12345,
        "protocols": ["xiaomi"],
        "capabilities": {"hls": false, "ptz": false, "snapshot": false, "discovery": true},
        "supported_encodings": ["h264", "h265"],
        "uptime_seconds": 3600,
        "restart_count": 2
      }]
    }
    ```
  - 更新现有 `GET /api/plugins` 返回 gRPC 插件信息（当前返回空列表）
  - TDD: 测试 API 端点

  **Must NOT do**:
  - 不改变现有 API 响应格式
  - 不添加插件安装/卸载 API（MVP 不支持运行时安装）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: API 端点需要协调 PluginManager 和现有 API 结构
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (integrates plugin framework)
  - **Parallel Group**: Wave 4
  - **Blocks**: 14, 15, 16
  - **Blocked By**: 6, 12

  **References**:
  **Pattern References**:
  - `internal/api/handler.go:Routes()` — 现有路由注册模式
  - `internal/api/handler.go:writeJSON()` — JSON 响应 helper
  - `internal/plugin/grpc_manager.go` — PluginManager 提供插件信息

  **Acceptance Criteria**:
  - [ ] GET /api/plugins 返回插件列表（含 gRPC 状态）
  - [ ] GET /api/plugins/{name}/capabilities 返回能力信息
  - [ ] POST /api/plugins/{name}/restart 重启插件
  - [ ] go test ./internal/api/... PASS

  **QA Scenarios:**
  ```
  Scenario: Plugin API endpoints
    Tool: Bash (curl)
    Steps:
      1. curl -s http://localhost:9090/api/plugins | jq .
      2. Verify response has plugins array with xiaomi entry
      3. curl -s http://localhost:9090/api/plugins/xiaomi/capabilities | jq .
      4. Verify capabilities object
    Expected Result: Correct JSON responses
    Evidence: .sisyphus/evidence/task-13-api-plugins.json
  ```

  **Commit**: YES
  - Message: `feat(api): plugin management REST API endpoints`
  - Files: internal/api/handler.go

---

- [x] 14. **Plugin management UI page**

  **What to do**:
  - 创建 `web/src/routes/Plugins.svelte` — 插件管理页面
  - 页面功能:
    - 插件列表卡片（名称、版本、状态指示灯、PID、运行时间、重启次数）
    - 每个插件展开详情：协议列表、能力、支持编码
    - 重启按钮（调用 POST /api/plugins/{name}/restart）
    - 自动刷新（10s 间隔）
  - 导航: 添加到现有 sidebar（图标: puzzle-piece 或 package）
  - 状态指示: 绿色=running, 红色=error, 灰色=stopped
  - 响应式设计: 移动端卡片式，桌面端表格式
  - i18n: 添加 plugin.title, plugin.status.*, plugin.capabilities.* 等键

  **Must NOT do**:
  - 不实现插件安装/卸载 UI
  - 不实现插件配置编辑器

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: Svelte 5 UI 页面开发，需要设计感
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 15, 16)
  - **Parallel Group**: Wave 4
  - **Blocks**: F1-F4
  - **Blocked By**: 13

  **References**:
  **Pattern References**:
  - `web/src/routes/Settings.svelte` — 页面布局参考（section + card 模式）
  - `web/src/routes/Cameras.svelte` — 状态显示参考（enabled/disabled 状态徽章）
  - `web/src/lib/api.ts:listPlugins()` — 现有 API 函数（需扩展返回类型）
  - `web/src/lib/i18n/en.json` — 现有 i18n 键格式
  - `web/src/lib/i18n/zh.json` — 中文 i18n

  **Acceptance Criteria**:
  - [ ] Plugins 页面在 sidebar 显示
  - [ ] 插件列表展示名称、状态、版本、运行时间
  - [ ] 重启按钮功能正常
  - [ ] i18n 中英文切换正常
  - [ ] 移动端响应式显示

  **QA Scenarios:**
  ```
  Scenario: Plugin page displays correctly
    Tool: Playwright
    Steps:
      1. Navigate to /plugins
      2. Verify page title shows 'Plugins'
      3. Verify xiaomi plugin card visible with status='running'
      4. Click restart button
      5. Verify status changes briefly then returns to 'running'
    Expected Result: Plugin status displayed, restart works
    Evidence: .sisyphus/evidence/task-14-plugins-page.png

  Scenario: Plugin page i18n
    Tool: Playwright
    Steps:
      1. Switch language to Chinese
      2. Navigate to /plugins
      3. Verify Chinese labels
      4. Switch back to English
    Expected Result: All labels switch correctly
    Evidence: .sisyphus/evidence/task-14-plugins-i18n.png
  ```

  **Commit**: YES
  - Message: `feat(ui): plugin management page with status display`
  - Files: web/src/routes/Plugins.svelte, web/src/lib/api.ts, web/src/lib/i18n/en.json, web/src/lib/i18n/zh.json, web/src/routes/+layout.svelte (nav update)

---

- [x] 15. **Dynamic protocol selector and encoding options**

  **What to do**:
  - 重构 `web/src/routes/Cameras.svelte` 的协议和编码选择器为动态:
    1. 页面加载时调用 `GET /api/plugins` 获取所有插件
    2. 合并内置协议（rtsp/http/onvif）+ 插件协议为动态列表
    3. 根据选中协议的 capabilities 动态渲染编码选项
    4. 根据插件能力显示/隐藏字段（username/password, onvif_endpoint 等）
  - 新增 API 类型:
    ```typescript
    interface ProtocolInfo {
      id: string;        // 'rtsp', 'http', 'onvif', 'xiaomi'
      label: string;     // from i18n
      encodings: string[]; // ['h264', 'h265']
      builtIn: boolean;  // true for rtsp/http/onvif
      capabilities: {
        hls: boolean;
        ptz: boolean;
        snapshot: boolean;
        discovery: boolean;
        auth: boolean;    // needs username/password
      }
    }
    ```
  - 新增 API 端点: `GET /api/protocols` — 返回合并后的协议列表
  - 更新 Dashboard.svelte 和 LiveView.svelte 的 HLS 支持检查:
    - 替换硬编码 protocol 检查为 capabilities.hls 检查
  - 更新 PTZ 显示逻辑:
    - 替换 `protocol === 'onvif'` 为 capabilities.ptz 检查
  - 更新 i18n: 新增未知协议的 fallback 显示

  **Must NOT do**:
  - 不破坏现有协议的配置体验
  - 不移除 legacy protocol 兼容逻辑

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 大量 Svelte 组件重构，需要理解动态表单渲染
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 14, 16)
  - **Parallel Group**: Wave 4
  - **Blocks**: F1-F4
  - **Blocked By**: 13

  **References**:
  **Pattern References**:
  - `web/src/routes/Cameras.svelte:803-831` — 现有硬编码协议/编码选择器（需重构）
  - `web/src/routes/Cameras.svelte:56-66` — 自动编码逻辑（需动态化）
  - `web/src/routes/Cameras.svelte:166-169` — Legacy 协议迁移（保留）
  - `web/src/routes/Dashboard.svelte:103` — HLS 支持检查（需改为 capability）
  - `web/src/routes/LiveView.svelte:104,116` — 协议支持检查（需改为 capability）
  - `web/src/routes/Cameras.svelte:437` — PTZ ONVIF 检查（需改为 capability）
  - `web/src/lib/i18n/en.json:cameras.protocol.*` — 现有协议 i18n 键

  **Acceptance Criteria**:
  - [ ] 协议选择器动态加载（内置 + 插件协议）
  - [ ] 编码选项根据选中协议动态变化
  - [ ] 字段显示根据 capabilities 动态变化
  - [ ] HLS/PTZ 支持检查使用 capabilities
  - [ ] 现有 4 种协议功能不受影响
  - [ ] 未知协议有 fallback 显示

  **QA Scenarios:**
  ```
  Scenario: Dynamic protocol selector
    Tool: Playwright
    Steps:
      1. Navigate to /cameras
      2. Click 'Add Camera'
      3. Verify protocol dropdown has: RTSP, HTTP, ONVIF, Xiaomi
      4. Select 'RTSP' → verify encoding: H.264, H.265, MJPEG
      5. Select 'Xiaomi' → verify encoding: H.264, H.265
      6. Verify username/password hidden for Xiaomi
    Expected Result: Dynamic options correct for each protocol
    Evidence: .sisyphus/evidence/task-15-dynamic-protocol.png
  ```

  **Commit**: YES
  - Message: `feat(ui): dynamic protocol selector and encoding options`
  - Files: web/src/routes/Cameras.svelte, web/src/routes/Dashboard.svelte, web/src/routes/LiveView.svelte, web/src/lib/api.ts, internal/api/handler.go

---

- [x] 16. **Plugin discovery panel abstraction**

  **What to do**:
  - 将 Cameras.svelte 中的 ONVIF 和 Xiaomi 发现面板抽象为插件驱动:
    1. 定义 `DiscoveryPanel` 接口/协议:
       - 插件提供发现 UI 的方式（JSON schema 描述表单字段 + API 端点）
    2. 在 `GET /api/plugins/{name}/capabilities` 中添加 discovery_schema
    3. 动态渲染发现面板:
       - 根据 discovery_schema 渲染表单字段（text, password, select）
       - 表单提交到插件提供的 API 端点
       - 结果列表渲染（设备名称、状态、添加按钮）
    4. 保留现有 ONVIF 和 Xiaomi 发现面板作为参考，逐步替换
  - 发现流程:
    - 用户点击“扫描设备” → 调用插件 API → 返回设备列表 → 选择添加
    - 每个插件定义自己的发现流程
  - 更新 Settings.svelte 添加插件设置区域:
    - 显示每个插件的配置表单（从 schema 生成）
    - 保存到 plugins.{name}.config

  **Must NOT do**:
  - 不破坏现有 ONVIF/Xiaomi 发现功能
  - 不实现通用 plugin marketplace UI

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 动态表单生成 + 抽象面板设计
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 14, 15)
  - **Parallel Group**: Wave 4
  - **Blocks**: F1-F4
  - **Blocked By**: 13

  **References**:
  **Pattern References**:
  - `web/src/routes/Cameras.svelte:569-630` — ONVIF 发现面板（需抽象）
  - `web/src/routes/Cameras.svelte:633-781` — Xiaomi 发现面板（需抽象）
  - `web/src/routes/Settings.svelte` — 现有设置页面结构
  - `internal/api/handler.go` (onvif/xiaomi discovery routes) — 需要通用化

  **Acceptance Criteria**:
  - [ ] 发现面板可由插件能力驱动渲染
  - [ ] ONVIF 和 Xiaomi 发现通过插件能力工作
  - [ ] Settings 页面有插件配置区域
  - [ ] 现有发现功能不受影响

  **QA Scenarios:**
  ```
  Scenario: Plugin-driven discovery
    Tool: Playwright
    Steps:
      1. Navigate to /cameras
      2. Click 'Scan Devices'
      3. Verify discovery panel renders based on plugin capabilities
      4. Complete discovery flow for Xiaomi
    Expected Result: Discovery panel works dynamically
    Evidence: .sisyphus/evidence/task-16-discovery.png
  ```

  **Commit**: YES
  - Message: `feat(ui): plugin discovery panel abstraction`
  - Files: web/src/routes/Cameras.svelte, web/src/routes/Settings.svelte, internal/api/handler.go

---

- [x] 17. **Architecture doc update**

  **What to do**:
  - 更新 `docs/private/plugin-architecture.md`:
    - 记录实际实施的架构（修正 proto 定义、帧流设计）
    - 记录验证结果（gRPC 方案选择、性能数据）
    - 更新状态: Phase 1-2 完成 → Phase 3-4 规划
    - 记录已知限制和 Phase 2 计划:
      - 内置记录器迁移
      - 热重载
      - cgroups 资源限制
      - 通用 plugin SDK 提取
    - 记录实际遇到的问题和解决方案
  - 更新 README.md 和相关文档（如果需要）
  - 确保 Next Phase 规划有足够的上下文

  **Must NOT do**:
  - 不删除原始设计方案（保留作为对比）

  **Recommended Agent Profile**:
  - **Category**: `writing`
    - Reason: 技术文档写作
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with 14, 15, 16)
  - **Parallel Group**: Wave 4
  - **Blocks**: F1-F4
  - **Blocked By**: all implementation tasks

  **References**:
  - `docs/private/plugin-architecture.md` — 原始设计方案
  - `.sisyphus/drafts/grpc-validation.md` — gRPC 验证结果
  - Actual implementation files — 需要记录实际架构

  **Acceptance Criteria**:
  - [ ] 架构文档更新完毕
  - [ ] Proto 定义修正为实际实现
  - [ ] Phase 2 规划内容明确
  - [ ] 包含实际性能数据和问题记录

  **QA Scenarios:**
  ```
  Scenario: Doc completeness check
    Tool: Bash
    Steps:
      1. Verify docs/private/plugin-architecture.md updated date
      2. Grep for 'Phase 2' section exists
      3. Grep for actual proto definition section
    Expected Result: Document is comprehensive and up-to-date
    Evidence: .sisyphus/evidence/task-17-doc-update.txt
  ```

  **Commit**: YES
  - Message: `docs(plugin): update architecture document with implementation notes`
  - Files: docs/private/plugin-architecture.md

## Final Verification Wave (MANDATORY)

> 4 review agents run in PARALLEL. ALL must APPROVE.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists. For each "Must NOT Have": search codebase for forbidden patterns. Check evidence files. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `go vet` + `go test ./...`. Review all changed files for: AI slop, excessive comments, over-abstraction, unused imports. Check all proto-generated code is committed.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N/N] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high` (+ `playwright` skill)
  Start from clean state. Execute EVERY QA scenario from EVERY task. Test: plugin crash isolation (kill -9), mock plugin streaming, API endpoints, frontend plugin page. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: verify 1:1 — everything in spec was built, nothing beyond spec was built. Check "Must NOT do" compliance. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | VERDICT`

---

## Commit Strategy

- **T1**: `feat(plugin): validate gRPC approach for CGO_ENABLED=0` - spike code
- **T2**: `feat(plugin): define Protocol Buffers SDK for NAL frame streaming` - plugin/proto/
- **T3**: `build(plugin): dual binary build system (main + plugin)` - Makefile, Dockerfile
- **T4**: `feat(config): add plugins section to YAML config schema` - internal/config/
- **T5**: `feat(plugin): implement FrameReceiver service for NAL frame handling` - internal/plugin/frame_receiver.go
- **T6**: `feat(plugin): implement PluginManager with lifecycle management` - internal/plugin/grpc_manager.go
- **T7**: `feat(plugin): implement gRPCRecorderAdapter for model.Recorder` - internal/plugin/grpc_adapter.go
- **T8**: `test(plugin): add mock plugin for integration testing` - test/mock_plugin/
- **T9**: `refactor(xiaomi): implement gRPC server for Xiaomi plugin process` - plugins/xiaomi/grpc_server.go
- **T10**: `refactor(xiaomi): refactor recorder to stream NAL units over gRPC` - plugins/xiaomi/grpc_recorder.go
- **T11**: `refactor(xiaomi): extract cloud auth to main process proxy` - api/, plugins/xiaomi/
- **T12**: `feat(camera): dual-mode dispatch (built-in + gRPC plugin)` - internal/camera/
- **T13**: `feat(api): plugin management REST API endpoints` - internal/api/
- **T14**: `feat(ui): plugin management page with status display` - web/src/routes/Plugins.svelte
- **T15**: `feat(ui): dynamic protocol selector and encoding options` - web/src/routes/Cameras.svelte
- **T16**: `feat(ui): plugin discovery panel abstraction` - web/src/routes/Cameras.svelte
- **T17**: `docs(plugin): update architecture document with implementation notes` - docs/private/

---

## Success Criteria

### Verification Commands
```bash
make build              # Produces mibee-nvr + plugins/xiaomi/xiaomi-plugin
make cross              # Cross-compiles both for ARM64
go test ./...           # All tests pass
file plugins/xiaomi/xiaomi-plugin  # ELF 64-bit
curl -s http://localhost:9090/api/plugins | jq '.plugins[0].status'  # "running"
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All tests pass
- [ ] Plugin crash isolation verified (kill -9 test)
- [ ] Backward compatibility with existing xiaomi: config verified
- [ ] Frontend plugin management UI functional
- [ ] Architecture doc updated
