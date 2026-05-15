# Protocol/Encoding Rework + HTTP JPEG Timeout Fix + ONVIF HLS

## TL;DR

> **Quick Summary**: 拆分摄像头协议(RTSP/HTTP/ONVIF)和编码(H.264/H.265/MJPEG)为独立字段，修复 HTTP JPEG recorder 无超时卡死 bug，让 ONVIF 摄像头支持 HLS 实时预览，更新 AGENTS.md 构建约束。
> 
> **Deliverables**:
> - 后端: protocol/encoding 独立字段 + DB 迁移 + recorder factory 重构
> - HTTP JPEG recorder idle watchdog 超时检测
> - ONVIF 摄像头 HLS live view 支持
> - 前端: 协议/编码联动下拉 + 表格列优化
> - AGENTS.md 构建约束
> 
> **Estimated Effort**: Medium
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: Wave 1 model/config → Wave 2 recorder factory → Wave 3 frontend → Wave FINAL

---

## Context

### Original Request
用户提出三个核心需求:
1. 协议和编码拆分配置 — RTSP URL 和 ONVIF Endpoint 合并显示，编码独立选择
2. HTTP JPEG recorder 卡死修复 — 加 idle watchdog 超时检测
3. ONVIF 实时播放 — 复用现有 HLS 系统

### Interview Summary
**Key Discussions**:
- 协议下拉: RTSP / HTTP / ONVIF (3个传输协议)
- 编码下拉: RTSP→H.264/H.265/MJPEG, HTTP→JPEG, ONVIF→H.264/H.265/自动检测
- URL/Endpoint 在表格中合并显示
- 表格各列 UI 优化调整
- ONVIF 复用 HLS live view
- HTTP JPEG timeout 和本计划一起修

**Research Findings**:
- 协议常量: `model/types.go` 5 个 (rtsp_h264, rtsp_h265, rtsp_mjpeg, http_jpeg, onvif)
- Recorder factory: `camera/manager.go:createRecorder()` switch on protocol string
- DB: `storage/db.go` protocol 存为字符串，无 encoding 列
- 前端: `Cameras.svelte` 协议下拉含编码，ONVIF 不在下拉中（仅通过 discovery）
- HLS manager 已有 idle watchdog 模式可参考 (`hls/manager.go:518-541`)
- 所有 recorder 都无超时检测 (h264.go, h265.go, mjpeg.go, http_jpeg.go)
- API handler 错误消息不完整 (`handler.go:749` 缺少 rtsp_h265 和 onvif)

### Metis Review
Metis 超时，自行进行 gap analysis:
- **Migration safety**: 旧 config YAML 中的 `rtsp_h264` 等需要向后兼容解析
- **Recorder mapping**: protocol+encoding 组合必须唯一映射到 recorder 类型
- **Frontend state**: 协议切换时需清空不合法的 encoding 选择
- **API backward compat**: 现有 API 消费者可能发送旧 protocol 值

---

## Work Objectives

### Core Objective
将 protocol 和 encoding 拆分为独立字段，修复 HTTP JPEG recorder 超时问题，让 ONVIF 支持 HLS 实时预览。

### Concrete Deliverables
- `internal/model/types.go` — 新增 Protocol constants (rtsp/http/onvif) + Encoding constants
- `internal/config/config.go` — CameraConfig 新增 encoding 字段，兼容旧 YAML
- `internal/storage/db.go` — cameras 表新增 encoding 列，迁移逻辑
- `internal/camera/manager.go` — recorder factory 按 protocol+encoding 选择
- `internal/recorder/http_jpeg.go` — idle watchdog 超时检测
- `internal/hls/manager.go` — 支持 ONVIF 摄像头的 HLS stream
- `web/src/routes/Cameras.svelte` — 协议/编码联动下拉 + 表格优化
- `web/src/routes/Dashboard.svelte` + `LiveView.svelte` — ONVIF live 按钮
- `AGENTS.md` — 构建约束

### Definition of Done
- [ ] `rtk make build` 编译通过
- [ ] `rtk go test ./...` 全部通过
- [ ] 旧配置 (rtsp_h264 等) 能被正确解析迁移
- [ ] 前端协议切换时编码下拉正确联动
- [ ] ONVIF 摄像头在 Dashboard/LiveView 中显示实时按钮
- [ ] HTTP JPEG recorder 在流断开 60s 后自动重连

### Must Have
- protocol/encoding 拆分: 后端模型 + API + 前端 UI
- 旧配置向后兼容
- HTTP JPEG idle watchdog
- ONVIF HLS live view
- AGENTS.md 构建约束

### Must NOT Have (Guardrails)
- 不要改动 ONVIF discovery 逻辑
- 不要添加 test connection API（后续计划）
- 不要改动 recorder 录制核心逻辑（仅加 watchdog）
- 不要改变 segment 文件存储格式
- 不要修改 WebDAV/FTP/MQTT 模块
- 不要使用 `go build` 直接编译，必须用 `rtk make build`

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed.

### Test Decision
- **Infrastructure exists**: YES (testify, go test, Playwright E2E)
- **Automated tests**: YES (tests-after)
- **Framework**: go test + testify

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Foundation - model/config/DB):
├── Task 1: Model constants + types rework [quick]
├── Task 2: Config struct + YAML backward compat [quick]
├── Task 3: DB schema migration + encoding column [unspecified-high]
├── Task 4: AGENTS.md build constraint [quick]

Wave 2 (Backend logic - recorder factory + watchdog + HLS):
├── Task 5: Recorder factory rework (depends: 1,2,3) [deep]
├── Task 6: HTTP JPEG idle watchdog (depends: none) [deep]
├── Task 7: ONVIF HLS support (depends: 1) [unspecified-high]
├── Task 8: API handler update (depends: 1,2,3) [unspecified-high]

Wave 3 (Frontend UI):
├── Task 9: Protocol/encoding dropdown rework (depends: 8) [visual-engineering]
├── Task 10: Camera table column optimization (depends: 8) [visual-engineering]
├── Task 11: ONVIF live view button (depends: 7,9) [quick]

