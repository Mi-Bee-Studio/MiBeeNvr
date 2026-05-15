# MiBee NVR 投产修复计划

## TL;DR

> **目标**: 修复所有投产阻断问题，完善前端主题/i18n/移动端体验，部署到测试 RPi 并验证通过。
> 
> **交付物**:
> - 后端安全加固（认证修复、安全头、速率限制）
> - 后端稳定性修复（panic 恢复、关机超时、配置安全默认值）
> - 前端主题系统修复（暗/亮模式实际生效）
> - 前端移动端导航（汉堡菜单）
> - 前端 i18n 缺口填补
> - 构建部署自动化（Makefile 安全升级 + 远程部署）
> - 数据库备份端点
> - 部署到 192.168.63.31 并端到端验证
> 
> **工作量估算**: Large
> **并行执行**: YES - 4 waves
> **关键路径**: T1(环境验证) → T2-T8(后端) + T9-T12(前端) → T13(构建部署) → T14-T15(部署验证)

---

## Context

### Original Request
用户要求对 MiBee NVR 项目进行全面的投产前评估，修复所有阻断级问题，添加前端中英文切换动态变化、黑白主题、及其他前端用户体验问题，最终部署到测试环境 `ssh mickey@192.168.63.31` 并测试通过。

### Interview Summary
**核心讨论**:
- 5 维度深度审查完成（安全、稳定性、资源、测试、运维）
- 前端 UX 审计完成（i18n 90%覆盖率、主题系统存在但不生效、移动导航断裂）
- 构建/部署管线分析完成（手动 scp、无远程部署自动化）
- Metis 缺口分析完成——安全漏洞范围从 17 个调整为 2 个确认 CRITICAL + 5 个 HIGH

**Metis 修正要点**:
- ❌ 路径遍历（S3）= FALSE — camera ID 来自 DB，非用户输入
- ❌ Upload 风险（S9-S10）= FALSE — 已有大小限制、类型验证、路径沙箱
- ❌ WebDAV 安全（S9）= FALSE — 方法白名单正确阻止写入
- ✅ 认证绕过（S1）= REAL — 空 password_hash 绕过所有认证
- ✅ FTP 明文（S4）= REAL — 无 TLS，密码明文

### Metis Review
**已处理的缺口**:
- 安全范围过度: 从 17 个漏洞调整为 2 个确认 CRITICAL，避免无效工作量
- 主题策略: 使用已有 CSS 变量，机械替换硬编码类名，不新增依赖
- DB 迁移: 不构建框架，仅添加 schema_version + VACUUM INTO 备份端点
- 部署自动化: 一个 Makefile 目标，不引入 Ansible/Docker

---

## Work Objectives

### Core Objective
修复所有确认的投产阻断问题，完善前端用户体验，构建安全部署流程，部署到测试 RPi 并通过端到端验证。

### Concrete Deliverables
- 后端: 认证修复、panic 恢复、关机超时、安全默认值、备份端点、安全头
- 前端: 可用的暗/亮主题切换、移动端汉堡导航、i18n 100% 覆盖
- 运维: 安全的 Makefile 升级目标、远程部署脚本
- 测试: 在 192.168.63.31 上运行的完整 NVR 系统

### Definition of Done
- [x] `ssh mickey@192.168.63.31 "systemctl is-active mibee-nvr"` 返回 "active"
- [x] `curl http://192.168.63.31:9090/api/health` 返回详细健康状态
- [x] 前端暗/亮主题切换实际生效（Playwright 截图对比）
- [x] 移动端 375px 视口下导航可用（汉堡菜单）
- [x] 前端中英文切换无遗漏字符串

### Must Have
- 认证系统正确工作（空密码不绕过）
- Web UI 受认证保护
- 录像器 panic 不导致进程挂死
- 关机在 30s 内完成
- 默认 segment_duration 为 30s（RPi 3B 安全）
- 暗亮主题实际切换生效
- 移动端导航可用

### Must NOT Have (Guardrails)
- ❌ 不重构认证系统（保持 BasicAuth + bcrypt）
- ❌ 不重构录像器架构或重连逻辑
- ❌ 不构建 DB 迁移框架
- ❌ 不引入前端新依赖（主题/i18n）
- ❌ 不添加 CI/CD、Docker、容器化
- ❌ 不修改 upload/handler.go、webdav/server.go（已确认安全）
- ❌ 不改变 systemd 服务的用户、路径、目录结构
- ❌ 主题修复仅为机械替换——零布局变更、零新组件

---

## Verification Strategy

> **零人工干预** — 所有验证由 agent 执行。无例外。

### Test Decision
- **Infrastructure exists**: YES（go test + 前端 npm run build）
- **Automated tests**: Tests-after（后端修复后补充关键测试）
- **Framework**: go test（后端）、Playwright（前端 E2E）

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Frontend/UI**: Use Playwright — Navigate, interact, assert DOM, screenshot
- **Backend/API**: Use Bash (curl) — Send requests, assert status + response
- **RPi Deploy**: Use Bash (ssh) — Remote commands, service status, log checks

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 0 (Start Immediately - environment validation):
└── Task 1: SSH connectivity + RPi environment check [quick]

Wave 1 (After Wave 0 - backend critical fixes, MAX PARALLEL):
├── Task 2: Fix auth bypass + implement hash-password CLI [deep]
├── Task 3: Add panic recovery to recorder goroutines [quick]
├── Task 4: Add shutdown timeout to main.go [quick]
├── Task 5: Fix default segment_duration + config validation [quick]
├── Task 6: Protect Web UI with auth middleware [quick]
├── Task 7: Add security headers + auth rate limiting [unspecified-high]
└── Task 8: Add DB backup endpoint + schema version tracking [unspecified-high]

Wave 2 (After Wave 0 - frontend UX fixes, parallel with Wave 1):
├── Task 9: Fix theme system (CSS var replacement in all components) [visual-engineering]
├── Task 10: Add mobile hamburger navigation [visual-engineering]
├── Task 11: Fix i18n gaps (hardcoded strings) [quick]
└── Task 12: Frontend UX polish (toast consistency, basic a11y) [visual-engineering]

Wave 3 (After Waves 1+2 - build, deploy, verify):
├── Task 13: Fix Makefile + add deploy/frontend targets [unspecified-high]
├── Task 14: Build, deploy to RPi, verify deployment [unspecified-high]
└── Task 15: End-to-end testing on RPi + fix issues [deep]

