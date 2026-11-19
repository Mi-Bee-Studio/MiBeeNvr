# 仓库架构治理总计划（2026-11-19 定案 · 执行中）

> 触发：用户指令「这些全部问题立即开始优化，包括代码的长度切割，和代码的架构规范等，
> 都做一个完整的计划出来全部一次性解决」。
>
> 本文档是**唯一执行台账**。每个 Phase 独立可合入，完成即勾选并记录 PR 号。
> 与既有定案的关系：`api-package-split.md`（2026-10-23）是本计划 Phase 3 的输入，
> 本计划不推翻它，只补齐触发条件并把它排进总序。

---

## 诊断基线（2026-11-19 实测）

| 指标 | 数值 |
|---|---|
| Go 产品码 | 166,616 行（不含测试/前端/tmp-probe） |
| `internal/` 包数 | 76 个 |
| `internal/api` | 73 源文件 / 21,810 行产品码 / 25,629 行测试 / 107 测试文件 |
| `internal/api` 外部依赖 | 84 个包 |
| `handler.go` | 1,207 行 / 48 条路由 |
| 最大 Go 文件 | `merge/mp4merge.go` 1,862 行 |
| 最大前端文件 | `routes/Recordings.svelte` 1,699 行 |
| 仓库体积 | 15 GB（tmp-probe 11 GB / build 1.8 GB / node_modules 2.1 GB / dist 112 MB） |

## 六类问题（按严重度）

1. **P0 磁盘与产物失控**：`tmp-probe` 11 GB（12,349 文件）、`build/` 1.8 GB 历史二进制、
   `dist/` 9 月旧安装包、`web/dist2/` 8 月旧 SPA。同一 SPA 有 4 个输出位置
   （`web/dist`、`web/dist2`、`dist/`、`internal/ui/static/`），是 1009/1101 陈旧 SPA
   事故的土壤。
2. **P0 私有物在公开工作树**：`docs/private/`（release-test-checklist.md）靠 gitignore 防守。
3. **P1 孤儿目录**（**已解决 2026-11-19**）：`e2e-tests/`（6 个 Playwright spec，
   无 package.json/无 config/CI 零引用）已删除；`tools/`（仅 1 个 README）已迁入
   `docs/architecture/`；`tests/` 已补 README 澄清职责，与包内单测的分工明确。
4. **P1 命名空间混乱**：认证/限流住 `internal/middleware/`（非 `internal/auth`）；
   `pkg/{camera,eventbus,streamhub}` 镜像与 `internal/` 同名且 internal 反向依赖 pkg。
5. **P2 上帝包与巨型文件**：`internal/api` 73 文件单包；8 个 Go 文件 >1,200 行；
   3 个前端文件 >1,550 行。
6. **P2 规范缺失**：无「包边界/命名/目录」成文规范；无 CI 守卫（行数、目录白名单）；
   AGENTS.md 206 KB 靠堆叠历史，检索价值衰减。

---

## Phase 0 — 卫生治理（1 PR，零代码风险）

- [ ] **0.1 构建产物清理**
  - `build/`：保留最新 `mibee-nvr-arm64`（11-18），删除其余 32 个历史二进制（~1.7 GB）
  - `dist/`：删除（2 个 9 月旧安装包，正式产物在 GitHub Release）
  - `web/dist2/`：删除（8 月旧 SPA，无任何构建脚本引用）
  - 验收：`make build` 仍绿；`git status` 干净
- [ ] **0.2 tmp-probe 分层清理**（先报告后删，需用户确认数据类目录）
  - 可删：`edge-profile-*`（浏览器 profile，618 MB×2+）、`wsltests`（220 MB）、`__pycache__`
  - 待确认：`y26-compare`（2.7 GB 评测语料）、`promo_20261110`（2.0 GB 宣传底稿）
- [ ] **0.3 私有物出仓**
  - `docs/private/release-test-checklist.md` → 迁到 `knowledge-base/`（内部仓）或工作区根 `tmp/`
  - 从 `.gitignore` 移除 `docs/private/` 条目（目录已不存在）
- [ ] **0.4 杂项**
  - 删根 `package-lock.json`（空壳 `{"packages":{}}`，已被 ignore）
  - `internal/transcoding/-decoders` / `-encoders`（各 13 字节的误建文件）删除并清 .gitignore

## Phase 1 — 缺陷修复（2 PR）

- [x] **1.1 Gitea #57**：`handlers_camera.go` 构造 `CameraUpdate` 时补
  `StableID` / `SubnetHints` 映射 + 回归测试（PUT 合法值后 GET 可读回）
