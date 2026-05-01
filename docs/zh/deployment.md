# MiBee NVR 部署指南

## 概述

MiBee NVR 支持多种部署方式，包括本地开发部署、生产环境部署和分布式部署。本章详细介绍各种部署方案、配置管理和维护策略。

### 部署方式

1. **本地开发部署**: 在开发环境中快速测试和开发
2. **单机生产部署**: 在单台服务器上运行完整功能
3. **容器化部署**: 使用 Docker 进行标准化部署
4. **集群部署**: 多节点高可用部署

## 编译准备

### 环境要求

- **Go 1.22+**: 编译工具链
- **Git**: 版本控制工具
- **Make**: 构建工具（可选）

### 安装依赖

```bash
# 安装 Go
sudo apt-get update
sudo apt-get install -y golang-go

# 设置 Go 环境变量
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin

# 验证安装
go version
```

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

### 编译选项说明

```bash
# 本地编译（当前架构）
make build

# 交叉编译（ARM64）
make cross

# 调试版本
make debug

# 发布版本
make release
```

### 自定义编译

```bash
# 直接使用 go build
go build -mod=readonly -ldflags="-s -w" -o mibee-nvr ./cmd/mibee-nvr

# 包含版本信息
go build -ldflags="-X 'main.Version=v1.0.0' -X 'main.BuildTime=$(date)'" \
  -o mibee-nvr ./cmd/mibee-nvr

# 静态编译（无 CGO 依赖）
CGO_ENABLED=0 go build -ldflags="-s -w" -o mibee-nvr ./cmd/mibee-nvr
```

### 多架构交叉编译

```bash
# 使用 Makefile 多架构编译
make all-arch

# 手动交叉编译
GOOS=linux GOARCH=amd64 go build -o mibee-nvr-linux-amd64 ./cmd/mibee-nvr
GOOS=linux GOARCH=arm64 go build -o mibee-nvr-linux-arm64 ./cmd/mibee-nvr
GOOS=linux GOARCH=arm go build -o mibee-nvr-linux-arm ./cmd/mibee-nvr
```

## SystemD 服务配置

### 创建服务文件

```bash
# 创建 SystemD 服务文件
sudo tee /etc/systemd/system/mibee-nvr.service << 'EOF'
[Unit]
Description=MiBee NVR - Network Video Recorder
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=mibee
Group=mibee
WorkingDirectory=/opt/mibee-nvr
ExecStart=/opt/mibee-nvr/mibee-nvr --config /opt/mibee-nvr/config.yaml
Restart=always
RestartSec=10
Environment=PATH=/usr/local/bin:/usr/bin:/bin
Environment=GOMAXPROCS=2

# 安全设置
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/opt/mibee-nvr
ReadWritePaths=/mnt/data/nvr

# 日志设置
StandardOutput=file:/var/log/mibee-nvr.log
StandardError=file:/var/log/mibee-nvr.err
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
```

### 创建用户和目录

```bash
# 创建用户和组
sudo groupadd -r mibee
sudo useradd -r -g mibee -s /bin/false -d /opt/mibee-nvr mibee

# 创建目录结构
sudo mkdir -p /opt/mibee-nvr
sudo mkdir -p /mnt/data/nvr
sudo mkdir -p /var/log/mibee-nvr
sudo mkdir -p /etc/mibee-nvr

# 设置权限
sudo chown -R mibee:mibee /opt/mibee-nvr
sudo chown -R mibee:mibee /mnt/data/nvr
sudo chmod 755 /opt/mibee-nvr
sudo chmod 755 /mnt/data/nvr
```

### 部署二进制文件

```bash
# 复制二进制文件
sudo cp mibee-nvr /opt/mibee-nvr/
sudo chmod +x /opt/mibee-nvr/mibee-nvr

# 复制配置文件
sudo cp config.example.yaml /opt/mibee-nvr/config.yaml

# 设置配置文件权限
sudo chown mibee:mibee /opt/mibee-nvr/config.yaml
sudo chmod 600 /opt/mibee-nvr/config.yaml
```

### 启动服务

```bash
# 重新加载 SystemD
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

## Docker 部署

### 创建 Dockerfile

```dockerfile
# Dockerfile
FROM golang:1.22-alpine AS builder

