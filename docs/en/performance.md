# Performance Tuning

> Scope: process memory discipline and background I/O pacing on resource-constrained hosts.  
> Modules: `internal/memlimit`, `internal/iobudget`.

---

## Memory Tiering

Three consumers compete for RAM on an NVR host, and misunderstanding their
relationship is the root of most "mysterious slowdowns":

| Tier | What it is | Who manages it | What happens when squeezed |
|------|------------|----------------|----------------------------|
| Go heap | Recorder ring buffers, merge sample metadata, API JSON | The Go runtime (GC) | GC runs hotter; recording latency rises |
| Page cache | Kernel cache of recording files | The kernel | Every read/write hits the disk → I/O amplification |
| tmpfs | `/tmp` extraction scratch (#746 lesson) | The kernel (RAM-backed) | Occupies RAM the heap believes it owns |

The default Go behavior (GOGC=100, no memory limit) grows the heap
aggressively: on a 4GB production host the NVR process peaked at 1.6GB and
evicted the page cache that recording I/O depended on — the extra disk reads
showed up as I/O stalls that looked like a storage problem but were a memory
problem.

### Automatic GOMEMLIMIT

Since #756 the server startup path installs a conservative **soft** memory
limit (`debug.SetMemoryLimit`) before any manager starts allocating:

```text
limit = min( 45% of physical RAM, 1 GiB )            # physical tier
limit = min( limit, 80% of the cgroup memory cap )   # when a cap exists
floor = 64 MiB                                        # never lower
```

Precedence — the first that applies wins:

1. `GOMEMLIMIT` env var (applied natively by the runtime before `main`;
   the NVR detects it and touches nothing),
2. `memory.soft_limit_bytes` in `mibee-nvr.yaml` (explicit operator value),
3. the automatic heuristic above.

To opt out of the heuristic:

```yaml
memory:
  disable_auto_limit: true   # keep the Go runtime default
```

The applied value is logged at startup (`process memory soft limit set`) and
published as the `nvr_memlimit_bytes` metric (0 = not set).

A soft limit never OOM-kills by itself — near the limit the GC simply works
harder. GOGC stays at its default; if you ever see GC thrash (rising
`go_memstats_gc_cpu_fraction`), raise `memory.soft_limit_bytes`.

### systemd / Docker outer limits

The in-process limit is the primary mechanism; the unit limits below are the
safety net (`deploy/mibee-nvr.service` ships them active):

```ini
MemoryHigh=450M   # soft: kernel reclaims instead of killing
MemoryMax=550M    # hard backstop
```

Values are sized for the 512MiB design baseline (RPi-3B class). On a 4GB
board use `High=1500M` / `Max=2G`. For Docker, see the commented `mem_limit`
example in `deploy/docker/docker-compose.yml` — a cgroup cap is also picked
up by the automatic heuristic (80% tier) inside the container.

---

## Background I/O Budgeting

Merge, cleanup deletes, repair and timelapse extraction are **background**
work; recording writes, API file serving and SQLite are **foreground** work.
On one kernel I/O queue they otherwise compete as equals — production
measurements showed ~60% PSI io-some with multi-second API stalls while a
timelapse merge ran (#748/#751).

Enable the shared byte-rate budget to make background work yield:

```yaml
io:
  budget_bytes_per_sec: 16777216   # 16 MiB/s ≈ 25% of a slow SD card
  # budget_burst_bytes: 33554432   # optional; default = one second of rate
  # delete_unlinks_per_sec: 200    # frame-tree unlink guardrail (default 200)
```

- **Off by default** — `budget_bytes_per_sec: 0` (or section absent) keeps
  behavior identical to previous releases.
- A sensible value is ~25% of the storage medium's sequential write
  throughput; busy media then automatically stretches background jobs while
  foreground work keeps its latency.
- The unlink guardrail additionally caps recursive frame-tree deletion at
  200 files/s (configurable), preventing the ext4 journal (jbd2) saturation
  that self-sustains even after the deleting process exits.

Observability: `nvr_iobudget_wait_seconds_total{consumer}` (time background
work spent parked) and `nvr_iobudget_bytes_charged_total{consumer}` /
`nvr_iobudget_unlinks_charged_total{consumer}` (billed volume), with
`consumer` ∈ {merge, cleanup, repair, timelapse}.

The `mibee-nvr repair delete-by-format` CLI reads the same `io:` section
from the YAML, so a manual mass-delete against a live server yields exactly
like the server's own cleanup.
