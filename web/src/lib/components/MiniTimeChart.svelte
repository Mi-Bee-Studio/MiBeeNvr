<script lang="ts">
  // Dependency-free mini time-series chart with readable axes (#692): Y tick
  // labels + unit, X time labels (HH:MM), gridlines, latest/peak annotations,
  // auto-decimation down to ~160 buckets so a 24h@30s ring stays light.
  interface Pt {
    t: number;
    v: number;
  }

  let {
    points = [],
    unit = '',
    color = 'var(--color-info, #3b82f6)',
    decimals = 0,
    emptyLabel = '--',
  }: {
    points?: Pt[];
    unit?: string;
    color?: string;
    decimals?: number;
    emptyLabel?: string;
  } = $props();

  const W = 340;
  const H = 120;
  const PAD = { l: 44, r: 12, t: 18, b: 16 };
  const x0 = PAD.l;
  const x1 = W - PAD.r;
  const y0 = PAD.t;
  const y1 = H - PAD.b;

  // Decimate by averaging into ≤160 buckets.
  let series = $derived.by(() => {
    if (points.length <= 160) return points;
    const n = 160;
    const size = points.length / n;
    const out: Pt[] = [];
    for (let i = 0; i < n; i++) {
      const a = Math.floor(i * size);
      const b = Math.max(a + 1, Math.floor((i + 1) * size));
      const slice = points.slice(a, b);
      out.push({ t: slice[0].t, v: slice.reduce((s, p) => s + p.v, 0) / slice.length });
    }
    return out;
  });

  // Round the ceiling to 1/2/5×10^k so tick labels stay round.
  function niceCeil(x: number): number {
    if (x <= 0) return 1;
    const base = Math.pow(10, Math.floor(Math.log10(x)));
    for (const m of [1, 2, 5, 10]) if (x <= m * base) return m * base;
    return 10 * base;
  }

  let maxV = $derived(Math.max(...series.map((p) => p.v), 0));
  let yMax = $derived(niceCeil(maxV * 1.15 || 1));
  let ticks = $derived([0, yMax / 2, yMax]);

  const fmtV = (v: number) => (decimals > 0 ? v.toFixed(decimals) : String(Math.round(v)));
  const fmtT = (ms: number) => {
    const d = new Date(ms);
    return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
  };

  function px(i: number): number {
    return series.length > 1 ? x0 + (i / (series.length - 1)) * (x1 - x0) : (x0 + x1) / 2;
  }
  function py(v: number): number {
    return y1 - (v / yMax) * (y1 - y0);
  }

  let line = $derived(series.map((p, i) => `${px(i).toFixed(1)},${py(p.v).toFixed(1)}`).join(' '));
  let area = $derived(line ? `${x0},${y1} ${line} ${x1},${y1}` : '');

  let last = $derived(series.length ? series[series.length - 1] : null);
  let peak = $derived.by(() => {
    if (series.length < 2) return null;
    let bi = 0;
    series.forEach((p, i) => {
      if (p.v > series[bi].v) bi = i;
    });
    return series[bi].v > 0 ? { i: bi, v: series[bi].v } : null;
  });

  let xLabels = $derived(
    series.length
      ? [0, Math.floor((series.length - 1) / 2), series.length - 1].map((i) => ({
          x: px(i),
          label: fmtT(series[i].t),
        }))
      : [],
  );

  // Keep annotation text inside the plot horizontally.
  function labelX(x: number, anchor: 'start' | 'end'): number {
    return anchor === 'end' ? Math.min(x, x1) : Math.max(x, x0);
  }
</script>

<svg
  viewBox="0 0 {W} {H}"
  preserveAspectRatio="none"
  data-testid="vm-timechart"
  role="img"
  aria-label={unit}
>
  {#if series.length < 2}
    <text class="vm-empty" x={(x0 + x1) / 2} y={(y0 + y1) / 2} text-anchor="middle">
      {emptyLabel}
    </text>
  {:else}
    <!-- unit caption -->
    {#if unit}
      <text class="vm-y" x={x0} y={10} text-anchor="start">{unit}</text>
    {/if}
    <!-- gridlines + Y tick labels -->
    {#each ticks as tv (tv)}
      <line class="vm-grid" x1={x0} y1={py(tv)} x2={x1} y2={py(tv)} />
      <text class="vm-y" x={x0 - 6} y={py(tv) + 3} text-anchor="end">{fmtV(tv)}</text>
    {/each}
    <!-- X time labels -->
    {#each xLabels as xl (xl.label)}
      <text class="vm-x" x={xl.x} y={H - 4} text-anchor="middle">{xl.label}</text>
    {/each}
    <!-- series -->
    <polygon points={area} fill={color} fill-opacity="0.12" />
    <polyline points={line} fill="none" stroke={color} stroke-width="1.5" />
    <!-- peak annotation (only when distinct from the latest point) -->
    {#if peak && (!last || peak.i !== series.length - 1)}
      <circle cx={px(peak.i)} cy={py(peak.v)} r="2.5" fill={color} fill-opacity="0.8" />
      <text
        class="vm-anno"
        x={labelX(px(peak.i), 'start')}
        y={Math.max(py(peak.v) - 4, 10)}
        text-anchor="start"
      >
        ▲{fmtV(peak.v)}
      </text>
    {/if}
    <!-- latest value -->
    {#if last}
      <circle cx={px(series.length - 1)} cy={py(last.v)} r="3" fill={color} />
      <text
        class="vm-anno"
        data-testid="vm-chart-last-value"
        x={labelX(px(series.length - 1) - 4, 'end')}
        y={Math.max(py(last.v) - 6, 10)}
        text-anchor="end"
      >
        {fmtV(last.v)}
      </text>
    {/if}
  {/if}
</svg>

<style>
  svg {
    width: 100%;
    height: 96px;
    background: var(--bg-tertiary);
    border-radius: var(--radius-sm);
    display: block;
  }
  .vm-grid {
    stroke: var(--border);
    stroke-width: 0.5;
    stroke-dasharray: 2 3;
  }
  .vm-y,
  .vm-x,
  .vm-anno,
  .vm-empty {
    font-size: 9px;
    fill: var(--text-tertiary);
    font-variant-numeric: tabular-nums;
  }
  .vm-anno {
    fill: var(--text-secondary);
    font-weight: 600;
  }
  .vm-empty {
    fill: var(--text-tertiary);
  }
</style>
