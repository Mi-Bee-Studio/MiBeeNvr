# 在飞牛 fnOS 上部署

> 通过 `.fpk` 包把 MiBee NVR 安装到 fnOS。该包只是封装现成的多架构 Docker 镜像，不涉及任何后端代码改动。fnOS 在目标平台里有最完整的官方开发工具链（`fnpack`）。

## 为什么用 host 网络

ONVIF WS-Discovery 使用 UDP 多播（`239.255.255.250:3702`），Docker 默认 bridge 会阻断它。因此包内容器用 `network_mode: host`，摄像头自动发现正常工作，并在 manifest 里设 `checkport=false`（host 网络的多端口服务没有单一有效的 `service_port`）。若宿主的 `9090`（Web）或 `2121`（FTP）已被占用：Web 端口首选在 **Web UI → 设置 → 通用 → "Web 界面端口"** 修改（装完即可改），或部署前设 `NVR_LISTEN_PORT` 环境变量；FTP 端口改 `mibee-nvr.yaml` 里的 `ftp.port`。

## 包内容

`.fpk` 源在 [`deploy/fnos/`](../../deploy/fnos)：

```
deploy/fnos/
├── manifest                     # 应用元数据（INI），版本在构建时注入
├── ICON.PNG  ICON_256.PNG       # 64/256 图标（用真实 logo 替换占位图）
├── config/{privilege,resource}  # 以 package 用户运行；声明 docker-project
├── app/docker/docker-compose.yaml   # host 网络，复用 ghcr 镜像
├── app/ui/config                # 桌面入口 → 打开 http://host:9090
├── cmd/main                     # 通过 `docker inspect` 检查运行状态
├── wizard/install               # 安装时提示
└── build.sh                     # 注入 X.Y.Z 版本号，执行 fnpack build
```

## 构建 `.fpk`

先安装 [`fnpack`](https://github.com/ckcoding/fnnas-docs/blob/main/docs/cli/fnpack.md)，然后：

```bash
./deploy/fnos/build.sh 0.10.0    # 版本号必须是 X.Y.Z，且与镜像 tag 一致
```

版本号必须是 `X.Y.Z`（fnOS 拒绝 pre-release 后缀），**并且**必须等于镜像 tag——不一致会在 NAS 拉镜像时报误导性的 `manifest unknown`。`build.sh` 会把同一个版本号写入 `manifest` 和 compose 的 `${VERSION}`。

## 安装 / 上架

- **本地测试：** fnOS → 应用中心 → 手动安装 → 选择生成的 `.fpk`，或 `appcenter-cli install-fpk mibee-nvr.fpk`。
- **上架应用商店：** 通过飞牛官网 → 关注飞牛 → 微信群，加任意粉丝群后联系社区主理人，加入「应用中心开发者先锋交流群」，按工作人员指引提交 `.fpk` + 截图。见 [fnOS 上架文档](https://github.com/ckcoding/fnnas-docs/blob/main/docs/quick-started/publish-application.md)。

## 升级流程

fnOS 通过应用生命周期驱动升级：提升 `manifest.version` + 镜像 tag，重新构建 `.fpk`，平台会执行 `upgrade_init`/`upgrade_callback`（数据/配置迁移钩子），再停止并重启应用。应用数据目录下的录像/数据库/配置在升级中保持不变。见 [fnOS 应用框架](https://github.com/ckcoding/fnnas-docs/blob/main/docs/core-concepts/framework.md)。

## 图标

`ICON.PNG` / `ICON_256.PNG` / `app/ui/images/icon_{64,256}.png` 是纯色占位图。上架前请用真实 logo 替换（仅支持 PNG，不支持 SVG）。
