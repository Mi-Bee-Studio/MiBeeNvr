# 摄像头配置 CRUD 管理

## TL;DR

> **Quick Summary**: 为 MiBee NVR 添加完整的摄像头配置管理能力：RESTful CRUD API、YAML 持久化、热重载 recorder、独立的 Web UI Cameras 页面。
>
> **Deliverables**:
> - 后端：5 个 RESTful API 端点 (GET/POST/GET/{id}/PUT/{id}/DELETE/{id})
> - 后端：`config.Save()` YAML 持久化（原子写入）
> - 后端：`CameraManager` 动态生命周期方法 (Add/Remove/Update/Restart)
> - 后端：Upload handler 改用 DB 验证摄像头
> - 前端：独立 `Cameras.svelte` 页面（表格 + 新增/编辑表单 + 删除确认）
> - 测试：每个后端功能 TDD 开发
>
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: Task 1 → Task 3 → Task 5 → Task 6 → Task 8 → Task 10 → F1-F4

---

## Context

### Original Request
用户想为摄像头配置添加新的管理功能：Web UI 编辑、API 增删改查、配置持久化到 YAML、热重载不重启生效。

### Interview Summary
**Key Discussions**:
- API 风格：标准 RESTful CRUD（GET list, POST create, GET detail, PUT update, DELETE）
- ID 策略：自动生成 `cam-` 前缀 + UUID，兼容现有用户自定义 ID
- Web UI：独立 `/cameras` 页面，Settings 页面移除摄像头管理
- 删除策略：仅删摄像头记录，保留关联录像（孤儿数据）
- 热重载：新建/删除/更新/开关 全部触发 recorder 生命周期变化
- Settings API：从 `/api/settings` 移除摄像头开关，统一到 `/api/cameras`

**Research Findings**:
- Config 只读加载，无写入能力 — 需新增 `config.Save()`
- CameraManager 只有批量 Start/Stop — 需新增单摄像头生命周期方法
- Upload handler 用静态 map 验证 — 需改用 DB 查询
- 前端 hash-based routing — 需在 App.svelte 添加 `#/cameras`
- 已有完善的测试模式 — testify + httptest + t.TempDir

### Metis Review
**Identified Gaps** (addressed):
- **删除时录像处理**: 选择仅删记录，保留录像（简单方案）
- **并发安全**: Handler.config 是共享指针，需加 mutex 保护
- **Upload handler 过期**: 静态 camMap 需改为 DB 查询
- **YAML 写入安全**: 使用原子写入（temp file + rename）
- **Settings API 摄像头部分**: 从 settings 移除，统一到 /api/cameras
- **UUID 迁移**: 不迁移现有 ID，仅新生成摄像头用 UUID

---

## Work Objectives

### Core Objective
为 MiBee NVR 添加完整的摄像头配置管理能力，支持通过 API 和 Web UI 进行增删改查，配置变更自动持久化到 YAML 并热重载 recorder。

### Concrete Deliverables
- `internal/config/config.go` — 新增 `Save()` 函数
- `internal/camera/manager.go` — 新增 `AddCamera()`, `RemoveCamera()`, `UpdateCamera()`, `RestartRecorder()` 方法
- `internal/api/handler.go` — 新增 5 个 CRUD 端点
- `internal/upload/handler.go` — `validateCamera()` 改用 DB 查询
- `web/src/routes/Cameras.svelte` — 新页面
- `web/src/lib/api.ts` — 新增摄像头 API 函数
- `web/src/App.svelte` — 添加路由
- `web/src/routes/Settings.svelte` — 移除摄像头管理部分

### Definition of Done
- [ ] `rtk go test ./internal/config/ -run TestSave -v` → PASS
- [ ] `rtk go test ./internal/camera/ -run TestCameraManager -v` → PASS
- [ ] `rtk go test ./internal/api/ -run TestHandle -v` → PASS (所有 CRUD 端点)
- [ ] `rtk go test ./internal/upload/ -v` → PASS
- [ ] `rtk go test ./... -v` → ALL PASS
- [ ] 前端 `#/cameras` 页面可正常访问和操作

### Must Have
- 标准 RESTful CRUD API（5 个端点）
- 新摄像头 ID 格式：`cam-` 前缀 + UUID
- 向后兼容现有 YAML 配置（用户自定义 ID 继续工作）
- 配置变更原子写入 YAML
- 热重载：新建→启动 recorder，删除→停止 recorder，更新→重启 recorder，开关→启停 recorder
- 删除摄像头仅删记录，保留关联录像
- 独立 Cameras 页面（表格 + 新增/编辑表单 + 删除确认）
- 从 Settings 页面移除摄像头管理
- Upload handler 使用 DB 验证摄像头
- 所有后端功能 TDD 开发

### Must NOT Have (Guardrails)
- 不改 FTP/WebDAV/MQTT 配置管理
- 不迁移现有摄像头 ID 到 UUID 格式
- 不添加 WebSocket/SSE 实时状态推送（用轮询）
- 不添加视频预览/直播功能
- 不添加摄像头分组/标签/元数据
- 不引入消息队列或微服务
- 不修改 recorder 接口（model.Recorder）
- 不重构 Start()/Stop() 方法
- 不修改现有 GET /api/cameras 的响应格式
- 不添加文件锁到配置写入

---

## Verification Strategy (MANDATORY)

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (Go testing + testify)
- **Automated tests**: TDD — 每个后端功能先写测试再实现
- **Framework**: Go testing + testify/assert + testify/require
- **TDD**: 每个任务遵循 RED (failing test) → GREEN (minimal impl) → REFACTOR

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **API endpoints**: Use Bash (curl) — Send requests, assert status + response fields
- **Backend logic**: Use Bash (go test) — Run specific test functions
- **Frontend**: Use Playwright — Navigate, interact, assert DOM, screenshot
- **Integration**: Use Bash — Start server, run full workflow, verify YAML persistence

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — foundation):
├── Task 1: config.Save() + atomic YAML persistence [deep]
├── Task 2: Camera ID 生成工具函数 [quick]
└── Task 3: Upload handler 改用 DB 验证摄像头 [unspecified-high]

