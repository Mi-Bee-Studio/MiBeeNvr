# 在飞牛 fnOS 上部署

> 通过 `.fpk` 包把 MiBee NVR 安装到 fnOS。每个版本发布两个包：**离线版** `.fpk`
> （双架构镜像 docker-save 进包 —— 安装无需访问镜像仓库，ghcr 慢/不可达环境首选）
> 和**在线版** `.fpk`（极小 —— 首次启动时拉取镜像，按延迟自动选择 ghcr 或阿里云
> ACR 镜像源）。包只是封装现成的多架构 Docker 镜像，不涉及后端代码改动。

## 为什么用 host 网络

ONVIF WS-Discovery 使用 UDP 多播（`239.255.255.250:3702`），Docker 默认 bridge 会阻断它。因此包内容器用 `network_mode: host`，摄像头自动发现正常工作，并在 manifest 里设 `checkport=false`（host 网络的多端口服务没有单一有效的 `service_port`）。若宿主的 `9090`（Web）或 `2121`（FTP）已被占用：Web 端口首选在 **Web UI → 设置 → 通用 → "Web 界面端口"** 修改（装完即可改），或部署前设 `NVR_LISTEN_PORT` 环境变量；FTP 端口改 `mibee-nvr.yaml` 里的 `ftp.port`。

## 离线版 vs 在线版

| | 离线 `.fpk` | 在线 `.fpk` |
|---|---|---|
| 体积 | ~150 MB（内置双架构镜像） | ~65 KB |
| 安装时联网 | 不需要（加载内置镜像） | 需要（启动时拉取） |
| 镜像来源 | 内置 tar | 自动：ghcr（海外）/ ACR（国内更快） |
| 适用 | ghcr 拉取慢/被墙 | 网络通畅、想要小包 |

两者都在[发布页](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases)：
`mibee-nvr-fnos-<ver>.fpk`（离线）与 `mibee-nvr-fnos-online-<ver>.fpk`（在线）。

## 包的工作原理（重要）

包**不使用** fnOS 的 `docker-project` 资源 —— 那会让 fnOS 在安装期间就执行
`docker compose up`，此时离线镜像还没 `docker load`，拉取必然失败。因此 `cmd/main`
自己掌管容器完整生命周期：

- **start**：（离线）`docker load` 内置的本架构 tar → `docker run --network host`；
  （在线）探测 ghcr 与 ACR 延迟 → 从更快的一方 `docker pull` → `docker run`
- **stop**：`docker stop` + `docker rm`
- **status**：`docker inspect`

`cmd/main` 是**双模式**的：发现 `${TRIM_APPDEST}/images/` 下有内置 tar 就走离线路径，
没有就走在线拉取路径 —— 同一份脚本同时服务两种包，从离线版升级到在线版（或反之）
无感切换。`app/docker/docker-compose.yaml` 实际上是 vestigial（保留作参考），
真正的入口是 `cmd/main`。`config/privilege` 以 **root** 运行（需要 Docker socket）；
`config/resource` 为 `{}`（无 docker-project）。

## 架构说明（x86）

`cmd/main` 把 `TRIM_SYS_ARCH` 映射到镜像架构：`x86`（fnOS 对 x86_64 的称呼，与
`x86_64`/`amd64`/`x64` 等价）→ amd64；`aarch64`/`arm64`/`armv8*`/`arm` → arm64
（64 位内核上 64 位镜像可正常运行）—— 修复了
[#311](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues/311) 中 x86 fnOS 主机
无法启动的问题（此前架构变量为空，产生 `mibee-nvr-.tar`）。包内不含 armv7 镜像
（fnOS 无 32 位 ARM 产品线）。

## 包源码

在 [`deploy/fnos/`](../../deploy/fnos)；在线版与离线版共用同一份源码（`cmd/main`
双模式），仅 `app/images/` 在线打包时不打入。

## 构建 `.fpk`

先安装 [`fnpack`](https://github.com/ckcoding/fnnas-docs/blob/main/docs/cli/fnpack.md)，然后：

```bash
./deploy/fnos/build.sh 0.12.0              # 离线包：需要本地已构建双架构镜像
./deploy/fnos/build.sh --online 0.12.0    # 在线包：不含镜像，无需 docker
```

版本号必须是 `X.Y.Z`（fnOS 拒绝 pre-release 后缀），**并且**必须等于镜像 tag——不一致会在 NAS 拉镜像时报误导性的 `manifest unknown`。`build.sh` 会把同一个版本号写入 `manifest` 和 compose 的 `${VERSION}`，并把产物命名为确定性的
`mibee-nvr-fnos-<ver>.fpk` / `mibee-nvr-fnos-online-<ver>.fpk`。

## 安装 / 上架

- **本地测试：** fnOS → 应用中心 → 手动安装 → 选择生成的 `.fpk`，或 `appcenter-cli install-fpk mibee-nvr.fpk`。
- **上架应用商店：** 通过飞牛官网 → 关注飞牛 → 微信群，加任意粉丝群后联系社区主理人，加入「应用中心开发者先锋交流群」，按工作人员指引提交 `.fpk` + 截图。见 [fnOS 上架文档](https://github.com/ckcoding/fnnas-docs/blob/main/docs/quick-started/publish-application.md)。

## 卸载、重装与配置文件位置

fnOS 卸载应用时**不勾选“删除应用数据”**会保留应用数据卷（录像、数据库、`mibee-nvr.yaml` 全在里面），重装后直接沿用旧配置。挂载源路径可通过 `docker inspect mibee-nvr` 查看（`/data` 的源路径），配置文件在 `<该路径>/mibee-nvr.yaml`。

**存储路径必须填容器内路径**（默认 `/data`），不要填 `/vol1/...` 这类宿主机路径——容器内不存在该挂载，进程（非 root）无法创建。历史版本（≤ v0.11.0）的初始化向导不校验这一点，填了宿主机路径的配置在下次重启时会因 `mkdir` 权限被拒进入崩溃循环（[#434](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues/434)）。新版已双重防护：向导保存前探测路径可创建性；启动时容器内不可用的 `root_dir` 会记录 ERROR 并回落到数据卷路径，而不是崩溃循环。旧版中招后的解法：卸载时勾选“删除应用数据”重装，或把数据卷里 `mibee-nvr.yaml` 的 `storage.root_dir` 改回 `/data` 后重启容器。

## 升级流程

fnOS 通过应用生命周期驱动升级：提升 `manifest.version` + 镜像 tag，重新构建 `.fpk`，平台会执行 `upgrade_init`/`upgrade_callback`（数据/配置迁移钩子），再停止并重启应用。应用数据目录下的录像/数据库/配置在升级中保持不变。见 [fnOS 应用框架](https://github.com/ckcoding/fnnas-docs/blob/main/docs/core-concepts/framework.md)。

## 图标

`ICON.PNG` / `ICON_256.PNG` / `app/ui/images/icon_{64,256}.png` 是纯色占位图。上架前请用真实 logo 替换（仅支持 PNG，不支持 SVG）。
