# Object-Storage Offload (S3 Cold Backup)

> Applies to MiBeeNvr v0.13.0 · feature issue [#874](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues/874), disabled by default

After a rolling merge finishes writing a recording, the NVR can **upload it asynchronously** to S3-compatible object storage for cold backup / archiving: local recording behavior stays completely unchanged, and once an upload has succeeded and been verified with a remote `HeadObject`, you can optionally evict the local copy to free disk. Good fits:

- Home broadband / NAS deployments with limited disk that want cold data archived to cheap object storage (Cloudflare R2 and B2 charge no egress)
- Off-site disaster recovery — recordings survive a stolen machine or a dead disk
- Cloud objects plug straight into S3-capable playback / forensics tooling (retention is left to bucket lifecycle policies)

Supported targets: AWS S3, MinIO, Cloudflare R2, Backblaze B2, Alibaba Cloud OSS, Tencent Cloud COS, and other S3-compatible stores (verified against a real MinIO).

## How it works

```text
segments ──rolling merge──▶ merged output ──(after min_age grace)──▶ offload_outbox queue
                                                                        │
                             scan loop (60s default) picks pending rows ──▶ S3 PUT ──▶ HeadObject verify
                                                                        │                  │
                                          late growth → size-compare re-upload ◀── verified → uploaded
                                                                                           │
                                                        manual CLI evicts local copy ◀── evictable
```

- **The outbox table persists upload state** (`pending → uploading → uploaded → evicted`, plus the terminal `skipped`): rows re-queue automatically after a crash and restart — nothing is lost and nothing is uploaded twice (overwriting writes are idempotent)
- **Minimum-age grace** (15 minutes by default): uploads are allowed only after the merge debounce and any backfill output have settled — a still-growing bucket is never uploaded; if late growth slips through anyway, a size-compare triggers an idempotent re-upload
- **Backlog-limit alarm** (5000 rows by default): when upstream bandwidth can't keep up with production, enqueueing stops and an alert fires instead of the queue growing forever
- Uploads run under the process-wide **I/O budget** (tenant `offload`), scheduled together with recording / playback / merging — they never squeeze the foreground
- **Remote objects are never deleted** — each object gets a per-object Head re-check before local eviction; remote cleanup is the bucket lifecycle policy's job

Object key layout: `<prefix>/<camera>/<date>/<id>.<ext>` (default prefix `recordings`).

## Enabling

The `storage.remote` block in `mibee-nvr.yaml` (off as a whole by default):

```yaml
storage:
  remote:
    enabled: true
    endpoint_url: https://s3.example.com        # required; AWS uses the regional endpoint
    region: auto                                  # R2 uses auto; MinIO can keep the default
    bucket: my-nvr-archive                        # required
    path_style: true                              # MinIO / self-hosted must be true; AWS virtual-hosted buckets use false
    access_key_id: ${S3_ACCESS_KEY}               # ${VAR} environment-variable references supported
    secret_access_key: ${S3_SECRET_KEY}           # auto-encrypted at rest; never written back in plaintext
    prefix: recordings
    upload:
      max_concurrency: 1                          # the home uplink is the bottleneck; default 1 (max 8)
      scan_interval_s: 60                         # discovery scan period (max 3600)
      min_age_s: 900                              # minimum age after merge completion (60–86400)
      backlog_limit: 5000                         # pending-row cap (0 = unlimited)
    evict:
      after_days: 0                               # 0 = upload only, no automatic eviction (recommended for now)
```

After editing, validate with `mibee-nvr validate-config` and restart the service.

**Credentials & safety:**

- `access_key_id` / `secret_access_key` accept `${VAR}` environment-variable references — expansion happens when the client is constructed, the config file stores only the reference, and saving from the web settings page never writes the plaintext back into the YAML
- `secret_access_key` belongs to the auto-encrypted field set: encrypted before hitting disk, handled consistently by `encrypt-config`
- An unset `${VAR}` expands to an empty string and fails startup validation with "credentials empty" — a wrong configuration fails loudly instead of running broken

## Evicting local copies

In this release eviction goes through the CLI (automatic eviction arrives in a later version). **Dry-run by default** — review the report before executing:

```bash
# Queue status: pending/uploading/uploaded/evicted counts and the oldest pending age
mibee-nvr offload status

# Eviction report (dry-run; touches no files)
mibee-nvr offload evict --all-uploaded

# One camera only
mibee-nvr offload evict --camera front-door

# Execute once confirmed: every object gets a HeadObject re-check that it exists
# remotely before the local file is deleted
mibee-nvr offload evict --all-uploaded --execute
```

- Eviction **deletes local files only**; remote objects are never removed — backing out of the cold backup is just re-downloading the objects
- Safe to run against a live service (WAL reads are concurrency-safe); for large batches prefer off-peak hours
- Before `--execute`, make sure the bucket's lifecycle policy won't delete objects prematurely

## Interaction with cleanup & retention

- `cleanup` (local retention) runs as usual — it only manages local files; whether a recording was uploaded never affects local cleanup decisions
- For long-term cold retention: keep local `retention_days` short (e.g. 7 days) and leave the remote side to bucket lifecycle (e.g. 365 days)
- Emergency cleanup triggered by the disk watermark (`disk_threshold_percent`) equally doesn't check upload state — for "uploaded means don't delete yet", free space proactively with the evict CLI

## FAQ

| Question | Answer |
|----------|--------|
| Do uploads steal disk IO from recording? | No — offload is one of the tenants sharing the I/O budget, scheduled together with recording / playback / merging |
| What if it crashes mid-upload? | The outbox row sits in `uploading` and re-queues automatically after restart, with an idempotent overwrite |
| Why does one recording never upload? | Check whether its age is below `min_age_s` (15 minutes by default); then whether the backlog hit its limit and raised the alarm |
| Can it be configured from the web UI? | The settings page edits credentials and the on/off switch; eviction and status go through the CLI |
| How do I configure R2? | `endpoint_url: https://<account>.r2.cloudflarestorage.com`, `region: auto`, `path_style: true` |

## Next Steps

- [CLI Reference](cli.md) — full `offload status / evict` options
- [Performance Tuning](performance.md) — how the I/O budget is split across tenants
- [Storage Management](storage-management.md) — local multi-disk and per-camera storage
