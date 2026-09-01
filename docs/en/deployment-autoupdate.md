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