Wave FINAL (After ALL tasks — 4 parallel reviews, then user okay):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality review (unspecified-high)
├── Task F3: Real manual QA on RPi (unspecified-high)
└── Task F4: Scope fidelity check (deep)
→ Present results → Get explicit user okay

Critical Path: T1 → T2 → T8 → T13 → T14 → T15 → F1-F4 → user okay
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 8 (Waves 1+2)
```

### Dependency Matrix

| Task | Blocked By | Blocks |
|------|-----------|--------|
| T1 | - | T2-T15 |
| T2 | T1 | T6, T13 |
| T3 | T1 | T13 |
| T4 | T1 | T13 |
| T5 | T1 | T13 |
| T6 | T1, T2 | T13 |
| T7 | T1 | T13 |
| T8 | T1 | T13 |
| T9 | T1 | T13 |
| T10 | T1 | T13 |
| T11 | T1 | T13 |
| T12 | T1 | T13 |
| T13 | T2-T12 | T14 |
| T14 | T13 | T15 |
| T15 | T14 | F1-F4 |

### Agent Dispatch Summary

- **Wave 0**: 1 task — T1 → `quick`
- **Wave 1**: 7 tasks — T2 → `deep`, T3-T6 → `quick`, T7-T8 → `unspecified-high`
- **Wave 2**: 4 tasks — T9-T10 → `visual-engineering`, T11 → `quick`, T12 → `visual-engineering`
- **Wave 3**: 3 tasks — T13 → `unspecified-high`, T14 → `unspecified-high`, T15 → `deep`
- **FINAL**: 4 tasks — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

---

- [x] 1. **SSH 连接 + RPi 环境验证**

  **What to do**:
  - 通过 `ssh mickey@192.168.63.31` 验证 SSH 连接
  - 检查目标目录 `/mnt/data/nvr/` 是否存在
  - 检查 `nvr` 用户是否存在
  - 检查是否有已运行的 mibee-nvr 服务
  - 检查磁盘空间 (`df -h /mnt/data`)
  - 检查是否已安装 mediamtx / rpicam-vid / ffmpeg
  - 检查端口占用 (9090, 2121)

  **Must NOT do**:
  - 不修改目标机器上的任何文件
  - 不安装任何软件

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can RunIn Parallel**: NO (blocks everything)
  - **Parallel Group**: Wave 0 (sequential)
  - **Blocks**: T2-T15 (all subsequent tasks)
  - **Blocked By**: None

  **References**:
  - `deploy/mibee-nvr.service` — 目标服务配置，查看 ExecStart 路径
  - `docs/en/deployment.md` — 部署文档，查看目录结构和用户设置
  - `Makefile` — install 目标，查看预期路径

  **Acceptance Criteria**:
  - [ ] SSH 连接成功: `ssh mickey@192.168.63.31 "echo ok"` 返回 "ok"
  - [ ] 目录存在: `ssh mickey@192.168.63.31 "ls /mnt/data/nvr/"` 不报错
  - [ ] 磁盘空间 > 1GB 可用
  - [ ] 记录所有环境信息到 `.sisyphus/evidence/task-1-env-check.txt`

  **QA Scenarios:**
  ```
  Scenario: SSH connectivity
    Tool: Bash (ssh)
    Steps:
      1. ssh mickey@192.168.63.31 "echo ok"
      2. ssh mickey@192.168.63.31 "uname -m"
    Expected Result: "ok" and "aarch64" (ARM64 confirmed)
    Evidence: .sisyphus/evidence/task-1-ssh-connect.txt

  Scenario: Environment readiness
    Tool: Bash (ssh)
    Steps:
      1. ssh mickey@192.168.63.31 "ls -la /mnt/data/nvr/ 2>&1"
      2. ssh mickey@192.168.63.31 "id nvr 2>&1"
      3. ssh mickey@192.168.63.31 "df -h /mnt/data"
      4. ssh mickey@192.168.63.31 "systemctl is-active mibee-nvr 2>&1"
    Expected Result: All commands return useful info or clear error states
    Evidence: .sisyphus/evidence/task-1-env-check.txt
  ```

  **Commit**: NO (validation only)

---


- [x] 2. **修复认证绕过 + 实现 hash-password CLI**

  **What to do**:
  - `internal/middleware/auth.go:15-18`: 移除空 passwordHash 直接放行逻辑。空 hash 时拒绝所有请求并记录警告
  - `cmd/mibee-nvr/main.go`: 添加 `hash-password` 子命令，调用 `middleware.HashPassword()` 并输出结果
  - 确保当 password_hash 为空时，启动日志打印明显警告

  **Must NOT do**:
  - 不重构认证架构（保持 BasicAuth + bcrypt）
  - 不添加 JWT/session/token 等新认证方式

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T3-T8)
  - **Blocks**: T6, T13
  - **Blocked By**: T1

  **References**:
  - `internal/middleware/auth.go:15-18` — 空 hash 绕过逻辑，THIS IS THE BUG
  - `internal/middleware/auth.go:29-36` — HashPassword() 函数已存在，需从 CLI 调用
  - `cmd/mibee-nvr/main.go:31-44` — 当前只有 --config 和 --version flag，需添加子命令
  - `config.example.yaml:10` — 注释引用了不存在的 hash-password 命令

  **Acceptance Criteria**:
  - [ ] 空 password_hash 时认证中间件拒绝请求（返回 401），不绕过
  - [ ] `./mibee-nvr hash-password test123` 输出 bcrypt 哈希字符串
  - [ ] 启动时空 hash 记录 WARNING 日志
  - [ ] `go test ./internal/middleware/... -v` PASS

  **QA Scenarios:**
  ```
  Scenario: Auth bypass blocked
    Tool: Bash
    Steps:
      1. Build: CGO_ENABLED=0 go build -o /tmp/nvr-test ./cmd/mibee-nvr/
      2. Run with empty hash: /tmp/nvr-test -config /tmp/test-config.yaml &
      3. curl -sf http://localhost:9090/api/recordings
    Expected Result: HTTP 401 Unauthorized
    Failure Indicators: HTTP 200 (auth bypassed)
    Evidence: .sisyphus/evidence/task-2-auth-bypass-blocked.txt

  Scenario: hash-password CLI works
    Tool: Bash
    Steps:
      1. ./mibee-nvr hash-password mysecret
    Expected Result: Output starts with "$2a$" (bcrypt format)
    Evidence: .sisyphus/evidence/task-2-hash-password-cli.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `fix(security): production hardening — auth, panic recovery, shutdown, config`
  - Files: `cmd/mibee-nvr/main.go`, `internal/middleware/auth.go`
  - Pre-commit: `go vet ./... && go test ./...`