- [x] **1.2 Gitea #58**：`rediscovery` 候选覆盖
  - 改为「与旧 IP 数值距离优先」排序 + 跨轮 round-robin 游标，避免每轮从 .1 重扫
  - 单测：1016 候选 × 30s 预算下，多轮累计覆盖全部候选
  - 随修：M5 上该相机的 `stable_id` 用修好的 API 回填为真实 SN `88492D665CCF`

## Phase 2 — 目录规范（1-2 PR）

- [x] **2.1 `tools/` 去留**：`tools/eval/README.md`（评测框架说明）→
  `docs/architecture/eval-replay.md`，删空目录
- [x] **2.2 `scripts/`**：CI 引用 `scripts/check-docs-links.py`（ci.yml:141），
  与 `tools/` 并存的单文件目录 → 合并为 `devtools/`（同步改 ci.yml + CONTRIBUTING.md）
- [x] **2.3 `e2e-tests/` 处置** —— **已删除**（用户裁定 2026-11-19：无法为 TDD 提供
  有效反馈即可移除）。6 个 spec 自 8 月起未运行、无 package.json/playwright.config、
  CI 零引用；等价覆盖由 Go 集成测试 + `web/src/__tests__` + 真机探针承担。
- [x] **2.4 `tests/` 命名**：加 `tests/README.md` 说明「这里是跨包集成测试，包内单测贴包放」；
  CI 显式列出 `./tests/...` 保证不被分片逻辑遗漏
- [x] **2.5 SPA 产物单一链路**：确立 `web/dist` → `internal/ui/static` 唯一方向
  （Makefile:27 已是此路径），在 `docs/architecture/platform.md` 写明并加注释

## Phase 3 — 代码切割（多 PR，按风险升序）

### 3.1 大文件切割（8 个目标，每文件 1 PR 或合并为 2-3 PR）—— ✅ 全部完成

| 文件 | 行数 | 建议切法 |
|---|---|---|
| 文件 | 原 → 现 | 拆出 |
|---|---|---|
| `merge/mp4merge.go` | 1862 → **995** | mp4moov.go, mp4writers.go |
| `xiaomi/recorder.go` | 1639 → **1502** | recorder_report.go |
| `hls/manager.go` | 1498 → **1153** | paramsets.go, substream_reader.go |
| `storage/db_recording.go` | 1457 → **953** | query_helpers.go, db_recording_paths.go |
| `recorder/base.go` | 1320 → **1009** | segment_files.go, metrics_stats.go |
| `config/validate.go` | 1295 → **1052** | validate_camera.go |
| `metrics/metrics.go` | 1220 → **971** | observers.go |
| `p2p/manager.go` | 1148 → **922** | observers.go |
| `api/handler.go` | 1207 → **950** | handler_setters.go, handler_gb28181.go |
| `web/lib/api/cameras.ts` | 1431 → **413** | camera-types.ts, camera-ptz.ts |

全部降至 1200 行阈值以下；后续批次继续收敛，**最终全仓 Go 产品文件均 ≤ 1000 行**
（最大 995 行 `merge/mp4merge.go`）。追加批次：

| 文件 | 原 → 现 | 拆出 |
|---|---|---|
| `storage/db.go` | 1134 → **676** | db_schema_migrations.go |
| `app/register.go` | 1118 → **813** | register_protocols.go |
| `api/handlers_settings.go` | 1111 → **916** | handlers_api_keys.go |
| `webrtc/manager.go` | 1109 → **966** | congestion.go, peer_types.go |
| `storage/manager.go` | 1023 → **191** | manager_segments.go |
| `api/handlers_onvif.go` | 1030 → **228** | handlers_ptz_vectors.go |
| `xiaomi/cloud.go` | 1046 → **913** | cloud_crypto.go |
| `muxer/mp4mux.go` | 1087 → **454** | mp4boxes.go |
| `xiaomi/recorder.go` | 1502 → **944** | recorder_packets.go, recorder_device.go |
| `hls/manager.go` | 1153 → **602** | manager_lifecycle.go |
| `p2psdk/p2p.go` | 1057 → **798** | webrtc_tuning.go |
| `config/validate.go` | 1051 → **96** | validate_details.go |
| `streamhub/streamhub.go` | 1008 → **888** | consumers.go |
| `recorder/base.go` | 1320 → **888** | segment_files.go, metrics_stats.go, adaptive_triggers.go |

> 切割纪律：只做**文件内移动**，不改逻辑、不改行为；每步 `go build` + 包内测试绿；
> 有测试的文件先确认测试覆盖再动。

### 3.2 `internal/api` 拆分（接 `api-package-split.md`）

