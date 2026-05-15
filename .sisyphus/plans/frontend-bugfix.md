# 前端 Bug 全面修复 — i18n、存储单位、代码质量

## TL;DR

> **Quick Summary**: 修复前端所有已发现的 bug：8 个文件约 80 处未国际化字符串、存储趋势图表单位自动适配（KB→MB→GB→TB）、15 个代码质量问题（未处理 Promise、空 catch、TypeScript any 类型、可访问性等）。
> 
> **Deliverables**:
> - en.json/zh.json 新增约 80 个翻译键
> - 8 个 .svelte 文件完成 i18n 替换
> - formatFileSize() 支持 TB + 新增 formatChartValue() 图表单位工具
> - Stats.svelte 存储趋势图表智能单位切换
> - Promise 错误处理、TypeScript 类型安全、可访问性等修复
> 
> **Estimated Effort**: Medium
> **Parallel Execution**: YES - 3 waves
> **Critical Path**: Task 1 (翻译键) → Task 4-9 (文件修复) → F1-F4 (验证)

---

## Context

### Original Request
用户发现前端部分内容未做多语言处理，要求修正所有未国际化字符串。同时要求优化仪表盘存储趋势的单位显示，使其能自动适配 KB/MB/GB。此外要求检查并修正所有其他前端 bug。

### Interview Summary
**Key Discussions**:
- 用户明确要求"修复所有的 bug"，包括 i18n、存储单位、代码质量共 15 个问题
- 项目使用自定义 i18n 系统（Svelte 5 $state rune），翻译文件在 web/src/lib/i18n/
- NVR 实际环境：2.7TB 硬盘，当前 formatFileSize() 缺 TB 导致显示 "2700.00 GB"

**Research Findings**:
- **i18n 审计**: 8/15 个 .svelte 文件有硬编码字符串，共约 80 处
  - Cameras.svelte 最严重（30+ 处中文）
  - Settings.svelte（19 处）、Stats.svelte（12 处）、LiveView.svelte（5+ 处）
  - Recordings.svelte（6 处）、Dashboard.svelte（5 处英文）、RecordingDetail.svelte（2 处）、Pagination.svelte（2 处英文）
- **存储单位**: formatFileSize() 只有 B/KB/MB/GB，缺 TB；图表硬编码除以 1024² 转 MB
- **后端**: 返回原始字节数（int64），无需改动

### Metis Review
**Identified Gaps** (addressed):
- 确认所有 ~80 个翻译键都是用户可见的 UI 文本（排除 console.error/warn 日志）
- 图表自动单位需要同时更新 y-axis 标签和数据 tooltip 格式
- 翻译键命名需遵循现有层级结构（recordings.*, cameras.*, settings.* 等）

---

## Work Objectives

### Core Objective
修复前端所有已发现的 bug，使所有用户可见文本完成 i18n、存储单位智能适配、代码质量达标。

### Concrete Deliverables
- `web/src/lib/i18n/en.json` + `zh.json` 新增约 80 个翻译键
- `web/src/lib/format.ts` 新增 TB 支持和 `formatChartValue()` 函数
- 8 个 .svelte 文件的硬编码字符串全部替换为 `t()` 调用
- Stats.svelte 存储趋势图表使用智能单位
- LiveView.svelte / Dashboard.svelte 的 Promise 错误处理修复
- TypeScript `any` 类型替换为具体类型

### Definition of Done
- [x] 切换语言（en↔zh）时，所有页面文本正确切换，无遗漏
- [x] 2.7TB 硬盘总空间显示为 "2.70 TB" 而非 "2700.00 GB"
- [x] 存储趋势图表 Y 轴标签根据数据量级自动显示合适单位
- [x] 无未处理的 Promise rejection（浏览器控制台无 Unhandled Promise 警告）
- [x] `rtk go test ./...` 通过
- [x] `cd web && npm run build` 成功
### Must Have
- 所有用户可见的 UI 文本必须通过 `t()` 函数获取
- `formatFileSize()` 支持 B/KB/MB/GB/TB 五级单位
- 存储趋势图表根据数据范围自动选择最佳单位
- 所有 Promise 链必须有 `.catch()` 处理
- TypeScript 代码无 `any` 类型（HLS 库类型除外，可用类型断言）

### Must NOT Have (Guardrails)
- 不修改后端 Go 代码（后端已返回原始字节，无需改动）
- 不修改 i18n 系统核心（index.svelte.ts），只添加翻译键
- 不将 console.error/warn 的日志文本国际化（这些是开发者调试信息）
- 不新增 npm 依赖
- 不改动 Login.svelte、Header.svelte 等已完全国际化的文件
- 不做代码重构或功能新增，只修复已识别的 bug
- 翻译键命名必须遵循现有层级模式（common.*, recordings.*, cameras.* 等）
- `formatChartValue()` 返回值必须包含数值和单位两部分，供 Chart.js 的 ticks callback 和 tooltip 使用

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (Playwright E2E tests)
- **Automated tests**: Tests-after（在实现后通过 Agent QA 验证）
- **Framework**: Playwright (existing) + browser verification

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Frontend/UI**: Use Playwright — Navigate, switch language, assert DOM text content, screenshot
- **Build verification**: Use Bash — `cd web && npm run build`, check exit code
- **Type checking**: Use Bash — `cd web && npx svelte-check` if available

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Foundation — 3 parallel tasks):
├── Task 1: Add ~80 translation keys to en.json + zh.json [quick]
├── Task 2: Extend formatFileSize() (TB) + create formatChartValue() [quick]
└── Task 3: Fix TypeScript types + preferences.ts + code quality [quick]