# 安装构建依赖
RUN apk add --no-cache git make

WORKDIR /app

# 复制源码
COPY . .

# 编译
RUN make build

# 运行时镜像
FROM alpine:3.19

# 安装运行时依赖
RUN apk add --no-cache ca-certificates tzdata

# 创建用户
RUN addgroup -g 1000 mibee && \
    adduser -u 1000 -G mibee -s /bin/sh -D mibee

# 创建目录
RUN mkdir -p /opt/mibee-nvr /mnt/data/nvr /var/log/mibee-nvr
RUN chown -R mibee:mibee /opt/mibee-nvr /mnt/data/nvr /var/log/mibee-nvr

# 从构建器复制二进制文件
COPY --from=builder /app/mibee-nvr /opt/mibee-nvr/mibee-nvr

# 复制配置文件
COPY --chown=mibee:mibee config.example.yaml /opt/mibee-nvr/config.yaml

# 切换用户
USER mibee

# 工作目录
WORKDIR /opt/mibee-nvr

# 暴露端口
EXPOSE 9090

# 启动命令
CMD ["./mibee-nvr", "--config", "config.yaml"]
```

### 构建和运行 Docker 镜像

```bash
# 构建镜像
docker build -t mibee-nvr:latest .

# 运行容器
docker run -d \
  --name mibee-nvr \
  -p 9090:9090 \
  -v /mnt/data/nvr:/mnt/data/nvr \
  -v /etc/mibee-nvr/config.yaml:/opt/mibee-nvr/config.yaml \
  --restart unless-stopped \
  mibee-nvr:latest
```

### 使用 Docker Compose

```yaml
# docker-compose.yml
version: '3.8'

services:
  mibee-nvr:
    image: mibee-nvr:latest
    container_name: mibee-nvr
    restart: unless-stopped
    ports:
      - "9090:9090"
    volumes:
      - /mnt/data/nvr:/mnt/data/nvr
      - ./config.yaml:/opt/mibee-nvr/config.yaml
      - ./logs:/var/log/mibee-nvr
    environment:
      - GOMAXPROCS=2
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:9090/api/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 40s
    networks:
      - mibee-network

volumes:
  nvr-storage:
    driver: local

networks:
  mibee-network:
    driver: bridge
```

### 启动 Docker 服务

```bash
# 使用 Docker Compose
docker-compose up -d

# 查看状态
docker-compose ps

# 查看日志
docker-compose logs -f

# 停止服务
docker-compose down
```

## 反向代理配置

### Caddy 配置

```caddyfile
# Caddyfile
{
    admin off
    log {
        output file /var/log/caddy/access.log
    }
}

# HTTP 反向代理
http://your-domain.com {
    reverse_proxy localhost:9090 {
        header_up Host {http.reverse_proxy.upstream.hostport}
        header_up X-Real-IP {http.request.remote_host}
        header_up X-Forwarded-For {http.request.remote_host}
        header_up X-Forwarded-Proto {http.request.scheme}
    }
    
    # 静态文件缓存
    @static {
        file {
            ext .css .js .png .jpg .jpeg .gif .svg .ico .woff .woff2
        }
    }
    
    route @static {
        cache {
            static
            ttl 1h
        }
    }
}

# HTTPS 反向代理
https://your-domain.com {
    reverse_proxy localhost:9090 {
        header_up Host {http.reverse_proxy.upstream.hostport}
        header_up X-Real-IP {http.request.remote_host}
        header_up X-Forwarded-For {http.request.remote_host}
        header_up X-Forwarded-Proto {https}
    }
    
    # SSL 证书配置
    tls your-email@example.com {
        protocols tls1.2 tls1.3
        ciphers TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384 TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384 TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
    }
    
    # 安全头
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"
        X-Content-Type-Options "nosniff"
        X-Frame-Options "DENY"
        X-XSS-Protection "1; mode=block"
        Referrer-Policy "strict-origin-when-cross-origin"
        Content-Security-Policy "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'"
    }
}
```

### Nginx 配置

```nginx
# nginx.conf
events {
    worker_connections 1024;
    use epoll;
    multi_accept on;
}

