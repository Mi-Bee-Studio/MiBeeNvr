# MiBee NVR 部署指南

## 概述

本指南介绍 MiBee NVR 的部署方法，包括从源码编译、系统服务配置、容器化部署、反向代理配置以及系统维护。

### 支持的部署方式

1. **本地编译部署**: 在开发环境中直接编译和运行
2. **系统服务部署**: 使用 systemd 管理服务
3. **容器化部署**: 使用 Docker 或 Podman 进行标准化部署
4. **远程部署**: 通过 Makefile 目标直接部署到树莓派设备

## 编译准备

### 环境要求

- **Go 1.26+**: 编译工具链
- **Git**: 版本控制工具
- **Make**: 构建工具

### 从源码编译

```bash
# 克隆仓库
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr

# 检查代码
go vet ./...

# 运行测试
go test ./... -v

# 编译本地版本
make build

# 编译 ARM64 版本（适用于树莓派等设备）
make cross
```

## 编译选项

### Makefile 目标

```bash
# 构建前端界面
make frontend

# 本地编译（当前架构）
make build

# 交叉编译 ARM64
make cross

# 运行测试
make test

# 代码检查
make lint

# 清理构建产物
make clean

# 安装到目标目录
make install

# 安装 systemd 服务
make install-service

# 卸载 systemd 服务
make uninstall-service
```

### Docker 部署

```bash
# 构建镜像（支持两种方式）
# 多阶段构建（容器内编译前端+后端，适用于 amd64 架构）
make docker-build

# 宿主交叉编译 + scratch 打包（适用于 ARM64 架构，树莓派等）
make docker-build-arm64

# 构建全部架构
make docker-build-all

# 推送到镜像仓库（需要先 docker/podman login）
make docker-push              # 推送 amd64
make docker-push-arm64        # 推送 arm64
make docker-push-all          # 推送全部

# 一键构建并推送
make docker-release
```

镜像版本号取自 git short SHA。例如当前提交为 `0c7e0eb`：

| 镜像 | 架构 | 基础镜像 |
|------|------|----------|
| `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:0c7e0eb` | amd64 | distroless |
| `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:0c7e0eb-arm64` | arm64 | scratch |

### 运行 Docker 容器

```bash
docker run -d \
  --name mibee-nvr \
  -p 9090:9090 \
  -v /mnt/data/nvr:/data \
  --restart unless-stopped \
  registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:0c7e0eb-arm64
```

配置文件通过卷挂载到 `/data/mibee-nvr.yaml`。WebDAV 路径可以通过环境变量覆盖。

## 系统服务配置

### 创建服务文件

```bash
# 创建 systemd 服务文件
sudo tee /etc/systemd/system/mibee-nvr.service << 'EOF'
[Unit]
Description=MiBee NVR - Network Video Recorder
After=network.target
Requires=network.target

[Service]
Type=simple
User=nvr
Group=nvr
ExecStart=/mnt/data/nvr/bin/mibee-nvr -config /mnt/data/nvr/mibee-nvr.yaml
Restart=on-failure
RestartSec=5s
WorkingDirectory=/mnt/data/nvr
LimitNOFILE=65536

# 安全加固
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/mnt/data/nvr
MemoryMax=512M

[Install]
WantedBy=multi-user.target
EOF
```

### 创建用户和目录

```bash
# 创建 nvr 用户
sudo useradd -r -s /bin/false -d /mnt/data/nvr nvr

# 创建目录结构
sudo mkdir -p /mnt/data/nvr/bin /mnt/data/nvr/recordings /mnt/data/nvr/hls
sudo chown -R nvr:nvr /mnt/data/nvr
sudo chmod 755 /mnt/data/nvr
```

### 部署二进制文件

```bash
# 复制二进制文件
sudo cp mibee-nvr-arm64 /mnt/data/nvr/bin/mibee-nvr
sudo chmod +x /mnt/data/nvr/bin/mibee-nvr

# 复制配置文件
sudo cp config.example.yaml /mnt/data/nvr/mibee-nvr.yaml

# 设置配置文件权限
sudo chown nvr:nvr /mnt/data/nvr/mibee-nvr.yaml
sudo chmod 600 /mnt/data/nvr/mibee-nvr.yaml
```

### 启动服务

```bash
# 重新加载 systemd
sudo systemctl daemon-reload

# 启用服务
sudo systemctl enable mibee-nvr

# 启动服务
sudo systemctl start mibee-nvr

# 检查服务状态
sudo systemctl status mibee-nvr

# 查看日志
sudo journalctl -u mibee-nvr -f
```

