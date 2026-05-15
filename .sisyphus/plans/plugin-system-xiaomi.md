# Plugin System + Xiaomi Camera Support

## TL;DR

> **Quick Summary**: 为 MiBee NVR 实现插件接口体系，并从 go2rtc 移植小米摄像头（MISS/CS2 协议）作为首个插件。包含后端录制器、小米云认证、前端设备发现 UI、构建标签、文档和部署测试。
> 
> **Deliverables**:
> - 插件接口定义 + 注册表（`internal/plugin/`）
> - 小米摄像头录制器（从 go2rtc 移植 CS2 协议）
> - 小米云认证 API（登录、Token 管理、设备发现）
> - 前端：小米账号登录 + 摄像头发现 + 一键添加
> - 构建标签支持（`//go:build xiaomi`）
> - 插件开发文档 + 小米配置文档
> - 部署到 RPi 并验证
> 
> **Estimated Effort**: Medium-Large
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: Plugin Interface → Xiaomi Core → Xiaomi Recorder → Frontend → Integration → Deploy

---

## Context

### Original Request
实现 NVR 插件安装能力，把小米摄像头支持从 go2rtc 复制出来做成插件，插件能力做成规范使其他扩展功能以插件方式扩展。在独立分支完成，包含前端设计开发、文档更新、部署测试。

### Interview Summary
**Key Discussions**:
- 插件方案：Go 接口注册 + 构建标签（编译时），不用 Go plugin (.so)
- 代码来源：从 go2rtc (MIT) 复制 pkg/xiaomi 适配
- 小米协议：MISS (Mi Secure Streaming) + CS2 P2P + ChaCha20 加密
- CGO_ENABLED=0 保持不变，单二进制部署
- 前端需要完整的账号登录→设备发现→一键添加流程

**Research Findings**:
- go2rtc pkg/xiaomi: 2,630 行，MIT 协议
- CS2 协议自包含（~690 行，stdlib-only），TUTK 需额外移植 10+ 文件
- 零新增依赖：golang.org/x/crypto + pion/rtp 已有
- 小米云 API：account.xiaomi.com OAuth，需处理 captcha/2FA
- 设备发现 API：/v2/home/device_list_page

### Metis Review
**Identified Gaps** (addressed):
- TUTK/Legacy 协议范围过大 → V1 仅 CS2，排除 TUTK 和 Legacy
- go2rtc 内部依赖深度被低估 → 仅移植自包含的 crypto + cs2 + miss/client + cloud
- 通用插件抽象层过早 → 先直接集成到工厂方法，预留提取能力
- Annex B → AVCC 转换需自实现 → ~20 行内联代码
- 构建标签方向 → V1 不加标签（全量编译），后续可加
- 前端认证流程 → 后端代理小米云 API

---

## Work Objectives

### Core Objective
为 MiBee NVR 建立可扩展的摄像头协议插件体系，并以小米摄像头（MISS/CS2 协议）作为首个实现，实现从账号登录到自动发现到一键添加的完整用户体验。

### Concrete Deliverables
- `internal/plugin/plugin.go` — 插件接口定义与注册表
- `plugins/xiaomi/` — 小米摄像头插件（CS2-only）
- `internal/camera/manager.go` — 改造为使用插件注册表
- `internal/api/handler.go` — 新增小米认证与设备发现 API
- `web/src/routes/Cameras.svelte` — 小米设备发现 UI
- `web/src/lib/api.ts` — 小米 API 客户端
- `docs/` — 插件开发指南 + 小米配置文档
- 独立 git 分支 + PR

### Definition of Done
- [ ] `make build` 成功（CGO_ENABLED=0），二进制增量 ≤2MB
- [ ] `make cross` 成功，arm64 二进制可部署到 RPi
- [ ] `go test ./...` 全部通过
- [ ] 前端 `npm run build` 无错误
- [ ] 部署到 RPi，RSS 增量 ≤30MB（1 路小米摄像头连接）
- [ ] Web UI 可完成：输入小米账号 → 看到设备列表 → 一键添加摄像头

### Must Have
- CS2 协议支持（覆盖 2020 年后的小米摄像头）
- 小米云账号登录（后端代理，Token 持久化）
- 设备发现（列出账号下所有摄像头）
- 一键添加到 NVR
- 插件接口定义（可供未来其他品牌扩展）
- MIT 版权声明（go2rtc 代码）
- 中英文 i18n

### Must NOT Have (Guardrails)
- 不移植 TUTK 协议（V1 仅 CS2）
- 不支持 Legacy 旧型号摄像头
- 不支持双向音频（仅录制）
- 不使用 Go plugin (.so) 动态加载
- 不新增第三方依赖
- 不破坏现有 RTSP/HTTP/ONVIF 协议支持
- 不改变现有 REST API 路由格式
- 不修改 `model.Recorder` 接口签名
- 不在 V1 加构建标签（全量编译）
- 不在浏览器直接调用小米云 API（全部通过 NVR 后端代理）

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed.

### Test Decision
- **Infrastructure exists**: YES (Go testify + Playwright E2E)
- **Automated tests**: TDD — tests first for plugin interface, then xiaomi implementation
- **Framework**: Go testing + testify/require (backend), Playwright (E2E)

