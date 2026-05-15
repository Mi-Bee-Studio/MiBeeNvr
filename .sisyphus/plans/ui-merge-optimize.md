# UI + 录像合并 + 省流优化

## TL;DR

> **Quick Summary**: 三合一功能升级：UI 导航改进（Logo 跳转大屏 + 渐变色）、后台定时合并录像段（1 小时窗口，同参数拼接）、监控大屏省流优化（子码流 + JPEG 快照 + MJPEG 降帧）。
> 
> **Deliverables**:
> - Logo 点击跳转 `#/dashboard` + 渐变色文字
> - `internal/merge/` 新包 — 后台录像合并管理器
> - MP4 段合并（H264/H265）— 同 SPS/PPS 快速拼接
> - MJPEG 目录合并
> - 子码流配置 + HLS 子码流连接
> - JPEG 快照 API（摄像头 snapshot URL → 缓存 → 返回）
> - MJPEG SampleInterval 可配置化
> - Dashboard 前端：快照缩略图模式 + 子码流 HLS 切换
> - 单元/集成测试
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: Config → MP4 Parser → H264/H265 Merge → Wire in main.go → Tests → Final QA

---

## Context

### Original Request
用户提出三个方向：
1. 导航条 "MiBee NVR" 点击回到监控大屏，文字加渐变色
2. 后台渐进式合并历史录像减少文件数（限制：1 小时窗口）
3. 监控大屏和实时预览省流优化

### Interview Summary
**Key Discussions**:
- 合并策略：同参数快速拼接（SPS/PPS 相同才合并，不解码不重编码）
- 合并时机：后台定时任务（类似 cleanup manager）
- 合并粒度：按小时分组，同一小时内同摄像头相邻段合并
- MJPEG 也需要合并
- 合并后删除原段
- 省流方案：子码流 + JPEG 快照 + 降帧 三管齐下
- 摄像头子码流支持不确定，需自适应
- 测试策略：Tests after

**Research Findings**:
- Logo: `Header.svelte:85` — `<a href="#/recordings">` 需改为 `#/dashboard`
- 录像段：每 30s 一个 MP4，15-20MB RAM，一天 4 路 ≈ 11520 文件
- MP4Muxer: ftyp+moov+mdat 结构，所有 sample 在 RAM 中累积后 Close() 写出
- HLS: hls.js 播放，最大 4 路并发，60s 空闲超时，无转码
- abema/go-mp4 已是依赖，可用于解析现有段
- CleanupManager 是后台任务的标准模板
- CameraConfig 无 SubStreamURL/SnapshotURL 字段
- MJPEG SampleInterval 已存在但硬编码为 1

### Metis Review
**Identified Gaps** (addressed):
- Pinned 录像合并策略 → 跳过 pinned，在其边界拆分合并窗口
- SPS/PPS 变化边界 → 必须按参数变化拆分，不跨参数合并
- H265 也需合并 → hvcC vs avcC 不同但逻辑相似
- 磁盘空间检查 → 合并前检查可用空间
- 合并+清理竞态 → 同周期内先合并后清理
- 子码流自动检测 → 不做，用户手动配置
- H264 录像不能降帧 → 降帧仅限 MJPEG 录制和直播预览
- 合并崩溃恢复 → 保留原段直到验证合并成功，CleanupTempFiles 清理残余
- 批量限制 → 每次合并上限（如 200 段）

---

## Work Objectives

### Core Objective
1. 导航 Logo 改为跳转监控大屏并添加渐变色美化
2. 实现后台定时录像合并，将同 1 小时内的相邻段合并为单个文件，减少文件数量
3. 通过子码流/JPEG 快照/降帧降低监控大屏和实时预览的带宽消耗

### Concrete Deliverables
- `web/src/components/Header.svelte` — Logo href + 渐变 CSS
- `internal/merge/manager.go` — 合并管理器（NewMergeManager, Run, RunOnce）
- `internal/merge/mp4merge.go` — H264/H265 MP4 段合并逻辑
- `internal/merge/mjpegmerge.go` — MJPEG 目录合并逻辑
- `internal/storage/db.go` — 新增合并查询方法
- `internal/config/config.go` — MergeConfig + SubStreamURL/SnapshotURL/SampleInterval
- `internal/api/handler.go` — 快照 API 端点
- `internal/hls/manager.go` — 子码流连接支持
- `web/src/routes/Dashboard.svelte` — 快照缩略图 + 子码流模式
- 测试文件

### Definition of Done
- [ ] 点击 Logo "MiBee NVR" 导航到 `#/dashboard`
- [ ] Logo 文字显示渐变色（明/暗主题均可见）
- [ ] 合并任务每小时运行一次，将同小时内同摄像头的相邻段合并
- [ ] 合并后原段从 DB 和磁盘删除，新段可正常播放
- [ ] Pinned 录像不被合并
- [ ] 子码流配置后 HLS 直播使用子码流 URL
- [ ] Dashboard 支持快照缩略图模式
- [ ] MJPEG SampleInterval 可在配置中设置
- [ ] 所有测试通过

### Must Have
- Logo 跳转 + 渐变色
- H264/H265 MP4 段合并（同 SPS/PPS，流式 I/O）
- MJPEG 目录合并
- 合并后删除原段
- 跳过 pinned 录像
- 后台定时任务
- 子码流 URL 配置 + HLS 使用
- JPEG 快照 API
- MJPEG SampleInterval 配置化
- 单元测试

### Must NOT Have (Guardrails)
- ❌ 不做转码/不引入 ffmpeg/不引入 CGO 依赖
- ❌ 不修改 H264Recorder/H265Recorder/MJPEGRecorder/MP4Muxer 的录制逻辑
- ❌ 不做 H264 录像降帧（会导致不可播放）
- ❌ 不做子码流自动检测/探测
- ❌ 不做合并 UI（进度条、状态页）
- ❌ 不做合并手动触发 API
- ❌ 不做自适应码率 ABR
- ❌ 不做合并通知系统
- ❌ 不把整个合并结果加载到 RAM（必须流式 I/O）
- ❌ 不在配置 UI 中添加合并/子码流设置（仅 YAML 配置）

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (testify/require)
- **Automated tests**: Tests after
- **Framework**: Go testing + testify/require
- **Agent-Executed QA**: ALWAYS (mandatory for all tasks)

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Frontend/UI**: Use Playwright — navigate, click, assert DOM, screenshot
- **Go backend**: Use `rtk go test` — run tests, check output
- **API**: Use Bash (curl) — send requests, assert status + response
- **Config**: Use Bash — verify config parsing, defaults, validation

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — foundation + UI):
├── Task 1: Logo 链接 + 渐变色 [visual-engineering]
├── Task 2: Config 扩展 (MergeConfig + SubStreamURL + SnapshotURL + SampleInterval) [quick]
├── Task 3: DB 合并查询方法 [quick]
├── Task 4: MP4 段解析器 (read moov boxes) [deep]
└── Task 5: 快照 API 端点 + 缓存 [unspecified-high]

