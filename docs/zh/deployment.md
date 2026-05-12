# MiBee NVR 部署指南

本指南涵盖 MiBee NVR 的安装、配置和生产环境维护。

## 安装方式

### 一键安装脚本（推荐）

安装脚本会自动下载最新版本的二进制文件、创建 `nvr` 系统用户、初始化配置并安装 systemd 服务。

```bash
# 安装最新版本
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | sudo bash
```

安装指定版本：

```bash
sudo ./install.sh --version v0.2.0
```

卸载（保留 `/var/lib/mibee-nvr` 中的录像数据）：

```bash
sudo ./install.sh --uninstall
```

如果配置文件不存在，安装程序会提示输入管理员密码。安装完成后，Web 界面可通过 `http://<主机IP>:9090` 访问。

### Docker

#### 前置条件

- Docker Engine 20.10+ 和 Docker Compose v2（或 Podman 等兼容运行时）
- 检查版本：
  ```bash
  docker --version
  docker compose version
  ```

#### 快速启动

```bash
# 1. 创建数据目录并复制配置文件
mkdir -p data
cp config.example.yaml data/mibee-nvr.yaml

# 2. 编辑配置文件
#    重要：将 storage.root_dir 更改为 "/data"（容器内路径）
#    设置管理员密码（见下方配置说明）
nano data/mibee-nvr.yaml

# 3. 启动服务
docker compose up -d

# 4. 打开 http://localhost:9090
```

#### 配置说明

Docker 部署与非 Docker 部署有一些关键差异：

- **数据目录**：`storage.root_dir` 必须设置为 `/data`（容器内部路径，与非 Docker 默认的 `/var/lib/mibee-nvr` 不同）。配置示例文件中的默认值需要手动修改。
- **端口映射**：
  - `9090`：Web 界面 / REST API
  - `2121`：FTP 服务
  - `2122-2140`：FTP 被动模式端口范围
  - 如需修改端口，在 `docker-compose.yml` 中修改冒号左侧的主机端口
- **数据持久化**：`./data:/data` 卷挂载会将主机数据目录映射到容器内。该目录存储：
  - 录像文件（MP4 片段）
  - SQLite 数据库（`mibee-nvr.db`）
  - 配置文件（`mibee-nvr.yaml`）
- **环境变量**：`NVR_DATA_DIR=/data` 告知容器数据目录的位置
- **时区**：通过 `TZ=Asia/Shanghai` 环境变量设置时区，确保录像时间戳正确
- **密码设置**：Docker 容器内无法使用 `mibee-nvr init` 子命令，请使用以下方式之一：
  1. 在配置文件中设置 `auth.password`（明文，首次启动时自动转换为哈希）
  2. 使用 `mibee-nvr hash-password <密码>` 生成哈希，粘贴到 `auth.password_hash`

#### docker-compose.yml 详解

完整的配置文件及各字段说明：

```yaml
services:
  mibee-nvr:
    # 容器镜像：官方预构建镜像
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest

    # 容器名称（便于管理和查看日志）
    container_name: mibee-nvr

    # 自动重启策略：除非手动停止，否则总是重启
    restart: unless-stopped

    # 端口映射：主机端口:容器端口
    ports:
      - "9090:9090"               # Web 界面和 REST API
      - "2121:2121"               # FTP 服务
      - "2122-2140:2122-2140"     # FTP 被动模式端口范围

    # 数据卷挂载：将主机的 ./data 目录挂载到容器的 /data
    # 用于持久化存储配置文件、录像数据和数据库
    volumes:
      - ./data:/data

    # 环境变量
    environment:
      - NVR_DATA_DIR=/data         # 指定数据目录路径
      - TZ=Asia/Shanghai            # 设置时区

    # 健康检查：每 30 秒检查一次服务状态
    healthcheck:
      test: ["CMD", "mibee-nvr", "health"]  # 健康检查命令
      interval: 30s                           # 检查间隔
      timeout: 5s                             # 超时时间
      start_period: 10s                       # 启动后延迟开始检查
      retries: 3                              # 重试次数
```

#### 使用预构建镜像 vs 本地构建

**选项 A：使用预构建镜像（推荐）**

- 镜像地址：`ghcr.io/mi-bee-studio/mibee-nvr:latest`
- 架构标签：`latest`（amd64）、`latest-arm64`（arm64）
- 国内镜像：`registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr`

直接使用 `docker-compose.yml` 中的默认镜像地址即可，无需额外操作。

**选项 B：本地构建**

如果需要自定义构建或使用最新源码：