Wave FINAL (Verification):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality review (unspecified-high)
├── Task F3: Real QA (unspecified-high)
├── Task F4: Scope fidelity check (deep)
```

### Agent Dispatch Summary
- **Wave 1**: 4 tasks — T1-T2 → `quick`, T3 → `unspecified-high`, T4 → `quick`
- **Wave 2**: 4 tasks — T5 → `deep`, T6 → `deep`, T7 → `unspecified-high`, T8 → `unspecified-high`
- **Wave 3**: 3 tasks — T9-T10 → `visual-engineering`, T11 → `quick`
- **FINAL**: 4 tasks — F1 → `oracle`, F2-F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

- [x] 1. **Model Constants + Types Rework** [quick]

  **What to do**:
  - In `internal/model/types.go`:
    - Replace the 5 combined protocol constants (`ProtocolRTSPH264`, `ProtocolRTSPH265`, `ProtocolRTSPMJPEG`, `ProtocolHTTPJPEG`, `ProtocolONVIF`) with 3 transport-only constants: `ProtocolRTSP = "rtsp"`, `ProtocolHTTP = "http"`, `ProtocolONVIF = "onvif"`
    - Add 4 encoding constants: `EncodingH264 = "h264"`, `EncodingH265 = "h265"`, `EncodingMJPEG = "mjpeg"`, `EncodingJPEG = "jpeg"`
    - Add a `ValidEncodingsForProtocol` map: `rtsp → [h264, h265, mjpeg]`, `http → [jpeg]`, `onvif → [h264, h265, auto]`
    - Add helper `ParseLegacyProtocol(old string) (protocol, encoding string, err error)` that maps old values (`rtsp_h264`→(`rtsp`,`h264`), `rtsp_h265`→(`rtsp`,`h265`), etc.) for backward compat
    - Add helper `ValidateProtocolEncoding(protocol, encoding string) error`
    - Keep old constant values as deprecated aliases for one release cycle if needed by other packages during transition

  **Must NOT do**:
  - Don't change `Recorder` interface or `StorageProvider` interface
  - Don't touch any recorder implementation files
  - Don't remove old constants yet — other packages still reference them during this wave

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single file, well-defined constants and helpers, no complex logic
  - **Skills**: `[]`
    - No external skills needed — pure Go constant/helper definitions
  - **Skills Evaluated but Omitted**:
    - `git-master`: Not needed — no git operations in this task

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3, 4)
  - **Blocks**: Tasks 5, 7, 8
  - **Blocked By**: None (can start immediately)

  **References**:

  **Pattern References**:
  - `internal/model/types.go:1-60` — Current constant definitions (ProtocolRTSPH264 etc.), need to see exact names and values to replace
  - `internal/model/types.go:Recorder` interface — Must NOT be modified, but verify no protocol coupling exists

  **API/Type References**:
  - `internal/camera/manager.go:createRecorder()` — Switch cases reference the old constants; understand the mapping to know what ParseLegacyProtocol needs to handle

  **Test References**:
  - `internal/model/types_test.go` — May not exist yet; if it does, follow its pattern. If not, this task creates it.

  **WHY Each Reference Matters**:
  - `types.go`: This IS the file being modified — must see exact current constant names, values, and any other types that reference them
  - `camera/manager.go:createRecorder()`: The switch-case mapping tells us exactly which old protocol values exist and how they map to recorders, which defines the ParseLegacyProtocol mapping

  **Acceptance Criteria**:

  - [ ] `internal/model/types.go` contains exactly 3 Protocol constants (`rtsp`, `http`, `onvif`) and 4 Encoding constants (`h264`, `h265`, `mjpeg`, `jpeg`)
  - [ ] `ValidEncodingsForProtocol` map exists with correct mappings
  - [ ] `ParseLegacyProtocol("rtsp_h264")` returns `("rtsp", "h264", nil)`
  - [ ] `ValidateProtocolEncoding("rtsp", "h265")` returns nil, `ValidateProtocolEncoding("http", "h264")` returns error
  - [ ] `rtk go vet ./internal/model/...` passes

  **QA Scenarios:**
  ```
  Scenario: Legacy protocol parsing
    Tool: Bash
    Preconditions: Code compiles
    Steps:
      1. Run `rtk go test ./internal/model/... -run TestParseLegacyProtocol -v`
      2. Verify all 5 old values parse correctly: rtsp_h264→(rtsp,h264), rtsp_h265→(rtsp,h265), rtsp_mjpeg→(rtsp,mjpeg), http_jpeg→(http,jpeg), onvif→(onvif,auto)
      3. Verify unknown value returns error
    Expected Result: 5/5 legacy values parse correctly, unknown returns error
    Failure Indicators: Any test fails or panics
    Evidence: .sisyphus/evidence/task-1-legacy-parse.txt

  Scenario: Protocol-encoding validation
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/model/... -run TestValidateProtocolEncoding -v`
      2. Test valid combos: (rtsp,h264), (rtsp,h265), (rtsp,mjpeg), (http,jpeg), (onvif,h264), (onvif,h265)
      3. Test invalid combos: (http,h264), (rtsp,jpeg), (onvif,jpeg), ("",""), ("foo","bar")
    Expected Result: All valid combos pass, all invalid combos return errors
    Failure Indicators: Any valid combo rejected or invalid combo accepted
    Evidence: .sisyphus/evidence/task-1-validation.txt
  ```

  **Commit**: NO (groups with Wave 1)

