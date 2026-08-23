# WebDAV Integration

MiBee NVR includes a built-in WebDAV server that provides access to camera recordings and snapshots through the standard WebDAV protocol. Supports both read-only and read-write modes, making it easy to integrate with third-party systems.

## Overview

- **Protocol**: WebDAV (Web-based Distributed Authoring and Versioning)
- **Access Method**: Standard WebDAV client
- **Path Prefix**: `/dav` (configurable)
- **Mode**: Read-only or read-write
- **Authentication**: Basic HTTP authentication
- **Compatibility**: Supports standard WebDAV clients

## Configuration

### Basic Settings

```yaml
webdav:
  enabled: true
  path_prefix: "/dav"
  read_write: false
  username: "dav_user"
  password: "dav_password"
```

### Configuration Options

| Field | Required | Type | Default | Description |
|------|----------|------|--------|----------|
| `enabled` | Yes | boolean | false | Enable WebDAV server |
| `path_prefix` | No | string | "/dav" | WebDAV URL prefix |
| `read_write` | No | boolean | false | Allow write operations |
| `username` | No | string | - | WebDAV username (if authentication enabled) |
| `password` | No | string | - | WebDAV password (if authentication enabled) |

### Full Configuration Example

```yaml
webdav:
  enabled: true
  path_prefix: "/video"
  read_write: false
  username: "admin"
  password: "secure_password"
```

## Usage

### Accessing WebDAV Resources

The WebDAV server can be accessed at `http://localhost:9090/dav`, with the following file structure:

```text
/dav/
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

### WebDAV Operations

#### Read-only Mode

**Basic Access**:
```bash
# List root directory
curl -u admin:password http://localhost:9090/dav/

# List recordings directory
curl -u admin:password http://localhost:9090/dav/recordings/h264/

# Download recording file
curl -u admin:password -O http://localhost:9090/dav/recordings/h264/camera_1/1704123456789012345.mp4

# Download snapshot
curl -u admin:password -O http://localhost:9090/dav/snapshots/camera_1/1704123456789012350.jpg
```

**File Browser**:
- Windows: Open File Explorer, enter `http://localhost:9090/dav`
- macOS: Connect to server, address `http://localhost:9090/dav`
- Linux: File Manager → Connect to Server → Address `http://localhost:9090/dav`

#### Read-write Mode

After enabling read-write mode, you can upload files and create directories:

```yaml
webdav:
  enabled: true
  path_prefix: "/dav"
  read_write: true
  username: "upload_user"
  password: "upload_password"
```

**Upload Files**:
```bash
# Create directory
mkdir -p /tmp/test_upload
echo "test content" > /tmp/test_upload/test.txt

# Upload file
curl -u upload_user:upload_password -T /tmp/test_upload/test.txt \
  http://localhost:9090/dav/recordings/h264/uploaded_files/

# Upload directory
curl -u upload_user:upload_password -T /tmp/test_upload/ \
  http://localhost:9090/dav/recordings/h264/uploaded_directory/
```

## Client Integration

### Windows

1. Open File Explorer
2. Enter `http://localhost:9090/dav` in the address bar
3. Enter username and password
4. Access recordings and snapshot files

### macOS

1. Open Finder
2. Go → Connect to Server
3. Enter address `http://localhost:9090/dav`
4. Enter username and password

### Linux

1. Open file manager (Nautilus, Dolphin, Thunar, etc.)
2. Select "Connect to Server"
3. Enter address `http://localhost:9090/dav`
4. Enter username and password

### Third-party Applications

#### VLC Media Player

```bash
# Direct playback of recordings
vlc http://localhost:9090/dav/recordings/h264/camera_1/1704123456789012345.mp4

# Access snapshot library
vlc http://localhost:9090/dav/snapshots/camera_1/
```

#### PotPlayer

```text
# Play recording
http://localhost:9090/dav/recordings/h264/camera_1/1704123456789012345.mp4

# Browse snapshots
http://localhost:9090/dav/snapshots/camera_1/
```

#### Adobe Premiere Pro

1. File → Open
2. Enter URL: `http://localhost:9090/dav/recordings/h264/`
3. Select recording files to import

## Cloud Storage Integration

### Cloud Drive Backup

Use rclone to sync WebDAV resources to cloud storage:

