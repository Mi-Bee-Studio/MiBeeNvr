# CLI Reference

> For MiBeeNvr v0.13.0 · command name `mibee-nvr` (prebuilt bundles may carry an arch suffix, e.g. `mibee-nvr-amd64`)

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
| [`validate-config`](#validate-config-validate-a-config-file) | Pre-deploy config check (exit 0 = would boot) |
| [`download-model`](#download-model-download-the-ai-model) | Download the browser-side AI model |
| [`merge-cameras`](#merge-cameras-merge-cameras) | Merge two duplicate camera entries |
| [`timelapse-merge`](#timelapse-merge-convert-recordings-to-timelapse) | Batch-convert recordings of any period/camera into timelapse merges |
| [`eval-replay`](#eval-replay-offline-replay-evaluation) | Replay the activity scorer / adaptive gate offline for tuning |
| [`repair`](#repair-data-repair) | Data repair toolkit (9 subcommands) |
| [`cleanup`](#cleanup-recording-cleanup) | Delete recordings by date / orphan files |
| [`offload`](#offload-object-storage-offload-queue) | S3 offload queue counters and local eviction |
| [`update`](#update-version-check-and-upgrade) | Version check and bare-metal upgrade execution |
| [`gen-gb35114-certs`](#gen-gb35114-certs-issue-gb35114-pilot-certificates) | Issue GB35114 level-A pilot certificates (`-tags gb35114` builds only) |

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

## validate-config — Validate a Config File

**Pre-deploy smoke check** for hand-edited YAML: runs the exact same Load → Validate pipeline the server uses at boot, without starting anything. **Exit code 0 = the NVR would boot on this config**, 1 = it would crash-loop under systemd:

```bash
mibee-nvr validate-config
mibee-nvr validate-config --config /data/mibee-nvr.yaml
# OK /data/mibee-nvr.yaml — the NVR would boot on this config
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config <path>` | `mibee-nvr.yaml` | Config file to validate (same default path a bare `mibee-nvr` start would load) |

Validation covers **illegal values** (ranges, formats, missing required fields) and surfaces **dangling references** at load time — the 2026-09-11 M5 incident was exactly that (a hand-edited YAML dropped a vision instance a camera still referenced, and the service restart-looped for ~2 minutes). Run it after every manual edit, before restarting the service.

## download-model — Download the AI Model

Downloads the ONNX model needed for browser-side AI detection (YOLOv11-nano, ~5.4 MB, from the official Ultralytics release) into the web static directory:

```bash
mibee-nvr download-model --config mibee-nvr.yaml
```

Includes 5 exponential-backoff retries plus size/integrity checks — useful for pre-downloading in offline environments and shipping it with the package. The model powers [browser-side AI detection](ai-detection-tuning.md).

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

## timelapse-merge — Convert Recordings to Timelapse

Batch-converts existing video recordings (H264/H265/AVI/MJPEG) of **any camera over any date range** into periodic timelapse merges — the CLI counterpart of `POST /api/timelapse/{id}/merge`, executed in-process against the storage DB. Sampling interval, output fps and source deletion are **per-run overrides that never mutate the camera config**:

```bash
# Preview: all JPEG cameras, everything since 2026-08-26
mibee-nvr timelapse-merge --camera all --encoding jpeg --start 2026-08-26

# Execute: 1-second sampling + delete sources after a successful merge
mibee-nvr timelapse-merge --camera all --encoding jpeg --start 2026-08-26 \
  --interval 1s --delete-sources --execute
```

Behavior notes:

- Windows (default `natural-day`) are enumerated across the date range and merged one by one; windows with an existing completed merge are skipped (safe to re-run and resume), open windows are skipped.
- `--delete-sources` deletes source recordings (DB rows + files) only after the window's merge **succeeded**; recordings being processed by MiBeeVision are always skipped.
- The command may run while the NVR is live (WAL concurrency, same as cleanup/repair); cameras with timelapse enabled are refused though — their windows belong to the server's merge scheduler (use `--force` or stop the server).
- **Self-downgrades on execute** (nice 19 + lowest best-effort IO class; `--no-throttle` disables): merges at the NVR's default priority starve online recording (2026-09-12 incident: load 8-13 for 4.5h, recordings dropped 17→10; renicing after the fact could not undo the storm).
- **Directory-form source deletion is rate-limited** (pause every 200 files, `--delete-throttle`): unlinking the millions of small files in MJPEG/timelapse frame directories saturates the ext4 journal (jbd2) for tens of minutes and slows all disk IO.
- Merge intermediates (frame extract/copy dirs) are written under the **storage root** at `<root>/periodic-merge/tmp/`, never the system `/tmp` (a 1s-sampled natural-day window needs ~3.5-7GB and small root partitions hit ENOSPC); merge output is generated streaming (#747), so memory use is independent of window size. Intermediates left behind by an interrupted run (Ctrl-C/crash) are reclaimed by the NVR server's startup sweep (grace period `storage.periodic_temp_grace_s`, default 24h — see [Configuration](configuration.md#storageperiodic_temp_grace_s)).

| Flag | Default | Description |
|------|---------|-------------|
| `--camera <ids\|all>` | (required) | Comma-separated camera IDs or `all` |
| `--encoding <enc>` | — | With `--camera all`: filter by encoding (e.g. `jpeg`) |
| `--start <YYYY-MM-DD>` | (required) | First window date (config timezone) |
| `--end <YYYY-MM-DD>` | yesterday | Last window date (inclusive) |
| `--duration <label>` | `natural-day` | Window size: `8h` / `12h` / `24h` / `7d` / `30d` … |
| `--interval <dur>` | camera `timelapse.interval`, else 30s | Frame sampling interval (e.g. `1s`) |
| `--fps <n>` | camera `merge_output_fps`, else 10 | Output playback fps |
| `--delete-sources` / `--no-delete-sources` | camera `delete_recordings_after_merge` | Override source deletion for this run |
| `--delete-throttle <dur>` | `200ms` | Pause between chunks when deleting directory-form sources; `0` disables |
| `--execute` | dry-run | Actually execute |
| `--force` | — | Process timelapse-enabled cameras while the NVR is running |
| `--no-throttle` | — | Skip the automatic self-downgrade (nice 19 + IO best-effort) |
| `--config <path>` | `mibee-nvr.yaml` | Config file path |

## eval-replay — Offline Replay Evaluation

Replays the **offline activity scorer** or the **adaptive recording gate** over a golden corpus of finished recordings, printing per-file detail plus per-label aggregates — adaptive recording (`recording_mode: adaptive`) tuning ships with a before/after table instead of a field gamble. See [Adaptive Recording](adaptive-recording.md).

```bash
# scorer replay (default)
mibee-nvr eval-replay --corpus corpus.json

# gate replay (default config)
mibee-nvr eval-replay --corpus corpus.json --gate

# gate replay with a candidate config, side by side
mibee-nvr eval-replay --corpus corpus.json --gate --videoexit=false
```

The corpus is a JSON array; paths may be absolute or relative to the manifest. Labels are free-form (framework convention: `rain` / `lowbitrate` / `static` / `active`):

```json
[{"path": "/mnt/data/nvr/cam-yard/seg.mp4", "camera": "cam-yard", "label": "rain"}]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--corpus <path>` | (required) | Corpus manifest JSON path |
| `--gate` | scorer | Replay the adaptive gate instead of the scorer |
| `--fps <n>` | derived per file | Frame rate for gate replay (0 = frames/duration) |
| `--spike <f>` | — | Candidate gate: `spike_factor` |
| `--noisefloor-bytes <n>` | — | Candidate gate: explicit `noise_floor_bytes` |
| `--autonoise true\|false` | — | Candidate gate: `auto_noise_floor` |
| `--videoexit true\|false` | — | Candidate gate: `video_exit` |

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
| `mjpeg-containerize` | Convert legacy dir-form MJPEG segments (one JPEG file per frame) into single-file AVI containers (#761); per segment "convert → verify → commit → remove source", and a failed verify leaves the row untouched |
| `timelapse-mjpeg` | Rewrite MJPEG periodic-merge outputs whose samples carry double-header JPEGs (non-compliant RTSP senders pack complete JPEGs into RFC 2435 payloads and the depacketizer prepends a synthesized header block, which browsers reject): the complete inner JPEG of every sample is salvaged losslessly and remuxed, and the DB row's frame count / file size refreshed; outputs with under 25% salvageable frames (source truncated at stream EOF) are skipped and left to the operator |

Examples:

```bash
# preview how many duration=0 recordings would be fixed
mibee-nvr repair duration

# execute, deleting unrecoverable files as well
mibee-nvr repair duration --execute --prune
```

`mjpeg-containerize` specific flags:

| Flag | Default | Purpose |
|------|---------|---------|
| `--camera <id>` | all | Convert only this camera's segments |
| `--limit N` | all | Convert at most N segments |
| `--keep-old` | off | Keep the source frame directory after conversion |
| `--busy-retries N` | `3` | Retry attempts on `SQLITE_BUSY` for the DB row flip (`0` = single attempt, no retry) |
| `--busy-wait <dur>` | `2s` | Linear backoff base between BUSY retries (Go duration, e.g. `500ms`, `5s`) |

The CLI shares the WAL DB with the live NVR — under disk saturation the server's merge transactions can outlast busy_timeout (raise both values then); on fast disks the default 3-attempt worst case is pure waste (lower them). Mirrors the `timelapse-merge --delete-throttle` precedent.

```bash
# preview (dry-run by default)
mibee-nvr repair mjpeg-containerize --camera yard-esp32

# execute; relax BUSY retries under disk saturation
mibee-nvr repair mjpeg-containerize --execute --busy-retries 5 --busy-wait 5s
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

## offload — Object-Storage Offload Queue

Operator surface of [`storage.remote`](storage-offload.md) (S3-compatible object-storage cold backup). Both subcommands require `storage.remote.enabled: true` in the config and exit with an error otherwise:

```bash
# outbox counters per status + backlog (pending+uploading)
mibee-nvr offload status

# evict local copies of confirmed uploads (dry-run report by default)
mibee-nvr offload evict --all-uploaded
mibee-nvr offload evict --camera front-door

# apply
mibee-nvr offload evict --all-uploaded --execute
```

Behavior notes:

- `evict` deletes the **local** file only for recordings whose upload is confirmed (`uploaded`); before `--execute` deletes anything, every item is re-verified with a fresh remote `HeadObject` — a failed check marks the item REFUSED and nothing is deleted for it. **The remote object is never deleted** (remote cleanup belongs to the bucket lifecycle policy).
- The eligibility window defaults to `storage.remote.evict.after_days`; with `after_days: 0` (upload-only mode) you must pass `--all-uploaded` to evict at all.
- Safe to run against a **live** server (WAL concurrent readers); prefer a quiet window for large batches.

| Flag | Description |
|------|-------------|
| `--all-uploaded` | Consider every confirmed upload regardless of `evict.after_days` |
| `--camera <id>` | Restrict to one camera |
| `--execute` | Actually delete local files (default is a dry-run report) |
| `--config <path>` | Config file path (default `mibee-nvr.yaml`) |

For the full pipeline (outbox state machine, upload grace, backlog limit) and configuration see [Object-Storage Offload](storage-offload.md).

## update — Version Check and Upgrade

The **upgrade execution layer** for bare-metal (systemd) deployments (#647), working with the `mibee-nvr-update.service` root helper (polkit-authorized). `--check` is sensing-only; upgrades must run as root:

```bash
# sensing only: show current/latest/deployment, change nothing
mibee-nvr update --check

# manual upgrade to the latest stable (requires sudo)
sudo mibee-nvr update
sudo mibee-nvr update --version v0.13.0

# root-helper entry (ExecStart of the update service; the request file is
# written by the app and consumed exactly once — removed on ANY outcome)
mibee-nvr update --apply-request /var/lib/mibee-nvr/update-request.json
```

| Flag | Default | Description |
|------|---------|-------------|
| `--check` | — | Print current / latest / available / deployment only |
| `--version <tag>` | latest stable | Target release tag |
| `--apply-request <file>` | — | Consume the app-written upgrade request file (helper entry) |
| `--config <path>` | `mibee-nvr.yaml` | Config file path |

- Artifacts are verified with sha256 + ed25519; after the swap the service passes a **health gate** (local health probe against the configured `server.listen`) and rolls back to the previous binary automatically on failure.
- **Linux bare-metal only**: permanently disabled for Docker deployments (the container is immutable — use Watchtower / `docker compose pull`); Windows/macOS desktop builds are refused too (a self-update would silently resolve to the linux binary).
- `update.download_mirror` switches artifact downloads to a mirror (the version check still hits the GitHub API).

For the auto-apply switch (`update.auto_apply`) and the service/polkit setup see [Automatic Updates](deployment-autoupdate.md).

## gen-gb35114-certs — Issue GB35114 Pilot Certificates

Only present in `-tags gb35114` builds (a default build explains the rebuild instead). Issues self-signed pilot material for GB35114 level-A secure registration: an SM2 platform identity plus device identities signed by the platform, laid out to match the three path keys of `gb28181.security35114`. See the [GB28181 guide — GB35114 Security](gb28181-guide.md).

```bash
mibee-nvr gen-gb35114-certs --platform-id 34020000002000000001 \
  --device-id 34020000001320000001,34020000001320000002 \
  --out-dir gb35114-certs [--days 3650]
```

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
- [Upgrade Guide](upgrade-guide.md) — upgrades and data migration