http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    
    # 日志格式
    log_format main '$remote_addr - $remote_user [$time_local] "$request" '
                    '$status $body_bytes_sent "$http_referer" '
                    '"$http_user_agent" "$http_x_forwarded_for"';
    
    access_log /var/log/nginx/mibee-nvr.access.log main;
    error_log /var/log/nginx/mibee-nvr.error.log;
    
    # 基础配置
    sendfile on;
    tcp_nopush on;
    tcp_nodelay on;
    keepalive_timeout 65;
    types_hash_max_size 2048;
    
    # 缓冲区配置
    client_max_body_size 100M;
    client_body_timeout 60s;
    client_header_timeout 60s;
    send_timeout 60s;
    
    # Gzip 压缩
    gzip on;
    gzip_vary on;
    gzip_min_length 1024;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript;
    
    # 上游服务器
    upstream mibee_nvr {
        server localhost:9090;
        keepalive 32;
    }
    
    # HTTP 服务器
    server {
        listen 80;
        server_name your-domain.com www.your-domain.com;
        
        # 重定向到 HTTPS
        return 301 https://$server_name$request_uri;
    }
    
    # HTTPS 服务器
    server {
        listen 443 ssl http2;
        server_name your-domain.com www.your-domain.com;
        
        # SSL 证书配置
        ssl_certificate /path/to/your/certificate.crt;
        ssl_certificate_key /path/to/your/private.key;
        
        # SSL 安全配置
        ssl_protocols TLSv1.2 TLSv1.3;
        ssl_ciphers ECDHE-RSA-AES128-GCM-SHA256:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-RSA-AES128-SHA256:ECDHE-RSA-AES256-SHA384;
        ssl_prefer_server_ciphers off;
        ssl_session_cache shared:SSL:10m;
        ssl_session_timeout 1d;
        ssl_session_tickets off;
        
        # 安全头
        add_header Strict-Transport-Security "max-age=31536000; includeSubDomains; preload" always;
        add_header X-Content-Type-Options "nosniff" always;
        add_header X-Frame-Options "SAMEORIGIN" always;
        add_header X-XSS-Protection "1; mode=block" always;
        add_header Referrer-Policy "strict-origin-when-cross-origin" always;
        
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
            
            # 缓冲区配置
            proxy_buffering on;
            proxy_buffer_size 4k;
            proxy_buffers 8 4k;
            proxy_busy_buffers_size 8k;
            
            # WebSocket 支持
            proxy_http_version 1.1;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection "upgrade";
        }
        
        # 静态文件缓存
        location ~* \.(css|js|png|jpg|jpeg|gif|svg|ico|woff|woff2)$ {
            proxy_pass http://mibee_nvr;
            proxy_cache static_cache;
            proxy_cache_valid 1h;
            proxy_cache_key $scheme$proxy_host$request_uri;
            add_header X-Cache $upstream_cache_status;
        }
    }
}
```

## 存储配置

### 文件系统选择

```bash
# 推荐的文件系统
# 对于 SSD：ext4
# 对于 SD 卡：f2fs 或 ext4
# 对于 NFS：nfs4

# 创建 ext4 文件系统
mkfs.ext4 /dev/sdb1

# 创建 f2fs 文件系统
mkfs.f2fs /dev/sdc1
```

### 挂载配置

```bash
# 创建挂载点
sudo mkdir -p /mnt/data/nvr

# 编辑 fstab
echo "UUID=your-uuid-here /mnt/data/nvr ext4 defaults,nofail 0 2" | sudo tee -a /etc/fstab

# 临时挂载
sudo mount -a
```

### 存储优化

```bash
# 设置 I/O 调度器
echo "noop" | sudo tee /sys/block/sdb/queue/scheduler

# 设置 readahead
sudo blockdev --setra 16384 /dev/sdb

# 禁用 atime
echo "relatime" | sudo tee /sys/block/sdb/queue/rotational
```

### 存储监控

```bash
# 监控磁盘使用
df -h /mnt/data/nvr

# 监控 I/O 性能
iostat -x 1

# 监控文件系统
find /mnt/data/nvr -type f -printf "%s\n" | sort -n | tail -10
```

## 监控和日志

### 日志配置

```yaml
# config.yaml - 日志配置
logging:
  level: info
  file: /var/log/mibee-nvr.log
  max_size: 100MB
  max_backups: 5
  compress: true
  format: json