Wave 2 (After Wave 1 — core implementation, MAX PARALLEL):
├── Task 6: 合并管理器骨架 + H264/H265 MP4 合并 (depends: 2, 3, 4) [deep]
├── Task 7: MJPEG 目录合并 (depends: 3) [quick]
├── Task 8: 子码流 HLS 连接 (depends: 2) [unspecified-high]
├── Task 9: HLS 帧率限制器 (depends: 2) [unspecified-high]
├── Task 10: Dashboard 前端改造 (depends: 5, 8) [visual-engineering]
└── Task 11: MJPEG SampleInterval 接线 (depends: 2) [quick]

Wave 3 (After Wave 2 — integration + wiring):
├── Task 12: main.go 接线合并管理器 (depends: 6, 7) [quick]
├── Task 13: 合并 + 清理排序 + 边界处理 (depends: 12) [unspecified-high]
└── Task 14: 前端 Dashboard 集成测试 (depends: 10, 11) [visual-engineering]

Wave 4 (After Wave 3 — testing):
├── Task 15: 合并功能测试 (depends: 13) [deep]
└── Task 16: 省流功能测试 (depends: 14) [unspecified-high]

Wave FINAL (After ALL — verification):
├── F1: Plan compliance audit (oracle)
├── F2: Code quality review (unspecified-high)
├── F3: Real manual QA (unspecified-high)
└── F4: Scope fidelity check (deep)
→ Present results → Get explicit user okay