```bash
# 多阶段构建（在容器内编译前端和后端，需要网络拉取基础镜像）
docker build -t mibee-nvr .

# 交叉编译 ARM64（在主机上交叉编译，不需要 QEMU）
make docker-build-arm64

# 构建所有架构
make docker-build-all
```

本地构建后，将 `docker-compose.yml` 中的 `image:` 替换为本地标签。

#### 常用 Docker 操作

```bash
# 查看实时日志
docker compose logs -f mibee-nvr

# 查看最近 100 行日志
docker compose logs --tail 100 mibee-nvr

# 重启服务
docker compose restart mibee-nvr

# 停止服务（保留数据）
docker compose down

# 停止并删除数据卷（警告：删除所有录像数据！）
docker compose down -v

# 更新到最新镜像
docker compose pull
docker compose up -d

# 查看容器状态
docker compose ps

# 查看资源使用情况
docker stats mibee-nvr

# 查看健康检查状态
docker inspect --format='{{.State.Health.Status}}' mibee-nvr
```

> **注意**：由于使用 distroless/scratch 基础镜像，无法通过 `docker exec` 进入容器调试。请使用 `docker compose logs` 查看日志。

#### 数据备份与恢复

**备份：**

```bash
# 1. 停止容器
docker compose stop

# 2. 备份数据目录
tar czf nvr-backup-$(date +%Y%m%d).tar.gz data/

# 3. 重新启动服务
docker compose start
```

**恢复：**

```bash
# 1. 停止并删除容器
docker compose down

# 2. 解压备份文件
tar xzf nvr-backup-20240101.tar.gz

# 3. 重新启动服务
docker compose up -d
```

#### 在树莓派上使用 Docker

树莓派需要使用 ARM64 架构镜像：

```yaml
# docker-compose.yml — 树莓派配置
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest-arm64
    # 或使用国内镜像：
    # image: registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:latest-arm64
    deploy:
      resources:
        limits:
          memory: 512m      # 限制内存，防止 OOM
```

注意事项：

- 片段时长必须保持在 30 秒（`segment_duration: "30s"`）
- 建议使用外接 USB 硬盘（ext4 格式）存储录像数据
- 同时录制不超过 2-3 路摄像头，具体取决于分辨率和码率

#### Docker 常见问题

**权限错误**

容器以非 root 用户运行（UID 65534）。如果遇到挂载目录权限问题：

```bash
chown -R 65534:65534 ./data
```

**端口冲突**

修改 `docker-compose.yml` 中的端口映射左侧值：

```yaml
ports:
  - "8090:9090"   # 将主机端口改为 8090
```

**容器频繁重启**

通常是配置文件错误导致。检查日志：

```bash
docker compose logs mibee-nvr
```

**FTP 无法连接**

确保被动端口范围（2122-2140）已映射且未被防火墙阻止。

**时区不正确**

在 `docker-compose.yml` 中添加 `TZ` 环境变量：

```yaml
environment:
  - TZ=Asia/Shanghai
```

**Docker Compose v1 vs v2**

- 使用 `docker compose`（带空格，v2 版本）
- 不使用 `docker-compose`（带连字符，v1 版本，已过时）

### 手动安装

如果你需要完全控制安装过程，或者安装脚本不适用于你的场景：

```bash
# 1. 从 GitHub Releases 下载二进制文件
#    https://github.com/Mi-Bee-Studio/MiBeeNvr/releases
sudo cp mibee-nvr /usr/local/bin/mibee-nvr
sudo chmod +x /usr/local/bin/mibee-nvr

# 2. 创建系统用户和数据目录
sudo useradd -r -s /bin/false -d /var/lib/mibee-nvr nvr
sudo mkdir -p /var/lib/mibee-nvr
sudo chown -R nvr:nvr /var/lib/mibee-nvr

# 3. 初始化配置（提示输入管理员密码）
sudo -u nvr /usr/local/bin/mibee-nvr init \
    --password <your-password> \
    --data-dir /var/lib/mibee-nvr \
    --config /var/lib/mibee-nvr/mibee-nvr.yaml \
    --listen ":9090"

# 4. 安装 systemd 服务
sudo cp deploy/mibee-nvr.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now mibee-nvr
```

### 从源码编译

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr

# 编译当前架构版本
make build

# 交叉编译 ARM64 版本（如树莓派）
make cross

# 运行测试
make test

