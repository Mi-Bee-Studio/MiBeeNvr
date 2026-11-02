<script lang="ts">
  import { appendAuthToken, API_BASE } from '$lib/api';
  import { t } from '$lib/i18n';

  // Live temperature overlay for a thermal camera's live view. Subscribes
  // to the per-camera SSE feed and renders the latest thermal.temperature
  // reading as a corner chip over the player. Until real-device adapters
  // land (ONVIF metadata stream / vendor radiometry), this component is fed
  // by whatever publishes through internal/thermal's Feed — the transport
  // and rendering are adapter-agnostic by design.

  let { cameraId }: { cameraId: string } = $props();

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
</div>
