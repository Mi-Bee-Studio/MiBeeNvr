# MiBee NVR: Rebrand + Restructure + UI Beautify + Open Source Prep

## TL;DR

> **Quick Summary**: 将 CamVault NVR 项目完整重命名为 MiBee NVR，按 Go 标准布局重组目录架构，美化 Web UI 跟随 mlsbs.top 风格，添加 MIT License 和 README 文档，准备开源推送到 GitHub。
> 
> **Deliverables**:
> - 全项目重命名 CamVault → MiBee NVR（Go module、二进制、所有引用）
> - 目录架构按 Go 标准布局重组（合并碎包、删除 vendor、清理根目录）
> - 前端依赖合并为单一 web/ 目录
> - Web UI CSS 美化跟随 mlsbs.top 科技深色风格
> - MIT LICENSE 文件
> - README.md 项目文档
> - 脱敏处理（无真实 IP/密码/用户名）
> - .gitignore 更新
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: T1 (Go rename) → T2 (restructure) → T5 (frontend rename) → T8 (UI beautify) → T9 (docs) → F1-F4

---

## Context

### Original Request
User wants to rebrand the NVR project under Mi&Bee Studio identity, restructure the directory layout to follow Go conventions, beautify the Web UI to match studio website style (mlsbs.top), and prepare everything for open-source release on GitHub with proper license and documentation.

### Interview Summary
**Key Discussions**:
- Project name: MiBee NVR
- Go module path: `github.com/Mi-Bee-Studio/MiBeeNvr`
- Git remote: `git@github.com:Mi-Bee-Studio/MiBeeNvr.git`
- License: MIT
- UI style: Follow mlsbs.top website (dark, tech, clean, metric cards)
- DB filename: Keep `camvault.db` (backward compatibility)
- Config default: Keep `camvault.yaml` (backward compatibility)
- systemd User: `nvr` (sanitized from `mickey`)
- Directory structure: Go standard layout (merge tiny packages, remove vendor)
- Frontend deps: Merge into single `web/` directory (remove root package.json)

**Research Findings**:
- mlsbs.top style: Dark theme, clean sans-serif fonts, metric cards with big numbers, professional tech feel, subtle gradients
- `go-mp4/` is a leftover local vendor copy — code uses go.mod dependency, vendor can be deleted
- `types/` package has only 1 file — should be merged into a core package
- `Counter.svelte` is dead code (never imported)
- Root `package.json` only has tailwindcss — should merge into `web/package.json`
- Root has compiled binaries (`camvault`, `camvault-arm64`) that need git rm
- `.gitignore` currently excludes `web/` directory — must fix for open source
- `tests/integration_test.go` at root level — evaluate if it should move into internal/

### Metis Review
**Identified Gaps** (addressed):
- localStorage key change (`camvault_auth` → `mibee_nvr_auth`): Will log out existing users — acceptable for rename
- MQTT topic default change: Update in config.example.yaml
- `go-mp4/` vendor directory: Remove entirely, use go.mod dependency
- Root compiled binaries: Must git rm before open source push
- `.gitignore` excluding `web/`: Must fix to only exclude `web/node_modules/` and `web/dist/`
- FTP banner strings: Update to MiBee NVR
- `hash-password` CLI comment in config: Update reference to new binary name, do NOT implement the feature
- HTML title is just "web": Must change to "MiBee NVR"

---

## Work Objectives

### Core Objective
Complete rebrand from CamVault to MiBee NVR with directory restructuring, UI polish, and open-source preparation — zero functional changes.

### Concrete Deliverables
- All Go imports changed from `github.com/mickey/camvault` to `github.com/Mi-Bee-Studio/MiBeeNvr`
- `cmd/camvault/` renamed to `cmd/mibee-nvr/`
- Binary renamed to `mibee-nvr`
- Directory structure reorganized per Go standard layout
- Root `package.json` and `node_modules/` removed, tailwindcss merged into `web/`
- `go-mp4/` vendor directory removed
- Web UI CSS redesigned to match mlsbs.top style
- MIT LICENSE file added
- README.md written
- config.example.yaml sanitized
- .gitignore updated
- Zero residual "camvault" references (case-insensitive, excluding DB/config backward compat)
- Zero residual "mickey" references in config/service files

### Definition of Done
- [ ] `go build ./cmd/mibee-nvr/` succeeds
- [ ] `go test ./...` passes all tests
- [ ] `go vet ./...` produces no warnings
- [ ] `grep -ri "camvault"` returns zero results outside `go-mp4/` and `node_modules/`
- [ ] `grep -ri "mickey"` returns zero results in config/deploy/service files
- [ ] `LICENSE` file exists with MIT content
- [ ] `README.md` exists with required sections
- [ ] Web build succeeds: `cd web && npm run build`