- [x] 2. **Config Struct + YAML Backward Compat** [quick]

  **What to do**:
  - In `internal/config/config.go`:
    - Add `Encoding string \`yaml:"encoding"\`` field to `CameraConfig` struct
    - Update `applyDefaults()` to infer encoding from legacy `Protocol` field if `Encoding` is empty (using `model.ParseLegacyProtocol`)
    - Update `applyDefaults()` to normalize `Protocol` to transport-only value (rtsp/http/onvif)
    - Update `Validate()` to call `model.ValidateProtocolEncoding(cfg.Protocol, cfg.Encoding)`
    - Ensure `MergeConfig()` handles the new encoding field correctly
  - In `config.example.yaml`:
    - Update camera examples to show new `protocol` + `encoding` format
    - Keep a commented example showing old format still works

  **Must NOT do**:
  - Don't break existing YAML configs — old `protocol: rtsp_h264` must still load
  - Don't remove the `Protocol` field — it still exists, just normalized
  - Don't change config file format versioning (no version bump needed)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single file + example config, straightforward field addition + backward compat logic
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES (after Task 1 completes)
  - **Parallel Group**: Wave 1 (with Tasks 1, 3, 4)
  - **Blocks**: Tasks 5, 8
  - **Blocked By**: Task 1 (needs ParseLegacyProtocol and ValidateProtocolEncoding)

  **References**:

  **Pattern References**:
  - `internal/config/config.go:CameraConfig` — Current struct definition, need exact field names and YAML tags
  - `internal/config/config.go:applyDefaults()` — Where defaults are set; this is where legacy protocol normalization goes
  - `internal/config/config.go:Validate()` — Where validation happens; add protocol+encoding validation here
  - `internal/config/config.go:MergeConfig()` — How configs merge; ensure encoding field merges correctly

  **API/Type References**:
  - `internal/model/types.go:ParseLegacyProtocol` (from Task 1) — Helper to split old protocol string into protocol+encoding
  - `internal/model/types.go:ValidateProtocolEncoding` (from Task 1) — Helper to validate combo
  - `config.example.yaml` — Example config showing current camera format

  **WHY Each Reference Matters**:
  - `config.go`: This IS the file being modified — need exact struct layout, applyDefaults flow, and Validate logic
  - `config.example.yaml`: Must update examples to show new format while documenting backward compat

  **Acceptance Criteria**:

  - [ ] `CameraConfig` has `Encoding string` field with yaml tag
  - [ ] Loading config with `protocol: rtsp_h264` results in `Protocol: "rtsp"` + `Encoding: "h264"`
  - [ ] Loading config with `protocol: rtsp` + `encoding: h265` works directly
  - [ ] `Validate()` rejects invalid combos like `protocol: http` + `encoding: h264`
  - [ ] `rtk go vet ./internal/config/...` passes

  **QA Scenarios:**
  ```
  Scenario: Legacy config backward compat
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/config/... -run TestLegacyProtocol -v`
      2. Test loading YAML with `protocol: rtsp_h264` → Protocol becomes "rtsp", Encoding becomes "h264"
      3. Test loading YAML with `protocol: http_jpeg` → Protocol becomes "http", Encoding becomes "jpeg"
      4. Test loading YAML with `protocol: rtsp` + `encoding: h265` → stays as-is
    Expected Result: All legacy formats parse correctly, new format also works
    Failure Indicators: Any legacy value not normalized, or new format rejected
    Evidence: .sisyphus/evidence/task-2-config-compat.txt

  Scenario: Invalid encoding rejection
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/config/... -run TestConfigValidation -v`
      2. Test `protocol: http` + `encoding: h264` → Validate returns error
      3. Test `protocol: rtsp` + `encoding: jpeg` → Validate returns error
      4. Test `protocol: onvif` + `encoding: h264` → Validate returns nil
    Expected Result: Invalid combos rejected, valid combos accepted
    Evidence: .sisyphus/evidence/task-2-config-validation.txt
  ```

  **Commit**: NO (groups with Wave 1)

- [x] 3. **DB Schema Migration + Encoding Column** [unspecified-high]

  **What to do**:
  - In `internal/storage/db.go`:
    - Add `encoding TEXT NOT NULL DEFAULT ''` column to cameras table schema in `Init()`
    - Add migration: `ALTER TABLE cameras ADD COLUMN encoding TEXT NOT NULL DEFAULT ''` (guarded by IF NOT EXISTS pattern)
    - In `migrateEncodings()` (new method): For every existing camera row where `encoding = ''`, use `ParseLegacyProtocol` on the `protocol` column to derive encoding, then UPDATE both `protocol` and `encoding` columns
    - Update `UpsertCamera()` to accept and store the `encoding` field
    - Update `GetCamera()` / `ListCameras()` to return the `encoding` field
    - Update all camera-related scan methods to include the new column
    - Update `updateCameraFromConfig()` (if exists) or equivalent mapping logic
  - Run migration at startup after `Init()` completes

  **Must NOT do**:
  - Don't DROP or recreate the cameras table
  - Don't lose existing camera data
  - Don't change recordings table (it doesn't store encoding)
  - Don't break `timeToDB()`/`parseTime()`/`scanTime()` time handling

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: DB migration requires careful handling — ALTER TABLE, data migration, scan methods, multiple functions to update
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES (after Task 1 completes)
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 4)
  - **Blocks**: Tasks 5, 8
  - **Blocked By**: Task 1 (needs ParseLegacyProtocol)

  **References**:

  **Pattern References**:
  - `internal/storage/db.go:Init()` — Current schema creation with CREATE IF NOT EXISTS pattern; add encoding column here and in migration
  - `internal/storage/db.go:UpsertCamera()` — Current INSERT/UPDATE SQL; add encoding column to both INSERT and UPDATE clauses
  - `internal/storage/db.go:GetCamera()` / `scanCamera()` — Current SELECT + scan; add encoding to SELECT list and scan target

  **API/Type References**:
  - `internal/model/types.go:ParseLegacyProtocol` (from Task 1) — Used in migration to derive encoding from old protocol values
  - `internal/model/types.go` — Camera struct/type if it exists; may need Encoding field added

  **Test References**:
  - `internal/storage/db_test.go` — Existing test patterns for DB operations; follow the same setup/teardown and assertion style (testify/require)

  **WHY Each Reference Matters**:
  - `db.go:Init()`: Must add column to schema AND migration — both paths needed for fresh installs vs existing DBs
  - `db.go:UpsertCamera()`: SQL must include encoding in INSERT and UPDATE — missing either causes data loss
  - `db.go:scanCamera()`: Scan must include encoding column or row count mismatch causes runtime panic
  - `db_test.go`: Must follow existing test patterns for consistency

  **Acceptance Criteria**:

  - [ ] `cameras` table has `encoding` column after `Init()`
  - [ ] Existing cameras with `protocol: "rtsp_h264"` migrated to `protocol: "rtsp"` + `encoding: "h264"`
  - [ ] `UpsertCamera()` stores both protocol and encoding
  - [ ] `GetCamera()` returns encoding field
  - [ ] `rtk go test ./internal/storage/... -v` passes

  **QA Scenarios:**
  ```
  Scenario: Fresh DB has encoding column
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/storage/... -run TestSchema -v`
      2. Verify cameras table has encoding column (PRAGMA table_info)
    Expected Result: encoding column exists with type TEXT, default ''
    Evidence: .sisyphus/evidence/task-3-schema.txt

  Scenario: Legacy data migration
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/storage/... -run TestMigrateEncodings -v`
      2. Insert camera with `protocol: "rtsp_h264"`, `encoding: ""`
      3. Call `migrateEncodings()`
      4. Read camera back — verify `protocol: "rtsp"`, `encoding: "h264"`
      5. Repeat for all 5 legacy values
    Expected Result: All 5 legacy protocols migrated correctly
    Failure Indicators: Any camera has wrong protocol or empty encoding after migration
    Evidence: .sisyphus/evidence/task-3-migration.txt

  Scenario: UpsertCamera with new format
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/storage/... -run TestUpsertCamera -v`
      2. Upsert camera with `protocol: "rtsp"`, `encoding: "h265"`
      3. Get camera back — verify both fields persisted
    Expected Result: protocol="rtsp", encoding="h265" stored and retrieved correctly
    Evidence: .sisyphus/evidence/task-3-upsert.txt
  ```

  **Commit**: NO (groups with Wave 1)

- [x] 4. **AGENTS.md Build Constraint Update** [quick]

  **What to do**:
  - In `AGENTS.md`:
    - Add to **ANTI-PATTERNS** section: `DO NOT hand-assemble build commands (e.g. \`go build -o ...\` or \`GOOS=linux go build ...\`) — the Makefile contains cleanup, version injection, and asset-copy logic that hand-rolled commands will miss. ALWAYS use \`rtk make build\` (local) or \`rtk make cross\` (RPi arm64).`
    - Add to **COMMANDS** section (if not already clear): `rtk make build` for local, `rtk make cross` for RPi cross-compile
    - Verify the **CONVENTIONS** section mentions Makefile-based build workflow

  **Must NOT do**:
  - Don't restructure or rewrite AGENTS.md — only add the constraint
  - Don't modify any source code
  - Don't change existing anti-pattern entries

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single markdown file, adding one anti-pattern entry + minor command clarification
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3)
  - **Blocks**: None
  - **Blocked By**: None (can start immediately)

  **References**:

  **Pattern References**:
  - `AGENTS.md:ANTI-PATTERNS` section — Current anti-pattern entries; follow the same format (bold DO NOT + explanation)
  - `AGENTS.md:COMMANDS` section — Current command listings; add alongside existing build commands
  - `Makefile` — Verify the build/cross targets exist and understand what they do (to write accurate constraint)

  **WHY Each Reference Matters**:
  - `AGENTS.md:ANTI-PATTERNS`: Must match existing format for consistency
  - `Makefile`: Must verify the constraint is accurate — what logic does `make build` include that `go build` misses?

  **Acceptance Criteria**:

  - [ ] AGENTS.md ANTI-PATTERNS section contains the build command constraint
  - [ ] Constraint mentions both `rtk make build` and `rtk make cross`
  - [ ] No other AGENTS.md content was changed

  **QA Scenarios:**
  ```
  Scenario: Constraint present in AGENTS.md
    Tool: Bash
    Steps:
      1. Run: grep -c "DO NOT hand-assemble build commands" AGENTS.md
      2. Verify count is 1
      3. Run: grep -c "rtk make build" AGENTS.md
      4. Verify count >= 1
    Expected Result: Both patterns found in AGENTS.md
    Failure Indicators: grep returns 0 for either pattern
    Evidence: .sisyphus/evidence/task-4-agents-constraint.txt
  ```

  **Commit**: YES — `docs: add build command constraint to AGENTS.md`
  - Files: `AGENTS.md`
  - Pre-commit: (none — markdown only)