Critical Path: T2 → T4 → T6 → T12 → T13 → T15 → F1-F4
Parallel Speedup: ~65% faster than sequential
Max Concurrent: 5 (Wave 1) / 6 (Wave 2)
```

### Dependency Matrix

| Task | Blocked By | Blocks | Wave |
|------|-----------|--------|------|
| 1 | - | - | 1 |
| 2 | - | 6, 8, 9, 11 | 1 |
| 3 | - | 6, 7 | 1 |
| 4 | - | 6 | 1 |
| 5 | - | 10 | 1 |
| 6 | 2, 3, 4 | 12 | 2 |
| 7 | 3 | 12 | 2 |
| 8 | 2 | 10 | 2 |
| 9 | 2 | 16 | 2 |
| 10 | 5, 8 | 14 | 2 |
| 11 | 2 | 14 | 2 |
| 12 | 6, 7 | 13 | 3 |
| 13 | 12 | 15 | 3 |
| 14 | 10, 11 | 16 | 3 |
| 15 | 13 | F1-F4 | 4 |
| 16 | 14 | F1-F4 | 4 |

### Agent Dispatch Summary

- **Wave 1**: 5 tasks — T1 → `visual-engineering`, T2 → `quick`, T3 → `quick`, T4 → `deep`, T5 → `unspecified-high`
- **Wave 2**: 6 tasks — T6 → `deep`, T7 → `quick`, T8 → `unspecified-high`, T9 → `unspecified-high`, T10 → `visual-engineering`, T11 → `quick`
- **Wave 3**: 3 tasks — T12 → `quick`, T13 → `unspecified-high`, T14 → `visual-engineering`
- **Wave 4**: 2 tasks — T15 → `deep`, T16 → `unspecified-high`
- **FINAL**: 4 tasks — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

---


- [x] 1. Logo 链接跳转大屏 + 渐变色美化

  **What to do**:
  - 修改 `web/src/components/Header.svelte` 第 85 行：`href="#/recordings"` → `href="#/dashboard"`
  - 为 `.logo` 类添加 CSS 渐变色效果（`background: linear-gradient(...)` + `background-clip: text` + `-webkit-text-fill-color: transparent`）
  - 渐变配色方案交由 UI agent 在实施时决定，确保明暗主题均可见
  - 不修改 Header 组件结构，仅改 href 和添加/修改 `.logo` 的 CSS 样式

  **Must NOT do**:
  - 不重构 Header 组件
  - 不添加新 CSS 文件
  - 不改变导航行为（其他 nav items 不变）

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: UI 视觉设计任务，需要配色审美
  - **Skills**: [`/frontend-ui-ux`]
    - `/frontend-ui-ux`: 设计师级 UI 配色和渐变设计
  - **Skills Evaluated but Omitted**:
    - `agent-browser`: 不需要浏览器自动化，是代码修改任务

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3, 4, 5)
  - **Blocks**: None
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `web/src/components/Header.svelte:85` — 当前 `<a href="#/recordings" class="logo">MiBee NVR</a>`，修改目标
  - `web/src/components/Header.svelte:203-209` — 当前 `.logo` CSS 样式，在此添加渐变
  - `web/src/routes/Dashboard.svelte` — 目标路由组件，确认 `#/dashboard` 路由存在

  **API/Type References**:
  - `web/src/App.svelte:35-41` — 路由解析，确认 `dashboard` 路由已注册

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: Logo 链接跳转到大屏
    Tool: Playwright
    Preconditions: 应用已启动，用户已登录
    Steps:
      1. 导航到任意非 dashboard 页面（如 #/recordings）
      2. 点击 Header 中的 "MiBee NVR" 链接（选择器：`a.logo`）
      3. 等待 URL hash 变为 `#/dashboard`
      4. 断言 Dashboard 组件已渲染（选择器：`.dashboard-grid` 或类似）
    Expected Result: URL 变为 #/dashboard，Dashboard 页面显示
    Failure Indicators: URL 不变，或显示 404/空白页
    Evidence: .sisyphus/evidence/task-1-logo-navigation.png

  Scenario: Logo 渐变色在明暗主题可见
    Tool: Playwright
    Preconditions: 应用已启动，用户已登录
    Steps:
      1. 切换到亮色主题
      2. 截图 Header 区域
      3. 切换到暗色主题
      4. 截图 Header 区域
      5. 检查 `.logo` 元素的 computed style 包含 `background-clip: text`
    Expected Result: 两个主题下渐变色均清晰可见，文字不消失
    Failure Indicators: 文字不可见（与背景色融合）或渐变未生效
    Evidence: .sisyphus/evidence/task-1-logo-gradient-light.png, .sisyphus/evidence/task-1-logo-gradient-dark.png
  ```

  **Commit**: YES
  - Message: `feat(ui): logo link to dashboard + gradient color`
  - Files: `web/src/components/Header.svelte`

- [x] 2. Config 扩展 — MergeConfig + 子码流 + 快照 + SampleInterval

  **What to do**:
  - 在 `internal/config/config.go` 中添加 `MergeConfig` 结构体:
    ```go
    type MergeConfig struct {
      Enabled           bool   `yaml:"enabled"`
      CheckInterval     string `yaml:"check_interval"`     // default: "1h"
      WindowSize        string `yaml:"window_size"`        // default: "1h" — 合并窗口大小
      BatchLimit        int    `yaml:"batch_limit"`        // default: 200 — 每次最多合并段数
      MinSegmentAge     string `yaml:"min_segment_age"`    // default: "10m" — 段最小年龄（避免合并正在录制的）
      MinSegmentsToMerge int   `yaml:"min_segments_to_merge"` // default: 3 — 少于此数不合并
    }
    ```
  - 在 `CameraConfig` 中添加可选字段:
    ```go
    SubStreamURL  string `yaml:"sub_stream_url"`   // 子码流 RTSP URL（可选）
    SnapshotURL   string `yaml:"snapshot_url"`     // JPEG 快照 HTTP URL（可选）
    SampleInterval int   `yaml:"sample_interval"` // MJPEG 采样间隔（默认 1 = 全部帧）
    ```
  - 在 `Config` 结构体中添加 `Merge MergeConfig` 字段
  - 在 `applyDefaults()` 中设置默认值
  - 在 `Validate()` 中验证字段（WindowSize > 0, BatchLimit > 0 等）
  - 更新 `config.example.yaml` 添加新配置示例和注释

  **Must NOT do**:
  - 不修改任何业务逻辑
  - 不修改现有配置字段的默认值
  - 不添加 UI 配置界面

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 纯结构体定义 + 默认值 + 验证，无复杂逻辑
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3, 4, 5)
  - **Blocks**: Tasks 6, 8, 9, 11
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/config/config.go:46-50` — `CleanupConfig` 结构体，MergeConfig 应仿照此模式
  - `internal/config/config.go:36-44` — `CameraConfig` 结构体，SubStreamURL/SnapshotURL 添加于此
  - `internal/config/config.go:180-210` — `applyDefaults()` 函数，添加默认值于此
  - `internal/config/config.go:212-260` — `Validate()` 函数，添加验证于此

  **External References**:
  - `config.example.yaml` — 添加新配置示例和注释

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: MergeConfig 默认值正确加载
    Tool: Bash
    Preconditions: 无配置文件（使用默认值）
    Steps:
      1. 编写测试：加载空配置，检查 Merge.Enabled=false, Merge.CheckInterval="1h", Merge.WindowSize="1h", Merge.BatchLimit=200
      2. 运行 `rtk go test ./internal/config/... -run TestMergeDefaults -v`
    Expected Result: 所有默认值正确
    Evidence: .sisyphus/evidence/task-2-merge-config-defaults.txt

  Scenario: MergeConfig 验证拒绝无效输入
    Tool: Bash
    Steps:
      1. 编写测试：WindowSize="0", BatchLimit=-1 应返回 validation error
      2. 运行 `rtk go test ./internal/config/... -run TestMergeValidation -v`
    Expected Result: Validate() 返回错误
    Evidence: .sisyphus/evidence/task-2-merge-config-validation.txt
  ```

  **Commit**: YES
  - Message: `feat(config): add merge, sub-stream, snapshot, sample-interval config`
  - Files: `internal/config/config.go, config.example.yaml`

- [x] 3. DB 合并查询方法

  **What to do**:
  - 在 `internal/storage/db.go` 中添加以下方法:
    - `ListMergeableSegments(ctx, cameraID string, windowStart, windowEnd time.Time) ([]*model.Recording, error)` — 查询指定时间窗口内同摄像头的已完成段（ended_at IS NOT NULL），按 started_at ASC 排序
    - `DeleteRecordingsBatch(ctx, ids []string) error` — 批量删除录像记录（事务内）
    - `ListCameraMergeWindows(ctx, cameraID string, minAge time.Duration) ([]TimeWindow, error)` — 按小时分组返回可合并的时间窗口，排除 pinned 和最近正在录制的段
  - 每个窗口返回：StartTime, EndTime, SegmentCount, Format
  - 查询条件：`pinned = 0 AND ended_at IS NOT NULL AND ended_at < NOW() - minSegmentAge`
  - 按小时分组：`GROUP BY strftime('%Y-%m-%d %H', started_at)`

  **Must NOT do**:
  - 不修改现有查询方法
  - 不修改 DB schema
  - 不处理 MP4 文件逻辑

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 标准 SQL 查询方法，遵循现有 db.go 模式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 4, 5)
  - **Blocks**: Tasks 6, 7
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/storage/db.go:69-95` — 录像表 schema 和索引，查询的基础
  - `internal/storage/db.go:200-280` — 现有 `ListRecordings()` 方法，仿照此查询构建模式
  - `internal/storage/db.go:380-420` — `DeleteRecording()` 和批量删除，DeleteRecordingsBatch 仿照此
  - `internal/storage/db.go:138` — `sqliteTimeFormat` 常量，时间查询格式

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: 按小时分组查询可合并窗口
    Tool: Bash
    Steps:
      1. 编写测试：插入 120 条 30s 段记录（同一小时），调用 ListCameraMergeWindows
      2. 断言返回 1 个窗口，SegmentCount=120
      3. 插入 1 条 pinned=1 的记录，再次查询，断言 pinned 段不在结果中
      4. 运行 `rtk go test ./internal/storage/... -run TestMergeWindows -v`
    Expected Result: 正确分组，排除 pinned，排除过新段
    Evidence: .sisyphus/evidence/task-3-merge-windows.txt
  ```

  **Commit**: YES
  - Message: `feat(storage): add merge query methods`
  - Files: `internal/storage/db.go`

- [x] 4. MP4 段解析器（读取 moov box）

  **What to do**:
  - 在 `internal/merge/parser.go` 中实现 MP4 段解析:
    - `ParseSegment(filePath string) (*SegmentInfo, error)` — 解析 MP4 文件的 moov box
    - `SegmentInfo` 结构体包含:
      - `SPS, PPS []byte` (H264) 或 `VPS, SPS, PPS []byte` (H265)
      - `Codec string` ("h264" / "h265")
      - `Timescale uint32`
      - `SampleCount int`
      - `TotalDuration time.Duration`
      - `MdatOffset, MdatSize int64`
      - `SampleTable` (stts, stsz, stsc, stco entries)
  - 使用 `abema/go-mp4` 库读取 box 结构（已是项目依赖）
  - 通过 `mp4.ReadBoxStructure()` 遍历 ftyp, moov, mdat
  - 从 moov → trak → mdia → minf → stbl 中提取 sample 表
  - 从 avcC (H264) 或 hvcC (H265) 中提取参数集
  - 注意：解析器只读不写，不修改任何文件

  **Must NOT do**:
  - 不修改 MP4Muxer
  - 不修改任何录制逻辑
  - 不加载整个文件到 RAM（使用 file seek + box parser）

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 需要深入理解 MP4 box 结构和 abema/go-mp4 API
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 5)
  - **Blocks**: Task 6
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/muxer/mp4mux.go` — MP4Muxer 写入 ftyp+moov+mdat 的逻辑，解析器需要理解这个结构来逆向读取
  - `internal/muxer/mp4mux.go:50-100` — AddH264Track/AddH265Track，了解 avcC/hvcC box 的写入格式

  **API/Type References**:
  - `internal/model/types.go` — FormatH264, FormatH265 常量

  **External References**:
  - abema/go-mp4 库文档：`mp4.ReadBoxStructure()` API 用于解析现有 MP4 文件
  - 查看 go.mod 确认 abema/go-mp4 版本和可用 API

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: 解析 H264 MP4 段文件
    Tool: Bash
    Steps:
      1. 编写测试：用 MP4Muxer 创建一个小 MP4 段（几帧 H264）
      2. 调用 ParseSegment() 解析
      3. 断言 Codec="h264", SPS/PPS 非空, SampleCount > 0
      4. 断言 MdatOffset 和 MdatSize 合理
      5. 运行 `rtk go test ./internal/merge/... -run TestParseH264Segment -v`
    Expected Result: 正确解析段信息
    Evidence: .sisyphus/evidence/task-4-parse-h264.txt

  Scenario: 解析 H265 MP4 段文件
    Tool: Bash
    Steps:
      1. 用 MP4Muxer 创建 H265 段（几帧）
      2. 调用 ParseSegment()
      3. 断言 Codec="h265", VPS/SPS/PPS 非空
    Expected Result: H265 参数集正确解析
    Evidence: .sisyphus/evidence/task-4-parse-h265.txt
  ```

  **Commit**: YES
  - Message: `feat(merge): add mp4 segment parser`
  - Files: `internal/merge/parser.go`

- [x] 5. 快照 API 端点 + 缓存

  **What to do**:
  - 在 `internal/api/handler.go` 中添加快照端点:
    - `GET /api/cameras/{id}/snapshot` — 获取摄像头当前帧的 JPEG 快照
  - 实现快照缓存机制:
    - 每个 camera 最多缓存 1 张快照
    - TTL: 可配置，默认 10 秒
    - 使用 sync.Map 或 map + mutex 存储 {cameraID: {data, timestamp}}
  - 快照获取逻辑:
    - 如果缓存未过期 → 直接返回缓存的 JPEG
    - 如果缓存过期/不存在 → HTTP GET 到 camera 的 SnapshotURL → 缓存 → 返回
    - 如果请求失败 → 返回上次缓存的快照（如有）+ 设置较短的 TTL
    - 如果无 SnapshotURL → 返回 404
  - 设置 `Content-Type: image/jpeg` 和 `Cache-Control: max-age=5`
  - 注册路由到 `Routes()` 方法

  **Must NOT do**:
  - 不为快照创建持久化存储（DB/文件）
  - 不从 RTSP 流中抽帧（仅 HTTP GET snapshot URL）
  - 不为快照添加认证之外的处理

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 需要理解 HTTP handler + 缓存 + 并发模式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 4)
  - **Blocks**: Task 10
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/api/handler.go:1078-1168` — `handleHLSStream` 方法，参考路由注册和 handler 模式
  - `internal/api/handler.go:113-121` — `Routes()` 方法，新路由注册于此
  - `internal/camera/manager.go` — 获取 camera config（需要 SnapshotURL）

  **API/Type References**:
  - `internal/model/types.go` — Camera 相关类型
  - `internal/config/config.go:CameraConfig` — SnapshotURL 字段（Task 2 添加）

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: 快照端点返回 JPEG 图像
    Tool: Bash (curl)
    Preconditions: 配置了 SnapshotURL 的摄像头
    Steps:
      1. curl -u admin:password http://localhost:9090/api/cameras/{id}/snapshot -o /tmp/snapshot.jpg
      2. 检查 HTTP 状态码 = 200
      3. 检查 Content-Type = image/jpeg
      4. file /tmp/snapshot.jpg 确认是 JPEG
    Expected Result: 返回有效的 JPEG 图像
    Evidence: .sisyphus/evidence/task-5-snapshot-api.txt

  Scenario: 无 SnapshotURL 返回 404
    Tool: Bash (curl)
    Steps:
      1. curl -u admin:password -s -o /dev/null -w "%{http_code}" http://localhost:9090/api/cameras/{no-snapshot-id}/snapshot
    Expected Result: HTTP 404
    Evidence: .sisyphus/evidence/task-5-snapshot-404.txt
  ```

  **Commit**: YES
  - Message: `feat(api): add snapshot endpoint with caching`
  - Files: `internal/api/handler.go`
- [x] 6. 合并管理器骨架 + H264/H265 MP4 合并逻辑

  **What to do**:
  - 创建 `internal/merge/manager.go`：
    - `MergeManager` 结构体（仿照 CleanupManager 模式）
    - `NewMergeManager(db, store, cfg MergeConfig, cameras func() []CameraConfig)`
    - `Run(ctx)` — 后台循环，按 CheckInterval 定期调用 RunOnce
    - `RunOnce(ctx)` — 单次合并流程
  - RunOnce 逻辑:
    1. 遍历每个 camera
    2. 查询该 camera 的可合并时间窗口
    3. 对每个窗口：查询段列表 → 解析每段的 SPS/PPS → 按相同参数分组 → 合并同组段
    4. 合并前检查磁盘空间（需要空间 > 合并后预期大小的 110%）
    5. 合并后验证新文件（size > 0, 可被解析器读取）
    6. 验证成功：DB 事务内删除原段 + 插入新段 → 删除原文件
    7. 验证失败：删除合并文件，log 错误，保留原段
  - 创建 `internal/merge/mp4merge.go`：
    - `MergeMP4Segments(segments []*SegmentInfo, outputPath string) error`
    - 流式 I/O：不把所有数据加载到 RAM
    - 步骤：
      1. 验证所有段的 SPS/PPS 字节完全相同
      2. 创建临时文件
      3. 写入 ftyp box（使用第一段的 ftyp）
      4. 留置 moov 占位（记录 offset）
      5. 逐段 stream-copy mdat 数据到临时文件（每次读 1MB buffer）
      6. 构建合并后的 sample table（调整 offset）
      7. 写入合并后的 moov box
      8. 同步文件
    - H264: 使用 avcC box 中的 SPS/PPS
    - H265: 使用 hvcC box 中的 VPS/SPS/PPS
  - 合并后 Recording 记录：
    - ID: `merged_` + nanoid
    - StartedAt: 最早段的 started_at
    - EndedAt: 最晚段的 ended_at
    - Duration: 总时长
    - FileSize: 合并文件实际大小
    - FrameCount: 所有色段 frame_count 之和

  **Must NOT do**:
  - 不把整个合并结果加载到 RAM
  - 不合并 pinned 录像
  - 不合并 SPS/PPS 不同的段
  - 不修改录制逻辑
  - 不做并行合并（一次只处理一个 camera）

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 核心 MP4 合并逻辑，需要深入理解 MP4 box 结构和流式 I/O
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 7-11)
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 12
  - **Blocked By**: Tasks 2, 3, 4

  **References**:

  **Pattern References**:
  - `internal/cleanup/cleanup.go:20-68` — CleanupManager 的完整结构，MergeManager 完全仿照此模式（NewXxxManager, Run, RunOnce）
  - `internal/cleanup/cleanup.go:85-120` — 时间基础清理逻辑，参考遍历 camera + 查询 + 删除的流程
  - `internal/cleanup/cleanup.go:183-190` — deleteRecording() 的 DB-first-then-file 删除模式
  - `internal/muxer/mp4mux.go:50-100` — ftyp + moov + mdat 写入顺序，理解这个以正确重建合并后的 moov
  - `internal/muxer/mp4mux.go:120-160` — sample table 写入（stts, stsz, stsc, stco），合并时需要重建这些表

  **API/Type References**:
  - `internal/model/types.go` — Recording 结构体，合并后需要创建新的 Recording 实例
  - `internal/storage/manager.go:56-88` — CreateSegment 文件命名模式，合并文件应遵循相同命名

  **Test References**:
  - `internal/cleanup/cleanup_test.go` — CleanupManager 测试模式

  **WHY Each Reference Matters**:
  - cleanup.go 是最直接的模板，合并管理器的生命周期、错误处理、日志模式完全一致
  - mp4mux.go 的写入顺序决定了解析器必须逆向理解的文件结构

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: H264 段合并为单文件
    Tool: Bash
    Steps:
      1. 编写测试：创建 3 个小 H264 MP4 段（相同 SPS/PPS，每段 5 帧）
      2. 调用 MergeMP4Segments()
      3. 断言输出文件存在且 size > 0
      4. 用 ParseSegment() 解析输出文件，断言 SampleCount = 15 (3×5)
      5. 断言帧数、时长之和正确
      6. 运行 `rtk go test ./internal/merge/... -run TestMergeH264 -v`
    Expected Result: 合并文件有效，sample 数正确
    Evidence: .sisyphus/evidence/task-6-merge-h264.txt

  Scenario: SPS/PPS 不同拒绝合并
    Tool: Bash
    Steps:
      1. 创建 2 个不同 SPS 的段
      2. 调用 MergeMP4Segments()
    Expected Result: 返回错误（参数不匹配）
    Evidence: .sisyphus/evidence/task-6-sps-mismatch.txt
  ```

  **Commit**: YES
  - Message: `feat(merge): add merge manager + h264/h265 mp4 merge logic`
  - Files: `internal/merge/manager.go, internal/merge/mp4merge.go`

