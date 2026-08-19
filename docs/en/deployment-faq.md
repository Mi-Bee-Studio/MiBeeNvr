# Deployment FAQ — NAS Packaging, Port Conflicts, Auto-Upgrade

> Applies to v0.11.0+. Covers upgrade mechanics and port-conflict protection across NAS platforms.
> Chinese version: `docs/zh/deployment-faq.md`.

## Contents

- [Q1: After the v0.11.0 stable release, how does each NAS platform auto-upgrade?](#q1-after-the-v0110-stable-release-how-does-each-nas-platform-auto-upgrade)
- [Q2: How do I prevent port conflicts? Can the port be set at init? Changed after install?](#q2-how-do-i-prevent-port-conflicts-can-the-port-be-set-at-init-changed-after-install)

---

## Q1: After the v0.11.0 stable release, how does each NAS platform auto-upgrade?

### 1.1 The in-app "upgrade sensing" layer (present, but never performs upgrades)

The app only **notifies** — it never **acts**.

- The backend polls GitHub Releases on a timer and caches the latest tag.
- Web Settings → "Check for Updates" shows: current version, latest version, `update_available`, changelog, and a `deployment` field (`"docker"` / `"binary"`, used to pick which upgrade instructions to display).
- Source comments are explicit: `update.ts` — "sensing layer only (never executes an upgrade)".

> **Conclusion: the app is only a sensor. The actual upgrade is decided by the deployment vehicle.**

### 1.2 Upgrade paths per NAS platform

Grouped by **deployment vehicle** (not NAS brand — almost every NAS runs the Docker image):

| Vehicle | Auto-upgrade method | Status |
|---|---|---|
| **Docker (Synology/QNAP/unRAID/ZSpace/iStoreOS alike)** | **Watchtower**. The compose file ships an `--profile auto-update` that scans containers labeled `com.centurylinklabs.watchtower.enable=true`, pulls new images and rolls recreations. Default interval hourly, tunable in compose | ✅ ready — see `deployment-autoupdate.md` |
| **Synology DSM 7.2+** | Besides Watchtower, Container Manager's built-in "Schedule Task → auto-update image" | ✅ |
| **QNAP** | Container Station has no built-in auto-update → rely on Watchtower | ✅ |
| **unRAID** | Community Applications panel "Update" (manual); or add Watchtower | ✅ |
| **fnOS (`.fpk` package)** | Dual channel: ① the underlying Docker image via Watchtower (automatic); ② `.fpk` version bumps re-submitted to the App Center, which then pushes an app update (manual review) | ⚠️ image layer automatic + store layer manual |
| **Bare-metal systemd (`install.sh`)** | **No auto-upgrade.** Re-run `install.sh`, or `install.sh --version vX.Y.Z` to pin a tag | ❌ gap |

### 1.3 User checklist when upgrading

**Docker users (recommended path):**
```bash
# manual upgrade
docker compose pull && docker compose up -d

# automatic upgrades (one-time enable)
docker compose --profile auto-update up -d   # starts Watchtower
```

**Bare-metal systemd users:**
```bash
# fetch and install the latest release (replaces the binary + restarts the service)
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | sudo bash

# or pin a version
sudo install.sh --version v0.11.0
```

### 1.4 Should we build an in-app one-click self-upgrade?

**Current decision: no.** Reasons:

- Self-replacement + restart of a systemd process requires root / `CAP_SYS_ADMIN` — impossible inside the container, and Docker users can't grant it.
- Ports, data directories, and config paths differ per platform; a self-upgrade script covering every scenario is high-maintenance.
- At v0.11.0 Docker is the primary path and Watchtower suffices; for bare metal, re-running `install.sh` is a single low-friction command.

**Conclusion: Docker = fully automatic via Watchtower; bare metal = semi-automatic via re-running `install.sh`; the app only notifies.** If needed later, add a `--self-update` subcommand in 1.x, bare-metal only.

### 1.5 Data safety during upgrades

- Config (`mibee-nvr.yaml`) and the database (SQLite WAL) live in the data directory (Docker: `/data`; bare metal: `/var/lib/mibee-nvr`) — **upgrades swap only the binary/image and never touch data**.
- Before a Docker upgrade, verify the volume mount (`/mnt/external/nvr:/data`); a mis-mounted recreate loses data.
- Before major-version upgrades, back up `mibee-nvr.yaml` + `*.db`.

---

## Q2: How do I prevent port conflicts? Can the port be set at init? Changed after install?

### 2.1 The architectural fact to understand first (core)

This project's **ONVIF WS-Discovery uses UDP multicast `239.255.255.250:3702`**, which forces every NAS Docker compose to use `network_mode: host`.

**With host networking, Docker port mapping (`ports: "9091:9090"`) is completely inert** — the container occupies host ports directly and Docker-level remapping cannot help.

> **Conclusion: on NAS, the only fix for a port conflict is changing the app config `server.listen`, not the Docker port mapping.** Different from typical Docker apps — make sure users know.

### 2.2 Current capability matrix (verified against code)

| Capability | Status | Notes |
|---|---|---|
| Set port at `init` | ✅ supported | `mibee-nvr init --listen :PORT`, default `:9090`, written to `server.listen` in `mibee-nvr.yaml` |
| Change port after install | ✅ | Edit `server.listen` in `mibee-nvr.yaml` → **restart** the process/container |
| Change port via Web UI | ✅ supported | Settings → General exposes the listen port (writes `server.listen`; backend PUT accepted since #270); restart required after saving |
| Hot reload (no restart) | ❌ not supported | An HTTP listener cannot re-bind at runtime; restart is mandatory |
| Avoid conflicts via Docker port mapping | ❌ inert under host networking | All NAS composes use `network_mode: host` |

### 2.3 Per-package specifics

| Package form | Set port at install | Change port after install |
|---|---|---|
| **`install.sh` (bare metal)** | ✅ supported (#268): `install.sh --port 9091`, or a TTY interactive prompt (pipe mode defaults to `9090` without blocking) | ✅ Edit `/var/lib/mibee-nvr/mibee-nvr.yaml` `server.listen` → `sudo systemctl restart mibee-nvr` |
| **Docker compose (NAS generic)** | ✅ via the mounted config or env | ✅ Edit `mibee-nvr.yaml` in the config volume → `docker compose restart mibee-nvr` |
| **fnOS `.fpk`** | Docker-compose underneath — same as above | Same as above |
| **unRAID CA template** | The XML template's `WebUI` field points at `9090`, but the real port comes from the in-container config | Edit `/mnt/user/appdata/mibee-nvr/data/mibee-nvr.yaml` → restart the container from the Docker panel |

### 2.4 Standard procedure to change the port (after install)

**Bare metal:**
```bash
sudo sed -i 's/^listen: :9090/listen: :9091/' /var/lib/mibee-nvr/mibee-nvr.yaml
# or edit the file directly: server.listen -> :9091
sudo systemctl restart mibee-nvr
```

**Docker (NAS — prefer the env var):**

With host networking the Docker mapping is inert; change the port with the `NVR_LISTEN_PORT` environment variable (the binary overrides `server.listen` at startup, see #269):
```yaml
# add to docker-compose.host.yml environment
environment:
  - NVR_LISTEN_PORT=9091
```
```bash
docker compose up -d   # takes effect on container (re)start, with or without an existing config
```

Or edit `mibee-nvr.yaml` in the mounted volume (`server.listen`) and `docker compose restart mibee-nvr`.

Relevant fields in `mibee-nvr.yaml`:
```yaml
server:
  listen: ":9091"        # ← change here
  tls_listen: ""         # optional: second HTTPS listener, e.g. ":9443"
```

> A **restart is mandatory** after changing — no hot reload.

### 2.5 Improvement backlog (closing the port-conflict UX pain)

Ranked by ROI:

1. ✅ **Interactive port entry in `install.sh`** (#268, done): `install.sh --port 9091` or TTY prompt `Listen port [9090]:`; pipe mode defaults without blocking.

2. ✅ **Compose env-driven port** (#269, done): the binary reads `NVR_LISTEN_PORT` at startup and overrides `server.listen` (env wins over the config file — 12-factor). NAS users change one compose variable instead of editing YAML.

3. ✅ **Web UI port setting + "restart required" hint** (#270, done): the Settings page gained a `server.listen` field with a restart-required notice on save.

---

## Appendix: related docs

| Topic | Doc |
|---|---|
| Docker manual + Watchtower auto-upgrade | `deployment-autoupdate.md` |
| Per-NAS deployment guides | `deployment-{synology,qnap,unraid,fnos,istoreos,zspace}.md` |
| Full configuration reference (incl. `server.listen`) | `configuration.md` |
| Reverse proxy (Caddy/Nginx as the port entry) | `deployment.md` |