Wave 2 (After Wave 1 — core lifecycle):
├── Task 4: CameraManager.AddCamera() (depends: 1, 2) [deep]
├── Task 5: CameraManager.RemoveCamera() (depends: 1) [deep]
├── Task 6: CameraManager.UpdateCamera() (depends: 1, 4) [deep]
└── Task 7: CameraManager.RestartRecorder() (depends: 1, 4) [deep]

Wave 3 (After Wave 2 — API endpoints):
├── Task 8: CRUD API endpoints (depends: 1-7) [deep]
├── Task 9: 从 Settings 页面/API 移除摄像头 (depends: 8) [quick]
└── Task 10: 前端 api.ts 摄像头 API 函数 (depends: 8) [quick]

Wave 4 (After Wave 3 — frontend):
├── Task 11: Cameras.svelte 页面 (depends: 10) [visual-engineering]
├── Task 12: 前端路由 + 导航更新 (depends: 11) [quick]
└── Task 13: main.go 接线更新 (depends: 1-8) [quick]

Wave FINAL (After ALL tasks — 4 parallel reviews):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality review (unspecified-high)
├── Task F3: Real manual QA (unspecified-high + playwright)
└── Task F4: Scope fidelity check (deep)
-> Present results -> Get explicit user okay

Critical Path: Task 1 → Task 4 → Task 6 → Task 8 → Task 11 → Task 12 → F1-F4
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 4 (Wave 2)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| 1 | - | 4, 5, 6, 7, 8, 13 | 1 |
| 2 | - | 4, 8 | 1 |
| 3 | - | 8 | 1 |
| 4 | 1, 2 | 6, 8 | 2 |
| 5 | 1 | 8 | 2 |
| 6 | 1, 4 | 8 | 2 |
| 7 | 1, 4 | 8 | 2 |
| 8 | 1-7 | 9, 10 | 3 |
| 9 | 8 | - | 3 |
| 10 | 8 | 11 | 3 |
| 11 | 10 | 12 | 4 |
| 12 | 11 | - | 4 |
| 13 | 1-8 | - | 4 |

### Agent Dispatch Summary

- **Wave 1**: **3** — T1 → `deep`, T2 → `quick`, T3 → `unspecified-high`
- **Wave 2**: **4** — T4-T7 → `deep`
- **Wave 3**: **3** — T8 → `deep`, T9 → `quick`, T10 → `quick`
- **Wave 4**: **3** — T11 → `visual-engineering`, T12 → `quick`, T13 → `quick`
- **FINAL**: **4** — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

### Wave 1 — 基础设施

- [x] 1. config.Save() YAML 持久化（原子写入）

  **What to do**:
  - 在 `internal/config/config.go` 中新增 `Save(path string, cfg *Config) error` 函数
  - 使用原子写入：先写入临时文件（同目录下 `.mibee-nvr.yaml.tmp`），再 `os.Rename` 覆盖原文件
  - 序列化完整的 `Config` 结构体回 YAML（保留注释不要求，但字段完整）
  - TDD: 先写 `TestSave` 测试用例：写入临时目录 → 验证文件内容 → 重新 Load → 对比一致
  - TDD: 测试原子性：模拟写入中途失败（权限错误）→ 原文件未被修改

  **Must NOT do**:
  - 不修改 `Load()` 或 `Validate()` 函数
  - 不添加文件锁
  - 不处理 YAML 注释保留

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 需要理解现有 Config 结构和 YAML 序列化，涉及原子写入的边界处理
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3)
  - **Blocks**: Tasks 4, 5, 6, 7, 8, 13
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/config/config.go:72-87` — `Load()` 函数的实现模式，Save 需要对称处理
  - `internal/config/config.go:11-19` — `Config` 结构体定义，需要完整序列化
  - `internal/config/config.go:118-165` — `applyDefaults()` 了解默认值如何应用

  **External References**:
  - Go `gopkg.in/yaml.v3` 已在项目中使用（`config.go:8`）

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/config/ -run TestSave -v` → PASS
  - [ ] 测试覆盖：正常保存、重新加载一致性、原子写入安全性

  **QA Scenarios:**
  ```
  Scenario: 保存配置并验证可重新加载
    Tool: Bash (go test)
    Preconditions: 有一个包含摄像头的 Config 对象
    Steps:
      1. go test ./internal/config/ -run TestSave -v
      2. 验证输出包含 PASS
    Expected Result: 测试通过，YAML 文件内容可被 Load() 正确解析
    Failure Indicators: 测试 FAIL，或 Load 返回 error
    Evidence: .sisyphus/evidence/task-1-save-reload.txt

  Scenario: 原子写入安全性（写入失败不破坏原文件）
    Tool: Bash (go test)
    Preconditions: 已有合法 YAML 配置文件
    Steps:
      1. go test ./internal/config/ -run TestSaveAtomic -v
      2. 验证写入到只读目录时原文件未被修改
    Expected Result: 原文件内容不变，Save 返回 error
    Failure Indicators: 原文件被清空或损坏
    Evidence: .sisyphus/evidence/task-1-atomic-safety.txt
  ```

  **Commit**: YES
  - Message: `feat(config): add Save() for YAML persistence with atomic write`
  - Files: `internal/config/config.go`, `internal/config/config_test.go`
  - Pre-commit: `rtk go test ./internal/config/ -v`