# 代码检查
make lint
```

直接将交叉编译的二进制文件部署到树莓派：

```bash
make deploy RPi_HOST=user@your-rpi-host
make deploy-check RPi_HOST=user@your-rpi-host
make rollback RPi_HOST=user@your-rpi-host
```

## Systemd 服务

服务文件维护在 [`deploy/mibee-nvr.service`](../../deploy/mibee-nvr.service) 中。关键配置：

- **二进制文件**：`/usr/local/bin/mibee-nvr`
- **配置文件**：`/var/lib/mibee-nvr/mibee-nvr.yaml`
- **工作目录**：`/var/lib/mibee-nvr`
- **运行用户**：`nvr`
- **安全加固**：`NoNewPrivileges`、`PrivateTmp`、`ProtectSystem=strict`、`ProtectHome`
- **内存限制**：`MemoryMax=512M`（默认注释掉；树莓派 3B 建议取消注释）

常用命令：

```bash
sudo systemctl start mibee-nvr
sudo systemctl stop mibee-nvr
sudo systemctl restart mibee-nvr
sudo systemctl status mibee-nvr
sudo journalctl -u mibee-nvr -f   # 跟踪日志
```

## 反向代理

### Caddy

Caddy 提供自动 HTTPS，配置最简洁：

```caddyfile
nvr.example.com {
    reverse_proxy localhost:9090
}
```

指定邮箱用于 TLS 证书：

```caddyfile
{
    email admin@example.com
}

nvr.example.com {
    reverse_proxy localhost:9090
}
```

### Nginx

```nginx
server {
    listen 80;
    server_name nvr.example.com;

    location / {
        proxy_pass http://localhost:9090;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /dav/ {
        proxy_pass http://localhost:9090;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_request_buffering off;
        proxy_buffering off;
    }
}
```

## 树莓派 3B 注意事项

树莓派 3B 仅有 905MB 内存。为保证稳定运行：

- **片段时长**：使用 30 秒（`segment_duration: "30s"`）。更长的片段会在内存中缓存更多帧（如 120 秒片段占用 60-80MB）。
- **内存限制**：在 `deploy/mibee-nvr.service` 中取消注释 `MemoryMax=512M`，防止 OOM。
- **存储**：使用外接 USB 硬盘（ext4）存储录像。SD 卡在持续写入场景下会快速磨损。
- **摄像头数量**：建议同时录制不超过 2-3 路 H.264/H.265 流，具体取决于分辨率和码率。

## 更新

### 使用安装脚本（推荐）

```bash
sudo ./install.sh --version v0.2.0
```

脚本会自动停止服务、替换二进制文件并重启。配置和录像数据不受影响。

### 手动更新

```bash
sudo systemctl stop mibee-nvr
sudo cp mibee-nvr /usr/local/bin/mibee-nvr
sudo chmod +x /usr/local/bin/mibee-nvr
sudo systemctl start mibee-nvr
```

更新前务必备份配置：

```bash
sudo cp /var/lib/mibee-nvr/mibee-nvr.yaml /var/lib/mibee-nvr/mibee-nvr.yaml.backup
```

## 监控

### 日志

```bash
sudo journalctl -u mibee-nvr -n 100    # 最近 100 行
sudo journalctl -u mibee-nvr -f        # 实时跟踪
sudo journalctl -u mibee-nvr --since "1 hour ago"
```

### 健康检查

```bash
sudo systemctl is-active mibee-nvr
curl -f http://localhost:9090/api/health
```

### 磁盘使用

```bash
df -h /var/lib/mibee-nvr
du -sh /var/lib/mibee-nvr/recordings
```

### Prometheus 指标

指标通过 `/metrics` 端点公开（无需认证）：

```bash
curl http://localhost:9090/metrics
```

## 故障排除

### 服务无法启动

```bash
sudo journalctl -u mibee-nvr -n 50
# 验证配置文件语法
sudo -u nvr /usr/local/bin/mibee-nvr -config /var/lib/mibee-nvr/mibee-nvr.yaml
```

### 摄像头连接失败

```bash
# 测试 RTSP 连接
ffmpeg -rtsp_transport tcp -i "rtsp://admin:pass@192.168.1.100:554/stream" -t 5 -f null -

# 检查网络
ping 192.168.1.100
```

### 端口冲突

```bash
sudo lsof -i :9090
sudo lsof -i :2121
```

### 权限错误

```bash
ls -la /var/lib/mibee-nvr/
sudo -u nvr ls /var/lib/mibee-nvr/
```

### 内存占用过高

将 `segment_duration` 减小到 30 秒。在树莓派 3B 上，建议在服务文件中取消注释 `MemoryMax=512M`。