### QA Policy
Every task includes agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Foundation — plugin interface + core xiaomi port):
├── Task 1: Plugin interface definition [quick]
├── Task 2: Port crypto module from go2rtc [quick]
├── Task 3: Port CS2 P2P transport from go2rtc [deep]
└── Task 4: Port MISS protocol client from go2rtc [deep]

Wave 2 (Backend integration — recorder + API + cloud auth):
├── Task 5: Xiaomi Recorder (implements model.Recorder) [deep]
├── Task 6: Xiaomi Cloud Auth API [unspecified-high]
├── Task 7: Refactor CameraManager to use plugin registry [deep]
└── Task 8: Xiaomi config schema + validation [quick]

Wave 3 (Frontend + Docs):
├── Task 9: Frontend Xiaomi login + device discovery UI [visual-engineering]
├── Task 10: i18n strings (en + zh) [quick]
├── Task 11: Plugin development guide doc [writing]
└── Task 12: Xiaomi setup guide doc [writing]

Wave 4 (Integration + Deploy):
├── Task 13: E2E tests for Xiaomi flow [unspecified-high]
├── Task 14: Cross-compile + deploy to RPi [quick]
└── Task 15: Integration verification on RPi [deep]

Wave FINAL (Review):
├── F1: Plan compliance audit [oracle]
├── F2: Code quality review [unspecified-high]
├── F3: Real QA on RPi [unspecified-high]
└── F4: Scope fidelity check [deep]
```

### Dependency Matrix

| Task | Depends On | Blocks |
|------|-----------|--------|
| 1 (Plugin Interface) | - | 5, 7, 9 |
| 2 (Crypto) | - | 4 |
| 3 (CS2 Transport) | - | 4 |
| 4 (MISS Client) | 2, 3 | 5 |
| 5 (Xiaomi Recorder) | 1, 4 | 13, 15 |
| 6 (Cloud Auth API) | - | 9, 13 |
| 7 (CameraManager refactor) | 1, 5 | 13, 14 |
| 8 (Config schema) | 1 | 5, 7 |
| 9 (Frontend UI) | 6, 1 | 13 |
| 10 (i18n) | 9 | 13 |
| 11 (Plugin dev doc) | 1, 5 | - |
| 12 (Xiaomi setup doc) | 6, 14 | - |
| 13 (E2E tests) | 5, 7, 9, 10 | F1-F4 |
| 14 (Deploy) | 7 | 15 |
| 15 (RPi verification) | 14 | F1-F4 |

### Agent Dispatch Summary

- **Wave 1**: 4 tasks — T1 → `quick`, T2 → `quick`, T3 → `deep`, T4 → `deep`
- **Wave 2**: 4 tasks — T5 → `deep`, T6 → `unspecified-high`, T7 → `deep`, T8 → `quick`
- **Wave 3**: 4 tasks — T9 → `visual-engineering`, T10 → `quick`, T11 → `writing`, T12 → `writing`
- **Wave 4**: 3 tasks — T13 → `unspecified-high`, T14 → `quick`, T15 → `deep`
- **FINAL**: 4 tasks — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

- [x] 1. Plugin Interface Definition

  **What to do**:
  - Create `internal/plugin/plugin.go` with `RecorderPlugin` interface and global registry
  - Interface methods: `Name()`, `Protocols()`, `NewRecorder()`, `RegisterRoutes()`, `ConfigSchema()`
  - Registry: `Register(p RecorderPlugin)`, `GetPlugin(protocol string) RecorderPlugin`, `AllPlugins()`
  - Write unit tests for register/lookup lifecycle

  **Must NOT do**:
  - Don't implement runtime dynamic loading (no Go plugin .so)
  - Don't add build tags yet (V1 all compiled in)

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3, 4)
  - **Blocks**: Tasks 5, 7, 8, 9
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/model/types.go:10-14` — `Recorder` interface to understand what plugins must produce
  - `internal/camera/manager.go:67-137` — Current `createRecorder()` factory switch — this is what plugins replace

  **API/Type References**:
  - `internal/config/config.go:CameraConfig` — Camera config struct that plugins receive
  - `internal/storage/manager.go:Manager` — SegmentStore interface for recording lifecycle

  **Test References**:
  - `internal/camera/manager_test.go` — Test patterns for camera/recorder creation

  **Acceptance Criteria**:
  - [ ] `internal/plugin/plugin.go` exists with RecorderPlugin interface
  - [ ] `go test ./internal/plugin/... -v` passes (register, lookup, multi-plugin)
  - [ ] Interface has: Name, Protocols, NewRecorder, RegisterRoutes, ConfigSchema methods
  - [ ] Registry supports concurrent access (sync.RWMutex)

  **QA Scenarios**:
  ```
  Scenario: Plugin registration and lookup
    Tool: Bash
    Steps:
      1. Run `go test ./internal/plugin/... -v -run TestRegister`
      2. Assert all tests pass with 0 failures
    Expected Result: PASS
    Evidence: .sisyphus/evidence/task-1-plugin-register.txt
  ```

  **Commit**: YES
  - Message: `feat(plugin): define plugin interface and registration system`
  - Files: `internal/plugin/plugin.go`, `internal/plugin/plugin_test.go`