- [x] 2. 摄像头 ID 生成工具函数（cam- 前缀 + UUID）

  **What to do**:
  - 在 `internal/camera/` 下新建 `id.go` 文件
  - 实现 `GenerateCameraID() string` 函数，返回 `cam-` 前缀 + UUID 格式的 ID
  - 使用 Go 标准库 `crypto/rand` 或 `google/uuid` 生成 UUID
  - TDD: 先写 `TestGenerateCameraID` 测试：验证前缀为 `cam-`、长度正确、唯一性

  **Must NOT do**:
  - 不修改现有 `CameraConfig.ID` 字段的类型
  - 不创建新的 package

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 简单的工具函数，几行代码
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3)
  - **Blocks**: Tasks 4, 8
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/camera/manager.go:1-14` — 包声明和 import 模式

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/camera/ -run TestGenerateCameraID -v` → PASS
  - [ ] 生成的 ID 格式为 `cam-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`
  - [ ] 连续生成 100 个 ID 无重复

  **QA Scenarios:**
  ```
  Scenario: ID 格式和唯一性验证
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/camera/ -run TestGenerateCameraID -v
    Expected Result: PASS，ID 前缀为 "cam-"，UUID 格式正确
    Failure Indicators: 前缀不匹配或 ID 重复
    Evidence: .sisyphus/evidence/task-2-id-gen.txt
  ```

  **Commit**: YES
  - Message: `feat(camera): add ID generation utility (cam- prefix + UUID)`
  - Files: `internal/camera/id.go`, `internal/camera/id_test.go`
  - Pre-commit: `rtk go test ./internal/camera/ -run TestGenerateCameraID -v`

- [x] 3. Upload handler 改用 DB 验证摄像头

  **What to do**:
  - 修改 `internal/upload/handler.go` 中的摄像头验证逻辑
  - 当前使用 `map[string]config.CameraConfig` 静态 map，改为调用 `db.GetCamera(ctx, cameraID)` 查询数据库
  - Handler 构造函数改为接收 `*storage.DB` 而非 `map[string]config.CameraConfig`
  - TDD: 先写测试验证 DB 查询路径工作，然后修改实现
  - TDD: 测试不存在 camera_id 的上传被拒绝

  **Must NOT do**:
  - 不重构 upload 的核心上传逻辑
  - 不修改 upload 的路由结构
  - 不修改 HTTP 上传协议

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 需要理解 upload handler 结构和 DB 接口，但不是超复杂
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2)
  - **Blocks**: Task 8
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/upload/handler.go` — 完整的 upload handler 实现，找到 validateCamera 或 camera_id 验证的地方
  - `cmd/mibee-nvr/main.go:88-91` — 当前 camMap 构建方式，理解要替换什么
  - `cmd/mibee-nvr/main.go:107` — `upload.NewHandler` 调用方式，构造函数签名需改
  - `internal/storage/db.go` — 找 `GetCamera` 或 `UpsertCamera` 了解 DB 查询接口

  **API/Type References**:
  - `internal/storage/db.go:CameraRow` — DB 返回的摄像头数据结构

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/upload/ -v` → PASS
  - [ ] Upload handler 不再依赖 `map[string]config.CameraConfig`
  - [ ] 无效 camera_id 的上传返回 404/400 错误

  **QA Scenarios:**
  ```
  Scenario: 新添加的摄像头可以上传文件
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/upload/ -run TestUploadNewCamera -v
      2. 先通过 DB 插入摄像头 → 再上传文件 → 验证成功
    Expected Result: 上传成功，文件保存正确
    Failure Indicators: 上传被拒绝（camera not found）
    Evidence: .sisyphus/evidence/task-3-upload-new-cam.txt

  Scenario: 不存在的 camera_id 上传被拒绝
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/upload/ -run TestUploadInvalidCamera -v
    Expected Result: 返回 400/404 错误
    Failure Indicators: 上传成功（应该被拒绝）
    Evidence: .sisyphus/evidence/task-3-upload-invalid.txt
  ```

  **Commit**: YES
  - Message: `fix(upload): validate cameras against DB instead of static map`
  - Files: `internal/upload/handler.go`, `internal/upload/handler_test.go`, `cmd/mibee-nvr/main.go`
  - Pre-commit: `rtk go test ./internal/upload/ -v`

### Wave 2 — 摄像头生命周期管理