Wave 2 (Per-file i18n + bug fixes — 6 parallel tasks, depend on Wave 1):
├── Task 4: Cameras.svelte: i18n 30+ strings + fix TypeScript any [unspecified-high]
├── Task 5: Settings.svelte: i18n 19 strings [unspecified-high]
├── Task 6: Stats.svelte: i18n 12 strings + storage chart auto-unit [unspecified-high]
├── Task 7: LiveView.svelte: i18n + fix Promise + fix TypeScript any [unspecified-high]
├── Task 8: Dashboard.svelte: i18n + fix Promise + catch + a11y + TypeScript any [deep]
└── Task 9: Recordings + RecordingDetail + Pagination: i18n + memory leak fix [unspecified-high]

Wave FINAL (After ALL tasks — 4 parallel reviews):
├── F1: Plan compliance audit (oracle)
├── F2: Code quality review (unspecified-high)
├── F3: Real manual QA (unspecified-high + playwright)
└── F4: Scope fidelity check (deep)
-> Present results -> Get explicit user okay

Critical Path: Task 1 → Task 4-9 → F1-F4 → user okay
Parallel Speedup: ~65% faster than sequential
Max Concurrent: 6 (Wave 2)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| 1 | — | 4,5,6,7,8,9 | 1 |
| 2 | — | 6 | 1 |
| 3 | — | — | 1 |
| 4 | 1 | F1-F4 | 2 |
| 5 | 1 | F1-F4 | 2 |
| 6 | 1, 2 | F1-F4 | 2 |
| 7 | 1 | F1-F4 | 2 |
| 8 | 1 | F1-F4 | 2 |
| 9 | 1 | F1-F4 | 2 |
| F1 | 4-9 | user | FINAL |
| F2 | 4-9 | user | FINAL |
| F3 | 4-9 | user | FINAL |
| F4 | 4-9 | user | FINAL |

### Agent Dispatch Summary

- **Wave 1**: **3** — T1→`quick`, T2→`quick`, T3→`quick`
- **Wave 2**: **6** — T4→`unspecified-high`, T5→`unspecified-high`, T6→`unspecified-high`, T7→`unspecified-high`, T8→`deep`, T9→`unspecified-high`
- **FINAL**: **4** — F1→`oracle`, F2→`unspecified-high`, F3→`unspecified-high`, F4→`deep`

---

## TODOs

---

## Final Verification Wave

- [x] F1. **Plan Compliance Audit** — `oracle` ✅ APPROVE — All Must Have verified, console.error t() violations fixed
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high` ✅ APPROVE — Build passes, 394/394 key parity, missing recordingDeleted key fixed
  Run `cd web && npm run build`. Review all changed .svelte and .ts files for: `any` types (should be eliminated), empty catches, unhandled promises, hardcoded Chinese/English strings outside `t()`, unused imports. Check AI slop: excessive comments, over-abstraction.
  Output: `Build [PASS/FAIL] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `oracle` ✅ APPROVE — Build passes, 394/394 key parity, zero hardcoded Chinese, zero console.error(t(, all strings via t()
  Start from clean state. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Key tests:
  1. Switch language en→zh→en, verify ALL text on ALL pages changes correctly
  2. Verify storage displays TB (not thousands of GB)
  3. Verify chart Y-axis label matches data unit
  4. Verify no console errors on any page
  Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep` ✅ APPROVE — 9/9 tasks compliant, 6/6 guardrails clean, 0 unaccounted files
  For each task: read "What to do", read actual diff. Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **Wave 1**: `fix(web): add i18n keys and utility functions for bug fixes` — en.json, zh.json, format.ts, hls-errors.ts, preferences.ts
- **Wave 2**: `fix(web): complete i18n and fix all frontend bugs` — all .svelte files
- Pre-commit: `cd web && npm run build`

---

## Success Criteria

### Verification Commands
```bash
cd web && npm run build    # Expected: success, no errors
```

### Final Checklist
- [x] All "Must Have" present
- [x] All "Must NOT Have" absent
- [x] All 8 files fully i18n'd (no hardcoded user-facing strings)
- [x] formatFileSize() handles B/KB/MB/GB/TB
- [x] Storage chart auto-selects appropriate unit
- [x] No unhandled Promise rejections
- [x] No TypeScript `any` in changed files
- [x] Language switching works on all pages