---

- [x] 2. Port Crypto Module from go2rtc

  **What to do**:
  - Copy `pkg/xiaomi/crypto/crypto.go` (68 lines) from go2rtc to `plugins/xiaomi/crypto.go`
  - This file is self-contained: only uses `golang.org/x/crypto/chacha20` + `golang.org/x/crypto/nacl/box` + `crypto/rand`
  - **Zero modifications needed** — copy as-is
  - Add MIT license header with go2rtc attribution
  - Write unit tests for GenerateKey, CalcSharedKey, Encode, Decode roundtrip

  **Must NOT do**:
  - Don't modify the crypto algorithms
  - Don't add external dependencies beyond what go2rtc uses

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3, 4)
  - **Blocks**: Task 4
  - **Blocked By**: None

  **References**:
  **External References**:
  - go2rtc source: `https://github.com/AlexxIT/go2rtc/blob/master/pkg/xiaomi/crypto/crypto.go`
  - Local copy: `/tmp/go2rtc/pkg/xiaomi/crypto/crypto.go`
  - License: MIT — must include copyright notice

  **Acceptance Criteria**:
  - [ ] `plugins/xiaomi/crypto.go` exists with MIT header
  - [ ] `go test ./plugins/xiaomi/... -v -run TestCrypto` passes
  - [ ] No new dependencies in go.mod (golang.org/x/crypto already present)

  **QA Scenarios**:
  ```
  Scenario: Crypto roundtrip encode/decode
    Tool: Bash
    Steps:
      1. Run `go test ./plugins/xiaomi/... -v -run TestCryptoRoundtrip`
      2. Assert: shared key derivation produces same result from both sides
      3. Assert: encode then decode returns original plaintext
    Expected Result: All assertions pass
    Evidence: .sisyphus/evidence/task-2-crypto-roundtrip.txt
  ```

  **Commit**: YES (group with Task 3)
  - Message: `feat(xiaomi): port crypto and CS2 transport from go2rtc (MIT)`
  - Files: `plugins/xiaomi/crypto.go`, `plugins/xiaomi/crypto_test.go`

- [x] 3. Port CS2 P2P Transport from go2rtc

  **What to do**:
  - Copy `pkg/xiaomi/miss/cs2/conn.go` (506 lines) from go2rtc to `plugins/xiaomi/cs2.go`
  - This file is self-contained: only uses stdlib (`net`, `encoding/binary`, `sync`, `time`)
  - Adaptations needed: change package from `cs2` to `xiaomi`, rename `Dial` if conflicts
  - Add MIT license header
  - Write unit tests for handshake framing (mock UDP connection)

  **Must NOT do**:
  - Don't port TUTK transport (pkg/tutk/) — out of scope for V1
  - Don't add external dependencies

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 4)
  - **Blocks**: Task 4
  - **Blocked By**: None

  **References**:
  **External References**:
  - go2rtc source: `https://github.com/AlexxIT/go2rtc/blob/master/pkg/xiaomi/miss/cs2/conn.go`
  - Local copy: `/tmp/go2rtc/pkg/xiaomi/miss/cs2/conn.go`

  **Pattern References**:
  - `internal/recorder/h264.go:H264Recorder` — existing recorder uses net connections, understand pattern

  **Acceptance Criteria**:
  - [ ] `plugins/xiaomi/cs2.go` exists with MIT header
  - [ ] `go vet ./plugins/xiaomi/...` — no warnings
  - [ ] Unit test: Conn interface satisfies read/write packet requirements

  **QA Scenarios**:
  ```
  Scenario: CS2 compiles and passes vet
    Tool: Bash
    Steps:
      1. Run `go vet ./plugins/xiaomi/...`
      2. Assert: no warnings or errors
    Expected Result: clean vet
    Evidence: .sisyphus/evidence/task-3-cs2-vet.txt
  ```

  **Commit**: YES (group with Task 2)
  - Message: `feat(xiaomi): port crypto and CS2 transport from go2rtc (MIT)`
  - Files: `plugins/xiaomi/cs2.go`, `plugins/xiaomi/cs2_test.go`

---

