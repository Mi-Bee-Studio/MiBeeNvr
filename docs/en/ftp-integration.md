# FTP 集成

MiBee NVR 包含内置 FTP 服务器，允许通过标准 FTP 协议访问摄像头录像和快照。支持被动模式、虚拟用户认证，便于与各种 FTP 客户端和系统集成。

## 概述

- **协议**: FTP (File Transfer Protocol)
- **模式**: 被动模式 (PASV)
- **认证**: 基本认证（虚拟用户）
- **端口**: 2121（默认）
- **路径**: `/recordings/` 和 `/snapshots/`
- **兼容性**: 支持标准 FTP 客户端

## 配置

### 基本设置

```yaml
ftp:
  enabled: true
  addr: "0.0.0.0"
  port: 2121
  port_range: "50000-50100"
  username: "ftp_user"
  password: "ftp_password"
```

### 配置选项

| 字段 | 必需 | 类型 | 默认值 | 描述 |
|------|------|------|--------|------|
| `enabled` | 是 | boolean | false | 是否启用 FTP 服务器 |
| `addr` | 是 | string | "0.0.0.0" | FTP 服务器监听地址 |
| `port` | 是 | integer | 2121 | FTP 服务器端口 |
| `port_range` | 是 | string | "50000-50100" | 被动模式端口范围 |
| `username` | 否 | string | - | FTP 用户名 |
| `password` | 否 | string | - | FTP 密码 |

### 完整配置示例

```yaml
ftp:
  enabled: true
  addr: "0.0.0.0"
  port: 2121
  port_range: "50000-50100"
  username: "nvr_admin"
  password: "secure_ftp_password"
  # 可选：TLS 加密
  tls:
    enabled: false
    cert_file: "/path/to/cert.pem"
    key_file: "/path/to/key.pem"
```

## 使用方法

### 访问 FTP 资源

FTP 服务器可通过 `ftp://localhost:2121` 访问，文件结构如下：

```text
ftp://localhost:2121/
├── recordings/
│   ├── h264/
│   │   ├── camera_1/
│   │   │   ├── 1704123456789012345.mp4
│   │   │   └── 1704123456789012346.mp4
│   │   └── camera_2/
│   │       ├── 1704123456789012347.mp4
│   │       └── 1704123456789012348.mp4
│   └── h265/
│       └── camera_1/
│           └── 1704123456789012349.mp4
└── snapshots/
    ├── camera_1/
    │   ├── 1704123456789012350.jpg
    │   └── 1704123456789012351.jpg
    └── camera_2/
        ├── 1704123456789012352.jpg
        └── 1704123456789012353.jpg
```

### FTP 命令行操作

#### 连接到 FTP 服务器

```bash
# 基本连接
ftp localhost 2121

# 指定用户名和密码
ftp -u ftp_user localhost 2121

# 在脚本中使用
echo "ftp_user
ftp_password" | ftp localhost 2121
```

#### 基本 FTP 操作

```bash
# 连接到服务器
ftp> open localhost 2121
ftp> user ftp_user
ftp> ftp_password

# 列出目录
ftp> ls
ftp> dir

# 切换目录
ftp> cd recordings/h264/camera_1
ftp> pwd

# 下载文件
ftp> get 1704123456789012345.mp4
ftp> mget *.mp4
ftp> get 1704123456789012345.mp4 local_filename.mp4

# 上传文件（仅限支持上传的配置）
ftp> put local_file.mp4
ftp> mput *.mp4

# 删除文件
ftp> delete old_file.mp4
ftp> mdelete *.tmp

# 退出
ftp> quit
```

### 使用 curl 操作 FTP

```bash
# 列出目录
curl -u ftp_user:ftp_password ftp://localhost:2121/

# 下载文件
curl -u ftp_user:ftp_password -O ftp://localhost:2121/recordings/h264/camera_1/1704123456789012345.mp4

# 查看文件信息
curl -u ftp_user:ftp_password -I ftp://localhost:2121/recordings/h264/camera_1/1704123456789012345.mp4
```