```

### 监控指标

```bash
# 健康检查
curl http://localhost:9090/api/health

# 系统统计
curl http://localhost:9090/api/stats

# 磁盘使用
df -h /mnt/data/nvr

# 进程监控
ps aux | grep mibee-nvr
```

### Prometheus 监控

```yaml
# prometheus.yml
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: 'mibee-nvr'
    static_configs:
      - targets: ['localhost:9090']
    metrics_path: /metrics
    scrape_interval: 15s
```

### Grafana 仪表板

```json
{
  "dashboard": {
    "title": "MiBee NVR 监控",
    "panels": [
      {
        "title": "系统状态",
        "type": "stat",
        "targets": [
          {
            "expr": "up{job=\"mibee-nvr\"}"
          }
        ]
      },
      {
        "title": "磁盘使用率",
        "type": "graph",
        "targets": [
          {
            "expr": "100 - (disk_free_bytes{mountpoint=\"/mnt/data/nvr\"} / disk_size_bytes{mountpoint=\"/mnt/data/nvr\"}) * 100"
          }
        ]
      }
    ]
  }
}
```

## 安全配置

### 防火墙配置

```bash
# 使用 ufw
sudo ufw allow 9090/tcp
sudo ufw allow 2121/tcp  # FTP
sudo ufw allow 8554/tcp  # RTSP
sudo ufw enable

# 使用 iptables
sudo iptables -A INPUT -p tcp --dport 9090 -j ACCEPT
sudo iptables -A INPUT -p tcp --dport 2121 -j ACCEPT
sudo iptables -A INPUT -p tcp --dport 8554 -j ACCEPT
```

### SSL/TLS 配置

```bash
# 生成自签名证书
openssl req -x509 -newkey rsa:4096 -keyout key.pem -out cert.pem -days 365 -nodes

# 配置 HTTPS
server:
  listen: ":9443"
  enable_https: true
  cert_file: "/path/to/cert.pem"
  key_file: "/path/to/key.pem"
```

### 安全加固

```bash
# 禁用不必要的服务
sudo systemctl stop telnet
sudo systemctl disable telnet

# 配置 fail2ban
echo "[mibee-nvr]
enabled = true
port = 9090
filter = mibee-nvr
logpath = /var/log/mibee-nvr.log
maxretry = 3
bantime = 3600" | sudo tee -a /etc/fail2ban/jail.local
```

## 备份和恢复

### 配置备份

```bash
#!/bin/bash
# backup-config.sh

BACKUP_DIR="/backup/mibee-nvr"
DATE=$(date +%Y%m%d_%H%M%S)

# 创建备份目录
mkdir -p $BACKUP_DIR

# 备份配置文件
cp /opt/mibee-nvr/config.yaml $BACKUP_DIR/config_$DATE.yaml

# 备份数据库
sqlite3 /mnt/data/nvr/nvr.db ".backup $BACKUP_DIR/nvr_$DATE.db"

# 备份录像列表
find /mnt/data/nvr -name "*.mp4" | sort > $BACKUP_DIR/recordings_$DATE.txt

# 压缩备份
tar -czf $BACKUP_DIR/backup_$DATE.tar.gz -C $BACKUP_DIR config_$DATE.yaml nvr_$DATE.db recordings_$DATE.txt

# 清理旧备份
find $BACKUP_DIR -name "backup_*.tar.gz" -mtime +7 -delete
```

### 自动备份

```bash
# 添加到 crontab
0 2 * * * /path/to/backup-config.sh
```

### 恢复程序

```bash
#!/bin/bash
# restore-config.sh

BACKUP_FILE=$1
RESTORE_DIR="/opt/mibee-nvr"

if [ ! -f "$BACKUP_FILE" ]; then
    echo "备份文件不存在: $BACKUP_FILE"
    exit 1
fi

# 停止服务
sudo systemctl stop mibee-nvr

# 解压备份
tar -xzf $BACKUP_FILE -C $RESTORE_DIR

# 恢复数据库
sqlite3 /mnt/data/nvr/nvr.db < $RESTORE_DIR/nvr_$(date +%Y%m%d).db

# 恢复配置文件
cp $RESTORE_DIR/config_$(date +%Y%m%d).yaml $RESTORE_DIR/config.yaml

