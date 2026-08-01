# 在群晖 Synology DSM 上部署

> 通过 **Container Manager → 项目**（docker-compose）在 Synology DSM 7.2+ 上以 Docker 运行 MiBee NVR。compose 路径支持 `network_mode: host`，正是 ONVIF 摄像头自动发现所需要的。无需 `.spk` 套件即可正常安装。

## 为什么用 host 网络

ONVIF WS-Discovery 使用 UDP 多播（`239.255.255.250:3702`），Docker 默认的 **bridge** 会阻断它。因此 compose 固定 `network_mode: host`；Container Manager 的「项目」会正确识别它。**不要**在 host 网络服务里同时声明 `ports:`——二者冲突，DSM 会拒绝该 compose。

## 通过 Container Manager → 项目安装

1. 在 **File Station** 里为项目建一个文件夹，如 `/volume1/docker/mibee-nvr/`。
2. 打开 **Container Manager → 容器 → 项目 → 新增**。
3. **项目名称** = `mibee-nvr`，**路径** = 第 1 步的文件夹。
4. **来源** 选 **「创建 docker-compose.yml」**，粘贴 host 网络 compose：
   ```yaml
   services:
     mibee-nvr:
       image: ghcr.io/mi-bee-studio/mibeenvr:latest
       container_name: mibee-nvr
       restart: unless-stopped
       network_mode: host          # ONVIF 自动发现所需
       volumes:
         - /volume1/docker/mibee-nvr/data:/data
       environment:
         - NVR_DATA_DIR=/data
       healthcheck:
         test: ["CMD", "mibee-nvr", "health"]
         interval: 30s
         timeout: 5s
         retries: 3
   ```
   （这与 [`deploy/compose/docker-compose.host.yml`](../../deploy/compose/docker-compose.host.yml) 一致，仅填入宿主路径。）
5. **下一步 → 完成**。Container Manager 拉取多架构镜像并启动容器。
6. 打开 `http://<NAS-IP>:9090` 完成初始化向导。

## 端口冲突

host 网络下容器直接监听 NAS 端口。MiBee NVR 用 `9090`（Web/API）和 `2121`（FTP）——这俩**不与** DSM 自身端口冲突。DSM 保留 `5000`（HTTP）/ `5001`（HTTPS）给其管理界面，`20/21` 给可选的内置 FTP。如果你也开了 DSM 的 FTP，请注意它与本 NVR 的 FTP（2121）是两套独立服务，别混淆。

若 `9090` 或 `2121` 被其它容器占用，改 `mibee-nvr.yaml` 里应用自己的端口（不要重映射端口）：
```yaml
server:
  listen: ":8080"
ftp:
  port: 2121
  passive_port_range: "2122-2140"
```

## 升级与回滚

见[自动升级指南](deployment-autoupdate.md)。简言之：在项目目录 `docker compose pull && up -d`，或固定 tag（`mibeenvr:0.10.0`）以便可复现回滚。`/volume1/docker/mibee-nvr/data` 下的数据不受重建影响。

## 原生 `.spk` 套件——何时值得（可选）

在群晖**套件中心**上架 `.spk` 能获得商店曝光，但成本不低：DSM 7 要求发布者签名证书 + 功能审核，且要为每个架构分别打包。两个务实选择：

- **仅用 Compose（本指南）：** 零打包成本，今天就能用，无需签名。推荐大多数用户。
- **把 Docker 镜像封装成 `.spk`：** 群晖开发者指南有 [Compile Docker Package](https://help.synology.com/developer-guide/examples/compile_docker_package.html) 流程，把容器作为套件中心条目发布。这是要进商店时成本最低的路径——复用同一镜像，无需逐架构重编译。

对独立 NVR 项目，建议先用 Compose，仅当套件中心曝光确实值得签名/审核成本时再做 `.spk`。