- [x] 4. Port MISS Protocol Client from go2rtc

  **What to do**:
  - Copy `pkg/xiaomi/miss/client.go` (338 lines) from go2rtc to `plugins/xiaomi/miss.go`
  - Remove TUTK vendor case — keep only CS2
  - Adapt imports: `cs2` → local package, `crypto` → local package
  - Copy `pkg/xiaomi/miss/producer.go` as reference for media packet parsing (codec IDs, packet format)
  - Add MIT license header
  - Write unit tests for login flow, StartMedia command construction

  **Must NOT do**:
  - Don't port backchannel/two-way audio (miss/backchannel.go)
  - Don't port legacy protocol (pkg/xiaomi/legacy/)
  - Don't copy go2rtc's pkg/h264 or pkg/h264/annexb — use inline Annex B parsing

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (if Tasks 2, 3 code is available in branch)
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3)
  - **Blocks**: Task 5
  - **Blocked By**: Tasks 2, 3 (needs crypto + cs2)

  **References**:
  **External References**:
  - go2rtc source: `https://github.com/AlexxIT/go2rtc/blob/master/pkg/xiaomi/miss/client.go`
  - Local copy: `/tmp/go2rtc/pkg/xiaomi/miss/client.go`
  - Local copy: `/tmp/go2rtc/pkg/xiaomi/miss/producer.go`

  **Pattern References**:
  - `internal/recorder/h264.go:writeFrames()` — existing H264 NALU handling pattern
  - `internal/muxer/mp4mux.go:parseSPSResolution()` — SPS parsing already exists in NVR

  **Acceptance Criteria**:
  - [ ] `plugins/xiaomi/miss.go` exists with MIT header
  - [ ] TUTK vendor case removed, only CS2 supported
  - [ ] `go vet ./plugins/xiaomi/...` — no warnings
  - [ ] Unit test: login command JSON format correct
  - [ ] Unit test: StartMedia command construction for known models

  **QA Scenarios**:
  ```
  Scenario: MISS client compiles and login format is correct
    Tool: Bash
    Steps:
      1. Run `go test ./plugins/xiaomi/... -v -run TestMissLogin`
      2. Assert: login JSON contains public_key, sign, support_encrypt fields
    Expected Result: PASS
    Evidence: .sisyphus/evidence/task-4-miss-login.txt
  ```

  **Commit**: YES
  - Message: `feat(xiaomi): port MISS protocol client from go2rtc (MIT)`
  - Files: `plugins/xiaomi/miss.go`, `plugins/xiaomi/miss_test.go`
---

- [x] 5. Xiaomi Recorder (implements model.Recorder)

  **What to do**:
  - Create `plugins/xiaomi/recorder.go` implementing `model.Recorder` interface
  - Follow `internal/recorder/h264.go` pattern exactly: constructor, Start() with reconnect loop, Stop(), Status()
  - In Start(): dial MISS client → authenticate → start media → read packets in loop → write NALUs to MP4Muxer
  - Implement inline Annex B → AVCC conversion (~20 lines): find 00 00 00 01/00 00 01 start codes, extract NALUs, prepend 4-byte length
  - Reuse `muxer/mp4mux.go:parseSPSResolution()` for SPS parsing
  - Segment lifecycle: `store.CreateSegment()` → write frames → `store.CloseSegment()` → `db.InsertRecording()`
  - Write comprehensive unit tests with mock MISS client

  **Must NOT do**:
  - Don't implement two-way audio
  - Don't copy go2rtc's pkg/h264/annexb package — write inline conversion
  - Don't change model.Recorder interface

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 6, 8)
  - **Parallel Group**: Wave 2
  - **Blocks**: Tasks 13, 15
  - **Blocked By**: Tasks 1, 4 (needs plugin interface + MISS client)

  **References**:
  **Pattern References**:
  - `internal/recorder/h264.go` — FULL reference for recorder implementation pattern (constructor, Start, ring buffer, segment lifecycle, reconnect)
  - `internal/recorder/h265.go` — H265 NALU handling, VPS/SPS/PPS tracking
  - `internal/recorder/onvif.go` — Example of delegating to another recorder, factory pattern

  **API/Type References**:
  - `internal/model/types.go:10-14` — Recorder interface (Start/Stop/Status)
  - `internal/storage/manager.go` — SegmentStore: CreateSegment/CloseSegment/WriteFrame
  - `internal/muxer/mp4mux.go` — MP4Muxer: AddH264Track/AddH265Track/WriteSample

  **Test References**:
  - `internal/recorder/h264_test.go` — Recorder test patterns with mock stores

  **Acceptance Criteria**:
  - [ ] `plugins/xiaomi/recorder.go` implements model.Recorder
  - [ ] `go test ./plugins/xiaomi/... -v -run TestXiaomiRecorder` passes
  - [ ] Test: mock MISS client → recorder produces MP4 segment file
  - [ ] Test: recorder reconnects after simulated disconnect
  - [ ] Annex B → AVCC conversion correct (SPS/PPS/IDR NALUs properly framed)

  **QA Scenarios**:
  ```
  Scenario: XiaomiRecorder produces valid MP4 segment from mock stream
    Tool: Bash
    Steps:
      1. Run `go test ./plugins/xiaomi/... -v -run TestXiaomiRecorderSegment`
      2. Assert: segment file created with non-zero size
      3. Assert: segment contains valid MP4 boxes (ftyp, moov, moof, mdat)
    Expected Result: PASS
    Evidence: .sisyphus/evidence/task-5-recorder-segment.txt
  ```

  **Commit**: YES
  - Message: `feat(xiaomi): implement XiaomiRecorder for NVR`
  - Files: `plugins/xiaomi/recorder.go`, `plugins/xiaomi/recorder_test.go`

---