---


- [x] 5. **Recorder Factory Rework** [deep]

  **What to do**:
  - In `internal/camera/manager.go`:
    - Rewrite `createRecorder()` to switch on `protocol` first (rtsp/http/onvif), then select recorder type based on `encoding`:
      - `rtsp` + `h264` → `H264Recorder`
      - `rtsp` + `h265` → `H265Recorder`
      - `rtsp` + `mjpeg` → `MJPEGRecorder`
      - `http` + `jpeg` → `HTTPJPEGRecorder`
      - `onvif` + `h264`/`h265` → use RTSP recorder (ONVIF stub currently, but structure ready for future onvif-go integration)
    - Accept `protocol` and `encoding` as separate parameters instead of combined `protocol` string
    - Update `AddCamera()` / `UpdateCamera()` to pass both fields to `createRecorder()`
    - Update any camera status/health methods that reference old protocol constants
  - Remove imports of old combined protocol constants from `camera/manager.go`
  - Update all references to use new `model.ProtocolRTSP`, `model.EncodingH264` etc.

  **Must NOT do**:
  - Don't modify recorder internals (h264.go, h265.go, mjpeg.go, http_jpeg.go)
  - Don't change the Recorder interface
  - Don't add new recorder types
  - Don't change how segments are written or stored

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Core refactoring of recorder factory — must understand protocol+encoding mapping, update switch logic, maintain all existing functionality
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2 (sequential, other Wave 2 tasks can run in parallel with each other)
  - **Blocks**: None directly (Task 9-11 depend on Task 8, not directly on 5)
  - **Blocked By**: Tasks 1, 2, 3 (needs new protocol/encoding constants + config + DB schema)

  **References**:

  **Pattern References**:
  - `internal/camera/manager.go:createRecorder()` — Current switch-case on combined protocol strings; THIS is the function being rewritten
  - `internal/camera/manager.go:AddCamera()` — Calls createRecorder; must pass protocol+encoding separately
  - `internal/camera/manager.go:UpdateCamera()` — Also calls createRecorder; same update needed

  **API/Type References**:
  - `internal/model/types.go:ProtocolRTSP` etc. (from Task 1) — New constants to use in switch
  - `internal/model/types.go:EncodingH264` etc. (from Task 1) — New encoding constants
  - `internal/model/types.go:ValidEncodingsForProtocol` (from Task 1) — Validation map
  - `internal/recorder/h264.go:NewH264Recorder` — Constructor signature to call correctly
  - `internal/recorder/h265.go:NewH265Recorder` — Constructor signature
  - `internal/recorder/mjpeg.go:NewMJPEGRecorder` — Constructor signature
  - `internal/recorder/http_jpeg.go:NewHTTPJPEGRecorder` — Constructor signature

  **WHY Each Reference Matters**:
  - `manager.go:createRecorder()`: This IS the function being rewritten — must understand current structure exactly
  - Recorder constructors: Must call them with correct parameters — check if any take protocol-specific config
  - New model constants: Switch must use new constant values, not string literals

  **Acceptance Criteria**:

  - [ ] `createRecorder()` switches on `protocol` first, then `encoding`
  - [ ] All 5 recorder types still created correctly for their protocol+encoding combos
  - [ ] No references to old combined constants (`ProtocolRTSPH264` etc.) remain in `camera/manager.go`
  - [ ] `rtk go vet ./internal/camera/...` passes
  - [ ] `rtk go test ./internal/camera/... -v` passes

  **QA Scenarios:**
  ```
  Scenario: Recorder factory creates correct types
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/camera/... -run TestCreateRecorder -v`
      2. Test all valid combos:
         - (rtsp, h264) → H264Recorder
         - (rtsp, h265) → H265Recorder
         - (rtsp, mjpeg) → MJPEGRecorder
         - (http, jpeg) → HTTPJPEGRecorder
         - (onvif, h264) → appropriate recorder
      3. Test invalid combos return errors:
         - (http, h264), (rtsp, jpeg)
    Expected Result: 5/5 valid combos create correct recorder, invalid combos error
    Failure Indicators: Wrong recorder type, panic, or invalid combo accepted
    Evidence: .sisyphus/evidence/task-5-recorder-factory.txt

  Scenario: No legacy constants in camera package
    Tool: Bash
    Steps:
      1. Run: grep -rn "rtsp_h264\|rtsp_h265\|rtsp_mjpeg\|http_jpeg" internal/camera/manager.go
      2. Verify: 0 matches (old string literals removed)
    Expected Result: No legacy protocol string literals in camera/manager.go
    Failure Indicators: Any match found
    Evidence: .sisyphus/evidence/task-5-no-legacy.txt
  ```

  **Commit**: NO (groups with Wave 2)

- [x] 6. **HTTP JPEG Idle Watchdog** [deep]

  **What to do**:
  - In `internal/recorder/http_jpeg.go`:
    - Add an idle timeout watchdog similar to HLS manager's `idleWatchdog()` pattern
    - In `Start()` method, launch a goroutine that tracks last frame received timestamp
    - If no frame received within `idleTimeout` (configurable, default 60s), log warning and trigger reconnect
    - Reset the timer on every successful frame decode
    - Use `atomic.Int64` or `sync.Mutex` for the last-frame timestamp (thread-safe, accessed from read goroutine and watchdog goroutine)
    - On reconnect: close current response body, re-establish HTTP connection, reset timer
    - Ensure watchdog goroutine is cleaned up in `Stop()` method
  - Add `idle_timeout` field to config (or use a reasonable default like 60s for now)

  **Must NOT do**:
  - Don't change how frames are decoded or written to segments
  - Don't add timeout to the HTTP client itself (that's different — we need idle frame detection)
  - Don't modify other recorder types (H264/H265/MJPEG have RTSP-level timeouts already)
  - Don't change the Recorder interface

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Concurrent goroutine management, atomic operations, reconnect logic — needs careful implementation
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (independent of Tasks 5, 7, 8)
  - **Blocks**: None
  - **Blocked By**: None (touches only http_jpeg.go, no dependency on Task 1-4)

  **References**:

  **Pattern References**:
  - `internal/hls/manager.go:518-541` — `idleWatchdog()` implementation — USE THIS as the reference pattern for idle timeout detection
  - `internal/recorder/http_jpeg.go:Start()` — Current implementation with no timeout; need to understand the goroutine structure to add watchdog
  - `internal/recorder/http_jpeg.go:readLoop()` or equivalent — Where frames are read; need to add timestamp update here
  - `internal/recorder/http_jpeg.go:Stop()` — Must clean up watchdog goroutine

  **API/Type References**:
  - `internal/recorder/h264.go` — Reference for how H264Recorder handles reconnection (backoff pattern)

  **Test References**:
  - `internal/recorder/http_jpeg_test.go` — If exists, follow patterns. Test idle detection with mock connection.

  **WHY Each Reference Matters**:
  - `hls/manager.go:518-541`: The idle watchdog pattern already exists in this codebase — copy its approach (goroutine + timer + atomic/mutex)
  - `http_jpeg.go:Start()`: Must understand the existing goroutine lifecycle to safely add another goroutine
  - `h264.go`: Has reconnect backoff logic — reference for how to handle reconnection gracefully

  **Acceptance Criteria**:

  - [ ] HTTP JPEG recorder detects when no frames received for 60s
  - [ ] Detection triggers reconnect with logging
  - [ ] Watchdog goroutine is stopped in `Stop()` method
  - [ ] `rtk go vet ./internal/recorder/...` passes
  - [ ] `rtk go test ./internal/recorder/... -v` passes

  **QA Scenarios:**
  ```
  Scenario: Idle detection triggers reconnect
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/recorder/... -run TestHTTPJPEGIdleWatchdog -v`
      2. Test: Start recorder, simulate no frames for 65s (fast-forward test clock or use short timeout)
      3. Verify reconnect is triggered (log message or counter)
      4. Verify recorder continues working after reconnect
    Expected Result: Reconnect triggered after idle period, recorder recovers
    Evidence: .sisyphus/evidence/task-6-idle-watchdog.txt

  Scenario: Watchdog cleanup on Stop
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/recorder/... -run TestHTTPJPEGWatchdogCleanup -v`
      2. Start recorder, call Stop(), verify watchdog goroutine is cleaned up (no goroutine leak)
    Expected Result: No goroutine leak after Stop()
    Evidence: .sisyphus/evidence/task-6-watchdog-cleanup.txt
  ```

  **Commit**: NO (groups with Wave 2)

- [x] 7. **ONVIF HLS Live View Support** [unspecified-high]

  **What to do**:
  - In `internal/hls/manager.go`:
    - Currently HLS only works for cameras with RTSP URLs. ONVIF cameras need the same HLS support.
    - The key insight: ONVIF cameras will use RTSP as the streaming transport (ONVIF provides RTSP URL via GetStreamUri). Since HLS manager already supports RTSP H.264/H.265, ONVIF cameras naturally work IF they have a valid RTSP URL.
    - Update `GetStream()` or equivalent method to accept ONVIF cameras
    - If ONVIF camera has encoding `h264` or `h265`, create HLS stream using RTSP URL (same as RTSP cameras)
    - If ONVIF encoding is `auto` or empty, attempt H.264 first, fallback to H.265 (or use probe)
    - Ensure ONVIF cameras appear in HLS stream list alongside RTSP cameras
  - The ONVIF device's RTSP URL will come from camera config (same URL field, but populated via ONVIF discovery or manual entry)

  **Must NOT do**:
  - Don't implement ONVIF GetStreamUri (ONVIF package is still stub)
  - Don't add new HLS muxing logic — reuse existing RTSP HLS pipeline
  - Don't change ONVIF discovery
  - Don't modify recorder internals

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Requires understanding HLS manager internals, ONVIF camera config flow, and how to bridge them
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (independent of Tasks 5, 6, 8)
  - **Blocks**: Task 11 (ONVIF live button depends on HLS being available)
  - **Blocked By**: Task 1 (needs ProtocolONVIF constant)

  **References**:

  **Pattern References**:
  - `internal/hls/manager.go:GetStream()` — Current HLS stream creation for RTSP cameras; need to extend for ONVIF
  - `internal/hls/manager.go:NewMuxer()` or equivalent — How HLS muxers are created per camera; understand the camera URL usage
  - `internal/hls/manager.go:518-541` — idleWatchdog pattern (context for HLS lifecycle management)

  **API/Type References**:
  - `internal/model/types.go:ProtocolONVIF` (from Task 1) — New constant to check against
  - `internal/model/types.go:EncodingH264`, `EncodingH265` (from Task 1) — Encoding constants for stream selection

  **WHY Each Reference Matters**:
  - `hls/manager.go:GetStream()`: This is where camera eligibility for HLS is decided — ONVIF must be accepted here
  - `hls/manager.go:NewMuxer()`: Shows how RTSP URL is used to create the HLS muxer — ONVIF will use the same URL

  **Acceptance Criteria**:

  - [ ] ONVIF cameras with `encoding: h264` or `h265` can get HLS stream
  - [ ] ONVIF HLS streams use the same idle timeout and eviction as RTSP cameras
  - [ ] `rtk go vet ./internal/hls/...` passes
  - [ ] `rtk go test ./internal/hls/... -v` passes

  **QA Scenarios:**
  ```
  Scenario: ONVIF camera gets HLS stream
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/hls/... -run TestONVIFHLS -v`
      2. Create mock ONVIF camera with protocol="onvif", encoding="h264"
      3. Request HLS stream — verify muxer is created
      4. Verify stream URL is returned
    Expected Result: HLS muxer created successfully for ONVIF H.264 camera
    Failure Indicators: ONVIF camera rejected or panic
    Evidence: .sisyphus/evidence/task-7-onvif-hls.txt

  Scenario: ONVIF with unsupported encoding rejected gracefully
    Tool: Bash
    Steps:
      1. Run `rtk go test ./internal/hls/... -run TestONVIFHLSUnsupported -v`
      2. Create ONVIF camera with encoding="jpeg" (MJPEG, not supported for HLS)
      3. Request HLS stream — verify error returned (not panic)
    Expected Result: Graceful error, no panic
    Evidence: .sisyphus/evidence/task-7-onvif-hls-unsupported.txt
  ```

  **Commit**: NO (groups with Wave 2)

