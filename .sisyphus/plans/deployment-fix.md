# 部署体验全面改进

## TL;DR

> **Quick Summary**: 修复 MiBee NVR 部署体验中的 10 个问题，使其对技术新手友好。包括：修复明文密码 BUG、添加 init 子命令、创建一键安装脚本、添加 docker-compose、统一 systemd 服务、添加版本注入、完善全部文档（含中文）。
> 
> **Deliverables**:
> - 明文密码 `auth.password` 自动转哈希（修复文档承诺但未实现的 BUG）
> - `mibee-nvr init` 子命令（交互式/非交互式初始化）
> - `mibee-nvr health` 子命令（用于 Docker HEALTHCHECK）
> - `install.sh` 一键安装脚本
> - `docker-compose.yml` 完整编排
> - 统一的 systemd 服务文件模板
> - release.yml 版本注入（`-ldflags`）
> - `.go-version` + `.nvmrc` 版本锁定
> - 完整更新的 EN + ZH 文档（getting-started、deployment、configuration、README）
> - 更新的 `config.example.yaml`（通用 Linux 默认路径）
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: Task 1 (password fix) → Task 2 (init cmd) → Task 9 (config example) → Task 10-13 (docs)

---

## Context

### Original Request
用户要求全面改进 MiBee NVR 的部署体验，面向通用 Linux 平台（同时保持 RPi 3B 轻量），特别重视文档完善。所有 10 个已识别的部署问题都需要修复。

### Interview Summary
**Key Discussions**:
- 目标平台：通用 Linux，作者日常用 RPi 3B 部署需保持轻量
- 测试策略：Tests after — 实现完成后补充关键测试
- 中文文档：`docs/zh/` 目录存在 5 个文件，必须与 EN 同步更新
- 默认路径：需从 `/mnt/data/nvr`（作者特定）改为通用路径

**Research Findings**:
- GitHub Releases 已有 v0.1.0、v0.2.0，含 amd64/arm64 二进制
- `auth.password` 明文密码支持在 `configuration.md` 中已承诺但代码未实现 — 这是一个 BUG
- `appVersion` 硬编码为 `"0.1.0-dev"`，release.yml 未注入版本号
- Docker HEALTHCHECK 在 distroless/scratch 基础镜像中无法使用 curl/wget
- `deploy/mibee-nvr.service` 与 `docs/en/deployment.md` 中的服务文件内容不一致
- `config.go:76` 定义了 `Password string` 字段但 `main.go:120` 只使用 `PasswordHash`

### Metis Review
**Identified Gaps** (addressed):
- 明文密码是 BUG 不是新功能：优先修复
- Docker HEALTHCHECK 需要二进制自身支持 health 子命令
- 版本号注入缺失影响用户体验
- 中文文档必须同步更新
- `init` 命令需支持非交互模式（flags）
- `install.sh` 需处理架构映射（`aarch64`→`arm64`、`x86_64`→`amd64`）
- `install.sh` 需要检测 root 权限
- 空密码 vs 未设置密码需区分处理
- 默认路径变更需向后兼容

---

## Work Objectives

### Core Objective
让一个技术新手能在 10 分钟内（使用预编译二进制）或 30 分钟内（从 Docker）完成 MiBee NVR 部署并开始录制。

### Concrete Deliverables
- 修复后的 `internal/middleware/auth.go` — 支持明文密码自动转哈希
- 新增 `init` 和 `health` 子命令到 `cmd/mibee-nvr/main.go`
- 新文件 `install.sh`（项目根目录）
- 新文件 `docker-compose.yml`（项目根目录）
- 更新 `.github/workflows/release.yml`（版本注入）
- 统一的 `deploy/mibee-nvr.service`
- 新文件 `.go-version` 和 `.nvmrc`
- 更新的 `config.example.yaml`
- 更新的全部文档（EN + ZH 共 10 个文件）

### Definition of Done
- [x] `mibee-nvr init --password test123 --data-dir /tmp/test-nvr-init` → exits 0
- [x] `mibee-nvr -config /tmp/test-nvr-init/mibee-nvr.yaml` 启动后 `curl -u admin:test123 http://localhost:9090/api/health` 返回 200
- [x] 配置 `auth.password: "plaintext"` + 空 `password_hash` → 服务启动且密码可用
- [x] `./install.sh` 检测架构、下载二进制、创建用户、安装服务
- [x] `docker-compose up` → 容器启动，健康检查通过
- [x] `mibee-nvr -version` 显示实际版本（非 `0.1.0-dev`）
- [x] `make build` 和 `make cross` 仍正常工作
- [x] 全部 `go test ./...` 通过
- [x] 所有 EN/ZH 文档内容一致且准确

### Must Have
- 明文密码自动转哈希（修复 BUG）
- init 子命令（交互式 + flags 双模式）
- install.sh 一键安装
- docker-compose.yml（含 FTP 端口暴露）
- 版本号注入
- README 添加 Release 下载链接
- EN/ZH 文档同步更新

### Must NOT Have (Guardrails)
- 不改变 config YAML 的现有 schema（向后兼容）
- 不改变 Makefile 的现有 targets（可添加新的）
- 不改变 Dockerfile 的基础镜像（distroless/scratch）
- 不修改 release.yml 的触发条件
- 不添加新的 Go 依赖
- 不移除 `hash-password` 子命令
- 不在 Docker 镜像中添加 curl/wget
- 不实现 ONVIF 扫描或摄像头发现（超出范围）
- 不添加自动更新机制
- 不支持非 systemd 的 init 系统（文档说明即可）

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (Go testify + Playwright E2E)
- **Automated tests**: Tests-after — 实现完成后补充关键测试
- **Framework**: Go testing + testify (existing)

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Backend/Go**: Use Bash — build, run, curl, check exit codes
- **Shell scripts**: Use Bash — run with args, check output, check file creation
- **Docker**: Use Bash — docker-compose up, curl health, check logs
- **Documentation**: Use Bash — check links, verify file references, check consistency

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — backend fixes, no cross-dependency):
├── Task 1: Fix plaintext password auto-hash (BUG) [quick]
├── Task 2: Add `init` subcommand [unspecified-high]
├── Task 3: Add `health` subcommand [quick]
├── Task 4: Fix version injection in release.yml [quick]
└── Task 5: Add .go-version + .nvmrc [quick]

Wave 2 (After Wave 1 — infrastructure, depends on Task 2+3):
├── Task 6: Create install.sh (depends: 2, 4) [unspecified-high]
├── Task 7: Create docker-compose.yml (depends: 3) [quick]
├── Task 8: Add Dockerfile HEALTHCHECK (depends: 3) [quick]
└── Task 9: Unify systemd service + update config.example.yaml (depends: 2) [quick]

