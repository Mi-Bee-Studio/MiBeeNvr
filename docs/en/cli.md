# CLI Reference

> For MiBeeNvr v0.11.0 · command name `mibee-nvr` (prebuilt bundles may carry an arch suffix, e.g. `mibee-nvr-amd64`)

MiBee NVR follows a "single binary + subcommands" design: **running it without a subcommand starts the server**, while subcommands run an administrative tool and exit.

```bash
mibee-nvr              # start the NVR server (long-running)
mibee-nvr <subcommand> # run an admin tool, then exit
mibee-nvr -version     # print version
```

## Starting the Server

```bash
mibee-nvr -config mibee-nvr.yaml
```

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `mibee-nvr.yaml` | Config file path |
| `-version` | — | Print version and exit |

> When started without a config file, the server **auto-initializes** and the browser opens the [setup wizard](wizard.md).

## Subcommand Overview

| Subcommand | Purpose |
|------------|---------|
| [`init`](#init-generate-config) | Generate a config file and admin account |
| [`hash-password`](#hash-password-generate-a-hash) | Generate a password hash |
| [`health`](#health-health-check) | HTTP health probe (for Docker HEALTHCHECK) |
| [`encrypt-config`](#encrypt-config-encrypt-sensitive-fields) | Encrypt plaintext secrets in the config |
| [`download-model`](#download-model-download-the-ai-model) | Download the browser-side AI model |
| [`merge-cameras`](#merge-cameras-merge-cameras) | Merge two duplicate camera entries |
| [`repair`](#repair-data-repair) | Data repair toolkit (7 subcommands) |
| [`cleanup`](#cleanup-recording-cleanup) | Delete recordings by date / orphan files |

---

## init — Generate Config

Generate a config file and set up the admin account:

```bash
mibee-nvr init --password your-password
```

| Flag | Default | Description |
|------|---------|-------------|
| `--password` | (interactive prompt) | Admin password, **at least 8 characters**; prompted in the terminal when omitted |
| `--username` | `admin` | Admin username |
| `--data-dir` | `/var/lib/mibee-nvr` | Data directory (recordings + database) |
| `--listen` | `:9090` | HTTP listen address |
| `--config` | `mibee-nvr.yaml` | Config output path |
| `--force` | — | Overwrite an existing config file (otherwise exits with an error) |

Generated defaults: `30s` segments, 30-day retention, FTP on 2121, WebDAV at `/dav`.

> You can skip `init` entirely: start without a config and complete the [setup wizard](wizard.md) in the browser.

## hash-password — Generate a Hash

```bash
mibee-nvr hash-password 'your-password'
# output: $2a$10$...
```

Paste the output into the `auth.password_hash` field of the config — handy for scripted deployments.

## health — Health Check

Probes the local server over HTTP (`GET /api/health`); exits 0 on success and 1 on failure — built for Docker `HEALTHCHECK` / systemd watchdogs:

```bash
mibee-nvr health                 # probe :9090
mibee-nvr health --addr :9191    # explicit address
mibee-nvr health --config /data/mibee-nvr.yaml   # read server.listen from config
```

Address resolution order: `--addr` > `server.listen` from `--config` > Docker auto-detection (reads the config inside the `NVR_DATA_DIR` data dir) > default `:9090`. **In host-network mode with a changed port you don't need any flag** — the command finds the real port automatically.

## encrypt-config — Encrypt Sensitive Fields

Encrypts **plaintext sensitive fields** (camera passwords, etc.) in place:

```bash
mibee-nvr encrypt-config --config mibee-nvr.yaml
```

Prints which fields were encrypted; already-encrypted or empty fields are skipped. The server reads the config as usual afterwards, but the plaintext is no longer human-readable.

## download-model — Download the AI Model

Downloads the ONNX model needed for browser-side AI detection (YOLOv11-nano, ~5.4 MB, from the official Ultralytics release) into the web static directory:

```bash
mibee-nvr download-model --config mibee-nvr.yaml
```

Includes 5 exponential-backoff retries plus size/integrity checks — useful for pre-downloading in offline environments and shipping it with the package. The model powers [browser-side AI detection](ai-detection.md).

## merge-cameras — Merge Cameras

**End-to-end merge** of two duplicate camera entries (e.g. the same device added once via ONVIF discovery and once via Xiaomi):

```bash
# preview first (dry-run by default)
mibee-nvr merge-cameras --source cam-old --target cam-new

# execute after reviewing
mibee-nvr merge-cameras --source cam-old --target cam-new --execute
```

Steps performed: back up the database → re-tag recordings/events and rewrite file paths to the target → move recording files → drop the source camera from the config → delete the source camera row. **Any failure rolls everything back automatically.**

| Flag | Description |
|------|-------------|
| `--source <id>` | Source camera ID (data moved away; deleted afterwards) |
| `--target <id>` | Target camera ID (data merged into; kept) |
| `--execute` | Actually perform the merge (default is dry-run preview) |
| `--force` | Proceed even if orphan records exist |
| `--config <path>` | Config file path (default `mibee-nvr.yaml`) |

## repair — Data Repair

A set of repair tools for runtime data issues. They **touch the database directly**. Prefer running with the server stopped (running is also safe — WAL mode allows concurrent readers — but stop for large repairs).

```bash
mibee-nvr repair <subcommand> [--dry-run | --execute] [--config mibee-nvr.yaml]
```

Every subcommand **defaults to dry-run** (reports what would change); add `--execute` to apply.

| Subcommand | Purpose |
|------------|---------|
| `duration` | Fix recordings stuck at duration=0 by re-probing the video files (`--prune` also deletes unrepairable rows) |
| `merge-status` | Reset "merged" flags when the merged output file has gone missing |
| `fragments` | Clean up segments the merge engine gave up on (incompatible/failed) |
| `delete-by-format` | Bulk-delete one camera's recordings of all formats except one (e.g. keep only timelapse segments) |
| `prune-intermediate-mp4` | Remove per-segment rolling-merge .mp4 outputs already folded into periodic (8h/24h/7d/30d) merges |
| `reclaim-orphan-merges` | Reclaim merged .mp4 files left on disk after their recording row was deleted via the web UI (touches only unreferenced outputs) |
| `normalize-endpoints` | Canonicalize ONVIF endpoints (elide default ports, lowercase, strip trailing slash) so dedup queries match |

Examples:

```bash
# preview how many duration=0 recordings would be fixed
mibee-nvr repair duration

# execute, deleting unrecoverable files as well
mibee-nvr repair duration --execute --prune
```

## cleanup — Recording Cleanup

Manual cleanup that bypasses the retention policy — **deletes files, DB rows, and orphaned AI events together**:

```bash
# preview deleting recordings before a date
mibee-nvr cleanup --before 2026-08-01 --dry-run

# execute
mibee-nvr cleanup --before 2026-08-01

# clean orphan files (video files on disk with no DB record)
mibee-nvr cleanup --orphans --dry-run
mibee-nvr cleanup --orphans
```

| Flag | Description |
|------|-------------|
| `--before YYYY-MM-DD` | Delete recordings before this date (files + DB rows + AI events) |
| `--orphans` | Scan the disk and delete video files absent from the DB (.mp4/.mkv/.avi/.dav/.flv) |
| `--dry-run` | Report only, delete nothing (strongly recommended first) |
| `--config <path>` | Config file path (default `mibee-nvr.yaml`; locates the storage root and database) |

> For day-to-day cleanup prefer the [retention policy](recording-playback.md) (`cleanup.retention_days`); this command is for post-migration slimming and incident cleanup.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `NVR_PASSWORD` | Set the admin password on first start (API returns 503 until a password exists) |
| `NVR_LISTEN_PORT` | Override the listen port |
| `NVR_DATA_DIR` | Docker data directory (used by `health` auto-detection) |
| `NVR_UID` / `NVR_GID` | In-container run user (align with host directory ownership) |

## Next Steps

- [Configuration Reference](config.md) — top-level YAML key cheat sheet
- [Setup Wizard](wizard.md) — first-run configuration in the browser
- [Upgrade Guide](upgrade-faq.md) — upgrades and data migration
