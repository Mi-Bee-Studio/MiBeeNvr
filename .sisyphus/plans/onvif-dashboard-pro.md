# MiBee NVR: ONVIF 支持 + 监控大屏 + Pro 版本评估

## TL;DR

> **Quick Summary**: 为 MiBee NVR 添加 ONVIF 协议支持（设备发现 + 流URL获取 + PTZ云台控制），新增 1-4 路自适应网格监控大屏，创建私有文档目录存放部署资料和 Pro 版本评估方案（含 GB28181 开发计划）。
> 
> **Deliverables**:
> - ONVIF 发现/管理/PTZ 后端 API + 前端 UI
> - 1-4 路自适应网格监控大屏页面
> - ONVIF 模拟器部署脚本（192.168.63.162）
> - `docs/private/` 私有文档目录
> - Pro 版本评估文档 + GB28181 开发方案
> - TDD 测试覆盖所有新增后端代码
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: T4 (ONVIF client) → T5/T6/T7 (Backend APIs) → T9/T10/T11 (Frontend)

---

## Context

### Original Request
用户要求：1) 支持 ONVIF 协议（基础版：设备发现+流URL+PTZ）；2) 在 192.168.63.31 部署 NVR，在 192.168.63.162 (RPi 3B) 运行 ONVIF 模拟设备进行测试；3) 新增监控大屏支持 1-4 路摄像头同时查看；4) 创建 docs/private/ 存放不提交远程的文档；5) 输出 Pro 版本评估（突破 4 路限制）和 GB28181 开发方案。

### Interview Summary
**Key Discussions**:
- ONVIF 范围: 基础版(发现+URL+PTZ) vs Pro版(完整ONVIF)
- .162 角色: 仅运行 ONVIF 模拟设备（不是 NVR）
- 监控大屏: 自适应网格布局 (1→全屏, 2→左右, 3→1大+2小, 4→2x2)
- 私有目录: docs/private/，添加到 .gitignore
- 测试策略: TDD（测试驱动开发）
- 无真实 ONVIF 摄像头，使用 onvif-go 内置虚拟摄像头服务

**Research Findings**:
- 推荐库: `github.com/0x524a/onvif-go` v1.1.4（200+ APIs，内置虚拟摄像头服务器）
- ONVIF 是发现/配置层，不是流协议 — 获取 RTSP URL 后复用现有录制器
- WS-Discovery: UDP multicast 239.255.255.250:3702
- ONVIF 认证: WS-Security Digest (SHA1(nonce+timestamp+password))
- H265 录制器已完整实现 (h265.go, 457行)
- HLS Manager 已有 maxStreams=4 限制
- GB28181 复杂度 HIGH (8/10)，推荐 sipgo 库
- 浏览器 H265: Safari 全支持, Chrome 107+ 需硬件, Firefox 有限

### Metis Review
**Identified Gaps** (addressed):
- ONVIF 设备离线/重连: 监控大屏需优雅处理摄像头下线
- PTZ 能力检测: 前端需根据摄像头能力显示/隐藏 PTZ 控件
- RTSP URL 过期: GetStreamURI 可能返回有时效的 URL，需在 recorder 启动时刷新
- PTZ 限流: 命令需要 rate-limit 防止过载
- 混合协议: 监控大屏需处理 MJPEG 等不支持实时预览的协议

---

## Work Objectives

### Core Objective
为 MiBee NVR 添加 ONVIF 协议支持（基础版）和 1-4 路监控大屏功能，同时创建私有文档目录和 Pro 版本评估文档。

### Concrete Deliverables
- `internal/onvif/` 新包：ONVIF 客户端封装（发现、流URL、PTZ）
- `internal/api/handler.go` 扩展：ONVIF discovery + PTZ API 端点
- `internal/camera/manager.go` 扩展：ONVIF 摄像头创建
- `internal/config/config.go` 扩展：ONVIF 配置字段
- `web/src/routes/Dashboard.svelte` 新页面：监控大屏
- `web/src/routes/Cameras.svelte` 扩展：ONVIF 发现 UI
- `web/src/components/PtzControl.svelte` 新组件：PTZ 控件
- `docs/private/` 新目录 + `.gitignore` 更新
- `docs/private/deployment-162.md` 部署文档
- `docs/private/pro-version-evaluation.md` Pro 评估文档
- `docs/private/gb28181-development-plan.md` GB28181 方案文档

### Definition of Done
- [ ] `rtk go test ./internal/onvif/... -v` → 全部 PASS
- [ ] `rtk go test ./internal/api/... -v` → 全部 PASS（含 ONVIF 相关测试）
- [ ] `rtk go vet ./...` → 无警告
- [ ] 监控大屏可同时显示 1-4 路 H264/H265 摄像头
- [ ] ONVIF 发现能检测到 .162 上的虚拟摄像头
- [ ] PTZ 控件可控制虚拟摄像头云台
- [ ] `docs/private/` 在 .gitignore 中，不会被提交

### Must Have
- ONVIF WS-Discovery 自动发现局域网设备
- ONVIF GetStreamURI 返回 RTSP URL 并自动创建摄像头
- ONVIF PTZ continuous/absolute 控制
- PTZ 能力检测（无 PTZ 的摄像头不显示控件）
- 监控大屏自适应网格 (1/2/3/4 路)
- 每个网格显示摄像头名称和状态
- `docs/private/` 目录及完整文档
- TDD: 所有后端新代码有测试覆盖

### Must NOT Have (Guardrails)
- ❌ 不实现 ONVIF 事件订阅（Pro 版本范围）
- ❌ 不实现 ONVIF 音频/门禁功能（Pro 版本范围）
- ❌ 不实现 WebRTC（当前 HLS 已满足需求）
- ❌ 不修改现有录制器核心逻辑（H264/H265/MJPEG 录制器不变）
- ❌ 不突破 4 路摄像头限制（Pro 版本评估文档讨论）
- ❌ 不实现 GB28181 协议（仅输出方案文档）
- ❌ 不添加 AI slop: 过度抽象、冗余注释、无意义 wrapper
- ❌ PTZ 不实现 preset 保存/召回（基础版只需要方向+变焦控制）

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (testify/require, existing test files)
- **Automated tests**: YES (TDD)
- **Framework**: Go testing + testify/require
- **TDD workflow**: Each backend task follows RED (failing test) → GREEN (minimal impl) → REFACTOR

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Frontend/UI**: Use Playwright - Navigate, interact, assert DOM, screenshot
- **API/Backend**: Use Bash (curl) - Send requests, assert status + response fields
- **Library/Module**: Use Bash (go test) - Run tests, verify pass/fail

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Foundation - 4 tasks, ALL parallel):
├── T1: docs/private/ + .gitignore [quick]
├── T2: ONVIF types + config + API validation [quick]
├── T3: Frontend dashboard routing + API types [quick]
└── T4: ONVIF client module (internal/onvif/) [deep]

