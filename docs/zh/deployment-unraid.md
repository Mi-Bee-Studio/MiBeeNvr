# 在 unRAID 上部署

> 从 unRAID 的 **Community Applications** 商店安装 MiBee NVR，或用自定义 Docker 模板部署。unRAID 是本 NVR 最友好的 NAS 平台：`host` 网络是一等公民，ONVIF 摄像头自动发现开箱即用。

## 为什么必须用 host 网络

ONVIF WS-Discovery 向多播组 `239.255.255.250:3702` 发 UDP 探测。Docker 默认的 **bridge** 网络不转发多播，会导致摄像头自动发现失败。因此模板固定 `Network=host`：容器共享宿主网络栈，多播正常收发。手动添加摄像头（RTSP/ONVIF URL）在 bridge 下仍可用，但自动发现会失效。

## 方式 A — 从 Community Applications 安装（推荐）

模板合入 CA 商店后：

1. **Apps** 标签页 → 搜索 `mibee-nvr` → 点 **Install**。
2. 把 **Data** 路径指向存储池里的共享（建议放在校验盘或外接硬盘上），如 `/mnt/user/appdata/mibee-nvr/data`。
3. **Network** 保持 `host`。
4. **NVR_UID / NVR_GID** 设为该共享的属主（默认是 unRAID 的 `nobody:users` = `99:100`）。
5. 点 **Apply**，容器会拉取多架构镜像并启动。
6. 打开 `http://<服务器IP>:9090` 完成初始化向导。

## 方式 B — 自定义 Docker 模板

模板尚未进入 CA 时，可手动添加：

1. **Docker** 标签页 → **Add Container** → **Template** 下拉 → **My Templates** → 命名 `mibee-nvr`。
2. **Repository** 填 `ghcr.io/mi-bee-studio/mibeenvr:latest`。
3. **Network Type** 选 `host`。
4. 添加 **Path**：Host Path = 你的共享，Container Path = `/data`，Access = `Read/Write`。
5. 添加 **Variables**：`NVR_DATA_DIR=/data`，可选 `NVR_UID`/`NVR_GID`。
6. **WebUI** 填 `http://[IP]:[PORT:9090]`。
7. **Apply**。

原始模板见 [`deploy/unraid/mibee-nvr.xml`](../../deploy/unraid/mibee-nvr.xml)。

## 端口冲突

host 网络下容器直接监听宿主端口。若 `9090`（Web/API）或 `2121`（FTP）已被占用，**不要**重映射端口——改 `mibee-nvr.yaml` 里应用自己的端口：

```yaml
server:
  listen: ":8080"     # Web/API
ftp:
  port: 2121          # FTP 控制端口
  passive_port_range: "2122-2140"
```

## 升级

- **手动 / 固定 tag（录像关键场景推荐）：** 镜像 tag 锁定到某个发布版本（如 `mibeenvr:0.10.0`），`docker pull` + 重建容器即可升级。固定 tag 同时也是干净的回滚目标。
- **自动升级：** 可选的 Watchtower profile（见[自动升级指南](deployment-autoupdate.md)）能按计划拉取 `:latest`。自动升级方便但牺牲回滚确定性——务必保留一个已知可靠的 tag。

录像、SQLite 数据库、配置和 AI 模型全部存放在 `/data` 下，重建容器不会触碰这些数据。

## 架构

镜像支持 `linux/amd64`、`linux/arm64`、`linux/arm/v7`；unRAID 主机是 x86_64，会自动拉取 `amd64`。armv7 在此冗余但无害。
