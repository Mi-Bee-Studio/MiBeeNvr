# 在 iStoreOS / OpenWrt 上部署

> 在 iStoreOS（或自带 Docker 的通用 OpenWrt 固件）上以 Docker 运行 MiBee NVR。iStoreOS 天然契合本应用：**默认就是 host 网络**，ONVIF 摄像头自动发现（UDP 多播）零配置即可工作。

## 硬件门槛 —— 请先读

这些是软路由/路由板 OS。一个**完整** NVR（录像到磁盘 + SQLite + 多路流解复用）只在以下设备上现实：

- **x86 软路由**（Intel N100 / J4125 / N305 级），**内存 ≥ 4 GB**；或
- **ARM 单板**（树莓派 4/5 4GB+、RK3588），配外接存储。

> **RK3588 用户注意**：NVR 的转码后端目前只支持 software / V4L2 M2M / VAAPI / NVENC，
> **不含 RKMPP（Rockchip NPU 硬编解码）**——RK3588 的 NPU 在 NVR 里用不上，转码只能走
> 软件编码（多路时 CPU 吃紧）。录像 / 直播 / 回放等核心功能均为纯 Go 实现，不依赖转码，
> 不受影响。

在 512 MB – 1 GB 设备上，只能做轻量**接入网关 / 实时预览**（关闭或严格限制录像），否则会 OOM。路由器闪存（8–32 GB eMMC）几天就会被视频写满——**务必把 `/data` 映射到外接存储**（USB 硬盘 / SATA / 挂载的 NAS 共享）。

## 为什么在这里最顺

iStoreOS 的 Docker 默认用 `network_mode: host`。路由器本身就是网络设备，NVR 进程直接位于 LAN 上——ONVIF 多播和广播直达摄像头，中间没有 bridge/NAT。无需改任何代码；应用已监听 `0.0.0.0`。

## 镜像源与国内加速

镜像同时发布在两个 registry，**内容完全一致**（同一多架构 manifest list）：

| Registry | 地址 | 适用 |
|---|---|---|
| GitHub ghcr | `ghcr.io/mi-bee-studio/mibeenvr` | 海外（默认） |
| 阿里云 ACR | `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr` | **中国大陆推荐**（匿名直拉、免登录、免 PAT） |

**SSH 一键安装（自动判断最优源）**——脚本并发探测两个 registry 的延迟，自动选最快的，完成拉取 + 启动（iStoreOS 若无 bash，先 `opkg install bash`，或用下面的 compose 路径）：

```bash
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/deploy/install-online.sh | bash
```

> 国内用户把 compose 里的 `image:` 换成阿里云地址也能加速：`registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:latest`

## 安装

### 通过 iStoreOS Web UI（推荐）

1. 进入 **系统 → Docker → Compose → 新建项目**。
2. 项目命名 `mibee-nvr`，把 [`deploy/istoreos/docker-compose.yml`](../../deploy/istoreos/docker-compose.yml) 的内容粘贴进去。
3. 把卷的左侧路径改指向你的外接存储（如 `/mnt/sata1/mibee-nvr/data`）。
4. 保存并启动项目。
5. 打开 `http://<设备IP>:9090` 完成初始化向导。

### 通过 SSH

```bash
mkdir -p /mnt/sata1/mibee-nvr && cd /mnt/sata1/mibee-nvr
# 下载 compose 文件（或 scp 你编辑好的副本）
wget -O docker-compose.yml https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/deploy/istoreos/docker-compose.yml
docker compose up -d
```

## 可选：作为第三方 Compose 应用源

iStoreOS/iStore 支持第三方 Compose 应用仓库。你可以把本仓库（或 fork）自建为应用源，用户添加一次即可一键安装：**系统 → 应用商店 → 添加第三方应用商店**，填入仓库 URL。源格式见 [iStoreOS discussion #1777](https://github.com/istoreos/istoreos/discussions/1777)。

## 端口冲突

host 网络下应用直接监听宿主端口。若 `9090` 或 `2121` 与其它服务冲突，改 `mibee-nvr.yaml` 里应用自己的端口（不要重映射端口）：

```yaml
server:
  listen: ":8080"
ftp:
  port: 2121
  passive_port_range: "2122-2140"
```

## 升级与回滚

- **手动（推荐）：** `cd /mnt/sata1/mibee-nvr && docker compose pull && docker compose up -d`。
- **固定 tag / 回滚：** 镜像 tag 锁到某个发布版本（`mibeenvr:0.10.0`），重建，并保留旧 tag 以便回滚。
- **自动升级：** 可选的 Watchtower profile——见[自动升级指南](deployment-autoupdate.md)。

`/data` 下的数据（录像、数据库、配置、模型）不受容器重建影响。