### 使用 lftp 高级操作

```bash
# 安装 lftp（Ubuntu/Debian）
sudo apt-get install lftp

# 基本连接
lftp ftp_user@localhost:2121

# 自动认证
lftp -u ftp_user,ftp_password localhost:2121

# 镜像整个目录
lftp -u ftp_user,ftp_password localhost:2121 -e "mirror -R /local/remote/ recordings/h264/camera_1/"

# 批量下载
lftp -u ftp_user,ftp_password localhost:2121 -e "get recordings/h264/camera_1/*.mp4"

# 并行下载
lftp -u ftp_user,ftp_password localhost:2121 -e "set ftp:parallel 4; mirror -P 4 recordings/h264/camera_1/ local_backup/"
```

## 客户端集成

### 文件管理器

#### Windows

1. 打开文件资源管理器
2. 在地址栏输入 `ftp://localhost:2121`
3. 输入用户名和密码
4. 访问录像和快照文件

#### macOS

1. 打开"访达"
2. 选择"前往" → "连接到服务器"
3. 输入地址 `ftp://localhost:2121`
4. 输入用户名和密码

#### Linux

1. 打开文件管理器
2. 选择"连接到服务器"
3. 输入地址 `ftp://localhost:2121`
4. 输入用户名和密码

### 媒体播放器

#### VLC 媒体播放器

```text
# 直接播放录像
ftp://ftp_user:ftp_password@localhost:2121/recordings/h264/camera_1/1704123456789012345.mp4

# 访问快照库
ftp://ftp_user:ftp_password@localhost:2121/snapshots/camera_1/
```

#### PotPlayer

```text
# 播放录像
ftp://ftp_user:ftp_password@localhost:2121/recordings/h264/camera_1/1704123456789012345.mp4
```

### 专业软件

#### Adobe Premiere Pro

1. 文件 → 打开
2. 选择 "FTP" 协议
3. 输入地址：`ftp://localhost:2121`
4. 输入用户名和密码
5. 浏览并选择要导入的录像文件

#### DaVinci Resolve

1. 媒体存储 → 添加存储
2. 选择 FTP 服务器
3. 服务器：`localhost:2121`
4. 用户名：`ftp_user`
5. 密码：`ftp_password`

## 自动化脚本

### Shell 脚本备份

```bash
#!/bin/bash
# FTP 备份脚本

FTP_SERVER="localhost"
FTP_PORT="2121"
FTP_USER="ftp_user"
FTP_PASS="ftp_password"
BACKUP_DIR="/mnt/backup/nvr"
LOG_FILE="/var/log/nvr_backup.log"

# 创建备份目录
mkdir -p "$BACKUP_DIR"

# FTP 命令脚本
FTP_SCRIPT=$(mktemp)

cat << EOF > "$FTP_SCRIPT"
open $FTP_SERVER $FTP_PORT
user $FTP_USER $FTP_PASS
binary
cd recordings/h264
lcd $BACKUP_DIR/h264
mget *
cd ../h265
lcd $BACKUP_DIR/h265
mget *
cd ../..
cd snapshots
lcd $BACKUP_DIR/snapshots
mget *
quit
EOF

# 执行 FTP 操作
echo "$(date): 开始 FTP 备份" >> "$LOG_FILE"
ftp -n -v < "$FTP_SCRIPT" >> "$LOG_FILE" 2>&1

# 清理临时文件
rm -f "$FTP_SCRIPT"

# 检查备份结果
if [ $? -eq 0 ]; then
    echo "$(date): FTP 备份成功" >> "$LOG_FILE"
else
    echo "$(date): FTP 备份失败" >> "$LOG_FILE"
fi

echo "备份完成，详细信息请查看日志文件"
```

### Python FTP 脚本

