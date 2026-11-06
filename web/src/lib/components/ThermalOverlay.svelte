<script lang="ts">
  import { appendAuthToken, API_BASE } from '$lib/api';
  import { t } from '$lib/i18n';
  import { gridCellAt, cellCenter, gridValues, cellsOverThreshold } from '$lib/spectrum';

  // Live temperature overlay for a thermal camera's live view. Subscribes
  // to the per-camera SSE feed and renders the latest thermal.temperature
  // reading as a corner chip over the player. Until real-device adapters
  // land (ONVIF metadata stream / vendor radiometry), this component is fed
  // by whatever publishes through internal/thermal's Feed — the transport
  // and rendering are adapter-agnostic by design.

  let {
    cameraId,
    onprobe,
  }: {
    cameraId: string;
    /** Fired on every probe click with the normalized position + resolved
     *  cell temperature (NaN when the grid fetch failed) — lets the parent
     *  mirror the spot on a dual-pane sibling pane (same device FOV). */
    onprobe?: (probe: { nx: number; ny: number; c: number }) => void;
  } = $props();

  type Reading = {
    camera_id: string;
    timestamp: string;
    max_c?: number;
    min_c?: number;
    spots?: Array<{ name?: string; temp_c: number }>;
  };

  let reading = $state<Reading | null>(null);
  let receivedAt = $state(0);
  let now = $state(Date.now());

  // Adapters are expected at ~1–5s cadence; anything older is stale (the
  // chip dims and says so rather than showing a confident dead number).
  const STALE_MS = 8000;
  let es: EventSource | null = null;
  let tick: ReturnType<typeof setInterval> | undefined;

  $effect(() => {
    es = new EventSource(
      appendAuthToken(`${API_BASE}/cameras/${encodeURIComponent(cameraId)}/events`),
    );
    // The events endpoint wraps every payload as {"Topic":...,"Data":{...}}
    // and names each SSE event after the topic.
    es.addEventListener('thermal.temperature', (e) => {
      try {
        const raw = JSON.parse((e as MessageEvent).data);
        reading = (raw.Data ?? raw.data) as Reading;
        receivedAt = Date.now();
      } catch {
        // Ignore malformed frames
      }
    });
    tick = setInterval(() => (now = Date.now()), 1000);
    return () => {
      es?.close();
      if (tick) clearInterval(tick);
    };
  });

  let stale = $derived(reading !== null && now - receivedAt > STALE_MS);

  // Cosmetic heat banding for the max value — informational, not an alarm.
  function band(v: number | undefined): string {
    if (v === undefined) return '';
    if (v >= 80) return 'text-red-400';
    if (v >= 40) return 'text-amber-400';
    return 'text-sky-300';
  }

  // ---- Spot temperature & threshold highlight (#1106 data path) ----
  // Click anywhere on the player to probe that point; the 32x24 grid comes
  // from the NVR proxy (device credentials never reach the browser).
  let probe = $state<{ nx: number; ny: number; c: number } | null>(null);
  let threshold = $state<number | null>(null);
  let hotCells = $state<Array<{ nx: number; ny: number; v: number }>>([]);
  let frameCache: { at: number; values: number[] | null } = { at: 0, values: null };
  let probeLayer: HTMLElement | null = $state(null);

  async function fetchGrid(): Promise<number[] | null> {
    if (Date.now() - frameCache.at < 1500) return frameCache.values;
    try {
      const res = await fetch(appendAuthToken(`${API_BASE}/cameras/${encodeURIComponent(cameraId)}/thermal/frame`));
      if (!res.ok) return null;
      frameCache = { at: Date.now(), values: gridValues(await res.json()) };
      return frameCache.values;
    } catch {
      return null;
    }
  }

  async function onProbeClick(e: MouseEvent) {
    const host = probeLayer?.parentElement;
    if (!host) return;
    const r = host.getBoundingClientRect();
    const nx = (e.clientX - r.left) / r.width;
    const ny = (e.clientY - r.top) / r.height;
    if (nx < 0 || nx > 1 || ny < 0 || ny > 1) return;
    const values = await fetchGrid();
    if (!values) {
      probe = { nx, ny, c: NaN };
      onprobe?.(probe);
      return;
    }
    const { x, y } = gridCellAt(nx, ny);
    probe = { nx, ny, c: values[y * 32 + x] };
    onprobe?.(probe);
  }

  $effect(() => {
    if (threshold === null) {
      hotCells = [];
      return;
    }
    let alive = true;
    const run = async () => {
      const values = await fetchGrid();
      if (!alive || !values) return;
      hotCells = cellsOverThreshold(values, threshold).map((c0) => ({ ...cellCenter(c0.x, c0.y), v: c0.v }));
    };
    run();
    const iv = setInterval(run, 3000);
    return () => {
      alive = false;
      clearInterval(iv);
    };
  });
</script>

<div
  class="absolute top-2 right-2 z-20 pointer-events-none select-none rounded-md
         px-2.5 py-1.5 text-xs font-mono backdrop-blur-sm bg-black/55
         text-white/90 border border-white/10 transition-opacity
         {stale || reading === null ? 'opacity-60' : 'opacity-100'}"
  data-testid="thermal-overlay"
>
  {#if reading === null}
    <span class="th-text-muted">{t('thermal.waiting')}</span>
  {:else}
    <div class="flex items-baseline gap-1.5">
      <span class="text-white/60 text-[10px]">{t('thermal.max')}</span>
      <span class="text-base font-bold {band(reading.max_c)}">
        {reading.max_c !== undefined ? reading.max_c.toFixed(1) : '--'}°C
      </span>
    </div>
    {#if reading.min_c !== undefined}
      <div class="text-[10px] text-white/60">
        {t('thermal.min')} {reading.min_c.toFixed(1)}°C
      </div>
    {/if}
    {#if stale}
      <div class="text-[10px] text-white/50">{t('thermal.stale')}</div>
    {/if}
  {/if}
  <label class="flex items-center gap-1 mt-0.5 pointer-events-auto cursor-pointer">
    <span class="text-[10px] text-white/60">{t('thermal.threshold')}</span>
    <input
      type="number"
      class="w-12 bg-black/40 border border-white/15 rounded px-1 text-[10px] text-white/90"
      value={threshold ?? ''}
      placeholder="--"
      oninput={(e) => {
        const v = parseFloat((e.target as HTMLInputElement).value);
        threshold = Number.isFinite(v) ? v : null;
      }}
      data-testid="thermal-threshold"
    />
  </label>
</div>

<!-- Click-to-probe layer + over-threshold markers, spanning the player. -->
<div
  bind:this={probeLayer}
  class="absolute inset-0 z-10 cursor-crosshair"
  onclick={onProbeClick}
  data-testid="thermal-probe-layer"
>
  {#each hotCells as hc (hc.nx.toFixed(3) + hc.ny.toFixed(3))}
    <span
      class="absolute w-2 h-2 -translate-x-1/2 -translate-y-1/2 rounded-full bg-red-500/85 ring-1 ring-white/70"
      style="left:{hc.nx * 100}%;top:{hc.ny * 100}%"
      title="{hc.v.toFixed(1)}°C"
    ></span>
  {/each}
  {#if probe}
    <span
      class="absolute -translate-x-1/2 -translate-y-[150%] px-1 py-0.5 rounded bg-black/70 text-[10px] font-mono text-white border border-white/20"
      style="left:{probe.nx * 100}%;top:{probe.ny * 100}%"
      data-testid="thermal-probe-value"
    >
      {Number.isNaN(probe.c) ? '--' : probe.c.toFixed(1)}°C
    </span>
  {/if}
</div>
