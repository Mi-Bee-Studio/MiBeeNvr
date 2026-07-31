# Deploy on unRAID

> Install MiBee NVR from the unRAID **Community Applications** store, or as a custom Docker template. unRAID is the friendliest NAS platform for this NVR: `host` networking is a first-class mode, so ONVIF camera auto-discovery works out of the box.

## Why host networking is required

ONVIF WS-Discovery sends UDP probes to the multicast group `239.255.255.250:3702`. Docker's default **bridge** network does not forward multicast, which breaks camera auto-discovery. The template therefore pins `Network=host`: the container shares the host network stack and multicast flows normally. Manual camera entry (RTSP/ONVIF URL) still works on bridge, but auto-discovery will not.

## Option A — Install from Community Applications (recommended)

Once the template is merged into the CA store:

1. **Apps** tab → search `mibee-nvr` → click **Install**.
2. Set the **Data** path to a share on your storage pool (ideally parity-protected or on an external drive), e.g. `/mnt/user/appdata/mibee-nvr/data`.
3. Leave **Network** on `host`.
4. Set **NVR_UID / NVR_GID** to match the share owner (defaults are unRAID's `nobody:users` = `99:100`).
5. Click **Apply**. The container pulls the multi-arch image and starts.
6. Open `http://<server-ip>:9090` and complete the setup wizard.

## Option B — Custom Docker template

If the template isn't in CA yet, add it manually:

1. **Docker** tab → **Add Container** → **Template** dropdown → **My Templates** → name it `mibee-nvr`.
2. Set **Repository** to `ghcr.io/mi-bee-studio/mibeenvr:latest`.
3. Set **Network Type** to `host`.
4. Add a **Path**: Host Path = your share, Container Path = `/data`, Access = `Read/Write`.
5. Add **Variables**: `NVR_DATA_DIR=/data`, optionally `NVR_UID`/`NVR_GID`.
6. Set **WebUI** to `http://[IP]:[PORT:9090]`.
7. **Apply**.

The raw template lives at [`deploy/unraid/mibee-nvr.xml`](../../deploy/unraid/mibee-nvr.xml).

## Port conflicts

On host networking the container binds directly to the host. If `9090` (Web/API) or `2121` (FTP) is already taken, do **not** remap ports — change the app's own port in `mibee-nvr.yaml`:

```yaml
server:
  listen: ":8080"     # Web/API
ftp:
  port: 2121          # FTP control
  passive_port_range: "2122-2140"
```

## Updates

- **Manual / pinned (recommended for recording-critical setups):** set the image tag to a specific release (e.g. `mibeenvr:0.10.0`), then `docker pull` + recreate to update. Pinning also gives you a clean rollback target.
- **Automatic:** the optional Watchtower profile (see [Auto-update guide](deployment-autoupdate.md)) can pull `:latest` on a schedule. Automatic updates trade convenience for rollback risk — keep a known-good tag handy.

Recordings, the SQLite DB, config, and AI models all live under `/data`, so recreating the container never touches your data.

## Architecture

The image supports `linux/amd64`, `linux/arm64`, and `linux/arm/v7`; unRAID hosts are x86_64, so `amd64` is pulled automatically. armv7 is redundant here but harmless.
