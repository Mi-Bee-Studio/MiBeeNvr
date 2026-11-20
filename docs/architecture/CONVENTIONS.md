# 代码与目录规范（CONVENTIONS）

> 本文是仓库结构与代码组织的**成文规范**。新代码一律遵守；review 时规范是拒收依据。

## 1. 仓库根目录白名单

只允许出现下列目录（新增需在本文件登记并说明理由）：

| 目录 | 职责 |
|---|---|
| `cmd/` | 可执行入口（`cmd/mibee-nvr`） |
| `internal/` | 产品代码，见 §2 |
| `web/` | Svelte SPA 源码 |
| `docs/` | 开发侧文档（`architecture/` 设计深潜；`zh` `en` 用户手册） |
| `deploy/` | 部署形态（docker / compose / fnos / grafana / home-assistant） |
| `custom_components/` | Home Assistant 集成（**HACS 硬约束**：必须在仓库根） |
| `tests/` | 跨包集成测试 + 仓库检查脚本（见 §4） |

禁止：`scripts/`、`tools/`、`dist/`、`dist2/`、`e2e-tests/`、临时探针目录。构建产物只允许
落在 `build/`（gitignored）与 `internal/ui/static/`（SPA 嵌入点）。

## 2. 包边界（`internal/`）

- **一个包一个职责**，包名即职责，不用 `util`/`common`/`helpers` 这类无信息名。
- **依赖方向**：叶子包（`model`、`config`、`slogx`、`httpx`、`backoff` 等）
  可被任意包引用；业务包之间**禁止双向依赖**。
- **跨域编排走 `app/`**：两个业务域需要协作时，由 `app` 装配，不要让
  `internal/a` 直接依赖 `internal/b` 且 `internal/b` 又依赖 `internal/a`。
- **`internal/api` 按域拆包**：`{streams,recordings,cameras,gb,system,relay,notify}api`
  各自暴露 `Register(r chi.Router, deps Deps)`，根 `Handler` 只做装配与依赖注入；
  域间不互相 import，跨域调用走 Deps 函数钩子。
- **全仓不得 import `pkg/*`**（该树已删除，CI 强制）。
- 认证/授权/限流类代码归 **`internal/auth`**，不要散落在 `internal/middleware`。

## 3. 文件规模

| 阈值 | 要求 |
|---|---|
| > 800 行 | review 时需说明为何不拆 |
| > 1,200 行 | **CI 拒绝新增**（存量持续递减） |

拆分必须满足：

- **纯移动，零逻辑改动**（一个 PR 只做一件事，行为零变化）。
- 先确认该文件有测试覆盖，再动。
- 拆完跑该包全量测试，不得只看编译通过。
- 新文件名要能自解释职责（如 `paramsets.go`、`observers.go`），不用 `part1.go`。

## 4. 测试位置

| 位置 | 类型 |
|---|---|
| `internal/<pkg>/*_test.go` | 单元测试，可访问包内私有符号 |
| `tests/` | 跨包装配的集成测试；CI 引用的仓库检查脚本也在这里（见其 README） |
| `web/src/__tests__/` | vitest 前端单测 |

新增有外部行为的功能，测试必须同 PR；无测试的 PR 需在描述里说明理由。

## 5. 没有 `pkg/` 树

`pkg/{camera,eventbus,streamhub}` 曾是为「仓外商业 P2P 模块」预留的契约层；
该模块并入 `internal/p2psdk` 后，契约层与三个 adapter 成了无消费者的空壳，已删除。
`app.RegisterValue` 现在注册具体类型（`*camera.CameraManager` 等）。

**如需恢复对外契约**：必须先有真实的仓外消费者与版本承诺，经维护者批准后按
当时的实际需求重新设计接口——不要凭空恢复旧镜像。CI 守卫会拒绝任何
`MiBeeNvr/pkg/` 引用与根目录 `pkg/`。

## 6. SPA 构建

唯一链路：`make frontend`（`web/dist` → 清空再填充 `internal/ui/static/`）。
不存在第二条输出路径。换芯必须重建 SPA，禁止复制旧产物（见平台文档 §6）。

## 7. 提交与产物

- 提交前 `git status` 核对**只含本次改动**；并行会话在制品不得顺手 `git add`。
- 一切内容按「明天就公开」标准写：无凭据、无内网地址、无生产标识、无商业数字。
- 编译产物发布前 grep 敏感词。
- **文档同规**：内部方案/迁移路径/执行台账/研究底稿不进公开仓；架构文档只描述
  「系统现在是什么样、为什么这样设计」，不保留过程性计划与决策记录。
