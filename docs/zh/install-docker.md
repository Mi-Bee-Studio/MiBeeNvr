# Docker 部署

> 适用于 MiBeeNvr v0.11.0

使用 Docker Compose 快速部署 MiBee NVR。

## 前置要求

- Docker 20.10+
- Docker Compose v2+
- 可用的磁盘空间（取决于摄像头数量和录制保留策略）

## 快速启动

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr
docker compose --project-directory . \
  -f deploy/docker/docker-compose.yml up -d
```

首次启动无需配置文件，MiBee NVR 会自动初始化。在浏览器打开 `http://localhost:9090`。

## 外部硬盘存储

默认录像存储在宿主机的 `./data` 目录。如需使用外部硬盘：

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest
    ports:
      - "9090:9090"
      - "1935:1935"
      - "9000:9000/udp"
      - "2121:2121"
    volumes:
      - /mnt/external/nvr:/data    # ← 改为你的宿主机路径
    environment:
      - NVR_DATA_DIR=/data          # 必须与卷挂载的右侧一致
      # - NVR_UID=1000               # 与宿主机目录所有者 UID 一致
      # - NVR_GID=1000               # 与宿主机目录所有者 GID 一致
```

> **重要**：卷挂载的右侧（`:data`）和 `NVR_DATA_DIR` 必须始终一致。如果容器无法启动，请检查宿主机目录是否存在，以及配置的 UID/GID 是否有写入权限。

## 持久化配置

### 方式一：使用环境变量

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest
    ports:
      - "9090:9090"
    environment:
      - NVR_LISTEN_PORT=9090
      - NVR_DATA_DIR=/data
      - NVR_PASSWORD=your-password
    volumes:
      - ./data:/data
```

### 方式二：挂载配置文件

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest
    ports:
      - "9090:9090"
    volumes:
      - ./mibee-nvr.yaml:/app/mibee-nvr.yaml
      - ./data:/data
```

先生成默认配置文件：

```bash
./mibee-nvr-amd64 init --password 你的密码
```

然后编辑 `mibee-nvr.yaml` 添加摄像头配置。

## 端口说明

| 端口 | 协议 | 用途 |
|------|------|------|
| 9090 | TCP | Web 界面和 API |
| 1935 | TCP | RTMP 推流接入 |
| 9000 | UDP | SRT 推流接入 |
| 2121 | TCP | FTP 访问录像文件 |

## 只读模式挂载

如果只读模式已启用（默认启用），通过 WebDAV 和 FTP 只能浏览和下载录像，无法删除。

如需通过 Web UI 管理录制，WebDAV 和 FTP 必须禁用：

```yaml
environment:
  - NVR_WEBDAV_ENABLED=false
  - NVR_FTP_ENABLED=false
```

## 健康检查

```bash
# 检查容器状态
docker compose ps

# 查看日志
docker compose logs -f mibee-nvr

# 健康检查
curl http://localhost:9090/api/v1/system/status
```

## 更新版本

```bash
# 拉取最新镜像
docker compose pull

# 重新创建容器（数据不丢失）
docker compose up -d
```

## 卸载

```bash
# 停止并移除容器（保留数据）
docker compose down

# 彻底删除（包括数据）
docker compose down -v
rm -rf ./data
```

## 常见问题

### 容器启动失败

```bash
# 查看详细错误
docker compose logs mibee-nvr

# 检查端口冲突
netstat -tlnp | grep 9090
```

### 权限问题

```bash
# 查看宿主机目录权限
ls -la /mnt/external/nvr/

# 调整 UID/GID
environment:
  - NVR_UID=1000
  - NVR_GID=1000
```

### 网络问题

如果摄像头在 Docker 网络之外，使用 `host` 网络模式：

```yaml
services:
  mibee-nvr:
    network_mode: host
```

## 下一步

- [NAS 部署](install-nas.md) — 群晖 / 威联通 / unRAID / 飞牛
- [录制与回放](recording-playback.md) — 录像管理
- [SRT / RTMP 推流接入](srt-rtmp.md) — 推流摄像头配置
