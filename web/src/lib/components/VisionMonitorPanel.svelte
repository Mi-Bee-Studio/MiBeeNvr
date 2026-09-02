<script lang="ts">
  import { onMount } from 'svelte';
  import { getVisionStatus, getVisionMetrics } from '$lib/api/settings';
  import type { VisionStatus, VisionSample } from '$lib/api/settings';
  import { t } from '$lib/i18n';
  import { BrainCircuit } from 'lucide-svelte';

  // Vision consumer monitor panel (#671): current heartbeat metrics tiles +
  // queue-depth / drop-rate sparklines from the heartbeat history ring.
  // Mounted at the top of the dashboard AI tab; renders nothing until the
  // first successful fetch.

  let status = $state<VisionStatus | null>(null);
  let points = $state<VisionSample[]>([]);
  let failed = $state(false);

  const POLL_MS = 15_000;

  async function refresh() {
    try {
      const [s, m] = await Promise.all([getVisionStatus(), getVisionMetrics(6)]);
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

  // Sparkline helpers — pure SVG polyline, no chart lib.
  const W = 240;
  const H = 40;

  function polyline(values: number[]): string {
    if (values.length === 0) return '';
    const max = Math.max(...values, 1);
    const step = values.length > 1 ? W / (values.length - 1) : W;
    return values
      .map((v, i) => `${(i * step).toFixed(1)},${(H - (v / max) * (H - 4) - 2).toFixed(1)}`)
      .join(' ');
  }

  // 队列深度序列 + 每采样窗丢弃增量(累计差分)。
  let queueSeries = $derived(points.map((p) => p.queue_depth));
  let dropSeries = $derived.by(() => {
    const out: number[] = [];
    let prev = 0;
    for (const p of points) {
      out.push(Math.max(0, p.dropped_total - prev));
      prev = p.dropped_total;
    }
    return out;
  });
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
          <span class="vm-device">{status.device}</span>
        {/if}
      </div>
    </div>

    {#if failed}
      <p class="vm-error">{t('visionMonitor.refreshFailed')}</p>
    {/if}

    {#if status.metrics}
      {@const m = status.metrics}
      <div class="vm-tiles">
        <div class="vm-tile">
          <span class="vm-k">{t('visionMonitor.queue')}</span>
          <span class="vm-v">{status.queue_depth ?? 0}<span class="vm-sub">/{m.queue_capacity ?? '?'}</span></span>
        </div>
        <div class="vm-tile">
          <span class="vm-k">{t('visionMonitor.workers')}</span>
          <span class="vm-v">{m.decode_workers ?? '-'}<span class="vm-sub">({m.workers_busy ?? 0} busy)</span></span>
        </div>
        <div class="vm-tile" class:vm-warn={(m.dropped_total ?? 0) > 0}>
          <span class="vm-k">{t('visionMonitor.dropped')}</span>
          <span class="vm-v">{m.dropped_total ?? 0}</span>
          <span class="vm-sub">{t('visionMonitor.droppedDetail', { full: String(m.dropped_queue_full ?? 0), ttl: String(m.dropped_ttl ?? 0) })}</span>
        </div>
        <div class="vm-tile">
          <span class="vm-k">{t('visionMonitor.hits')}</span>
          <span class="vm-v">{m.events_emitted ?? 0}</span>
        </div>
        <div class="vm-tile">
          <span class="vm-k">{t('visionMonitor.latency')}</span>
          <span class="vm-v">{((m.seg_ms_p50 ?? 0) / 1000).toFixed(1)}s<span class="vm-sub">/p90 {((m.seg_ms_p90 ?? 0) / 1000).toFixed(1)}s</span></span>
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

      {#if points.length > 1}
        <div class="vm-charts">
          <div class="vm-chart">
            <span class="vm-k">{t('visionMonitor.queueTrend')}</span>
            <svg viewBox="0 0 {W} {H}" preserveAspectRatio="none" data-testid="vm-spark-queue">
              <polyline points={polyline(queueSeries)} fill="none" stroke="var(--color-info, #3b82f6)" stroke-width="1.5" />
            </svg>
          </div>
          <div class="vm-chart">
            <span class="vm-k">{t('visionMonitor.dropTrend')}</span>
            <svg viewBox="0 0 {W} {H}" preserveAspectRatio="none" data-testid="vm-spark-drop">
              <polyline points={polyline(dropSeries)} fill="none" stroke="var(--color-danger, #ef4444)" stroke-width="1.5" />
            </svg>
          </div>
        </div>
      {/if}
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
  }

  .vm-device {
    margin-left: 0.5rem;
    padding: 0 0.375rem;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    font-family: ui-monospace, monospace;
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
  }

  .vm-chart svg {
    width: 100%;
    height: 40px;
    background: var(--bg-tertiary);
    border-radius: var(--radius-sm);
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
