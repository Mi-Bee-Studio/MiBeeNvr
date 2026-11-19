# 代码与目录规范（CONVENTIONS）

> 本文是**成文规范**，与 `repo-governance-plan.md`（执行台账）配套。新代码一律遵守；
> 存量按台账 Phase 3 逐步收敛。review 时规范是拒收依据。

## 1. 仓库根目录白名单

只允许出现下列目录（新增需在本文件登记并说明理由）：

| 目录 | 职责 |
|---|---|
| `cmd/` | 可执行入口（`cmd/mibee-nvr`） |
| `internal/` | 产品代码，见 §2 |
| `pkg/` | 对外契约接口（见 §5） |
| `web/` | Svelte SPA 源码 |
| `docs/` | 开发侧文档（`architecture/` 设计深潜；`zh` `en` 用户手册） |
| `deploy/` | 部署形态（docker / compose / fnos / grafana） |
| `devtools/` | 仓库维护脚本（CI 引用的检查工具） |
| `custom_components/` | Home Assistant 集成（**HACS 硬约束**：必须在仓库根） |
| `tests/` | 跨包集成测试（见 §4） |

禁止：`scripts/`、`tools/`、`dist/`、`dist2/`、`e2e-tests/`、临时探针目录。构建产物只允许
落在 `build/`（gitignored）与 `internal/ui/static/`（SPA 嵌入点）。

## 2. 包边界（`internal/`）

- **一个包一个职责**，包名即职责，不用 `util`/`common`/`helpers` 这类无信息名。
- **依赖方向**：叶子包（`model`、`config`、`slogx`、`httpx`、`backoff`、`fadivse` 等）
  可被任意包引用；业务包之间**禁止双向依赖**。
- **跨域编排走 `app/`**：两个业务域需要协作时，由 `app` 装配，不要让
  `internal/a` 直接依赖 `internal/b` 且 `internal/b` 又依赖 `internal/a`。
- **`internal/*` 与全仓不得 import `pkg/*`**（pkg/ 已删除，CI 强制）。
- 认证/授权/限流类代码归 **`internal/auth`**，不要散落在 `internal/middleware`。

## 3. 文件规模

| 阈值 | 要求 |
|---|---|
| > 800 行 | review 时需说明为何不拆 |
| > 1,200 行 | **CI 拒绝新增**（存量按台账 Phase 3 递减） |

拆分必须满足：

- **纯移动，零逻辑改动**（一个 PR 只做一件事，行为零变化）。
- 先确认该文件有测试覆盖，再动。
- 拆完跑该包全量测试，不得只看编译通过。
- 新文件名要能自解释职责（如 `paramsets.go`、`observers.go`），不用 `part1.go`。

## 4. 测试位置

| 位置 | 类型 |
|---|---|
| `internal/<pkg>/*_test.go` | 单元测试，可访问包内私有符号 |
| `tests/` | 跨包装配的集成测试 |
| `web/src/__tests__/` | vitest 前端单测 |

新增有外部行为的功能，测试必须同 PR；无测试的 PR 需在描述里说明理由。

## 5. `pkg/` 已移除（2026-11-19，治理 4.3）

`pkg/{camera,eventbus,streamhub}` 曾是为「仓外商业 P2P 模块」预留的契约层；
该模块已于 2026-10 并入 `internal/p2psdk`，契约层与三个 adapter 成了无消费者的
空壳，已删除。`app.RegisterValue` 现在注册具体类型（`*camera.CameraManager` 等）。

**如需恢复对外契约**：必须先有真实的仓外消费者与版本承诺，经用户裁定后按
当时的实际需求重新设计接口——不要凭空恢复旧镜像。CI 守卫会拒绝任何
`MiBeeNvr/pkg/` 引用与根目录 `pkg/`。

## 6. SPA 构建

唯一链路：`make frontend`（`web/dist` → 清空再填充 `internal/ui/static/`）。
不存在第二条输出路径。换芯必须重建 SPA，禁止复制旧产物（见平台文档 §6）。

## 7. 提交与产物

- 提交前 `git status` 核对**只含本次改动**；并行会话在制品不得顺手 `git add`。
- 一切内容按「明天就公开」标准写：无凭据、无内网地址、无生产标识、无商业数字。
- 编译产物发布前 grep 敏感词。

## 8. 现有待收敛项

见 `repo-governance-plan.md`。当前已知偏离：

- `internal/api` 73 文件单包（拆分方案见 `api-package-split.md`，Step 1 `httpx` 已完成）
- `internal/{camera,event,streamhub}/adapter.go` 反向依赖 `pkg/`
- AGENTS.md 尚未瘦身
