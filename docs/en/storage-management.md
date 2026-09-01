# Storage Management & Recording Migration

> For MiBeeNvr v0.12.0

Where recordings live, how to swap disks, and how to move history — all **runtime operations, no restart required**. The database is decoupled from the recording root (SQLite stays on the data volume), so switching storage never walks off with your index.

## Concepts

| Concept | Description |
|---------|-------------|
| **Default root** | `storage.root_dir` — cameras without an override record here |
| **Candidate volumes** | A whitelist of directories usable as a recording root (managed at runtime; the settings dropdown reads it) |
| **Per-camera storage** | `storage.camera_roots` assigns one camera its own root (e.g. a high-bitrate camera gets its own disk) |
| **Background migration** | The idle-time job that moves history after a switch (rate-limited + time window, never fights recording IO) |

## Swapping Disks in Three Steps (Web UI)

1. **Add a candidate volume**: Settings → Storage → add a candidate path (must be mounted and writable; NAS deployments list platform-authorized dirs — the page notes "managed by the platform")
2. **Switch the default root or set per-camera**: switch the default root on the Storage page, or pick one camera's own root in its edit form's "Storage" section
3. **Migrate history**: optionally tick "delete recordings in the original storage after migration", then click "Migrate existing recordings to the new storage" — takes effect immediately; new segments land in the new location right away, history moves in the background

The switch is immediate (the next segment goes to the new location); migration runs in the background; the NVR never needs a restart.

## Configuration

```yaml
storage:
  root_dir: "/var/lib/mibee-nvr"   # default recording root
  # db_path: ""                    # database location (data dir by default; auto-pinned
  #                                 # on bare-metal first boot — root switches don't move it)
  camera_roots:                    # per-camera overrides (optional)
    backyard: "/mnt/bigdisk/recordings"
  migration_rate_mb: 15            # background copy rate cap (MB/s, default 15)
  migration_window: "22:00-06:00"  # only migrate in this local-time window (empty = always, rate-limited)
```

The migrator works file-by-file: copy → rewrite the DB row → verify → delete source. Every job pre-checks capacity (target needs a 20% margin) and is refused up front if the disk can't hold it — no half-written failures.

## API

### Candidate volumes

```bash
# List (env_managed=true means the deploy platform manages candidates — e.g. fnOS
# authorized dirs; manual adds are session-only there)
curl -u admin:password http://192.168.1.50:9090/api/storage/candidates

# Add at runtime (validates exists/dir/writable; refuses the current root and
# paths in use as camera overrides)
curl -u admin:password -X POST \
  http://192.168.1.50:9090/api/storage/candidates \
  -H "Content-Type: application/json" \
  -d '{"path": "/mnt/newdisk"}'

# Remove
curl -u admin:password -X DELETE \
  "http://192.168.1.50:9090/api/storage/candidates?path=/mnt/newdisk"
```

### Per-camera storage + migration

```bash
# Set this camera's recording root; migrate=true also enqueues its history
curl -u admin:password -X PUT \
  http://192.168.1.50:9090/api/cameras/backyard/storage-root \
  -H "Content-Type: application/json" \
  -d '{"root": "/mnt/bigdisk/recordings", "migrate": true, "delete_source": true}'

# Query current setting and migration progress
curl -u admin:password http://192.168.1.50:9090/api/cameras/backyard/storage-root
```

### Batch migration

```bash
# One-shot disk swap: hot-switch the default root + clear per-camera overrides +
# enqueue every camera with history
curl -u admin:password -X POST \
  http://192.168.1.50:9090/api/storage/migrate \
  -H "Content-Type: application/json" \
  -d '{"target": "/mnt/newdisk", "delete_source": true}'

# Queue and status (job states: queued/running/paused/done/failed)
curl -u admin:password http://192.168.1.50:9090/api/storage/migrate/status
```

## Platform Notes

- **fnOS**: candidate dirs come from the app's authorized directories (platform-managed) — paths added manually in the web UI don't survive a restart; authorize them on the NAS for long-term use
- **Docker**: container-internal paths must be mounted first (`-v /mnt/newdisk:/mnt/newdisk`), then added as candidates. Note that external storage (USB enclosures) is unusable on some NAS platforms due to kernel-level restrictions — prefer storage-pool directories
- **Database location**: the SQLite database stays on the data volume (it does not follow the recording root). Legacy installs are migrated once automatically on upgrade — no manual steps

## FAQ

| Question | Answer |
|----------|--------|
| Old recordings still on the old disk after switching? | Expected — new segments go to the new location immediately; history needs the migration (storage-page button or API `migrate: true`) |
| Does migration stall recording? | No — 15MB/s rate limit by default + optional time window; capacity pre-checks refuse doomed jobs instead of failing halfway |
| Migrate just one camera? | Yes — the per-camera API (`PUT /api/cameras/{id}/storage-root`) |
| Can migration be interrupted? | The migrator resumes across restarts (file-by-file); source deletion happens only after each file verifies |

## Next Steps

- [Adaptive Recording](adaptive-recording.md) — cut disk usage at the write-density level first
- [Storage Research](storage-research.md) — design deep-dive of the storage subsystem
