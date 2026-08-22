# WebDAV / FTP Storage

> Applies to MiBeeNvr v0.11.0

MiBee NVR includes built-in WebDAV and FTP servers for accessing recording files in read-only or read-write mode.

## Feature Comparison

| Feature | WebDAV | FTP |
|---------|--------|-----|
| Protocol | HTTP/HTTPS | FTP |
| Encryption | ✅ TLS | ✅ FTPS |
| Browser access | ✅ | ❌ |
| CLI tools | ✅ rclone / curl | ✅ ftp / lftp |
| File manager | ✅ Native support | ✅ Requires FTP client |
| Transfer efficiency | Medium | High (binary mode) |

## WebDAV Configuration

### Enable WebDAV

```yaml
webdav:
  enabled: true
  read_write: false               # false = read-only, true = read-write
  port: 9090                      # Shares the HTTP port by default
  path: "/dav"                    # WebDAV path
```

### Read-Only Mode (Default)

In read-only mode, WebDAV lets you browse and download recordings but not delete them:

```yaml
webdav:
  enabled: true
  read_write: false
```

### Read-Write Mode

Enabling read-write mode allows managing recording files through WebDAV:

```yaml
webdav:
  enabled: true
  read_write: true
```

> **Warning**: Read-write mode allows deleting recording files. Use with caution.

### Accessing WebDAV

```bash
# Browser access
# Open https://your-nvr-address:9090/dav/

# rclone mount
rclone mount nvr:/ /mnt/nvr --vfs-cache-mode full

# curl download
curl -u admin:password \
  "http://192.168.1.50:9090/dav/recordings/front-door/2026-08-18/00-00-00.mp4" \
  -o recording.mp4
```

### rclone Configuration

```ini
# ~/.config/rclone/rclone.conf
[nvr]
type = webdav
url = http://192.168.1.50:9090/dav/
vendor = other
user = admin
pass = your-password
```

### File Manager Mount

| Operating System | Method |
|------------------|--------|
| Windows | Map network drive → `\\192.168.1.50@9090\dav` |
| macOS | Finder → Go → Connect to Server → `http://192.168.1.50:9090/dav/` |
| Linux | File manager → Locations → Other Locations → `davs://192.168.1.50:9090/dav/` |

## FTP Configuration

### Enable FTP

```yaml
ftp:
  enabled: true
  port: 2121
  read_write: false               # false = read-only, true = read-write
  max_connections: 10             # Maximum concurrent connections
```

### Read-Only Mode (Default)

```yaml
ftp:
  enabled: true
  port: 2121
  read_write: false
```

### Read-Write Mode

```yaml
ftp:
  enabled: true
  port: 2121
  read_write: true
```

### Accessing FTP

```bash
# Command-line FTP
ftp 192.168.1.50 2121
# Username: admin
# Password: your-password

# lftp (supports recursive download)
lftp -u admin,password 192.168.1.50:2121
mirror --reverse /recordings/front-door/ ./local-folder/

# WinSCP / FileZilla
# Host: 192.168.1.50
# Port: 2121
# Username: admin
# Password: your-password
```

## Directory Structure

The directory structure served by WebDAV and FTP:

```text
/
├── recordings/                   # Recording files
│   ├── front-door/               # Camera ID
│   │   ├── 2026-08-18/           # Date directory
│   │   │   ├── 00-00-00.mp4     # Recording segment
│   │   │   ├── 00-01-00.mp4
│   │   │   └── ...
│   │   └── 2026-08-17/
│   │       └── ...
│   └── driveway/
│       └── ...
├── snapshots/                    # Snapshot images (if enabled)
└── timelapse/                    # Timelapse videos (if enabled)
```

## Security Configuration

### TLS Encryption

WebDAV supports HTTPS:

```yaml
webdav:
  enabled: true
  tls:
    enabled: true
    cert_file: "/path/to/cert.pem"
    key_file: "/path/to/key.pem"
```

### FTPS Encryption

FTP supports FTPS (FTP over TLS):

```yaml
ftp:
  enabled: true
  tls:
    enabled: true
    cert_file: "/path/to/cert.pem"
    key_file: "/path/to/key.pem"
```

### Access Control

Restrict access to specific IPs:

```yaml
webdav:
  enabled: true
  allowed_ips:
    - "192.168.1.0/24"            # LAN only
    - "10.0.0.0/8"

ftp:
  enabled: true
  allowed_ips:
    - "192.168.1.0/24"
```

## FAQ

### WebDAV Connection Fails

1. **Authentication failed**: verify the username and password are correct.
2. **Wrong path**: the WebDAV path is usually `/dav/` (note the trailing slash).
3. **TLS certificate**: if using a self-signed certificate, the client must trust it.
4. **Firewall**: ensure port 9090 is open.

### FTP Connection Fails

1. **Passive mode**: some firewalls require FTP passive mode.
2. **Port**: the default FTP port is 2121 (not 21).
3. **Authentication failed**: verify the username and password are correct.
4. **Firewall**: ensure port 2121 is open.

### Slow Transfer Speeds

- **WebDAV**: higher HTTP overhead, better suited for small files.
- **FTP**: more efficient in binary mode.
- **Recommendation**: use FTP for large file transfers, WebDAV for browsing or small files.

### Recordings Disappear After Deletion

- If `read_write: true`, both WebDAV and FTP can delete recordings.
- Keep read-only mode (`read_write: false`) to prevent accidental deletion.
- To delete recordings, use the web UI instead.

## Performance Tuning

### Concurrent Connections

```yaml
webdav:
  max_connections: 20

ftp:
  max_connections: 10
```

### Cache Configuration

```yaml
webdav:
  cache:
    enabled: true
    max_size: "1G"                 # Cache size
    ttl: "5m"                      # Cache expiry
```

## Next Steps

- [ONVIF Discovery](onvif-discovery.md) — zero-configuration camera setup
- [SRT / RTMP Ingest](srt-rtmp.md) — push-stream camera configuration
- [Recording & Playback](recording-playback.md) — recording management