- [x] 7. MJPEG 目录合并逻辑

  **What to do**:
  - 创建 `internal/merge/mjpegmerge.go`：
    - `MergeMJPEGSegments(ctx, segments []*model.Recording, outputPath string) (*model.Recording, error)`
  - 合并步骤：
    1. 创建目标目录（遵循 manager.go 命名模式）
    2. 遍历每个源目录：
       - 列出所有 JPEG 文件（已按时间戳命名排序）
       - Move 每个 JPEG 到目标目录
    3. 删除空源目录
    4. 计算合并后的总大小（filepath.Walk）
    5. 返回新的 Recording 结构体
  - 错误处理：如果中途失败，已移动的文件保留在目标目录（不回滚，下次合并会继续）

  **Must NOT do**:
  - 不复制文件（用 os.Rename 移动，省空间）
  - 不修改 JPEG 文件内容
  - 不重命名 JPEG 文件（保持原始时间戳文件名）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 简单的目录操作，无复杂逻辑
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 6, 8-11)
  - **Blocks**: Task 12
  - **Blocked By**: Task 3

  **References**:

  **Pattern References**:
  - `internal/recorder/mjpeg.go:294-362` — MJPEG 段关闭逻辑，了解目录结构和文件命名
  - `internal/storage/manager.go:76-81` — MJPEG 段的文件命名和目录结构

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: MJPEG 目录合并
    Tool: Bash
    Steps:
      1. 创建 3 个源目录，每个包含 10 个 JPEG 文件
      2. 调用 MergeMJPEGSegments()
      3. 断言目标目录包含 30 个 JPEG 文件
      4. 断言源目录已删除
      5. 运行 `rtk go test ./internal/merge/... -run TestMergeMJPEG -v`
    Expected Result: 30 个文件在目标目录，源目录已清理
    Evidence: .sisyphus/evidence/task-7-merge-mjpeg.txt
  ```

  **Commit**: YES
  - Message: `feat(merge): add mjpeg directory merge logic`
  - Files: `internal/merge/mjpegmerge.go`

- [x] 8. 子码流 HLS 连接

  **What to do**:
  - 修改 `internal/camera/manager.go` 的 `createRecorder()` 方法：
    - 当 CameraConfig 包含 `SubStreamURL` 时，为 HLS 创建一个单独的 RTSP 连接到子码流
  - 修改 `internal/hls/manager.go`：
    - `StartStream()` 方法接受可选的 `subStreamURL` 参数
    - 当提供了 subStreamURL 时，创建独立的 RTSP client 连接到子码流
    - 子码流帧通过 OnHLSFrame 回调传递给 HLS muxer
    - 主码流继续用于录制
  - 子码流 HLS 连接独立于录制连接：
    - 录制：主 RTSP URL → Recorder → 文件
    - 直播：SubStreamURL → RTSP client → OnHLSFrame → HLS Manager
  - 如果 SubStreamURL 为空或连接失败：回退到主码流（现有行为）
  - 子码流连接失败时 log warning，不阻塞录制

  **Must NOT do**:
  - 不修改录制逻辑（录制始终用主码流）
  - 不做子码流自动检测
  - 不引入转码
  - 不影响没有配置 SubStreamURL 的摄像头

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 需要理解 RTSP 连接管理和 HLS 管道
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 10
  - **Blocked By**: Task 2

  **References**:

  **Pattern References**:
  - `internal/recorder/h264.go:230-280` — RTSP 连接建立和 RTP 处理，子码流连接需仿照此模式
  - `internal/hls/manager.go:62-90` — StartStream/StopStream，需扩展以支持子码流源
  - `internal/camera/manager.go` — createRecorder()，需在此添加子码流连接创建

  **API/Type References**:
  - `internal/config/config.go:CameraConfig` — SubStreamURL 字段（Task 2 添加）

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: 子码流连接并生成 HLS
    Tool: Bash
    Steps:
      1. 配置带 SubStreamURL 的摄像头
      2. 请求 HLS stream: curl -u admin:pass http://localhost:9090/api/cameras/{id}/stream/index.m3u8
      3. 断言 .m3u8 返回有效内容
      4. 请求第一个 .ts segment，断言非空
    Expected Result: HLS 使用子码流生成低码率流
    Evidence: .sisyphus/evidence/task-8-substream-hls.txt

  Scenario: 无子码流时回退主码流
    Tool: Bash
    Steps:
      1. 使用无 SubStreamURL 的摄像头
      2. 请求 HLS stream
      3. 断言仍正常工作（回退到主码流 OnHLSFrame）
    Expected Result: 行为与现有一致
    Evidence: .sisyphus/evidence/task-8-substream-fallback.txt
  ```

  **Commit**: YES
  - Message: `feat(hls): add sub-stream connection support`
  - Files: `internal/hls/manager.go, internal/camera/manager.go`