Wave 2 (Backend APIs + TDD - 4 tasks, parallel):
├── T5: ONVIF discovery API + tests (depends: T2, T4) [unspecified-high]
├── T6: ONVIF camera management + tests (depends: T2, T4) [unspecified-high]
├── T7: ONVIF PTZ API + tests (depends: T4) [unspecified-high]
└── T8: ONVIF simulator setup on .162 (depends: T4) [quick]

Wave 3 (Frontend - 3 tasks, parallel):
├── T9: Monitoring dashboard UI (depends: T3) [visual-engineering]
├── T10: ONVIF camera discovery UI (depends: T5, T6) [visual-engineering]
└── T11: PTZ control UI (depends: T7) [visual-engineering]

Wave 4 (Documentation - 3 tasks, parallel):
├── T12: .162 deployment documentation (depends: T8) [writing]
├── T13: Pro version evaluation document (depends: none) [writing]
└── T14: GB28181 development plan document (depends: none) [writing]

Wave FINAL (Verification - 4 tasks, parallel):
├── F1: Plan compliance audit [oracle]
├── F2: Code quality review [unspecified-high]
├── F3: Real manual QA [unspecified-high]
└── F4: Scope fidelity check [deep]
-> Present results -> Get explicit user okay

Critical Path: T4 → T5/T6/T7 → T10/T11 → F1-F4
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 4 (Wave 1)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| T1 | - | T12 | 1 |
| T2 | - | T5, T6 | 1 |
| T3 | - | T9 | 1 |
| T4 | - | T5, T6, T7, T8 | 1 |
| T5 | T2, T4 | T10 | 2 |
| T6 | T2, T4 | T10 | 2 |
| T7 | T4 | T11 | 2 |
| T8 | T4 | T12 | 2 |
| T9 | T3 | F1-F4 | 3 |
| T10 | T5, T6 | F1-F4 | 3 |
| T11 | T7 | F1-F4 | 3 |
| T12 | T8 | F1-F4 | 4 |
| T13 | - | F1-F4 | 4 |
| T14 | - | F1-F4 | 4 |

### Agent Dispatch Summary

- **Wave 1**: 4 tasks - T1→`quick`, T2→`quick`, T3→`quick`, T4→`deep`
- **Wave 2**: 4 tasks - T5→`unspecified-high`, T6→`unspecified-high`, T7→`unspecified-high`, T8→`quick`
- **Wave 3**: 3 tasks - T9→`visual-engineering`, T10→`visual-engineering`, T11→`visual-engineering`
- **Wave 4**: 3 tasks - T12→`writing`, T13→`writing`, T14→`writing`
- **FINAL**: 4 tasks - F1→`oracle`, F2→`unspecified-high`, F3→`unspecified-high`, F4→`deep`

---

## TODOs

- [x] 1. **docs/private/ 目录 + .gitignore 更新**

  **What to do**:
  - 创建 `docs/private/` 目录并添加 `.gitkeep` 文件
  - 在 `.gitignore` 中添加 `docs/private/` 条目
  - 在 `docs/private/` 中创建 `README.md` 说明目录用途

  **Must NOT do**:
  - 不修改现有 docs/ 下的任何文件
  - 不将 docs/private/ 追踪到 git

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2, T3, T4)
  - **Blocks**: T12, T13, T14
  - **Blocked By**: None

  **References**:
  - `.gitignore` - 当前 gitignore 配置，理解格式模式
  - `docs/en/`, `docs/zh/` - 现有文档结构作为参考

  **Acceptance Criteria**:
  - [ ] `docs/private/` 目录存在
  - [ ] `docs/private/.gitkeep` 文件存在
  - [ ] `docs/private/README.md` 存在，说明目录用途
  - [ ] `grep -q 'docs/private/' .gitignore` 返回 0
  - [ ] `git status docs/private/` 显示 untracked

  **QA Scenarios**:
  ```
  Scenario: 验证私有目录不被 git 追踪
    Tool: Bash
    Steps:
      1. 运行: git status docs/private/ 2>&1
      2. 运行: grep 'docs/private/' .gitignore
      3. 运行: ls docs/private/.gitkeep docs/private/README.md
    Expected Result: git status 显示 untracked，grep 返回匹配，ls 找到文件
    Evidence: .sisyphus/evidence/task-1-private-dir.txt
  ```

  **Commit**: YES
  - Message: `chore(docs): add docs/private/ directory and gitignore entry`
  - Files: `docs/private/.gitkeep`, `docs/private/README.md`, `.gitignore`