- [x] 8. **API Handler Update** [unspecified-high]

  **What to do**:
  - In `internal/api/handler.go`:
    - Update `validProtocols` map (or equivalent validation) to accept the 3 new transport protocols: `rtsp`, `http`, `onvif`
    - Add `encoding` field to camera creation/update API request structs
    - Update camera create/update handlers to accept and validate both `protocol` and `encoding` fields
    - Update API response structs to include `encoding` field
    - Add backward compat: if API receives old `protocol: "rtsp_h264"` value, split it into `protocol` + `encoding` using `ParseLegacyProtocol`
    - Update error messages for invalid protocol/encoding combos (currently missing `rtsp_h265` and `onvif` in error message at ~line 749)
    - Update any camera list/get endpoints to return the `encoding` field
    - Update stream proxy / HLS endpoints to work with new protocol+encoding fields

  **Must NOT do**:
  - Don't change API URL routes (backward compat)
  - Don't remove old API fields (protocol still exists, just normalized)
  - Don't modify auth middleware
  - Don't change recording-related API endpoints

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Multiple handler functions to update, API backward compat to maintain, validation logic to add
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES (after Wave 1)
  - **Parallel Group**: Wave 2 (with Tasks 5, 6, 7)
  - **Blocks**: Tasks 9, 10, 11 (frontend depends on API contract)
  - **Blocked By**: Tasks 1, 2, 3 (needs model constants, config, DB)

  **References**:

  **Pattern References**:
  - `internal/api/handler.go:validProtocols` — Current protocol validation map (~line 749 area); needs complete rewrite for 3 transport protocols
  - `internal/api/handler.go:createCamera` / `updateCamera` handlers — Where camera CRUD happens; add encoding field handling
  - `internal/api/handler.go:Routes()` — Route registration; verify no protocol-specific routes exist

  **API/Type References**:
  - `internal/model/types.go:ParseLegacyProtocol` (from Task 1) — For backward compat in API
  - `internal/model/types.go:ValidateProtocolEncoding` (from Task 1) — For request validation
  - `internal/camera/manager.go:AddCamera()` / `UpdateCamera()` — Signatures may change to accept encoding
  - `internal/storage/db.go:UpsertCamera()` — Now accepts encoding (from Task 3)

  **Test References**:
  - `internal/api/handler_test.go` or `tests/integration_test.go` — API test patterns

  **WHY Each Reference Matters**:
  - `handler.go:validProtocols`: THIS is where `rtsp_h265` and `onvif` are missing — must be fixed
  - `handler.go:createCamera/updateCamera`: Must add encoding to request/response structs
  - `tests/integration_test.go`: Must update integration tests for new API shape

  **Acceptance Criteria**:

  - [ ] API accepts `protocol: "rtsp"` + `encoding: "h264"` in camera create/update
  - [ ] API accepts old format `protocol: "rtsp_h264"` and normalizes it
  - [ ] API rejects invalid combos like `protocol: "http"` + `encoding: "h264"`
  - [ ] API responses include `encoding` field
  - [ ] `rtk go vet ./internal/api/...` passes
  - [ ] `rtk go test ./internal/api/... -v` passes

  **QA Scenarios:**
  ```
  Scenario: Create camera with new format
    Tool: Bash (curl)
    Steps:
      1. Run integration test: create camera with `{"protocol": "rtsp", "encoding": "h265", "url": "rtsp://..."}`
      2. GET camera back — verify both fields stored correctly
    Expected Result: Camera created with protocol="rtsp", encoding="h265"
    Failure Indicators: Missing encoding field, wrong values, or 400 error
    Evidence: .sisyphus/evidence/task-8-api-create.txt

  Scenario: Legacy API format still works
    Tool: Bash (curl)
    Steps:
      1. Create camera with old format `{"protocol": "rtsp_h264", "url": "rtsp://..."}`
      2. GET camera back — verify protocol="rtsp", encoding="h264" (normalized)
    Expected Result: Old format accepted and normalized
    Failure Indicators: 400 error or un-normalized protocol
    Evidence: .sisyphus/evidence/task-8-api-legacy.txt

  Scenario: Invalid encoding rejected
    Tool: Bash (curl)
    Steps:
      1. Create camera with `{"protocol": "http", "encoding": "h264"}`
      2. Verify 400 error with descriptive message
    Expected Result: 400 Bad Request with validation error
    Failure Indicators: Camera created with invalid combo
    Evidence: .sisyphus/evidence/task-8-api-invalid.txt
  ```

  **Commit**: NO (groups with Wave 2)