- [x] 9. HLS 帧率限制器（直播预览降帧）

  **What to do**:
  - 在 `internal/hls/manager.go` 中添加帧率限制逻辑：
    - 新增 `MaxFPS int` 配置项（默认 0 = 不限制）
    - 在 frame writer 中实现帧率控制：跟踪上一帧 PTS，如果距上一帧不足 1/MaxFPS 秒则丢弃
  - 仅影响 HLS 直播流，不影响录制
  - 添加到 `MergeConfig` 或单独的 `HLSConfig`（建议放在 camera 级别配置，如 `hls_max_fps`）
  - 实现方式：在 HLS manager 的 WriteFrame 方法中，检查帧间隔

  **Must NOT do**:
  - 不修改 H264Recorder/H265Recorder 的帧率
  - 不做解码/重编码
  - 不影响 MJPEG（MJPEG 有自己的 SampleInterval）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 需要理解 HLS 管道中的帧流处理
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 16
  - **Blocked By**: Task 2

  **References**:

  **Pattern References**:
  - `internal/hls/manager.go:220-240` — frame writer 逻辑，帧率限制在此处实现
  - `internal/hls/manager.go:235-239` — 非阻塞帧写入（buffer 满时丢帧），仿照此丢弃模式

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: HLS 帧率限制生效
    Tool: Bash
    Steps:
      1. 配置 hls_max_fps: 10
      2. 启动 HLS stream
      3. 收集 5 秒的 .ts segments
      4. 解析 segment 中的帧数，断言 <= 50 (10fps × 5s 容差)
    Expected Result: 帧率不超过配置值
    Evidence: .sisyphus/evidence/task-9-fps-limit.txt
  ```

  **Commit**: YES
  - Message: `feat(hls): add frame rate limiter for live preview`
  - Files: `internal/hls/manager.go, internal/config/config.go`

- [x] 10. Dashboard 前端改造 — 快照缩略图 + 子码流模式

  **What to do**:
  - 修改 `web/src/routes/Dashboard.svelte`：
    - **快照模式**：默认情况下，4 格大屏使用 JPEG 快照而非 HLS 流
      - 每隔 2 秒调用 `/api/cameras/{id}/snapshot` 更新缩略图
      - 用 `<img>` 显示快照，带 loading spinner
      - 快照失败时显示错误占位符
    - **点击放大**：单击某个格子时，该路切换为 HLS 实时流（全屏播放）
      - 双击或长按再次缩小回快照模式
    - **子码流模式**：如果摄像头支持子码流，HLS 自动使用子码流（后端处理）
    - **降级策略**：snapshot_url → sub_stream HLS → main stream HLS
  - 更新 `web/src/lib/api.ts`：
    - 添加 `getSnapshotUrl(cameraId: string): string` 方法
  - 快照定时器在组件销毁时清理（onDestroy）
  - 响应式布局保持不变（1/2/3/4 格）

  **Must NOT do**:
  - 不改变现有的路由和导航结构
  - 不修改 Header 组件
  - 不添加新页面

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 前端 UI 改造，涉及交互和视觉体验
  - **Skills**: [`/frontend-ui-ux`]
    - `/frontend-ui-ux`: Dashboard 交互体验设计

  **Parallelization**:
  - **Can Run In Parallel**: YES (partially)
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 14
  - **Blocked By**: Tasks 5, 8

  **References**:

  **Pattern References**:
  - `web/src/routes/Dashboard.svelte:1-441` — 完整的 Dashboard 组件，当前使用 HLS 全时流
  - `web/src/routes/Dashboard.svelte:233-254` — 当前 HLS player 初始化逻辑，需改为按需初始化
  - `web/src/routes/Dashboard.svelte:352-359` — 当前 video 元素，需添加 img 元素用于快照模式
  - `web/src/lib/api.ts` — API 客户端，添加快照 URL 方法

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: Dashboard 默认显示快照缩略图
    Tool: Playwright
    Preconditions: 应用已启动，有配置了 SnapshotURL 的摄像头
    Steps:
      1. 导航到 #/dashboard
      2. 等待页面加载
      3. 断言每个摄像头格子显示 <img> 元素（非 <video>）
      4. 等待 3 秒，断言 <img> 的 src 包含 /snapshot
    Expected Result: 默认显示快照而非 HLS 流
    Evidence: .sisyphus/evidence/task-10-dashboard-snapshots.png

  Scenario: 点击放大切换 HLS 实时流
    Tool: Playwright
    Steps:
      1. 在 Dashboard 快照模式下，点击某个摄像头格子
      2. 等待 HLS player 初始化
      3. 断言该格子显示 <video> 元素
      4. 断言 video 正在播放（currentTime > 0）
    Expected Result: 点击后切换为 HLS 实时播放
    Evidence: .sisyphus/evidence/task-10-dashboard-hls-switch.png
  ```

  **Commit**: YES
  - Message: `feat(ui): dashboard snapshot mode + sub-stream handling`
  - Files: `web/src/routes/Dashboard.svelte, web/src/lib/api.ts`

