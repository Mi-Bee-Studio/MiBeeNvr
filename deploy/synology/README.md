# MiBee NVR — Synology Container Manager template

One-import project template for DSM 7.2+ (Container Manager → Project).
No manual compose typing — download `docker-compose.yml`, upload, done.

## Import steps

1. **File Station** → create the project folder `/volume1/docker/mibee-nvr/`.
2. Upload this folder's `docker-compose.yml` into it (File Station → Upload).
3. **Container Manager → Project → Add**:
   - Project name: `mibee-nvr`
   - Path: `/volume1/docker/mibee-nvr`
   - Source: **Upload docker-compose.yml** → pick the uploaded file
4. **Next → Done**. The multi-arch image is pulled and started.
5. Open `http://<nas-ip>:9090` and complete the setup wizard.

## Customize before starting (optional)

- **Storage location**: change the left side of the volume mapping to a
  shared folder with enough space, e.g. `/volume2/recordings:/data`.
- **Port conflict** (`9090` taken): uncomment `NVR_LISTEN_PORT=8080` —
  or change it later in the Web UI (Settings → General, #270).
- **China mirror**: swap the `image:` line to
  `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:latest`.

## Why host networking

ONVIF WS-Discovery auto-scan needs UDP multicast, which Docker bridge
blocks. `network_mode: host` fixes it; `ports:` must NOT be declared
together with host mode (DSM rejects the compose).

Full guide: [docs/en/deployment-synology.md](../../docs/en/deployment-synology.md)
