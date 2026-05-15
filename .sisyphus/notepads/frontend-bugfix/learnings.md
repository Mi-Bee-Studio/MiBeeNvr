# Frontend Bugfix — Learnings

## i18n System
- Uses `$state` in `index.svelte.ts` for reactivity
- `t(key, params?)` function with `{param}` placeholder syntax
- Keys follow hierarchy: `common.*`, `recordings.*`, `cameras.*`, `stats.*`, `settings.*`, `detail.*`, `live.*`, `dashboard.*`, `onvif.*`, `ptz.*`
- Fallback: if key not in current lang, falls back to English, then returns key string
- en.json has 343 lines, zh.json has 344 lines

## format.ts
- `formatFileSize(bytes)` only has B/KB/MB/GB — MISSING TB
- `formatDate(dateStr)` and `formatDuration(seconds)` already locale-aware
- Chart in Stats.svelte hardcodes `total_size / (1024 * 1024)` for MB — needs auto-unit

## Cameras.svelte Hardcoded Strings Found
- Merge config section (lines 630-778): ~30 Chinese strings for labels, options, toasts
- Fallback strings in formatTimeAgo(): '从未录制', '活跃', 'm前', 'h前'
- ONVIF section: 'Auto-detect' option, '(ONVIF Endpoint)' span
- Placeholders: '未设置', '已设置'
- Toast messages: '已恢复全局默认设置', '操作失败'
- TypeScript `any`: line 82 (deviceDetail), line 163 (camera as any)

## Stats.svelte Hardcoded Strings Found
- Chart labels: 'Storage (MB)', 'Recordings'
- Merge monitoring section: '合并状态', '启用', '关闭', '待合并', '上次运行', '合并段数', '新建文件', '错误数', '暂无待合并'
- Chart data hardcoded to MB: `d.total_size / (1024 * 1024)`

## Dashboard.svelte Issues Found
- TypeScript `any`: `hlsInstances: Record<string, any>`
- Empty catch blocks: lines 49, 237, 241
- Hardcoded English title attributes: "Live", "Buffering", "Error", "Snapshot mode"

## Conventions
- Console.error/warn text should NOT be internationalized (developer-facing only)
- Translation keys must use existing hierarchy patterns
- Build verification: `cd web && npm run build`