- [x] 6. Xiaomi Cloud Auth API

  **What to do**:
  - Copy `pkg/xiaomi/cloud.go` (568 lines) from go2rtc to `plugins/xiaomi/cloud.go`
  - Adapt: replace `core.RandString` with `crypto/rand` equivalent, replace `zerolog` with `slog`
  - Add API endpoints in `internal/api/handler.go`:
    - `POST /api/xiaomi/auth` — login with username/password, returns userID
    - `POST /api/xiaomi/auth/captcha` — submit captcha
    - `POST /api/xiaomi/auth/verify` — submit 2FA code
    - `GET /api/xiaomi/devices?user={id}` — list cameras from Xiaomi cloud
  - Store passToken in NVR config under `xiaomi` section
  - Proxy all Xiaomi API calls through NVR backend (never browser-direct)
  - Write integration tests for auth API endpoints

  **Must NOT do**:
  - Don't call Xiaomi APIs directly from frontend
  - Don't store credentials in localStorage
  - Don't expose raw Xiaomi session cookies to browser

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 5, 7, 8)
  - **Parallel Group**: Wave 2
  - **Blocks**: Tasks 9, 12, 13
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/api/handler.go:Routes()` — chi router registration pattern
  - `internal/middleware/auth.go` — existing auth middleware for protected endpoints

  **External References**:
  - go2rtc source: `https://github.com/AlexxIT/go2rtc/blob/master/pkg/xiaomi/cloud.go`
  - Local copy: `/tmp/go2rtc/pkg/xiaomi/cloud.go`
  - Local copy: `/tmp/go2rtc/internal/xiaomi/xiaomi.go` (cloud API usage pattern)

  **API/Type References**:
  - `internal/config/config.go` — config structure, atomic save pattern

  **Acceptance Criteria**:
  - [ ] `plugins/xiaomi/cloud.go` exists with MIT header
  - [ ] API endpoints registered under `/api/xiaomi/`
  - [ ] `go test ./internal/api/... -v -run TestXiaomi` passes
  - [ ] Test: POST /api/xiaomi/auth with wrong credentials returns structured error
  - [ ] Test: GET /api/xiaomi/devices without auth returns empty list

  **QA Scenarios**:
  ```
  Scenario: Xiaomi auth API returns structured error
    Tool: Bash
    Steps:
      1. Run `go test ./internal/api/... -v -run TestXiaomiAuth`
      2. Assert: wrong credentials return 401 with JSON error body
    Expected Result: PASS
    Evidence: .sisyphus/evidence/task-6-auth-api.txt
  ```

  **Commit**: YES
  - Message: `feat(api): add Xiaomi cloud auth and device discovery endpoints`
  - Files: `plugins/xiaomi/cloud.go`, `internal/api/handler.go`

---

- [x] 7. Refactor CameraManager to Use Plugin Registry

  **What to do**:
  - Add plugin registry lookup to `camera/manager.go:createRecorder()`
  - If protocol matches a registered plugin, delegate to plugin's NewRecorder()
  - Keep existing built-in recorder creation as fallback
  - Register Xiaomi plugin in `main.go` init
  - Add `xiaomi` to `model.ValidEncodingsForProtocol`
  - Write tests verifying plugin-registered protocol creates correct recorder

  **Must NOT do**:
  - Don't remove existing RTSP/HTTP/ONVIF recorder creation
  - Don't change model.Recorder interface
  - Don't break existing tests

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 5, 6, 8)
  - **Parallel Group**: Wave 2
  - **Blocks**: Tasks 13, 14
  - **Blocked By**: Task 1 (needs plugin interface)

  **References**:
  **Pattern References**:
  - `internal/camera/manager.go:67-137` — Current `createRecorder()` factory — add plugin case here
  - `internal/model/types.go:139-143` — `ValidEncodingsForProtocol` map — add xiaomi entry

  **Test References**:
  - `internal/camera/manager_test.go` — Existing factory tests

  **Acceptance Criteria**:
  - [ ] `createRecorder()` checks plugin registry before built-in factories
  - [ ] `xiaomi` protocol registered in `ValidEncodingsForProtocol`
  - [ ] `main.go` imports and registers Xiaomi plugin
  - [ ] `go test ./internal/camera/... -v` passes (existing + new tests)
  - [ ] Existing RTSP/HTTP/ONVIF tests still pass unchanged

  **QA Scenarios**:
  ```
  Scenario: Plugin registry correctly delegates xiaomi protocol
    Tool: Bash
    Steps:
      1. Run `go test ./internal/camera/... -v -run TestPluginFactory`
      2. Assert: xiaomi protocol creates XiaomiRecorder
      3. Assert: rtsp protocol still creates H264Recorder (no regression)
    Expected Result: Both pass
    Evidence: .sisyphus/evidence/task-7-plugin-factory.txt
  ```

  **Commit**: YES
  - Message: `refactor(camera): use plugin registry for recorder creation`
  - Files: `internal/camera/manager.go`, `internal/model/types.go`, `cmd/mibee-nvr/main.go`

---