- [x] 11. MJPEG SampleInterval 接线到配置

  **What to do**:
  - 修改 `internal/recorder/mjpeg.go`：
    - 在 `MJPEGConfig` 中使用 `SampleInterval` 字段（已在 CameraConfig 中由 Task 2 添加）
    - 当前 `SampleInterval` 硬编码为 1（mjpeg.go:104），改为从配置读取
    - 默认值 1（保存所有帧），用户可配置为更大值（如 5 = 每 5 帧保存 1 帧）
  - 修改 `internal/camera/manager.go`：
    - 在 createRecorder 时将 CameraConfig.SampleInterval 传递给 MJPEGConfig

  **Must NOT do**:
  - 不修改 H264/H265 的帧率
  - 不修改 SampleInterval 的丢弃逻辑（已正确实现）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 极小的配置接线改动
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 14
  - **Blocked By**: Task 2

  **References**:

  **Pattern References**:
  - `internal/recorder/mjpeg.go:32` — SampleInterval 字段定义
  - `internal/recorder/mjpeg.go:104` — SampleInterval 使用处，当前从 config 读取但默认值问题
  - `internal/recorder/mjpeg.go:290` — `seq % r.cfg.SampleInterval != 0` 丢弃逻辑
  - `internal/camera/manager.go` — createRecorder 方法，传递 config 给 recorder

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: SampleInterval 配置生效
    Tool: Bash
    Steps:
      1. 配置 MJPEG 摄像头 sample_interval: 5
      2. 录制 100 帧
      3. 断言保存的 JPEG 文件数 ≈ 20 (100/5)
      4. 运行相关测试
    Expected Result: 只保存每第 5 帧
    Evidence: .sisyphus/evidence/task-11-sample-interval.txt
  ```

  **Commit**: YES
  - Message: `feat(recorder): wire mjpeg sample-interval to config`
  - Files: `internal/recorder/mjpeg.go, internal/camera/manager.go`
- [x] 12. main.go 接线合并管理器

  **What to do**:
  - 修改 `cmd/mibee-nvr/main.go`：
    - 在 CleanupManager 创建之后，创建 MergeManager
    - `mergeMgr := merge.NewMergeManager(db, store, cfg.Merge, camerasFunc)`
    - 在 goroutine 中启动 `mergeMgr.Run(ctx)`
    - 确保 mergeMgr 在 HTTP server 启动之前开始
  - 修改 cleanup 启动顺序：**先启动 merge，再启动 cleanup**（同周期内合并先于清理执行，避免竞态）
  - 确保 graceful shutdown 时 ctx cancel → mergeMgr 自动停止

  **Must NOT do**:
  - 不修改现有 subsystem 的启动顺序（除了 merge/cleanup 的相对顺序）
  - 不添加命令行参数

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 少量接线代码，遵循现有 main.go 模式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (sequential, depends on merge components)
  - **Blocks**: Task 13
  - **Blocked By**: Tasks 6, 7

  **References**:

  **Pattern References**:
  - `cmd/mibee-nvr/main.go` — 找到 CleanupManager 的创建和启动位置，MergeManager 紧随其后
  - `internal/cleanup/cleanup.go:20-40` — CleanupManager 构造函数，MergeManager 应有相同签名风格

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: MergeManager 正确启动和停止
    Tool: Bash
    Steps:
      1. 配置 merge.enabled: true
      2. 启动应用
      3. 检查日志包含 `[merge-manager] started`
      4. 发送 SIGTERM
      5. 检查日志包含 `[merge-manager] stopped`
    Expected Result: MergeManager 正确启动和优雅停止
    Evidence: .sisyphus/evidence/task-12-merge-wired.txt
  ```

  **Commit**: YES
  - Message: `feat(main): wire merge manager`
  - Files: `cmd/mibee-nvr/main.go`