- [x] 4. CameraManager.AddCamera() 动态添加摄像头

  **What to do**:
  - 在 `internal/camera/manager.go` 中新增 `AddCamera(ctx context.Context, cam config.CameraConfig) error` 方法
  - 使用 `GenerateCameraID()` 生成 ID（如果 ID 为空）
  - 将摄像头追加到 `cm.cfg.Cameras` 切片
  - 调用 `cm.db.UpsertCamera()` 写入数据库
  - 如果 `cam.Enabled` 且协议为 `rtsp_h264` 或 `rtsp_mjpeg`，创建并启动 recorder
  - 调用 `config.Save()` 持久化到 YAML
  - 使用 `cm.mu` 保护并发修改
  - CameraManager 需要新增 `configPath string` 字段用于持久化
  - TDD: 先写 `TestCameraManager_AddCamera` — 测试添加 enabled camera → recorder 创建；测试添加 disabled camera → 无 recorder；测试重复 ID → 错误

  **Must NOT do**:
  - 不修改 `Start()` 或 `Stop()` 方法
  - 不修改 `model.Recorder` 接口
  - 不修改 `config.CameraConfig` 结构体

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 核心业务逻辑，需要理解 recorder 创建流程、并发安全、持久化集成
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 5)
  - **Blocks**: Tasks 6, 7, 8
  - **Blocked By**: Tasks 1, 2

  **References**:
  **Pattern References**:
  - `internal/camera/manager.go:36-98` — `Start()` 方法中创建 recorder 的完整逻辑，AddCamera 需要复用相同的 recorder 创建模式
  - `internal/camera/manager.go:55-94` — switch/case 按 protocol 创建不同 recorder 的模式
  - `internal/camera/manager.go:16-22` — CameraManager 结构体，需要添加 `configPath` 字段

  **API/Type References**:
  - `internal/config/config.go:31-39` — `CameraConfig` 结构体
  - `internal/recorder/h264.go` — `H264Config` 和 `NewH264Recorder`
  - `internal/recorder/mjpeg.go` — `MJPEGConfig` 和 `NewMJPEGRecorder`

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/camera/ -run TestCameraManager_AddCamera -v` → PASS
  - [ ] 添加 enabled RTSP camera → recorder 自动启动
  - [ ] 添加 disabled camera → 无 recorder
  - [ ] 添加 http_jpeg camera → 无 recorder（由 upload handler 处理）
  - [ ] 重复 ID 返回错误
  - [ ] YAML 文件被更新

  **QA Scenarios:**
  ```
  Scenario: 添加一个 RTSP H264 摄像头并启动 recorder
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/camera/ -run TestCameraManager_AddCamera -v
      2. 验证添加 enabled camera 后 RecorderCount() 增加
    Expected Result: PASS，recorder 已创建并处于 Running 状态
    Failure Indicators: recorder 未创建或状态为 Error
    Evidence: .sisyphus/evidence/task-4-add-h264.txt

  Scenario: 添加重复 ID 的摄像头返回错误
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/camera/ -run TestCameraManager_AddCamera_Duplicate -v
    Expected Result: 返回 error，包含 duplicate 相关信息
    Failure Indicators: 不返回错误或 panic
    Evidence: .sisyphus/evidence/task-4-add-dup.txt
  ```

  **Commit**: YES
  - Message: `feat(camera): add AddCamera() dynamic lifecycle method`
  - Files: `internal/camera/manager.go`, `internal/camera/manager_test.go`
  - Pre-commit: `rtk go test ./internal/camera/ -run TestCameraManager_AddCamera -v`

- [x] 5. CameraManager.RemoveCamera() 动态删除摄像头

  **What to do**:
  - 在 `internal/camera/manager.go` 中新增 `RemoveCamera(ctx context.Context, cameraID string) error`
  - 如果该 camera 有运行中的 recorder，先调用 `rec.Stop()` 优雅停止
  - 从 `cm.recorders` map 中删除
  - 从 `cm.cfg.Cameras` 切片中移除该条目
  - 调用 `config.Save()` 持久化
  - **不删除数据库中的摄像头记录和关联录像**（仅删 config，保留 DB 记录）
  - TDD: 测试删除有 recorder 的 camera → recorder 停止；删除无 recorder 的 camera → 正常；删除不存在的 → 错误

  **Must NOT do**:
  - 不删除关联录像
  - 不删除数据库中 cameras 表记录
  - 不修改 Start()/Stop()

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 需要处理 recorder 优雅停止、配置清理、持久化的一致性
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Task 4)
  - **Blocks**: Task 8
  - **Blocked By**: Task 1

  **References**:
  **Pattern References**:
  - `internal/camera/manager.go:100-119` — `Stop()` 方法，展示了停止所有 recorder 的模式，RemoveCamera 需要停止单个
  - `internal/camera/manager.go:36-98` — `Start()` 了解 recorder 如何被创建，以便反向清理

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/camera/ -run TestCameraManager_RemoveCamera -v` → PASS
  - [ ] 删除有 recorder 的 camera → recorder 停止，config 中移除
  - [ ] 删除不存在 camera → 返回错误
  - [ ] YAML 文件被更新（移除了对应条目）

  **QA Scenarios:**
  ```
  Scenario: 删除运行中的摄像头并停止 recorder
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/camera/ -run TestCameraManager_RemoveCamera -v
      2. 先 AddCamera → 再 RemoveCamera → 验证 RecorderCount() 减少
    Expected Result: PASS，recorder 停止，config 更新
    Failure Indicators: recorder 未停止或 config 未更新
    Evidence: .sisyphus/evidence/task-5-remove.txt

  Scenario: 删除不存在的摄像头
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/camera/ -run TestCameraManager_RemoveCamera_NotFound -v
    Expected Result: 返回 error "camera not found"
    Failure Indicators: 不返回错误
    Evidence: .sisyphus/evidence/task-5-remove-notfound.txt
  ```

  **Commit**: YES
  - Message: `feat(camera): add RemoveCamera() dynamic lifecycle method`
  - Files: `internal/camera/manager.go`, `internal/camera/manager_test.go`
  - Pre-commit: `rtk go test ./internal/camera/ -run TestCameraManager_RemoveCamera -v`

