# Draft: 前端 Bug 修复

## Requirements (confirmed)
- 用户要求: 修正前端未做多语言处理的内容
- 用户要求: 仪表盘存储趋势单位优化，自动适配 KB/MB/GB
- 用户要求: 检查并修正其他前端 bug

## Research Findings

### I18n 审计结果
- **i18n 系统**: 自定义轻量级方案，使用 Svelte 5 `$state` rune，支持 en/zh
- **翻译文件**: `web/src/lib/i18n/en.json` (343 keys), `zh.json` (344 keys)
- **使用方式**: `import { t } from '$lib/i18n'` → `t('key.name')`
- **未国际化文件**: 8/15 个 .svelte 文件有硬编码字符串（约 80 处）
  - Cameras.svelte: 30+ 处（最严重）
  - Settings.svelte: 19 处
  - Stats.svelte: 12 处
  - LiveView.svelte: 5+ 处（混合中英文）
  - Recordings.svelte: 6 处
  - Dashboard.svelte: 5 处（英文）
  - RecordingDetail.svelte: 2 处
  - Pagination.svelte: 2 处（英文）
- **已完全国际化**: App.svelte, Login.svelte, Header.svelte, ThemeToggle.svelte, Toast.svelte, LanguageSwitcher.svelte, PtzControl.svelte

### 存储趋势单位问题
- **当前**: `formatFileSize()` 只支持 B/KB/MB/GB，缺少 TB
- **图表**: Stats.svelte 硬编码转换为 MB（`/(1024*1024)`），标签 "Storage (MB)"
- **后端**: 返回原始字节数
- **实际环境**: NVR 有 2.7TB 硬盘，需要 TB 支持
- **修复点**: 
  1. `format.ts` 的 `formatFileSize()` 添加 TB 单位
  2. 图表数据根据数据范围自动选择合适单位
  3. 图表标签动态显示当前单位

### 其他发现的 Bug
1. **未处理的 Promise rejection** - LiveView.svelte, Dashboard.svelte 的 .then() 链缺少 .catch()
2. **空的 catch 块** - Dashboard.svelte 多处静默吞掉错误
3. **TypeScript `any` 类型** - hls-errors.ts, LiveView.svelte, Dashboard.svelte, Cameras.svelte
4. **可访问性问题** - Dashboard.svelte 缺少键盘导航
5. **代码重复** - Stats.svelte 重复 import
6. **不一致的警告消息** - preferences.ts 有效值与警告消息不匹配
7. **潜在内存泄漏** - RecordingDetail.svelte blob URL 处理

## Open Questions
- 用户是否要修复所有发现的 bug，还是只聚焦 i18n + 存储单位？
- 测试策略选择

## Scope Boundaries
- INCLUDE: 前端 i18n 修复、存储单位优化
- TBD: 其他 bug 的修复范围