Wave 3 (After Wave 2 — documentation, depends on all above):
├── Task 10: Update getting-started.md (EN+ZH) (depends: 1, 2, 6, 7) [writing]
├── Task 11: Update deployment.md (EN+ZH) (depends: 6, 8, 9) [writing]
├── Task 12: Update configuration.md (EN+ZH) (depends: 1, 2, 9) [writing]
└── Task 13: Update README.md + README.zh.md (depends: 10, 11, 12) [writing]

Wave 4 (After Wave 3 — tests + integration):
├── Task 14: Add integration tests (depends: 1, 2, 3) [unspecified-high]
└── Task 15: Full end-to-end deployment verification (depends: all) [deep]

Wave FINAL (After ALL tasks — 4 parallel reviews):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality review (unspecified-high)
├── Task F3: Real manual QA (unspecified-high)
└── Task F4: Scope fidelity check (deep)
-> Present results -> Get explicit user okay

Critical Path: Task 1 → Task 2 → Task 9 → Task 10-13 → Task 15 → F1-F4
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 5 (Wave 1)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| 1 | - | 10, 12, 14 | 1 |
| 2 | - | 6, 9, 10, 12, 14 | 1 |
| 3 | - | 7, 8, 14 | 1 |
| 4 | - | 6, 13 | 1 |
| 5 | - | - | 1 |
| 6 | 2, 4 | 10, 11, 13 | 2 |
| 7 | 3 | 10, 13 | 2 |
| 8 | 3 | 11 | 2 |
| 9 | 2 | 10, 11, 12 | 2 |
| 10 | 1, 2, 6, 7 | 13 | 3 |
| 11 | 6, 8, 9 | 13 | 3 |
| 12 | 1, 2, 9 | 13 | 3 |
| 13 | 10, 11, 12 | 15 | 3 |
| 14 | 1, 2, 3 | 15 | 4 |
| 15 | 13, 14 | F1-F4 | 4 |

### Agent Dispatch Summary

- **Wave 1**: 5 — T1 `quick`, T2 `unspecified-high`, T3 `quick`, T4 `quick`, T5 `quick`
- **Wave 2**: 4 — T6 `unspecified-high`, T7 `quick`, T8 `quick`, T9 `quick`
- **Wave 3**: 4 — T10 `writing`, T11 `writing`, T12 `writing`, T13 `writing`
- **Wave 4**: 2 — T14 `unspecified-high`, T15 `deep`
- **FINAL**: 4 — F1 `oracle`, F2 `unspecified-high`, F3 `unspecified-high`, F4 `deep`

---

## TODOs

- [x] 1. Fix plaintext password auto-hash (BUG FIX)

  **What to do**:
  - In `internal/middleware/auth.go`: 修改 `NewAuthMiddleware()` 使其同时接受 `password` 和 `passwordHash` 参数
  - 如果 `passwordHash` 为空但 `password` 非空：用 `HashPassword()` 生成哈希，然后写回配置文件（调用 `config.Save()`）
  - 在 `cmd/mibee-nvr/main.go:120` 传入 `cfg.Auth.Password` 参数
  - 确保 FTP 也使用相同的凭据（`main.go:244` 已经使用 `cfg.Auth.Password`，保持不变）
  - 添加日志：当检测到明文密码自动转哈希时，打印 info 日志
  - **向后兼容**：如果 `passwordHash` 非空，忽略 `password` 字段（现有行为不变）

  **Must NOT do**:
  - 不添加新的 Go 依赖
  - 不改变配置文件 schema（`password` 和 `password_hash` 字段均已存在）
  - 不移除 `hash-password` 子命令
  - 不强制密码策略（长度、复杂度）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 范围明确，修改 2-3 个文件的已知位置
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3, 4, 5)
  - **Blocks**: Tasks 10, 12, 14
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/middleware/auth.go` — `NewAuthMiddleware()` 构造函数，当前只接受 username + passwordHash
  - `internal/middleware/auth.go:HashPassword()` — bcrypt 哈希生成函数（已存在）
  - `internal/config/config.go:73-77` — `AuthConfig` 结构体，已有 `Password` 和 `PasswordHash` 两个字段
  - `internal/config/config.go:129-161` — `config.Save()` 原子写入模式（temp+rename）
  - `cmd/mibee-nvr/main.go:120` — `authMW := authmw.NewAuthMiddleware(cfg.Auth.Username, cfg.Auth.PasswordHash)` 需增加 password 参数
  - `docs/en/configuration.md` — 文档中已承诺 `auth.password` 明文密码支持但代码未实现

  **WHY Each Reference Matters**:
  - `auth.go` 的 `NewAuthMiddleware` 是唯一需要修改签名的函数，需要增加 password 参数
  - `config.Save()` 是将自动生成的哈希写回配置文件的正确方式
  - `main.go:120` 是调用点，需要传递新参数

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Plaintext password auto-hash
    Tool: Bash
    Preconditions: Fresh build of mibee-nvr
    Steps:
      1. Create test config with auth.password: "test123" and password_hash: ""
      2. Start mibee-nvr with this config on port 19990
      3. Run: curl -s -o /dev/null -w "%{http_code}" -u admin:test123 http://localhost:19990/api/health
      4. Check that config file now contains non-empty password_hash
      5. Stop the server
      6. Start again — verify auth still works with test123
    Expected Result: curl returns 200, config file updated with hash
    Failure Indicators: 401 response, config file unchanged, server crash
    Evidence: .sisyphus/evidence/task-1-plaintext-password.txt

  Scenario: Hash takes precedence over plaintext
    Tool: Bash
    Preconditions: Config with both password and password_hash set
    Steps:
      1. Create config with auth.password: "wrong" and password_hash set to hash of "correct"
      2. Start mibee-nvr
      3. curl -u admin:correct http://localhost:19991/api/health
      4. curl -u admin:wrong http://localhost:19991/api/health
    Expected Result: "correct" returns 200, "wrong" returns 401
    Evidence: .sisyphus/evidence/task-1-hash-precedence.txt

  Scenario: Empty both password and hash
    Tool: Bash
    Preconditions: Config with auth.password: "" and password_hash: ""
    Steps:
      1. Start mibee-nvr
      2. Attempt curl without auth: curl http://localhost:19992/api/health
      3. Attempt curl with any auth: curl -u admin:anything http://localhost:19992/api/health
    Expected Result: Both return 401 (no auth accepted when no password configured)
    Evidence: .sisyphus/evidence/task-1-empty-password.txt
  ```

  **Commit**: YES
  - Message: `fix(auth): support plaintext password auto-hash`
  - Files: `internal/middleware/auth.go`, `cmd/mibee-nvr/main.go`
  - Pre-commit: `go test ./internal/middleware/... ./cmd/... -v`