- [x] 8. Xiaomi Config Schema + Validation

  **What to do**:
  - Add `XiaomiConfig` to `internal/config/config.go` with fields: `UserID`, `Token` (passToken), `Region`
  - Add `xiaomi` section to Config struct
  - Update `config.example.yaml` with xiaomi section and comments about token security
  - Add validation: if xiaomi cameras exist in config, xiaomi section must have Token
  - CameraConfig: add optional fields for Xiaomi-specific params (DID, Model, Vendor)
  - Write config validation tests

  **Must NOT do**:
  - Don't encrypt tokens in V1 (document that they are plaintext)
  - Don't pollute existing CameraConfig with xiaomi-only fields — use `map[string]interface{}` or dedicated section

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 5, 6, 7)
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 5 (config validation for recorder)
  - **Blocked By**: Task 1 (plugin config schema interface)

  **References**:
  **Pattern References**:
  - `internal/config/config.go` — YAML config with struct tags + applyDefaults() + Validate()

  **Acceptance Criteria**:
  - [ ] `XiaomiConfig` struct with UserID, Token, Region fields
  - [ ] config.example.yaml updated with xiaomi section
  - [ ] `go test ./internal/config/... -v` passes

  **QA Scenarios**:
  ```
  Scenario: Config validation rejects xiaomi camera without token
    Tool: Bash
    Steps:
      1. Run `go test ./internal/config/... -v -run TestXiaomiConfig`
      2. Assert: config with xiaomi camera but no token fails validation
    Expected Result: PASS
    Evidence: .sisyphus/evidence/task-8-config-validation.txt
  ```

  **Commit**: YES
  - Message: `feat(config): add Xiaomi config schema and validation`
  - Files: `internal/config/config.go`, `config.example.yaml`

- [x] 9. Frontend Xiaomi Login + Device Discovery UI

  **What to do**:
  - Add Xiaomi device discovery section to `Cameras.svelte` (similar to ONVIF discovery panel at line 422-484)
  - Section expands to show: login form → device list → one-click add
  - Login form: username, password fields → POST /api/xiaomi/auth
  - Handle captcha/2FA: show image/input when API returns captcha/verify required
  - Device list: GET /api/xiaomi/devices → show cards with camera name, model, IP
  - Each device card has "Add to NVR" button → POST /api/cameras with Xiaomi camera config
  - Add Xiaomi API functions to `web/src/lib/api.ts`
  - Ensure responsive design (mobile + desktop)

  **Must NOT do**:
  - Don't call Xiaomi APIs directly from browser
  - Don't store Xiaomi credentials in localStorage
  - Don't add a new route (keep within #/cameras)
  - Don't add new npm dependencies

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 10, 11, 12)
  - **Parallel Group**: Wave 3
  - **Blocks**: Tasks 10, 13
  - **Blocked By**: Tasks 6 (needs API endpoints), 1 (plugin interface for config)

  **References**:
  **Pattern References**:
  - `web/src/routes/Cameras.svelte:422-484` — ONVIF discovery panel (closest existing pattern)
  - `web/src/routes/Login.svelte` — Login form pattern with validation, loading states
  - `web/src/lib/api.ts` — API client with auth header injection

  **API/Type References**:
  - `web/src/lib/api.ts:apiRequest<T>()` — generic API wrapper
  - `web/src/lib/i18n/en.json` — i18n string keys

  **Acceptance Criteria**:
  - [ ] Xiaomi discovery panel in Cameras.svelte
  - [ ] Login form with validation (empty fields, wrong credentials)
  - [ ] Device list displayed after successful auth
  - [ ] "Add to NVR" button creates camera via API
  - [ ] `cd web && npm run build` — no errors

  **QA Scenarios**:
  ```
  Scenario: Xiaomi login form validates empty fields
    Tool: Bash
    Steps:
      1. Run `cd web && npm run build`
      2. Assert: build succeeds with 0 errors
    Expected Result: PASS
    Evidence: .sisyphus/evidence/task-9-frontend-build.txt

  Scenario: Xiaomi panel renders in camera management page
    Tool: Playwright
    Steps:
      1. Navigate to #/cameras
      2. Assert: Xiaomi discovery section visible
      3. Assert: Login form has username and password inputs
    Expected Result: PASS
    Evidence: .sisyphus/evidence/task-9-frontend-render.png
  ```

  **Commit**: YES
  - Message: `feat(ui): add Xiaomi device discovery to camera management`
  - Files: `web/src/routes/Cameras.svelte`, `web/src/lib/api.ts`

---

- [x] 10. i18n Strings (English + Chinese)

  **What to do**:
  - Add all Xiaomi-related UI strings to `web/src/lib/i18n/en.json`
  - Add corresponding Chinese translations to `web/src/lib/i18n/zh.json`
  - Strings needed: xiaomi.title, xiaomi.login, xiaomi.username, xiaomi.password, xiaomi.signIn, xiaomi.captcha, xiaomi.verify, xiaomi.noDevices, xiaomi.addCamera, xiaomi.deviceAdded, xiaomi.authFailed, etc.

  **Must NOT do**:
  - Don't hardcode strings in components
  - Don't miss any strings used in Task 9 UI

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3
  - **Blocks**: Task 13
  - **Blocked By**: Task 9 (needs to know which strings)

  **Acceptance Criteria**:
  - [ ] en.json has xiaomi.* keys
  - [ ] zh.json has matching xiaomi.* keys
  - [ ] All strings from Task 9 UI covered

  **Commit**: YES (group with Task 9)
  - Message: `feat(i18n): add Xiaomi strings for en/zh`
  - Files: `web/src/lib/i18n/en.json`, `web/src/lib/i18n/zh.json`

