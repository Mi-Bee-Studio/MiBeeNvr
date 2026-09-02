# Auto-update (Docker)

> How to keep the MiBee NVR Docker image up to date. The in-app version check (the **sensing** layer, [Settings → About](../en/configuration.md)) tells you a new version exists; this guide covers the **execution** layer — actually replacing the image. Choose manual (recommended for recording-critical setups) or optional automatic updates via Watchtower.

## Two layers, kept separate by design

| Layer | What it does | How |
|-------|--------------|-----|
| **Sensing** (built in) | Polls GitHub Releases, shows a badge + About panel | `GET /api/update/check`; no action taken |
| **Execution** (this guide) | Pulls a new image and recreates the container | Manual `docker compose`, or Watchtower |

They are deliberately separate: a NAS box holds recordings and config that must not be silently changed, so the app never upgrades itself (Docker containers are immutable anyway). The user always authorizes the change — directly, or by opting into Watchtower.

## Manual update (recommended)

The safest path for anything recording video:

```bash
cd <the-dir-with-your-compose-file>
docker compose pull          # pulls the new image
docker compose up -d         # recreates the container with the new image
```

Data under `./data` (recordings, SQLite DB, config, AI models) is untouched — recreation only swaps the image.

### Rollback

Pin the image tag to a specific release to make rollback trivial. The `mibeenvr` image publishes `{version}`, `{major}.{minor}`, `{major}`, and `latest` for every release. Set a fixed tag in your compose, e.g.:

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:0.12.0   # pinned, reproducible
```

To roll back, change the tag to the previous release and `docker compose up -d`. Avoid combining `:latest` with automatic cleanup if you want a rollback image to remain on the host.

## Automatic updates via Watchtower (optional)

[Watchtower](https://github.com/containrrr/watchtower) watches running containers and recreates them when their registry image updates. The bundled profile is **off by default** — you opt in explicitly.

### Enable

```bash
# 1. One-time: authenticate to ghcr.io (Watchtower needs this to pull).
#    Use a GitHub Personal Access Token with read:packages scope.
docker login ghcr.io -u <your-github-user> --password-stdin <<< "<your-PAT>"

# 2. Start with the auto-update profile.
docker compose --profile auto-update up -d
```

Watchtower now checks daily at 04:00 (host time) and recreates **only** the `mibee-nvr` container when a new image exists.

### Why it's safe the way it's configured

- **Scoped to MiBee NVR only.** `WATCHTOWER_LABEL_ENABLE=true` + the `com.centurylinklabs.watchtower.enable=true` label on the NVR service mean Watchtower ignores every other container on the host.
- **Old images are kept.** `WATCHTOWER_CLEANUP=false` leaves the previous image on disk so you can roll back. Do not enable cleanup if you want rollback.
- **Off-peak schedule.** `WATCHTOWER_SCHEDULE=0 0 4 * * *` (6-field cron) runs at 04:00 to avoid clashing with active recording.
- **Data is never touched.** Only the image is replaced; `./data` persists.

### What Watchtower does NOT do

- **No health-gated rollback.** If the new image starts but is unhealthy, Watchtower will not revert automatically. The NVR ships a healthcheck (`mibee-nvr health`) that the container runtime honors for status reporting, but automatic rollback requires manual action: change the image tag back to the last known-good release and `docker compose up -d`. Pair this with the in-app version badge so you notice a bad upgrade.
- **No configuration migration.** The app handles its own DB/config migrations on startup.

### Disable

```bash
docker compose --profile auto-update stop watchtower
# or remove the profile usage entirely and run `docker compose up -d` (default, no Watchtower).
```

## Notifications

Watchtower supports [Shoutrrr](https://containrrr.dev/shoutrrr/) notification URLs. Uncomment `WATCHTOWER_NOTIFICATION_URL` in the compose file and set a Discord / Telegram / Slack / webhook URL to get a message whenever it updates (or, in monitor-only mode, whenever it notices a new image).

## Per-platform notes

| Platform | Best update path |
|----------|------------------|
| unRAID | [CA Application Auto Update](https://forums.unraid.net/topic/51959-plugin-ca-application-auto-update/) plugin (native), or Watchtower |
| iStoreOS / OpenWrt | `docker compose pull && up -d`, or Watchtower |
| 飞牛 fnOS | `.fpk` app lifecycle (`upgrade_init` / `upgrade_callback`) — native, preferred on fnOS |
| Synology DSM | Container Manager: rebuild from new image, or Watchtower |
| QNAP QTS | Container Station: rebuild, or Watchtower |

See the per-platform deployment docs for specifics.

## Verifying bare-metal release artifacts (#646)

Every GitHub Release ships `checksums.txt` (SHA-256 over the bare binaries, `sha256sum` format) and `checksums.txt.sig` (a 64-byte raw ed25519 signature over checksums.txt). The signing private key lives in the repository secret; the public key is embedded in the binary (`internal/update/verify.go`) and distributed as PEM in the repo (`deploy/keys/release-signing.pub.pem`). Until the bare-metal auto-updater (#647) lands, verify manually:

```bash
# 1. Integrity: the downloaded mibee-nvr-<arch> matches checksums.txt
sha256sum -c checksums.txt --ignore-missing

