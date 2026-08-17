# 部署 FAQ — NAS 打包、端口冲突、自动升级

> 适用于 v0.11.0+。覆盖各大 NAS 平台的升级机制与端口冲突防护。
> 英文版待补:`docs/en/deployment-faq.md`。

## 目录

- [Q1：发布 v0.11.0 正式版后，各 NAS 平台如何自动升级？](#q1发布-v0110-正式版后各-nas-平台如何自动升级)
- [Q2：如何防止端口冲突？初始化能否设端口？装完能否改？](#q2如何防止端口冲突初始化能否设端口装完能否改)

---

## Q1：发布 v0.11.0 正式版后，各 NAS 平台如何自动升级？

### 1.1 应用内的"升级感知"层（已具备，但不执行升级）

应用内只做**提醒**，从不**动手**升级。

- 后端定时轮询 GitHub Releases，缓存最新 tag。
- Web 设置页 →「检查更新」显示：当前版本、最新版本、`update_available`、changelog、`deployment` 字段（`"docker"` / `"binary"`，用于决定给用户展示哪套升级指引文字）。
- 源码注释明确：`update.ts` —— "sensing layer only (never executes an upgrade)"。

> **结论：应用内只是传感器。真正的升级由部署载体决定。**

### 1.2 各 NAS 平台的升级路径

按"部署载体"分类（不是按 NAS 品牌，因为几乎所有 NAS 都走 Docker）：

| 载体 | 自动升级方式 | 现状 |
|---|---|---|
| **Docker（群晖/威联通/unRAID/极空间/iStoreOS 通用）** | **Watchtower**。compose 里已有 `--profile auto-update`，扫描带 label `com.centurylinklabs.watchtower.enable=true` 的容器，拉新镜像 → 滚动重建。默认每小时，可在 compose 里调整 | ✅ 已就绪，详见 `deployment-autoupdate.md` |
| **群晖 DSM 7.2+** | 除 Watchtower 外，Container Manager 自带"计划任务 → 自动更新镜像" | ✅ |
| **威联通 QNAP** | Container Station 无内置自动更新 → 依赖 Watchtower | ✅ |
| **unRAID** | Community Applications 面板手动 "Update"；或挂 Watchtower | ✅ |
| **fnOS（`.fpk` 包）** | 双通道：① 底层 Docker 镜像走 Watchtower（自动）；② `.fpk` 版本号 bump 重提交商店，商店再推"应用更新"（手动审核） | ⚠️ 镜像层自动 + 商店层手动 |
| **裸机 systemd（`install.sh` 安装）** | **无自动升级**。只能重跑 `install.sh`，或 `install.sh --version vX.Y.Z` 指定 tag | ❌ 缺口 |

### 1.3 用户操作清单（升级时该做什么）

**Docker 用户（推荐路径）：**
```bash
# 手动升级
docker compose pull && docker compose up -d

# 自动升级（一次性开启）
docker compose --profile auto-update up -d   # 启动 Watchtower
```

**裸机 systemd 用户：**
```bash
# 重新拉取并安装最新版（覆盖二进制 + 重启服务）
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | sudo bash

# 或指定版本
sudo install.sh --version v0.11.0
```

### 1.4 要不要做"应用内一键自升级"？

**当前建议：不做。** 理由：

- systemd 进程自替换 + 重启需要 root / `CAP_SYS_ADMIN`，容器内做不到，Docker 用户拿不到这个能力。
- 端口、数据目录、配置路径各平台不同，自升级脚本要兼容全场景，维护成本高。
- v0.11.0 阶段 Docker 是主推路径，Watchtower 已够；裸机用户重跑 `install.sh` 是一行命令，门槛极低。

**结论：Docker 走 Watchtower 全自动；裸机走"重跑 install.sh"半自动；应用内只提醒。** 若未来要补，等 1.x 再加 `--self-update` 子命令，且仅对裸机生效。

### 1.5 升级时数据安全

- 配置（`mibee-nvr.yaml`）和数据库（SQLite WAL）都在数据目录（Docker：`/data`；裸机：`/var/lib/mibee-nvr`），**升级只换二进制/镜像，不碰数据**。
- Docker 升级前确保 volume 挂载正确（`/mnt/external/nvr:/data`），否则容器重建会丢数据。
- 建议大版本升级前备份 `mibee-nvr.yaml` + `*.db`。

---

## Q2：如何防止端口冲突？初始化能否设端口？装完能否改？

### 2.1 必须先理解的架构事实（核心）

本项目 **ONVIF WS-Discovery 用 UDP 组播 `239.255.255.250:3702`**，这迫使所有 NAS 的 Docker compose 都用 `network_mode: host`。

**一旦用 host 网络，Docker 的端口映射（`ports: "9091:9090"`）完全失效** —— 容器直接占用宿主机端口，无法在 Docker 层重映射。

> **结论：NAS 场景下，端口冲突的唯一解是改应用配置 `server.listen`，不是改 Docker 端口映射。** 这点和普通 Docker 应用不同，务必告知用户。

### 2.2 现状能力表（已逐项核实代码）

| 能力 | 现状 | 说明 |
|---|---|---|
| `init` 时设端口 | ✅ 支持 | `mibee-nvr init --listen :PORT`，默认 `:9090`，写入 `mibee-nvr.yaml` 的 `server.listen` 字段 |
| 装完后改端口 | ✅ 可改 | 编辑 `mibee-nvr.yaml` 的 `server.listen` → **重启**进程/容器 |
| Web UI 改端口 | ✅ 支持 | 设置页「通用/General」可改监听端口（写入 `server.listen`，后端 PUT 自 #270 起接受），保存后重启生效 |
| 热重载（不重启生效） | ❌ 不支持 | HTTP listener 无法运行时重新 bind，必须重启 |
| Docker 端口映射规避冲突 | ❌ host 网络下失效 | 所有 NAS compose 用 `network_mode: host` |

### 2.3 各"打包安装包"的具体情况

| 安装包形态 | 安装时设端口 | 装完改端口 |
|---|---|---|
| **`install.sh`（裸机）** | ✅ 支持（#268）：`install.sh --port 9091`，或 TTY 交互提示输入端口（pipe 模式走默认 `9090` 不阻塞） | ✅ 改 `/var/lib/mibee-nvr/mibee-nvr.yaml` 的 `server.listen` → `sudo systemctl restart mibee-nvr` |
| **Docker compose（NAS 通用）** | ✅ 改挂载的 config 或 env | ✅ 改 config 卷里的 `mibee-nvr.yaml` → `docker compose restart mibee-nvr` |
| **fnOS `.fpk`** | 底层是 docker-compose，同上 | 同上 |
| **unRAID CA 模板** | XML 模板 `WebUI` 字段指向 `9090`，但实际端口由容器内 config 决定 | 改 `/mnt/user/appdata/mibee-nvr/data/mibee-nvr.yaml` → 在 Docker 面板重启容器 |

### 2.4 改端口的标准操作（装完后）

**裸机：**
```bash
sudo sed -i 's/^listen: :9090/listen: :9091/' /var/lib/mibee-nvr/mibee-nvr.yaml
# 或直接编辑文件，把 server.listen 改成 :9091
sudo systemctl restart mibee-nvr
```

**Docker（NAS，推荐用 env 变量）：**

host 网络下 Docker 端口映射无效，用 `NVR_LISTEN_PORT` 环境变量改端口（二进制启动时覆盖 `server.listen`，见 #269）：
```yaml
# docker-compose.host.yml 的 environment 加一行
environment:
  - NVR_LISTEN_PORT=9091
```
```bash
docker compose up -d   # 重启容器即生效（首次启动或已有 config 都适用）
```

或直接编辑挂载卷里的 `mibee-nvr.yaml`（`server.listen`）再 `docker compose restart mibee-nvr`。

`mibee-nvr.yaml` 中的相关字段：
```yaml
server:
  listen: ":9091"        # ← 改这里
  tls_listen: ""         # 可选：HTTPS 第二监听口，如 ":9443"
```

> 改完**必须重启**，无热重载。

### 2.5 待改进项（堵住端口冲突的体验痛点）

按 ROI 排序：

1. ✅ **`install.sh` 增加端口交互入口**（#268，已实现）：`install.sh --port 9091` 或 TTY 交互提示 `Listen port [9090]:`，pipe 模式走默认不阻塞。

2. ✅ **compose env 驱动端口**（#269，已实现）：二进制启动时读 `NVR_LISTEN_PORT` 覆盖 `server.listen`（env 优先于 config 文件，12-factor）。NAS 用户改 compose 一个变量即可，不用动 YAML。

3. ✅ **Web UI 增加端口设置 + “需重启生效”提示**（#270，已实现）：设置页加 `server.listen` 字段，保存时提示需重启生效。

---

## 附录：相关文档

| 主题 | 文档 |
|---|---|
| Docker 手动 + Watchtower 自动升级 | `deployment-autoupdate.md` |
| 各 NAS 平台部署指引 | `deployment-{synology,qnap,unraid,fnos,istoreos,zspace}.md` |
| 完整配置参考（含 `server.listen`） | `configuration.md` |
| 反向代理（Caddy/Nginx 改端口入口） | `deployment.md` |