- [x] 2. **ONVIF 协议常量 + 配置字段 + API 验证**

  **What to do**:
  - TDD: 先写测试用例
  - 在 `internal/model/types.go` 添加 `ProtoONVIF Protocol = "onvif"` 常量
  - 在 `internal/config/config.go` 的 `CameraConfig` 添加 ONVIF 字段:
    - `ONVIFEndpoint string \`yaml:"onvif_endpoint"\`` (ONVIF 服务地址，如 http://192.168.1.100/onvif/device_service)
  - 更新 `config.Validate()` 添加 `"onvif"` 到有效协议列表
  - 更新 `internal/api/handler.go` 的 `validProtocols` map 添加 `"onvif": true`
  - 更新 `internal/camera/manager.go` 的 `Start()` 和 `createRecorder()` switch 添加 onvif case

  **Must NOT do**:
  - 不实现 ONVIF 录制器（ONVIF 摄像头使用发现的 RTSP URL + 现有 H264/H265 录制器）
  - 不添加 ONVIF 专用的 Recorder 实现
  - 不修改现有录制器代码

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T3, T4)
  - **Blocks**: T5, T6
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/model/types.go:111-116` - 现有 Protocol 常量定义模式
  - `internal/config/config.go:35-43` - CameraConfig 结构体定义
  - `internal/config/config.go:150` - Validate() 中的协议校验逻辑
  - `internal/api/handler.go:705-710` - validProtocols map
  - `internal/camera/manager.go:64-103` - createRecorder() 工厂方法
  - `internal/camera/manager.go:156-211` - Start() 方法中的协议 switch

  **WHY Each Reference Matters**:
  - types.go: 需要了解 Protocol 类型定义模式，添加新常量
  - config.go: CameraConfig 是添加 ONVIF 字段的地方
  - config.go Validate: 需要把 onvif 加入有效协议列表
  - handler.go validProtocols: API 层也需要接受 onvif 协议
  - manager.go: 需要了解 ONVIF 摄像头如何接入现有录制流程

  **Acceptance Criteria**:
  - [ ] `internal/model/types.go` 包含 `ProtoONVIF Protocol = "onvif"`
  - [ ] `internal/config/config.go` CameraConfig 包含 ONVIFEndpoint 字段
  - [ ] `config.Validate()` 接受 `"onvif"` 协议
  - [ ] `validProtocols` map 包含 `"onvif": true`
  - [ ] TDD: 测试文件存在且 `rtk go test ./internal/config/... -v` PASS

  **QA Scenarios**:
  ```
  Scenario: ONVIF 协议常量正确注册
    Tool: Bash
    Steps:
      1. grep 'ProtoONVIF' internal/model/types.go
      2. grep 'onvif' internal/api/handler.go
      3. rtk go test ./internal/config/... -v -run TestValidate
    Expected Result: 所有 grep 返回匹配，测试 PASS
    Evidence: .sisyphus/evidence/task-2-onvif-types.txt

  Scenario: 无效协议被拒绝
    Tool: Bash
    Steps:
      1. rtk go test ./internal/config/... -v -run TestValidateInvalidProtocol
    Expected Result: 测试验证 onvif 有效但 unknown_protocol 无效
    Evidence: .sisyphus/evidence/task-2-onvif-types-invalid.txt
  ```

  **Commit**: YES
  - Message: `feat(onvif): add ONVIF protocol types and config fields`
  - Files: `internal/model/types.go`, `internal/config/config.go`, `internal/api/handler.go`, `internal/camera/manager.go`

---

- [x] 3. **前端 Dashboard 路由 + API 类型 + i18n**

  **What to do**:
  - 在 `web/src/lib/api.ts` 添加 Dashboard 相关 API 函数:
    - `getDashboardCameras()` 获取支持实时预览的摄像头列表（H264/H265）
    - `discoverONVIFDevices(timeout)` ONVIF 设备发现
    - `addONVIFCamera(endpoint, username, password, profileToken)` 添加 ONVIF 摄像头
    - `ptzControl(cameraId, command, params)` PTZ 控制
    - `getCameraCapabilities(cameraId)` 获取摄像头能力（含 PTZ 支持）
  - 在 `web/src/lib/api.ts` 添加相关 TypeScript 类型
  - 在 `web/src/App.svelte` 的 `parseRoute()` 添加 `#/dashboard` 路由
  - 在 `web/src/App.svelte` 的 `currentRoute` 条件中添加 Dashboard 组件渲染
  - 在 `web/src/components/Header.svelte` 的导航项中添加 Dashboard 链接
  - 在 `web/src/lib/i18n/en.json` 和 `zh.json` 添加 Dashboard 和 ONVIF 相关翻译字符串

  **Must NOT do**:
  - 不创建 Dashboard.svelte 组件本身（T9 任务）
  - 不添加 ONVIF 发现 UI（T10 任务）
  - 不添加 PTZ UI 组件（T11 任务）

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2, T4)
  - **Blocks**: T9
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `web/src/lib/api.ts` - 现有 API 客户端模式（apiRequest 泛型封装、类型定义、auth header 注入）
  - `web/src/App.svelte:parseRoute()` - 哈希路由解析逻辑
  - `web/src/App.svelte` - 现有路由条件渲染模式
  - `web/src/components/Header.svelte` - 导航项定义模式
  - `web/src/lib/i18n/en.json` - 现有 i18n 键结构
  - `web/src/lib/i18n/zh.json` - 中文翻译模式

  **WHY Each Reference Matters**:
  - api.ts: 需要了解 apiRequest 泛型模式来添加新的 API 调用函数
  - App.svelte parseRoute: 需要了解如何添加新的哈希路由
  - Header.svelte: 需要了解导航项的数据结构来添加 Dashboard 链接
  - i18n: 需要遵循现有的键命名约定

  **Acceptance Criteria**:
  - [ ] `api.ts` 包含 dashboard/ONVIF/PTZ 相关 API 函数和类型
  - [ ] `App.svelte` 包含 `#/dashboard` 路由解析
  - [ ] `Header.svelte` 包含 Dashboard 导航链接
  - [ ] `en.json` 和 `zh.json` 包含 dashboard/onvif/ptz 相关翻译键
  - [ ] `rtk npm run build` (in web/) 成功

  **QA Scenarios**:
  ```
  Scenario: 路由和导航集成
    Tool: Playwright
    Steps:
      1. 导航到 http://localhost:5173
      2. 检查 Header 导航栏包含 Dashboard 链接
      3. 点击 Dashboard 链接
      4. 验证 URL hash 变为 #/dashboard
    Expected Result: 导航栏显示 Dashboard，点击后正确路由
    Evidence: .sisyphus/evidence/task-3-dashboard-routing.png

  Scenario: API 类型编译正确
    Tool: Bash
    Steps:
      1. cd web && rtk npm run build 2>&1
    Expected Result: 构建成功无类型错误
    Evidence: .sisyphus/evidence/task-3-api-types-build.txt
  ```

  **Commit**: YES
  - Message: `feat(web): add dashboard routing and API types`
  - Files: `web/src/lib/api.ts`, `web/src/App.svelte`, `web/src/components/Header.svelte`, `web/src/lib/i18n/en.json`, `web/src/lib/i18n/zh.json`

---