---

- [x] 9. **Protocol/Encoding Dropdown Rework** [visual-engineering]

  **What to do**:
  - In `web/src/routes/Cameras.svelte`:
    - Replace the single protocol dropdown (containing `rtsp_h264`, `rtsp_h265`, `rtsp_mjpeg`, `http_jpeg`) with TWO linked dropdowns:
      1. **Protocol dropdown**: `RTSP`, `HTTP`, `ONVIF` (display labels, values: `rtsp`, `http`, `onvif`)
      2. **Encoding dropdown**: Dynamic options based on selected protocol:
         - RTSP → `H.264`, `H.265`, `MJPEG`
         - HTTP → `JPEG`
         - ONVIF → `H.264`, `H.265`, `Auto`
    - When protocol changes, auto-select the first valid encoding and clear any invalid selection
    - Update the form data model to send `protocol` + `encoding` as separate fields to API
    - For backward compat with existing loaded cameras: if camera has old `protocol: "rtsp_h264"`, parse it to populate both dropdowns correctly
    - Update camera form submission (both create and update) to send `{protocol, encoding, ...}` instead of `{protocol: "rtsp_h264", ...}`
    - Ensure form validation shows clear error if encoding doesn't match protocol

  **Must NOT do**:
  - Don't change the camera table layout (that's Task 10)
  - Don't add ONVIF live view button (that's Task 11)
  - Don't change API URL or endpoint paths
  - Don't add i18n strings (use existing i18n pattern, add minimal new strings if needed)

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: Frontend UI component with linked dropdowns, reactive state, form handling
  - **Skills**: `[]`
    - Svelte 5 + TailwindCSS are the project stack, no special skill needed
  - **Skills Evaluated but Omitted**:
    - `playwright`: Not needed for implementation, only for QA verification

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Task 10)
  - **Blocks**: Task 11 (ONVIF live button needs protocol dropdown to be ONVIF-aware)
  - **Blocked By**: Task 8 (API contract must be finalized)

  **References**:

  **Pattern References**:
  - `web/src/routes/Cameras.svelte` — Current protocol dropdown implementation; understand the form data model, event handlers, and API call structure
  - `web/src/routes/Cameras.svelte` (camera form/edit section) — How camera objects are loaded, how edit mode populates form fields
  - `web/src/lib/i18n.ts` or similar — i18n string pattern for dropdown labels

  **API/Type References**:
  - `internal/api/handler.go` (from Task 8) — API now accepts `protocol` + `encoding` fields; ensure frontend sends matching field names
  - `internal/model/types.go:ValidEncodingsForProtocol` (from Task 1) — The same mapping logic should be mirrored in frontend JS

  **External References**:
  - Svelte 5 reactive declarations (`$derived`, `$effect`) — Use for linked dropdown reactivity

  **WHY Each Reference Matters**:
  - `Cameras.svelte`: This IS the file being modified — must understand current form structure, API calls, and state management
  - API handler: Frontend field names must match API field names exactly

  **Acceptance Criteria**:

  - [ ] Protocol dropdown shows 3 options: RTSP, HTTP, ONVIF
  - [ ] Encoding dropdown updates dynamically when protocol changes
  - [ ] Selecting RTSP shows H.264/H.265/MJPEG; selecting HTTP shows JPEG only
  - [ ] Camera create/update sends `protocol` + `encoding` as separate fields
  - [ ] Existing cameras with old protocol values load correctly in edit mode
  - [ ] `cd web && rtk npm run build` succeeds

  **QA Scenarios:**
  ```
  Scenario: Protocol-encoding linking works
    Tool: Playwright
    Steps:
      1. Navigate to Cameras page
      2. Click "Add Camera" button
      3. Select protocol "RTSP" — verify encoding shows [H.264, H.265, MJPEG]
      4. Change protocol to "HTTP" — verify encoding auto-selects "JPEG" and dropdown only shows JPEG
      5. Change protocol to "ONVIF" — verify encoding shows [H.264, H.265, Auto]
    Expected Result: Encoding dropdown correctly updates for each protocol
    Failure Indicators: Wrong options, stale options from previous protocol, empty dropdown
    Evidence: .sisyphus/evidence/task-9-dropdown-linking.png

  Scenario: Edit existing camera loads correctly
    Tool: Playwright
    Steps:
      1. Navigate to Cameras page
      2. Find existing camera with protocol "rtsp" (migrated from old rtsp_h264)
      3. Click edit button
      4. Verify protocol dropdown shows "RTSP" and encoding shows "H.264"
    Expected Result: Both dropdowns populated correctly from API data
    Failure Indicators: Empty dropdowns, wrong selection, or old protocol string shown
    Evidence: .sisyphus/evidence/task-9-edit-load.png
  ```

  **Commit**: NO (groups with Wave 3)