---

- [x] 3. **添加录像器 goroutine panic 恢复**

  **What to do**:
  - `internal/recorder/h264.go`: 在 `run()` 函数开头添加 `defer func() { if r := recover(); r != nil { log.Printf(...) } }()`
  - `internal/recorder/mjpeg.go`: 同上
  - panic 恢复后: 确保 `r.done` channel 被关闭（防止 camMgr.Stop() 永久阻塞）
  - panic 恢复后: 设置 recorder status 为 StatusStopped
  - panic 恢复后: 记录 panic 堆栈到日志（使用 `runtime.Stack()`）

  **Must NOT do**:
  - 不修改重连/退避逻辑
  - 不修改 ring buffer 或帧处理逻辑
  - 不添加自动重启（超出范围）

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2, T4-T8)
  - **Blocks**: T13
  - **Blocked By**: T1

  **References**:
  - `internal/recorder/h264.go:141-163` — `run()` 函数，需在开头添加 defer recover
  - `internal/recorder/h264.go:142-143` — 现有 defer close(r.done) 和 setStatus，panic 会跳过
  - `internal/recorder/mjpeg.go:111-133` — 同上，`run()` 需添加 defer recover
  - `internal/recorder/h264.go:249-316` — `writeFrames()` goroutine，也需独立 panic 恢复
  - `internal/camera/manager.go:159-164` — Stop() 等待 r.done channel，理解为何 panic 恢复很重要

  **Acceptance Criteria**:
  - [ ] `run()` 函数有 panic recovery defer
  - [ ] `writeFrames()` goroutine 有独立 panic recovery
  - [ ] panic 后 r.done channel 被正确关闭
  - [ ] panic 后 status 设置为 StatusStopped
  - [ ] panic 堆栈记录到日志
  - [ ] `go test ./internal/recorder/... -v` PASS

  **QA Scenarios:**
  ```
  Scenario: Panic recovered in recorder
    Tool: Bash
    Steps:
      1. Build: CGO_ENABLED=0 go build -o /tmp/nvr-test ./cmd/mibee-nvr/
      2. Run with test config that has a camera
      3. Verify process stays alive even if recorder panics
    Expected Result: Process continues running, panic logged with stack trace
    Failure Indicators: Process exits/crashes
    Evidence: .sisyphus/evidence/task-3-panic-recovery.txt

  Scenario: Existing tests still pass
    Tool: Bash
    Steps:
      1. rtk go test ./internal/recorder/... -v -count=1
    Expected Result: All tests PASS
    Evidence: .sisyphus/evidence/task-3-tests.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: (same as T2 commit)
  - Files: `internal/recorder/h264.go`, `internal/recorder/mjpeg.go`
  - Pre-commit: `go test ./internal/recorder/... -v`

---

- [x] 4. **添加关机超时保护**

  **What to do**:
  - `cmd/mibee-nvr/main.go:191-195`: 在 signal handler 中使用 `context.WithTimeout(context.Background(), 30*time.Second)`
  - 如果 camMgr.Stop() 或 srv.Shutdown() 在 30s 内未完成，记录 WARNING 并强制退出
  - 使用 goroutine + select + timer 实现超时

  **Must NOT do**:
  - 不并行化录像器停止（保持顺序）
  - 不修改 camMgr.Stop() 或 recorder.Stop() 内部逻辑

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2, T3, T5-T8)
  - **Blocks**: T13
  - **Blocked By**: T1

  **References**:
  - `cmd/mibee-nvr/main.go:186-195` — 当前 signal handler，需添加超时
  - `cmd/mibee-nvr/main.go:193` — camMgr.Stop() 无超时
  - `cmd/mibee-nvr/main.go:194` — srv.Shutdown(context.Background()) 无超时

  **Acceptance Criteria**:
  - [ ] 进程在 SIGTERM 后 30s 内必定退出
  - [ ] 超时时记录 WARNING 日志
  - [ ] 正常关机不受影响

  **QA Scenarios:**
  ```
  Scenario: Shutdown timeout enforced
    Tool: Bash
    Steps:
      1. Build and start NVR
      2. Send SIGTERM: kill -TERM $PID
      3. Measure time: time kill -0 $PID 2>/dev/null; while [ $? -eq 0 ]; do sleep 1; kill -0 $PID 2>/dev/null; done
    Expected Result: Process exits within 35 seconds
    Failure Indicators: Process still running after 35s
    Evidence: .sisyphus/evidence/task-4-shutdown-timeout.txt

  Scenario: Normal shutdown works
    Tool: Bash
    Steps:
      1. Build and start NVR with no cameras
      2. Send SIGTERM
      3. Check exit code
    Expected Result: Process exits cleanly within 5 seconds
    Evidence: .sisyphus/evidence/task-4-normal-shutdown.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: (same as T2 commit)
  - Files: `cmd/mibee-nvr/main.go`
  - Pre-commit: `go vet ./cmd/...`