- [x] 6. CameraManager.UpdateCamera() 更新摄像头配置

  **What to do**:
  - 在 `internal/camera/manager.go` 中新增 `UpdateCamera(ctx context.Context, cameraID string, updates CameraUpdate) (*config.CameraConfig, error)`
  - `CameraUpdate` 结构体包含可选字段：Name, URL, Protocol, Username, Password, Enabled（指针类型，nil 表示不更新）
  - 合并更新到 `cm.cfg.Cameras` 中对应条目
  - 如果 URL/Protocol/Username/Password 有变化，停止旧 recorder 并按新配置创建
  - 如果 Enabled 从 true→false，停止 recorder；false→true，启动 recorder
  - 调用 `cm.db.UpsertCamera()` 更新 DB
  - 调用 `config.Save()` 持久化
  - TDD: 测试更新名称（不重启 recorder）；更新 URL（重启 recorder）；切换 enabled；更新不存在的 camera → 错误

  **Must NOT do**:
  - 不允许修改 ID 字段（ID 不可变）
  - 不修改 recorder 接口

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 最复杂的生命周期方法，需要判断哪些字段变更需要重启 recorder
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO（depends on Task 4 for AddCamera pattern）
  - **Parallel Group**: Wave 2 (sequential after Task 4)
  - **Blocks**: Task 8
  - **Blocked By**: Tasks 1, 4

  **References**:
  **Pattern References**:
  - `internal/camera/manager.go:55-94` — recorder 创建的 switch/case 模式
  - `internal/camera/manager.go:100-119` — recorder 停止模式
  - `internal/config/config.go:31-39` — `CameraConfig` 字段定义

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/camera/ -run TestCameraManager_UpdateCamera -v` → PASS
  - [ ] 更新 name（不变） → recorder 不重启
  - [ ] 更新 url → recorder 重启
  - [ ] enabled true→false → recorder 停止
  - [ ] enabled false→true → recorder 启动
  - [ ] 更新不存在的 camera → 错误
  - [ ] YAML 和 DB 同步更新

  **QA Scenarios:**
  ```
  Scenario: 更新 URL 触发 recorder 重启
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/camera/ -run TestCameraManager_UpdateCamera_URL -v
    Expected Result: recorder 被重启，新 URL 生效
    Evidence: .sisyphus/evidence/task-6-update-url.txt

  Scenario: 切换 enabled 状态
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/camera/ -run TestCameraManager_UpdateCamera_Toggle -v
    Expected Result: enabled→disabled 停止 recorder，disabled→enabled 启动
    Evidence: .sisyphus/evidence/task-6-toggle.txt
  ```

  **Commit**: YES
  - Message: `feat(camera): add UpdateCamera() with smart recorder restart`
  - Files: `internal/camera/manager.go`, `internal/camera/manager_test.go`
  - Pre-commit: `rtk go test ./internal/camera/ -run TestCameraManager_UpdateCamera -v`

- [x] 7. CameraManager.RestartRecorder() 重启单个 recorder

  **What to do**:
  - 在 `internal/camera/manager.go` 中新增 `RestartRecorder(ctx context.Context, cameraID string) error`
  - 停止旧 recorder → 创建新 recorder → 启动
  - 如果旧 recorder 不存在但 config 中 enabled=true，直接创建（相当于修复）
  - TDD: 测试重启运行中的 recorder；重启不存在的 recorder 但 enabled=true；重启 disabled camera → 错误

  **Must NOT do**:
  - 不修改 recorder 接口

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 需要理解 recorder 生命周期，和 UpdateCamera 有重叠但职责不同
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 6)
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 8
  - **Blocked By**: Tasks 1, 4

  **References**:
  **Pattern References**:
  - `internal/camera/manager.go:55-94` — recorder 创建和启动模式
  - `internal/camera/manager.go:100-119` — recorder 停止模式

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/camera/ -run TestCameraManager_RestartRecorder -v` → PASS
  - [ ] 重启运行中的 recorder → 旧停止 + 新启动
  - [ ] 重启 disabled camera → 错误

  **QA Scenarios:**
  ```
  Scenario: 重启运行中的 recorder
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/camera/ -run TestCameraManager_RestartRecorder -v
    Expected Result: 旧 recorder 停止，新 recorder 启动
    Evidence: .sisyphus/evidence/task-7-restart.txt
  ```

  **Commit**: YES (groups with Tasks 4-6)
  - Message: `feat(camera): add RestartRecorder() for single camera restart`
  - Files: `internal/camera/manager.go`, `internal/camera/manager_test.go`
  - Pre-commit: `rtk go test ./internal/camera/ -v`

### Wave 3 — API 端点 + 清理