```python
#!/usr/bin/env python3
import ftplib
import os
from datetime import datetime
import logging

class FTPBackupClient:
    def __init__(self, host, port, username, password):
        self.host = host
        self.port = port
        self.username = username
        self.password = password
        self.ftp = None
        
    def connect(self):
        """连接到 FTP 服务器"""
        try:
            self.ftp = ftplib.FTP()
            self.ftp.connect(self.host, self.port, timeout=30)
            self.ftp.login(self.username, self.password)
            self.ftp.voidcmd('TYPE I')  # 二进制模式
            return True
        except Exception as e:
            logging.error(f"FTP 连接失败: {e}")
            return False
    
    def list_directory(self, remote_path):
        """列出远程目录内容"""
        if not self.ftp:
            return []
        
        files = []
        try:
            self.ftp.retrlines(f'LIST {remote_path}', files.append)
        except Exception as e:
            logging.error(f"列出目录失败: {e}")
        
        return files
    
    def download_file(self, remote_path, local_path):
        """下载单个文件"""
        if not self.ftp:
            return False
        
        try:
            os.makedirs(os.path.dirname(local_path), exist_ok=True)
            with open(local_path, 'wb') as f:
                self.ftp.retrbinary(f'RETR {remote_path}', f.write)
            return True
        except Exception as e:
            logging.error(f"下载文件失败: {e}")
            return False
    
    def mirror_directory(self, remote_path, local_path):
        """镜像整个目录"""
        if not self.ftp:
            return False
        
        try:
            # 创建本地目录
            os.makedirs(local_path, exist_ok=True)
            
            # 获取文件列表
            files = self.list_directory(remote_path)
            
            for file_info in files:
                # 解析文件名（简化版）
                filename = file_info.split()[-1]
                if filename in ['.', '..', 'README.md']:
                    continue
                
                remote_file = f"{remote_path}/{filename}"
                local_file = f"{local_path}/{filename}"
                
                if file_info.startswith('-'):
                    # 是文件，下载
                    self.download_file(remote_file, local_file)
                elif file_info.startswith('d'):
                    # 是目录，递归处理
                    self.mirror_directory(remote_file, local_file)
            
            return True
        except Exception as e:
            logging.error(f"镜像目录失败: {e}")
            return False
    
    def disconnect(self):
        """断开连接"""
        if self.ftp:
            try:
                self.ftp.quit()
            except:
                self.ftp.close()
            self.ftp = None

def main():
    logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(levelname)s - %(message)s')
    
    # 配置
    config = {
        'host': 'localhost',
        'port': 2121,
        'username': 'ftp_user',
        'password': 'ftp_password',
        'backup_dir': '/var/backup/nvr'
    }
    
    # 创建备份客户端
    backup = FTPBackupClient(
        config['host'],
        config['port'],
        config['username'],
        config['password']
    )
    
    # 连接到 FTP 服务器
    if not backup.connect():
        logging.error("无法连接到 FTP 服务器")
        return 1
    
    try:
        # 备份录像文件
        logging.info("开始备份录像文件...")
        backup.mirror_directory('/recordings/h264', f"{config['backup_dir']}/h264")
        backup.mirror_directory('/recordings/h265', f"{config['backup_dir']}/h265")
        
        # 备份快照
        logging.info("开始备份快照...")
        backup.mirror_directory('/snapshots', f"{config['backup_dir']}/snapshots")
        
        logging.info("备份完成")
        return 0
        
    except Exception as e:
        logging.error(f"备份过程中发生错误: {e}")
        return 1
    finally:
        backup.disconnect()

if __name__ == "__main__":
    exit(main())
```

### PowerShell 脚本