- [x] 5. **修复默认 segment_duration + 配置验证**

  **What to do**:
  - `internal/config/config.go`: 将 `segment_duration` 默认值从 `"10m"` 改为 `"30s"`
  - `config.example.yaml`: 同步更新示例配置中的 segment_duration 为 `"30s"`
  - `internal/config/config.go` Validate(): 添加 segment_duration 上限检查（如 >5m 则警告）
  - `internal/config/config.go` Validate(): 添加 retention_days 范围检查（1-3650）
  - `internal/config/config.go` Validate(): 添加 disk_threshold 范围检查（50-99）
  - `internal/config/config.go`: 添加 `version` 字段（字符串，如 `"1.0"`）

  **Must NOT do**:
  - 不修改 segment_duration 解析逻辑
  - 不修改 MP4Muxer 内部实现

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2-T4, T6-T8)
  - **Blocks**: T13
  - **Blocked By**: T1

  **References**:
  - `internal/config/config.go:163` — 当前默认 segment_duration 为 "10m"，需改为 "30s"
  - `internal/config/config.go:125-151` — Validate() 函数，需添加更多检查
  - `config.example.yaml:6` — 示例配置中的 segment_duration，需同步更新
  - `internal/muxer/mp4mux.go:24-25` — samples []sample 无限增长，理解为何 30s 限制重要

  **Acceptance Criteria**:
  - [ ] `grep -n '10m' internal/config/config.go` 在默认值区域无匹配
  - [ ] `config.example.yaml` 中 segment_duration 为 "30s"
  - [ ] Validate() 检查 segment_duration 上限（>5m 警告或拒绝）
  - [ ] Validate() 检查 retention_days 和 disk_threshold 范围
  - [ ] Config 结构体有 version 字段
  - [ ] `go test ./internal/config/... -v` PASS

  **QA Scenarios:**
  ```
  Scenario: Default segment_duration is safe for RPi
    Tool: Bash
    Steps:
      1. grep -n '10m' internal/config/config.go
      2. grep -n '30s' internal/config/config.go
      3. grep 'segment_duration' config.example.yaml
    Expected Result: No "10m" in defaults, "30s" present in both files
    Evidence: .sisyphus/evidence/task-5-config-defaults.txt

  Scenario: Config validation catches dangerous values
    Tool: Bash
    Steps:
      1. rtk go test ./internal/config/... -v -run TestValidate
    Expected Result: Tests verify segment_duration upper bound, retention_days range
    Evidence: .sisyphus/evidence/task-5-config-validation.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: (same as T2 commit)
  - Files: `internal/config/config.go`, `config.example.yaml`
  - Pre-commit: `go test ./internal/config/... -v`

---

- [x] 6. **Web UI 添加认证保护**

  **What to do**:
  - `cmd/mibee-nvr/main.go:137-139`: 将 NotFound handler（静态文件服务）包裹在 auth 中间件中
  - 确保 `/api/health` 仍然是公开路由（无需认证）
  - 确保所有静态资源请求（.js, .css, .svg, .html）都受认证保护

  **Must NOT do**:
  - 不修改路由架构
  - 不改变 API handler 的路由注册方式

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2-T5, T7-T8)
  - **Blocks**: T13
  - **Blocked By**: T1, T2 (auth middleware must be fixed first)

  **References**:
  - `cmd/mibee-nvr/main.go:137-139` — NotFound handler 未包裹 auth，THIS IS THE FIX
  - `cmd/mibee-nvr/main.go:85-92` — auth 中间件创建和 API handler 挂载，参考此模式
  - `internal/api/handler.go:44-46` — 公开路由（health, login），需保持不变
  - `internal/ui/embed.go` — //go:embed static/* 理解静态文件来源

  **Acceptance Criteria**:
  - [ ] `curl -sf http://localhost:9090/` 返回 401
  - [ ] `curl -sf http://localhost:9090/api/health` 返回 200（公开）
  - [ ] `curl -sf -u admin:password http://localhost:9090/` 返回 200（认证后可访问）

  **QA Scenarios:**
  ```
  Scenario: Static files require auth
    Tool: Bash
    Steps:
      1. Build and start NVR with auth configured
      2. curl -sf http://localhost:9090/
      3. curl -sf http://localhost:9090/index.html
    Expected Result: Both return HTTP 401
    Failure Indicators: HTTP 200 without credentials
    Evidence: .sisyphus/evidence/task-6-static-auth.txt

  Scenario: Health endpoint stays public
    Tool: Bash
    Steps:
      1. curl -sf http://localhost:9090/api/health
    Expected Result: HTTP 200 with {"status":"ok"}
    Evidence: .sisyphus/evidence/task-6-health-public.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: (same as T2 commit)
  - Files: `cmd/mibee-nvr/main.go`
  - Pre-commit: `go vet ./cmd/...`

---

- [x] 7. **添加安全响应头 + 认证速率限制**

  **What to do**:
  - 创建 `internal/middleware/security.go`（或扩展 auth.go）:
    - 添加安全头中间件: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `X-XSS-Protection: 1; mode=block`, `Referrer-Policy: strict-origin-when-cross-origin`
    - 添加 `Content-Security-Policy` 头（允许 inline scripts/styles 因 SPA 需要）
  - 在 `internal/middleware/auth.go` 添加简单速率限制:
    - 每 IP 每分钟最多 20 次认证失败
    - 使用 `sync.Map` + 时间窗口实现（无外部依赖）
    - 超限时返回 429 Too Many Requests
    - 记录失败尝试日志
  - 在 `cmd/mibee-nvr/main.go` 中注册安全头中间件

  **Must NOT do**:
  - 不引入外部 rate limiting 库
  - 不修改 API handler 内部逻辑

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2-T6, T8)
  - **Blocks**: T13
  - **Blocked By**: T1

  **References**:
  - `internal/middleware/auth.go` — 现有认证中间件，需在此添加速率限制
  - `cmd/mibee-nvr/main.go:85-92` — 中间件注册位置，需添加安全头
  - `internal/api/handler.go` — API handler，理解中间件链

  **Acceptance Criteria**:
  - [ ] 所有 HTTP 响应包含安全头 (X-Content-Type-Options, X-Frame-Options 等)
  - [ ] 连续 20 次错误密码后返回 429
  - [ ] 速率限制在 1 分钟窗口后重置
  - [ ] `go test ./internal/middleware/... -v` PASS

  **QA Scenarios:**
  ```
  Scenario: Security headers present
    Tool: Bash
    Steps:
      1. Build and start NVR
      2. curl -sI http://localhost:9090/api/health | grep -i 'x-content-type\|x-frame\|x-xss'
    Expected Result: All three headers present
    Evidence: .sisyphus/evidence/task-7-security-headers.txt

  Scenario: Rate limiting blocks brute force
    Tool: Bash
    Steps:
      1. for i in $(seq 1 25); do curl -sf -u admin:wrongpass http://localhost:9090/api/recordings -o /dev/null -w "%{http_code}\n"; done
    Expected Result: First 20 return 401, then 429 Too Many Requests
    Evidence: .sisyphus/evidence/task-7-rate-limit.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: (same as T2 commit)
  - Files: `internal/middleware/auth.go`, `internal/middleware/security.go`(new), `cmd/mibee-nvr/main.go`
  - Pre-commit: `go test ./internal/middleware/... -v`

---

