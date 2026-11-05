<script lang="ts">
  import { getThermalHistory, type ThermalPoint } from '$lib/api/thermal';
  import { t } from '$lib/i18n';

  // P4 playback temperature curve (#38 Tier 2): the minute-aggregated scene
  // extremes for [from, to] as a compact SVG band — max line solid, min line
  // dashed, the band between them filled. Hover reads the nearest stored
  // minute. Empty/failed loads render a quiet hint, never an error card (the
  // curve is a bonus next to the recording, not a precondition).

  let {
    cameraId,
    from,
    to,
    points = 480,
  }: {
    cameraId: string;
    from: string; // ISO
    to: string; // ISO
    points?: number;
  } = $props();

  let data = $state<ThermalPoint[]>([]);
  let loaded = $state(false);

  $effect(() => {
    const ctrl = new AbortController();
    loaded = false;
    data = [];
    getThermalHistory(cameraId, from, to, { signal: ctrl.signal, points })
      .then((h) => {
        data = h.points ?? [];
        loaded = true;
      })
      .catch(() => {
        // 404 (not thermal / no history yet) or transient — quiet hint.
        data = [];
        loaded = true;
      });
    return () => ctrl.abort();
  });

  const W = 640;
  const H = 150;
  const M = { top: 10, right: 10, bottom: 18, left: 44 };

  // Value domain over the visible points with a small margin; a flat scene
  // (min==max) still gets a readable ~2°C window.
  let domain = $derived.by(() => {
    let lo = Infinity;
    let hi = -Infinity;
    for (const p of data) {
      if (p.min_c != null) lo = Math.min(lo, p.min_c);
      if (p.max_c != null) hi = Math.max(hi, p.max_c);
    }
    if (lo === Infinity && hi === -Infinity) return null;
    if (lo === Infinity) lo = hi;
    if (hi === -Infinity) hi = lo;
    if (hi - lo < 2) {
      const mid = (hi + lo) / 2;
      lo = mid - 1;
      hi = mid + 1;
    }
    const pad = (hi - lo) * 0.1;
    return { lo: lo - pad, hi: hi + pad };
  });

  let t0 = $derived(Date.parse(from) || 0);
  let t1 = $derived(Date.parse(to) || t0 + 1);
  const x = (iso: string): number => {
    const ms = Date.parse(iso);
    if (Number.isNaN(ms) || t1 <= t0) return M.left;
    return M.left + ((ms - t0) / (t1 - t0)) * (W - M.left - M.right);
  };
  const y = (v: number): number => {
    if (!domain) return H - M.bottom;
    const f = (domain.hi - v) / (domain.hi - domain.lo);
    return M.top + Math.min(1, Math.max(0, f)) * (H - M.top - M.bottom);
  };

  function lineOf(pick: (p: ThermalPoint) => number | null | undefined): string {
    let d = '';
    let pen = false;
    for (const p of data) {
      const v = pick(p);
      if (v == null || Number.isNaN(v)) {
        pen = false;
        continue;
      }
      const px = x(p.minute).toFixed(1);
      const py = y(v).toFixed(1);
      d += `${pen ? 'L' : 'M'}${px} ${py}`;
      pen = true;
    }
    return d;
  }

  let maxLine = $derived(lineOf((p) => p.max_c));
  let minLine = $derived(lineOf((p) => p.min_c));
  // Band between min and max: forward along max, backward along min.
  let band = $derived.by(() => {
    const maxPts: Array<[number, number]> = [];
    const minPts: Array<[number, number]> = [];
    for (const p of data) {
      if (p.max_c != null) maxPts.push([x(p.minute), y(p.max_c)]);
      if (p.min_c != null) minPts.push([x(p.minute), y(p.min_c)]);
    }
    if (maxPts.length < 2 || minPts.length < 2) return '';
    let d = `M${maxPts[0][0].toFixed(1)} ${maxPts[0][1].toFixed(1)}`;
    for (let i = 1; i < maxPts.length; i++) d += `L${maxPts[i][0].toFixed(1)} ${maxPts[i][1].toFixed(1)}`;
    for (let i = minPts.length - 1; i >= 0; i--) d += `L${minPts[i][0].toFixed(1)} ${minPts[i][1].toFixed(1)}`;
    return d + 'Z';
  });

  // Y ticks: 3 clean steps across the domain.
  let ticks = $derived.by(() => {
    if (!domain) return [] as Array<{ v: number; y: number; label: string }>;
    const out: Array<{ v: number; y: number; label: string }> = [];
    for (let i = 0; i <= 2; i++) {
      const v = domain.lo + ((domain.hi - domain.lo) * i) / 2;
      out.push({ v, y: y(v), label: `${v.toFixed(1)}°` });
    }
    return out;
  });

  // Hover: nearest stored point by x distance.
  let hover = $state<{ px: number; py: number; p: ThermalPoint } | null>(null);
  let svgEl: SVGSVGElement | undefined = $state();

  function onMove(e: MouseEvent) {
    if (!svgEl || data.length === 0) return;
    const r = svgEl.getBoundingClientRect();
    const px = ((e.clientX - r.left) / r.width) * W;
    let best: ThermalPoint | null = null;
    let bestDx = Infinity;
    for (const p of data) {
      const dx = Math.abs(x(p.minute) - px);
      if (dx < bestDx) {
        bestDx = dx;
        best = p;
      }
    }
    if (!best) return;
    const v = best.max_c ?? best.min_c;
    if (v == null) return;
    hover = { px: x(best.minute), py: y(v), p: best };
  }

  const fmtTime = (iso: string): string =>
    new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