- [x] 2. Add `init` subcommand

  **What to do**:
  - 在 `cmd/mibee-nvr/main.go` 中添加 `init` 子命令（跟随 `hash-password` 的 os.Args 模式）
  - 支持两种模式：
    - **非交互式**: `mibee-nvr init --password <pw> --data-dir <path> [--listen :9090] [--config <path>]`
    - **交互式**: `mibee-nvr init`（无 flags）→ 提示输入密码、数据目录等
  - `init` 执行流程：
    1. 检查配置文件是否已存在 → 存在则提示覆盖或退出
    2. 询问/获取密码 → 调用 `HashPassword()` 生成哈希
    3. 询问/获取数据目录 → 创建目录（如果不存在）
    4. 生成完整的配置文件 → 调用 `config.Save()` 写入
    5. 打印下一步操作指引（如何启动、如何添加摄像头）
  - 默认值：
    - `data-dir`: `/var/lib/mibee-nvr`
    - `listen`: `:9090`
    - `config`: `mibee-nvr.yaml`（当前目录）
    - `username`: `admin`
  - 退出码：0=成功, 1=错误, 2=已存在（非交互模式下）

  **Must NOT do**:
  - 不添加 CLI 框架库（如 cobra）— 使用 os.Args 直检查
  - 不自动启动服务
  - 不自动下载或安装任何东西
  - 不执行摄像头扫描/发现
  - 不交互式编辑 YAML（只生成完整的配置文件）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 需要设计交互式 CLI 流程、处理多种输入模式、生成配置文件，代码量中等
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3, 4, 5)
  - **Blocks**: Tasks 6, 9, 10, 12, 14
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `cmd/mibee-nvr/main.go:46-58` — `hash-password` 子命令模式：`if len(os.Args) > 1 && os.Args[1] == "hash-password" { ... }`，init 应使用相同模式
  - `cmd/mibee-nvr/main.go:37-40` — flag 定义模式：`flag.String("config", ...)`
  - `internal/config/config.go:73-109` — 所有配置结构体定义（Config, ServerConfig, StorageConfig, AuthConfig 等）
  - `internal/config/config.go:129-161` — `config.Save(path, cfg)` 原子写入
  - `internal/config/config.go:245-350` — `applyDefaults()` 默认值参考
  - `internal/middleware/auth.go:HashPassword()` — 密码哈希生成

  **WHY Each Reference Matters**:
  - `hash-password` 子命令是 init 的实现模式参考
  - `config.Save()` 是写配置文件的安全方式（原子写入）
  - `applyDefaults()` 提供所有默认值，init 生成的配置应包含这些默认值

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Non-interactive init
    Tool: Bash
    Preconditions: Clean /tmp/nvr-init-test directory
    Steps:
      1. Run: ./mibee-nvr init --password test123 --data-dir /tmp/nvr-init-test
      2. Check exit code is 0
      3. Verify file exists: ls /tmp/nvr-init-test/mibee-nvr.yaml
      4. Verify config contains: grep 'password_hash' mibee-nvr.yaml (should be non-empty bcrypt hash)
      5. Verify config contains: grep '/tmp/nvr-init-test' mibee-nvr.yaml
      6. Verify directory was created: ls -la /tmp/nvr-init-test/
    Expected Result: Exit 0, config file exists with hashed password and correct data dir
    Failure Indicators: Exit non-0, no config file, empty password_hash
    Evidence: .sisyphus/evidence/task-2-init-noninteractive.txt

  Scenario: Init creates valid runnable config
    Tool: Bash
    Preconditions: Init completed in previous scenario
    Steps:
      1. Start: ./mibee-nvr -config mibee-nvr.yaml &
      2. Sleep 2 seconds
      3. curl -s -o /dev/null -w "%{http_code}" -u admin:test123 http://localhost:9090/api/health
      4. Kill the server process
    Expected Result: Health check returns 200
    Failure Indicators: 401, connection refused, server crash
    Evidence: .sisyphus/evidence/task-2-init-runnable.txt

  Scenario: Init rejects overwrite without flag
    Tool: Bash
    Preconditions: Config file already exists from previous init
    Steps:
      1. Run: ./mibee-nvr init --password test123 --data-dir /tmp/nvr-init-test
      2. Check exit code is 2 (already exists)
    Expected Result: Exit code 2, original config preserved
    Failure Indicators: Exit 0 (overwrote silently), config changed
    Evidence: .sisyphus/evidence/task-2-init-no-overwrite.txt
  ```

  **Commit**: YES
  - Message: `feat(cli): add init subcommand for first-time setup`
  - Files: `cmd/mibee-nvr/main.go`
  - Pre-commit: `go build ./cmd/mibee-nvr/ && go test ./... -v`

- [x] 3. Add `health` subcommand

  **What to do**:
  - 在 `cmd/mibee-nvr/main.go` 中添加 `health` 子命令
  - 实现：向 `server.listen` 地址发送 HTTP GET `/api/health`
  - 退出码：0=健康, 1=不健康（连接失败或非 200 响应）
  - 用法：`mibee-nvr health [--config mibee-nvr.yaml]` 或 `mibee-nvr health --addr :9090`
  - 从配置文件读取 listen 地址（如提供 config），否则默认 `:9090`
  - 使用 Go stdlib `net/http` 发送请求（不添加依赖）
  - 这是 Docker HEALTHCHECK 的基础：`HEALTHCHECK CMD ["mibee-nvr", "health"]`

  **Must NOT do**:
  - 不添加 curl/wget 依赖
  - 不启动 HTTP 服务器
  - 不依赖配置文件必须存在（支持 --addr 直接指定）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: <30 行新代码，明确的输入输出
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 4, 5)
  - **Blocks**: Tasks 7, 8, 14
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `cmd/mibee-nvr/main.go:46-58` — 子命令模式（hash-password）
  - `cmd/mibee-nvr/main.go:37-38` — flag 定义：`configPath = flag.String("config", ...)`
  - `internal/api/handler.go` — 搜索 `/api/health` 端点定义，确认返回格式

  **WHY Each Reference Matters**:
  - hash-password 子命令是 health 子命令的实现模式
  - 需要知道 health 端点的实际 URL 和预期响应格式

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Health check against running server
    Tool: Bash
    Preconditions: mibee-nvr running on port 19995
    Steps:
      1. Run: ./mibee-nvr health --addr :19995
      2. Check exit code
    Expected Result: Exit 0
    Evidence: .sisyphus/evidence/task-3-health-running.txt

  Scenario: Health check against stopped server
    Tool: Bash
    Preconditions: No server running on port 19996
    Steps:
      1. Run: ./mibee-nvr health --addr :19996
      2. Check exit code
    Expected Result: Exit 1 (connection refused)
    Evidence: .sisyphus/evidence/task-3-health-stopped.txt
  ```

  **Commit**: YES
  - Message: `feat(cli): add health subcommand for Docker HEALTHCHECK`
  - Files: `cmd/mibee-nvr/main.go`
  - Pre-commit: `go build ./cmd/mibee-nvr/`