- [x] 8. **添加 DB 备份端点 + schema 版本追踪**

  **What to do**:
  - `internal/storage/db.go`: 在 Init() 中创建 `schema_meta` 表（`key TEXT PRIMARY KEY, value TEXT`），插入 `schema_version = "1"`
  - `internal/storage/db.go`: 添加 `Backup(ctx context.Context, dst string) error` 方法，使用 `VACUUM INTO dst` 实现备份
  - `internal/api/handler.go`: 添加 `POST /api/backup` 端点，触发备份并返回文件路径
  - `internal/api/handler.go`: 添加 `GET /api/backup` 端点，返回最近备份信息
  - 备份文件保存到 storage root 下的 `backups/` 子目录

  **Must NOT do**:
  - 不构建 DB 迁移框架（只加 schema 版本记录）
  - 不实现定时自动备份（后续功能）
  - 不修改现有表结构

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2-T7)
  - **Blocks**: T13
  - **Blocked By**: T1

  **References**:
  - `internal/storage/db.go:52-82` — Init() 函数中 CREATE TABLE IF NOT EXISTS，在此添加 schema_meta 表
  - `internal/storage/db.go:31-43` — SQLite pragma 配置，理解 WAL 模式对 VACUUM INTO 的影响
  - `internal/api/handler.go:44-46` — 路由注册模式，参考 Routes() 函数
  - `internal/api/handler.go` — Handler 结构体，理解如何添加新端点

  **Acceptance Criteria**:
  - [ ] `schema_meta` 表存在且有 `schema_version = "1"` 记录
  - [ ] `POST /api/backup` 返回备份文件路径
  - [ ] 备份文件是有效的 SQLite 数据库（可打开查询）
  - [ ] `go test ./internal/storage/... -v` PASS

  **QA Scenarios:**
  ```
  Scenario: Backup creates valid SQLite file
    Tool: Bash
    Steps:
      1. Build and start NVR with test data
      2. curl -sf -X POST -u admin:pass http://localhost:9090/api/backup | jq .
      3. sqlite3 /path/to/backup.db "SELECT count(*) FROM recordings;"
    Expected Result: Backup file exists and contains same recording count as main DB
    Evidence: .sisyphus/evidence/task-8-backup.txt

  Scenario: Schema version tracked
    Tool: Bash
    Steps:
      1. Start NVR with fresh DB
      2. sqlite3 recordings/mibee-nvr.db "SELECT value FROM schema_meta WHERE key='schema_version';"
    Expected Result: Returns "1"
    Evidence: .sisyphus/evidence/task-8-schema-version.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: (same as T2 commit)
  - Files: `internal/storage/db.go`, `internal/api/handler.go`
  - Pre-commit: `go test ./internal/storage/... ./internal/api/... -v`

---

- [x] 9. **修复前端主题系统（CSS 变量替换）**

  **What to do**:
  - 将所有 Svelte 组件中的硬编码 Tailwind 颜色类替换为 `app.css` 中已定义的 CSS 变量引用
  - 替换策略: `bg-slate-900` → `bg-[var(--bg-primary)]`，`text-slate-100` → `text-[var(--text-primary)]` 等
  - 审计 `app.css:18-73` 中所有可用的 CSS 变量，确保覆盖所有需要的颜色
  - 如果缺少变量（如某个组件需要 `--border-focus` 等），在 app.css 中补充
  - 涉及文件（ALL route 组件）:
    - `web/src/routes/Login.svelte`
    - `web/src/routes/Recordings.svelte`
    - `web/src/routes/RecordingDetail.svelte`
    - `web/src/routes/Cameras.svelte`
    - `web/src/routes/Stats.svelte`
    - `web/src/routes/Settings.svelte`
    - `web/src/components/Header.svelte`
    - `web/src/components/Pagination.svelte`
  - 验证暗色/亮色模式切换实际生效（ThemeToggle.svelte 不需修改）

  **Must NOT do**:
  - 不新增前端 npm 依赖
  - 不修改 ThemeToggle.svelte
  - 不改变组件布局或新增组件
  - 纯机械替换颜色类名

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`/frontend-ui-ux`]
    - `/frontend-ui-ux`: CSS 变量和 Tailwind 集成经验

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T10-T12)
  - **Blocks**: T13
  - **Blocked By**: T1

  **References**:
  - `web/src/app.css:18-73` — 已定义的暗/亮主题 CSS 变量，替换时参考此列表
  - `web/src/app.css:75-200` — `[data-theme="light"]` 覆盖变量，理解亮色主题如何工作
  - `web/src/components/ThemeToggle.svelte` — 主题切换组件，不修改但理解其如何设置 data-theme 属性
  - `web/src/routes/Recordings.svelte` — 最大的页面，包含最多硬编码颜色（bg-slate-900, bg-slate-800 等）
  - `web/src/routes/Login.svelte` — 登录页，使用 bg-slate-900 背景

  **Acceptance Criteria**:
  - [ ] `rg 'bg-slate-900|bg-slate-800|text-slate-100|text-slate-300|border-slate-700' web/src/routes/ --count` 返回 0 匹配
  - [ ] `rg 'var\(--' web/src/routes/ --count` 每个路由文件 > 0 匹配
  - [ ] `cd web && npm run build` 成功无错误
  - [ ] 暗色主题下外观与修改前一致
  - [ ] 亮色主题切换后页面正确变亮

  **QA Scenarios:**
  ```
  Scenario: Theme toggle actually changes colors
    Tool: Playwright
    Steps:
      1. Open http://localhost:9090/ in browser
      2. Login with credentials
      3. Take screenshot (dark mode default)
      4. Click theme toggle button
      5. Take screenshot (should be light mode)
      6. Verify body background color changed from dark to light
    Expected Result: Screenshots show different background colors
    Evidence: .sisyphus/evidence/task-9-theme-dark.png, task-9-theme-light.png

  Scenario: No hardcoded color classes remain
    Tool: Bash
    Steps:
      1. rg 'bg-slate-900|bg-slate-800' web/src/routes/ --count
      2. rg 'text-slate-100|text-slate-300|text-slate-400' web/src/routes/ --count
    Expected Result: 0 matches in all files
    Failure Indicators: Any matches found
    Evidence: .sisyphus/evidence/task-9-no-hardcoded-colors.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Message: `feat(ui): theme system, mobile nav, i18n gaps, UX polish`
  - Files: `web/src/routes/*.svelte`, `web/src/components/*.svelte`, `web/src/app.css`
  - Pre-commit: `cd web && npm run build`