- [x] 4. **ONVIF 客户端模块 (internal/onvif/)**

  **What to do**:
  - TDD: 先写测试用例
  - 添加依赖: `go get github.com/0x524a/onvif-go@latest`
  - 创建 `internal/onvif/` 包:
    - `client.go` - ONVIF 客户端封装:
      - `NewClient(endpoint, username, password)` 创建 ONVIF 客户端
      - `GetDeviceInformation(ctx)` 获取设备信息（厂商、型号、固件版本）
      - `GetProfiles(ctx)` 获取媒体配置文件列表（含编码格式、分辨率）
      - `GetStreamURI(ctx, profileToken)` 获取 RTSP 流 URL
      - `GetCapabilities(ctx)` 获取设备能力（含 PTZ 支持）
    - `discovery.go` - WS-Discovery 设备发现:
      - `Discover(ctx, timeout)` UDP multicast 发现局域网 ONVIF 设备
      - 返回设备列表（IP、端口、XAddrs、Scopes）
    - `ptz.go` - PTZ 控制:
      - `PTZContinuousMove(ctx, profileToken, velocity)` 连续移动
      - `PTZAbsoluteMove(ctx, profileToken, position)` 绝对定位
      - `PTZRelativeMove(ctx, profileToken, displacement)` 相对移动
      - `PTZStop(ctx, profileToken)` 停止移动
      - `PTZGetStatus(ctx, profileToken)` 获取当前位置
    - `types.go` - 内部类型定义:
      - `DiscoveredDevice` - 发现的设备信息
      - `DeviceProfile` - 媒体配置文件
      - `StreamInfo` - 流 URL 及元信息
      - `PTZCapabilities` - PTZ 能力范围
      - `PTZVector` - PTZ 向量 (pan, tilt, zoom)
    - `client_test.go` - 客户端单元测试
    - `discovery_test.go` - 发现功能测试
    - `ptz_test.go` - PTZ 控制测试

  **Must NOT do**:
  - 不实现 ONVIF 事件订阅
  - 不实现 ONVIF 音频/门禁功能
  - 不修改现有录制器代码
  - 不创建 ONVIF 录制器（复用现有 H264/H265 录制器）
  - 不添加 PTZ preset 管理（仅基础移动控制）

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: ONVIF 协议复杂度较高，需要深入理解 SOAP/XML 交互、WS-Discovery 组播、PTZ 控制逻辑
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2, T3)
  - **Blocks**: T5, T6, T7, T8
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/recorder/h264.go` - RTSP 客户端连接模式，参考错误处理和重连逻辑
  - `internal/camera/manager.go:64-103` - createRecorder() 工厂方法，理解 ONVIF 摄像头如何接入
  - `internal/model/types.go` - 核心接口和类型定义

  **API/Type References**:
  - `internal/config/config.go:35-43` - CameraConfig 结构体，理解 ONVIF 配置如何融入
  - `internal/model/types.go:29-39` - Camera 结构体，理解摄像头数据模型

  **External References**:
  - onvif-go 库: `github.com/0x524a/onvif-go` - ONVIF 客户端库
  - onvif-go Discovery: `discovery.Discover(ctx, timeout)` - WS-Discovery 实现
  - onvif-go Media: `client.GetStreamURI(ctx, profileToken)` - 获取 RTSP URL
  - onvif-go PTZ: `client.ContinuousMove(ctx, profileToken, velocity)` - PTZ 控制
  - onvif-go Server: 内置虚拟摄像头服务器，用于测试
  - WS-Discovery 协议: UDP multicast 239.255.255.250:3702

  **WHY Each Reference Matters**:
  - h264.go: 了解现有的 RTSP 连接和错误处理模式
  - manager.go: 理解 ONVIF 摄像头最终要如何融入 CameraManager
  - onvif-go: 这是核心依赖库，需要了解其 API 模式

  **Acceptance Criteria**:
  - [ ] `internal/onvif/` 包存在，包含 client.go, discovery.go, ptz.go, types.go
  - [ ] `go.mod` 包含 `github.com/0x524a/onvif-go` 依赖
  - [ ] TDD: `rtk go test ./internal/onvif/... -v` → ALL PASS
  - [ ] Client 封装支持 GetDeviceInformation, GetProfiles, GetStreamURI, GetCapabilities
  - [ ] Discovery 支持 UDP multicast 发现
  - [ ] PTZ 支持 ContinuousMove, AbsoluteMove, Stop, GetStatus

  **QA Scenarios**:
  ```
  Scenario: ONVIF 客户端编译和测试通过
    Tool: Bash
    Steps:
      1. rtk go test ./internal/onvif/... -v 2>&1
      2. rtk go vet ./internal/onvif/... 2>&1
    Expected Result: 所有测试 PASS，vet 无警告
    Evidence: .sisyphus/evidence/task-4-onvif-client-tests.txt

  Scenario: ONVIF 发现功能可编译
    Tool: Bash
    Steps:
      1. grep -r 'Discover' internal/onvif/discovery.go
      2. grep -r 'func.*Discover' internal/onvif/discovery_test.go
    Expected Result: 发现函数和测试函数都存在
    Evidence: .sisyphus/evidence/task-4-onvif-discovery.txt
  ```

  **Commit**: YES
  - Message: `feat(onvif): implement ONVIF client module`
  - Files: `internal/onvif/*.go`, `go.mod`, `go.sum`

---

- [x] 5. **ONVIF 设备发现 API 端点 + TDD**

  **What to do**:
  - TDD: 先写测试用例
  - 在 `internal/api/handler.go` 添加 ONVIF 发现端点:
    - `POST /api/onvif/discover` - 触发 WS-Discovery 发现局域网设备
      - Request: `{ "timeout": 5 }` (秒)
      - Response: `{ "devices": [{ "uuid": "", "name": "", "xaddrs": [], "scopes": [], "hardware": "" }] }`
    - `GET /api/onvif/discover/{ip}` - 查询指定 IP 的 ONVIF 设备详情
      - Response: 设备信息 + profiles + 能力
  - 创建 `internal/api/onvif_test.go` 测试文件
  - 注册路由到 `Routes()` 方法

  **Must NOT do**:
  - 不实现自动添加摄像头（T6 任务）
  - 不实现 PTZ（T7 任务）
  - 不修改现有摄像头 CRUD 端点

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T6, T7, T8)
  - **Blocks**: T10
  - **Blocked By**: T2, T4

  **References**:
  **Pattern References**:
  - `internal/api/handler.go:625-655` - GET /api/cameras 端点模式，参考 JSON 响应格式
  - `internal/api/handler.go:712-793` - POST /api/cameras 端点模式，参考请求解析和验证
  - `internal/api/handler.go` - `Routes()` 方法，参考路由注册模式

  **API/Type References**:
  - `internal/onvif/discovery.go` - Discover() 函数签名
  - `internal/onvif/client.go` - GetDeviceInformation(), GetProfiles(), GetCapabilities() 签名
  - `internal/onvif/types.go` - DiscoveredDevice, DeviceProfile 等类型

  **Test References**:
  - `internal/api/handler.go` - TestHandler() / TestHandlerWithAuth() 测试工厂模式

  **WHY Each Reference Matters**:
  - handler.go: 需要了解现有的 API 端点模式和测试工厂方法
  - onvif 包: 新端点需要调用 ONVIF 客户端模块的函数

  **Acceptance Criteria**:
  - [ ] `POST /api/onvif/discover` 端点存在
  - [ ] `GET /api/onvif/discover/{ip}` 端点存在
  - [ ] TDD: `rtk go test ./internal/api/... -v -run ONVIF` → ALL PASS
  - [ ] 发现端点返回设备列表 JSON
  - [ ] 详情端点返回设备信息 + profiles + 能力

  **QA Scenarios**:
  ```
  Scenario: ONVIF 发现 API 可调用
    Tool: Bash (curl)
    Steps:
      1. curl -s -X POST http://localhost:9090/api/onvif/discover -H 'Content-Type: application/json' -d '{"timeout": 3}' -u admin:password
      2. 验证响应包含 devices 数组
    Expected Result: HTTP 200, JSON 包含 devices 字段（可能为空数组）
    Evidence: .sisyphus/evidence/task-5-discover-api.txt

  Scenario: 无效参数拒绝
    Tool: Bash (curl)
    Steps:
      1. curl -s -X POST http://localhost:9090/api/onvif/discover -H 'Content-Type: application/json' -d '{"timeout": -1}' -u admin:password
    Expected Result: HTTP 400, 错误消息说明参数无效
    Evidence: .sisyphus/evidence/task-5-discover-api-invalid.txt
  ```

  **Commit**: YES
  - Message: `feat(api): add ONVIF device discovery endpoint`
  - Files: `internal/api/handler.go`, `internal/api/onvif_test.go`

- [x] 6. **ONVIF 摄像头管理 API + TDD**

  **What to do**:
  - TDD: 先写测试用例
  - 扩展 `POST /api/cameras` 支持 ONVIF 摄像头添加:
    - 当 protocol="onvif" 时，额外接受 `onvif_endpoint`, `profile_token` 参数
    - 调用 ONVIF 客户端获取设备信息和 RTSP 流 URL
    - 根据 profile encoding 自动选择录制器 (rtsp_h264 或 rtsp_h265)
    - 创建摄像头配置并启动录制
  - 添加 `GET /api/cameras/{id}/onvif/profiles` 获取 ONVIF 摄像头的可用 profiles
  - 创建 `internal/api/onvif_camera_test.go` 测试文件

  **Must NOT do**:
  - 不修改现有非 ONVIF 摄像头的添加逻辑
  - 不自动发现并添加（需要用户确认）
  - 不存储 ONVIF 凭据到配置文件（仅存储 endpoint 和 profile token）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T5, T7, T8)
  - **Blocks**: T10
  - **Blocked By**: T2, T4

  **References**:
  **Pattern References**:
  - `internal/api/handler.go:712-793` - POST /api/cameras 端点，参考摄像头创建逻辑
  - `internal/camera/manager.go:277-319` - AddCamera() 方法，参考摄像头添加流程
  - `internal/camera/manager.go:64-103` - createRecorder() 工厂方法

  **API/Type References**:
  - `internal/onvif/client.go` - GetStreamURI() 返回 RTSP URL
  - `internal/onvif/types.go` - DeviceProfile.Encoding 字段区分 H264/H265
  - `internal/config/config.go:35-43` - CameraConfig 结构体

  **WHY Each Reference Matters**:
  - handler.go POST cameras: 需要了解摄像头创建的完整流程来扩展 ONVIF 支持
  - AddCamera: 需要了解 CameraManager 如何管理摄像头生命周期
  - GetStreamURI: ONVIF 摄像头的核心——获取 RTSP URL 后复用现有录制器

  **Acceptance Criteria**:
  - [ ] `POST /api/cameras` 接受 protocol="onvif" 并成功创建摄像头
  - [ ] ONVIF 摄像头自动选择正确的录制器 (H264/H265)
  - [ ] `GET /api/cameras/{id}/onvif/profiles` 返回可用 profiles
  - [ ] TDD: `rtk go test ./internal/api/... -v -run ONVIFCamera` → ALL PASS

  **QA Scenarios**:
  ```
  Scenario: ONVIF 摄像头添加成功
    Tool: Bash (curl)
    Steps:
      1. curl -s -X POST http://localhost:9090/api/cameras -H 'Content-Type: application/json' -d '{"name": "Test ONVIF Cam", "protocol": "onvif", "onvif_endpoint": "http://192.168.63.162:8080/onvif/device_service", "username": "admin", "password": "password"}' -u admin:password
      2. 验证响应包含 camera id 和 status
    Expected Result: HTTP 201, 返回带 id 的摄像头对象
    Evidence: .sisyphus/evidence/task-6-onvif-camera-add.txt
  ```

  **Commit**: YES
  - Message: `feat(api): add ONVIF camera management endpoint`
  - Files: `internal/api/handler.go`, `internal/api/onvif_camera_test.go`, `internal/camera/manager.go`

- [x] 7. **ONVIF PTZ 控制 API + TDD**

  **What to do**:
  - TDD: 先写测试用例
  - 在 `internal/api/handler.go` 添加 PTZ 控制端点:
    - `POST /api/cameras/{id}/ptz/move` - PTZ 移动控制
      - Request: `{ "mode": "continuous|absolute|relative", "pan": 0.5, "tilt": 0.3, "zoom": 0.0 }`
    - `POST /api/cameras/{id}/ptz/stop` - 停止 PTZ 移动
    - `GET /api/cameras/{id}/ptz/status` - 获取当前 PTZ 位置
  - 在 CameraManager 中缓存 ONVIF 客户端实例（按 cameraID）
  - 创建 `internal/api/ptz_test.go` 测试文件

  **Must NOT do**:
  - 不实现 PTZ preset 保存/召回
  - 不实现 PTZ 巡航/轨迹功能
  - 不为非 ONVIF 摄像头暴露 PTZ 端点（返回 404）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T5, T6, T8)
  - **Blocks**: T11
  - **Blocked By**: T4

  **References**:
  **Pattern References**:
  - `internal/api/handler.go` - 现有 API 端点模式
  - `internal/camera/manager.go:267-272` - GetRecorder() 方法，参考获取摄像头录制器模式

  **API/Type References**:
  - `internal/onvif/ptz.go` - PTZContinuousMove, PTZAbsoluteMove, PTZStop, PTZGetStatus
  - `internal/onvif/types.go` - PTZVector 类型

  **WHY Each Reference Matters**:
  - ptz.go: 需要了解 PTZ 客户端函数签名来构建 API 端点
  - GetRecorder: 需要了解如何通过 cameraID 获取对应录制器/客户端

  **Acceptance Criteria**:
  - [ ] `POST /api/cameras/{id}/ptz/move` 端点存在
  - [ ] `POST /api/cameras/{id}/ptz/stop` 端点存在
  - [ ] `GET /api/cameras/{id}/ptz/status` 端点存在
  - [ ] 非 ONVIF 摄像头请求 PTZ 返回 404
  - [ ] TDD: `rtk go test ./internal/api/... -v -run PTZ` → ALL PASS

  **QA Scenarios**:
  ```
  Scenario: PTZ 连续移动
    Tool: Bash (curl)
    Steps:
      1. curl -s -X POST http://localhost:9090/api/cameras/{id}/ptz/move -d '{"mode": "continuous", "pan": 0.5, "tilt": 0.0, "zoom": 0.0}' -u admin:password
    Expected Result: HTTP 200, PTZ 命令发送成功
    Evidence: .sisyphus/evidence/task-7-ptz-move.txt

  Scenario: 非 ONVIF 摄像头拒绝 PTZ
    Tool: Bash (curl)
    Steps:
      1. curl -s -X POST http://localhost:9090/api/cameras/{non-onvif-id}/ptz/move -d '{"mode": "continuous", "pan": 0.5}' -u admin:password
    Expected Result: HTTP 404, 错误消息说明该摄像头不支持 PTZ
    Evidence: .sisyphus/evidence/task-7-ptz-reject.txt
  ```

  **Commit**: YES
  - Message: `feat(api): add ONVIF PTZ control endpoint`
  - Files: `internal/api/handler.go`, `internal/api/ptz_test.go`

- [x] 8. **ONVIF 模拟器部署 (.162)**

  **What to do**:
  - 编写 Shell 脚本用于在 192.168.63.162 上部署 ONVIF 虚拟摄像头服务:
    - 使用 onvif-go 内置虚拟摄像头服务器
    - 配置虚拟摄像头参数（H264/H265 profile，分辨率，PTZ 能力）
    - 创建 systemd service 文件实现开机自启
  - 创建部署文档 `docs/private/onvif-simulator-setup.md`
  - 包含：依赖安装、编译、配置、启动、验证步骤

  **Must NOT do**:
  - 不在 .162 上部署完整 NVR（仅 ONVIF 模拟设备）
  - 不修改主项目代码
  - 不需要自动部署（手动执行脚本即可）

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T5, T6, T7)
  - **Blocks**: T12
  - **Blocked By**: T4

  **References**:
  - `github.com/0x524a/onvif-go` - 内置虚拟摄像头服务器，提供 ONVIF 设备模拟
  - `deploy/` - 现有 systemd service 文件模式
  - `Makefile` - 现有编译和部署命令模式

  **Acceptance Criteria**:
  - [ ] 部署脚本存在且可执行
  - [ ] `docs/private/onvif-simulator-setup.md` 存在
  - [ ] 脚本包含：安装 Go、编译 onvif-go server、配置、启动步骤
  - [ ] systemd service 文件存在

  **QA Scenarios**:
  ```
  Scenario: 部署脚本语法正确
    Tool: Bash
    Steps:
      1. bash -n docs/private/deploy-onvif-simulator.sh
      2. ls docs/private/onvif-simulator-setup.md
    Expected Result: bash -n 无语法错误，文档文件存在
    Evidence: .sisyphus/evidence/task-8-deploy-script.txt
  ```

  **Commit**: YES
  - Message: `docs(onvif): add ONVIF simulator setup guide`
  - Files: `docs/private/onvif-simulator-setup.md`, `docs/private/deploy-onvif-simulator.sh`, `deploy/onvif-simulator.service`

---

- [x] 9. **监控大屏 UI (Dashboard.svelte)**

  **What to do**:
  - 创建 `web/src/routes/Dashboard.svelte` 监控大屏页面:
    - 加载支持实时预览的摄像头列表 (protocol=rtsp_h264 或 rtsp_h265)
    - 根据摄像头数量自动选择布局:
      - 1 个摄像头: 全屏显示
      - 2 个摄像头: 左右分屏 (grid-cols-2)
      - 3 个摄像头: 1大+2小 (主区域 + 侧边2小)
      - 4 个摄像头: 2x2 网格
    - 每个摄像头格子包含:
      - HLS.js 播放器 (复用 LiveView.svelte 的 HLS 初始化模式)
      - 摄像头名称标签
      - 状态指示 (录制中/高线/错误)
      - 点击放大为全屏
    - 最大支持 4 个摄像头
    - 自动刷新摄像头列表
    - 无可预览摄像头时显示空状态提示
  - 使用 TailwindCSS 网格布局 + 现有设计系统 token
  - i18n 支持 (Dashboard 相关字符串)

  **Must NOT do**:
  - 不支持 5+ 路摄像头 (Pro 版本范围)
  - 不实现 WebRTC
  - 不支持 MJPEG 摄像头实时预览 (仅显示提示)
  - 不添加录制控制按钮 (仅预览)

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 前端 UI 组件，需要自适应布局和视频播放器集成
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with T10, T11)
  - **Blocks**: F1-F4
  - **Blocked By**: T3

  **References**:
  **Pattern References**:
  - `web/src/routes/LiveView.svelte` - HLS 播放器实现，参考 hls.js 初始化、auth 注入、错误处理
  - `web/src/app.css` - 设计系统 token (th-bg-primary, th-text-primary, .card 等)
  - `web/src/routes/Cameras.svelte` - 摄像头列表加载和状态显示模式
  - `web/src/lib/api.ts` - API 调用函数

  **API/Type References**:
  - `web/src/lib/api.ts` - getDashboardCameras() 函数（T3 添加）
  - Stream URL: `/api/cameras/{id}/stream/index.m3u8`

  **WHY Each Reference Matters**:
  - LiveView.svelte: 核心参考——HLS 播放器初始化和 auth 注入模式必须复用
  - app.css: 设计系统 token 确保视觉一致性
  - Cameras.svelte: 摄像头状态显示模式

  **Acceptance Criteria**:
  - [ ] `web/src/routes/Dashboard.svelte` 文件存在
  - [ ] `#/dashboard` 路由渲染 Dashboard 组件
  - [ ] 1 个摄像头: 全屏显示
  - [ ] 2 个摄像头: 左右分屏
  - [ ] 3 个摄像头: 1大+2小布局
  - [ ] 4 个摄像头: 2x2 网格
  - [ ] 每格显示摄像头名称和状态
  - [ ] `rtk npm run build` (in web/) 成功

  **QA Scenarios**:
  ```
  Scenario: 监控大屏显示 4 路摄像头
    Tool: Playwright
    Steps:
      1. 导航到 #/dashboard
      2. 等待摄像头列表加载
      3. 验证网格布局包含 4 个视频元素
      4. 验证每个视频元素上方有摄像头名称
      5. 截图保存
    Expected Result: 2x2 网格布局，每个格子有视频播放器和摄像头名称
    Evidence: .sisyphus/evidence/task-9-dashboard-4cam.png

  Scenario: 空状态处理
    Tool: Playwright
    Steps:
      1. 禁用所有 H264/H265 摄像头
      2. 导航到 #/dashboard
      3. 验证显示空状态提示
    Expected Result: 显示“无可预览摄像头”提示
    Evidence: .sisyphus/evidence/task-9-dashboard-empty.png
  ```

  **Commit**: YES
  - Message: `feat(web): implement monitoring dashboard with adaptive grid`
  - Files: `web/src/routes/Dashboard.svelte`

- [x] 10. **ONVIF 摄像头发现/管理 UI**

  **What to do**:
  - 扩展 `web/src/routes/Cameras.svelte` 添加 ONVIF 功能:
    - 添加「扫描 ONVIF 设备」按钮
    - 点击后调用 `/api/onvif/discover` 开始发现
    - 显示发现设备列表（名称、IP、厂商、型号）
    - 每个设备显示「查看详情」和「添加为摄像头」按钮
    - 查看详情: 显示设备 profiles（编码、分辨率、PTZ 能力）
    - 添加为摄像头: 选择 profile → 自动创建摄像头
    - 添加成功后刷新摄像头列表
  - 使用现有摄像头添加对话框模式
  - i18n 支持

  **Must NOT do**:
  - 不自动添加发现的设备（必须用户确认）
  - 不修改现有手动添加摄像头的功能

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with T9, T11)
  - **Blocks**: F1-F4
  - **Blocked By**: T5, T6

  **References**:
  **Pattern References**:
  - `web/src/routes/Cameras.svelte` - 现有摄像头管理页面，添加按钮和对话框模式
  - `web/src/lib/api.ts` - discoverONVIFDevices(), addONVIFCamera() 函数（T3 添加）

  **Acceptance Criteria**:
  - [ ] Cameras 页面有「扫描 ONVIF 设备」按钮
  - [ ] 点击后显示发现的设备列表
  - [ ] 可以查看设备详情（profiles）
  - [ ] 可以选择 profile 添加为摄像头
  - [ ] `rtk npm run build` 成功

  **QA Scenarios**:
  ```
  Scenario: ONVIF 发现 UI 交互
    Tool: Playwright
    Steps:
      1. 导航到 #/cameras
      2. 点击「扫描 ONVIF 设备」按钮
      3. 等待扫描完成
      4. 验证发现设备列表显示
    Expected Result: 显示发现结果（可能为空或包含设备）
    Evidence: .sisyphus/evidence/task-10-onvif-discover-ui.png
  ```

  **Commit**: YES
  - Message: `feat(web): add ONVIF camera discovery UI`
  - Files: `web/src/routes/Cameras.svelte`

- [x] 11. **PTZ 控制 UI 组件**

  **What to do**:
  - 创建 `web/src/components/PtzControl.svelte` PTZ 控件:
    - 方向控制板 (上下左右箭头)
    - 变焦控制 (+/- 按钮)
    - 长按持续移动，松开停止
    - 仅对 ONVIF + PTZ 能力摄像头显示
  - 在 LiveView.svelte 中集成 PTZ 控件:
    - 检测摄像头是否支持 PTZ
    - 在视频播放器下方显示 PTZ 控件
  - 在 Dashboard.svelte 中集成 PTZ 控件:
    - 点击摄像头格子时显示 PTZ 控件浮层
  - i18n 支持

  **Must NOT do**:
  - 不实现 PTZ preset 管理
  - 不实现 PTZ 巡航/轨迹
  - 不为非 PTZ 摄像头显示控件

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with T9, T10)
  - **Blocks**: F1-F4
  - **Blocked By**: T7

  **References**:
  **Pattern References**:
  - `web/src/routes/LiveView.svelte` - 实时预览页面，PTZ 控件将嵌入此处
  - `web/src/routes/Dashboard.svelte` (T9) - 监控大屏，PTZ 控件将作为浮层
  - `web/src/lib/api.ts` - ptzControl() 函数（T3 添加）
  - `web/src/app.css` - 设计系统 token

  **Acceptance Criteria**:
  - [ ] `web/src/components/PtzControl.svelte` 文件存在
  - [ ] 方向控制板 (上下左右) 正常渲染
  - [ ] 变焦控制 (+/-) 正常渲染
  - [ ] LiveView 中 ONVIF 摄像头显示 PTZ 控件
  - [ ] 非 ONVIF/无 PTZ 摄像头不显示控件
  - [ ] `rtk npm run build` 成功

  **QA Scenarios**:
  ```
  Scenario: PTZ 控件渲染
    Tool: Playwright
    Steps:
      1. 导航到 ONVIF 摄像头的 #/live/{id}
      2. 等待视频加载
      3. 检查 PTZ 控件区域存在
      4. 验证方向箭头按钮存在
    Expected Result: PTZ 控件在视频下方显示
    Evidence: .sisyphus/evidence/task-11-ptz-ui.png
  ```

  **Commit**: YES
  - Message: `feat(web): add PTZ control component`
  - Files: `web/src/components/PtzControl.svelte`, `web/src/routes/LiveView.svelte`, `web/src/routes/Dashboard.svelte`

---

- [x] 12. **192.168.63.162 部署文档**

  **What to do**:
  - 创建 `docs/private/deployment-162.md` 完整部署文档:
    - 系统环境 (RPi 3B, OS, 网络)
    - ONVIF 模拟器安装和配置步骤
    - NVR 客户端 (.31) 连接 .162 测试步骤
    - 常见问题和故障排除
    - 网络拓扑图

  **Recommended Agent Profile**:
  - **Category**: `writing`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with T13, T14)
  - **Blocks**: F1-F4
  - **Blocked By**: T8

  **Acceptance Criteria**:
  - [ ] `docs/private/deployment-162.md` 存在
  - [ ] 包含安装、配置、测试、故障排除章节
  - [ ] 至少 500 字，结构清晰

  **QA Scenarios**:
  ```
  Scenario: 文档存在且结构完整
    Tool: Bash
    Steps:
      1. wc -l docs/private/deployment-162.md
      2. grep -c '##' docs/private/deployment-162.md
    Expected Result: 至少 50 行，至少 4 个章节标题
    Evidence: .sisyphus/evidence/task-12-deploy-doc.txt
  ```

  **Commit**: YES
  - Message: `docs: add 192.168.63.162 deployment documentation`
  - Files: `docs/private/deployment-162.md`

- [x] 13. **Pro 版本评估文档**

  **What to do**:
  - 创建 `docs/private/pro-version-evaluation.md` 详细评估文档:
    - **市场分析**: 竞品调研（iSpy, Blue Iris, Shinobi, Frigate 等的收费模式）
    - **技术可行性**: 突破 4 路限制的技术方案:
      - HLS Manager maxStreams 调整
      - 内存预算分析 (RPi 3B 905MB)
      - 性能基准测试建议
      - WebRTC vs MSE vs HLS 方案对比
    - **收费模式建议**:
      - 免费版: 最多 4 路 + 基础 ONVIF
      - Pro 版: 无限路 + 完整 ONVIF + GB28181
      - 定价策略 (一次性/订阅/按设备)
      - 许可证实现方案 (License key 验证)
    - **实现路线图**:
      - Phase 1: 4→8 路 (优化 HLS Manager)
      - Phase 2: 8→16 路 (MSE/WebRTC 方案)
      - Phase 3: 16+ 路 (多设备级联)
    - **风险评估**: 技术风险、市场风险、许可证保护风险
    - **结论和建议**

  **Recommended Agent Profile**:
  - **Category**: `writing`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with T12, T14)
  - **Blocks**: F1-F4
  - **Blocked By**: None

  **Acceptance Criteria**:
  - [ ] `docs/private/pro-version-evaluation.md` 存在
  - [ ] 包含市场分析、技术可行性、收费模式、实现路线图、风险评估章节
  - [ ] 至少 2000 字

  **QA Scenarios**:
  ```
  Scenario: Pro 评估文档结构完整
    Tool: Bash
    Steps:
      1. wc -w docs/private/pro-version-evaluation.md
      2. grep -c '##' docs/private/pro-version-evaluation.md
    Expected Result: 至少 2000 词，至少 6 个章节标题
    Evidence: .sisyphus/evidence/task-13-pro-eval.txt
  ```

  **Commit**: YES
  - Message: `docs: add Pro version evaluation document`
  - Files: `docs/private/pro-version-evaluation.md`

- [x] 14. **GB28181 开发方案文档**

  **What to do**:
  - 创建 `docs/private/gb28181-development-plan.md` 详细开发方案:
    - **GB28181 协议概述**: 架构、信令流程、设备注册、实时预览、历史回放
    - **技术架构**:
      - SIP 信令服务 (推荐 github.com/emiago/sipgo)
      - SDP 会话描述
      - RTP/RTCP 媒体接收
      - MP4 录制集成
    - **模块设计**:
      - `internal/gb28181/sip.go` - SIP 服务器
      - `internal/gb28181/device.go` - 设备管理
      - `internal/gb28181/session.go` - 会话管理
      - `internal/gb28181/media.go` - 媒体接收
    - **实现复杂度评估**: 8/10, 预估 6-8 周
    - **关键挑战**: SIP 状态管理、GB28181 XML 扩展、NAT 穿透、设备兼容性
    - **分阶段实施计划**:
      - Phase 1: SIP 注册 + 设备目录
      - Phase 2: 实时预览 (INVITE + RTP)
      - Phase 3: 历史回放
      - Phase 4: 级联对接
    - **依赖和前置条件**
    - **测试策略**

  **Recommended Agent Profile**:
  - **Category**: `writing`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with T12, T13)
  - **Blocks**: F1-F4
  - **Blocked By**: None

  **Acceptance Criteria**:
  - [ ] `docs/private/gb28181-development-plan.md` 存在
  - [ ] 包含协议概述、技术架构、模块设计、复杂度评估、实施计划章节
  - [ ] 至少 3000 字

  **QA Scenarios**:
  ```
  Scenario: GB28181 文档结构完整
    Tool: Bash
    Steps:
      1. wc -w docs/private/gb28181-development-plan.md
      2. grep -c '##' docs/private/gb28181-development-plan.md
    Expected Result: 至少 3000 词，至少 8 个章节标题
    Evidence: .sisyphus/evidence/task-14-gb28181-plan.txt
  ```

  **Commit**: YES
  - Message: `docs: add GB28181 development plan`
  - Files: `docs/private/gb28181-development-plan.md`

## Final Verification Wave

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `rtk go vet ./...` + `rtk go test ./... -v`. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high`
  Start from clean state. Execute EVERY QA scenario from EVERY task. Test cross-task integration (ONVIF discovery → camera add → dashboard view → PTZ control). Test edge cases: device offline, no PTZ camera, mixed protocols. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff (git log/diff). Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **T1**: `chore(docs): add docs/private/ directory and gitignore entry` - docs/private/.gitkeep, .gitignore
- **T2**: `feat(onvif): add ONVIF protocol types and config fields` - model/types.go, config/config.go, api/handler.go
- **T3**: `feat(web): add dashboard routing and API types` - web/src/lib/api.ts, web/src/App.svelte
- **T4**: `feat(onvif): implement ONVIF client module` - internal/onvif/*.go, go.mod
- **T5**: `feat(api): add ONVIF device discovery endpoint` - internal/api/handler.go, internal/onvif/*_test.go
- **T6**: `feat(api): add ONVIF camera management endpoint` - internal/api/handler.go, internal/camera/manager.go
- **T7**: `feat(api): add ONVIF PTZ control endpoint` - internal/api/handler.go, internal/onvif/ptz_test.go
- **T8**: `docs(onvif): add ONVIF simulator setup guide` - docs/private/onvif-simulator-setup.md
- **T9**: `feat(web): implement monitoring dashboard with adaptive grid` - web/src/routes/Dashboard.svelte
- **T10**: `feat(web): add ONVIF camera discovery UI` - web/src/routes/Cameras.svelte
- **T11**: `feat(web): add PTZ control component` - web/src/components/PtzControl.svelte
- **T12**: `docs: add 192.168.63.162 deployment documentation` - docs/private/deployment-162.md
- **T13**: `docs: add Pro version evaluation document` - docs/private/pro-version-evaluation.md
- **T14**: `docs: add GB28181 development plan` - docs/private/gb28181-development-plan.md

---

## Success Criteria

### Verification Commands
```bash
rtk go test ./internal/onvif/... -v      # Expected: ALL PASS
rtk go test ./internal/api/... -v         # Expected: ALL PASS (incl. ONVIF tests)
rtk go test ./internal/camera/... -v      # Expected: ALL PASS
rtk go vet ./...                          # Expected: no warnings
cd web && rtk npm run build               # Expected: successful build
ls docs/private/                          # Expected: at least 4 files
grep -q "docs/private/" .gitignore        # Expected: exit code 0
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All tests pass (TDD coverage)
- [ ] ONVIF discovery finds virtual camera on .162
- [ ] Dashboard shows 1-4 cameras simultaneously
- [ ] PTZ controls work on virtual camera
- [ ] docs/private/ not tracked by git
- [ ] Pro evaluation document covers: licensing model, technical approach, 4-camera limit analysis
- [ ] GB28181 document covers: architecture, SIP integration, implementation phases, complexity assessment