- [x] 4. Fix version injection in release.yml

  **What to do**:
  - 修改 `.github/workflows/release.yml` 的 Build binaries 步骤
  - 将 `appVersion` 从硬编码常量改为可通过 `-ldflags` 注入的变量
  - 在 `cmd/mibee-nvr/main.go` 中将 `const appVersion = "0.1.0-dev"` 改为 `var appVersion = "0.1.0-dev"`
  - release.yml 的 go build 命令添加：`-ldflags="-s -w -X main.appVersion=${GITHUB_REF_NAME}"`
  - 本地 `make build` 和 `make cross` 不传 ldflags，保持 `0.1.0-dev`（开发构建标识）
  - Makefile 可选择性添加 `VERSION` 变量支持（可选）

  **Must NOT do**:
  - 不改变 release 触发条件
  - 不改变二进制文件名
  - 不添加新的 CI 步骤

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 2 个文件各改 1-2 行
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 5)
  - **Blocks**: Tasks 6, 13
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `cmd/mibee-nvr/main.go:42` — `const appVersion = "0.1.0-dev"` → 改为 `var`
  - `.github/workflows/release.yml:31-33` — 当前 build 命令：`CGO_ENABLED=0 GOOS=linux ... go build -o mibee-nvr-...`
  - `Makefile:18` — `go build -ldflags="-s -w"` 本地构建

  **WHY Each Reference Matters**:
  - `main.go:42` 是要改的常量声明位置
  - `release.yml:31-33` 是要添加 ldflags 的构建命令
  - Makefile 保持不变确保本地开发体验不变

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Version injection works
    Tool: Bash
    Preconditions: Fresh build
    Steps:
      1. Build with version: CGO_ENABLED=0 go build -ldflags="-X main.appVersion=v0.3.0" -o /tmp/nvr-version-test ./cmd/mibee-nvr/
      2. Run: /tmp/nvr-version-test -version
      3. Check output contains "v0.3.0"
    Expected Result: Output shows "MiBee NVR version v0.3.0"
    Evidence: .sisyphus/evidence/task-4-version-injection.txt

  Scenario: Default version without ldflags
    Tool: Bash
    Steps:
      1. Run: ./mibee-nvr -version
      2. Check output contains "0.1.0-dev"
    Expected Result: Output shows "MiBee NVR version 0.1.0-dev"
    Evidence: .sisyphus/evidence/task-4-version-default.txt
  ```

  **Commit**: YES
  - Message: `fix(ci): inject version via ldflags in release workflow`
  - Files: `cmd/mibee-nvr/main.go`, `.github/workflows/release.yml`
  - Pre-commit: `go build ./cmd/mibee-nvr/`

- [x] 5. Add .go-version and .nvmrc

  **What to do**:
  - 创建 `.go-version` 文件，内容为 `1.26`
  - 创建 `.nvmrc` 文件，内容为 `22`
  - 这些文件帮助开发者使用正确的工具版本

  **Must NOT do**:
  - 不修改任何代码
  - 不修改 CI 配置（CI 已硬编码版本号）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 创建 2 个单行文件
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 4)
  - **Blocks**: None
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `go.mod` line 2 — `go 1.26` 确认 Go 版本
  - `web/package.json` — Node.js 依赖（无 engines 字段，从 Dockerfile 确认 Node 22）
  - `Dockerfile:2` — `FROM node:22-slim` 确认 Node 版本

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Version files exist and correct
    Tool: Bash
    Steps:
      1. cat .go-version → should output "1.26"
      2. cat .nvmrc → should output "22"
    Expected Result: Both files contain correct version numbers
    Evidence: .sisyphus/evidence/task-5-version-files.txt
  ```

  - [x] 5. Add .go-version and .nvmrc
  - Message: `chore: add .go-version and .nvmrc for version pinning`
  - Files: `.go-version`, `.nvmrc`
  - Pre-commit: none