---

- [x] 10. **添加移动端汉堡导航**

  **What to do**:
  - `web/src/components/Header.svelte`: 在 768px 以下视口隐藏桌面导航链接，显示汉堡按钮
  - 添加汉堡菜单展开/收起逻辑（Svelte 5 reactive state）
  - 展开时显示垂直排列的导航链接（覆盖层或下拉菜单）
  - 点击导航链接后自动收起菜单
  - 汉堡按钮使用 CSS-only 或 inline SVG 图标（不引入图标库）
  - 使用 Tailwind 的 `md:` breakpoint 确保桌面端不受影响

  **Must NOT do**:
  - 不修改桌面端导航布局
  - 不引入新依赖
  - 不改变路由逻辑

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`/frontend-ui-ux`]
    - `/frontend-ui-ux`: 响应式设计经验

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T9, T11-T12)
  - **Blocks**: T13
  - **Blocked By**: T1

  **References**:
  - `web/src/components/Header.svelte` — 现有导航栏，需添加汉堡菜单
  - `web/src/routes/App.svelte` — 路由逻辑，理解导航结构
  - `web/src/app.css` — 样式系统，理解设计语言

  **Acceptance Criteria**:
  - [ ] 视口 >= 768px: 桌面导航正常显示，无汉堡按钮
  - [ ] 视口 < 768px: 汉堡按钮可见，导航链接隐藏
  - [ ] 点击汉堡按钮: 菜单展开显示所有导航链接
  - [ ] 点击导航链接: 菜单收起 + 正确导航
  - [ ] `cd web && npm run build` 成功

  **QA Scenarios:**
  ```
  Scenario: Mobile navigation works at 375px
    Tool: Playwright
    Steps:
      1. Set viewport to 375x812 (iPhone)
      2. Open http://localhost:9090/
      3. Login
      4. Verify: hamburger button visible, nav links hidden
      5. Click hamburger button
      6. Verify: nav links now visible in vertical layout
      7. Click "Recordings" link
      8. Verify: navigated to recordings, menu closed
    Expected Result: All steps pass
    Evidence: .sisyphus/evidence/task-10-mobile-nav-375px.png

  Scenario: Desktop navigation unchanged
    Tool: Playwright
    Steps:
      1. Set viewport to 1280x800
      2. Open http://localhost:9090/
      3. Login
      4. Verify: nav links visible inline, no hamburger button
    Expected Result: Desktop navigation looks same as before
    Evidence: .sisyphus/evidence/task-10-desktop-nav.png
  ```

  **Commit**: YES (groups with Wave 2)
  - Message: (same as T9 commit)
  - Files: `web/src/components/Header.svelte`
  - Pre-commit: `cd web && npm run build`

---