# 2. (Optional) Provenance: prove checksums.txt was produced by this project
curl -fsSL -o release-signing.pub.pem \
  https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/deploy/keys/release-signing.pub.pem
openssl pkeyutl -verify -pubin -inkey release-signing.pub.pem \
  -rawin -in checksums.txt -sigfile checksums.txt.sig
```

Notes:

- `checksums.txt` covers the three bare binaries (amd64/arm64/armv7). The fnOS `.fpk` is distributed through the fnOS store channel; Docker images are integrity-protected by registry digests.
- If a Release has no `checksums.txt.sig`, the signing secret was not configured (that release is unsigned) — step 1 alone still checks integrity.
- A key rotation means newer Releases are signed with a new key; always take the public key from the repo's main branch.

## Bare-metal auto-update (#647, systemd installs)

Outside Docker, bare-metal installs (via install.sh) are the last online scenario that can be automated. The architecture deliberately avoids in-process self-replacement — the `mibee-nvr.service` sandbox (`User=nvr` + `ProtectSystem=strict`) already forbids the app from writing `/usr/local/bin`:

```text
app (nvr user, sandboxed)              root helper (mibee-nvr-update.service)
  sensing layer sees a new release
  AND update.auto_apply: true
  ├─ write /var/lib/mibee-nvr/update-request.json
  └─ systemctl start mibee-nvr-update.service  →  mibee-nvr update --apply-request …
                                                    ① prechecks (non-docker, strict semver step, disk ≥ 3× artifact)
                                                    ② stream-download checksums + ed25519 sig + binary
                                                    ③ sha256 + signature verify (any failure aborts, zero changes)
                                                    ④ keep .prev → atomic binary replace
                                                    ⑤ systemctl restart mibee-nvr
                                                    ⑥ health gate: /api/health readiness probe
                                                    ⑦ automatic .prev rollback + restart on failure
                                                    ⑧ append to update-history.jsonl
```

A polkit rule (`/etc/polkit-1/rules.d/60-mibee-nvr-update.rules`, installed by the installer) authorizes the nvr user to start exactly this ONE unit — minimal privilege surface.

### Enable

```yaml
update:
  auto_apply: true   # default false: announce only, never execute
```

Permanently disabled conditions (even when enabled): `dev` builds, non-stable channels, candidate ≤ current (never downgrades), Docker deployments.

### Manual upgrade (no polkit needed)

```bash
sudo mibee-nvr update              # upgrade to latest stable
sudo mibee-nvr update --version v0.12.1
mibee-nvr update --check           # status only, changes nothing
```

### Rollback

The previous version is kept as `<binary>.prev`; the health gate rolls back automatically on failure. Manual rollback:

```bash
sudo systemctl stop mibee-nvr
sudo cp /usr/local/bin/mibee-nvr.prev /usr/local/bin/mibee-nvr
sudo systemctl start mibee-nvr
```

Upgrade history lives in `<data-dir>/update-history.jsonl` (time, from/to, result, failure reason).

### Scope

Only install.sh/systemd bare-metal installs. Docker belongs to Watchtower; fnOS/unRAID store installs belong to the platform's upgrade mechanism; offline environments should download manually (see the verification section above). On distros without polkit the automatic trigger is unavailable, but the `sudo mibee-nvr update` manual path still works.