### Must Have
- ALL Go import paths updated (31+ files)
- go.mod module path changed
- cmd/ directory renamed
- Binary produces `-help` output with "MiBee NVR" name
- UI displays "MiBee NVR" branding
- MIT LICENSE file
- README.md with install instructions
- No hardcoded IPs/passwords in example config
- No personal usernames in deploy files

### Must NOT Have (Guardrails)
- **NO functional changes** — rename and restructure only
- **NO API interface changes** — same endpoints, same JSON
- **NO database schema changes** — same tables, same columns
- **NO new CLI subcommands** — including `hash-password`
- **NO new npm dependencies** — use existing Tailwind only
- **NO UI component restructuring** — CSS/styling changes only
- **NO CGO** — `CGO_ENABLED=0` always
- **NO video transcoding/decoding** — unchanged
- **NO real-time preview** — out of scope
- **NO excessive comments/JSDoc** — minimal documentation in code
- **NO over-abstraction** — don't create unnecessary interfaces
- **README.md ≤ 200 lines**
- **Do NOT modify anything inside `go-mp4/`** — delete the entire directory instead
- **Do NOT change DB filename** — keep `camvault.db` for backward compatibility
- **Do NOT change default config filename** — keep `camvault.yaml`

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (82 tests across 15 packages)
- **Automated tests**: Tests-after (existing tests must still pass after rename/restructure)
- **Framework**: Go testing + `go test ./...`

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Go build/test**: Use Bash — `go build`, `go test ./...`, `go vet ./...`
- **Web build**: Use Bash — `cd web && npm run build`
- **Grep verification**: Use Bash — `grep -ri "camvault"` to verify zero residuals
- **File existence**: Use Bash — `ls`, `head`, `test -f`

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Foundation — sequential, everything depends on this):
└── Task 1: Go module rename + all import paths + cmd/ dir rename [deep]

Wave 2 (Cleanup + Restructure — parallel after rename):
├── Task 2: Restructure directory layout (merge types, remove go-mp4 vendor, clean root) [deep]
├── Task 3: Merge frontend deps (root package.json → web/) [quick]
├── Task 4: Sanitize config + deploy files [quick]

Wave 3 (Frontend + UI — parallel after Wave 2):
├── Task 5: Frontend rename (Svelte files, api.ts, index.html, CSS comments) [unspecified-high]
├── Task 6: UI CSS beautification to match mlsbs.top style [visual-engineering]

Wave 4 (Docs + Final — parallel after Wave 3):
├── Task 7: MIT LICENSE + README.md [writing]
├── Task 8: Update .gitignore + git init + final verification [quick]

Wave FINAL (After ALL tasks — 4 parallel reviews, then user okay):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality review (unspecified-high)
├── Task F3: Real verification QA (unspecified-high)
└── Task F4: Scope fidelity check (deep)
→ Present results → Get explicit user okay

Critical Path: T1 → T2 → T5 → T6 → T8 → F1-F4
Parallel Speedup: ~50% faster than sequential
Max Concurrent: 4 (Wave 4)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| T1 | — | T2, T3, T4, T5 | 1 |
| T2 | T1 | T5, T6, T7, T8 | 2 |
| T3 | T1 | T5, T6 | 2 |
| T4 | T1 | T8 | 2 |
| T5 | T2, T3 | T6, T8 | 3 |
| T6 | T5 | T8 | 3 |
| T7 | T2 | T8 | 4 |
| T8 | T4, T5, T6, T7 | F1-F4 | 4 |
| F1-F4 | T8 | user okay | FINAL |

### Agent Dispatch Summary

- **Wave 1**: 1 task — T1 → `deep`
- **Wave 2**: 3 tasks — T2 → `deep`, T3 → `quick`, T4 → `quick`
- **Wave 3**: 2 tasks — T5 → `unspecified-high`, T6 → `visual-engineering`
- **Wave 4**: 2 tasks — T7 → `writing`, T8 → `quick`
- **FINAL**: 4 tasks — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