- [x] 8. RESTful Camera CRUD API 端点

  **What to do**:
  - 在 `internal/api/handler.go` 中新增 5 个 handler 函数：
    - `handleCreateCamera` — POST /api/cameras，生成 ID，调用 `camMgr.AddCamera()`
    - `handleGetCamera` — GET /api/cameras/{id}，从 DB 查询单个
    - `handleUpdateCamera` — PUT /api/cameras/{id}，调用 `camMgr.UpdateCamera()`
    - `handleDeleteCamera` — DELETE /api/cameras/{id}，调用 `camMgr.RemoveCamera()`
  - 修改 `handleListCameras` 保持不变（已存在）
  - Handler 需要新增 `camMgr *camera.CameraManager` 字段
  - 在 `Routes()` 中注册新路由（在 protected group 内）
  - 请求体验证：name 必填、protocol 必须是合法值、url 必填
  - TDD: 每个端点先写测试 — 正常创建、缺少必填字段、无效 protocol、重复创建、更新不存在、删除不存在

  **Must NOT do**:
  - 不修改 recordings、settings、auth 相关 handler
  - 不修改现有 GET /api/cameras 响应格式
  - 不添加新的中间件
  - 不添加 WebSocket/SSE

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 大量 API 端点，需要仔细处理请求验证、错误响应、路由注册
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (depends on ALL Wave 2 tasks)
  - **Blocks**: Tasks 9, 10
  - **Blocked By**: Tasks 1-7

  **References**:
  **Pattern References**:
  - `internal/api/handler.go:36-64` — `Routes()` 方法，了解路由注册模式
  - `internal/api/handler.go:390-400` — `handleListCameras` 已有实现
  - `internal/api/handler.go:23-28` — Handler 结构体，需要添加 camMgr 字段
  - `internal/api/handler.go:434-442` — `writeJSON`/`writeError` 辅助函数

  **API/Type References**:
  - `internal/config/config.go:31-39` — `CameraConfig` 结构体（API 请求/响应映射）
  - `internal/storage/db.go` — `ListCameras()`, `UpsertCamera()` DB 接口

  **Test References**:
  - `internal/api/handler_test.go` — 现有测试模式（doRequest/parseJSON helper）

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/api/ -run TestHandleCreateCamera -v` → PASS
  - [ ] `rtk go test ./internal/api/ -run TestHandleGetCamera -v` → PASS
  - [ ] `rtk go test ./internal/api/ -run TestHandleUpdateCamera -v` → PASS
  - [ ] `rtk go test ./internal/api/ -run TestHandleDeleteCamera -v` → PASS
  - [ ] POST 无效 protocol → 400
  - [ ] PUT 不存在的 camera → 404
  - [ ] DELETE 不存在的 camera → 404

  **QA Scenarios:**
  ```
  Scenario: 创建摄像头完整流程
    Tool: Bash (curl)
    Preconditions: 服务器运行在 :9090
    Steps:
      1. curl -s -X POST http://localhost:9090/api/cameras -u admin:pass -H 'Content-Type: application/json' -d '{"name":"Front Door","protocol":"rtsp_h264","url":"rtsp://192.168.1.100/stream","enabled":true}'
      2. 验证响应 201，包含 "id":"cam-" 前缀
      3. curl -s http://localhost:9090/api/cameras -u admin:pass | grep "Front Door"
    Expected Result: 创建成功，列表中可见
    Failure Indicators: 非 201 状态码或列表中找不到
    Evidence: .sisyphus/evidence/task-8-create.txt

  Scenario: 创建摄像头缺少必填字段
    Tool: Bash (curl)
    Steps:
      1. curl -s -X POST http://localhost:9090/api/cameras -u admin:pass -H 'Content-Type: application/json' -d '{"name":"Test"}'
    Expected Result: 400 Bad Request
    Failure Indicators: 201 Created
    Evidence: .sisyphus/evidence/task-8-create-missing.txt

  Scenario: 删除不存在的摄像头
    Tool: Bash (curl)
    Steps:
      1. curl -s -X DELETE http://localhost:9090/api/cameras/nonexistent -u admin:pass
    Expected Result: 404 Not Found
    Evidence: .sisyphus/evidence/task-8-delete-notfound.txt
  ```

  **Commit**: YES
  - Message: `feat(api): add camera CRUD endpoints`
  - Files: `internal/api/handler.go`, `internal/api/handler_test.go`
  - Pre-commit: `rtk go test ./internal/api/ -run TestHandle -v`

- [x] 9. 从 Settings 页面和 API 移除摄像头管理

  **What to do**:
  - 修改 `internal/api/handler.go` 中 `handleGetSettings` — 移除 cameras 字段
  - 修改 `internal/api/handler.go` 中 `handleUpdateSettings` — 移除 cameras 更新逻辑
  - 修改 `web/src/routes/Settings.svelte` — 移除 Camera Management 区块
  - 修改 `web/src/lib/api.ts` — `SettingsConfig` 接口移除 cameras 字段

  **Must NOT do**:
  - 不修改 cleanup settings 相关逻辑
  - 不修改前端偏好设置部分
  - 不删除 GET /api/cameras 端点

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 主要是删除代码，不涉及新逻辑
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Task 10)
  - **Blocks**: None
  - **Blocked By**: Task 8

  **References**:
  **Pattern References**:
  - `internal/api/handler.go:477-502` — `handleGetSettings` 中 cameras 序列化部分需移除
  - `internal/api/handler.go:552-571` — `handleUpdateSettings` 中 camera enabled 更新逻辑需移除
  - `web/src/routes/Settings.svelte:212-264` — Camera Management 区块需移除
  - `web/src/lib/api.ts:50-53` — `SettingsConfig` 接口需移除 cameras

  **Acceptance Criteria**:
  - [ ] GET /api/settings 响应中无 cameras 字段
  - [ ] PUT /api/settings 请求体中 cameras 字段被忽略
  - [ ] Settings.svelte 页面无 Camera Management 区块
  - [ ] `rtk go test ./internal/api/ -v` → PASS

  **QA Scenarios:**
  ```
  Scenario: Settings API 不再返回 cameras
    Tool: Bash (go test)
    Steps:
      1. go test ./internal/api/ -run TestHandleGetSettings -v
      2. 验证响应体不包含 cameras 字段
    Expected Result: PASS
    Evidence: .sisyphus/evidence/task-9-settings-no-cam.txt
  ```

  **Commit**: YES
  - Message: `refactor(api): remove camera management from settings endpoint`
  - Files: `internal/api/handler.go`, `internal/api/handler_test.go`, `web/src/routes/Settings.svelte`, `web/src/lib/api.ts`
  - Pre-commit: `rtk go test ./internal/api/ -v`

- [x] 10. 前端 api.ts 摄像头 API 函数

  **What to do**:
  - 在 `web/src/lib/api.ts` 中新增：
    - `createCamera(data: CreateCameraRequest): Promise<Camera>` — POST /api/cameras
    - `getCamera(id: string): Promise<Camera>` — GET /api/cameras/{id}
    - `updateCamera(id: string, data: UpdateCameraRequest): Promise<Camera>` — PUT /api/cameras/{id}
    - `deleteCamera(id: string): Promise<void>` — DELETE /api/cameras/{id}
  - 新增 `CreateCameraRequest` 和 `UpdateCameraRequest` 接口
  - 复用现有的 `apiRequest<T>()` 辅助函数
  - 移除 `SettingsConfig` 中的 `cameras` 字段（配合 Task 9）

  **Must NOT do**:
  - 不修改现有的 `listCameras()` 函数签名
  - 不修改认证逻辑

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 简单的 API 调用封装，模式已有
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Task 9)
  - **Blocks**: Task 11
  - **Blocked By**: Task 8

  **References**:
  **Pattern References**:
  - `web/src/lib/api.ts:119-146` — `apiRequest<T>()` 通用请求函数
  - `web/src/lib/api.ts:29-35` — `Camera` 接口定义
  - `web/src/lib/api.ts:276-278` — `listCameras()` 已有实现，新函数应遵循相同模式

  **Acceptance Criteria**:
  - [ ] TypeScript 类型正确，无编译错误
  - [ ] `createCamera` 发送 POST 请求到 /api/cameras
  - [ ] `updateCamera` 发送 PUT 请求到 /api/cameras/{id}
  - [ ] `deleteCamera` 发送 DELETE 请求到 /api/cameras/{id}

  **QA Scenarios:**
  ```
  Scenario: API 函数类型检查
    Tool: Bash (npx tsc --noEmit)
    Steps:
      1. cd web && npx tsc --noEmit 2>&1 | head -20
    Expected Result: 无类型错误
    Evidence: .sisyphus/evidence/task-10-api-types.txt
  ```

  **Commit**: YES
  - Message: `feat(web): add camera API client functions`
  - Files: `web/src/lib/api.ts`
  - Pre-commit: `cd web && npx tsc --noEmit`

### Wave 4 — 前端页面 + 集成

- [x] 11. Cameras.svelte 独立页面

  **What to do**:
  - 创建 `web/src/routes/Cameras.svelte`
  - 页面内容：
    - 摄像头列表表格（名称、协议、状态、URL、操作按钮）
    - 「添加摄像头」按钮 → 展开表单（name, protocol select, url, username/password, enabled toggle）
    - 每行操作：编辑按钮、删除按钮（带确认对话框）
    - 编辑和新增使用同一个表单组件（通过编辑状态切换）
  - 风格：深色主题 slate-900 背景，cyan-500 强调色，与 Settings/Recordings 页面一致
  - 表单验证：name 必填、url 必填、protocol 必选
  - 操作反馈：成功提示（绿色文字）、错误提示（红色文字）
  - i18n: 为新文本添加中英文翻译 key 到 i18n 文件

  **Must NOT do**:
  - 不添加视频预览功能
  - 不添加 WebSocket 实时状态更新
  - 不创建组件库或设计系统
  - 不修改其他页面

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 前端 UI 页面开发，需要 Tailwind 样式和 Svelte 交互逻辑
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 4 (sequential)
  - **Blocks**: Task 12
  - **Blocked By**: Task 10

  **References**:
  **Pattern References**:
  - `web/src/routes/Settings.svelte:1-109` — Svelte 页面结构、数据加载、表单处理模式
  - `web/src/routes/Settings.svelte:111-321` — 深色主题样式、card 布局、表单验证模式
  - `web/src/routes/Recordings.svelte` — 表格布局和列表渲染模式
  - `web/src/routes/Settings.svelte:101-104` — toggle 开关组件模式（复用相同的 toggle 样式）

  **API/Type References**:
  - `web/src/lib/api.ts:29-35` — `Camera` 接口
  - Task 10 新增的 `createCamera`, `updateCamera`, `deleteCamera` 函数

  **External References**:
  - `web/src/lib/i18n/index.ts` — i18n 配置，添加新翻译 key

  **Acceptance Criteria**:
  - [ ] 页面在 `#/cameras` 可访问
  - [ ] 列表显示所有摄像头（名称、协议、状态、URL）
  - [ ] 添加摄像头表单工作（填写 → 提交 → 列表更新）
  - [ ] 编辑摄像头表单工作
  - [ ] 删除摄像头有确认对话框
  - [ ] 表单验证（必填字段）
  - [ ] 中英文切换正常

  **QA Scenarios:**
  ```
  Scenario: 添加摄像头完整流程
    Tool: Playwright
    Preconditions: 服务器运行，已登录
    Steps:
      1. 导航到 http://localhost:9090/#/cameras
      2. 点击「添加摄像头」按钮
      3. 填写 name="Test Cam", protocol=rtsp_h264, url="rtsp://192.168.1.100/stream"
      4. 点击保存
      5. 等待列表中出现 "Test Cam"
    Expected Result: 列表新增一行，包含 "Test Cam" 和 rtsp_h264
    Failure Indicators: 列表未更新或错误提示
    Evidence: .sisyphus/evidence/task-11-add-camera.png

  Scenario: 删除摄像头确认对话框
    Tool: Playwright
    Steps:
      1. 在摄像头列表中点击删除按钮
      2. 验证出现确认对话框
      3. 点击确认
      4. 验证列表中该摄像头消失
    Expected Result: 确认后摄像头从列表移除
    Evidence: .sisyphus/evidence/task-11-delete-camera.png

  Scenario: 表单验证 — 缺少必填字段
    Tool: Playwright
    Steps:
      1. 点击「添加摄像头」
      2. 不填写任何字段直接点击保存
      3. 验证显示验证错误提示
    Expected Result: 显示红色错误提示，表单不提交
    Evidence: .sisyphus/evidence/task-11-validation.png
  ```

  **Commit**: YES
  - Message: `feat(web): add independent Cameras page with CRUD operations`
  - Files: `web/src/routes/Cameras.svelte`, `web/src/lib/i18n/index.ts`
  - Pre-commit: `cd web && npx tsc --noEmit`

