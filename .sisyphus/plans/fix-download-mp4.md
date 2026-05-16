# Fix Download Progress & MP4 VLC Playback

## TL;DR

> **Quick Summary**: 修复两个关联 bug：(1) 下载进度不更新 → 下载处理器缺 Content-Length；(2) VLC 无法播放 → 同一问题 + MP4 写入路径错误。两处代码修改，部署到 RPi 验证。
> 
> **Deliverables**:
> - 修复后的 `internal/api/handler.go` — 使用 `http.ServeFile()` 替代 `os.ReadFile()`
> - 修复后的 `internal/recorder/h264.go` — muxer 写入 tempPath + 原子重命名
> - 更新对应的单元测试
> - 交叉编译部署到 192.168.63.31 并验证
> 
> **Estimated Effort**: Short
> **Parallel Execution**: YES - 2 waves
> **Critical Path**: Task 1 → Task 3 → Task 4 → Task 5

---

## Context

### Original Request
192.168.63.31 上部署了该项目，下载过程的百分比没有随下载进度而变化，下载下来的视频用 VLC 无法播放。

### Root Cause Analysis
**Bug 1 (下载进度不更新)**: `internal/api/handler.go:305-320` 使用 `os.ReadFile()` + `w.Write(data)` 模式：
- 无 `Content-Length` header → 浏览器无法计算下载百分比
- 无 `Accept-Ranges` header → 不支持断点续传
- 整个文件加载到内存 → 大文件可能导致内存问题

**Bug 2 (VLC 无法播放)**: 同一下载处理器问题。另外 H264 recorder 的 temp→rename 模式损坏：
- `h264.go:284` muxer 传入 `finalPath` 而非 `tempPath` → 绕过了原子重命名
- `closeCurrentSegment()` 中 temp 文件清理代码是死代码（`curTempPath` 在清理前已被置空）
- 空 `.tmp` 文件不断积累

### Metis Review
**Identified Gaps** (addressed):
- 必须在 `http.ServeFile()` 前设置 `Content-Disposition` header
- `muxer.Close()` 失败时必须清理 temp 文件并跳过 DB 插入
- 必须遵循 `h264.go:closeCurrentSegment()` 的新执行顺序：muxer.Close() → CloseSegment() → DB insert → clear state
- 参照 `mjpeg.go:255-264` 的关闭流程模式

---

## Work Objectives

### Core Objective
修复下载处理器和 H264 录制器的原子写入，使浏览器能正确显示下载进度，VLC 能正常播放 MP4。

### Concrete Deliverables
- `internal/api/handler.go` — 下载端点使用 `http.ServeFile()`
- `internal/recorder/h264.go` — muxer 写入 tempPath + 正确的原子重命名流程
- 更新的单元测试
- 部署到 192.168.63.31 的已验证二进制

### Definition of Done
- [ ] 浏览器下载 MP4 时进度条正常显示百分比
- [ ] 下载的 MP4 文件 VLC 可正常播放
- [ ] `rtk go test ./... -v` 全部通过
- [ ] 录制完成后无残留 `.tmp` 文件
- [ ] 192.168.63.31 上功能验证通过

### Must Have
- `http.ServeFile()` 替代 `os.ReadFile()+w.Write()`
- `Content-Disposition` header 在 `ServeFile()` 之前设置
- muxer 使用 tempPath 而非 finalPath
- `CloseSegment()` 执行原子重命名
- muxer.Close() 失败时清理 temp 文件
- 所有现有测试通过

### Must NOT Have (Guardrails)
- 不得修改 MP4Muxer 结构体或任何 muxer 方法
- 不得修改 SegmentStore 接口
- 不得修改 MJPEG recorder（它已经是正确的）
- 不得修改 WebDAV 或 FTP handler
- 不得手动实现 Range 请求处理
- 不得用 wrapper 代码修改 `http.ServeFile()` 行为
- 不得添加 AI slop：过度注释、不必要的抽象、多余的日志

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (go test + testify)
- **Automated tests**: YES (tests-after — 小修复，先改代码后更新测试)
- **Framework**: go test

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately - two independent fixes):
├── Task 1: Fix download handler [quick]
└── Task 2: Fix H264 recorder atomic write [quick]

