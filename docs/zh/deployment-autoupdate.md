# 自动升级（Docker）

> 如何保持 MiBee NVR 的 Docker 镜像为最新。应用内置的版本检测（**感知**层，[设置 → 关于](../zh/configuration.md)）告诉你有新版本；本指南覆盖**执行**层——真正替换镜像。可选择手动（录像关键场景推荐）或用 Watchtower 可选自动升级。

## 两层设计，刻意分离

| 层 | 作用 | 方式 |
|----|------|------|
| **感知层**（内置） | 轮询 GitHub Releases，显示红点 + 关于面板 | `GET /api/update/check`；不执行任何动作 |
| **执行层**（本指南） | 拉取新镜像并重建容器 | 手动 `docker compose`，或 Watchtower |

二者刻意分离：NAS 守护着录像和配置等持久数据，不能被静默更改（Docker 容器本身也不可变）。升级始终由用户授权——直接操作，或显式开启 Watchtower。

## 手动升级（推荐）

对任何正在录像的部署，这是最安全的路径：

```bash
cd <你的 compose 文件所在目录>
docker compose pull          # 拉取新镜像
docker compose up -d         # 用新镜像重建容器
```

`./data` 下的数据（录像、SQLite 数据库、配置、AI 模型）不受影响——重建只替换镜像。

### 回滚

把镜像 tag 固定到某个发布版本，回滚就很轻松。`mibeenvr` 镜像每次发布都推送 `{version}`、`{major}.{minor}`、`{major}`、`latest`。在 compose 里设固定 tag，如：

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:0.12.0   # 固定，可复现
```

回滚时把 tag 改回上一个版本，`docker compose up -d` 即可。若想保留回滚镜像，**不要**把 `:latest` 和自动清理一起用。

## 用 Watchtower 自动升级（可选）

[Watchtower](https://github.com/containrrr/watchtower) 监视运行中的容器，当其 registry 镜像更新时重建它。内置的 profile **默认关闭**——需显式开启。

### 开启

```bash
# 1. 一次性：登录 ghcr.io（Watchtower 拉镜像需要鉴权）。
#    使用带 read:packages 权限的 GitHub 个人访问令牌（PAT）。
docker login ghcr.io -u <你的-github-用户名> --password-stdin <<< "<你的-PAT>"

# 2. 用 auto-update profile 启动。
docker compose --profile auto-update up -d
```

此后 Watchtower 每天 04:00（宿主时间）检查一次，仅在出现新镜像时重建 **mibee-nvr** 容器。

### 这样配置为什么安全

- **只管 MiBee NVR。** `WATCHTOWER_LABEL_ENABLE=true` + NVR 服务上的 `com.centurylinklabs.watchtower.enable=true` label，意味着 Watchtower 忽略宿主上的其它容器。
- **保留旧镜像。** `WATCHTOWER_CLEANUP=false` 把上一个镜像留在磁盘上以便回滚。想要回滚能力就别开 cleanup。
- **避开高峰。** `WATCHTOWER_SCHEDULE=0 0 4 * * *`（6 字段 cron）在 04:00 执行，避开活跃录像时段。
- **数据绝不动。** 只替换镜像；`./data` 持久保留。

### Watchtower 不会做的事

- **不做健康闸门回滚。** 若新镜像启动了但不健康，Watchtower 不会自动回退。NVR 自带健康检查（`mibee-nvr health`），容器运行时会据此上报状态，但自动回滚仍需手动：把镜像 tag 改回上一个已知可靠的版本，`docker compose up -d`。建议配合应用内的版本红点，以便及时察觉升级异常。
- **不做配置迁移。** 配置/数据库迁移由应用在启动时自行处理。

### 关闭

```bash
docker compose --profile auto-update stop watchtower
# 或完全不用 profile，直接 docker compose up -d（默认，不起 Watchtower）。
```

## 通知

Watchtower 支持 [Shoutrrr](https://containrrr.dev/shoutrrr/) 通知 URL。在 compose 文件里取消 `WATCHTOWER_NOTIFICATION_URL` 的注释，填入 Discord / Telegram / Slack / webhook URL，即可在每次更新（或 monitor-only 模式下的每次发现）时收到消息。

## 各平台要点

| 平台 | 最佳升级路径 |
|------|-------------|
| unRAID | [CA Application Auto Update](https://forums.unraid.net/topic/51959-plugin-ca-application-auto-update/) 插件（原生），或 Watchtower |
| iStoreOS / OpenWrt | `docker compose pull && up -d`，或 Watchtower |
| 飞牛 fnOS | `.fpk` 应用生命周期（`upgrade_init` / `upgrade_callback`）——原生，飞牛上首选 |
| 群晖 DSM | Container Manager：从新镜像重建，或 Watchtower |
| 威联通 QTS | Container Station：重建，或 Watchtower |

详见各平台部署文档。