```powershell
# PowerShell FTP 备份脚本
param (
    [string]$Server = "localhost",
    [int]$Port = 2121,
    [string]$Username = "ftp_user",
    [string]$Password = "ftp_password",
    [string]$BackupDir = "C:\Backup\NVR",
    [string]$LogPath = "C:\Logs\nvr_backup.log"
)

function Write-Log {
    param ([string]$Message)
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    $logEntry = "$timestamp - $Message"
    Add-Content -Path $LogPath -Value $logEntry
    Write-Host $logEntry
}

function Backup-FTPFiles {
    param ([string]$RemotePath, [string]$LocalPath)
    
    Write-Log "开始备份 $RemotePath 到 $LocalPath"
    
    try {
        # 创建本地目录
        if (-not (Test-Path $LocalPath)) {
            New-Item -ItemType Directory -Path $LocalPath -Force | Out-Null
        }
        
        # 使用 WebClient 进行 FTP 操作
        $credentials = New-Object System.Net.NetworkCredential($Username, $Password)
        $webClient = New-Object System.Net.WebClient
        $webClient.Credentials = $credentials
        
        # 获取文件列表
        $url = "ftp://$Server`:$Port$RemotePath"
        $listContent = $webClient.DownloadString($url)
        
        # 解析并下载文件
        foreach ($line in $listContent -split "`r`n") {
            if ($line.Trim() -ne "") {
                $filename = $line.Split()[-1]
                if ($filename -notin @[".", ".."]) {
                    $remoteFile = "$RemotePath/$filename"
                    $localFile = Join-Path $LocalPath $filename
                    
                    try {
                        $webClient.DownloadFile("$url/$filename", $localFile)
                        Write-Log "下载: $filename"
                    } catch {
                        Write-Log "下载失败: $filename - $($_.Exception.Message)"
                    }
                }
            }
        }
        
        Write-Log "备份完成: $RemotePath"
        return $true
        
    } catch {
        Write-Log "备份失败: $RemotePath - $($_.Exception.Message)"
        return $false
    }
}

# 主程序
try {
    Write-Log "开始 FTP 备份"
    
    # 备份录像文件
    Backup-FTPFiles "/recordings/h264" "$BackupDir/h264"
    Backup-FTPFiles "/recordings/h265" "$BackupDir/h265"
    
    # 备份快照
    Backup-FTPFiles "/snapshots" "$BackupDir/snapshots"
    
    Write-Log "FTP 备份完成"
    
} catch {
    Write-Log "备份过程中发生错误: $($_.Exception.Message)"
    exit 1
}
```

## 高级配置

### TLS/SSL 安全连接

```yaml
ftp:
  enabled: true
  addr: "0.0.0.0"
  port: 990  # FTPS 默认端口
  port_range: "50000-50100"
  username: "secure_user"
  password: "secure_password"
  tls:
    enabled: true
    cert_file: "/etc/ssl/certs/nvr.crt"
    key_file: "/etc/ssl/private/nvr.key"
```

### 多用户支持

```yaml
ftp:
  enabled: true
  port: 2121
  users:
    - username: "backup_user"
      password: "backup_password"
      read_only: true
      allowed_dirs: ["/recordings/h264", "/snapshots"]
    - username: "admin_user"
      password: "admin_password"
      read_only: false
      allowed_dirs: ["/"]
    - username: "upload_user"
      password: "upload_password"
      read_only: false
      upload_only: true
      allowed_dirs: ["/uploads"]
```

### 防火墙配置

```bash
# UFW 配置（Ubuntu/Debian）
sudo ufw allow 2121/tcp      # FTP 控制端口
sudo ufw allow 50000:50100/tcp  # 被动模式端口范围

# iptables 配置
sudo iptables -A INPUT -p tcp --dport 2121 -j ACCEPT
sudo iptables -A INPUT -p tcp --dport 50000:50100 -j ACCEPT

# 指定 IP 限制
sudo iptables -A INPUT -p tcp --dport 2121 -s 192.168.1.0/24 -j ACCEPT
sudo iptables -A INPUT -p tcp --dport 2121 -j DROP
```

## 监控和维护

### 日志监控

```bash
# 查看 FTP 服务日志
journalctl -u mibee-nvr -f | grep ftp

# Docker 容器日志
docker logs -f mibee-nvr | grep ftp

# 分析 FTP 连接
sudo netstat -anp | grep :2121
```

### 磁盘空间监控

```bash
#!/bin/bash
# FTP 磁盘空间监控脚本