- [x] 6. Create install.sh one-click installer

  **What to do**:
  - 创建 `install.sh` 项目根目录一键安装脚本
  - 脚本流程：
    1. 检查 root 权限（非 root 则退出并提示 sudo）
    2. 检测架构：`uname -m` → `aarch64`/`arm64` → `arm64`, `x86_64` → `amd64`
    3. 检查依赖：`curl` 或 `wget` 至少一个可用
    4. 从 GitHub Releases API 获取最新版本：`https://api.github.com/repos/Mi-Bee-Studio/MiBeeNvr/releases/latest`
    5. 下载对应架构的二进制文件
    6. 创建 `nvr` 系统用户（`useradd -r -s /bin/false -d /var/lib/mibee-nvr`），如果已存在则跳过
    7. 创建数据目录 `/var/lib/mibee-nvr`（如果不存在）
    8. 安装二进制到 `/usr/local/bin/mibee-nvr`
    9. 如果配置文件不存在，运行 `mibee-nvr init --password <prompt>`
    10. 安装 systemd 服务文件到 `/etc/systemd/system/mibee-nvr.service`
    11. 启用并启动服务
    12. 打印安装结果和访问 URL
  - 幂等性：重复运行不会覆盖已有配置或数据库
  - 支持 `--version <tag>` 指定版本（默认 latest）
  - 支持 `--uninstall` 卸载

  **Must NOT do**:
  - 不添加自动更新机制
  - 不支持非 systemd 系统（检测到非 systemd 时打印手动安装指引）
  - 不安装 Go/Node.js（只下载预编译二进制）
  - 不修改防火墙规则

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Shell 脚本逻辑较多（架构检测、API 调用、用户创建、服务安装），需要健壮的错误处理
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2
  - **Blocks**: Tasks 10, 11, 13
  - **Blocked By**: Tasks 2 (init cmd), 4 (version)

  **References**:

  **Pattern References**:
  - `deploy/mibee-nvr.service` — 现有 systemd 服务文件模板（需更新路径后使用）
  - `docs/en/deployment.md:117-134` — 用户创建和目录设置的命令参考
  - `config.example.yaml` — 初始配置模板
  - GitHub Releases API: `https://api.github.com/repos/Mi-Bee-Studio/MiBeeNvr/releases/latest`

  **WHY Each Reference Matters**:
  - `deploy/mibee-nvr.service` 是安装脚本要安装的服务文件，需要使用更新后的版本
  - deployment.md 的用户创建命令是经过验证的
  - GitHub API 是获取最新下载 URL 的唯一方式

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Install script syntax check
    Tool: Bash
    Steps:
      1. Run: bash -n install.sh
      2. Run: shellcheck install.sh (if available, non-blocking)
    Expected Result: No syntax errors
    Evidence: .sisyphus/evidence/task-6-syntax-check.txt

  Scenario: Install script architecture detection
    Tool: Bash
    Steps:
      1. Run: bash -c 'source install.sh; detect_arch' (or extract and test the function)
      2. Verify aarch64 → arm64 mapping
      3. Verify x86_64 → amd64 mapping
    Expected Result: Correct architecture mapping
    Evidence: .sisyphus/evidence/task-6-arch-detect.txt

  Scenario: Install script root check
    Tool: Bash
    Steps:
      1. Run: bash install.sh (as non-root user)
      2. Check exit code and error message
    Expected Result: Exit 1, clear error about needing root/sudo
    Evidence: .sisyphus/evidence/task-6-root-check.txt
  ```

  **Commit**: YES
  - Message: `feat(deploy): add one-click install.sh script`
  - Files: `install.sh`
  - Pre-commit: `bash -n install.sh`

- [x] 7. Create docker-compose.yml

  **What to do**:
  - 创建 `docker-compose.yml` 项目根目录
  - 内容：
    - 单服务 `mibee-nvr`
    - 镜像：使用 GHCR 或 Docker Hub 上的官方镜像（或本地构建）
    - 端口映射：`9090:9090`（HTTP）、`2121:2121`（FTP）
    - 卷挂载：`./data:/data`（配置 + 录像持久化）
    - 配置文件：需要先在 `./data/mibee-nvr.yaml` 创建配置
    - 健康检查：使用 `mibee-nvr health` 子命令
    - 重启策略：`unless-stopped`
    - 环境变量：无（使用配置文件）
  - 创建 `docker-compose.example.yml` 或在文件中详细注释
  - 在 README 中添加 Docker 快速启动指引

  **Must NOT do**:
  - 不添加 mediamtx/mqtt 等辅助服务（超出范围）
  - 不使用命名卷（使用 bind mount 更直观）
  - 不添加环境变量配置覆盖（使用配置文件）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 单文件创建，YAML 结构简单
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 6, 8, 9)
  - **Blocks**: Tasks 10, 13
  - **Blocked By**: Task 3 (health cmd)

  **References**:

  **Pattern References**:
  - `Dockerfile` — 现有 Docker 配置：暴露 9090、卷 /data、入口 mibee-nvr
  - `Dockerfile.arm64` — scratch 基础镜像版本
  - `docs/en/deployment.md:59-65` — Docker 运行示例（需完善）
  - `config.example.yaml` — 默认端口配置：9090, 2121, 2122-2140

  **WHY Each Reference Matters**:
  - Dockerfile 定义了容器内的端口和卷，docker-compose 需要映射这些
  - FTP 被动端口范围 (2122-2140) 是新手容易遗漏的关键配置

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Docker compose syntax valid
    Tool: Bash
    Steps:
      1. Run: docker-compose config -f docker-compose.yml (or docker compose config)
      2. Check for validation errors
    Expected Result: Valid YAML, no errors
    Evidence: .sisyphus/evidence/task-7-compose-syntax.txt

  Scenario: Docker compose has all required ports
    Tool: Bash
    Steps:
      1. grep '9090' docker-compose.yml
      2. grep '2121' docker-compose.yml
      3. grep 'health' docker-compose.yml
      4. grep '/data' docker-compose.yml
    Expected Result: All port and volume references found
    Evidence: .sisyphus/evidence/task-7-compose-ports.txt
  ```

  **Commit**: YES
  - Message: `feat(docker): add docker-compose.yml with full port exposure`
  - Files: `docker-compose.yml`
  - Pre-commit: `docker-compose config`

- [x] 8. Add HEALTHCHECK to Dockerfiles

  **What to do**:
  - 在 `Dockerfile`（distroless 基础）添加：`HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["mibee-nvr", "health"]`
  - 在 `Dockerfile.arm64`（scratch 基础）添加相同的 HEALTHCHECK 指令
  - 因为二进制自带 `health` 子命令（Task 3），无需 curl/wget

  **Must NOT do**:
  - 不添加 curl/wget 到镜像
  - 不改变基础镜像

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 每个文件加 1 行指令
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 6, 7, 9)
  - **Blocks**: Task 11
  - **Blocked By**: Task 3 (health cmd must exist first)

  **References**:

  **Pattern References**:
  - `Dockerfile:49-53` — 当前末尾：EXPOSE + ENTRYPOINT + CMD，HEALTHCHECK 应插在 EXPOSE 后
  - `Dockerfile.arm64:9-12` — scratch 版本，同上

  **WHY Each Reference Matters**:
  - HEALTHCHECK 指令的插入位置需要在 EXPOSE 之后、ENTRYPOINT 之前

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Dockerfiles contain HEALTHCHECK
    Tool: Bash
    Steps:
      1. grep 'HEALTHCHECK' Dockerfile → should find match
      2. grep 'HEALTHCHECK' Dockerfile.arm64 → should find match
      3. grep 'mibee-nvr.*health' Dockerfile Dockerfile.arm64 → should reference health subcommand
    Expected Result: Both files contain HEALTHCHECK using mibee-nvr health
    Evidence: .sisyphus/evidence/task-8-healthcheck.txt
  ```

  **Commit**: YES
  - Message: `feat(docker): add HEALTHCHECK to Dockerfiles`
  - Files: `Dockerfile`, `Dockerfile.arm64`
  - Pre-commit: `docker build --check .` (or visual verification)

- [x] 9. Unify systemd service + update config.example.yaml

  **What to do**:
  - 更新 `deploy/mibee-nvr.service`：
    - 路径改为通用：`ExecStart=/usr/local/bin/mibee-nvr`、`WorkingDirectory=/var/lib/mibee-nvr`
    - 保留 `docs/en/deployment.md` 中的安全加固选项（`NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict` 等）
    - `ReadWritePaths=/var/lib/mibee-nvr`（匹配新默认路径）
    - `MemoryMax` 注释掉并说明 RPi 用户可取消注释
    - 添加注释说明如何自定义路径
  - 更新 `config.example.yaml`：
    - `storage.root_dir` 默认改为 `/var/lib/mibee-nvr`
    - 摄像头示例简化为 1-2 个典型示例
    - 添加 `auth.password` 字段说明（明文密码支持）
    - 所有默认值与 `applyDefaults()` 一致
    - 添加中文和英文注释说明每个配置项
  - 更新 `internal/config/config.go:252` — `applyDefaults()` 中的 `RootDir` 默认值从 `/mnt/data/nvr` 改为 `/var/lib/mibee-nvr`
  - 更新 Makefile `install` target 路径（如果保持与 install.sh 一致）

  **Must NOT do**:
  - 不破坏现有用户的配置（如果配置文件指定了路径，使用配置文件值）
  - 不删除 Makefile 中的 RPi deploy targets（仅更新 install target）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 更新 3 个配置文件中的值
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 6, 7, 8)
  - **Blocks**: Tasks 10, 11, 12
  - **Blocked By**: Task 2 (init cmd determines default paths)

  **References**:

  **Pattern References**:
  - `deploy/mibee-nvr.service` — 当前服务文件（硬编码 /mnt/data/nvr）
  - `docs/en/deployment.md:88-114` — 完整版服务文件（含安全加固）
  - `config.example.yaml` — 当前配置示例
  - `internal/config/config.go:251-253` — `applyDefaults()` 中的 RootDir 默认值
  - `Makefile:34-35` — `install` target: `mkdir -p /mnt/data/nvr/bin`

  **WHY Each Reference Matters**:
  - 两个服务文件不一致，需要合并为统一的、参数化的版本
  - `applyDefaults()` 中的硬编码路径必须与 config.example.yaml 一致
  - Makefile install target 需要匹配新路径

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Service file uses generic paths
    Tool: Bash
    Steps:
      1. grep '/var/lib/mibee-nvr' deploy/mibee-nvr.service → should match WorkingDirectory and ReadWritePaths
      2. grep '/usr/local/bin' deploy/mibee-nvr.service → should match ExecStart
      3. grep 'ProtectSystem=strict' deploy/mibee-nvr.service → should match
    Expected Result: All paths use generic Linux locations
    Evidence: .sisyphus/evidence/task-9-service-paths.txt

  Scenario: Config example has correct defaults
    Tool: Bash
    Steps:
      1. grep 'root_dir' config.example.yaml → should show /var/lib/mibee-nvr
      2. Verify config.example.yaml has auth.password field commented
    Expected Result: Default path and auth fields are correct
    Evidence: .sisyphus/evidence/task-9-config-defaults.txt
  ```

  **Commit**: YES
  - Message: `fix(deploy): unify systemd service and update config defaults`
  - Files: `deploy/mibee-nvr.service`, `config.example.yaml`, `internal/config/config.go`, `Makefile`
  - Pre-commit: `go test ./internal/config/... -v`

