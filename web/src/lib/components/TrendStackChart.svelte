<script lang="ts">
  /**
   * Per-day storage-trend stacked bar chart (pure SVG — no Chart.js).
   *
   * Why not Chart.js: stacking order there is GLOBAL per dataset, but this
   * chart needs each day's bar to order its camera segments INDEPENDENTLY by
   * that day's bytes (biggest on top / bottom, user-togglable). SVG gives
   * exact per-bar control.
   *
   * Hover any segment for a styled breakdown of THAT day: every camera in
   * the bar's display order with color dot, bytes and share-of-day.
   */
  import { formatFileSize } from '$lib/format';
  import { t } from '$lib/i18n';
  import { BAR_COLORS } from '$lib/charts';

  let {
    trends,
    cameraFilter = '',
    bigOnTop = true,
    height = 288,
    legendHint = '',
    onselect,
  }: {
    /** Daily trend rows (oldest→newest; only date + camera_sizes are used). */
    trends: { date: string; camera_sizes?: Record<string, number> }[];
    /** '' = all cameras; a camera display-name isolates one series. */
    cameraFilter?: string;
    /** true = biggest segment on top of each bar. */
    bigOnTop?: boolean;
    height?: number;
    /** Tooltip text for legend items (click = isolate that camera). */
    legendHint?: string;
    /** Called with a camera name when a legend item is clicked (toggle). */
    onselect?: (name: string) => void;
  } = $props();

  const W = 720;
  const M = { top: 10, right: 10, bottom: 26, left: 52 };
  const innerW = W - M.left - M.right;
  const innerH = () => height - M.top - M.bottom;

  // Hover state: which bar + segment, and the pointer position (relative to
  // the chart container) for the floating breakdown panel.
  let hover = $state<{ barIdx: number; segName: string; px: number; py: number } | null>(null);
  let containerEl = $state<HTMLDivElement | null>(null);

  // Hover is tracked with ONE bubbled mousemove on the container (plus click
  // for touch): e.target identifies the segment rect via data attributes.
  // Per-rect onmouseenter is NOT used — Svelte 5's delegated-event handling
  // never fired it here (verified in-browser: native listener fires, the
  // Svelte handler does not).
  function onContainerPoint(e: MouseEvent) {
    if (!containerEl) return;
    const rect = (e.target as Element).closest?.('rect.seg') as SVGRectElement | null;
    if (!rect) {
      hover = null;
      return;
    }
    const box = containerEl.getBoundingClientRect();
    hover = {
      barIdx: Number(rect.dataset.bar),
      segName: rect.dataset.name || '',
      px: e.clientX - box.left,
      py: e.clientY - box.top,
    };
  }

  // Breakdown rows for the hovered day, in the bar's display order
  // (top→bottom = reversed stacking order).
  let hoverRows = $derived.by(() => {
    if (!hover) return null;
    const d = days[hover.barIdx];
    if (!d) return null;
    const rows = [...d.segs].reverse().map((s) => ({
      ...s,
      pct: d.total > 0 ? (s.size / d.total) * 100 : 0,
    }));
    return { date: d.date, total: d.total, rows };
  });

  // Stable color per camera across days (palette order by first appearance).
  let cameraColor = $derived.by(() => {
    const m = new Map<string, string>();
    let i = 0;
    for (const d of trends) {
      for (const name of Object.keys(d.camera_sizes || {})) {
        if (!m.has(name)) m.set(name, BAR_COLORS[i++ % BAR_COLORS.length]);
      }
    }
    return m;
  });

  // Per-day segments, each day sorted INDEPENDENTLY by that day's size.
  // Stacking is computed bottom-up, so "biggest on top" = ascending order.
  let days = $derived.by(() => {
    const out: { date: string; label: string; segs: { name: string; size: number }[]; total: number }[] = [];
    for (const d of trends) {
      let segs = Object.entries(d.camera_sizes || {})
        .map(([name, size]) => ({ name, size }))
        .filter((s) => s.size > 0);
      if (cameraFilter) segs = segs.filter((s) => s.name === cameraFilter);
      segs.sort((a, b) => (bigOnTop ? a.size - b.size : b.size - a.size));
      out.push({ date: d.date, label: d.date.slice(5), segs, total: segs.reduce((s, x) => s + x.size, 0) });
    }
    return out;
  });

  let maxTotal = $derived(Math.max(1, ...days.map((d) => d.total)));

  // Nice round y-axis step (1/2/5 × 10^n) so gridlines read naturally.
  function niceStep(raw: number): number {
    const pow = Math.pow(10, Math.floor(Math.log10(raw)));
    for (const k of [1, 2, 5, 10]) {
      if (k * pow >= raw) return k * pow;
    }
    return 10 * pow;
  }
  let step = $derived(niceStep(maxTotal / 4));
  let yMax = $derived(Math.ceil(maxTotal / step) * step);

  function y(v: number): number {
    return M.top + innerH() * (1 - v / yMax);
  }
  let band = $derived(innerW / Math.max(1, days.length));
  let barW = $derived(Math.max(3, band * 0.62));

  // Bottom-up cumulative rect geometry per day.
  let bars = $derived(
    days.map((d, i) => {
      const x = M.left + band * i + (band - barW) / 2;
      let acc = 0;
      const rects = d.segs.map((s) => {
        const y0 = y(acc);
        acc += s.size;
        const y1 = y(acc);
        return { name: s.name, size: s.size, x, top: y1, h: Math.max(0, y0 - y1) };
      });
      return { idx: i, label: d.label, x, rects, total: d.total, base: M.top + innerH() };
    }),
  );

  let ticks = $derived.by(() => {
    const out: { v: number; y: number; label: string }[] = [];
    for (let v = 0; v <= yMax + 1e-9; v += step) {
      out.push({ v, y: y(v), label: formatFileSize(v) });
    }
    return out;
  });

  // Tooltip placement: prefer right of the cursor, flip left when near the
  // right edge; clamp vertically.
  let tipStyle = $derived.by(() => {
    if (!hover || !containerEl) return '';
    const cw = containerEl.clientWidth;
    const left = hover.px + 14 + 190 > cw ? Math.max(4, hover.px - 204) : hover.px + 14;
    const top = Math.max(4, Math.min(hover.py - 10, (containerEl.clientHeight || 300) - 160));
    return `left: ${left}px; top: ${top}px;`;
  });