---

- [x] 11. Plugin Development Guide

  **What to do**:
  - Create `docs/en/plugin-development.md`
  - Document: plugin interface, how to implement RecorderPlugin, registration, config schema, API route extension
  - Include example: minimal plugin skeleton
  - Document build tag convention (future)
  - Document testing patterns for plugins

  **Recommended Agent Profile**:
  - **Category**: `writing`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3
  - **Blocks**: None
  - **Blocked By**: Tasks 1, 5 (needs real interface + implementation as reference)

  **Acceptance Criteria**:
  - [ ] `docs/en/plugin-development.md` exists
  - [ ] Contains interface spec, example plugin, testing guide

  **Commit**: YES
  - Message: `docs: add plugin development guide`
  - Files: `docs/en/plugin-development.md`

---

- [x] 12. Xiaomi Setup Guide

  **What to do**:
  - Create `docs/en/xiaomi-setup.md`
  - Document: how to get Xiaomi credentials, supported camera models, config format, troubleshooting
  - List CS2-supported models with go2rtc model names
  - Document security note about token storage
  - Chinese version: `docs/zh/xiaomi-setup.md`

  **Recommended Agent Profile**:
  - **Category**: `writing`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3
  - **Blocks**: None
  - **Blocked By**: Tasks 6, 14 (needs tested API + deployed setup as reference)

  **Acceptance Criteria**:
  - [ ] `docs/en/xiaomi-setup.md` exists
  - [ ] `docs/zh/xiaomi-setup.md` exists
  - [ ] Lists supported models, config example, troubleshooting

  **Commit**: YES
  - Message: `docs: add Xiaomi camera setup guide (en/zh)`
  - Files: `docs/en/xiaomi-setup.md`, `docs/zh/xiaomi-setup.md`

- [x] 13. E2E Tests for Xiaomi Flow

  **What to do**:
  - Create `e2e-tests/tests/xiaomi-setup.spec.ts`
  - Test: Xiaomi discovery panel visible on cameras page
  - Test: Login form validates empty fields
  - Test: Auth API returns error for wrong credentials
  - Test: Device list API returns JSON structure
  - Test: Add camera button creates camera via API
  - Use existing `hls-helpers.ts` login pattern for authentication

  **Must NOT do**:
  - Don't test with real Xiaomi credentials in E2E
  - Don't depend on external network for E2E tests

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 4 (sequential after Wave 3)
  - **Blocks**: F1-F4
  - **Blocked By**: Tasks 5, 7, 9, 10

  **References**:
  **Pattern References**:
  - `e2e-tests/tests/hls-helpers.ts:navigateToDashboard()` — login + navigation pattern
  - `e2e-tests/tests/hls-playback.spec.ts` — existing E2E test structure

  **Acceptance Criteria**:
  - [ ] `e2e-tests/tests/xiaomi-setup.spec.ts` exists
  - [ ] `cd e2e-tests && npx playwright test tests/xiaomi-setup.spec.ts` passes
  - [ ] 3+ test scenarios covering auth, device list, add camera

  **Commit**: YES
  - Message: `test(e2e): add Playwright tests for Xiaomi setup flow`
  - Files: `e2e-tests/tests/xiaomi-setup.spec.ts`

---

- [x] 14. Cross-compile + Deploy to RPi

  **What to do**:
  - Verify `make cross` produces arm64 binary with xiaomi plugin included
  - Check binary size: must be ≤23MB (21MB baseline + 2MB max)
  - Deploy to RPi: `make deploy RPi_HOST=mickey@192.168.63.31`
  - Verify service restart: `make deploy-check RPi_HOST=mickey@192.168.63.31`
  - Verify Xiaomi API endpoints accessible: `curl /api/xiaomi/auth`, `curl /api/xiaomi/devices`

  **Must NOT do**:
  - Don't change CGO_ENABLED=0
  - Don't modify Docker builds (can be updated later)

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 13)
  - **Parallel Group**: Wave 4
  - **Blocks**: Task 15
  - **Blocked By**: Task 7 (needs compiled binary)

  **Acceptance Criteria**:
  - [ ] `make cross` succeeds, arm64 binary ≤23MB
  - [ ] Deployed to RPi, service healthy
  - [ ] `/api/health` returns 200
  - [ ] `/api/xiaomi/devices` returns 200 (empty list without auth)

  **QA Scenarios**:
  ```
  Scenario: Deployed NVR responds to Xiaomi API
    Tool: Bash (ssh)
    Steps:
      1. `ssh mickey@192.168.63.31 "curl -s -u admin:admin http://localhost:9090/api/xiaomi/devices"`
      2. Assert: returns 200 with valid JSON
      3. `ssh mickey@192.168.63.31 "ps aux | grep mibee-nvr | grep -v grep | awk '{print $6}'"`
      4. Assert: RSS ≤100MB
    Expected Result: API responds, memory within budget
    Evidence: .sisyphus/evidence/task-14-deploy-check.txt
  ```

  **Commit**: YES
  - Message: `chore(deploy): deploy xiaomi plugin to RPi for testing`
  - Files: (no code changes, deployment only)

