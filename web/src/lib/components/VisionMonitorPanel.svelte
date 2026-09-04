<script lang="ts">
  import { onMount } from 'svelte';
  import { getVisionStatus, getVisionMetrics } from '$lib/api/settings';
  import type { VisionStatus, VisionSample } from '$lib/api/settings';
  import { t } from '$lib/i18n';
  import { BrainCircuit } from 'lucide-svelte';
  import MiniTimeChart from './MiniTimeChart.svelte';

  // Vision consumer monitor panel (#671/#692): current heartbeat metric tiles
  // + axis-labelled queue-depth / drop-rate charts from the heartbeat history
  // ring (~24h @ 30s). Mounted at the top of the dashboard Vision tab; renders
  // nothing until the first successful fetch.

  let status = $state<VisionStatus | null>(null);
  let points = $state<VisionSample[]>([]);
  let failed = $state(false);

  const POLL_MS = 15_000;
  const HISTORY_HOURS = 24;

  async function refresh() {
    try {
      const [s, m] = await Promise.all([getVisionStatus(), getVisionMetrics(HISTORY_HOURS)]);
      status = s;
      points = m.points ?? [];
      failed = false;
    } catch {
      failed = true;
    }
  }

  onMount(() => {
    refresh();
    const timer = setInterval(refresh, POLL_MS);
    return () => clearInterval(timer);
  });

  interface Pt {
    t: number;
    v: number;
  }

  // Bucket the ~30s heartbeat ring into ≤140 windows so a full 24h stays
  // readable; `rate` turns the cumulative dropped_total into drops/min.
  function bucketize(samples: VisionSample[], rate: boolean): Pt[] {
    if (samples.length === 0) return [];
    const size = Math.max(1, Math.ceil(samples.length / 140));
    const out: Pt[] = [];
    for (let i = 0; i < samples.length; i += size) {
      const slice = samples.slice(i, i + size);
      const t = Date.parse(slice[0].ts);
      if (!rate) {
        const avg = slice.reduce((s, p) => s + p.queue_depth, 0) / slice.length;
        out.push({ t, v: avg });
        continue;
      }
      const base = i > 0 ? samples[i - 1].dropped_total : slice[0].dropped_total;
      const drops = Math.max(0, slice[slice.length - 1].dropped_total - base);
      const spanMs =
        Date.parse(slice[slice.length - 1].ts) -
        (i > 0 ? Date.parse(samples[i].ts) : Date.parse(slice[0].ts));
      const mins = Math.max(0.5, spanMs / 60_000);
      out.push({ t, v: drops / mins });
    }
    return out;
  }

  let queueSeries = $derived(bucketize(points, false));
  let dropSeries = $derived(bucketize(points, true));

  const fmtSeen = (iso?: string) => {
    if (!iso) return '';
    const d = new Date(iso);
    return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(
      d.getSeconds(),
    ).padStart(2, '0')}`;
  };
</script>

{#if status?.enabled}
  <div class="vision-monitor th-bg-elevated" data-testid="vision-monitor-panel">
    <div class="vm-head">
      <div class="vm-title">
        <BrainCircuit size={16} />
        <span>{t('visionMonitor.title')}</span>
      </div>
      <div class="vm-status">
        {#if status.healthy}
          <span class="dot dot-ok"></span>
          <span>{t('visionMonitor.healthy')}</span>
        {:else}
          <span class="dot dot-bad"></span>
          <span>{t('visionMonitor.degraded')}</span>
        {/if}
        {#if status.device}
          <span class="vm-chip">{status.device}</span>
        {/if}
        {#if status.last_seen}
          <span class="vm-chip">{t('visionMonitor.lastSeen', { time: fmtSeen(status.last_seen) })}</span>
        {/if}
        <span class="vm-range">{t('visionMonitor.sampleNote')}</span>
      </div>
    </div>

    {#if failed}
      <p class="vm-error">{t('visionMonitor.refreshFailed')}</p>
    {/if}

    {#if status.metrics}
      {@const m = status.metrics}
      <div class="vm-tiles">
        <div class="vm-tile" data-testid="vm-tile-queue">
          <span class="vm-k">{t('visionMonitor.queue')}</span>
          <span class="vm-v">{status.queue_depth ?? 0}<span class="vm-sub">/{m.queue_capacity ?? '?'}</span></span>
        </div>
        <div class="vm-tile" data-testid="vm-tile-decoded">
          <span class="vm-k">{t('visionMonitor.decodedQueue')}</span>
          <span class="vm-v">{m.decoded_queue_depth ?? 0}</span>
        </div>
        <div class="vm-tile">
          <span class="vm-k">{t('visionMonitor.workers')}</span>
          <span class="vm-v">{m.decode_workers ?? '-'}<span class="vm-sub"> · {m.workers_busy ?? 0} busy</span></span>
        </div>
        <div class="vm-tile">
          <span class="vm-k">{t('visionMonitor.processed')}</span>
          <span class="vm-v">{status.processed ?? '-'}</span>
        </div>
        <div class="vm-tile">
          <span class="vm-k">{t('visionMonitor.hits')}</span>
          <span class="vm-v">{m.events_emitted ?? 0}</span>
        </div>
        <div class="vm-tile">
          <span class="vm-k">{t('visionMonitor.latency')}</span>
          <span class="vm-v">{((m.seg_ms_p50 ?? 0) / 1000).toFixed(1)}s<span class="vm-sub"> / p90 {((m.seg_ms_p90 ?? 0) / 1000).toFixed(1)}s</span></span>
        </div>
        <div class="vm-tile" class:vm-warn={(m.dropped_total ?? 0) > 0}>
          <span class="vm-k">{t('visionMonitor.dropped')}</span>
          <span class="vm-v">{m.dropped_total ?? 0}</span>
          <span class="vm-sub">{t('visionMonitor.droppedDetail', { full: String(m.dropped_queue_full ?? 0), ttl: String(m.dropped_ttl ?? 0) })}</span>
        </div>
        <div class="vm-tile">
          <span class="vm-k">{t('visionMonitor.marked')}</span>
          <span class="vm-v">{status.drops_marked_total ?? 0}</span>
        </div>
        {#if m.mem_available_mb}
          <div class="vm-tile" class:vm-warn={m.mem_available_mb < 600}>
            <span class="vm-k">{t('visionMonitor.mem')}</span>
            <span class="vm-v">{m.mem_available_mb}MB</span>
            <span class="vm-sub">load {m.load1 ?? 0}</span>
          </div>
        {/if}
      </div>

      <div class="vm-charts">
        <div class="vm-chart" data-testid="vm-chart-queue">
          <div class="vm-chart-head">
            <span class="vm-k">{t('visionMonitor.queueTrend')}</span>
          </div>
          <MiniTimeChart
            points={queueSeries}
            unit={t('visionMonitor.unitSegments')}
            emptyLabel={t('visionMonitor.noData')}
            color="var(--color-info, #3b82f6)"
          />
        </div>
        <div class="vm-chart" data-testid="vm-chart-drop">
          <div class="vm-chart-head">
            <span class="vm-k">{t('visionMonitor.dropTrend')}</span>
          </div>
          <MiniTimeChart
            points={dropSeries}
            unit={t('visionMonitor.unitDropsPerMin')}
            emptyLabel={t('visionMonitor.noData')}
            color="var(--color-danger, #ef4444)"
            decimals={1}
          />
        </div>
      </div>
      <p class="vm-hint">{t('visionMonitor.hint')}</p>
    {:else}
      <p class="vm-hint">{t('visionMonitor.noMetrics')}</p>
    {/if}
  </div>
{/if}

<style>
  .vision-monitor {
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: 0.875rem 1rem;
    margin: 0 0 1rem 0;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .vm-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  .vm-title {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    font-weight: 600;
    font-size: 0.875rem;
    color: var(--text-primary);
  }

  .vm-status {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    font-size: 0.75rem;
    color: var(--text-secondary);
    flex-wrap: wrap;
  }

  .vm-chip {
    padding: 0 0.375rem;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    font-family: ui-monospace, monospace;
  }

  .vm-range {
    color: var(--text-tertiary);
    font-size: 0.6875rem;
  }

  .dot {
    width: 8px;
    height: 8px;
    border-radius: 9999px;
  }
  .dot-ok { background: var(--color-success, #22c55e); }
  .dot-bad { background: var(--color-danger, #ef4444); }

  .vm-tiles {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
    gap: 0.5rem;
  }

  .vm-tile {
    display: flex;
    flex-direction: column;
    gap: 0.125rem;
    padding: 0.5rem 0.625rem;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
  }

  .vm-tile.vm-warn .vm-v { color: var(--color-danger, #ef4444); }

  .vm-k {
    font-size: 0.6875rem;
    color: var(--text-tertiary);
  }

  .vm-v {
    font-size: 1.125rem;
    font-weight: 600;
    color: var(--text-primary);
    font-variant-numeric: tabular-nums;
  }

  .vm-sub {
    font-size: 0.6875rem;
    font-weight: 400;
    color: var(--text-tertiary);
  }

  .vm-charts {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.75rem;
  }

  .vm-chart {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    min-width: 0;
  }

  .vm-chart-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
  }

  .vm-unit {
    font-size: 0.6875rem;
    color: var(--text-tertiary);
  }

  .vm-hint, .vm-error {
    font-size: 0.6875rem;
    color: var(--text-tertiary);
    margin: 0;
  }

  .vm-error { color: var(--color-danger, #ef4444); }

  @media (max-width: 639px) {
    .vm-charts { grid-template-columns: 1fr; }
  }
</style>