- [x] 11. **修复 i18n 缺口（硬编码字符串）**

  **What to do**:
  - 将以下硬编码字符串提取为 i18n key:
    - `web/src/routes/Recordings.svelte:139` — "MiBee NVR" logo → `t('app.name')`
    - `web/src/routes/Cameras.svelte:152` — 同上
    - `web/src/routes/Stats.svelte:80` — 同上
    - `web/src/routes/Settings.svelte:103` — 同上
    - `web/src/routes/Cameras.svelte:226-228` — "RTSP H.264"/"RTSP MJPEG"/"HTTP JPEG" → `t('camera.protocol.*')`
    - `web/src/routes/Recordings.svelte:231` — `{recording.format === 'h264' ? 'MP4' : 'JPEG'}` → `t('recording.format.*')`
    - `web/src/routes/RecordingDetail.svelte:458` — 同上
  - 在 `web/src/lib/i18n/en.json` 和 `zh.json` 中添加缺失的 key
  - 确保 en.json 和 zh.json key 完全一致（parity check）

  **Must NOT do**:
  - 不重构 i18n 系统
  - 不添加新的 i18n 依赖

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T9-T10, T12)
  - **Blocks**: T13
  - **Blocked By**: T1

  **References**:
  - `web/src/lib/i18n/en.json` — 英文翻译文件，需添加新 key
  - `web/src/lib/i18n/zh.json` — 中文翻译文件，需添加新 key
  - `web/src/lib/i18n/index.ts` — i18n 实现，理解 t() 函数用法
  - `web/src/routes/Recordings.svelte:139, 231` — 硬编码字符串位置
  - `web/src/routes/Cameras.svelte:152, 226-228` — 硬编码字符串位置
  - `web/src/routes/Stats.svelte:80` — 硬编码字符串位置
  - `web/src/routes/Settings.svelte:103` — 硬编码字符串位置
  - `web/src/routes/RecordingDetail.svelte:458` — 硬编码字符串位置

  **Acceptance Criteria**:
  - [ ] `diff <(jq -r 'keys[]' web/src/lib/i18n/en.json | sort) <(jq -r 'keys[]' web/src/lib/i18n/zh.json | sort)` 无输出
  - [ ] `rg '"MiBee NVR"' web/src/routes/ --count` 仅匹配 i18n 文件中的 value
  - [ ] `rg '"RTSP H.264"' web/src/routes/` 无匹配
  - [ ] `cd web && npm run build` 成功

  **QA Scenarios:**
  ```
  Scenario: i18n key parity between languages
    Tool: Bash
    Steps:
      1. diff <(jq -r 'keys[]' web/src/lib/i18n/en.json | sort) <(jq -r 'keys[]' web/src/lib/i18n/zh.json | sort)
    Expected Result: No output (identical key sets)
    Evidence: .sisyphus/evidence/task-11-i18n-parity.txt

  Scenario: No hardcoded UI strings in routes
    Tool: Bash
    Steps:
      1. rg '"MiBee NVR"' web/src/routes/ --count -l
      2. rg '"RTSP H.264"' web/src/routes/ -l
      3. rg '"MP4"' web/src/routes/ -l
    Expected Result: No files matched (or only i18n value references)
    Evidence: .sisyphus/evidence/task-11-no-hardcoded-strings.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Message: (same as T9 commit)
  - Files: `web/src/lib/i18n/en.json`, `web/src/lib/i18n/zh.json`, `web/src/routes/*.svelte`
  - Pre-commit: `cd web && npm run build`

---

- [x] 12. **前端 UX 打磨（Toast 一致性 + 基础无障碍）**

  **What to do**:
  - Toast 一致性:
    - 在 Cameras.svelte, RecordingDetail.svelte, Stats.svelte, Settings.svelte 中统一使用 Toast 组件
    - 操作成功 → green toast, 操作失败 → red toast, 网络错误 → orange toast
  - 基础无障碍:
    - 导航链接添加 `aria-label`
    - 表单验证错误添加 `aria-live="polite"`
    - 汉堡按钮添加 `aria-expanded` 属性
    - 加载状态添加 `aria-busy="true"`
  - 确保所有修改使用 CSS 变量（与 T9 主题系统对齐）

  **Must NOT do**:
  - 不引入新组件库
  - 不改变 Toast 组件 API
  - 不做大规模布局变更

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`/frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T9-T11)
  - **Blocks**: T13
  - **Blocked By**: T1

  **References**:
  - `web/src/components/Toast.svelte` — 现有 Toast 组件，理解其 API
  - `web/src/routes/Cameras.svelte` — 使用 inline error divs 而非 Toast，需统一
  - `web/src/routes/Settings.svelte` — 同上
  - `web/src/components/Header.svelte` — 导航链接需添加 aria-label

  **Acceptance Criteria**:
  - [ ] 所有用户操作反馈使用 Toast（无 inline error divs）
  - [ ] 导航链接有 `aria-label`
  - [ ] 表单错误有 `aria-live`
  - [ ] `cd web && npm run build` 成功

  **QA Scenarios:**
  ```
  Scenario: Toast notifications work
    Tool: Playwright
    Steps:
      1. Login to NVR
      2. Go to Cameras page
      3. Add a camera with invalid data
      4. Verify: red toast appears with error message
      5. Add a camera with valid data
      6. Verify: green toast appears with success message
    Expected Result: Toast notifications appear for both success and failure
    Evidence: .sisyphus/evidence/task-12-toast-success.png, task-12-toast-error.png

  Scenario: Accessibility attributes present
    Tool: Bash
    Steps:
      1. rg 'aria-label' web/src/components/Header.svelte --count
      2. rg 'aria-live' web/src/routes/ --count
    Expected Result: Both have > 0 matches
    Evidence: .sisyphus/evidence/task-12-a11y.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Message: (same as T9 commit)
  - Files: `web/src/routes/*.svelte`, `web/src/components/Header.svelte`
  - Pre-commit: `cd web && npm run build`

---

- [x] 13. **修复 Makefile + 添加部署自动化目标**

  **What to do**:
  - 修复 `install` 目标: 先停止服务 → 备份旧二进制 → 复制新二进制 → 重启服务
  - 添加 `frontend` 目标: `cd web && npm run build && cp -r dist/* ../internal/ui/static/`
  - 添加 `deploy` 目标: `frontend → cross → scp → ssh restart`
    - 使用 `scp mibee-nvr-arm64 mickey@192.168.63.31:/tmp/`
    - 使用 `ssh mickey@192.168.63.31 "sudo ..."` 执行远程命令
  - 添加 `rollback` 目标: 恢复备份二进制并重启服务
  - 添加 `deploy-check` 目标: 验证远程服务状态和健康检查
  - 修改 `build` 和 `cross` 目标依赖 `frontend`（先构建前端）

  **Must NOT do**:
  - 不引入 Ansible/Capistrano 等工具
  - 不重写整个 Makefile
  - 不添加 CI/CD 配置

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (depends on all prior tasks)
  - **Parallel Group**: Wave 3 (sequential)
  - **Blocks**: T14
  - **Blocked By**: T2-T12

  **References**:
  - `Makefile` — 当前所有目标，理解现有结构
  - `web/package.json` — `npm run build` 命令，前端构建脚本
  - `internal/ui/embed.go` — //go:embed static/*，理解前端如何嵌入
  - `deploy/mibee-nvr.service` — 服务配置，理解 ExecStart 路径
  - `docs/en/deployment.md` — 部署文档，理解目录结构和流程

  **Acceptance Criteria**:
  - [ ] `make deploy` 执行完整流程: 前端构建 → 交叉编译 → scp → 远程重启
  - [ ] `make install` 先停止服务再复制
  - [ ] `make rollback` 恢复备份二进制
  - [ ] `make deploy-check` 验证远程服务健康

  **QA Scenarios:**
  ```
  Scenario: Deploy to test RPi
    Tool: Bash
    Steps:
      1. make deploy
      2. ssh mickey@192.168.63.31 "systemctl is-active mibee-nvr"
      3. curl -sf http://192.168.63.31:9090/api/health | jq .status
    Expected Result: Service active, health returns "ok"
    Evidence: .sisyphus/evidence/task-13-deploy.txt

  Scenario: Rollback works after failed deploy
    Tool: Bash
    Steps:
      1. make rollback
      2. ssh mickey@192.168.63.31 "systemctl is-active mibee-nvr"
    Expected Result: Service restored to previous version
    Evidence: .sisyphus/evidence/task-13-rollback.txt
  ```

  **Commit**: YES (groups with Wave 3)
  - Message: `feat(ops): safe Makefile deploy, DB backup endpoint`
  - Files: `Makefile`
  - Pre-commit: `make build`

---

- [x] 14. **构建、部署到 RPi、验证部署**

  **What to do**:
  - 执行完整构建流程: `make deploy`（前端 → 后端 → 传输 → 重启）
  - 验证远程服务启动成功
  - 验证 API 健康检查
  - 验证前端页面可访问
  - 验证认证保护生效
  - 如果部署失败: 排查问题并修复（可能需要调整 Makefile 或配置）
  - 如果需要配置文件: 通过 scp 传输 config.example.yaml 并调整

  **Must NOT do**:
  - 不修改源代码（此任务仅构建和部署）
  - 如果发现 bug，回到对应任务修复后重新部署

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (sequential, after T13)
  - **Blocks**: T15
  - **Blocked By**: T13

  **References**:
  - `Makefile` — deploy 目标，理解部署流程
  - `config.example.yaml` — 配置模板，可能需要调整后传输
  - `.sisyphus/evidence/task-1-env-check.txt` — T1 收集的环境信息，参考目标状态

  **Acceptance Criteria**:
  - [ ] `ssh mickey@192.168.63.31 "systemctl is-active mibee-nvr"` → active
  - [ ] `curl -sf http://192.168.63.31:9090/api/health` → {"status":"ok"}
  - [ ] `curl -sf http://192.168.63.31:9090/` → 401 (需认证)
  - [ ] 前端页面可通过浏览器访问（带认证）

  **QA Scenarios:**
  ```
  Scenario: Full deployment successful
    Tool: Bash (ssh)
    Steps:
      1. make deploy 2>&1 | tee /tmp/deploy.log
      2. ssh mickey@192.168.63.31 "systemctl is-active mibee-nvr"
      3. curl -sf http://192.168.63.31:9090/api/health
      4. curl -sf http://192.168.63.31:9090/api/stats
    Expected Result: Service active, API responding, stats show camera count
    Evidence: .sisyphus/evidence/task-14-full-deploy.txt

  Scenario: Auth protection active on RPi
    Tool: Bash
    Steps:
      1. curl -sf http://192.168.63.31:9090/ -o /dev/null -w "%{http_code}"
      2. curl -sf http://192.168.63.31:9090/api/recordings -o /dev/null -w "%{http_code}"
    Expected Result: Both return 401
    Evidence: .sisyphus/evidence/task-14-auth-on-rpi.txt
  ```

  **Commit**: NO (build/deploy task, no code changes)

---

- [x] 15. **RPi 端到端测试 + 问题修复**

  **What to do**:
  - 在 RPi 上执行完整的端到端测试:
    - 登录功能（正确/错误密码）
    - 录像列表查看
    - 摄像头管理（添加/编辑/删除）
    - 设置页面（查看/保存）
    - 统计页面
    - 主题切换（暗/亮）实际生效
    - 语言切换（中/英）无遗漏
    - 移动端视口（汉堡菜单）
    - DB 备份端点
    - 安全头检查
    - 速率限制检查
  - 发现的问题立即修复（小型修复就地处理）
  - 修复后重新 make deploy 验证
  - 所有测试结果记录到 evidence 目录

  **Must NOT do**:
  - 不做大规模重构
  - 修复范围仅限于测试发现的具体问题

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: [`/playwright`]
    - `/playwright`: 浏览器自动化测试

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (sequential, after T14)
  - **Blocks**: F1-F4
  - **Blocked By**: T14

  **References**:
  - `.sisyphus/evidence/task-14-full-deploy.txt` — T14 部署结果，确认服务状态
  - `docs/en/api-reference.md` — API 文档，参考测试端点
  - 所有之前任务的 QA Scenarios — 逐一在 RPi 上执行验证

  **Acceptance Criteria**:
  - [ ] 所有认证场景通过（正确密码登录、错误密码拒绝、速率限制）
  - [ ] 前端主题切换实际生效（截图对比）
  - [ ] 前端语言切换无遗漏字符串
  - [ ] 移动端导航可用（汉堡菜单）
  - [ ] DB 备份端点返回有效备份文件
  - [ ] 安全头存在于所有响应
  - [ ] hash-password CLI 命令可用
  - [ ] 服务稳定运行 30+ 分钟

  **QA Scenarios:**
  ```
  Scenario: Full E2E test suite on RPi
    Tool: Playwright + Bash (ssh/curl)
    Steps:
      1. Open browser to http://192.168.63.31:9090/
      2. Verify: redirected to login (401 protection)
      3. Login with credentials
      4. Navigate: Recordings → Cameras → Stats → Settings
      5. Switch theme: dark → light → dark (screenshot each)
      6. Switch language: zh → en → zh (verify no missing strings)
      7. Resize to 375px: verify hamburger menu works
      8. API tests: curl /api/backup, /api/health, /api/stats
      9. Security: verify headers, rate limiting
      10. Wait 30 min: verify service still active
    Expected Result: All 10 steps pass without errors
    Evidence: .sisyphus/evidence/task-15-e2e-test-suite/
  ```

  **Commit**: YES (if fixes were needed)
  - Message: `fix: e2e test fixes on RPi`
  - Files: (depends on what was found)
  - Pre-commit: `go test ./... && cd web && npm run build`

---
## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `go vet ./...` + `go test ./...` + `cd web && npm run build`. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA on RPi** — `unspecified-high` (+ `playwright` skill if UI)
  SSH into 192.168.63.31. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-feature integration (theme + i18n + mobile nav). Test edge cases: empty state, login failure, camera disconnect. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff (git log/diff). Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **Wave 0**: No commit (validation only)
- **Wave 1 Backend**: Single commit `fix(security): production hardening — auth, panic recovery, shutdown, config`
- **Wave 2 Frontend**: Single commit `feat(ui): theme system, mobile nav, i18n gaps, UX polish`
- **Wave 3 Deploy**: Single commit `feat(ops): safe Makefile deploy, DB backup endpoint`
- Pre-commit: `go vet ./... && go test ./...`

## Success Criteria

### Verification Commands
```bash
# Backend health
ssh mickey@192.168.63.31 "systemctl is-active mibee-nvr"  # Expected: active
curl -sf http://192.168.63.31:9090/api/health | jq .status  # Expected: "ok"

# Auth protection
curl -sf http://192.168.63.31:9090/ | head -5  # Expected: 401 or redirect to login
curl -sf http://192.168.63.31:9090/api/recordings | jq .  # Expected: 401

# Hash-password CLI
ssh mickey@192.168.63.31 "/mnt/data/nvr/bin/mibee-nvr hash-password test123"  # Expected: bcrypt hash

# Theme system
curl -sf http://192.168.63.31:9090/ | grep 'data-theme'  # Expected: found

# No hardcoded color classes in frontend
rg 'bg-slate-900|bg-slate-800|text-slate-100' web/src/routes/ --count  # Expected: 0

# i18n key parity
diff <(jq -r 'keys[]' web/src/lib/i18n/en.json | sort) <(jq -r 'keys[]' web/src/lib/i18n/zh.json | sort)  # Expected: no output

# Config safety
grep -n '10m' internal/config/config.go  # Expected: 0 matches in defaults

# Go tests pass
go test ./... -count=1  # Expected: all PASS
```

### Final Checklist
- [x] All "Must Have" present and verified on RPi
- [x] All "Must NOT Have" absent (grep verification)
- [x] All go tests pass
- [x] Frontend builds without errors
- [x] Theme toggle works (dark/light switch visible)
- [x] Mobile nav works (hamburger menu at 375px)
- [x] i18n 100% coverage (no hardcoded UI strings)
- [x] Auth protects all routes (including static files)
- [x] Service runs stable on RPi for 30+ minutes
