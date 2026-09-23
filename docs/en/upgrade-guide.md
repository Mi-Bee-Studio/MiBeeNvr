# Upgrade Guide

This guide covers upgrade paths and breaking changes between MiBee NVR releases. Always back up your database and config before upgrading.

## Quick path table

| From → To | Status | Action required |
|-----------|--------|-----------------|
| **0.12.x → 0.13.0** | 🟡 **Read: security tightening & default changes** | Several endpoints moved behind auth; `?token=` passthrough removed; DB schema v40 (no downgrade); HLS/MJPEG/merge default changes. See [0.12.x → 0.13.0](#012x--0130). |
| **0.11.x → 0.12.0** | 🟢 **transparent (one caveat)** | No breaking API changes; automatic incremental DB migration; GB28181 default media transport switched to TCP-passive. See [0.11.x → 0.12.0](#011x--0120). |
| **0.10.x → 0.11.0** | 🟡 **Read the license + API changes** | License change (AGPL-3.0); one API response field rename; plain-HTTP AAC live-audio degradation. See [0.10.x → 0.11.0](#010x--0110). |
| **v0.9.1 → 0.10.0** | 🟡 **Action required** | Fix combined `protocol` strings; optional disk-space reclaim. See [v0.9.1 → v0.10.0](#v091--v0100). |
| v0.9.0 → v0.9.1 | 🟢 Transparent | None. |
| v0.8.x → v0.9.x | 🟡 Back up first | Large storage-layer refactor. Back up the DB, then upgrade. |
| v0.8.x → 0.10.0 | 🟡 Two hops | Upgrade to v0.9.x first, then to 0.10.0. |
| **< v0.9.x → 0.10.0** | 🔴 **Not supported (direct)** | You **must** upgrade to 0.9.x first — see [below](#below-v09x--v0100-not-supported-direct) for why. |

---

## 0.12.x → 0.13.0

0.13.0 is a major release: **object-storage offload** (async upload to S3-compatible cold backup), **tiered recording + pixel-domain gating** (an end-to-end pipeline for smart-encoder cameras), **Windows / macOS desktop editions**, **bare-metal auto-update** (signature verification + automatic rollback on failure), **I/O budget scheduling**, and the **end of recording write stalls**. The incremental DB migration runs automatically on first boot, but the behavior changes below need your attention:

### 🔴 Security tightening (#879)

- **Download and playback endpoints moved into the authenticated route group**: `/api/recordings/{id}/download`, `/api/recordings/{id}/merged`, the delayed-merge download, playback segments (`/api/cameras/{id}/playback/*`), `/api/events`, and `/api/health/cameras` were anonymously accessible in v0.12.x; they now require the same credentials as every other API (Basic Auth / API Key / session cookie / Bearer token all work). **Integrations pulling these endpoints anonymously from scripts or third-party tools need to add credentials**
- **The legacy base64 `?token=` passthrough is removed** — use an `Authorization: Bearer` session token, a URL-signed token, or an API Key instead
- `local_bypass` now rejects cross-site requests (browser requests with `Sec-Fetch-Site: cross-site` no longer skip auth); the CSP `script-src` is tightened to inline hashes — only affects heavily modified deployments that inject custom scripts into the page
- **Credential-free first start**: when starting without a password set, the terminal prints a one-time setup code; the first-run wizard asks for it to complete initialization (loopback access is exempt)

### 🔴 Database schema v33 → v40 (no downgrade)

The incremental migration runs automatically on first start: camera groups (v36–v38), `motion_confidence` (v34), AI event sources (v35), tiered-recording data backfill (v39), `offload_outbox` (v40). **Once migrated there is no going back to the v0.12.x binary** — back up `mibee-nvr.db` and your config file before upgrading.

### 🟡 Storage & merge default changes

- **HLS `low_latency` now defaults to on**: an unset key means low-latency HLS (in practice deployments have been running in LL mode since the feature landed — the key was dead until now); set `low_latency: false` explicitly for the classic segmented playlist
- **MJPEG recording segments now default to a single-file AVI container** (eliminates the per-frame-file metadata churn on SD/eMMC): existing directory-form segments keep playing; the per-camera `http_jpeg_avi` key is deprecated (old configs don't error), the new key `mjpeg_form: dir` opts back into directories explicitly; existing segments can be migrated in one pass with `mibee-nvr repair mjpeg-containerize`
- **Batch folding of flash-disconnect fragments is on by default** (`merge.rolling_fragment_hold_s` unset = 300s): fragments from flapping cameras are folded in batches instead of rewriting a whole bucket per fragment — a significant drop in IO load is expected; set `0` explicitly to turn it off
- `merge.rolling_bucket_retain` defaults to `2`: cameras whose picture quality swings (e.g. Xiaomi HD/SD switching) now keep one bucket per quality, slightly increasing disk usage; set `1` explicitly to restore the old single-bucket behavior
- `merge.transcode_grace` defaults to `90s`: fresh segments from cameras with transcode jobs are held back from folding for up to 90 seconds (fixes a transcode/merge race; set `"0s"` to fold immediately)
- **`cleanup` validation tightened**: `disk_threshold_percent` valid range is 50–99 and `retention_days` 1–3650, and the web settings page now validates with the same rules as startup loading — out-of-range values the v0.12.x settings page accepted (e.g. 20%) are rejected after the upgrade. Also adds a last-good-config backup restored at startup (this is the fix for the v0.12 crash-loop bug)

### 🟡 Other behavior changes

- `POST /api/auth/password` no longer requires the old password — loopback requests (the companion for the desktop menubar/tray) can change the password directly; remote requests still need a logged-in session
- Third-party-library stdlib logs are throttled to one line per 10 seconds by default (`observability.stdlog_throttle`), fixing journald getting flooded; set `off` to disable
- FTP falls back to the admin account when no dedicated credentials are configured, and prints a startup warning (for a plaintext protocol, prefer separate low-privilege credentials `ftp.username` / `ftp.password`)
- The desktop editions (Windows/macOS) listen on `127.0.0.1` only, with passwordless local login, and are not exposed to the LAN by default; opening up to the LAN requires explicitly changing the listen address in the tray/menubar

### 🟢 New features at a glance

- **Object-storage offload**: merged recordings upload asynchronously to S3-compatible storage (AWS S3 / MinIO / Cloudflare R2 / B2 / OSS / COS); after the upload verifies, the local copy can be evicted safely — see [Object-Storage Offload](storage-offload.md)
- **Tiered recording + pixel-domain gating**: smart-encoder cameras (static ~0.5fps / 5MP VBR) get a 24/7 continuous sub-stream baseline plus on-demand main-stream, with fine gating by classic CV sampled at ~1fps — no camera-side cooperation required — see [Adaptive Recording](adaptive-recording.md)
- **Windows / macOS desktop editions**: single-file installers (Setup.exe / DMG), system tray and macOS menubar, passwordless local access — see [Desktop Edition](desktop.md)
- **Bare-metal auto-update**: ed25519 signature verification of release artifacts, automatic rollback when a health gate fails, one-click upgrade and upgrade history on the web settings page (`update.auto_apply` off by default)
- **I/O budget scheduling**: background tasks (merge / cleanup / repair / timelapse / transcode / offload) share a token bucket instead of competing with the foreground for disk IO on equal terms; the two rollout switches `io.recording_writes_budgeted` / `io.playback_reads_budgeted` are off by default — see [Performance Tuning](performance.md)
- **Recording write stalls eliminated**: MP4 segments flush to disk incrementally (no more whole-file burst write at Close); each segment takes its own write lock, so cameras no longer serialize each other's disk writes
- **Memory auto-sizing**: GOMEMLIMIT is set automatically from physical RAM / cgroup quota, so small-memory boards stop OOMing (`memory:` config block)
- **Global recording default switch** `recording.default_enabled`: turn recording off in one place for view-only deployments
- **Camera groups**: drag-and-drop grouping / ordering / renaming in the web UI (`/api/cameras/groups`)
- **RTSP output supports MJPEG/JPEG** (RTP-JPEG); latest-frame snapshots for H.264/H.265 (optional FFmpeg) and MQTT snapshot triggering
- **HMAC-signed webhook trigger endpoint**: `POST /api/trigger/webhook/{camera_id}`, Stripe-style signatures + two-way replay protection — see [Webhook Trigger Integration](webhook-integration.md)
- **MQTT event-bus forwarding** (`mqtt.status_events`) and scheduled forced-recording windows (`{"action":"record","duration":"60s"}`)
- **ONVIF camera-side motion detection trigger** (`motion_source: camera:onvif`) — see [ONVIF Guide](onvif-guide.md)
- **Home Assistant custom integration ships with releases** (installable via HACS, mDNS auto-discovery) — see [Home Assistant Integration](home-assistant.md)
- **Vision multi-instance routing** + push circuit breaker with automatic backoff; an AI event snapshot endpoint
- **Timelapse MJPEG dual-mode GA** + `timelapse-merge` CLI for batch conversion + merged-history playback — see [Timelapse Recording](timelapse.md)
- **GB35114 level-A secure registration pilot** (SM2 certificates; requires a `-tags gb35114` build) and on-demand main-stream activation for GB28181 cascade
- **`validate-config` CLI**, deep orphan-directory scanning, and startup config sanity guards (a crash-loop fuse)

Full change list in the v0.13.0 notes on [GitHub Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases).

---

## 0.11.x → 0.12.0

v0.12.0 brings the **sub-stream system** (on-demand pull + quality switching on every protocol + RTSP output), **adaptive recording** (motion-aware + audio trigger + activity scores), **per-camera storage with hot migration**, and **end-to-end stream observability** (flow tree / latency badge / frame trace). For existing deployments the upgrade is nearly transparent:

- **No breaking API changes**; the `recordings` table gains `motion_score` / `activity_flags` / `timeline_map` columns — **migrated automatically on first boot**, no manual steps
- ⚠️ **GB28181 default media transport is now TCP-passive** — measured ~16% frame loss over UDP. Deployments that explicitly configured `media_transport` in YAML are unaffected; unconfigured GB cameras switch to TCP pull on upgrade (supported by mainstream Hikvision/Dahua devices)
- Building the frontend **from source** now needs Node 24; users of release artifacts are unaffected
- New features are opt-in per camera (e.g. `recording_mode: adaptive`); leaving them off keeps 0.11.x behavior

Full change list in the v0.12.0 notes on [GitHub Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases).

---

## 0.10.x → 0.11.0

0.11.0 adds GB/T 28181 national-standard platform access (opt-in), continuous recording playback with a full-day timeline, and changes the project license. The upgrade itself is transparent for the database — no schema migration is required.

### ⚖️ License change (please read)

Starting with v0.11.0, MiBee NVR is licensed under **AGPL-3.0-only** (previously MIT; releases up to v0.10.1 remain MIT-licensed). In plain terms:

- **Just using MiBee NVR** (running it, recording cameras, watching streams — including commercially): no obligations, nothing changes for you.
- **Redistributing a modified version**: your modified version must be released under AGPL-3.0.
- **Building your own program on the `pkg/` extension interfaces**: covered by a [linking exception](../../LICENSE.pkg-linking-exception) — your program's license stays your choice.
- **A separate program talking to a running NVR** over its HTTP/WebSocket APIs: never affected by the license.

See [LICENSE](../../LICENSE), [NOTICE](../../NOTICE), and [CONTRIBUTING.md](../../CONTRIBUTING.md) for details.

### 🔴 Breaking: `GET /api/cameras/{id}/protocols` field names

JSON field names in the entries are now snake_case, consistent with every other endpoint (#332). If you have scripts consuming this endpoint, update them:

| 0.10.x | 0.11.0 |
|--------|--------|
| `Protocol` | `protocol` |
| `Available` | `available` |
| `Reason` | `reason` |

### 🟡 Plain-HTTP AAC live-audio degradation

The FAAD2 (GPL-2.0) WASM fallback for AAC live-preview audio was removed for license compatibility (#319). AAC live audio now requires WebCodecs (HTTPS or localhost). On plain-HTTP LAN deployments AAC live preview degrades with a hint — the same behavior Opus already had. **Not affected**: G.711 live audio (works everywhere), and AAC audio in recordings/playback (decoded natively by the browser's MP4 pipeline).

### 🟢 GB/T 28181 platform access (opt-in, off by default)

v0.11.0 adds a GB/T 28181 platform role: SIP-capable cameras (Hikvision, Dahua, Uniview, …) register with the NVR and appear as normal cameras — with PTZ, two-way voice talk, device-side recording retrieval, alarm/catalog/mobile-position subscriptions, and cascade to an upper platform. Everything is **disabled by default**; nothing changes unless you turn it on. See the [GB/T 28181 guide](gb28181-guide.md) for setup.

New config sections: `gb28181:` (platform role) and `gb28181_cascade:` (lower-platform role). New DB tables (`gb28181_devices`, `gb28181_channels`, `camera_ptz_presets`, `cascade_channels`, `gb28181_fingerprints`) are created with `CREATE TABLE IF NOT EXISTS` on first start — no migration.

### 🟢 LAN identity & discovery

- `server.device_id`: a stable UUID is generated on first start and persisted to the config; `GET /api/health` now carries `device_id`/`device_name` so clients can anchor on an identity instead of an IP.
- mDNS/DNS-SD announcement (`_mibee-nvr._tcp`, on by default) and a UDP discovery responder (port 49090, on by default; answers `MIBEE-NVR-DISCv1?` probes for multicast-restricted networks). Both log and continue on bind failure — configurable under `server.discovery.*`.

### 🟢 HLS playback on iOS/AVPlayer

Playlist requests authenticated with a session token now receive a scoped `mbs_session` cookie, fixing 401s on media segments in players that cannot set per-request headers (iOS AVPlayer). No action required.

---

## v0.9.1 → v0.10.0

0.10.0 is a large release (H.265 WASM live playback, Timelapse v3, stateless auth tokens, MJPEG-over-WebSocket, AI model integrity hardening, and a 0.10.0 architecture cleanup). It contains a few **breaking changes** that require a one-time config or post-deploy step.

### 🔴 Step 1 — Split any combined `protocol` strings (REQUIRED, blocks startup)

0.10.0 rejects combined protocol strings like `"rtsp_h264"` at config validation. This is a **hard error** — the binary will refuse to start until you fix it.

**Check your config:**

```bash
grep -nE 'protocol:\s*".*_(h264|h265|mjpeg|jpeg)"' /path/to/mibee-nvr.yaml
```

**If any line matches, split it into separate fields:**

```yaml
# ❌ Before (0.9.x accepted this, 0.10.0 rejects it)
- id: "front-door"
  protocol: "rtsp_h264"
  url: "rtsp://..."

# ✅ After
- id: "front-door"
  protocol: "rtsp"
  encoding: "h264"
  url: "rtsp://..."
```

The exact startup error you'll see if you forget this:

```text
camera[0].protocol "rtsp_h264": combined format is no longer supported in
0.10.0+; split into separate protocol ("rtsp") and encoding fields
```

This applies to every camera whose `protocol` contains an underscore (`rtsp_h264`, `rtsp_h265`, `rtsp_mjpeg`, `http_jpeg`, `onvif_jpeg`, …).

### 🔴 Step 2 — Back up your database (RECOMMENDED)

The migration from schema v28 → v29 **automatically** creates a backup via `VACUUM INTO` at `<db>.pre-v29-backup` before dropping the legacy `recordings.merged` column. A manual backup is a cheap extra insurance:

```bash
cp /var/lib/mibee-nvr/nvr.db /var/lib/mibee-nvr/nvr.db.pre-upgrade
```

The migration:
1. Backs up the DB to `<db>.pre-v29-backup` (best-effort; failures only log a warning).
2. Updates `merge_status='merged'` for any row with the old `merged=1` flag (safety net).
3. Drops the `merged` column (`merge_status` is now the single source of truth).

This is transparent for v0.9.x users. The v0.10.0 DB schema is at baseline **v29**.

### 🟡 Step 3 — Reclaim leaked merge MP4 files (post-deploy, OPTIONAL but recommended)

A bug fixed in 0.10.0 (#117/#119) meant that deleting a recording through the web UI before this fix did **not** delete its merged-output MP4 file on disk. The fix covers future deletes, but **files leaked before the upgrade are still on disk**. Reclaim them with the repair CLI:

```bash
# 1. Dry-run first — see how much space is reclaimable
./mibee-nvr repair reclaim-orphan-merges --dry-run

# 2. Apply (default throttle is 20ms between deletes — USB-HDD friendly)
./mibee-nvr repair reclaim-orphan-merges --execute

# Optional: limit to one camera, cap the count
./mibee-nvr repair reclaim-orphan-merges --execute --camera front-door --limit 1000
```

Only `.mp4` files that no recording row references (neither as `file_path` nor `merge_path`) are removed. Source frame directories and raw segments are never touched. `--dry-run` is the default.

### 🟡 Step 4 — Note changed defaults (affects you only if you left these unset)

| Setting | v0.9.1 default | 0.10.0 default | Notes |
|---------|----------------|----------------|-------|
| `cleanup.disk_threshold_percent` | 95 | **85** | Triggers cleanup earlier, avoiding the HDD performance cliff that starts around 90% full. Only applies if your config leaves it at `0`/unset — an explicit value is preserved. |
| `cameras[].timelapse.merge_duration` | `"1h"` | **`"natural-day"`** | Per-camera timelapse now defaults to a midnight-aligned 24h window (in your configured timezone). The previous 1h hard cap is removed; `8h`/`12h`/`24h`/`7d`/`30d`/`natural-day` and arbitrary durations ≤ 30d are accepted. Note: **rolling-window merge** (`merge.rolling_window`) is still capped at 1h. |

### 🟢 Removed API endpoints (affects external scripts only)

Two timelapse endpoints were removed (the underlying gallery feature was replaced by Timelapse v3 periodic merges):

| Removed (0.10.0 returns 404) | Replacement |
|-------------------------------|-------------|
| `GET /api/timelapse/{id}/thumbnail` | `GET /api/timelapse/merges` + `GET /api/timelapse/merges/{id}` |
| `GET /api/timelapse/{id}/preview` | `GET /api/timelapse/merges/{id}/download` (sets `X-Timelapse-Codec` header so the frontend picks `<video>` vs JPEG cycler) |

If you have external scripts or bookmarks pointing at the old endpoints, migrate them. The NVR's own frontend already uses the new endpoints.

### 🟢 `streaming.default_protocol` field removed (silent — stale YAML keys are ignored)

The global `streaming.default_protocol` config field was removed. The frontend's Player Orchestrator now auto-selects the best protocol per camera (probes `/api/cameras/{id}/protocols`, folds in codec + browser capability, demotes on health failure, upgrades after stability). A global default only added config-cognition overhead.

- **You don't need to do anything.** A stale `default_protocol:` key in your existing YAML is **silently ignored** (YAML decoding does not strict-check unknown fields). No startup error.
- Per-camera overrides remain available via the Protocol Switcher on each camera's LiveView page.
- **Behavior change for H.264 cameras with no per-camera override:** the initial protocol preference is now the latency-optimal order (`webrtc > flv > ll-hls > hls > mjpeg`), instead of a global default (usually `hls`). The orchestrator still adapts at runtime — this only changes the first choice.

### 🟢 New config fields (all backward-compatible, safe defaults)

| Field | Default | Purpose |
|-------|---------|---------|
| `merge.rolling_backfill_concurrency` | `0` (auto: 1 if ≤2GB RAM, else 3) | Bounds concurrent cameras during rolling-merge backfill. |
| `streaming.webrtc.ice_servers` | `[]` (LAN-only) | STUN/TURN servers for cross-network WebRTC. Empty = LAN-only (old behavior). |
| `cameras[].timelapse.retain_intermediate_mp4` | `false` | Keep rolling-merge intermediate MP4 files after a periodic merge folds them in (default: clean up to save disk — ~1.5GB/day/camera). |

### Pre-flight checklist

```bash
# 1. Fix combined protocol strings (REQUIRED — blocks startup otherwise)
grep -nE 'protocol:\s*".*_(h264|h265|mjpeg|jpeg)"' mibee-nvr.yaml
#   → split every match into protocol + encoding

# 2. Back up the DB (cheap insurance on top of the auto v29 backup)
cp /var/lib/mibee-nvr/nvr.db /var/lib/mibee-nvr/nvr.db.pre-upgrade

# 3. Deploy the new binary, start the service, confirm health
curl http://localhost:9090/api/health

# 4. After deploy: reclaim pre-fix leaked merge MP4s (one-time)
./mibee-nvr repair reclaim-orphan-merges --dry-run     # review
./mibee-nvr repair reclaim-orphan-merges --execute     # apply
```

### Notes for self-builders (not release binaries)

If you build the binary yourself rather than using a release artifact:

- **Build the frontend before the Go binary.** The embedded SPA (`internal/ui/static/`) must be regenerated with `cd web && npm run build` (or `make build`, which does this for you). A stale embedded UI bundle may still reference removed endpoints and produce 404s in the browser.
- Release binaries are built with a fresh frontend; this only affects local/custom builds.

---

## Below v0.9.x → v0.10.0: NOT supported (direct)

You **cannot** upgrade directly from below v0.9.x to 0.10.0. There is no startup-time version guard that aborts the process, but you will hit one of these failures instead:

- **DB timestamp parsing fails.** 0.10.0 removed support for legacy Go `time.Time.String()` formats — the monotonic-clock suffix (`m=+...`) and timezone abbreviations (`CST`). Rows containing these formats become zero-time. v0.9.x rewrote these to canonical SQLite timestamps; you must run v0.9.x first to normalize them.
- **DB schema is too old.** The v28→v29 migration assumes the v28 schema (produced by v0.9.x). Earlier schemas lack columns the migration expects.

**Correct path:** upgrade to the latest v0.9.x first, let it run once to normalize the DB, then upgrade to 0.10.0.

### Manual schema repair when you can't roll back

If you already upgraded straight to 0.10.0, your recordings are still on disk, but the UI errors out — and you can't or don't want to roll back to v0.9.x — you can **manually bring the DB schema up to 0.10.0** with the script below. Your recording files are not touched; only the missing columns get added.

> ⚠️ **Back up first.** This edits the DB directly; the backup is your only rollback.

**Symptom checklist — you're affected if:**

- the Recordings page shows `failed to list recordings` but the `.mp4` segments are clearly still on disk;
- adding/editing a camera errors out (e.g. `no such column: stable_id`);
- any "database" error appeared after jumping from any v0.3.0 – v0.8.0 straight to 0.10.0.

**Step 1 — stop the service and back up the DB.**

Bare metal (systemd):

```bash
systemctl stop mibee-nvr
cp /var/lib/mibee-nvr/nvr.db /var/lib/mibee-nvr/nvr.db.pre-fix   # adjust to your Storage.root_dir
```

Docker:

```bash
docker compose stop mibee-nvr
cp ./data/nvr.db ./data/nvr.db.pre-fix          # default volume is ./data:/data; DB filename is under Storage.root_dir
```

**Step 2 — save this repair script as `fix-schema.sql`** (copy in full):

```sql
-- ===== MiBeeNvr manual schema repair (0.3.0-0.8.0 -> 0.10.0) =====
-- Each ADD COLUMN runs independently; if a column already exists you'll see
-- "duplicate column name" -- that error can be ignored, the rest still apply.
-- Re-running is idempotent (it won't damage data).

-- ----- recordings -----
ALTER TABLE recordings ADD COLUMN merge_status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE recordings ADD COLUMN merge_path TEXT DEFAULT '';
ALTER TABLE recordings ADD COLUMN merge_error TEXT DEFAULT '';
ALTER TABLE recordings ADD COLUMN merge_tier TEXT DEFAULT '';
ALTER TABLE recordings ADD COLUMN merge_progress INTEGER DEFAULT 0;
ALTER TABLE recordings ADD COLUMN merge_quality TEXT DEFAULT 'complete';
ALTER TABLE recordings ADD COLUMN archived INTEGER DEFAULT 0;
ALTER TABLE recordings ADD COLUMN ai_status TEXT DEFAULT NULL;
ALTER TABLE recordings ADD COLUMN ai_processed_at TEXT DEFAULT NULL;
ALTER TABLE recordings ADD COLUMN ai_error TEXT DEFAULT NULL;
-- Migrate legacy merged=1 rows into merge_status (only effective if the merged column still exists)
UPDATE recordings SET merge_status='merged' WHERE merged=1 AND merge_status='pending';

-- ----- cameras -----
ALTER TABLE cameras ADD COLUMN merge_enabled INTEGER;
ALTER TABLE cameras ADD COLUMN merge_check_interval TEXT;
ALTER TABLE cameras ADD COLUMN merge_window_size TEXT;
ALTER TABLE cameras ADD COLUMN merge_batch_limit INTEGER;
ALTER TABLE cameras ADD COLUMN merge_min_segment_age TEXT;
ALTER TABLE cameras ADD COLUMN merge_min_segments_to_merge INTEGER;
ALTER TABLE cameras ADD COLUMN onvif_endpoint TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN profile_token TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN stream_encoding TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN archived INTEGER DEFAULT 0;
ALTER TABLE cameras ADD COLUMN archived_at DATETIME DEFAULT NULL;
ALTER TABLE cameras ADD COLUMN archive_retention_days INTEGER DEFAULT 0;
ALTER TABLE cameras ADD COLUMN merge_duration TEXT DEFAULT 'natural-day';
ALTER TABLE cameras ADD COLUMN stream_key TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN srt_passphrase TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN srt_stream_id TEXT DEFAULT '';
ALTER TABLE cameras ADD COLUMN activation_state TEXT DEFAULT 'active';
ALTER TABLE cameras ADD COLUMN stable_id TEXT DEFAULT '';

-- ----- schema_meta / feature_flags cleanup -----
INSERT OR IGNORE INTO schema_meta(key,value) VALUES('schema_version','29');
UPDATE schema_meta SET value='29' WHERE key='schema_version';
INSERT OR IGNORE INTO feature_flags(key,value) VALUES('protocol.srt',1);
INSERT OR IGNORE INTO feature_flags(key,value) VALUES('protocol.rtmp',1);
```

**Step 3 — run the repair script.** The NVR's Docker runtime image (Alpine-based) does **not** ship `sqlite3`, so pick whichever fits your setup:

- **Option A — host already has sqlite3** (bare metal):

  ```bash
  sqlite3 /var/lib/mibee-nvr/nvr.db < fix-schema.sql
  ```
  A few `duplicate column name: ...` lines are expected (they mean the column already exists); only other errors need attention.

- **Option B — throwaway Alpine container** (recommended for Docker, nothing to install on the host):

  ```bash
  docker run --rm -v "$PWD/data:/data" -v "$PWD/fix-schema.sql:/fix-schema.sql:ro" alpine \
    sh -c "apk add --no-cache sqlite >/dev/null && sqlite3 /data/nvr.db < /fix-schema.sql"
  ```
  Replace `$PWD/data` with your actual volume directory (default `./data:/data`). `duplicate column name` errors are safe to ignore.

**Step 4 — verify the repair** (same sqlite3 session):

```bash
# Option A
sqlite3 /var/lib/mibee-nvr/nvr.db "SELECT merge_quality FROM recordings LIMIT 1;"
sqlite3 /var/lib/mibee-nvr/nvr.db "SELECT stable_id FROM cameras LIMIT 1;"

# Option B (Docker)
docker run --rm -v "$PWD/data:/data" alpine \
  sh -c "apk add --no-cache sqlite >/dev/null && sqlite3 /data/nvr.db 'SELECT merge_quality FROM recordings LIMIT 1;'"
```

Both should no longer report `no such column` (the first returns `complete`, the second an empty string or a serial) — that means the repair worked.

**Step 5 — restart and confirm the Recordings page loads normally.**

```bash
# Bare metal
systemctl start mibee-nvr
# Docker
docker compose start mibee-nvr
```

> This script is **idempotent**: re-running it on an already-0.10.0 schema just prints a bunch of `duplicate column name` and harms nothing. When in doubt about which columns you're missing, just run the full thing — it's the simplest path.

---

## General upgrade best practices

1. **Always back up** `nvr.db` and `mibee-nvr.yaml` before upgrading.
2. **Read the release notes** for the version you're targeting — this guide covers structural changes; release notes cover features and fixes.
3. **Stop the service** before replacing the binary (`systemctl stop mibee-nvr`), then start it again after.
4. **Check health** after deploy: `curl http://localhost:9090/api/health` should return `{"status":"ok",...}`.
5. **Watch the logs** for the first few minutes: `journalctl -u mibee-nvr -f`.
6. **Roll back** if needed: `make rollback RPi_HOST=user@host` restores the previous binary. The DB backup lets you restore the data if necessary.
