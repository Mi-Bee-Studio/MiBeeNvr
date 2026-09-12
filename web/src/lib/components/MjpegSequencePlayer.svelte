<script lang="ts">
  /**
   * Canvas-based JPEG sequence player — the smooth-playback replacement for
   * the per-frame-GET img cycler.
   *
   * - Fetches frames in multipart batches (BATCH frames per request) instead
   *   of one HTTP GET per frame.
   * - Decodes via createImageBitmap into a small LRU around the playhead and
   *   renders with canvas.drawImage — no <img src> swaps, no opacity
   *   transitions, no per-frame DOM churn → no flicker.
   *   A late frame keeps the previous frame on screen instead of a spinner.
   * - Pacing is a drift-corrected requestAnimationFrame accumulator — a
   *   cache miss never stretches the frame interval (the old cycler armed
   *   the next timeout only after the fetch resolved).
   *
   * `currentIndex`/`playing`/`speed` are bindable so hosts can drive the
   * player with their own control bar (PlaybackPanel) or use the built-in
   * minimal controls (the unified recording viewer).
   */
  import { onMount } from 'svelte';
  import { Play, Pause } from 'lucide-svelte';

  interface Props {
    /** Known total frame count; 0 = learn from the first batch response. */
    frameCount?: number;
    /** Base playback fps (recordings default 10; merges pass their row fps). */
    fps?: number;
    fetchBatch: (offset: number, limit: number, signal?: AbortSignal) => Promise<import('$lib/multipart').FrameBatch>;
    currentIndex?: number;
    playing?: boolean;
    speed?: number;
    loop?: boolean;
    /** Render the built-in control bar (standalone usage). */
    showControls?: boolean;
    /** Optional per-frame timestamp label. */
    frameTimestamp?: ((i: number) => string) | null;
    /** Changing this resets caches/playhead WITHOUT unmounting the canvas —
     *  hosts use it to swap in the next segment seamlessly (the last frame
     *  stays on screen until the first new frame decodes). */
    resetKey?: string | number;
    onEnded?: () => void;
    onError?: (message: string) => void;
  }

  let {
    frameCount = 0,
    fps = 10,
    fetchBatch,
    currentIndex = $bindable(0),
    playing = $bindable(false),
    speed = $bindable(1),
    loop = false,
    showControls = true,
    frameTimestamp = null,
    resetKey = '',
    onEnded,
    onError,
  }: Props = $props();

  const BATCH = 120;
  const BLOB_CACHE_CAP = 600;
  const BITMAP_WINDOW_BACK = 16;
  const BITMAP_WINDOW_AHEAD = 32;

  let canvas: HTMLCanvasElement | null = $state(null);
  let total = $state(frameCount);
  let ready = $state(false);
  let errorMsg = $state('');

  let blobCache = new Map<number, Blob>();
  let bitmaps = new Map<number, ImageBitmap>();
  const fetchedBatches = new Set<number>();
  const inflightBatches = new Map<number, Promise<void>>();
  let abort: AbortController | null = null;

  // --- Batch fetching ------------------------------------------------------

  function ensureBatch(batchStart: number): Promise<void> {
    if (total > 0 && batchStart >= total) return Promise.resolve();
    if (fetchedBatches.has(batchStart)) return Promise.resolve();
    const existing = inflightBatches.get(batchStart);
    if (existing) return existing;

    // Snapshot the controller: a reset() mid-flight must be able to detect
    // that this response belongs to the PREVIOUS sequence and drop it.
    const myAbort = abort;
    const signal = myAbort?.signal;
    const p = fetchBatch(batchStart, BATCH, signal)
      .then((batch) => {
        if (myAbort !== abort) return; // reset happened — stale sequence
        if (batch.total > 0 && batch.total !== total) total = batch.total;
        batch.frames.forEach((blob, k) => {
          blobCache.set(batch.offset + k, blob);
        });
        pruneBlobCache();
        fetchedBatches.add(batchStart);
        if (batch.frames.length < BATCH) fetchedBatches.add(batchStart + BATCH); // tail
        // The current frame's data may just have arrived (drawCurrent bailed
        // out when the blob was missing) — paint it now. Guarded against
        // re-entry by drawCurrent itself.
        void drawCurrent();
      })
      .catch((e) => {
        if (signal?.aborted) return;
        errorMsg = e instanceof Error ? e.message : String(e);
        onError?.(errorMsg);
      })
      .finally(() => {
        inflightBatches.delete(batchStart);
      });
    inflightBatches.set(batchStart, p);
    return p;
  }

  function pruneBlobCache() {
    if (blobCache.size <= BLOB_CACHE_CAP) return;
    const keepFrom = Math.max(0, currentIndex - BLOB_CACHE_CAP / 2);
    for (const k of [...blobCache.keys()].sort((a, b) => a - b)) {
      if (blobCache.size <= BLOB_CACHE_CAP) break;
      if (k < keepFrom) blobCache.delete(k);
    }
  }

  function ensureAround(i: number) {
    void ensureBatch(Math.floor(i / BATCH) * BATCH);
    // Prefetch the next batch halfway through the current one so playback
    // never catches up with the network on LAN.
    if (i % BATCH >= BATCH * 0.5) void ensureBatch((Math.floor(i / BATCH) + 1) * BATCH);
    pruneBitmaps(i);
  }

  function pruneBitmaps(center: number) {
    for (const k of [...bitmaps.keys()]) {
      if (k < center - BITMAP_WINDOW_BACK || k > center + BITMAP_WINDOW_AHEAD) {
        bitmaps.get(k)?.close();
        bitmaps.delete(k);
      }
    }
  }

  // --- Decode + draw -------------------------------------------------------

  let drawing = false;
  let redrawQueued = false;

  async function ensureBitmap(i: number): Promise<ImageBitmap | null> {
    const cached = bitmaps.get(i);
    if (cached) return cached;
    const blob = blobCache.get(i);
    if (!blob) return null;
    try {
      const bmp = await createImageBitmap(blob);
      // Playhead may have moved on while decoding — only cache if still wanted.
      if (Math.abs(i - currentIndex) > BITMAP_WINDOW_AHEAD * 4) {
        bmp.close();
        return null;
      }
      bitmaps.set(i, bmp);
      pruneBitmaps(currentIndex);
      return bmp;
    } catch {
      blobCache.delete(i); // corrupt frame — refetch would give the same bytes
      return null;
    }
  }

  async function drawCurrent() {
    if (drawing) {
      redrawQueued = true;
      return;
    }
    drawing = true;
    try {
      const idx = currentIndex;
      const bmp = await ensureBitmap(idx);
      if (!bmp) {
        ensureAround(idx);
        return;
      }
      if (canvas) {
        if (canvas.width !== bmp.width || canvas.height !== bmp.height) {
          canvas.width = bmp.width;
          canvas.height = bmp.height;
        }
        canvas.getContext('2d')?.drawImage(bmp, 0, 0);
      }
      ready = true;
    } finally {
      drawing = false;
      if (redrawQueued) {
        redrawQueued = false;
        void drawCurrent();
      }
    }
  }

  // --- Pacing --------------------------------------------------------------

  let rafId = 0;
  let lastTs = 0;
  let acc = 0;

  function frameTick(ts: number) {
    if (!playing) {
      rafId = 0;
      lastTs = 0;
      return;
    }
    if (lastTs === 0) lastTs = ts;
    acc += (ts - lastTs) * speed;
    lastTs = ts;
    const interval = 1000 / Math.max(1, fps);
    let steps = 0;
    while (acc >= interval && steps < 10) {
      acc -= interval;
      steps++;
      if (total > 0 && currentIndex < total - 1) {
        currentIndex++;
      } else if (loop && total > 0) {
        currentIndex = 0;
      } else {
        // End of sequence: hold the last frame, stop, notify the host.
        acc = 0;
        playing = false;
        lastTs = 0;
        rafId = 0;
        onEnded?.();
        return;
      }
    }
    rafId = requestAnimationFrame(frameTick);
  }

  $effect(() => {
    if (playing && !rafId) rafId = requestAnimationFrame(frameTick);
    return () => {
      lastTs = 0;
    };
  });

  // --- Reactions -----------------------------------------------------------

  $effect(() => {
    // Learn the total from the first batch when the host didn't know it.
    if (total === 0) void ensureBatch(0);
  });

  // Soft reset on resetKey change: clear every cache + cancel in-flight
  // batches belonging to the previous sequence, then re-seed the new one —
  // the canvas element (and its last painted frame) survives, so a host
  // swapping segments this way shows NO loading flash. The pacing rAF loop
  // is deliberately NOT cancelled here (effects run in declaration order —
  // cancelling would freeze playback), and `ready` is deliberately kept
  // true: flipping it would strobe the first-load spinner overlay on every
  // segment hop (segments can be 3-6 frames — a hop every second).
  $effect(() => {
    resetKey;
    abort?.abort();
    abort = new AbortController();
    for (const bmp of bitmaps.values()) bmp.close();
    bitmaps.clear();
    blobCache.clear();
    fetchedBatches.clear();
    inflightBatches.clear();
    total = frameCount;
    errorMsg = '';
    currentIndex = 0;
    acc = 0;
    lastTs = 0;
    ensureAround(0);
  });

  $effect(() => {
    // Clamp seeks to the known range.
    if (total > 0 && currentIndex >= total) currentIndex = Math.max(0, total - 1);
  });

  $effect(() => {
    currentIndex;
    errorMsg = '';
    ensureAround(currentIndex);
    void drawCurrent();
  });

  onMount(() => {
    abort = new AbortController();
    ensureAround(0);
    return () => {
      abort?.abort();
      if (rafId) cancelAnimationFrame(rafId);
      rafId = 0;
      for (const bmp of bitmaps.values()) bmp.close();
      bitmaps.clear();
      blobCache.clear();
    };
  });

  function togglePlay() {
    if (!playing && total > 0 && currentIndex >= total - 1) currentIndex = 0;
    playing = !playing;
  }

  function cycleSpeed() {
    speed = speed >= 8 ? 1 : speed * 2;
  }

  function fmtCounter(): string {
    return total > 0 ? `${currentIndex + 1} / ${total}` : `${currentIndex + 1}`;
  }