</script>

<div class="relative w-full" data-testid="thermal-history-chart">
  {#if loaded && (!data.length || !domain)}
    <div class="flex items-center justify-center h-[150px] text-xs th-text-muted">
      {t('thermal.historyEmpty')}
    </div>
  {:else if !loaded}
    <div class="flex items-center justify-center h-[150px]">
      <div class="spinner spinner-sm"></div>
    </div>
  {:else}
    <svg
      bind:this={svgEl}
      viewBox="0 0 {W} {H}"
      class="w-full cursor-crosshair"
      style="height: {H}px;"
      role="img"
      aria-label={t('thermal.historyTitle')}
      onmousemove={onMove}
      onmouseleave={() => (hover = null)}
    >
      {#each ticks as tk}
        <line x1={M.left} y1={tk.y} x2={W - M.right} y2={tk.y} class="grid" />
        <text x={M.left - 6} y={tk.y + 3} class="tick" text-anchor="end">{tk.label}</text>
      {/each}
      <text x={M.left} y={H - 4} class="tick">{fmtTime(from)}</text>
      <text x={W - M.right} y={H - 4} class="tick" text-anchor="end">{fmtTime(to)}</text>

      {#if band}
        <path {band} class="band" />
      {/if}
      <path d={maxLine} class="line-max" />
      <path d={minLine} class="line-min" />

      {#if hover}
        <line x1={hover.px} y1={M.top} x2={hover.px} y2={H - M.bottom} class="cursor" />
        <circle cx={hover.px} cy={hover.py} r="3" class="dot" />
      {/if}
    </svg>
    {#if hover}
      <div
        class="absolute z-10 pointer-events-none px-2 py-1 rounded bg-black/75 text-white text-[11px] font-mono border border-white/20"
        style="left: min(calc(100% - 130px), {(hover.px / W) * 100}%); top: 4px;"
      >
        {fmtTime(hover.p.minute)}
        {#if hover.p.max_c != null}
          <span class="text-amber-300"> {hover.p.max_c.toFixed(1)}°C</span>
        {/if}
        {#if hover.p.min_c != null}
          <span class="text-sky-300"> / {hover.p.min_c.toFixed(1)}°C</span>
        {/if}
      </div>
    {/if}
  {/if}
</div>

<style>
  .grid {
    stroke: var(--border);
    stroke-width: 1;
    stroke-dasharray: 2 4;
  }
  .tick {
    fill: var(--text-secondary);
    font-size: 10px;
    font-family: ui-monospace, monospace;
  }
  .band {
    fill: var(--color-primary);
    fill-opacity: 0.12;
  }
  .line-max {
    fill: none;
    stroke: #f59e0b;
    stroke-width: 1.5;
  }
  .line-min {
    fill: none;
    stroke: #38bdf8;
    stroke-width: 1;
    stroke-dasharray: 4 3;
  }
  .cursor {
    stroke: var(--text-secondary);
    stroke-width: 1;
  }
  .dot {
    fill: #f59e0b;
    stroke: white;
    stroke-width: 1;
  }
</style>
