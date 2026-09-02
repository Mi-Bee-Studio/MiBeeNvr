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

## 裸机安装的产物校验（#646）

每次 GitHub Release 附带 `checksums.txt`(裸二进制的 SHA-256,`sha256sum` 格式)与 `checksums.txt.sig`(对 checksums.txt 的 ed25519 签名,64 字节原始签名)。签名私钥保存在仓库 Secret 中,公钥内嵌于程序内(`internal/update/verify.go`)并以 PEM 形式随仓库分发(`deploy/keys/release-signing.pub.pem`)。裸机自动升级(#647)落地前,可手动校验:

```bash
# 1. 校验完整性:下载的 mibee-nvr-<arch> 与 checksums.txt 对应
sha256sum -c checksums.txt --ignore-missing

# 2. (可选)校验来源:证明 checksums.txt 出自本项目,未被篡改
curl -fsSL -o release-signing.pub.pem \
  https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/deploy/keys/release-signing.pub.pem
openssl pkeyutl -verify -pubin -inkey release-signing.pub.pem \
  -rawin -in checksums.txt -sigfile checksums.txt.sig
```

注意:

- `checksums.txt` 覆盖三个裸二进制(amd64/arm64/armv7);fnOS `.fpk` 由飞牛商店渠道分发,不在其列;Docker 镜像由 registry 摘要保证完整性。
- 若某次 Release 没有 `checksums.txt.sig`,说明签名 Secret 未配置(该 Release 未签名),只做第 1 步完整性校验。
- 密钥轮换意味着新 Release 用新公钥签发;以仓库 main 分支上的公钥为准。

## 裸机自动升级(#647,systemd 安装)

Docker 之外,`install.sh` 安装的裸机环境是最后一个可自动化的在线场景。架构刻意绕开进程内自我替换——`mibee-nvr.service` 的沙箱(`User=nvr` + `ProtectSystem=strict`)本来就禁止应用写 `/usr/local/bin`:

```text
应用(nvr 用户,沙箱内)                root helper(mibee-nvr-update.service)
  感知层发现新版 + update.auto_apply: true
  ├─ 写请求文件 /var/lib/mibee-nvr/update-request.json
  └─ systemctl start mibee-nvr-update.service   →  mibee-nvr update --apply-request …
                                                    ① 前置检查(非 docker、semver 严格更新、磁盘 ≥ 产物×3)
                                                    ② 流式下载校验和 + ed25519 签名 + 二进制
                                                    ③ sha256 + 签名验证(失败即中止,系统零改动)
                                                    ④ 保留 .prev → 原子替换二进制
                                                    ⑤ systemctl restart mibee-nvr
                                                    ⑥ 健康门禁:/api/health 就绪探针
                                                    ⑦ 失败自动回滚 .prev 并重启
                                                    ⑧ 升级历史落盘 update-history.jsonl
```

polkit 规则(`/etc/polkit-1/rules.d/60-mibee-nvr-update.rules`,installer 自动安装)只放行 nvr 用户**启动这一个 unit**,特权面最小。

### 开启

```yaml
update:
  auto_apply: true   # 默认 false,仅提示不执行
```

恒禁用条件(开了也不会执行):`dev` 构建、非 stable channel、候选版本 ≤ 当前(永不降级)、Docker 部署。

### 手动升级(无需 polkit)

```bash
sudo mibee-nvr update              # 升级到最新 stable
sudo mibee-nvr update --version v0.12.1
mibee-nvr update --check           # 只看状态,零改动
```

### 回滚

升级前自动保留上一版为 `<二进制>.prev`;健康门禁失败会**自动**回滚并重启。手动回滚:

```bash
sudo systemctl stop mibee-nvr
sudo cp /usr/local/bin/mibee-nvr.prev /usr/local/bin/mibee-nvr
sudo systemctl start mibee-nvr
```

升级记录在 `<数据目录>/update-history.jsonl`(时间、from/to、结果、失败原因)。

### 适用边界

仅适用 `install.sh`/systemd 裸机安装。Docker 归 Watchtower;fnOS/unRAID 等商店渠道归平台的升级机制;离线环境请手动下载(校验方法见上一节)。无 polkit 的老发行版上自动触发不可用,但 `sudo mibee-nvr update` 手动路径不受影响。
