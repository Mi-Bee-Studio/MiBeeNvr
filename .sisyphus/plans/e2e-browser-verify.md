# E2E 浏览器验证 + 修复

## TL;DR

> **目标**: 在 RPi 部署环境 (192.168.63.31:9090) 进行全面浏览器测试，验证所有页面的主题切换、语言切换、移动端适配、功能完整性，修复发现的问题并重新部署。
>
> **交付物**:
> - 所有 6 个页面（Login、Recordings、Cameras、Stats、Settings、RecordingDetail）在浏览器中正确渲染
> - 主题切换（暗/亮）在所有页面正常工作
> - 语言切换（中/英）在所有页面正常工作（包括表头、筛选器、按钮等）
> - 移动端汉堡菜单在 375px 视口下可用
> - 登录流程完整（登录→浏览→退出→重新登录）
> - 所有发现的 bug 已修复并重新部署

---

## Context

### 前置工作
- frontend-fix 计划 9/9 任务全部完成
- Header.svelte 已集成到 App.svelte
- 5 个路由页面内联 header 已删除
- Login.svelte 有独立 ThemeToggle + LanguageSwitcher
- LanguageSwitcher 修复为 Svelte 5 语法
- i18n 翻译文件 en.json/zh.json 各有 192 个 key，覆盖所有页面
- Recordings.svelte 所有标签均使用 `t()` 函数

### 已知状态
- Playwright 部分测试已完成：Recordings 页面加载 ✅、主题切换 ✅、语言切换导航 ✅、0 console 错误 ✅
- **未测试**: Login 页面、Cameras、Stats、Settings、RecordingDetail、移动端 375px

---

## TODOs