Wave 2 (After Wave 1 - tests):
└── Task 3: Update unit tests for download handler [quick]

Wave 3 (After Wave 2 - build and deploy):
└── Task 4: Cross-compile, deploy to 192.168.63.31, verify [unspecified-high]

Wave FINAL (After Wave 3):
└── Task F1: End-to-end verification on RPi [unspecified-high]
```

### Dependency Matrix

| Task | Blocked By | Blocks | Wave |
|------|-----------|--------|------|
| 1    | -         | 3      | 1    |
| 2    | -         | 3      | 1    |
| 3    | 1, 2      | 4      | 2    |
| 4    | 3         | F1     | 3    |
| F1   | 4         | -      | FINAL |

### Agent Dispatch Summary

- **Wave 1**: 2 agents — T1 → `quick`, T2 → `quick`
- **Wave 2**: 1 agent — T3 → `quick`
- **Wave 3**: 1 agent — T4 → `unspecified-high`
- **FINAL**: 1 agent — F1 → `unspecified-high`

---

## TODOs

- [x] 1. Fix Download Handler — Replace os.ReadFile with http.ServeFile

  **What to do**:
  - 在 `internal/api/handler.go` 的 `handleDownloadRecording` 函数中（行 305-320），将 `os.ReadFile(filePath)` + `w.Write(data)` 替换为 `http.ServeFile(w, r, filePath)`
  - 在调用 `http.ServeFile()` 之前设置 `Content-Disposition` header（保留现有行为）
  - 删除不再需要的 `contentType` 变量和 `os.ReadFile` 调用
  - 保留行 256-303 的 MJPEG frame 下载逻辑不变（它已经正确使用 `http.ServeFile`）
  - 保留行 281-303 的文件存在性检查和目录处理逻辑
  - `http.ServeFile()` 会自动处理 Content-Type、Content-Length、Accept-Ranges、Range 请求

  **Must NOT do**:
  - 不要修改 MJPEG frame 下载逻辑（行 256-279）
  - 不要修改文件存在性检查逻辑（行 281-303）
  - 不要添加 wrapper 或 interceptor
  - 不要手动设置 Content-Length 或 Accept-Ranges

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 单文件、小范围、明确的代码替换
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Task 2)
  - **Blocks**: Task 3
  - **Blocked By**: None

  **References**:

  **Pattern References** (existing code to follow):
  - `internal/api/handler.go:272` — MJPEG frame download 已经正确使用 `http.ServeFile(w, r, framePath)` — 完全相同的模式
  - `internal/api/handler.go:256-279` — MJPEG frame 下载块（不需要改动）

  **API/Type References**:
  - `net/http.ServeFile` — Go 标准库，自动设置 Content-Length, Accept-Ranges, Content-Type, 支持 Range 请求

  **Acceptance Criteria**:
  - [ ] `internal/api/handler.go` 中 `handleDownloadRecording` 不再使用 `os.ReadFile`
  - [ ] 使用 `http.ServeFile(w, r, filePath)` 提供文件下载
  - [ ] `Content-Disposition` header 在 `ServeFile()` 前设置
  - [ ] MJPEG frame 下载逻辑未改变
  - [ ] `rtk go vet ./internal/api/...` 无错误

  **QA Scenarios (MANDATORY)**:

  ```
  Scenario: Download handler uses http.ServeFile
    Tool: Bash (grep)
    Preconditions: handler.go has been modified
    Steps:
      1. grep -n "http.ServeFile" internal/api/handler.go
      2. Verify the handleDownloadRecording function contains http.ServeFile call
      3. grep -n "os.ReadFile" internal/api/handler.go
      4. Verify os.ReadFile does NOT appear in handleDownloadRecording (line 239+)
    Expected Result: http.ServeFile found in download handler, os.ReadFile removed
    Evidence: .sisyphus/evidence/task-1-servefile-grep.txt

  Scenario: Content-Disposition header preserved
    Tool: Bash (grep)
    Preconditions: handler.go has been modified
    Steps:
      1. grep -n "Content-Disposition" internal/api/handler.go
      2. Verify Content-Disposition is set BEFORE http.ServeFile call
    Expected Result: Content-Disposition header line appears before ServeFile call
    Evidence: .sisyphus/evidence/task-1-content-disposition.txt
  ```

  **Commit**: YES
  - Message: `fix(api): use http.ServeFile for recording downloads`
  - Files: `internal/api/handler.go`

---

- [x] 2. Fix H264 Recorder Atomic Write Pattern

  **What to do**:
  - **修改 1**: `internal/recorder/h264.go:284` — 将 `muxer.NewMP4Muxer(finalPath)` 改为 `muxer.NewMP4Muxer(tempPath)`
  - **修改 2**: 重构 `closeCurrentSegment()` 函数（行 318-355），按新顺序执行：
    1. `muxer.Close()` — 完成 MP4 写入到 tempPath
    2. 如果 muxer.Close() 失败 → 删除 tempPath 文件，记录日志，跳过后续步骤
    3. `r.store.CloseSegment(tempPath, finalPath)` — 执行 fsync + 原子重命名
    4. DB 插入录制记录
    5. 清理状态变量（muxer=nil, curFinalPath="", frameCount=0, curTempPath=""）
  - **修改 3**: 删除旧的死代码 temp 文件清理逻辑（行 351-354）
  - **参照模式**: 查看 `internal/recorder/mjpeg.go` 的关闭流程模式（mjpeg recorder 如何处理 segment 关闭）

  **Must NOT do**:
  - 不要修改 MP4Muxer 结构体或任何 muxer 方法
  - 不要修改 SegmentStore 接口
  - 不要修改 MJPEG recorder
  - 不要修改 CreateSegment 或 CloseSegment 方法

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 小范围代码重构，约 15 行改动，路径明确
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Task 1)
  - **Blocks**: Task 3
  - **Blocked By**: None

  **References**:

  **Pattern References** (existing code to follow):
  - `internal/recorder/h264.go:278-355` — 当前需要修改的完整区域
  - `internal/recorder/mjpeg.go` — MJPEG recorder 的关闭流程模式参考

  **API/Type References**:
  - `internal/storage/manager.go:CloseSegment()` — 执行 `tempFile.Sync()` + `os.Rename(tempPath, finalPath)` — 原子重命名
  - `internal/storage/manager.go:CreateSegment()` — 创建 temp 路径和 final 路径，返回两个路径

  **WHY Each Reference Matters**:
  - `mjpeg.go` 展示了正确的 segment 关闭模式
  - `manager.go:CloseSegment()` 是要调用的原子重命名方法（之前从未被 H264 recorder 调用）

  **Acceptance Criteria**:
  - [ ] `muxer.NewMP4Muxer(tempPath)` 使用 tempPath 而非 finalPath
  - [ ] `closeCurrentSegment()` 调用 `r.store.CloseSegment(r.curTempPath, r.curFinalPath)`
  - [ ] muxer.Close() 失败时清理 temp 文件并跳过 DB 插入
  - [ ] 旧的死代码 temp 清理逻辑已删除
  - [ ] `rtk go vet ./internal/recorder/...` 无错误

  **QA Scenarios (MANDATORY)**:

  ```
  Scenario: Muxer uses tempPath instead of finalPath
    Tool: Bash (grep)
    Preconditions: h264.go has been modified
    Steps:
      1. grep -n "NewMP4Muxer" internal/recorder/h264.go
      2. Verify the argument is tempPath, not finalPath
    Expected Result: Line shows muxer.NewMP4Muxer(tempPath)
    Evidence: .sisyphus/evidence/task-2-muxer-path.txt

  Scenario: CloseSegment is called in closeCurrentSegment
    Tool: Bash (grep)
    Preconditions: h264.go has been modified
    Steps:
      1. grep -n "CloseSegment" internal/recorder/h264.go
      2. Verify CloseSegment is called with tempPath and finalPath
    Expected Result: CloseSegment call found with correct arguments
    Evidence: .sisyphus/evidence/task-2-close-segment.txt

  Scenario: muxer.Close failure handled correctly
    Tool: Bash (grep)
    Preconditions: h264.go has been modified
    Steps:
      1. Read closeCurrentSegment function
      2. Verify that on muxer.Close() error, tempPath is removed and function returns early
    Expected Result: Error handling removes temp file and skips DB insert
    Evidence: .sisyphus/evidence/task-2-error-handling.txt
  ```

  **Commit**: YES
  - Message: `fix(recorder): use atomic temp→rename for H264 segment writes`
  - Files: `internal/recorder/h264.go`

---

- [x] 3. Update Unit Tests

  **What to do**:
  - 运行 `rtk go test ./... -v` 确认所有现有测试通过
  - 查看现有的 download handler 测试（`internal/api/` 下的测试文件）
  - 如果存在 `TestDownloadRecording` 或类似测试，确保它验证：
    - 响应包含 `Content-Length` header
    - 响应包含 `Accept-Ranges` header
    - 响应状态码为 200
  - 如果不存在下载测试，添加一个简单的测试用例
  - 确保 recorder 测试通过（h264 recorder 的修改不应破坏现有测试）

  **Must NOT do**:
  - 不要为 MP4Muxer 添加测试（不在修改范围内）
  - 不要修改 MJPEG 相关测试

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 测试更新和验证，标准模式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2 (sequential after Wave 1)
  - **Blocks**: Task 4
  - **Blocked By**: Task 1, Task 2

  **References**:

  **Test References**:
  - `internal/api/handler_test.go` — 现有 API handler 测试模式
  - `tests/integration_test.go` — 集成测试（7 个场景）

  **Acceptance Criteria**:
  - [ ] `rtk go test ./... -v` 全部通过
  - [ ] 下载测试（如存在）验证 Content-Length 和 Accept-Ranges headers

  **QA Scenarios (MANDATORY)**:

  ```
  Scenario: All tests pass
    Tool: Bash
    Preconditions: Code changes complete
    Steps:
      1. rtk go test ./... -v
    Expected Result: All tests PASS, 0 failures
    Failure Indicators: Any FAIL in output
    Evidence: .sisyphus/evidence/task-3-test-results.txt

  Scenario: Go vet clean
    Tool: Bash
    Preconditions: Code changes complete
    Steps:
      1. rtk go vet ./...
    Expected Result: No output (clean)
    Evidence: .sisyphus/evidence/task-3-vet-results.txt
  ```

  **Commit**: YES (squash with previous commits if desired, or separate)
  - Message: `test(api): update download handler tests for ServeFile`
  - Files: test files as needed

---

- [x] 4. Cross-Compile, Deploy to RPi, and Verify

  **What to do**:
  - 执行 `rtk make cross` 交叉编译 ARM64 二进制
  - 通过 SCP 部署到 192.168.63.31: `scp ./mibee-nvr-arm64 mickey@192.168.63.31:/tmp/mibee-nvr-arm64`
  - SSH 到设备：`ssh mickey@192.168.63.31`
  - 停止服务：`sudo systemctl stop mibee-nvr`
  - 备份旧二进制：`sudo cp /mnt/data/nvr/bin/mibee-nvr /mnt/data/nvr/bin/mibee-nvr.bak`
  - 替换二进制：`sudo cp /tmp/mibee-nvr-arm64 /mnt/data/nvr/bin/mibee-nvr && sudo chmod +x /mnt/data/nvr/bin/mibee-nvr`
  - 启动服务：`sudo systemctl start mibee-nvr`
  - 检查服务状态：`sudo systemctl status mibee-nvr`
  - 检查日志：`sudo journalctl -u mibee-nvr -n 50 --no-pager`

  **Must NOT do**:
  - 不要修改配置文件
  - 不要删除旧录制数据
  - 不要修改 systemd service 文件（除非二进制路径变了）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 涉及远程部署、多个步骤、需要验证和排错
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3
  - **Blocks**: F1
  - **Blocked By**: Task 3

  **References**:

  **External References**:
  - `Makefile` — `cross` target 的具体编译命令和输出路径
  - `deploy/` — systemd service 文件，确认二进制安装路径

  **Acceptance Criteria**:
  - [ ] 交叉编译成功，生成 `mibee-nvr-arm64`
  - [ ] 二进制已部署到 192.168.63.31
  - [ ] 服务正常启动（systemctl status 显示 active）
  - [ ] 日志无错误

  **QA Scenarios (MANDATORY)**:

  ```
  Scenario: Cross-compilation succeeds
    Tool: Bash
    Preconditions: All code changes and tests complete
    Steps:
      1. rtk make cross
      2. Verify mibee-nvr-arm64 binary exists
      3. file mibee-nvr-arm64 (should show ELF ARM64)
    Expected Result: Binary exists, is ARM64 ELF
    Evidence: .sisyphus/evidence/task-4-cross-compile.txt

  Scenario: Service running on RPi
    Tool: Bash (ssh)
    Preconditions: Binary deployed
    Steps:
      1. ssh mickey@192.168.63.31 "sudo systemctl status mibee-nvr"
      2. Verify status is "active (running)"
    Expected Result: Service active, no errors
    Evidence: .sisyphus/evidence/task-4-service-status.txt
  ```

  **Commit**: NO (deployment, no code change)

---

## Final Verification Wave

- [x] F1. End-to-End Verification on RPi

  **What to do**:
  - SSH 到 192.168.63.31
  - 等待新的录制段产生（或触发一次录制）
  - 通过 Web UI 或 curl 测试下载：
    - `curl -I -u user:pass "http://192.168.63.31:9090/api/recordings/{id}/download"` — 验证响应头包含 Content-Length 和 Accept-Ranges
  - 下载一个 MP4 文件到本地
  - 用 ffprobe 验证 MP4 文件结构：`ffprobe downloaded.mp4`
  - 用 VLC 或 ffplay 播放验证：`ffplay downloaded.mp4`
  - 检查录制目录无残留 .tmp 文件：`ssh mickey@192.168.63.31 "find /mnt/data/nvr/recordings -name '*.tmp' | wc -l"` — 应为 0
  - 在浏览器中访问 Web UI 下载页面，验证进度条显示百分比

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 需要远程交互、多步验证、可能需要排错
  - **Skills**: []

  **QA Scenarios (MANDATORY)**:

  ```
  Scenario: Download response headers correct
    Tool: Bash (curl)
    Preconditions: Service running on RPi with a recording available
    Steps:
      1. ssh mickey@192.168.63.31 "curl -sI http://localhost:9090/api/recordings" to get recording IDs
      2. curl -I "http://192.168.63.31:9090/api/recordings/{id}/download" (with auth if needed)
      3. Verify response contains "Content-Length:" header with file size
      4. Verify response contains "Accept-Ranges: bytes"
    Expected Result: Both headers present
    Evidence: .sisyphus/evidence/f1-download-headers.txt

  Scenario: Downloaded MP4 is valid
    Tool: Bash (ffprobe)
    Preconditions: MP4 file downloaded from RPi
    Steps:
      1. Download MP4 from RPi via curl
      2. ffprobe -v error -show_format -show_streams downloaded.mp4
      3. Verify it shows video stream info (codec_name=h264, width, height, duration)
    Expected Result: ffprobe shows valid MP4 with H264 stream, no errors
    Evidence: .sisyphus/evidence/f1-ffprobe-output.txt

  Scenario: No temp files remaining
    Tool: Bash (ssh)
    Preconditions: At least one recording has completed
    Steps:
      1. ssh mickey@192.168.63.31 "find /mnt/data/nvr/recordings -name '*.tmp' -type f"
      2. Count results
    Expected Result: Empty output (0 .tmp files)
    Evidence: .sisyphus/evidence/f1-no-tmp-files.txt
  ```

---

## Commit Strategy

- **Task 1**: `fix(api): use http.ServeFile for recording downloads` - internal/api/handler.go
- **Task 2**: `fix(recorder): use atomic temp→rename for H264 segment writes` - internal/recorder/h264.go
- **Task 3**: `test(api): update download handler tests for ServeFile` - test files
- **Task 4**: NO COMMIT (deployment only)

---

## Success Criteria

### Verification Commands
```bash
rtk go test ./... -v          # Expected: all PASS
rtk go vet ./...               # Expected: no output
rtk make cross                 # Expected: mibee-nvr-arm64 binary
ssh mickey@192.168.63.31 "sudo systemctl status mibee-nvr"  # Expected: active (running)
```

### Final Checklist
- [ ] 浏览器下载进度条显示百分比
- [ ] VLC 可播放下载的 MP4 文件
- [ ] 录制目录无 .tmp 残留文件
- [ ] 所有 go test 通过
- [ ] 192.168.63.31 服务正常运行