## 反向代理配置

### Caddy 配置

```caddyfile
# Caddyfile
{
    email admin@example.com
}

# MiBee NVR 主界面
https://nvr.example.com {
    reverse_proxy localhost:9090
    
    # 基本认证
    basicauth {
        admin password123
    }
}

# WebDAV 访问
https://nvr.example.com/dav/ {
    reverse_proxy localhost:9090
    basicauth {
        admin password123
    }
    # WebDAV 特定方法
    @webdav method {GET HEAD POST PUT DELETE PROPFIND PROPPATCH COPY MOVE LOCK UNLOCK}
    reverse_proxy @webdav localhost:9090
}
EOF

# 重启 Caddy
sudo systemctl restart caddy
```

### Nginx 配置

```nginx
# nginx.conf
http {
    # 基本配置
    sendfile on;
    tcp_nopush on;
    keepalive_timeout 65;
    
    # 日志格式
    log_format main '$remote_addr - $remote_user [$time_local] "$request" '
                    '$status $body_bytes_sent "$http_referer" '
                    '"$http_user_agent" "$http_x_forwarded_for"';
    
    access_log /var/log/nginx/mibee-nvr.access.log main;
    error_log /var/log/nginx/mibee-nvr.error.log;

    # 上游服务器
    upstream mibee_nvr {
        server localhost:9090;
        keepalive 32;
    }

    # MiBee NVR 主界面
    server {
        listen 80;
        server_name nvr.example.com;
        
        # 重定向到 HTTPS
        return 301 https://$server_name$request_uri;
    }

    # HTTPS 服务器
    server {
        listen 443 ssl http2;
        server_name nvr.example.com;
        
        # SSL 证书配置
        ssl_certificate /path/to/certificate.crt;
        ssl_certificate_key /path/to/private.key;
        
        # SSL 安全配置
        ssl_protocols TLSv1.2 TLSv1.3;
        ssl_ciphers ECDHE-RSA-AES128-GCM-SHA256:ECDHE-RSA-AES256-GCM-SHA384;
        ssl_prefer_server_ciphers off;
        ssl_session_cache shared:SSL:10m;
        ssl_session_timeout 1d;
        ssl_session_tickets off;
        
        # 反向代理配置
        location / {
            proxy_pass http://mibee_nvr;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            
            # 超时配置
            proxy_connect_timeout 60s;
            proxy_send_timeout 60s;
            proxy_read_timeout 60s;
        }

        # WebDAV 位置
        location /dav/ {
            proxy_pass http://mibee_nvr;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            
            # WebDAV 特定设置
            proxy_request_buffering off;
            proxy_buffering off;
        }
    }
}
```

## 访问协议说明

### Web 界面

- **URL**: `https://your-domain.com` 或 `http://your-ip:9090`
- **认证**: 需要基本认证
- **功能**: 摄像头管理、录像回放、实时预览

### FTP 访问

- **端口**: 2121（不能通过反向代理）
- **协议**: FTP（不是 SFTP）
- **认证**: 需要与 Web 界面相同的认证
- **访问**: 直接连接到服务器的 FTP 端口

### WebDAV 访问

- **URL**: `https://your-domain.com/dav/` 或 `http://your-ip:9090/dav/`
- **认证**: 需要基本认证
- **访问**: 只读访问录像文件
- **协议**: WebDAV (RFC 4918)

## 存储和性能考虑

### 内存要求

- **最小**: 512MB RAM（30秒片段）
- **推荐**: 1GB+ RAM（多摄像头）
- **片段内存使用**:
  - 30秒片段：~15-20MB 每个片段
  - 60秒片段：~30-40MB 每个片段  
  - 120秒片段：~60-80MB 每个片段

### 磁盘空间规划

- **单个摄像头**: 每天 1-5GB（取决于分辨率和帧率）
- **多个摄像头**: 按比例增加
- **保留策略**: 规划 retention_days + 清理延迟缓冲

### 文件系统考虑

- 使用 ext4 在 Linux 上获得最佳性能
- 考虑 SSD 以获得更好的写入性能
- 监控多个并发录像的磁盘 I/O

### 性能建议

1. **片段时长**: 低内存系统使用 30 秒
2. **磁盘监控**: 设置合适的 disk_threshold_percent（80-95）
3. **保留策略**: 定期清理防止磁盘空间问题
4. **网络**: 确保摄像头和服务器网络连接稳定

## 更新 MiBee NVR

### 备份配置