- [x] 10. Update getting-started.md (EN + ZH)

  **What to do**:
  - 重写 `docs/en/getting-started.md`：
    - 添加醒目的 "下载" 部分：链接到 GitHub Releases 页面，说明如何选择 amd64/arm64
    - 添加三种安装方式（按简单程度排序）：
      1. **预编译二进制**（最快）：下载 → chmod → `mibee-nvr init` → 启动
      2. **Docker**：`docker-compose up`（附最小编排示例）
      3. **一键安装脚本**：`curl -fsSL | bash`
      4. **源码构建**（开发者）
    - 添加 `mibee-nvr init` 使用说明
    - 添加密码设置指引（明文密码 vs hash-password）
    - 添加“5分钟快速体验”流程：下载 → init → 启动 → 添加摄像头（通过 Web UI）
    - 保留现有的摄像头协议示例（RTSP H.264/H.265/MJPEG、HTTP JPEG、ONVIF）
    - 添加常见问题排查（服务未启动、端口冲突、权限错误）
  - 同步更新 `docs/zh/getting-started.md`（中文翻译）

  **Must NOT do**:
  - 不添加截图（超出范围，截图需要实际运行环境）
  - 不重写 API 参考文档（在 configuration.md 中处理）

  **Recommended Agent Profile**:
  - **Category**: `writing`
    - Reason: 纯文档任务，需要清晰的逐步指引
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Tasks 11, 12, 13)
  - **Blocks**: Task 13
  - **Blocked By**: Tasks 1 (password), 2 (init), 6 (install.sh), 7 (docker-compose)

  **References**:

  **Pattern References**:
  - `docs/en/getting-started.md` — 现有内容作为重写基础
  - `docs/zh/getting-started.md` — 中文版，必须同步
  - `README.md` — Quick Start 部分（简化版）
  - `config.example.yaml` — 更新后的配置示例
  - `install.sh` — 新的安装脚本（引用其用法）
  - `docker-compose.yml` — Docker 快速启动（引用其用法）

  **WHY Each Reference Matters**:
  - getting-started 是新用户接触的第一个文档，必须与实际功能完全匹配
  - install.sh 和 docker-compose.yml 是新安装方式，文档必须涵盖

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Getting started links are valid
    Tool: Bash
    Steps:
      1. grep -oP 'https?://[^ )"]+' docs/en/getting-started.md | while read url; do curl -sI "$url" | head -1; done
      2. Verify all links return 200/301/302
    Expected Result: All external links reachable
    Evidence: .sisyphus/evidence/task-10-links.txt

  Scenario: EN/ZH content parity
    Tool: Bash
    Steps:
      1. Count ## headings in EN: grep '^## ' docs/en/getting-started.md | wc -l
      2. Count ## headings in ZH: grep '^## ' docs/zh/getting-started.md | wc -l
      3. Compare counts (should be equal ±1)
    Expected Result: Same structure in both languages
    Evidence: .sisyphus/evidence/task-10-parity.txt
  ```

  **Commit**: YES
  - Message: `docs: rewrite getting-started guide (EN+ZH)`
  - Files: `docs/en/getting-started.md`, `docs/zh/getting-started.md`
  - Pre-commit: none

- [x] 11. Update deployment.md (EN + ZH)

  **What to do**:
  - 重写 `docs/en/deployment.md`：
    - 统一使用 `deploy/mibee-nvr.service`（不再有文档中的"另一个版本"）
    - 路径统一为 `/var/lib/mibee-nvr` 和 `/usr/local/bin/mibee-nvr`
    - 添加 `install.sh` 的完整使用说明（含 `--version`、`--uninstall` 参数）
    - Docker 部署部分引用 `docker-compose.yml`
    - systemd 部分简化（因为 install.sh 已自动化）但保留手动步骤作为备选
    - 反向代理部分保留 Caddy/Nginx 示例
    - 添加 RPi 3B 特别提示：内存限制、segment_duration 建议、外挂存储
    - 更新 "Updating" 部分：使用 install.sh 升级
  - 同步更新 `docs/zh/deployment.md`

  **Must NOT do**:
  - 不在文档中创建第二个 systemd 服务文件版本
  - 不添加监控/日志聚合指南（超出范围）

  **Recommended Agent Profile**:
  - **Category**: `writing`
    - Reason: 文档任务，需要准确的运维指引
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Tasks 10, 12, 13)
  - **Blocks**: Task 13
  - **Blocked By**: Tasks 6 (install.sh), 8 (HEALTHCHECK), 9 (systemd)

  **References**:

  **Pattern References**:
  - `docs/en/deployment.md` — 现有内容作为重写基础
  - `docs/zh/deployment.md` — 中文版
  - `deploy/mibee-nvr.service` — 更新后的统一服务文件
  - `install.sh` — 安装脚本（文档引用其用法）
  - `Dockerfile` + `Dockerfile.arm64` — Docker 部署参考
  - `docs/en/mediamtx-guide.md` — MediaMTX 集成（保持不动的现有文档）

  **WHY Each Reference Matters**:
  - 当前 deployment.md 中的 systemd 服务文件与 deploy/ 不一致，必须消除这个矛盾
  - install.sh 是新的安装方式，文档必须准确描述

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: No duplicate service file in docs
    Tool: Bash
    Steps:
      1. grep -c '\[Unit\]' docs/en/deployment.md → should be 0 (no inline service file)
      2. grep -c 'deploy/mibee-nvr.service' docs/en/deployment.md → should be ≥1 (references actual file)
    Expected Result: No inline systemd service file, references deploy/ file
    Evidence: .sisyphus/evidence/task-11-no-duplicate.txt

  Scenario: EN/ZH deployment parity
    Tool: Bash
    Steps:
      1. grep '^## ' docs/en/deployment.md | wc -l
      2. grep '^## ' docs/zh/deployment.md | wc -l
      3. Compare (should be equal ±1)
    Expected Result: Same section structure
    Evidence: .sisyphus/evidence/task-11-parity.txt
  ```

  **Commit**: YES
  - Message: `docs: rewrite deployment guide (EN+ZH)`
  - Files: `docs/en/deployment.md`, `docs/zh/deployment.md`
  - Pre-commit: none