```bash
# Configure rclone remote
rclone config

# Set up remote named backup
# Choose WebDAV
# Address: http://localhost:9090/dav
# Username: admin
# Password: password
# Rename to backup

# Perform sync
rclone sync /mnt/disk/nvr backup:video/recordings/

# Regular backup script
#!/bin/bash
rclone sync /var/lib/mibee-nvr/recordings/ backup:recordings/
rclone sync /var/lib/mibee-nvr/snapshots/ backup:snapshots/
```

### Windows Backup Tools

Use Robocopy for backup:

```batch
@echo off
set SOURCE=D:\mibee-nvr\recordings\
set DEST=\\192.168.1.100\dav\recordings\
set USER=admin
set PASS=password

net use %DEST% %PASS% /user:%USER%
robocopy %SOURCE% %DEST% /E /R:2 /W:5
net use %DEST% /delete
```

## Security Configuration

### HTTPS Support

It's recommended to enable HTTPS through a reverse proxy:

```nginx
# Nginx configuration example
server {
    listen 443 ssl;
    server_name nvr.example.com;
    
    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;
    
    location /dav/ {
        proxy_pass http://localhost:9090/;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        auth_basic "WebDAV Access";
        auth_basic_user_file /path/to/.htpasswd;
    }
}
```

### User Authentication

Use htpasswd to generate user passwords:

```bash
# Install htpasswd (Ubuntu/Debian)
sudo apt-get install apache2-utils

# Create user
htpasswd -c /etc/nginx/.htpasswd dav_user

# Add more users
htpasswd /etc/nginx/.htpasswd upload_user
```

### Network Restrictions

Use firewall to restrict access:

```bash
# Allow specific IP to access WebDAV
iptables -A INPUT -p tcp --dport 9090 -s 192.168.1.0/24 -j ACCEPT
iptables -A INPUT -p tcp --dport 9090 -j DROP

# Or use UFW
ufw allow from 192.168.1.0/24 to any port 9090
ufw deny to any port 9090
```

## Troubleshooting

### Common Issues

**Connection Refused**
```text
curl: (7) Failed to connect to localhost port 9090: Connection refused
```
**Solution**: Ensure WebDAV is enabled and running

**Authentication Failed**
```text
HTTP/1.1 401 Unauthorized
```
**Solution**: Check if username and password are correct

**Permission Error**
```text
HTTP/1.1 403 Forbidden
```
**Solution**: Check read-write mode settings, confirm directory permissions

**Timeout Error**
```text
curl: (28) Operation timed out after 30001 milliseconds
```
**Solution**: Network issue, check connection or increase timeout

### Debugging Methods

**Enable Debug Logs**
```yaml
observability:
  log_level: "debug"
```

**Test WebDAV Connection**
```bash
# Test basic connection
curl -v http://localhost:9090/dav/

# Test authentication
curl -v -u admin:password http://localhost:9090/dav/

# Test file upload (read-write mode only)
curl -v -u admin:password -T /etc/hosts http://localhost:9090/dav/test.txt
```

**Check System Logs**
```bash
# View detailed logs
journalctl -u mibee-nvr -f | grep webdav

# Docker container
docker logs -f mibee-nvr | grep webdav
```

### Performance Optimization

#### Large File Upload

```yaml
# Adjust server configuration
server:
  max_upload_size: "1GB"
  read_timeout: "300s"
  write_timeout: "300s"
```

#### Network Optimization

```bash
# Adjust TCP settings (for large file transfers)
sysctl -w net.core.rmem_max=134217728
sysctl -w net.core.wmem_max=134217728
sysctl -w net.ipv4.tcp_rmem="4096 87380 134217728"
sysctl -w net.ipv4.tcp_wmem="4096 65536 134217728"
```

## Integration Examples

### Apple Time Machine

Implement Time Machine backup via WebDAV:

```bash
# Create backup task
defaults write com.apple.TimeMachine DoNotOfferNewDisks false
defaults write com.apple.TimeMachine CustomBackupDisk "http://localhost:9090/dav/time_machine"

# Or use manual setup
# Open Time Machine → Select Disk → Enter WebDAV address
```

### rsync Backup

```bash
# Use rsync to backup to WebDAV
rsync -avz /var/lib/mibee-nvr/recordings/ \
  admin@localhost:/var/lib/mibee-nvr/recordings/backup/

# Exclude specific files
rsync -avz --exclude='*.tmp' /var/lib/mibee-nvr/recordings/ \
  admin@localhost:/var/lib/mibee-nvr/recordings/backup/
```