- [x] 1. Go Module Rename + All Import Paths + cmd/ Directory Rename

  **What to do**:
  - Edit `go.mod`: Change module path from `github.com/mickey/camvault` to `github.com/Mi-Bee-Studio/MiBeeNvr`
  - Use `ast_grep_replace` or sed to replace ALL Go import paths across all `.go` files: `github.com/mickey/camvault` → `github.com/Mi-Bee-Studio/MiBeeNvr`
  - Rename directory `cmd/camvault/` → `cmd/mibee-nvr/`
  - Update `cmd/mibee-nvr/main.go`:
    - Version string: `CamVault` → `MiBee NVR`
    - Usage string references to `camvault` → `mibee-nvr`
    - DB path stays `camvault.db` (DO NOT CHANGE)
    - Config default flag stays `camvault.yaml` (DO NOT CHANGE)
  - Update `Makefile`: binary name `camvault` → `mibee-nvr`, all references
  - Update `internal/ftp/server.go`: Banner `"CamVault FTP Server"` → `"MiBee NVR FTP Server"`
  - Update `internal/muxer/mp4mux.go` and all other Go files that reference `CamVault` in string literals
  - Run `GOPROXY=https://goproxy.cn,direct go mod tidy` to regenerate go.sum
  - Run `GOPROXY=https://goproxy.cn,direct go build ./cmd/mibee-nvr/` to verify
  - Run `GOPROXY=https://goproxy.cn,direct go test ./...` to verify all tests pass

  **Must NOT do**:
  - Do NOT change DB filename (keep `camvault.db`)
  - Do NOT change default config flag (keep `camvault.yaml`)
  - Do NOT add new CLI subcommands
  - Do NOT modify go-mp4/ directory

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Mechanical but pervasive rename touching 30+ files, must be thorough
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 1 (solo)
  - **Blocks**: T2, T3, T4, T5
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `go.mod:1` — Current module declaration `module github.com/mickey/camvault`
  - `cmd/camvault/main.go:1-10` — Package declaration and imports showing current module path
  - `cmd/camvault/main.go:30` — Config flag with default `camvault.yaml`
  - `cmd/camvault/main.go:56` — DB path hardcoded as `camvault.db`
  - `internal/ftp/server.go:72,91` — FTP banner strings with `CamVault`
  - `Makefile` — Build targets referencing `camvault` binary name

  **API/Type References**:
  - All 14 internal packages under `internal/` — each has Go files importing `github.com/mickey/camvault/internal/*`
  - `tests/integration_test.go` — Imports the old module path

  **Acceptance Criteria**:

  ```
  Scenario: Go build succeeds after rename
    Tool: Bash
    Steps:
      1. CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go build -o mibee-nvr ./cmd/mibee-nvr/
      2. test -f mibee-nvr
    Expected Result: Binary exists and is executable
    Evidence: .sisyphus/evidence/task-1-build.txt

  Scenario: All tests pass after rename
    Tool: Bash
    Steps:
      1. CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go test ./... 2>&1
      2. Verify exit code 0 and all tests pass
    Expected Result: 82+ tests pass, 0 failures
    Evidence: .sisyphus/evidence/task-1-tests.txt

  Scenario: Zero old module path in Go files
    Tool: Bash
    Steps:
      1. grep -r "github.com/mickey/camvault" --include="*.go" . | grep -v go-mp4/
    Expected Result: Zero results
    Evidence: .sisyphus/evidence/task-1-grep-imports.txt

  Scenario: go vet clean
    Tool: Bash
    Steps:
      1. GOPROXY=https://goproxy.cn,direct go vet ./... 2>&1
    Expected Result: Zero warnings
    Evidence: .sisyphus/evidence/task-1-vet.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 2. Restructure Directory Layout

  **What to do**:
  - **Merge `internal/types/types.go`**: Move contents into the most relevant package. Types like `Recording`, `Camera` likely belong in `internal/storage/` or a new `internal/model/`. Evaluate each type and place appropriately. Then delete `internal/types/` directory and update all imports.
  - **Delete `go-mp4/` vendor directory**: `rm -rf go-mp4/`. The code uses `github.com/abema/go-mp4` via go.mod. Promote it from indirect to direct dependency: run `GOPROXY=https://goproxy.cn,direct go mod tidy`.
  - **Evaluate `tests/integration_test.go`**: Read it. If it tests internal packages, move it into the relevant `internal/*/test/` or keep at root as `integration_test.go` (Go convention allows both). If it's empty/trivial, delete it and the `tests/` directory.
  - **Delete dead code**: Remove `web/src/lib/Counter.svelte` (never imported anywhere).
  - **Remove root compiled binaries**: `rm -f camvault camvault-arm64` (they should already be .gitignored).
  - **Clean up empty `.gitkeep` files**: Remove from `deploy/`, `web_embed/`, `tests/` if those dirs have actual content now.
  - Run `GOPROXY=https://goproxy.cn,direct go build ./cmd/mibee-nvr/` and `go test ./...` after each structural change.

  **Must NOT do**:
  - Do NOT modify any functional logic
  - Do NOT add new files beyond what's needed for type relocation
  - Do NOT break existing test coverage

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Requires understanding type dependencies to correctly relocate types
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (depends on T1)
  - **Parallel Group**: Wave 2 (with T3, T4)
  - **Blocks**: T5, T6, T7, T8
  - **Blocked By**: T1

  **References**:

  **Pattern References**:
  - `internal/types/types.go` — All type definitions to relocate. Read this FIRST to understand what types exist and where they're used.
  - `internal/storage/db.go` — Likely consumer of types, check imports
  - `internal/api/handler.go` — Likely consumer of types
  - `internal/recorder/h264.go`, `internal/recorder/mjpeg.go` — Likely consumers of types

  **API/Type References**:
  - Use `lsp_find_references` on each type in `types.go` to find ALL usages before moving

  **Acceptance Criteria**:

  ```
  Scenario: Build succeeds after restructure
    Tool: Bash
    Steps:
      1. CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go build -o mibee-nvr ./cmd/mibee-nvr/
    Expected Result: Binary created successfully
    Evidence: .sisyphus/evidence/task-2-build.txt

  Scenario: All tests pass after restructure
    Tool: Bash
    Steps:
      1. CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go test ./... 2>&1
    Expected Result: Same number of tests as before, all pass
    Evidence: .sisyphus/evidence/task-2-tests.txt

  Scenario: go-mp4 vendor directory removed
    Tool: Bash
    Steps:
      1. test -d go-mp4 && echo "EXISTS" || echo "GONE"
    Expected Result: "GONE"
    Evidence: .sisyphus/evidence/task-2-vendor-removed.txt

  Scenario: types/ directory no longer exists
    Tool: Bash
    Steps:
      1. test -d internal/types && echo "EXISTS" || echo "GONE"
    Expected Result: "GONE"
    Evidence: .sisyphus/evidence/task-2-types-merged.txt

  Scenario: Zero references to old types package path
    Tool: Bash
    Steps:
      1. grep -r "Mi-Bee-Studio/MiBeeNvr/internal/types" --include="*.go" . | grep -v go-mp4/
    Expected Result: Zero results
    Evidence: .sisyphus/evidence/task-2-no-old-types-import.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 3. Merge Frontend Dependencies (Root package.json → web/)

  **What to do**:
  - Read root `package.json` to understand what deps exist (tailwindcss, @tailwindcss/vite, vite)
  - Read `web/package.json` to see existing deps
  - Read `web/vite.config.js` (or `.ts`) to understand current build config
  - Read root `vite.config.js` if it exists
  - Merge tailwindcss-related deps from root into `web/package.json`
  - Update `web/vite.config.js` to include tailwindcss plugin if it was in root config
  - Delete root `package.json`, root `package-lock.json`, root `node_modules/`
  - Delete root `vite.config.js` if it existed
  - Verify `cd web && npm install && npm run build` succeeds
  - Verify output appears in `internal/ui/static/` (or wherever build target is)

  **Must NOT do**:
  - Do NOT add new npm packages beyond what already exists
  - Do NOT change Svelte component structure
  - Do NOT upgrade any package versions

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Mechanical merge of package.json files, no deep logic
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T2, T4)
  - **Blocks**: T5
  - **Blocked By**: T1

  **References**:

  **Pattern References**:
  - `package.json` (root) — Dependencies to merge (tailwindcss, @tailwindcss/vite)
  - `web/package.json` — Target package.json to merge into
  - `web/vite.config.js` — Current web build config
  - `web/src/app.css:1` — `@import "tailwindcss"` shows tailwind is used in web source
  - `web/postcss.config.js` or `web/tailwind.config.js` if they exist

  **Acceptance Criteria**:

  ```
  Scenario: Root package.json removed
    Tool: Bash
    Steps:
      1. test -f package.json && echo "EXISTS" || echo "GONE"
    Expected Result: "GONE"
    Evidence: .sisyphus/evidence/task-3-root-pkg-gone.txt

  Scenario: Root node_modules removed
    Tool: Bash
    Steps:
      1. test -d node_modules && echo "EXISTS" || echo "GONE"
    Expected Result: "GONE"
    Evidence: .sisyphus/evidence/task-3-root-modules-gone.txt

  Scenario: Web build succeeds with merged deps
    Tool: Bash
    Steps:
      1. cd web && npm install 2>&1
      2. npm run build 2>&1
    Expected Result: Build completes successfully
    Evidence: .sisyphus/evidence/task-3-web-build.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 4. Sanitize Config + Deploy Files

  **What to do**:
  - **`config.example.yaml`**:
    - Change `password: "pass123"` → `password: "your-password-here"`
    - Change `192.168.1.101-103` → `192.168.1.X` placeholder IPs
    - Change `camvault hash-password` comment → `mibee-nvr hash-password`
    - Change MQTT topic `camvault/trigger` → `mibeenr/trigger`
    - Review ALL lines for any hardcoded IPs, passwords, internal hostnames
  - **`deploy/camvault.service`**:
    - Rename file to `deploy/mibee-nvr.service`
    - Change `Unit Description=CamVault NVR` → `Description=MiBee NVR`
    - Change `User=mickey` → `User=nvr`
    - Change `ExecStart` binary path from `camvault` → `mibee-nvr`
    - Change `WantedBy` and alias references if any
  - **`deploy/Caddyfile.example`**:
    - Update any `camvault` references to `mibee-nvr`
    - Ensure no hardcoded IPs
  - **Delete old deploy file**: Remove `deploy/camvault.service` after creating `deploy/mibee-nvr.service`

  **Must NOT do**:
  - Do NOT change actual runtime config format (only example values)
  - Do NOT add new config options

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Simple find-and-replace in 2-3 files
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T2, T3)
  - **Blocks**: T8
  - **Blocked By**: T1

  **References**:

  **Pattern References**:
  - `config.example.yaml` — Full config example with all sections
  - `deploy/camvault.service` — systemd unit file
  - `deploy/Caddyfile.example` — Caddy reverse proxy config

  **Acceptance Criteria**:

  ```
  Scenario: No hardcoded credentials in config example
    Tool: Bash
    Steps:
      1. grep -E "(pass123|192\\.168\\.1\\.10[1-3])" config.example.yaml
    Expected Result: Zero results
    Evidence: .sisyphus/evidence/task-4-no-creds.txt

  Scenario: No mickey username in deploy files
    Tool: Bash
    Steps:
      1. grep -ri "mickey" deploy/
    Expected Result: Zero results
    Evidence: .sisyphus/evidence/task-4-no-mickey.txt

  Scenario: New service file exists
    Tool: Bash
    Steps:
      1. test -f deploy/mibee-nvr.service && echo "EXISTS" || echo "MISSING"
      2. grep "User=nvr" deploy/mibee-nvr.service
      3. grep "MiBee NVR" deploy/mibee-nvr.service
    Expected Result: File exists, contains User=nvr and MiBee NVR
    Evidence: .sisyphus/evidence/task-4-service-file.txt

  Scenario: Old service file removed
    Tool: Bash
    Steps:
      1. test -f deploy/camvault.service && echo "EXISTS" || echo "GONE"
    Expected Result: "GONE"
    Evidence: .sisyphus/evidence/task-4-old-service-gone.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 5. Frontend Rename (Svelte + API + HTML + CSS Comments)

  **What to do**:
  - **`web/index.html`**: Change `<title>web</title>` → `<title>MiBee NVR</title>`
  - **`web/src/lib/api.ts`**: Change `AUTH_KEY = 'camvault_auth'` → `AUTH_KEY = 'mibee_nvr_auth'`
  - **`web/src/routes/Login.svelte`**: Change `CamVault` → `MiBee NVR` in all display text
  - **`web/src/app.css`**: Update comment header from `CamVault` → `MiBee NVR`
  - **`web/src/App.svelte`**: Check for any `CamVault` references
  - **`web/src/routes/Recordings.svelte`**: Check for any `CamVault` references
  - **`web/src/routes/RecordingDetail.svelte`**: Check for any `CamVault` references
  - **`web/src/routes/Stats.svelte`**: Check for any `CamVault` references
  - Run `cd web && npm run build` to verify frontend builds
  - Verify `internal/ui/static/` gets updated with new build

  **Must NOT do**:
  - Do NOT change component structure or routing
  - Do NOT change CSS styling (that's Task 6)
  - Do NOT add new npm dependencies

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Multiple file edits with build verification
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with T6 — but T6 depends on T5)
  - **Blocks**: T6, T8
  - **Blocked By**: T2, T3

  **References**:

  **Pattern References**:
  - `web/index.html:7` — Current title `<title>web</title>`
  - `web/src/lib/api.ts:49` — `AUTH_KEY = 'camvault_auth'` localStorage key
  - `web/src/routes/Login.svelte:39` — `CamVault` display text in login page
  - `web/src/app.css:3-6` — CSS comment referencing `CamVault Web UI`
  - All other `.svelte` files — scan for `CamVault` or `camvault` strings

  **Acceptance Criteria**:

  ```
  Scenario: Web build succeeds after rename
    Tool: Bash
    Steps:
      1. cd web && npm run build 2>&1
    Expected Result: Build completes successfully
    Evidence: .sisyphus/evidence/task-5-web-build.txt

  Scenario: HTML title updated
    Tool: Bash
    Steps:
      1. grep '<title>MiBee NVR</title>' web/index.html
    Expected Result: Match found
    Evidence: .sisyphus/evidence/task-5-html-title.txt

  Scenario: localStorage key updated
    Tool: Bash
    Steps:
      1. grep 'mibee_nvr_auth' web/src/lib/api.ts
    Expected Result: Match found
    Evidence: .sisyphus/evidence/task-5-auth-key.txt

  Scenario: Zero CamVault in frontend source
    Tool: Bash
    Steps:
      1. grep -ri "camvault" web/src/ web/index.html
    Expected Result: Zero results
    Evidence: .sisyphus/evidence/task-5-no-camvault.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 6. UI CSS Beautification (mlsbs.top Style)

  **What to do**:
  - **Design Direction**: Follow mlsbs.top website style — dark tech theme, clean sans-serif typography, metric cards with large numbers, subtle brand accents, professional feel
  - **Edit `web/src/app.css`**: 
    - Update color palette: Replace amber/orange accent with a cooler tone (blue/cyan/teal) matching mlsbs.top tech aesthetic
    - Keep the dark slate base (`bg-slate-900`, `bg-slate-800`) — it already matches
    - Refine card styles: Add subtle border glow or gradient for hover states
    - Improve badge styles: More refined colors
    - Better button styling: Subtle gradients instead of flat colors
    - Update spinner if needed
  - **Edit Svelte route files (CSS ONLY)**:
    - `web/src/routes/Login.svelte`: Polish login card styling, add subtle brand elements, improve form aesthetics
    - `web/src/routes/Recordings.svelte`: Polish table/list styling, better card layout
    - `web/src/routes/RecordingDetail.svelte`: Polish detail view
    - `web/src/routes/Stats.svelte`: Make stat cards look like mlsbs.top metric cards (big numbers, subtle bg)
    - `web/src/App.svelte`: Polish navigation bar styling
  - **Brand Elements**:
    - Replace `CamVault` text with `MiBee NVR` in Login.svelte (if not already done in T5)
    - Add subtle bee/honeycomb motif OR keep it clean text-only (user preference for clean)
  - Run `cd web && npm run build` to verify

  **Must NOT do**:
  - Do NOT restructure Svelte components (layout changes only via CSS)
  - Do NOT add new npm packages or icon libraries
  - Do NOT change routing logic
  - Do NOT add JavaScript/TypeScript logic changes
  - Do NOT add excessive animations (keep it professional/minimal)

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: CSS design and UI polish task
  - **Skills**: [`/frontend-ui-ux`]
    - `/frontend-ui-ux`: Designer-turned-developer skill for crafting stunning UI/UX

  **Parallelization**:
  - **Can Run In Parallel**: NO (depends on T5)
  - **Parallel Group**: Wave 3 (after T5)
  - **Blocks**: T8
  - **Blocked By**: T5

  **References**:

  **Pattern References**:
  - `web/src/app.css` — Full CSS design system (144 lines, Tailwind utility classes)
  - `web/src/routes/Login.svelte` — Login page with dark card styling
  - `web/src/routes/Recordings.svelte` — Recordings list page
  - `web/src/routes/Stats.svelte` — Stats page with progress bars
  - `web/src/App.svelte` — SPA layout with navigation

  **External References**:
  - mlsbs.top website design: Dark background, clean layout, metric cards with large numbers, professional tech feel, subtle blue accents, "128 devices / 12 homes / 99.9% uptime" style metric display

  **Acceptance Criteria**:

  ```
  Scenario: Web build succeeds after UI changes
    Tool: Bash
    Steps:
      1. cd web && npm run build 2>&1
    Expected Result: Build completes successfully
    Evidence: .sisyphus/evidence/task-6-web-build.txt

  Scenario: CSS no longer uses amber accent as primary
    Tool: Bash
    Steps:
      1. grep -c 'amber' web/src/app.css
    Expected Result: Count should be significantly reduced (amber removed as primary accent)
    Evidence: .sisyphus/evidence/task-6-css-accent.txt

  Scenario: Stats page has metric card styling
    Tool: Bash
    Steps:
      1. grep -E '(text-3xl|text-4xl|text-5xl)' web/src/routes/Stats.svelte
    Expected Result: At least one large text class found for metric display
    Evidence: .sisyphus/evidence/task-6-metric-cards.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 7. MIT LICENSE + README.md

  **What to do**:
  - **Create `LICENSE` file** (root): Standard MIT License text with:
    - Year: 2026
    - Copyright holder: `Mi&Bee Studio`
  - **Create `README.md`** (root): Max 200 lines, written in **Chinese** (user's primary language), including:
    - `# MiBee NVR` title with brief description
    - Badges: License: MIT, Go Version, Platform: ARM64/AMD64
    - `## ✨ Features` — Bulleted list of key features (RTSP recording, Web UI, WebDAV, FTP, MQTT, multi-camera, hybrid cleanup, etc.)
    - `## 📋 Requirements` — Hardware/software requirements (RPi 3B+, external storage, Go 1.22+, etc.)
    - `## 🚀 Quick Start` — Download/clone, config, build, run commands
    - `## ⚙️ Configuration` — Link to `config.example.yaml`, brief explanation of key sections
    - `## 🏗️ Build from Source` — Go build commands, cross-compile for ARM64
    - `## 📁 Project Structure` — Brief directory layout overview
    - `## 🤝 Contributing` — Link to CONTRIBUTING.md (or brief guideline)
    - `## 📄 License` — MIT license notice with link to LICENSE file
    - `## 🐝 About` — Mi&Bee Studio mention with link to mlsbs.top
  - Do NOT include real IPs, passwords, or internal hostnames

  **Must NOT do**:
  - Do NOT exceed 200 lines for README
  - Do NOT include real credentials or internal IPs
  - Do NOT include screenshots (none exist yet, add placeholder comments)
  - Do NOT write excessive documentation — concise and practical

  **Recommended Agent Profile**:
  - **Category**: `writing`
    - Reason: Documentation writing task requiring clear Chinese prose
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with T8)
  - **Blocks**: T8
  - **Blocked By**: T2

  **References**:

  **Pattern References**:
  - `config.example.yaml` — Reference for configuration section
  - `Makefile` — Reference for build commands
  - `deploy/mibee-nvr.service` — Reference for systemd deployment
  - `deploy/Caddyfile.example` — Reference for reverse proxy setup
  - `cmd/mibee-nvr/main.go` — Reference for feature list

  **External References**:
  - https://www.mlsbs.top/ — Studio website for About section

  **Acceptance Criteria**:

  ```
  Scenario: LICENSE file is valid MIT
    Tool: Bash
    Steps:
      1. head -1 LICENSE
    Expected Result: "MIT License"
    Evidence: .sisyphus/evidence/task-7-license.txt

  Scenario: README has required sections
    Tool: Bash
    Steps:
      1. grep -E '^## ' README.md
    Expected Result: At least 6 sections found (Features, Requirements, Quick Start, Configuration, Build, License)
    Evidence: .sisyphus/evidence/task-7-readme-sections.txt

  Scenario: README mentions MiBee NVR
    Tool: Bash
    Steps:
      1. head -1 README.md
    Expected Result: "# MiBee NVR"
    Evidence: .sisyphus/evidence/task-7-readme-title.txt

  Scenario: README under 200 lines
    Tool: Bash
    Steps:
      1. wc -l README.md
    Expected Result: ≤ 200
    Evidence: .sisyphus/evidence/task-7-readme-length.txt

  Scenario: No real credentials in docs
    Tool: Bash
    Steps:
      1. grep -E '(pass123|192\\.168\\.)' README.md LICENSE
    Expected Result: Zero results
    Evidence: .sisyphus/evidence/task-7-no-creds.txt
  ```

  **Commit**: NO (groups with all tasks)

