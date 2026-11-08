<script lang="ts">
  import { appendAuthToken, API_BASE } from '$lib/api';
  import { t } from '$lib/i18n';
  import { gridCellAt, cellCenter, gridFrame, cellsOverThreshold, thermalToVisible, type GridFrame } from '$lib/spectrum';
  import { mediaContentBox, toNormalized, toHostPx } from '$lib/mediaBox';

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
     *  mirror the spot on a dual-pane sibling pane. vx/vy is the same spot in
     *  the visible picture when the device reports its thermal→visible
     *  registration (else equal to nx/ny: the FOVs are assumed aligned). */
    onprobe?: (probe: { nx: number; ny: number; c: number; vx: number; vy: number }) => void;
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
  // Click anywhere on the player to probe that point; the temperature grid
  // (32x24 SPEC, or the vendor's native geometry, e.g. 160x120) comes from
  // the NVR proxy (device credentials never reach the browser).
  let probe = $state<{ nx: number; ny: number; c: number } | null>(null);
  let threshold = $state<number | null>(null);
  let hotCells = $state<Array<{ nx: number; ny: number; v: number }>>([]);
  // Bumped on resize so marker pixel positions re-measure the picture box.
  let layoutTick = $state(0);
  let frameCache: { at: number; frame: GridFrame | null } = { at: 0, frame: null };
  // Bumped when a fresh grid lands so the hover readout re-derives from it.
  let frameVersion = $state(0);
  let probeLayer: HTMLElement | null = $state(null);

  async function fetchGrid(): Promise<GridFrame | null> {
    if (Date.now() - frameCache.at < 1500) return frameCache.frame;
    try {
      const res = await fetch(appendAuthToken(`${API_BASE}/cameras/${encodeURIComponent(cameraId)}/thermal/frame`));
      if (!res.ok) return null;
      frameCache = { at: Date.now(), frame: gridFrame(await res.json()) };
      frameVersion += 1;
      return frameCache.frame;
    } catch {
      return null;
    }
  }

  // Marker position in layer pixels: measured against the picture's content
  // box (the stream is letter/pillar-boxed inside the player), not the layer.
  function markerStyle(nx: number, ny: number): string {
    void layoutTick;
    const host = probeLayer?.parentElement;
    if (!host) return `left:${nx * 100}%;top:${ny * 100}%`;
    const p = toHostPx(host, mediaContentBox(host), nx, ny);
    return `left:${p.x}px;top:${p.y}px`;
  }

  $effect(() => {
    const bump = () => (layoutTick += 1);
    window.addEventListener('resize', bump);
    const iv = setInterval(bump, 2000); // the player may resize without a window resize
    return () => {
      window.removeEventListener('resize', bump);
      clearInterval(iv);
    };
  });

  async function onProbeClick(e: MouseEvent) {
    const host = probeLayer?.parentElement;
    if (!host) return;
    const pos = toNormalized(mediaContentBox(host), e.clientX, e.clientY);
    if (!pos) return;
    const { nx, ny } = pos;
    const frame = await fetchGrid();
    if (!frame) {
      probe = { nx, ny, c: NaN };
      onprobe?.({ ...probe, vx: nx, vy: ny });
      return;
    }
    const { x, y } = gridCellAt(nx, ny, frame.cols, frame.rows);
    probe = { nx, ny, c: frame.values[y * frame.cols + x] };
    const v = thermalToVisible(nx, ny, frame.visibleRect);
    onprobe?.({ ...probe, vx: v.nx, vy: v.ny });
  }

  // Hover readout: the temperature under the cursor, from the cached grid —
  // mousemoves never wait on the network; fetchGrid throttles itself to the
  // ~1.5s cache window and the tooltip re-derives when a fresh frame lands.
  let hover = $state<{ px: number; py: number; nx: number; ny: number } | null>(null);
  const hoverC = $derived.by(() => {
    void frameVersion;
    if (!hover || !frameCache.frame) return null;
    const { x, y } = gridCellAt(hover.nx, hover.ny, frameCache.frame.cols, frameCache.frame.rows);
    return frameCache.frame.values[y * frameCache.frame.cols + x];
  });

  function onHoverMove(e: MouseEvent) {
    const host = probeLayer?.parentElement;
    if (!host) return;
    const pos = toNormalized(mediaContentBox(host), e.clientX, e.clientY);
    if (!pos) {
      hover = null; // cursor is on the letterbox, off the picture
      return;
    }
    const lr = probeLayer!.getBoundingClientRect();
    hover = { px: e.clientX - lr.left, py: e.clientY - lr.top, nx: pos.nx, ny: pos.ny };
    void fetchGrid();
  }

  function onHoverLeave() {
    hover = null;
  }

  $effect(() => {
    if (threshold === null) {
      hotCells = [];
      return;
    }
    let alive = true;
    const run = async () => {
      const frame = await fetchGrid();
      if (!alive || !frame) return;
      hotCells = cellsOverThreshold(frame.values, threshold, 8, frame.cols).map((c0) => ({
        ...cellCenter(c0.x, c0.y, frame.cols, frame.rows),
        v: c0.v,
      }));
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
  onmousemove={onHoverMove}
  onmouseleave={onHoverLeave}
  data-testid="thermal-probe-layer"
>
  {#each hotCells as hc (hc.nx.toFixed(3) + hc.ny.toFixed(3))}
    <span
      class="absolute w-2 h-2 -translate-x-1/2 -translate-y-1/2 rounded-full bg-red-500/85 ring-1 ring-white/70"
      style={markerStyle(hc.nx, hc.ny)}
      title="{hc.v.toFixed(1)}°C"
    ></span>
  {/each}
  {#if probe}
    <span
      class="absolute -translate-x-1/2 -translate-y-[150%] px-1 py-0.5 rounded bg-black/70 text-[10px] font-mono text-white border border-white/20"
      style={markerStyle(probe.nx, probe.ny)}
      data-testid="thermal-probe-value"
    >
      {Number.isNaN(probe.c) ? '--' : probe.c.toFixed(1)}°C
    </span>
  {/if}
  {#if hover && hoverC !== null}
    <span
      class="absolute translate-x-2 -translate-y-7 px-1.5 py-0.5 rounded bg-black/75 text-[10px] font-mono
             text-white border border-white/20 pointer-events-none whitespace-nowrap"
      style="left:{hover.px}px;top:{hover.py}px"
      data-testid="thermal-hover-value"
    >
      {hoverC.toFixed(1)}°C
    </span>
  {/if}
</div>