- [x] 13. 合并 + 清理排序 + 边界情况处理

  **What to do**:
  - 修改 `internal/merge/manager.go` 的 RunOnce 添加边界处理：
    - **Pinned 录像保护**：合并窗口内遇到 pinned=1 的段，在 pinned 段处拆分窗口，分别合并左右部分
    - **SPS/PPS 变化边界**：解析每段的参数集，参数不同时拆分合并组
    - **段最小年龄**：只合并且 `ended_at` 距今超过 MinSegmentAge（如 10 分钟）的段，避免合并正在录制的段
    - **空段/损坏段**：跳过 file_size=0 或 frame_count=0 的段
    - **批量限制**：每次 RunOnce 最多处理 BatchLimit 个段（默认 200）
    - **磁盘空间检查**：合并前检查可用空间 > 预期合并大小的 110%
    - **合并非空验证**：合并后文件必须存在且 size > 0 且可被解析器读取
  - 确保合并和清理在同一周期内不冲突：
    - 在 RunOnce 中标记正在处理的段（如 DB 字段或内存标记）
    - 或：让 cleanup 检查段是否正在被合并
    - 最简方案：在 cleanup.RunOnce 开始前，先运行 merge.RunOnce

  **Must NOT do**:
  - 不修改 CleanupManager 代码（通过 main.go 启动顺序控制）
  - 不添加 DB schema 变更（不加 merging 状态字段）
  - 不做并行合并

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 边界情况处理需要细致的并发和错误处理
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 14)
  - **Parallel Group**: Wave 3
  - **Blocks**: Task 15
  - **Blocked By**: Task 12

  **References**:

  **Pattern References**:
  - `internal/cleanup/cleanup.go:85-120` — 现有清理逻辑的边界处理模式
  - `internal/storage/db.go:79` — pinned 字段，查询时过滤 pinned=0

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: Pinned 录像不被合并
    Tool: Bash
    Steps:
      1. 插入 10 段，其中第 5 段 pinned=1
      2. 运行 RunOnce
      3. 断言生成 2 个合并文件（前 4 段 + 后 5 段）
      4. 断言 pinned 段仍存在且未变
    Expected Result: Pinned 段保留，其余正确合并
    Evidence: .sisyphus/evidence/task-13-pinned-protection.txt

  Scenario: 磁盘空间不足时跳过合并
    Tool: Bash
    Steps:
      1. Mock 磁盘空间检查返回极小值
      2. 运行 RunOnce
      3. 断言无合并操作发生，日志包含 disk space warning
    Expected Result: 安全跳过，不创建文件
    Evidence: .sisyphus/evidence/task-13-disk-check.txt
  ```

  **Commit**: YES
  - Message: `feat(merge): add edge case handling + pinned protection`
  - Files: `internal/merge/manager.go`

- [x] 14. 前端 Dashboard 集成测试

  **What to do**:
  - 确认 Dashboard 前端的所有改造正确集成：
    - 快照模式 → 点击放大 HLS → 缩小回快照的完整流程
    - 不支持快照的摄像头仍使用 HLS 模式
    - 不支持子码流的摄像头仍使用主码流
    - MJPEG 摄像头显示正确状态
  - 添加前端交互细节优化：
    - 快照加载中显示 spinner
    - 快照失败显示占位符 + 错误信息
    - HLS 连接中显示 loading 状态
    - 移动端响应式保持正确

  **Must NOT do**:
  - 不修改后端逻辑
  - 不添加新页面或路由

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 前端 UI 集成和交互优化
  - **Skills**: [`/frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 13)
  - **Parallel Group**: Wave 3
  - **Blocks**: Task 16
  - **Blocked By**: Tasks 10, 11

  **References**:

  **Pattern References**:
  - `web/src/routes/Dashboard.svelte` — Task 10 改造后的 Dashboard 组件

  **Acceptance Criteria**:

  **QA Scenarios:**
  ```
  Scenario: Dashboard 快照→HLS→快照 完整流程
    Tool: Playwright
    Steps:
      1. 导航到 #/dashboard
      2. 断言显示快照缩略图（<img> 元素）
      3. 点击第一个摄像头格子
      4. 等待 3 秒，断言切换为 HLS (<video> 元素)
      5. 点击缩小按钮（或双击）
      6. 断言回到快照模式
    Expected Result: 快照↔HLS 切换流畅
    Evidence: .sisyphus/evidence/task-14-dashboard-flow.png
  ```

  **Commit**: YES
  - Message: `feat(ui): dashboard integration polish`
  - Files: `web/src/routes/Dashboard.svelte`