FTP_SERVER="localhost"
FTP_PORT="2121"
FTP_USER="ftp_user"
FTP_PASS="ftp_password"

# 获取磁盘使用情况
DISK_USAGE=$(curl -s -u $FTP_USER:$FTP_PASS ftp://$FTP_SERVER:$FTP_PORT/recordings/ | grep -o '[0-9]*%' | head -1)

# 获取文件数量
FILE_COUNT=$(curl -s -u $FTP_USER:$FTP_PASS ftp://$FTP_SERVER:$FTP_PORT/recordings/ | grep -o '[^ ]*$' | wc -l)

echo "磁盘使用: $DISK_USAGE"
echo "文件数量: $FILE_COUNT"

# 发送警告（如果需要）
if [[ ${DISK_USAGE%?} -gt 90 ]]; then
    echo "警告：FTP 磁盘使用率超过 90%"
    # 可以发送邮件或通知
fi
```

### 自动清理脚本

```bash
#!/bin/bash
# 自动清理 FTP 老文件脚本

CLEAN_THRESHOLD_DAYS=30
FTP_SERVER="localhost"
FTP_PORT="2121"
FTP_USER="ftp_user"
FTP_PASS="ftp_password"

# 清理录像文件
find /var/lib/mibee-nvr/recordings/ -type f -mtime +$CLEAN_THRESHOLD_DAYS -delete

# 清理快照文件
find /var/lib/mibee-nvr/snapshots/ -type f -mtime +7 -delete

# 清理 FTP 临时文件
find /tmp/ -name "ftp*" -mtime +1 -delete

echo "FTP 清理完成"
```

## 故障排除

### 常见问题

**连接被拒绝**
```text
ftp: Connection refused
```
**解决方案**：确保 FTP 服务器已启用并运行在正确端口

**认证失败**
```text
530 Login incorrect.
```
**解决方案**：检查用户名和密码配置

**被动模式失败**
```text
425 Can't open data connection.
```
**解决方案**：检查被动模式端口范围是否正确，确保防火墙允许相关端口

**传输超时**
```text
i/o timeout
```
**解决方案**：检查网络连接，增加超时设置

### 调试方法

**启用调试日志**
```yaml
observability:
  log_level: "debug"
```

**测试 FTP 连接**
```bash
# 基本连接测试
ftp localhost 2121

# 使用 telnet 测试端口
telnet localhost 2121

# 使用 nmap 测试端口
nmap -p 2121 localhost
```

**检查配置文件**
```bash
# 验证配置
./mibee-nvr -config mibee-nvr.yaml --validate
```

## 性能优化

### 网络优化

```bash
# 调整 TCP 窗口大小
sysctl -w net.core.rmem_max=134217728
sysctl -w net.core.wmem_max=134217728
sysctl -w net.ipv4.tcp_rmem="4096 87380 134217728"
sysctl -w net.ipv4.tcp_wmem="4096 65536 134217728"
```

### 缓存设置

```yaml
# 在配置中添加缓存优化
storage:
  cache_size: "256MB"
  max_open_files: 1000
```

### 负载均衡

对于高并发访问场景，可以配置多个 FTP 实例：

```yaml
# 主服务器配置
ftp:
  enabled: true
  addr: "192.168.1.100"
  port: 2121

# 备份服务器配置
backup_ftp:
  enabled: true
  addr: "192.168.1.101"
  port: 2121
  username: "backup_user"
  password: "backup_password"
```

## 安全最佳实践

1. **使用强密码**：为 FTP 用户设置强密码
2. **限制访问**：只允许来自可信网络的访问
3. **定期轮换密码**：定期更新 FTP 用户密码
4. **使用 TLS 加密**：在生产环境中使用 FTPS
5. **监控日志**：定期检查 FTP 访问日志
6. **备份策略**：实施定期的自动备份策略
7. **访问控制**：根据需要限制用户访问目录

通过 FTP 集成，MiBee NVR 可以轻松与各种 FTP 客户端和系统集成，实现自动化备份、远程访问和文件管理等功能。