- [x] 12. 前端路由和导航更新

  **What to do**:
  - 修改 `web/src/App.svelte`：
    - import Cameras 组件
    - 在 `parseRoute()` 中添加 `#/cameras` 路由
    - 添加对应的渲染区块
  - 修改所有页面的导航栏（Settings.svelte, Recordings.svelte, Stats.svelte）：
    - 添加「Cameras」导航链接
    - 导航顺序：Recordings → Cameras → Stats → Settings
  - 确保 CameraDetail 页面如存在也正确链接

  **Must NOT do**:
  - 不提取共享 header 为组件（留作未来优化）
  - 不修改 hash 路由机制本身

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 简单的路由注册和导航链接添加
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with Task 13)
  - **Blocks**: None
  - **Blocked By**: Task 11

  **References**:
  **Pattern References**:
  - `web/src/App.svelte` — 完整的路由定义和渲染模式
  - `web/src/routes/Settings.svelte:118-134` — 导航栏模式，所有页面需要一致
  - `web/src/routes/Recordings.svelte` — 另一个页面的导航栏（确认格式一致）

  **Acceptance Criteria**:
  - [ ] `#/cameras` 路由可访问，显示 Cameras.svelte 页面
  - [ ] 所有页面导航栏包含 Cameras 链接
  - [ ] Cameras 页面当前导航项高亮显示

  **QA Scenarios:**
  ```
  Scenario: 导航到 Cameras 页面
    Tool: Playwright
    Steps:
      1. 导航到 http://localhost:9090/#/recordings
      2. 点击导航栏中的 "Cameras" 链接
      3. 验证 URL 变为 #/cameras
      4. 验证页面显示摄像头列表
    Expected Result: 页面正确渲染
    Evidence: .sisyphus/evidence/task-12-nav-cameras.png
  ```

  **Commit**: YES
  - Message: `feat(web): add /cameras route and navigation link`
  - Files: `web/src/App.svelte`, `web/src/routes/Settings.svelte`, `web/src/routes/Recordings.svelte`, `web/src/routes/Stats.svelte`
  - Pre-commit: `cd web && npx tsc --noEmit`