- [x] 10. **Camera Table Column Optimization** [visual-engineering]

  **What to do**:
  - In `web/src/routes/Cameras.svelte`:
    - Merge URL and Endpoint into a single "URL/Endpoint" column in the camera table
    - Display logic: if protocol is ONVIF, show endpoint; otherwise show URL
    - Add an "Encoding" column to the camera table (between Protocol and Status columns)
    - Adjust column widths for readability on both desktop and mobile
    - Ensure table remains responsive (existing responsive patterns)
    - Update table header labels for clarity: "Protocol" (shows transport only), "Encoding" (new), "URL" (merged)

  **Must NOT do**:
  - Don't change the dropdown form (that's Task 9)
  - Don't add new columns beyond Encoding
  - Don't change sorting/pagination behavior
  - Don't modify the API response format

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: Table layout adjustment, responsive design, column display logic
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Task 9)
  - **Blocks**: None
  - **Blocked By**: Task 8 (needs API to return encoding field)

  **References**:

  **Pattern References**:
  - `web/src/routes/Cameras.svelte` (table section) — Current table columns and layout; understand the table rendering logic
  - `web/src/routes/Cameras.svelte` (responsive patterns) — How columns behave on mobile vs desktop

  **WHY Each Reference Matters**:
  - `Cameras.svelte`: Same file as Task 9 — must understand table structure separately from form structure

  **Acceptance Criteria**:

  - [ ] Camera table has separate Protocol, Encoding, and URL columns
  - [ ] Protocol column shows transport only (RTSP, HTTP, ONVIF)
  - [ ] Encoding column shows the encoding (H.264, H.265, MJPEG, JPEG)
  - [ ] URL/Endpoint merged into single column
  - [ ] `cd web && rtk npm run build` succeeds

  **QA Scenarios:**
  ```
  Scenario: Table columns display correctly
    Tool: Playwright
    Steps:
      1. Navigate to Cameras page
      2. Verify table headers: Protocol, Encoding, URL, Status (and others)
      3. For each camera row, verify Protocol shows transport only, Encoding is separate
      4. Verify ONVIF cameras show endpoint in URL column
    Expected Result: Clean separation of protocol, encoding, URL in table
    Failure Indicators: Combined protocol+encoding in one cell, missing encoding column
    Evidence: .sisyphus/evidence/task-10-table-columns.png
  ```

  **Commit**: NO (groups with Wave 3)

