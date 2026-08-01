# 在威联通 QNAP QTS / QuTShero 上部署

> 通过 **Container Station → 应用程序**（docker-compose）在 QNAP 上以 Docker 运行 MiBee NVR。Container Station 3.x 在 compose 中支持 `network_mode: host`，正是 ONVIF 摄像头自动发现所需要的。

## 为什么用 host 网络

ONVIF WS-Discovery 使用 UDP 多播（`239.255.255.250:3702`），Docker 默认的 **bridge** 会阻断它。因此 compose 固定 `network_mode: host`。Container Station 2 的 GUI 历史上**不支持** host 模式，但 CS 3.x 的 compose/应用程序路径可以接受并按 host 网络运行。请优先用下面的**应用程序（compose）**路径，而非单容器 GUI。

## 通过 Container Station → 应用程序安装

1. 从 App Center 安装/启用 **Container Station**（推荐 3.x）。
2. 在 **Container Station → 应用程序 → 创建** 里粘贴 compose 文件：
   ```yaml
   services:
     mibee-nvr:
       image: ghcr.io/mi-bee-studio/mibeenvr:latest
       container_name: mibee-nvr
       restart: unless-stopped
       network_mode: host          # ONVIF 自动发现所需
       volumes:
         - /share/Container/mibee-nvr/data:/data
       environment:
         - NVR_DATA_DIR=/data
       healthcheck:
         test: ["CMD", "mibee-nvr", "health"]
         interval: 30s
         timeout: 5s
         retries: 3
   ```
   Container Station 安装时会创建名为 `Container` 的共享文件夹，把数据映射到这里（如 `/share/Container/mibee-nvr/data`）。
3. **校验 → 部署**。多架构镜像被拉取，容器启动。
4. 打开 `http://<NAS-IP>:9090` 完成初始化向导。

若你的固件在应用程序路径下阻止 host 模式，可用 SSH 兜底：
```bash
ssh admin@<NAS-IP>
mkdir -p /share/Container/mibee-nvr && cd /share/Container/mibee-nvr
# 把上面的 compose 存为 docker-compose.yml，然后：
docker compose up -d
```

## 端口冲突

QNAP QTS 自身用 `8080`（HTTP 管理）/ `443`（HTTPS）/ `80`（Web Server，若启用）。这些与 MiBee NVR 的 `9090`（Web）或 `2121`（FTP）不冲突。若某个 QNAP 服务占用了你需要的端口，请在 QTS **控制面板 → 网络**里改该服务的端口，不要重映射 NVR。

若要改 NVR 自己的端口（如 `9090`/`2121` 被其它容器占用），编辑 `mibee-nvr.yaml`：
```yaml
server:
  listen: ":8080"
ftp:
  port: 2121
  passive_port_range: "2122-2140"
```

## 升级与回滚

见[自动升级指南](deployment-autoupdate.md)。`docker compose pull` 后在应用程序里重新部署，或固定 tag（`mibeeenvr:0.10.0`）以便可复现回滚。`/share/Container/mibee-nvr/data` 下的数据在重建中保持不变。

## 原生 `.qpkg`——何时值得（可选）

威联通 **App Center** 对自助上架相对开放（用 [QDK](https://github.com/qnap-dev/QDK)），甚至可自建第三方 App 源让用户订阅安装。社区有把 Docker 容器封装成 QPKG 的做法。但要正常使用并不需要 QPKG——上面的 Compose/应用程序路径已足够。仅当 App Center 曝光对你重要时才投入 QPKG 打包，并复用同一多架构镜像（QPKG 可封装容器而非逐架构发二进制）。
