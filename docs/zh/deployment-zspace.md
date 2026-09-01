# 在极空间 ZSpace（ZOS）上部署

> 通过极空间内置的 **Docker → Compose 项目** 以 Docker 运行 MiBee NVR。ZOS 在 compose 中支持 host 网络，正是 ONVIF 摄像头自动发现所需要的。极空间无私有包格式——这里的「应用」就是 Docker 容器。

## 为什么用 host 网络

ONVIF WS-Discovery 使用 UDP 多播（`239.255.255.250:3702`），Docker 默认的 **bridge** 会阻断它。ZOS 的 Docker 支持 host / macvlan / bridge；compose 固定 `network_mode: host`，摄像头发现无需额外配置即可工作。

## 镜像源与国内加速

镜像同时发布在两个 registry，**内容完全一致**（同一多架构 manifest list）：

| Registry | 地址 | 适用 |
|---|---|---|
| GitHub ghcr | `ghcr.io/mi-bee-studio/mibeenvr` | 海外（默认） |
| 阿里云 ACR | `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr` | **中国大陆推荐**（匿名直拉、免登录、免 PAT） |

**SSH 一键安装（自动判断最优源）**——脚本并发探测两个 registry 的延迟，自动选最快的，完成拉取 + 启动：

```bash
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/deploy/install-online.sh | bash
```

> 不会 SSH？走下面的 Docker → Compose 路径即可。国内用户把 compose 里的 `image:` 换成阿里云地址也能加速：`registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:latest`

## 通过 Docker → Compose 安装

1. 在 **文件管理** 里，从存储池选一个文件夹存放持久数据（如外接硬盘或大共享上的路径）。
2. 打开 **系统 → Docker → Compose → 新建项目**（旧固件可能是 **Docker → 项目**）。
3. 项目命名 `mibee-nvr`，粘贴：
   ```yaml
   services:
     mibee-nvr:
       image: ghcr.io/mi-bee-studio/mibeenvr:latest
       container_name: mibee-nvr
       restart: unless-stopped
       network_mode: host          # ONVIF 自动发现所需
       volumes:
         - <你的存储路径>/mibee-nvr/data:/data
       environment:
         - NVR_DATA_DIR=/data
       healthcheck:
         test: ["CMD", "mibee-nvr", "health"]
         interval: 30s
         timeout: 5s
         retries: 3
   ```
   把 `<你的存储路径>` 换成第 1 步的路径（录像很占空间——用大存储池或外接盘，别用系统盘）。
4. 保存并启动项目。多架构镜像被拉取（x86 机型取 `amd64`，Z2Pro/T6 等 ARM 机型取 `arm64`）。
5. 打开 `http://<NAS-IP>:9090` 完成初始化向导。

## 导入现成模板

不想手抄 Compose？仓库里有可直接导入的编排模板：
[`deploy/zspace/docker-compose.yml`](../../deploy/zspace/docker-compose.yml)
（导入步骤见 [`README.md`](../../deploy/zspace/README.md)）。默认走阿里云镜像源
（国内免登录拉取）+ host 网络，并带 `NVR_LISTEN_PORT`（改端口）注释开关。

## 端口冲突

host 网络下容器直接监听 NAS 端口。MiBee NVR 用 `9090`（Web）和 `2121`（FTP）；若与其它服务或容器冲突，改 `mibee-nvr.yaml` 里应用自己的端口（不要重映射）：
```yaml
server:
  listen: ":8080"
ftp:
  port: 2121
  passive_port_range: "2122-2140"
```

## 与极空间自带「监控中心」的区别

极空间自带 **监控中心**（轻量 RTSP/ONVIF 预览工具）。两者可共存，定位不同：

| | 极空间 监控中心 | MiBee NVR |
|---|---|---|
| 实时预览 | 有 | 有 |
| 浏览器端 AI 检测（ONNX） | 无 | **有** |
| 录像到磁盘 + 保留/合并 | 有限 | **完整**（纯 Go 合并，无需 FFmpeg） |
| FTP / WebDAV / WebRTC 流媒体 | 无 | **有** |
| 多架构，x86 + ARM 机型通吃 | — | 是 |

监控中心用于快速看预览，MiBee NVR 用于 AI 智能分析与长期录像。

## 升级与回滚

见[自动升级指南](deployment-autoupdate.md)。在项目里 `docker compose pull && up -d`，或固定 tag（`mibeenvr:0.12.0`）以便回滚。映射路径下的数据在重建中保持不变。

## 进入官方应用商店（可选）

极空间官方商店收录精选的「Docker 应用」和「第三方应用」，但**没有公开的自助开发者门户**——进驻需联系极空间商务。注意监控中心与本 NVR 功能重叠，所以商务沟通时应以上面的差异化定位切入。目前，Compose 路径 + 社区模板（如提交到社区 compose 模板库）是触达极空间用户的即时、零门槛方式。
