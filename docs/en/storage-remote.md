# Remote Object Storage Offload (S3-Compatible)

MiBee NVR can upload merged recordings to an S3-compatible object store (AWS S3, MinIO, Cloudflare R2, Backblaze B2, Aliyun OSS, Tencent COS — one wire protocol) for off-site disaster recovery or archiving from a small local disk. Local recording is never changed: recording, merging, playback and cleanup keep working exactly as before, and uploading is a strictly background side channel that yields to recording I/O.

## How It Works

- **What gets uploaded**: rolling-merge products — the consolidated ~hourly MP4 files the merge engine already produces (not the raw 30-second fragments). One recording = one object, keeping object counts and request counts low.
- **When**: a background scan (default every 60s) picks up merged recordings whose window has been closed for at least `upload.min_age_s` (default 15 minutes — this covers merge debounce and backfill so a still-growing window is never uploaded). A rare late append is caught by a size check and re-uploaded (an idempotent overwrite of the same object).
- **Crash safety**: every upload is tracked in an outbox table (`pending → uploading → uploaded → evicted`). If the NVR is killed mid-upload, the next start re-queues the row and re-uploads — same object key, idempotent overwrite, no duplicates, no lost segments.
- **Confirmation**: an upload only counts as `uploaded` after a HEAD request verifies the remote object's size matches. A truncated or ghost success never marks a recording as confirmed.
- **Your uplink is the limit, not the NVR**: uploads pace themselves against the shared I/O budget (`offload` tenant), and a configurable backlog cap stops queueing with a loud warning when recording production outpaces the uplink — data is never silently dropped.

## Configuration

```yaml
storage:
  remote:
    enabled: true
    endpoint_url: "http://192.168.1.10:9000"   # S3 API endpoint
    region: "auto"                              # R2/B2/MinIO ignore this
    bucket: "nvr-archive"
    path_style: true                            # MinIO/self-hosted: keep true
    access_key_id: "${S3_ACCESS_KEY}"           # env-var refs supported
    secret_access_key: "${S3_SECRET_KEY}"
    upload:
      max_concurrency: 1                        # parallel PUTs
      scan_interval_s: 60                       # discovery sweep
      min_age_s: 900                            # window-finalization grace
      backlog_limit: 5000                       # queue cap (0 = unlimited)
    evict:
      after_days: 0                             # local retention after upload
    playback:
      presigned: false                          # 302 to signed URLs (see below)
      endpoint_url: ""                          # browser-reachable override
      ttl_s: 3600
```

Settings changes apply on the next start; the web UI (Settings → Storage) covers the same fields and shows live queue status. Remote offload is disabled by default.

### Credentials

`access_key_id` / `secret_access_key` support `${VAR}` environment-variable references — the NVR expands them when it builds the S3 client and never writes the plaintext back to the config file. If you set `NVR_ENCRYPTION_KEY`, the stored secret is additionally encrypted at rest (`mibee-nvr encrypt-config`).

### Per-Camera Routing

Route specific cameras to a different bucket and/or key prefix (the same semantics as per-camera storage roots):

```yaml
storage:
  remote:
    camera_overrides:
      front-door: { bucket: "important", prefix: "yard" }
      back-yard:   { prefix: "outdoor" }
```

The outbox row pins each object's bucket at enqueue time — later config edits never move already-uploaded objects. Overrides referencing unknown cameras are rejected at validation.

## Eviction (Freeing Local Space)

Uploading alone never deletes anything locally. Two ways to reclaim space after upload is confirmed:

- **Automatic** — set `evict.after_days: 30` and local copies are deleted 30 days after upload confirmation.
- **Manual CLI** — validate first, then apply:

```bash
mibee-nvr offload evict --config /path/to/mibee-nvr.yaml --all-uploaded          # dry-run report
mibee-nvr offload evict --config /path/to/mibee-nvr.yaml --all-uploaded --execute
```

Every eviction re-verifies the remote object with a fresh HEAD request (exact size match) before the local file is deleted. A failed verification refuses the eviction and reports loudly — this is the only defense against an upload that lied. **The NVR never deletes remote objects**; retention in the bucket is managed by your storage provider's lifecycle rules.

## Playback of Remote Recordings

Evicted recordings stay visible in the web UI: the Recordings page has a Cloud-archive section listing them per day, playable inline and downloadable.

- **Default (proxy)**: playback is served through the NVR (`GET /api/offload/objects/{id}`, HTTP Range). The proxy coalesces the many small range requests a browser emits while scrubbing — aligned block fetches with a small LRU cache — so seek-heavy playback does not hammer the object store.
- **Presigned direct playback** (`playback.presigned: true`): the playback endpoint answers with a 302 redirect to a short-lived signed URL, so video bytes flow directly from the store to the browser and the NVR relays nothing. Turn this on only if browsers can reach the store — if the NVR reaches MinIO via an internal Docker name, set `playback.endpoint_url` to the browser-reachable address. Any signing failure silently falls back to proxying.

## Observability

```bash
mibee-nvr offload status --config /path/to/mibee-nvr.yaml
```

The same counters are exposed as Prometheus metrics: `nvr_offload_outbox_rows{status}` (backlog = pending+uploading) and `nvr_offload_uploaded_bytes_total`, plus `/api/offload/status` for the settings page.

## FAQ

**Which storage providers work?** Anything speaking the S3 API: AWS S3, MinIO (self-hosted — recommended for LAN use), Cloudflare R2, Backblaze B2, Aliyun OSS, Tencent COS. Path-style addressing covers MinIO and most self-hosted stores; AWS virtual-hosted buckets set `path_style: false`.

**What happens when my uplink is slower than recording?** The backlog grows until `backlog_limit`, then enqueueing pauses with a warning (local retention still applies — nothing is lost). Either raise the limit, accept the lag, or reduce what you record.

**Can the NVR run entirely from the cloud (no local disk)?** No, deliberately. Crash consistency, merge and playback would all depend on the uplink — the weakest link must not be the main path. Local-first with async upload is the supported model.

**Cost notes**: egress and per-request charges are the main drivers on paid stores — the proxy's range coalescing and uploading merged (not fragmented) recordings are both designed to keep request counts low.
