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

使用项目提供的 `docker-compose.yml`：

```bash
# 1. 准备数据目录和配置文件
mkdir -p data
cp config.example.yaml data/mibee-nvr.yaml
# 编辑配置：设置密码、添加摄像头
nano data/mibee-nvr.yaml

# 2. 启动服务
docker compose up -d

# 3. 打开 http://localhost:9090
```

端口说明：

| 端口 | 用途 |
|------|------|
| 9090 | Web 界面 / REST API |
| 2121 | FTP |
| 2122-2140 | FTP 被动模式 |

Docker 镜像内置健康检查（`mibee-nvr health`），每 30 秒执行一次。数据通过 `./data` 卷挂载持久化。

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