- [x] Step 1 `httpx` 叶子包 —— **已完成**（`internal/httpx/httpx.go`，api/upload 双方在用）
- [x] 前置 golden 路由测试 —— **已完成**（285 条 method+path 快照，`routes_golden_test.go`）
- [ ] Step 2 逐域迁移：`streams` → `recordings` → `cameras` → `system` → `gb`
  - 每域暴露 `Register(r chi.Router, deps Deps)`，`Handler` 只留聚合
  - **前置**：先补 golden 路由表测试（`Routes()` 的 method+path 快照）
- [ ] Step 3 测试随域走
- [ ] Step 4 `handler.go` 收敛到 <400 行纯装配
- 验收：每步 CI 四绿 + 路由行为零变化

### 3.3 前端切割 ✅ 纯逻辑已抽出

| 文件 | 行数 | 建议切法 |
|---|---|---|
| 文件 | 原 → 现 | 抽出 |
|---|---|---|
| `routes/Recordings.svelte` | 1699 → **1673** | lib/recordings-windows.ts |
| `routes/recordings/PlaybackPanel.svelte` | 1675 → **1657** | lib/playback-helpers.ts |
| `lib/components/CameraForm.svelte` | 1590 → **1568** | lib/camera-form-helpers.ts |

纯逻辑（无响应式耦合）已外移并全部配单测（+25 tests）。剩余体量是 Svelte
模板与状态编排，进一步拆分需引入子组件边界，风险高于收益，暂缓。

> 依赖 `web/src/lib/api/cameras.ts`（1,431 行）先按资源域拆分。

## Phase 4 — 规范固化（1 PR）

- [ ] **4.1 `docs/architecture/CONVENTIONS.md`**（新增，成文规范）
  - 包边界：`internal/<domain>` 只允许向内依赖 `model`/`config`/`slogx` 等叶子；
    跨域调用走 `app` 层装配，禁止 `internal/a` ← `internal/b` 双向
  - 命名：认证/授权类代码归 `internal/auth`（不在 `middleware`）；`pkg/` 只放真正的
    对外契约，且 internal 不得反向 import `pkg/`
  - 目录：仓库根只允许 `cmd/ internal/ pkg/ web/ docs/ deploy/ scripts|devtools/ custom_components/`（HACS 硬约束）
  - 文件规模：单文件 ≤800 行警告 / ≤1,200 行 CI 失败（新代码）；存量按 Phase 3 递减
- [ ] **4.2 CI 守卫**
  - 新增 `.github/workflows/repo-guard.yml`：目录白名单 + 新增文件行数上限 + 禁止
    `internal/*` import `pkg/*` 的 grep 断言
- [x] **4.3 `pkg/` 边界裁定** —— **选项 A 执行完毕（用户裁定 2026-11-19）**
  - 删除 `pkg/{camera,eventbus,streamhub}` 三个包与
    `internal/{camera,event,streamhub}/adapter.go`（及对应测试）——无仓外消费者
    （商业 P2P 已并入 internal/p2psdk，直接依赖 internal 实现）
  - `app.RegisterValue` 改注册具体类型（`*camera.CameraManager` / `*event.EventBus`），
    doc.go 示例同步
  - repo-guard：`pkg/` 列入禁止根目录；`MiBeeNvr/pkg/` 任何引用直接报错（基线归零）
- [ ] **4.4 AGENTS.md 瘦身**
  - 206 KB 编年史 → 保留「当前事实 + 索引」，历史迁 `docs/architecture/history/` 或
    dev-journal；根文件目标 ≤30 KB

## Phase 5 — 收尾

- [ ] `make build` + WSL `go test ./...` 全绿
- [ ] 全量五类敏感扫描（凭据/端点/私有仓名/生产标识/商业数字）
- [ ] dev-journal 记一条（含根因与本次裁定）
- [ ] M5 验证（若涉及运行时行为）

---

## 执行顺序与依赖

```
Phase 0（卫生）          —— 无依赖，先做
   ↓
Phase 1（缺陷）          —— 无依赖，可与 0 并行
   ↓
Phase 2（目录）          —— 依赖 0 完成（避免挪动已删目录）
   ↓
Phase 3.1（大文件）      —— 依赖 2（目录稳定后再切文件）
Phase 3.2（api 拆分）    —— 独立，需 golden 路由测试先行
Phase 3.3（前端）        —— 独立
   ↓
Phase 4（规范固化）      —— 依赖 3 的经验数据（行数阈值要实测校准）
```

## 需要用户裁定的 3 个点

1. **`tmp-probe` 内容目录**（`y26-compare` 2.7 GB 评测语料 / `promo_20261110` 2.0 GB 宣传底稿）
   —— 删还是留？宣传成品是否已全部迁到 `D:\Projects\宣传材料`？

---
*台账维护：每完成一项勾选并追加 PR 号；发现新问题追加到对应 Phase 而非另开文档。*