# 启动服务
sudo systemctl start mibee-nvr

echo "恢复完成"
```

## 更新策略

### 滚动更新

```bash
#!/bin/bash
# update-mibee-nvr.sh

SERVICE_NAME="mibee-nvr"
BACKUP_DIR="/backup"
NEW_VERSION="v1.0.0"

# 创建备份
./backup-config.sh

# 停止服务
sudo systemctl stop $SERVICE_NAME

# 备份当前版本
sudo cp /opt/mibee-nvr/mibee-nvr $BACKUP_DIR/mibee-nvr_$NEW_VERSION.backup

# 下载新版本
wget -O /tmp/mibee-nvr_new https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/download/$NEW_VERSION/mibee-nvr-linux-amd64

# 验证下载
chmod +x /tmp/mibee-nvr_new
sha256sum -c checksum.txt

# 替换二进制文件
sudo cp /tmp/mibee-nvr_new /opt/mibee-nvr/mibee-nvr
sudo chmod +x /opt/mibee-nvr/mibee-nvr

# 启动服务
sudo systemctl start $SERVICE_NAME

# 检查服务状态
sleep 5
sudo systemctl status $SERVICE_NAME

# 清理临时文件
rm /tmp/mibee-nvr_new
```

### 零停机更新

```bash
#!/bin/bash
# zero-downtime-update.sh

SERVICE_NAME="mibee-nvr"
INSTANCE_COUNT=3

# 逐个更新实例
for i in $(seq 1 $INSTANCE_COUNT); do
    echo "更新实例 $i"
    
    # 停止实例
    sudo systemctl stop $SERVICE_NAME-$i
    
    # 更新二进制文件
    sudo cp /tmp/mibee-nvr_new /opt/$SERVICE_NAME-$i/mibee-nvr
    sudo chmod +x /opt/$SERVICE_NAME-$i/mibee-nvr
    
    # 启动实例
    sudo systemctl start $SERVICE_NAME-$i
    
    # 健康检查
    sleep 10
    curl -f http://localhost:9090/api/health || echo "健康检查失败"
done
```

## 故障排除

### 常见问题

1. **服务无法启动**
   ```bash
   # 检查日志
   sudo journalctl -u mibee-nvr -n 50
   
   # 检查配置文件
   sudo -u mibee /opt/mibee-nvr/mibee-nvr --config /opt/mibee-nvr/config.yaml --validate
   ```

2. **摄像头连接失败**
   ```bash
   # 测试摄像头连接
   ffmpeg -rtsp_transport tcp -i "rtsp://admin:password@192.168.1.100:554/stream" -t 5 -f null -
   
   # 检查网络连接
   ping 192.168.1.100
   nmap -p 554 192.168.1.100
   ```

3. **存储空间不足**
   ```bash
   # 检查磁盘使用
   df -h /mnt/data/nvr
   
   # 查找大文件
   find /mnt/data/nvr -type f -size +100M -exec ls -lh {} \;
   
   # 清理旧录像
   sudo systemctl start mibee-nvr-cleanup
   ```

4. **内存使用过高**
   ```bash
   # 监控内存使用
   free -h
   top -p $(pgrep mibee-nvr)
   
   # 调整配置
   # 在 config.yaml 中设置合适的 segment_duration
   ```

### 性能调优

```bash
# 系统调优
echo "vm.swappiness=10" | sudo tee -a /etc/sysctl.conf
echo "vm.vfs_cache_pressure=50" | sudo tee -a /etc/sysctl.conf

# 网络调优
echo "net.core.rmem_max = 16777216" | sudo tee -a /etc/sysctl.conf
echo "net.core.wmem_max = 16777216" | sudo tee -a /etc/sysctl.conf

# 应用调优
export GOMAXPROCS=2
export GOMEMLIMIT=1GiB
```

## 总结

本部署指南涵盖了 MiBee NVR 的各种部署方案，包括：

- 编译和构建
- SystemD 服务配置
- Docker 容器化部署
- 反向代理配置
- 存储配置
- 监控和日志
- 安全配置
- 备份和恢复
- 更新策略
- 故障排除

根据实际需求选择合适的部署方案，并定期维护和监控系统状态。建议在生产环境中使用容器化部署，这样可以提高系统的可移植性和可维护性。