- [x] 12. Update configuration.md (EN + ZH)

  **What to do**:
  - 更新 `docs/en/configuration.md`：
    - 添加 `auth.password` 明文密码字段说明
    - 明确说明 `auth.password` 和 `auth.password_hash` 的优先级
    - 添加 `mibee-nvr init` 命令说明（初始化配置）
    - 添加 `mibee-nvr health` 命令说明
    - 确认所有配置项默认值与 `applyDefaults()` 一致
    - 确认 `storage.root_dir` 默认值已更新为 `/var/lib/mibee-nvr`
    - 添加每个配置项的示例值和说明
  - 同步更新 `docs/zh/configuration.md`

  **Must NOT do**:
  - 不添加不存在的配置项
  - 不改变配置文档的整体结构（只更新内容）

  **Recommended Agent Profile**:
  - **Category**: `writing`
    - Reason: 配置参考文档，需要精确描述每个字段
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Tasks 10, 11, 13)
  - **Blocks**: Task 13
  - **Blocked By**: Tasks 1 (password), 2 (init), 9 (config defaults)

  **References**:

  **Pattern References**:
  - `docs/en/configuration.md` — 现有配置参考
  - `docs/zh/configuration.md` — 中文版
  - `internal/config/config.go:15-109` — 所有配置结构体定义和字段
  - `internal/config/config.go:245-350` — `applyDefaults()` 默认值
  - `config.example.yaml` — 更新后的配置示例

  **WHY Each Reference Matters**:
  - 配置文档必须与代码中的结构体和默认值完全一致
  - `configuration.md` 已经提到了 `auth.password` 但描述不准确（需要匹配实际行为）

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Config doc matches code defaults
    Tool: Bash
    Steps:
      1. grep 'root_dir' docs/en/configuration.md → should mention /var/lib/mibee-nvr as default
      2. grep 'password' docs/en/configuration.md → should describe both password and password_hash fields
      3. Verify no hardcoded /mnt/data/nvr in configuration docs
    Expected Result: All defaults match code, auth.password documented
    Evidence: .sisyphus/evidence/task-12-config-doc.txt
  ```

  **Commit**: YES
  - Message: `docs: update configuration reference (EN+ZH)`
  - Files: `docs/en/configuration.md`, `docs/zh/configuration.md`
  - Pre-commit: none

- [x] 13. Update README.md + README.zh.md

  **What to do**:
  - 更新 `README.md`：
    - 在 Quick Start 部分添加醒目的下载链接（链接到 GitHub Releases）
    - 添加三种安装方式摘要（预编译二进制、Docker、一键安装）
    - 更新 "Documentation" 表格链接
    - 添加 `mibee-nvr init` 快速体验示例
    - 保持 Screenshot、Features、Project Structure 不变
    - Docker 部分引用 `docker-compose.yml`
  - 同步更新 `README.zh.md`（中文版）

  **Must NOT do**:
  - 不删除任何现有内容（只添加和更新）
  - 不添加 badge（CI status、Go Report 等）

  **Recommended Agent Profile**:
  - **Category**: `writing`
    - Reason: 文档更新，保持现有风格
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (must be last doc task)
  - **Parallel Group**: Wave 3 (sequential after Tasks 10, 11, 12)
  - **Blocks**: Task 15
  - **Blocked By**: Tasks 10, 11, 12 (all other docs must be done first)

  **References**:

  **Pattern References**:
  - `README.md` — 现有内容
  - `README.zh.md` — 中文版
  - `docs/en/getting-started.md` — 更新后的详细指南（README 摘要应与之匹配）
  - `install.sh` — 安装脚本用法
  - `docker-compose.yml` — Docker 快速启动

  **WHY Each Reference Matters**:
  - README 是 GitHub 首页，下载链接和快速体验指引必须醒目
  - 必须与 getting-started.md 的内容一致

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: README has download links
    Tool: Bash
    Steps:
      1. grep -i 'release' README.md → should find GitHub Releases link
      2. grep -i 'download' README.md → should find download instructions
      3. grep 'install.sh' README.md → should find install script reference
    Expected Result: Download and install references found
    Evidence: .sisyphus/evidence/task-13-readme.txt
  ```

  **Commit**: YES
  - Message: `docs: update README with download links and quick start`
  - Files: `README.md`, `README.zh.md`
  - Pre-commit: none