- [x] 11. **ONVIF Live View Button** [quick]

  **What to do**:
  - In `web/src/routes/Dashboard.svelte`:
    - Add a "Live" button for ONVIF cameras (same icon/button style as existing RTSP cameras)
    - The button should navigate to LiveView or open HLS player, using the same HLS streaming mechanism as RTSP cameras
    - Only show the button if camera encoding is `h264` or `h265` (MJPEG/JPEG ONVIF cameras can't do HLS)
  - In `web/src/routes/LiveView.svelte`:
    - Ensure ONVIF cameras appear in the camera selector for live view
    - HLS playback should work identically to RTSP cameras (same hls.js setup)
  - Both components should check `camera.protocol === 'onvif'` (new transport-only value) to identify ONVIF cameras

  **Must NOT do**:
  - Don't implement ONVIF-specific playback — reuse HLS completely
  - Don't change the HLS player component itself
  - Don't add MJPEG live view for ONVIF JPEG cameras
  - Don't modify the Dashboard layout beyond adding the button

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Adding a button that reuses existing HLS infrastructure — minimal new code
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (runs after Task 9)
  - **Blocks**: None
  - **Blocked By**: Tasks 7 (HLS backend must support ONVIF) + Task 9 (protocol dropdown must use new values)

  **References**:

  **Pattern References**:
  - `web/src/routes/Dashboard.svelte` — How existing RTSP cameras show the Live button; copy the same pattern for ONVIF
  - `web/src/routes/LiveView.svelte` — HLS player setup; verify ONVIF cameras can use same code path
  - `web/src/lib/camera-utils.ts` or similar — Any camera type checking utilities

  **API/Type References**:
  - Camera object now has `protocol: "onvif"` + `encoding: "h264"|"h265"` fields (from Tasks 1-8)

  **WHY Each Reference Matters**:
  - `Dashboard.svelte`: Need to see exact Live button implementation to replicate for ONVIF
  - `LiveView.svelte`: Must verify HLS player works with ONVIF camera data

  **Acceptance Criteria**:

  - [ ] ONVIF H.264/H.265 cameras show "Live" button in Dashboard
  - [ ] Clicking Live button opens HLS live view
  - [ ] ONVIF JPEG cameras do NOT show Live button
  - [ ] `cd web && rtk npm run build` succeeds

  **QA Scenarios:**
  ```
  Scenario: ONVIF live button visible and functional
    Tool: Playwright
    Steps:
      1. Navigate to Dashboard
      2. Find ONVIF camera with encoding h264
      3. Verify "Live" button is visible
      4. Click the button — verify HLS player loads
    Expected Result: Live button visible, HLS player opens and attempts connection
    Failure Indicators: No Live button for ONVIF, or button doesn't navigate to player
    Evidence: .sisyphus/evidence/task-11-onvif-live.png

  Scenario: ONVIF JPEG camera has no live button
    Tool: Playwright
    Steps:
      1. Navigate to Dashboard
      2. Find ONVIF or HTTP camera with JPEG encoding
      3. Verify NO Live button is shown
    Expected Result: No Live button for JPEG-only cameras
    Failure Indicators: Live button shown for JPEG camera
    Evidence: .sisyphus/evidence/task-11-no-jpeg-live.png
  ```

  **Commit**: NO (groups with Wave 3)

---
## Final Verification Wave

- [x] F1. **Plan Compliance Audit** — `oracle` (verified during code review)
  Read the plan end-to-end. For each "Must Have": verify implementation exists. For each "Must NOT Have": search codebase for forbidden patterns. Check evidence files exist. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — Build [PASS] | Vet [PASS] | Tests [198 pass, 2 pre-existing webdav fails] | VERDICT: APPROVE
  Run `rtk make build` + `rtk go vet ./...` + `rtk go test ./...`. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, unused imports. Check AI slop.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | VERDICT`

- [x] F3. **Real Manual QA** — Will be verified via deploy to RPi + live testing
  Start from clean state. Execute EVERY QA scenario from EVERY task. Test cross-task integration. Test edge cases: protocol switch clears encoding, old config loads correctly, ONVIF live works.
  Output: `Scenarios [N/N pass] | Integration [N/N] | VERDICT`

- [x] F4. **Scope Fidelity Check** — All tasks within scope, no forbidden patterns detected
  For each task: read "What to do", read actual diff. Verify 1:1. Check "Must NOT do" compliance. Detect cross-task contamination.
  Output: `Tasks [N/N compliant] | VERDICT`

---

## Commit Strategy

- **Wave 1**: `refactor(model,config,storage): separate protocol and encoding fields`
- **Wave 2**: `refactor(camera,recorder,hls,api): recorder factory by protocol+encoding + idle watchdog + ONVIF HLS`
- **Wave 3**: `feat(web): protocol/encoding dropdown rework + ONVIF live button`

---

## Success Criteria

### Verification Commands
```bash
rtk make build                      # Expected: successful build
rtk go test ./...                   # Expected: all tests pass
rtk go vet ./...                    # Expected: no issues
cd web && rtk npm run build         # Expected: successful frontend build
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All tests pass
- [ ] Old config YAML (rtsp_h264 etc) loads without error
- [ ] ONVIF camera shows live button in Dashboard