- [x] 8. Update .gitignore + Final Cleanup + Verification

  **What to do**:
  - **Update `.gitignore`**:
    - Remove `web/` exclusion — web source must be tracked for open source
    - Add specific exclusions: `web/node_modules/`, `web/dist/`
    - Remove `web_embed/` exclusion if it was for old build artifacts
    - Ensure binary exclusions: `mibee-nvr`, `mibee-nvr-arm64`, `camvault`, `camvault-arm64`
    - Add standard Go gitignore: `*.exe`, `*.test`, `*.out`
    - Add `.sisyphus/` (project management artifacts)
  - **Remove compiled binaries**: `rm -f camvault camvault-arm64 mibee-nvr mibee-nvr-arm64`
  - **Initialize git repo** if not already: `git init`
  - **Final comprehensive grep check**:
    - `grep -ri "camvault" --include="*.go" --include="*.svelte" --include="*.ts" --include="*.css" --include="*.html" --include="*.yaml" --include="*.json" --include="*.mod" --include="*.service" --include="Makefile" --include="*.md" . 2>/dev/null | grep -v node_modules/ | grep -v .sisyphus/`
    - Should return ZERO results
  - **Build and test verification**:
    - `CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go build -o mibee-nvr ./cmd/mibee-nvr/`
    - `CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go test ./...`
    - `CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go vet ./...`
    - `cd web && npm run build`
    - Cross-compile: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOPROXY=https://goproxy.cn,direct go build -o mibee-nvr-arm64 ./cmd/mibee-nvr/`
  - **Verify binary runs**: `./mibee-nvr -help` should show `MiBee NVR` in output

  **Must NOT do**:
  - Do NOT push to remote (user will do that manually)
  - Do NOT configure git user (use system defaults)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Cleanup and verification, mostly running commands
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (partially)
  - **Parallel Group**: Wave 4 (with T7)
  - **Blocks**: F1-F4
  - **Blocked By**: T4, T5, T6, T7

  **References**:

  **Pattern References**:
  - `.gitignore` — Current gitignore rules
  - `Makefile` — Build targets to verify

  **Acceptance Criteria**:

  ```
  Scenario: Zero camvault residuals in entire project
    Tool: Bash
    Steps:
      1. grep -ri "camvault" --include="*.go" --include="*.svelte" --include="*.ts" --include="*.css" --include="*.html" --include="*.yaml" --include="*.json" --include="*.mod" --include="*.service" --include="Makefile" --include="*.md" . 2>/dev/null | grep -v node_modules/ | grep -v .sisyphus/
    Expected Result: Zero results
    Evidence: .sisyphus/evidence/task-8-no-camvault.txt

  Scenario: Zero mickey in config/deploy
    Tool: Bash
    Steps:
      1. grep -ri "mickey" config.example.yaml deploy/ 2>/dev/null
    Expected Result: Zero results
    Evidence: .sisyphus/evidence/task-8-no-mickey.txt

  Scenario: Full build passes
    Tool: Bash
    Steps:
      1. CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go build -o mibee-nvr ./cmd/mibee-nvr/
      2. ./mibee-nvr -help 2>&1 | head -5
    Expected Result: Binary builds, help output shows "MiBee NVR"
    Evidence: .sisyphus/evidence/task-8-build.txt

  Scenario: All tests pass
    Tool: Bash
    Steps:
      1. CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go test ./... 2>&1
    Expected Result: All tests pass
    Evidence: .sisyphus/evidence/task-8-tests.txt

  Scenario: Cross-compile ARM64 succeeds
    Tool: Bash
    Steps:
      1. CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOPROXY=https://goproxy.cn,direct go build -o mibee-nvr-arm64 ./cmd/mibee-nvr/
    Expected Result: ARM64 binary created
    Evidence: .sisyphus/evidence/task-8-arm64.txt

  Scenario: Web build succeeds
    Tool: Bash
    Steps:
      1. cd web && npm run build 2>&1
    Expected Result: Build succeeds
    Evidence: .sisyphus/evidence/task-8-web-build.txt

  Scenario: Binary help shows MiBee NVR
    Tool: Bash
    Steps:
      1. ./mibee-nvr -help 2>&1
    Expected Result: Output contains "MiBee NVR"
    Evidence: .sisyphus/evidence/task-8-binary-name.txt
  ```

  **Commit**: YES
  - Message: `feat: rebrand CamVault to MiBee NVR with directory restructure and UI refresh`
  - Files: All changed files
  - Pre-commit: `CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go build ./cmd/mibee-nvr/ && go test ./...`




## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, grep, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go build ./cmd/mibee-nvr/` + `go vet ./...` + `go test ./...`. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Vet [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Verification QA** — `unspecified-high`
  Run EVERY acceptance criteria command from EVERY task. Verify: grep for zero "camvault" residuals, grep for zero "mickey" in config/deploy, LICENSE exists, README has required sections, web build succeeds. Cross-compile for ARM64. Save all evidence to `.sisyphus/evidence/final-qa/`.
  Output: `Criteria [N/N pass] | Build [PASS/FAIL] | Cross-compile [PASS/FAIL] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual git diff. Verify 1:1 — everything in spec was done, nothing beyond spec. Check "Must NOT do" compliance. Verify no functional changes were introduced. Flag any unaccounted changes.
  Output: `Tasks [N/N compliant] | Functional Changes [NONE/N found] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **Single commit** after all tasks verified: `feat: rebrand CamVault to MiBee NVR with directory restructure and UI refresh`
- Pre-commit: `CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go build ./cmd/mibee-nvr/ && go test ./...`

---

## Success Criteria

### Verification Commands
```bash
# Build succeeds
CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go build -o mibee-nvr ./cmd/mibee-nvr/
# Expected: binary created, zero errors

# All tests pass
CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct go test ./...
# Expected: 82 tests pass, 0 failures

# Zero camvault residuals
grep -ri "camvault" --include="*.go" --include="*.svelte" --include="*.ts" --include="*.css" --include="*.html" --include="*.yaml" --include="*.json" --include="*.mod" --include="*.service" --include="Makefile" --include="*.md" . 2>/dev/null | grep -v "node_modules/" | grep -v ".sisyphus/"
# Expected: zero results

# Zero mickey in config/deploy
grep -ri "mickey" config.example.yaml deploy/ 2>/dev/null
# Expected: zero results

# LICENSE exists
head -1 LICENSE
# Expected: "MIT License"

# README has required sections
grep -E "^## " README.md
# Expected: includes Features, Installation, Configuration, License

# Web build succeeds
cd web && npm run build
# Expected: builds successfully

# Cross-compile ARM64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o mibee-nvr-arm64 ./cmd/mibee-nvr/
# Expected: binary created
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All 82+ tests pass
- [ ] Zero residual "camvault" references
- [ ] Zero residual "mickey" in config/deploy
- [ ] MIT LICENSE file exists
- [ ] README.md complete
- [ ] Web UI shows "MiBee NVR" branding
- [ ] .gitignore properly configured
- [ ] ARM64 cross-compilation succeeds