```bash
sudo cp /mnt/data/nvr/mibee-nvr.yaml /mnt/data/nvr/mibee-nvr.yaml.backup
```

### 停止服务

```bash
sudo systemctl stop mibee-nvr
```

### 更新二进制文件

```bash
# 下载新二进制文件或从源码构建
sudo cp mibee-nvr-arm64 /mnt/data/nvr/bin/mibee-nvr
sudo chmod +x /mnt/data/nvr/bin/mibee-nvr
```

### 启动服务

```bash
sudo systemctl start mibee-nvr
```

### 检查状态

```bash
sudo systemctl status mibee-nvr
```

## 监控和维护

### 日志管理

```bash
# 查看日志
sudo journalctl -u mibee-nvr -n 100

# 跟踪日志
sudo journalctl -u mibee-nvr -f

# 日志轮转（添加到 /etc/logrotate.d/mibee-nvr）
sudo tee /etc/logrotate.d/mibee-nvr > /dev/null <<EOF
/var/log/caddy/nvr_access.log {
    daily
    missingok
    rotate 7
    compress
    delaycompress
    notifempty
    create 644 nvr nvr
}
EOF
```

### 健康检查

```bash
# 检查服务是否运行
sudo systemctl is-active mibee-nvr

# 检查 Web 界面
curl -f http://localhost:9090/api/health

# 检查磁盘使用
df -h /mnt/data/nvr
```

### 定期维护任务

1. **检查日志**: 查看错误或警告信息
2. **监控磁盘使用**: 确保保留策略正常工作
3. **更新软件**: 定期更新到最新版本
4. **备份配置**: 定期备份配置文件

## 远程部署

### 使用 Makefile 部署到树莓派

```bash
# 设置 RPi 主机地址
export RPi_HOST=user@your-rpi-host

# 部署到远程 RPi
make deploy

# 部署后检查
make deploy-check

# 回滚到上一个版本
make rollback
```

### 远程部署流程

1. 交叉编译 ARM64 二进制文件
2. 停止远程服务并备份当前版本
3. 上传新二进制文件
4. 启动远程服务
5. 检查服务健康状态

## 故障排除

### 常见问题

1. **服务无法启动**
   ```bash
   # 检查日志
   sudo journalctl -u mibee-nvr -n 50

   # 检查配置文件语法
   sudo -u nvr /mnt/data/nvr/bin/mibee-nvr -config /mnt/data/nvr/mibee-nvr.yaml
   ```

2. **摄像头连接失败**
   ```bash
   # 测试摄像头连接
   ffmpeg -rtsp_transport tcp -i "rtsp://admin:password@192.168.1.100:554/stream" -t 5 -f null -

   # 检查网络连接
   ping 192.168.1.100
   nmap -p 554 192.168.1.100
   ```

3. **端口冲突**
   ```bash
   # 检查端口占用
   netstat -an | grep :9090

   # 查看正在使用端口的进程
   sudo lsof -i :9090
   ```

4. **权限错误**
   ```bash
   # 检查文件所有权
   ls -la /mnt/data/nvr/

   # 检查 nvr 用户的权限
   sudo -u nvr ls /mnt/data/nvr/
   ```

### 资源监控

```bash
# 监控内存使用
ps aux | grep mibee-nvr

# 监控磁盘 I/O
iostat -x 1

# 监控网络连接
netstat -an | grep :9090
```

## CLI 命令参考

### 启动命令

```bash
# 使用默认配置文件
./mibee-nvr

# 指定配置文件路径
./mibee-nvr -config /path/to/config.yaml

# 显示版本信息
./mibee-nvr -version

# 生成密码哈希
./mibee-nvr hash-password mypassword
```

### 有效的配置文件名

默认配置文件名为 `mibee-nvr.yaml`，不是 `config.yaml`。

```yaml
# observability 配置在配置文件的顶部级别
observability:
  log_level: info          # debug, info, warn, error
  log_format: text        # json, text
```

## 总结

本部署指南涵盖了 MiBee NVR 的准确部署信息，包括：

- 从源码编译和各种 Makefile 目标
- SystemD 服务配置和用户管理
- Docker 容器化部署（两种构建方式）
- 反向代理配置（Caddy/Nginx）
- 访问协议说明（Web、FTP、WebDAV）
- 存储和性能考虑
- 远程部署到树莓派的流程
- 监控、维护和故障排除

所有命令、配置文件名、系统参数都基于实际代码中的实现。请参考实际代码获取最新信息。

**重要提示**: MiBee NVR 是单二进制应用，不支持集群部署或零停机更新。每次更新需要停止服务后替换二进制文件。