</script>

<div class="flex flex-col items-center w-full bg-black">
  <div class="relative flex items-center justify-center w-full min-h-[200px] max-h-[75vh] overflow-hidden">
    <canvas bind:this={canvas} class="max-w-full max-h-[75vh] object-contain" width="0" height="0"></canvas>
    {#if !ready && !errorMsg}
      <div class="absolute inset-0 flex items-center justify-center bg-black/40">
        <div class="spinner spinner-lg"></div>
      </div>
    {/if}
    {#if errorMsg}
      <div class="absolute inset-0 flex items-center justify-center bg-black/60 p-4">
        <p class="text-sm text-red-400 text-center">{errorMsg}</p>
      </div>
    {/if}
  </div>

  {#if showControls}
    <div class="w-full flex items-center gap-3 px-3 py-2 th-bg-secondary border-t th-border text-sm">
      <button
        class="btn btn-sm btn-primary"
        onclick={togglePlay}
        disabled={total > 0 && total <= 1}
        aria-label={playing ? 'pause' : 'play'}
      >
        {#if playing}
          <Pause size={14} />
        {:else}
          <Play size={14} />
        {/if}
      </button>
      <input
        type="range"
        class="flex-1 range range-sm range-primary"
        min="0"
        max={Math.max(0, total - 1)}
        value={currentIndex}
        oninput={(e) => (currentIndex = Number((e.target as HTMLInputElement).value))}
        disabled={total <= 1}
      />
      <span class="tabular-nums th-text-secondary whitespace-nowrap">{fmtCounter()}</span>
      <button class="btn btn-sm btn-ghost" onclick={cycleSpeed} disabled={total <= 1}>{speed}×</button>
      {#if frameTimestamp}
        <span class="th-text-tertiary whitespace-nowrap hidden sm:inline">{frameTimestamp(currentIndex)}</span>
      {/if}
    </div>
  {/if}
</div>