- [x] 15. 合并功能单元/集成测试

  **What to do**:
  - 为 `internal/merge/` 包编写完整测试：
    - `manager_test.go`: RunOnce 逻辑测试（pinned, SPS/PPS 边界, 空段, 批量限制）
    - `mp4merge_test.go`: MP4 合并测试（H264 合并, H265 合并, 不同参数拒绝）
    - `mjpegmerge_test.go`: MJPEG 目录合并测试
    - `parser_test.go`: MP4 解析器测试
  - 使用 `testify/require` 断言风格
  - 测试辅助函数使用 `t.Helper()`
  - 测试 MP4 合并：用 MP4Muxer 创建测试段 → 合并 → 解析验证
  - 测试 Dashboard 快照交互
  - 运行 `rtk go test ./internal/merge/... -v` 全部通过

  **Must NOT do**:
  - 不使用 `testify/assert`（仅用 `require`）
  - 不跳过 `t.Helper()` 在 helper 函数中

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 需要创建完整的测试基础设施和测试段文件
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 16)
  - **Parallel Group**: Wave 4
  - **Blocks**: F1-F4
  - **Blocked By**: Task 13

  **References**:

  **Pattern References**:
  - `internal/cleanup/cleanup_test.go` — CleanupManager 测试模式
  - `internal/storage/db_test.go` — DB 测试模式
  - `tests/integration_test.go` — 集成测试模式

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/merge/... -v` → PASS (0 failures)

  **QA Scenarios:**
  ```
  Scenario: 完整合并测试套件通过
    Tool: Bash
    Steps:
      1. 运行 `rtk go test ./internal/merge/... -v`
      2. 断言所有测试 PASS，无 FAIL 或 SKIP
    Expected Result: 全部测试通过
    Evidence: .sisyphus/evidence/task-15-merge-tests.txt
  ```

  **Commit**: YES
  - Message: `test(merge): add unit and integration tests`
  - Files: `internal/merge/*_test.go`

- [x] 16. 省流功能测试

  **What to do**:
  - 为省流功能编写测试：
    - 子码流 HLS 连接测试
    - HLS 帧率限制测试
    - 快照 API 缓存测试
    - MJPEG SampleInterval 测试
  - 运行 `rtk go test ./internal/hls/... -v` 和相关测试全部通过

  **Must NOT do**:
  - 不使用 `testify/assert`（仅用 `require`）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 需要理解 HLS 和 RTSP 测试模式
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 15)
  - **Parallel Group**: Wave 4
  - **Blocks**: F1-F4
  - **Blocked By**: Task 14

  **References**:

  **Pattern References**:
  - `internal/hls/manager_test.go` — 现有 HLS 测试模式（如存在）
  - `internal/recorder/mjpeg_test.go` — MJPEG 测试模式（如存在）

  **Acceptance Criteria**:
  - [ ] `rtk go test ./internal/hls/... -v` → PASS
  - [ ] `rtk go test ./internal/api/... -run TestSnapshot -v` → PASS

  **QA Scenarios:**
  ```
  Scenario: 省流测试套件通过
    Tool: Bash
    Steps:
      1. 运行 `rtk go test ./internal/hls/... ./internal/api/... -v`
      2. 断言所有测试 PASS
    Expected Result: 全部通过
    Evidence: .sisyphus/evidence/task-16-optimize-tests.txt
  ```

  **Commit**: YES
  - Message: `test(hls): add traffic optimization tests`
  - Files: `internal/hls/*_test.go, internal/api/*_test.go`

## Final Verification Wave

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `rtk go vet ./...` + `rtk go test ./...`. Review all changed files for: empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high` (+ `playwright` skill)
  Start from clean state. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff. Verify 1:1 — everything in spec was built, nothing beyond spec. Check "Must NOT do" compliance. Detect cross-task contamination.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | VERDICT`

---

## Commit Strategy

- **Wave 1**: `feat(ui): logo link to dashboard + gradient color` — Header.svelte
- **Wave 1**: `feat(config): add merge, sub-stream, snapshot, sample-interval config` — config.go
- **Wave 1**: `feat(storage): add merge query methods` — db.go
- **Wave 1**: `feat(merge): add mp4 segment parser` — merge/parser.go
- **Wave 1**: `feat(api): add snapshot endpoint with caching` — handler.go
- **Wave 2**: `feat(merge): add h264/h265 mp4 merge logic` — merge/mp4merge.go
- **Wave 2**: `feat(merge): add mjpeg directory merge logic` — merge/mjpegmerge.go
- **Wave 2**: `feat(hls): add sub-stream connection support` — hls/manager.go
- **Wave 2**: `feat(hls): add frame rate limiter for live preview` — hls/manager.go
- **Wave 2**: `feat(ui): dashboard snapshot mode + sub-stream handling` — Dashboard.svelte
- **Wave 2**: `feat(recorder): wire mjpeg sample-interval to config` — mjpeg.go
- **Wave 3**: `feat(main): wire merge manager + cleanup ordering` — main.go
- **Wave 3**: `feat(merge): add edge case handling + pinned protection` — merge/
- **Wave 4**: `test(merge): add unit and integration tests` — merge/
- **Wave 4**: `test(hls): add traffic optimization tests` — hls/

---

## Success Criteria

### Verification Commands
```bash
rtk go test ./internal/merge/... -v           # Expected: PASS — all merge tests
rtk go test ./internal/hls/... -v             # Expected: PASS — HLS sub-stream tests
rtk go test ./internal/storage/... -v         # Expected: PASS — DB merge query tests
rtk go vet ./...                               # Expected: no issues
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All tests pass
- [ ] Logo navigates to dashboard
- [ ] Merged MP4 playable in browser
- [ ] Sub-stream reduces bandwidth when configured
- [ ] Snapshot thumbnails render on dashboard