- [x] 14. Add integration tests

  **What to do**:
  - 添加以下测试：
    1. 明文密码自动转哈希测试（在 `internal/middleware/auth_test.go` 或新建文件）
    2. `init` 子命令测试（在 `tests/` 目录）
    3. `health` 子命令测试（在 `tests/` 目录）
  - 使用 `testify/require` 断言风格（与现有测试一致）
  - 测试用例：
    - 明文密码 → 自动生成哈希 → 哈希不为空 → 认证通过
    - 既有哈希又有明文 → 哈希优先
    - init 非交互模式 → 配置文件创建 → 密码哈希正确 → 目录创建
    - init 配置已存在 → 退出码 2
    - health 运行中的服务器 → 退出码 0
    - health 无服务器 → 退出码 1

  **Must NOT do**:
  - 不修改现有测试
  - 不添加 E2E 测试（仅单元/集成测试）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 需要理解现有测试模式并编写新的测试
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with Task 15)
  - **Blocks**: Task 15
  - **Blocked By**: Tasks 1, 2, 3 (features must exist first)

  **References**:

  **Pattern References**:
  - `tests/integration_test.go` — 现有集成测试模式
  - `internal/middleware/auth_test.go` — 现有 auth 测试（如果存在）
  - `internal/api/handler.go` — `TestHandler()` / `TestHandlerWithAuth()` 工厂函数

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: All new tests pass
    Tool: Bash
    Steps:
      1. Run: go test ./... -v 2>&1 | grep -E '(PASS|FAIL|---)'
      2. Count new test cases (should be ≥6)
    Expected Result: All tests PASS, 0 failures
    Evidence: .sisyphus/evidence/task-14-tests.txt
  ```

  **Commit**: YES
  - Message: `test: add integration tests for init, health, and password auto-hash`
  - Files: `tests/`, `internal/middleware/`
  - Pre-commit: `go test ./... -v`

- [x] 15. Full end-to-end deployment verification

  **What to do**:
  - 执行完整的新手部署流程验证：
    1. **预编译二进制路径**：下载 Release → init → 启动 → 认证 → 健康检查
    2. **Docker 路径**：docker-compose up → 健康检查 → 认证
    3. **Install.sh 路径**：运行安装脚本 → 服务启动 → 健康检查
    4. **源码构建路径**：make build → init → 启动
  - 记录每条路径的耗时和遇到的问题
  - 验证文档中的每个步骤与实际行为一致
  - 验证中文文档内容与英文文档一致

  **Must NOT do**:
  - 不修复发现的问题（记录问题，创建后续任务）
  - 不在生产环境执行

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 需要全面验证多种部署路径，记录详细结果
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with Task 14)
  - **Blocks**: F1-F4
  - **Blocked By**: Tasks 13, 14 (docs and tests must be done)

  **References**:

  **Pattern References**:
  - `docs/en/getting-started.md` — 验证文档中的步骤与实际一致
  - `docs/en/deployment.md` — 验证部署指南准确性
  - `README.md` — 验证 Quick Start 可行性
  - `install.sh` — 验证安装脚本完整性
  - `docker-compose.yml` — 验证 Docker 部署可行性

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**
  ```
  Scenario: Binary path end-to-end
    Tool: Bash
    Steps:
      1. Download latest release binary (or use local build)
      2. chmod +x mibee-nvr
      3. ./mibee-nvr init --password e2etest --data-dir /tmp/e2e-binary-test
      4. ./mibee-nvr -config mibee-nvr.yaml &
      5. sleep 2
      6. curl -u admin:e2etest http://localhost:9090/api/health
      7. ./mibee-nvr health
      8. Kill server
    Expected Result: Every step succeeds, health returns 200
    Evidence: .sisyphus/evidence/task-15-e2e-binary.txt

  Scenario: Documentation accuracy check
    Tool: Bash
    Steps:
      1. Read getting-started.md and execute each command as written
      2. Verify no broken links in docs
      3. Verify no hardcoded /mnt/data/nvr in any docs
      4. Verify EN/ZH section counts match
    Expected Result: All docs commands work, no stale references
    Evidence: .sisyphus/evidence/task-15-docs-accuracy.txt
  ```

  **Commit**: NO (verification only, no code changes)

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.

- [x] F1. **Plan Compliance Audit** — `oracle` (REJECT → FIXED: install.sh URL mismatch + docker-compose typo)
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — APPROVE
  Run `go vet ./...` + `go test ./...` + `make build`. Review all changed files for: `as any`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — APPROVE
  Start from clean state. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration. Test edge cases: empty state, invalid input, re-run idempotency. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — APPROVE
  For each task: read "What to do", read actual diff. Verify 1:1 — everything in spec was built, nothing beyond spec. Check "Must NOT do" compliance. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **Wave 1**: `fix(auth): support plaintext password auto-hash` — internal/middleware/auth.go, internal/config/config.go
- **Wave 1**: `feat(cli): add init subcommand for first-time setup` — cmd/mibee-nvr/main.go
- **Wave 1**: `feat(cli): add health subcommand for Docker HEALTHCHECK` — cmd/mibee-nvr/main.go
- **Wave 1**: `fix(ci): inject version via ldflags in release workflow` — .github/workflows/release.yml
- **Wave 1**: `chore: add .go-version and .nvmrc for version pinning` — .go-version, .nvmrc
- **Wave 2**: `feat(deploy): add one-click install.sh script` — install.sh
- **Wave 2**: `feat(docker): add docker-compose.yml with full port exposure` — docker-compose.yml
- **Wave 2**: `feat(docker): add HEALTHCHECK to Dockerfiles` — Dockerfile, Dockerfile.arm64
- **Wave 2**: `fix(deploy): unify systemd service and update config defaults` — deploy/mibee-nvr.service, config.example.yaml
- **Wave 3**: `docs: rewrite getting-started guide (EN+ZH)` — docs/en/getting-started.md, docs/zh/getting-started.md
- **Wave 3**: `docs: rewrite deployment guide (EN+ZH)` — docs/en/deployment.md, docs/zh/deployment.md
- **Wave 3**: `docs: update configuration reference (EN+ZH)` — docs/en/configuration.md, docs/zh/configuration.md
- **Wave 3**: `docs: update README with download links and quick start` — README.md, README.zh.md
- **Wave 4**: `test: add integration tests for init and install` — tests/

---

## Success Criteria

### Verification Commands
```bash
# Build
make build                    # Expected: success, ./mibee-nvr exists

# Password auto-hash
cat > /tmp/test-config.yaml <<EOF
server:
  listen: ":19999"
storage:
  root_dir: "/tmp/test-nvr-data"
auth:
  username: "admin"
  password: "test123"
cameras: []
cleanup:
  retention_days: 30
  check_interval: "1h"
  disk_threshold_percent: 95
EOF
./mibee-nvr -config /tmp/test-config.yaml &
sleep 2
curl -u admin:test123 http://localhost:19999/api/health  # Expected: 200

# Init command
./mibee-nvr init --password mypass --data-dir /tmp/nvr-init-test  # Expected: exit 0
ls /tmp/nvr-init-test/mibee-nvr.yaml                          # Expected: file exists

# Health command
./mibee-nvr health --config /tmp/test-config.yaml              # Expected: exit 0

# Version
./mibee-nvr -version                                           # Expected: actual version, not "0.1.0-dev"

# Docker compose
docker-compose up -d && sleep 3
curl http://localhost:9090/api/health                           # Expected: 200
docker-compose down

# Install script (dry-run check)
bash -n install.sh                                             # Expected: syntax OK

# Tests
go test ./... -v                                               # Expected: all pass
```

### Final Checklist
- [x] All "Must Have" present
- [x] All "Must NOT Have" absent
- [x] All tests pass
- [x] EN/ZH docs content parity
- [x] No new Go dependencies added
- [x] Docker images still use distroless/scratch
- [x] `make build` and `make cross` unchanged