- [x] 13. main.go 接线更新

  **What to do**:
  - 修改 `cmd/mibee-nvr/main.go`：
    - 传递 `configPath` 到 `CameraManager`（用于持久化）
    - 传递 `CameraManager` 到 `api.Handler`（用于 CRUD 操作）
    - 更新 `upload.NewHandler` 调用（Task 3 改了签名，传入 DB 而非 camMap）
    - 移除 `camMap` 构建代码（不再需要）

  **Must NOT do**:
  - 不修改启动顺序（CameraManager.Start 仍然在 HTTP server 启动后）
  - 不修改 graceful shutdown 流程

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 简单的接线代码修改，遵循已有模式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with Task 12)
  - **Blocks**: None
  - **Blocked By**: Tasks 1-8

  **References**:
  **Pattern References**:
  - `cmd/mibee-nvr/main.go:88-91` — camMap 构建（需移除）
  - `cmd/mibee-nvr/main.go:94-97` — CameraManager 和 Handler 创建（需传新参数）
  - `cmd/mibee-nvr/main.go:107` — upload.NewHandler 调用（需改签名）

  **Acceptance Criteria**:
  - [ ] `rtk go build ./cmd/mibee-nvr/` → 编译成功
  - [ ] `rtk go test ./... -v` → ALL PASS
  - [ ] 服务器可正常启动和关闭

  **QA Scenarios:**
  ```
  Scenario: 全项目编译和测试
    Tool: Bash
    Steps:
      1. go build ./cmd/mibee-nvr/
      2. go test ./...
    Expected Result: 编译成功，所有测试通过
    Failure Indicators: 编译错误或测试失败
    Evidence: .sisyphus/evidence/task-13-build-test.txt
  ```

  **Commit**: YES
  - Message: `chore: update main.go wiring for camera management`
  - Files: `cmd/mibee-nvr/main.go`
  - Pre-commit: `rtk go test ./... -v`

## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `rtk go vet ./...` + `rtk go test ./...` + check for race conditions. Review all changed files for: empty catches, fmt.Println in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names. Verify TDD: every implementation function has a corresponding test.
  Output: `Build [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high` (+ `playwright` skill)
  Start server. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration: create camera → verify recorder starts → update URL → verify restart → delete → verify stop. Test edge cases: invalid protocol, duplicate name, delete with recordings. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff. Verify 1:1 — everything in spec was built, nothing beyond spec. Check "Must NOT do" compliance. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **Wave 1**: `feat(config): add Save() for YAML persistence with atomic write` — config.go, config_test.go
- **Wave 1**: `feat(camera): add ID generation utility (cam- prefix + UUID)` — id.go, id_test.go
- **Wave 1**: `fix(upload): validate cameras against DB instead of static map` — upload/handler.go
- **Wave 2**: `feat(camera): add dynamic lifecycle methods to CameraManager` — manager.go, manager_test.go
- **Wave 3**: `feat(api): add camera CRUD endpoints` — handler.go, handler_test.go
- **Wave 3**: `refactor(api): remove camera management from settings endpoint` — handler.go, Settings.svelte
- **Wave 3**: `feat(web): add camera API client functions` — api.ts
- **Wave 4**: `feat(web): add independent Cameras page` — Cameras.svelte, App.svelte
- **Final**: `chore: update main.go wiring for camera management` — main.go

---

## Success Criteria

### Verification Commands
```bash
rtk go test ./internal/config/ -run TestSave -v           # Expected: PASS
rtk go test ./internal/camera/ -run TestCameraManager -v  # Expected: PASS
rtk go test ./internal/api/ -run TestHandle -v            # Expected: ALL PASS
rtk go test ./internal/upload/ -v                         # Expected: PASS
rtk go test ./...                                         # Expected: ALL PASS
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All tests pass
- [ ] YAML config persists across simulated restarts
- [ ] Camera CRUD works via curl without server restart
- [ ] Cameras page accessible at #/cameras
- [ ] Settings page no longer shows camera management
- [ ] Upload handler validates against DB
