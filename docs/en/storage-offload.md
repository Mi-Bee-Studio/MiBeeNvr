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
                                                        evict local copy: auto / CLI ◀── evictable
```

- **What gets uploaded**: rolling-merge products — the consolidated ~hourly MP4 files the merge engine already produces (not the raw 30-second fragments). One recording = one object, keeping object counts and request counts low
- **The outbox table persists upload state** (`pending → uploading → uploaded → evicted`, plus the terminal `skipped`): rows re-queue automatically after a crash and restart — nothing is lost and nothing is uploaded twice (overwriting writes are idempotent)
- **Minimum-age grace** (15 minutes by default): uploads are allowed only after the merge debounce and any backfill output have settled — a still-growing bucket is never uploaded; if late growth slips through anyway, a size-compare triggers an idempotent re-upload
- **An upload only counts as `uploaded` after a `HeadObject` verifies the remote size matches** — a truncated or ghost success never gets confirmed
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
      after_days: 0                               # 0 = upload only (default); 30 = auto-evict 30 days after confirmed upload
    playback:
      presigned: false                            # 302 to signed URLs for direct playback (see Playback below)
      endpoint_url: ""                            # browser-reachable endpoint override used for presigning
      ttl_s: 3600                                 # presigned URL lifetime in seconds
```

After editing, validate with `mibee-nvr validate-config` and restart the service. The web UI (Settings → Storage) edits the same fields and shows live queue status.

**Credentials & safety:**

- `access_key_id` / `secret_access_key` accept `${VAR}` environment-variable references — expansion happens when the client is constructed, the config file stores only the reference, and saving from the web settings page never writes the plaintext back into the YAML
- `secret_access_key` belongs to the auto-encrypted field set: when `NVR_ENCRYPTION_KEY` (or `NVR_ENCRYPTION_KEY_FILE`) is set, the stored secret is encrypted before hitting disk and handled consistently by `encrypt-config`; `${VAR}` references are left as references
- An unset `${VAR}` expands to an empty string and fails startup validation with "credentials empty" — a wrong configuration fails loudly instead of running broken

### Per-camera routing

Route specific cameras to a different bucket and/or key prefix (the same semantics as per-camera storage roots):

```yaml
storage:
  remote:
    camera_overrides:
      front-door: { bucket: important-bucket, prefix: yard }
      back-yard:   { prefix: outdoor }
```

The outbox row pins each object's bucket at enqueue time — later config edits never move already-uploaded objects. Overrides referencing unknown cameras are rejected at validation.

## Evicting local copies

Uploading alone never deletes anything locally. Two ways to reclaim space after an upload is confirmed:

- **Automatic** — set `evict.after_days: 30` and a background sweep deletes local copies 30 days after upload confirmation. It reuses the same evict path as the CLI, so every deletion is `HeadObject`-verified first; a failed verification refuses the eviction and keeps the local file
- **Manual CLI** — dry-run by default, review the report before executing:

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
- Before evicting, make sure the bucket's lifecycle policy won't delete objects prematurely

## Playback of remote recordings

Evicted recordings stay visible in the web UI: the Recordings page lists them in a cloud-archive section, playable inline and downloadable.

- **Default (proxy)**: playback is served through the NVR (`GET /api/offload/objects/{id}`, HTTP Range). The proxy coalesces the many small range requests a browser emits while scrubbing — aligned block fetches with a small LRU cache — so seek-heavy playback does not hammer the object store
- **Presigned direct playback** (`playback.presigned: true`): the playback endpoint answers with a 302 redirect to a short-lived signed URL, so video bytes flow directly from the store to the browser and the NVR relays nothing. Turn this on only if browsers can reach the store — if the NVR reaches MinIO via an internal Docker name, set `playback.endpoint_url` to the browser-reachable address. Any signing failure silently falls back to proxying

## Observability

Besides `mibee-nvr offload status`, the same counters are exposed as Prometheus metrics: `nvr_offload_outbox_rows{status}` (backlog = pending + uploading) and `nvr_offload_uploaded_bytes_total`; `/api/offload/status` feeds the settings page.

## Interaction with cleanup & retention

- `cleanup` (local retention) runs as usual — it only manages local files; whether a recording was uploaded never affects local cleanup decisions
- For long-term cold retention: keep local `retention_days` short (e.g. 7 days) and leave the remote side to bucket lifecycle (e.g. 365 days)
- Emergency cleanup triggered by the disk watermark (`disk_threshold_percent`) equally doesn't check upload state — for "uploaded means don't delete yet", enable `evict.after_days` or free space proactively with the evict CLI

## FAQ

| Question | Answer |
|----------|--------|
| Do uploads steal disk IO from recording? | No — offload is one of the tenants sharing the I/O budget, scheduled together with recording / playback / merging |
| What if it crashes mid-upload? | The outbox row sits in `uploading` and re-queues automatically after restart, with an idempotent overwrite |
| Why does one recording never upload? | Check whether its age is below `min_age_s` (15 minutes by default); then whether the backlog hit its limit and raised the alarm |
| What if my uplink is slower than recording production? | The backlog grows to `backlog_limit`, then enqueueing pauses with a warning — local retention still applies, nothing is lost. Raise the limit, accept the lag, or record less |
| Can the NVR run entirely from the cloud (no local disk)? | No, deliberately — crash consistency, merge and playback would all depend on the uplink; local-first with async upload is the supported model |
| Can it be configured from the web UI? | The settings page edits the fields and shows live queue status; eviction runs via `evict.after_days` or the CLI |
| How do I configure R2? | `endpoint_url: https://<account>.r2.cloudflarestorage.com`, `region: auto`, `path_style: true` |
| What does it cost on paid stores? | Egress and per-request charges are the main drivers — the proxy's range coalescing and uploading merged (not fragmented) recordings both keep request counts low |

## Next Steps

- [CLI Reference](cli.md) — full `offload status / evict` options
- [Performance Tuning](performance.md) — how the I/O budget is split across tenants
- [Storage Management](storage-management.md) — local multi-disk and per-camera storage