---

- [x] 15. Integration Verification on RPi

  **What to do**:
  - On RPi: verify full Xiaomi flow end-to-end
  - Test `/api/xiaomi/auth` with real Xiaomi account credentials
  - Test device discovery returns actual camera list
  - Add a Xiaomi camera to NVR via API
  - Verify recording starts (check segment files created)
  - Verify HLS stream works for Xiaomi camera
  - Monitor memory for 10 minutes with 1 Xiaomi camera connected
  - Take screenshots of Web UI showing Xiaomi device discovery

  **Must NOT do**:
  - Don't test with more than 1 Xiaomi camera simultaneously (memory budget)
  - Don't leave debug logging enabled after testing

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: [`agent-browser`]

  **Parallelization**:
  - **Can Run In Parallel**: NO (needs RPi exclusively)
  - **Parallel Group**: Wave 4 (after Task 14)
  - **Blocks**: F1-F4
  - **Blocked By**: Task 14 (needs deployed binary)

  **References**:
  **Pattern References**:
  - `deploy/` — systemd service configuration
  - NVR at `http://192.168.63.31:9090`
  - SSH: `mickey@192.168.63.31`

  **Acceptance Criteria**:
  - [ ] Xiaomi auth succeeds with real account
  - [ ] Device list shows cameras from account
  - [ ] Camera added to NVR via UI or API
  - [ ] Recording starts, segment files created in `/mnt/data/nvr/`
  - [ ] Memory stable ≤100MB for 10 minutes
  - [ ] Screenshots captured

  **QA Scenarios**:
  ```
  Scenario: Full Xiaomi integration on RPi
    Tool: Bash + Browser
    Steps:
      1. SSH: verify service running
      2. curl: test auth endpoint with credentials
      3. curl: verify device list returns cameras
      4. Browser: open NVR UI, navigate to cameras page
      5. Browser: test Xiaomi discovery panel
      6. Monitor memory for 10 minutes
    Expected Result: All steps pass
    Evidence: .sisyphus/evidence/task-15-integration/
  ```

  **Commit**: NO (verification only)
## Final Verification Wave

- [x] F1. **Plan Compliance Audit** — `oracle` — VERDICT: APPROVE (7/7 Must Have, 10/10 Must NOT Have, 15/15 tasks)

- [x] F2. **Code Quality Review** — `unspecified-high` — VERDICT: APPROVE (Build PASS, Vet PASS, 646 tests, MIT headers fixed)

- [x] F3. **Real Manual QA** — `unspecified-high` — VERDICT: APPROVE (RPi health 200, 63MB RSS, binary 20MB, Xiaomi API 200, existing cameras unaffected)

- [x] F4. **Scope Fidelity Check** — `deep` — VERDICT: APPROVE (15/15 compliant, scope CLEAN, no forbidden patterns)

---

## Commit Strategy

- **Wave 1**: `feat(plugin): define plugin interface and registration system` — internal/plugin/plugin.go
- **Wave 1**: `feat(xiaomi): port crypto module from go2rtc (MIT)` — plugins/xiaomi/crypto.go
- **Wave 1**: `feat(xiaomi): port CS2 P2P transport from go2rtc (MIT)` — plugins/xiaomi/cs2.go
- **Wave 1**: `feat(xiaomi): port MISS protocol client from go2rtc (MIT)` — plugins/xiaomi/miss.go
- **Wave 2**: `feat(xiaomi): implement XiaomiRecorder for NVR` — plugins/xiaomi/recorder.go
- **Wave 2**: `feat(api): add Xiaomi cloud auth and device discovery endpoints` — internal/api/handler.go
- **Wave 2**: `refactor(camera): use plugin registry for recorder creation` — internal/camera/manager.go
- **Wave 3**: `feat(ui): add Xiaomi device discovery to camera management` — web/src/routes/Cameras.svelte
- **Wave 4**: `test(e2e): add Playwright tests for Xiaomi flow` — e2e-tests/
- **Wave 4**: `docs: add plugin development and Xiaomi setup guides` — docs/

---

## Success Criteria

### Verification Commands
```bash
rtk make build                                                    # Expected: success, binary ≤23MB
rtk make cross                                                    # Expected: success, arm64 binary
rtk go test ./... -v                                              # Expected: all pass
cd web && rtk npm run build                                       # Expected: no errors
ssh mickey@192.168.63.31 "ps aux | grep mibee-nvr | grep -v grep" # Expected: RSS ≤100MB
curl -u admin:admin http://192.168.63.31:9090/api/xiaomi/devices  # Expected: 200 JSON
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All tests pass
- [ ] Binary size ≤23MB
- [ ] RPi memory ≤100MB with 1 Xiaomi camera
- [ ] Frontend i18n complete (en + zh)
- [ ] MIT license headers on go2rtc-derived code
- [ ] Documentation published
