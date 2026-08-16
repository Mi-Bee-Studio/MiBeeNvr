# MiBee NVR — QNAP Container Station template

One-import application template for QTS 5.x (Container Station →
Applications). No manual compose typing.

## Import steps

1. **Container Station → Applications → Create Application**.
2. Name: `mibee-nvr`. Source: paste the contents of `docker-compose.yml`
   (or upload the file, depending on your QTS version).
3. Adjust the volume path if your storage pool differs — the default
   `/share/CACHEDEV1_DATA/mibee-nvr/data` matches the typical first pool.
4. **Deploy**. The multi-arch image is pulled and started.
5. Open `http://<nas-ip>:9090` and complete the setup wizard.

## Customize before deploying (optional)

- **Storage location**: point the left side of the volume mapping at a
  volume with enough space.
- **Port conflict** (`9090` taken by QTS or another app): uncomment
  `NVR_LISTEN_PORT=8080` — or change it later in the Web UI
  (Settings → General, #270).
- **China mirror**: swap the `image:` line to
  `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:latest`.

## Why host networking

ONVIF WS-Discovery auto-scan needs UDP multicast, which Docker bridge
blocks. `network_mode: host` fixes it; do NOT combine with `ports:`.

Full guide: [docs/en/deployment-qnap.md](../../docs/en/deployment-qnap.md)