</script>

<div class="relative" bind:this={containerEl} onmousemove={onContainerPoint} onclick={onContainerPoint} onmouseleave={() => (hover = null)}>
  <svg viewBox="0 0 {W} {height}" class="w-full" role="img" style="height: {height}px;">
    <!-- gridlines + y labels -->
    {#each ticks as tk}
      <line x1={M.left} y1={tk.y} x2={W - M.right} y2={tk.y} class="grid" />
      <text x={M.left - 6} y={tk.y + 3} class="tick" text-anchor="end">{tk.label}</text>
    {/each}

    <!-- bars: each day's segments independently ordered; hover a segment for
         that day's full breakdown (identified via data-bar/data-name) -->
    {#each bars as b (b.idx)}
      {#each b.rects as r (r.name)}
        <rect
          x={r.x}
          y={r.top}
          width={barW}
          height={r.h}
          rx="1"
          fill={cameraColor.get(r.name) || 'rgba(139,92,246,0.7)'}
          class="seg"
          class:seg-hover={hover?.barIdx === b.idx && hover?.segName === r.name}
          class:seg-dim={hover !== null && hover.barIdx === b.idx && hover.segName !== r.name}
          data-bar={b.idx}
          data-name={r.name}
        />
      {/each}
      <text x={b.x + barW / 2} y={b.base + 14} class="tick" text-anchor="middle">{b.label}</text>
    {/each}
  </svg>

  <!-- hover breakdown: that day's cameras in the bar's display order -->
  {#if hoverRows}
    <div class="tip" style={tipStyle} role="tooltip">
      <div class="tip-title">{hoverRows.date} · {t('dashboard.trend.dayTotalPrefix')}{formatFileSize(hoverRows.total)}</div>
      {#each hoverRows.rows as r (r.name)}
        <div class="tip-row" class:tip-row-active={r.name === hover?.segName}>
          <span class="dot" style="background: {cameraColor.get(r.name)}"></span>
          <span class="tip-name">{r.name}</span>
          <span class="tip-val">{formatFileSize(r.size)}</span>
          <span class="tip-pct">{r.pct.toFixed(1)}%</span>
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Legend doubles as the camera picker: click an item to isolate that
     camera, click it again (or another) to switch. -->
<div class="flex flex-wrap gap-x-4 gap-y-1 justify-center mt-1 text-[11px]">
  {#each [...cameraColor.entries()] as [name, color] (name)}
    <button
      class="legend-item flex items-center gap-1.5"
      class:legend-active={cameraFilter === name}
      class:legend-dim={cameraFilter !== '' && cameraFilter !== name}
      title={legendHint}
      onclick={() => onselect?.(cameraFilter === name ? '' : name)}
    >
      <span class="inline-block w-2.5 h-2.5 rounded-sm flex-shrink-0" style="background: {color}"></span>
      <span class="truncate max-w-[160px]">{name}</span>
    </button>
  {/each}
</div>

<style>
  .grid {
    stroke: rgba(128, 128, 128, 0.15);
    stroke-width: 1;
  }
  .tick {
    fill: var(--text-muted, var(--text-secondary));
    font-size: 10px;
    font-variant-numeric: tabular-nums;
  }
  .seg {
    cursor: pointer;
    transition: opacity 0.1s ease;
  }
  .seg-hover {
    stroke: var(--text-primary, #fff);
    stroke-width: 1.5;
  }
  .seg-dim {
    opacity: 0.45;
  }
  .tip {
    position: absolute;
    z-index: 20;
    min-width: 180px;
    max-width: 300px;
    padding: 0.5rem 0.625rem;
    border-radius: var(--radius-md, 8px);
    border: 1px solid var(--border);
    background: var(--bg-elevated, var(--bg-secondary));
    box-shadow: var(--shadow-lg, 0 4px 16px rgba(0, 0, 0, 0.25));
    pointer-events: none;
    font-size: 12px;
  }
  .tip-title {
    font-weight: 600;
    color: var(--text-primary);
    margin-bottom: 0.3rem;
    font-variant-numeric: tabular-nums;
  }
  .tip-row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 1px 2px;
    color: var(--text-secondary);
    border-radius: 4px;
  }
  .tip-row-active {
    background: var(--bg-tertiary, rgba(128, 128, 128, 0.12));
    color: var(--text-primary);
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 2px;
    flex-shrink: 0;
  }
  .tip-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 140px;
  }
  .tip-val {
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .tip-pct {
    font-variant-numeric: tabular-nums;
    color: var(--text-muted, var(--text-secondary));
    min-width: 3rem;
    text-align: right;
    white-space: nowrap;
  }
  .legend-item {
    background: none;
    border: none;
    padding: 2px 6px;
    margin: -2px -6px;
    border-radius: 9999px;
    color: var(--text-muted, var(--text-secondary));
    cursor: pointer;
    transition: color 0.1s ease, background-color 0.1s ease;
  }
  .legend-item:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary, rgba(128, 128, 128, 0.12));
  }
  .legend-active {
    color: var(--text-primary);
    font-weight: 600;
    background: var(--bg-tertiary, rgba(128, 128, 128, 0.12));
  }
  .legend-dim {
    opacity: 0.5;
  }
</style>
