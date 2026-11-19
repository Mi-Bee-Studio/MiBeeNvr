# tests/ — 跨包集成测试

本目录**只放跨包/端到端形态的 Go 集成测试**（需要把多个子系统装配起来才能验证的行为）。

## 与包内测试的分工

| 位置 | 放什么 |
|---|---|
| `internal/<pkg>/*_test.go` | **单元测试**——贴着被测包放，可访问包内私有符号。这是绝大多数测试的归宿。 |
| `tests/`（本目录） | **集成测试**——跨包装配、真实文件/进程形态、端到端数据通路。 |
| `e2e-tests/` | Playwright 浏览器 E2E（当前未接入 CI，见 `docs/architecture/repo-governance-plan.md` Phase 2.3）。 |

## 怎么跑

```bash
# 本目录全部
go test ./tests/... -count=1

# 只跑子目录里的集成用例
go test ./tests/integration/... -count=1
```

CI 的 `test` job 用 `go list ./...` 分片，已覆盖本目录；本地跑全量时若只写了
`./internal/...` 会漏掉这里，注意别漏。

## 编写约定

- 用 `testing.Short()` 跳过需要真实磁盘/网络的用例，保证 `-short` 仍然快。
- 临时目录用 `t.TempDir()`，不要写仓库内路径。
- 不引入外部服务依赖；需要真机的用例请放到 `tmp/` 的探针脚本里，不进 CI。
