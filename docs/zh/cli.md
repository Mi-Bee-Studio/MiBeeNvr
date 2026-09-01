# CLI 用户手册

> 适用于 MiBeeNvr v0.12.0 · 命令名 `mibee-nvr`（预编译包可能带架构后缀，如 `mibee-nvr-amd64`）

MiBee NVR 是「单二进制 + 子命令」形态：**不带子命令直接运行即启动服务器**，带子命令则执行对应的管理工具后退出。

```bash
mibee-nvr              # 启动 NVR 服务器（长驻进程）
mibee-nvr <子命令>      # 执行管理工具，完成后退出
mibee-nvr -version     # 打印版本
```

## 启动服务器

```bash
mibee-nvr -config mibee-nvr.yaml
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-config` | `mibee-nvr.yaml` | 配置文件路径 |
| `-version` | — | 打印版本后退出 |

> 没有配置文件时启动会**自动初始化**，浏览器打开后进入[初始化向导](wizard.md)。

## 子命令总览

| 子命令 | 用途 |
|--------|------|
| [`init`](#init-生成配置) | 交互式生成配置文件和管理员账户 |
| [`hash-password`](#hash-password-生成密码哈希) | 生成密码哈希 |
| [`health`](#health-健康检查) | HTTP 健康探测（Docker HEALTHCHECK 用） |
| [`encrypt-config`](#encrypt-config-加密敏感字段) | 加密配置中的明文密码 |
| [`download-model`](#download-model-下载-ai-模型) | 下载浏览器端 AI 检测模型 |
| [`merge-cameras`](#merge-cameras-合并摄像头) | 合并两个重复的摄像头条目 |
| [`repair`](#repair-数据修复) | 数据修复工具集（7 个子命令） |
| [`cleanup`](#cleanup-录像清理) | 按日期 / 孤儿文件清理录像 |

---

## init — 生成配置

生成配置文件并设置管理员账户：

```bash
mibee-nvr init --password 你的密码
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--password` | （交互输入） | 管理员密码，**至少 8 位**；未提供时在终端提示输入 |
| `--username` | `admin` | 管理员用户名 |
| `--data-dir` | `/var/lib/mibee-nvr` | 数据目录（录像 + 数据库） |
| `--listen` | `:9090` | HTTP 监听地址 |
| `--config` | `mibee-nvr.yaml` | 配置文件输出路径 |
| `--force` | — | 覆盖已存在的配置文件（否则报错退出） |

生成的配置默认值：片段时长 `30s`、保留 30 天、FTP 2121、WebDAV `/dav`。

> 也可以完全跳过 `init`：无配置启动 → Web 向导完成（参阅[初始化向导](wizard.md)）。

## hash-password — 生成密码哈希

```bash
mibee-nvr hash-password '你的密码'
# 输出: $2a$10$...
```

把输出粘贴到配置文件的 `auth.password_hash` 字段，适合在脚本中批量初始化。

## health — 健康检查

对本地服务做 HTTP 探测（`GET /api/health`），成功退出码 0，失败退出码 1 —— 供 Docker `HEALTHCHECK` / systemd watchdog 使用：

```bash
mibee-nvr health                 # 探测 :9090
mibee-nvr health --addr :9191    # 显式指定地址
mibee-nvr health --config /data/mibee-nvr.yaml   # 从配置读取 server.listen
```

地址解析优先级：`--addr` > `--config` 中的 `server.listen` > Docker 自动探测（读 `NVR_DATA_DIR` 数据目录下的配置）> 默认 `:9090`。**host 网络模式下改过监听端口时无需加参数**，命令会自动找到真实端口。

## encrypt-config — 加密敏感字段

把配置文件中的**明文敏感字段**（摄像头密码等）就地加密：

```bash
mibee-nvr encrypt-config --config mibee-nvr.yaml
```

输出加密了哪些字段；已是密文或为空的字段会跳过。加密后服务照常读取，人工无法直接看到密码明文。

## download-model — 下载 AI 模型

下载浏览器端 AI 检测所需的 ONNX 模型（YOLOv11-nano，约 5.4MB，来自 Ultralytics 官方发布）到 Web 静态目录：

```bash
mibee-nvr download-model --config mibee-nvr.yaml
```

内置 5 次指数退避重试和大小 / 完整性校验，适合离线环境预下载后随包分发。模型用于[浏览器端 AI 检测](ai-detection-tuning.md)。

## merge-cameras — 合并摄像头

把两个重复的摄像头条目**端到端合并**（例如同一台设备被 ONVIF 发现和小米接入各加了一次）：

```bash
# 先预览（默认 dry-run）
mibee-nvr merge-cameras --source cam-old --target cam-new

# 确认无误后执行
mibee-nvr merge-cameras --source cam-old --target cam-new --execute
```

执行步骤：备份数据库 → 改写录像 / 事件的摄像头归属与文件路径 → 移动录像文件 → 从配置中移除源摄像头 → 删除源摄像头数据库行。**任一步失败自动回滚**。

| 参数 | 说明 |
|------|------|
| `--source <id>` | 源摄像头 ID（数据搬离方，合并后删除） |
| `--target <id>` | 目标摄像头 ID（数据并入方，保留） |
| `--execute` | 真正执行（默认 dry-run 仅预览） |
| `--force` | 存在孤儿记录时仍然继续 |
| `--config <path>` | 配置文件路径（默认 `mibee-nvr.yaml`） |

## repair — 数据修复

针对运行期数据问题的一组修复工具，**直接操作数据库**。建议优先在服务停止时运行（运行中也安全 —— WAL 模式支持并发读，但大修停服更稳）。

```bash
mibee-nvr repair <子命令> [--dry-run | --execute] [--config mibee-nvr.yaml]
```

所有子命令**默认 dry-run**（只报告将改动什么），加 `--execute` 才真正落库。

| 子命令 | 用途 |
|--------|------|
| `duration` | 修复 duration=0 的录像：重新探测视频文件恢复真实时长（`--prune` 顺带删除无法修复的记录） |
| `merge-status` | 重置「已合并」标记 —— 合并产物文件丢失时回退为未合并 |
| `fragments` | 清理合并引擎放弃的碎片段（不兼容 / 失败） |
| `delete-by-format` | 按格式批量删除某摄像头的录像，保留指定格式（如只留延时摄影段） |
| `prune-intermediate-mp4` | 清理已并入周期合并产物（8h/24h/7d/30d）的滚动合并中间 .mp4 |
| `reclaim-orphan-merges` | 回收 Web UI 删除录像后遗留的孤儿合并 .mp4（只动无引用产物，不碰源段） |
| `normalize-endpoints` | 规范化 ONVIF endpoint（省略默认端口 / 小写 / 去尾斜杠），修复去重查询不匹配 |

示例：

```bash
# 预览将修复多少条 duration=0 的录像
mibee-nvr repair duration

# 执行修复，并删除探测失败的坏文件
mibee-nvr repair duration --execute --prune
```

## cleanup — 录像清理

绕过保留策略的手动清理工具，**同时删除文件、数据库行和孤儿 AI 事件**：

```bash
# 预览删除某日期之前的录像
mibee-nvr cleanup --before 2026-08-01 --dry-run

# 执行
mibee-nvr cleanup --before 2026-08-01

# 清理孤儿文件（磁盘上有视频文件但数据库无记录）
mibee-nvr cleanup --orphans --dry-run
mibee-nvr cleanup --orphans
```

| 参数 | 说明 |
|------|------|
| `--before YYYY-MM-DD` | 删除此日期之前的录像（文件 + DB 行 + AI 事件） |
| `--orphans` | 扫描磁盘删除数据库无记录的视频文件（.mp4/.mkv/.avi/.dav/.flv） |
| `--dry-run` | 只统计不删除（强烈建议先跑一遍） |
| `--config <path>` | 配置文件路径（默认 `mibee-nvr.yaml`，用于定位存储根目录和数据库） |

> 日常清理请优先使用[保留策略](recording-playback.md)（`cleanup.retention_days`）；本命令适合迁移后瘦身、异常善后等场景。

## 环境变量速查

| 变量 | 说明 |
|------|------|
| `NVR_PASSWORD` | 首次启动设置管理员密码（无密码时 API 返回 503） |
| `NVR_LISTEN_PORT` | 覆盖监听端口 |
| `NVR_DATA_DIR` | Docker 数据目录（`health` 子命令自动探测用） |
| `NVR_UID` / `NVR_GID` | 容器内运行用户（对齐宿主目录权限） |

## 下一步

- [配置参考](config.md) — YAML 顶层键速查
- [初始化向导](wizard.md) — Web 端首次配置
- [升级指南](upgrade-guide.md) — 版本升级与数据迁移
