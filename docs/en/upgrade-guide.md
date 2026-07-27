# Upgrade Guide

This guide covers upgrade paths and breaking changes between MiBee NVR releases. Always back up your database and config before upgrading.

## Quick path table

| From → To | Status | Action required |
|-----------|--------|-----------------|
| **v0.9.1 → 0.10.0** | 🟡 **Action required** | Fix combined `protocol` strings; optional disk-space reclaim. See [v0.9.1 → 0.10.0](#v091--v01000). |
| v0.9.0 → v0.9.1 | 🟢 Transparent | None. |
| v0.8.x → v0.9.x | 🟡 Back up first | Large storage-layer refactor. Back up the DB, then upgrade. |
| v0.8.x → 0.10.0 | 🟡 Two hops | Upgrade to v0.9.x first, then to 0.10.0. |
| **< v0.9.x → 0.10.0** | 🔴 **Not supported (direct)** | You **must** upgrade to 0.9.x first — see [below](#below-v09x--v01000-not-supported-direct) for why. |

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

```
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

## < v0.9.x → v0.10.0: NOT supported (direct)

You **cannot** upgrade directly from below v0.9.x to 0.10.0. There is no startup-time version guard that aborts the process, but you will hit one of these failures instead:

- **DB timestamp parsing fails.** 0.10.0 removed support for legacy Go `time.Time.String()` formats — the monotonic-clock suffix (`m=+...`) and timezone abbreviations (`CST`). Rows containing these formats become zero-time. v0.9.x rewrote these to canonical SQLite timestamps; you must run v0.9.x first to normalize them.
- **DB schema is too old.** The v28→v29 migration assumes the v28 schema (produced by v0.9.x). Earlier schemas lack columns the migration expects.

**Correct path:** upgrade to the latest v0.9.x first, let it run once to normalize the DB, then upgrade to 0.10.0.

---

## General upgrade best practices

1. **Always back up** `nvr.db` and `mibee-nvr.yaml` before upgrading.
2. **Read the release notes** for the version you're targeting — this guide covers structural changes; release notes cover features and fixes.
3. **Stop the service** before replacing the binary (`systemctl stop mibee-nvr`), then start it again after.
4. **Check health** after deploy: `curl http://localhost:9090/api/health` should return `{"status":"ok",...}`.
5. **Watch the logs** for the first few minutes: `journalctl -u mibee-nvr -f`.
6. **Roll back** if needed: `make rollback RPi_HOST=user@host` restores the previous binary. The DB backup lets you restore the data if necessary.