### Python Automation Script

```python
#!/usr/bin/env python3
import requests
import os
from datetime import datetime, timedelta

def download_snapshots(camera_id, output_dir):
    """Download snapshots for a specific camera"""
    base_url = "http://localhost:9090/dav/snapshots"
    auth = ("admin", "password")
    
    url = f"{base_url}/{camera_id}/"
    response = requests.get(url, auth=auth)
    
    if response.status_code == 200:
        # Create output directory
        os.makedirs(output_dir, exist_ok=True)
        
        # Here we need to parse WebDAV list response
        # Actual implementation needs to handle WebDAV XML response
        print(f"Downloading snapshots for camera {camera_id} to {output_dir}")

def list_recordings(camera_id=None, encoding=None):
    """List recording files"""
    base_url = "http://localhost:9090/dav/recordings"
    auth = ("admin", "password")
    
    if camera_id and encoding:
        url = f"{base_url}/{encoding}/{camera_id}/"
    elif encoding:
        url = f"{base_url}/{encoding}/"
    else:
        url = f"{base_url}/"
    
    response = requests.get(url, auth=auth)
    
    if response.status_code == 200:
        print("Available files:")
        print(response.text)

if __name__ == "__main__":
    # Example usage
    list_recordings(camera_id="camera_1", encoding="h264")
    download_snapshots("camera_1", "/tmp/snapshots")
```

### Node.js Script

```javascript
const fs = require('fs');
const axios = require('axios');

class WebDAVClient {
  constructor(baseUrl, username, password) {
    this.baseUrl = baseUrl;
    this.auth = {
      username: username,
      password: password
    };
  }

  async listDirectory(path) {
    const url = `${this.baseUrl}${path}`;
    const response = await axios.get(url, {
      auth: this.auth,
      headers: {
        Depth: 1
      }
    });
    return response.data;
  }

  async downloadFile(remotePath, localPath) {
    const url = `${this.baseUrl}${remotePath}`;
    const response = await axios.get(url, {
      auth: this.auth,
      responseType: 'stream'
    });
    
    const writer = fs.createWriteStream(localPath);
    response.data.pipe(writer);
    
    return new Promise((resolve, reject) => {
      writer.on('finish', resolve);
      writer.on('error', reject);
    });
  }

  async uploadFile(localPath, remotePath) {
    const url = `${this.baseUrl}${remotePath}`;
    const fileData = fs.readFileSync(localPath);
    
    await axios.put(url, fileData, {
      auth: this.auth,
      headers: {
        'Content-Type': 'application/octet-stream'
      }
    });
  }
}

// Usage example
const client = new WebDAVClient('http://localhost:9090/dav', 'admin', 'password');

// List directory
client.listDirectory('/recordings/h264/').then(console.log);

// Download file
client.downloadFile('/recordings/h264/camera_1/latest.mp4', '/tmp/latest.mp4');

// Upload file
client.uploadFile('/tmp/upload.mp4', '/recordings/h264/uploaded/video.mp4');
```

## Best Practices

1. **Regular cleanup**: Set up automatic cleanup policies to avoid storage space exhaustion
2. **Backup strategy**: Regularly backup important recordings to other locations
3. **Access control**: Set appropriate access permissions for different users
4. **Monitoring**: Monitor WebDAV server usage and performance
5. **Logging**: Enable detailed logs for troubleshooting
6. **Network security**: Use HTTPS and strong passwords to protect data security

### Monitoring Script Example

```bash
#!/bin/bash
# Monitor WebDAV usage

WEBDAV_URL="http://localhost:9090/dav"
USERNAME="admin"
PASSWORD="password"

# Get disk usage
DISK_USAGE=$(curl -s -u $USERNAME:$PASSWORD $WEBDAV_URL/ | grep -o '[0-9]*%' | head -1)

# Get file count
FILE_COUNT=$(curl -s -u $USERNAME:$PASSWORD $WEBDAV_URL/recordings/ | grep -o '<[^>]*>' | wc -l)

echo "Disk usage: $DISK_USAGE"
echo "File count: $FILE_COUNT"

# Send warning if usage exceeds 90%
if [[ ${DISK_USAGE%?} -gt 90 ]]; then
    echo "Warning: Disk usage exceeds 90%"
    # Could send email or notification
fi
```

Through WebDAV integration, MiBee NVR can easily integrate with various third-party systems to achieve automatic backup, remote access, and file management capabilities.