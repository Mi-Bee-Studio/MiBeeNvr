# Platform Cross-cutting Concerns

Auth, events, storage, config, health and SPA delivery — the foundation every
feature area stands on.

## 1. Auth chain

```mermaid
flowchart LR
    REQ["HTTP request"] --> AK{"Bearer mbv_…?\n(constant-time compare)"}
    AK -- yes --> APIK["API-key identity (machine-to-machine; skips rate limits)"]
    AK -- no --> BA["BasicAuth + bcrypt\n(cached; failure rate-limited)"]
    APIK & BA --> MIX["middleware chain"]
    MIX --> GW["gateway SSO (reverse-proxy path auth;\nsilent re-mint on wake)"]
```

- `?api_key=` query fallback exists for SSE/WebSocket clients that cannot set
  headers — the URL itself carries the credential.
- API keys are independent of user passwords, revocable, and never expose user
  credentials.
- A stateless session token layer sits above BasicAuth; login failures are
  rate-limited (never auto-retry on auth failure).

## 2. Event bus & SSE

```mermaid
flowchart LR
    PUB["publishers (recorder / cleanup / onvif / gb / p2p …)"] --> BUS["event bus (pub/sub)"]
    BUS --> SUBS["subscribers (merge / push / statistics)"]
    BUS --> SSEF["GET /api/events?filter=topic.\npublic, rate-limited; {Topic,Data} envelope"]
```

The bus is the loose-coupling spine: AI push, cascade catalogs, notifications
and webhook/MQTT triggers (HMAC-signed, replay-windowed) all hang off it.

## 3. Storage management

```mermaid
flowchart TB
    ROOTS["storage roots (multiple candidates)"] --> ASSIGN["per-camera assignment\n(hot switch + capacity pre/post checks)"]
    ASSIGN --> MIG["background migration\n(rate-limited, time-windowed, per-file row rewrites, resumable)"]
    MIG --> FALL["unavailable-root fallback at startup + candidate pruning"]
    TIER["tiered rows: 5m rotation → nightly fold"] --> CLEAN["cleanup: retention batches\nskips ai_status=processing"]
```

## 4. Configuration

- Many top-level sections plus an extension map for out-of-tree consumers;
  atomic saves (tmp + rename); credential fields stored encrypted.
- The running process re-serializes the whole YAML on shutdown — patch config
  files with the service stopped, never live.

## 5. Health & observability

- Periodic health checks with self-remediation (source restarts, session
  rebuilds); remote log shipping; Prometheus metrics for auth and HTTP.
- Update checks compare git-describe builds against the base tag so
  development builds never show a phantom "update available".

## 6. SPA delivery

- Svelte 5 + Vite; the build lands in `internal/ui/static` and is embedded in
  the binary — **a binary refresh must always ship a freshly built SPA**.
- The service worker caches by content fingerprint; cache-busting follows the
  build hash.