- [x] 1. **全面浏览器 E2E 测试** ✅ 全部 8 项测试通过

  **What to do**:
  使用 Playwright 在 http://192.168.63.31:9090 执行以下测试（凭据: admin/admin）：
  
  1. **Login 页面**: 访问 `/#/login`，验证：
     - 登录表单正确渲染（用户名、密码输入框、登录按钮）
     - 右上角有 ThemeToggle 和 LanguageSwitcher
     - ThemeToggle 可切换暗/亮主题
     - LanguageSwitcher 可切换中/英（表单标签、按钮文本都切换）
     - 输入 admin/admin 点击登录后跳转到 Recordings 页面
  
  2. **Recordings 页面**: 访问 `/#/recordings`，验证：
     - Header 显示正确（导航链接、ThemeToggle、LanguageSwitcher、Logout 按钮）
     - 切换语言为中文 → 筛选器标签（摄像头、格式、状态）变为中文
     - 切换语言为中文 → 表头（摄像头、格式、时长、大小、日期、状态、操作）变为中文
     - 切换回英文 → 所有标签恢复英文
     - 主题切换暗/亮正常
     - 无 console 错误
  
  3. **Cameras 页面**: 访问 `/#/cameras`，验证：
     - 页面正确渲染，有 Header
     - 标题 "Camera Management" / "摄像头管理" 随语言切换
     - "Add Camera" / "添加摄像头" 按钮文本随语言切换
     - 无 console 错误
  
  4. **Stats 页面**: 访问 `/#/stats`，验证：
     - 页面正确渲染，有 Header
     - 标题和统计标签随语言切换
     - 无 console 错误
  
  5. **Settings 页面**: 访问 `/#/settings`，验证：
     - 页面正确渲染，有 Header
     - 设置项标签随语言切换
     - 无 console 错误
  
  6. **RecordingDetail 页面**: 如果有录像数据，访问 `/#/recording-detail/{id}`：
     - 页面正确渲染，有 Header + 返回按钮
     - 操作按钮（下载、固定、删除）文本随语言切换
     - 无 console 错误
  
  7. **移动端 375px**: 设置视口为 375x812 (iPhone X)，验证：
     - 汉堡菜单按钮可见
     - 点击汉堡菜单 → 下拉导航菜单展开
     - 导航链接可点击跳转
     - 主题切换和语言切换仍然可用
     - Logout 按钮只显示图标不显示文字
  
  8. **退出登录流程**: 点击 Logout → 跳转到 Login 页面 → 重新登录成功

  **Must NOT do**:
  - 不修改任何代码（此任务仅测试）
  - 不创建新的 npm 依赖

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: [`playwright`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 1 (testing)
  - **Blocks**: T2
  - **Blocked By**: None

  **References**:
  - RPi URL: http://192.168.63.31:9090
  - 凭据: admin / admin
  - `web/src/components/Header.svelte` — 共享导航组件 (373 行)
  - `web/src/lib/i18n/en.json` / `zh.json` — 192 个翻译 key
  - `web/src/routes/` — 所有路由页面

  **Acceptance Criteria**:
  - [ ] 所有 6 个页面正确渲染，0 console 错误
  - [ ] 主题切换在所有页面正常工作
  - [ ] 语言切换在所有页面正常工作（包括表头、筛选器、按钮）
  - [ ] 移动端 375px 汉堡菜单可用
  - [ ] 登录/退出流程正常
  - [ ] 截图保存为证据

  **QA Scenarios:**
  ```
  Scenario: Full E2E browser test
    Tool: Playwright
    Steps: 见上方 1-8 测试步骤
    Expected Result: 所有页面正确渲染、交互正常、无错误
    Evidence: .sisyphus/evidence/e2e-browser-test/
  ```

  **Commit**: NO (仅测试)

---

- [x] 2. **修复浏览器测试发现的问题** ✅ CSP header 添加 media-src blob: 'self'，旧静态文件清理

  **What to do**:
  根据任务 1 的测试结果，修复所有发现的 bug。可能的问题类型：
  - i18n key 缺失或不匹配
  - 样式问题（布局错位、主题色不正确）
  - 功能问题（按钮不工作、路由错误）
  - 移动端适配问题
  
  每个修复后需要：
  1. `cd web && npm run build` 零错误
  2. 复制 dist 到 internal/ui/static/
  3. 重新构建 Go binary 并部署到 RPi
  4. 在浏览器中验证修复有效

  **Must NOT do**:
  - 不修改后端代码
  - 不修改 upload/handler.go 或 webdav/server.go
  - 不新增 npm 依赖

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: [`playwright`, `frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2 (fix)
  - **Blocks**: T3
  - **Blocked By**: T1

  **Acceptance Criteria**:
  - [ ] 任务 1 发现的所有问题已修复
  - [ ] `cd web && npm run build` 零错误
  - [ ] 部署到 RPi 后浏览器验证修复有效
  - [ ] 无新的 regression

  **Commit**: NO (与 T3 合并)

---

- [x] 3. **最终部署 + 回归验证** ✅ 已部署到 RPi，E2E 回归测试全通过

  **What to do**:
  1. 构建前端: `cd web && npm run build`
  2. 复制 dist: `cp -r web/dist/* internal/ui/static/`
  3. 构建并部署: `make deploy`
  4. 等待 3 秒后运行部署检查: `make deploy-check`
  5. 回归测试：重复任务 1 的关键测试路径
     - Login 页面正常
     - 所有页面有 Header
     - 主题切换正常
     - 语言切换正常
     - 移动端正常
  6. Go 测试: `go test ./...` 全部通过

  **Must NOT do**:
  - 不修改任何代码

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: [`playwright`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (final)
  - **Blocks**: None
  - **Blocked By**: T2

  **Acceptance Criteria**:
  - [ ] `cd web && npm run build` 零错误
  - [ ] `make deploy` 成功
  - [ ] `make deploy-check` 通过
  - [ ] 回归测试全部通过
  - [ ] `go test ./...` 全部通过

  **Commit**: YES
  - Message: `fix(ui): browser-verified theme, language switching, and mobile nav`
  - Files: 所有修改的前端文件
  - Pre-commit: `cd web && npm run build`

---

## Final Verification Wave

- [x] F1. **Oracle 审查**: ✅ CSP 更改安全（blob: 同源），无逻辑错误
- [x] F2. **浏览器回归**: ✅ 全量 Playwright E2E 测试通过（8/8），0 console errors
- [x] F3. **构建验证**: ✅ `npm run build` 零错误 + `go test ./...` 264/264 pass + `go vet` clean

---

## Commit Strategy

- **Single commit** (if fixes needed): `fix(ui): browser-verified theme, language switching, and mobile nav`
- **No commit** (if no fixes needed): 测试通过即可交付

---

## Success Criteria

### Verification Commands
```bash
cd web && npm run build                              # Expected: exit 0
make deploy && make deploy-check                      # Expected: service active, health ok
go test ./...                                        # Expected: all pass
```

### Final Checklist
- [x] 所有 6 个页面在浏览器中正确渲染
- [x] 主题切换在所有页面正常工作
- [x] 语言切换在所有页面正常工作
- [x] 移动端 375px 汉堡菜单可用
- [x] 登录/退出流程正常
- [x] 0 console 错误
- [x] `npm run build` 零错误
- [x] `go test ./...` 全部通过
- [x] 部署到 RPi 后所有页面正常
