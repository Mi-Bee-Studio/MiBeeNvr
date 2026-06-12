<script lang="ts">
  import type { Detection } from '$lib/ai-detection/inference';
  import { getAuthHeader } from '$lib/api';
  import { t } from '$lib/i18n';
  import type { DetectionObj } from '$lib/api';

  let {
    detections = [],
    visible = true,
    width = 0,
    height = 0,
    source = 'local',
    cameraId = '',
    frameWidth = 0,
    frameHeight = 0,
    onSourceChange,
  }: {
    detections?: Detection[];
    visible?: boolean;
    width?: number;
    height?: number;
    source?: 'local' | 'backend';
    cameraId?: string;
    frameWidth?: number;
    frameHeight?: number;
    onSourceChange?: (source: 'local' | 'backend') => void;
  } = $props();

  let canvasEl: HTMLCanvasElement | undefined = $state();
  let backendDetections: Detection[] = $state([]);
  let wsConnected = $state(false);

  // Non-reactive WS management
  let ws: WebSocket | null = null;
  const MAX_RECONNECT_ATTEMPTS = 5;
  const RECONNECT_DELAY = 3000;
  let reconnectAttempts = 0;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let destroyed = false;

  // Pick which detections to render based on active source
  let renderDetections = $derived(
    source === 'backend' ? backendDetections : detections,
  );

  // ─── Color mapping by COCO class category ──────────────────────────────

  function getClassColor(classId: number): string {
    if (classId === 0) return '#22c55e'; // person → green
    if (classId >= 1 && classId <= 8) return '#3b82f6'; // vehicle (bicycle, car, motorcycle, airplane, bus, train, truck, boat) → blue
    if (classId >= 15 && classId <= 25) return '#f97316'; // animal (cat, dog, horse, sheep, cow, elephant, bear, zebra, giraffe, bird) → orange
    return '#eab308'; // other → yellow
  }

  // ─── WebSocket management (backend AI source) ─────────────────────────

  function buildWsUrl(): string {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    let url = `${proto}//${location.host}/api/ai/events/ws`;
    const authHeader = getAuthHeader();
    if (authHeader) {
      const token = authHeader.startsWith('Basic ') ? authHeader.slice(6) : authHeader;
      url += `?token=${encodeURIComponent(token)}`;
    }
    return url;
  }

  function connectWs(): void {
    if (destroyed || source !== 'backend' || !cameraId) return;
    if (ws) return;

    try {
      const socket = new WebSocket(buildWsUrl());
      ws = socket;

      socket.onopen = () => {
        if (destroyed) {
          socket.close();
          return;
        }
        wsConnected = true;
        reconnectAttempts = 0;
      };

      socket.onmessage = (event: MessageEvent) => {
        if (destroyed) return;
        try {
          const msg = JSON.parse(event.data as string);
          if (msg.type === 'detection' && msg.camera_id === cameraId) {
            backendDetections = convertBackendDetections(
              msg.detections || [],
              msg.frame_width || frameWidth,
              msg.frame_height || frameHeight,
              width,
              height,
            );
          }
        } catch (e) {
          console.warn('[AiOverlay] Failed to parse WS message:', e);
        }
      };

      socket.onclose = () => {
        wsConnected = false;
        ws = null;
        if (!destroyed && source === 'backend') {
          scheduleReconnect();
        }
      };

      socket.onerror = () => {
        // onclose will always follow onerror — reconnect is handled there
      };
    } catch (e) {
      console.warn('[AiOverlay] WebSocket connection failed:', e);
      ws = null;
      scheduleReconnect();
    }
  }

  function scheduleReconnect(): void {
    if (destroyed || source !== 'backend' || reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) return;
    reconnectAttempts++;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (!destroyed && source === 'backend') {
        connectWs();
      }
    }, RECONNECT_DELAY);
  }

  function closeWs(): void {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    if (ws) {
      try {
        ws.close();
      } catch {
        /* already closed */
      }
      ws = null;
    }
    wsConnected = false;
    reconnectAttempts = 0;
  }

  function convertBackendDetections(
    objs: DetectionObj[],
    fw: number,
    fh: number,
    cw: number,
    ch: number,
  ): Detection[] {
    if (!objs || objs.length === 0) return [];
    const scaleX = fw > 0 && cw > 0 ? cw / fw : 1;
    const scaleY = fh > 0 && ch > 0 ? ch / fh : 1;
    return objs.map((obj) => ({
      bbox: [
        (obj.bbox[0] ?? 0) * scaleX,
        (obj.bbox[1] ?? 0) * scaleY,
        (obj.bbox[2] ?? 0) * scaleX,
        (obj.bbox[3] ?? 0) * scaleY,
      ] as [number, number, number, number],
      confidence: obj.confidence ?? 0,
      classId: obj.class_id ?? 0,
      label: obj.class_name ?? 'unknown',
    }));
  }

  // ─── React to source/cameraId changes ──────────────────────────────────

  $effect(() => {
    const _source = source;
    const _cameraId = cameraId;

    // Close any existing WS and reset state
    closeWs();
    backendDetections = [];

    if (_source !== 'backend' || !_cameraId) return;
    connectWs();
  });

  // ─── Cleanup on component destroy ─────────────────────────────────────

  $effect(() => {
    return () => {
      destroyed = true;
      closeWs();
    };
  });

  // ─── Canvas rendering ──────────────────────────────────────────────────

  $effect(() => {
    // Read reactive deps so Svelte tracks them
    const _d = renderDetections;
    const _w = width;
    const _h = height;
    const _v = visible;

    if (!canvasEl || !_v || _w === 0 || _h === 0 || _d.length === 0) {
      // Clear canvas when hidden or no detections
      if (canvasEl) {
        const ctx = canvasEl.getContext('2d');
        ctx?.clearRect(0, 0, canvasEl.width, canvasEl.height);
      }
      return;
    }

    // Match canvas internal size to display size
    if (canvasEl.width !== _w || canvasEl.height !== _h) {
      canvasEl.width = _w;
      canvasEl.height = _h;
    }

    const ctx = canvasEl.getContext('2d');
    if (!ctx) return;

    ctx.clearRect(0, 0, _w, _h);

    for (const det of _d) {
      const [x1, y1, x2, y2] = det.bbox;
      const color = getClassColor(det.classId);
      const label = `${det.label} ${Math.round(det.confidence * 100)}%`;

      // Bounding box
      ctx.strokeStyle = color;
      ctx.lineWidth = 2;
      ctx.strokeRect(x1, y1, x2 - x1, y2 - y1);

      // Label background
      ctx.font = '11px monospace';
      const textMetrics = ctx.measureText(label);
      const textHeight = 14;
      const padX = 4;
      const padY = 2;
      const labelW = textMetrics.width + padX * 2;
      const labelH = textHeight + padY * 2;

      // Position label above box; if too high, put it inside the top
      let labelX = x1;
      let labelY = y1 - labelH;
      if (labelY < 0) labelY = y1;

      ctx.fillStyle = 'rgba(0, 0, 0, 0.65)';
      ctx.fillRect(labelX, labelY, labelW, labelH);

      // Label text
      ctx.fillStyle = color;
      ctx.fillText(label, labelX + padX, labelY + textHeight);
    }
  });
</script>

<!-- svelte-ignore binding_property_non_reactive -->
<canvas
  bind:this={canvasEl}
  class="absolute inset-0 w-full h-full pointer-events-none"
  style="z-index: 5;"
  aria-hidden="true"
></canvas>

<!-- Source toggle button (only rendered when onSourceChange callback is provided) -->
{#if onSourceChange}
  <button
    class="absolute bottom-1.5 right-1.5 z-10 px-1.5 py-0.5 text-[10px] leading-none rounded-full pointer-events-auto cursor-pointer select-none
           bg-black/60 text-white/75 border border-white/15
           hover:bg-black/85 hover:text-white/90 hover:border-white/30
           transition-colors duration-150"
    onclick={() => onSourceChange(source === 'local' ? 'backend' : 'local')}
    title={source === 'local' ? t('ai.source.backend') : t('ai.source.local')}
    aria-label={source === 'local' ? t('ai.source.switchTo', { source: t('ai.source.backend') }) : t('ai.source.switchTo', { source: t('ai.source.local') })}
  >
    AI: {source === 'local' ? t('ai.source.local') : t('ai.source.backend')}
  </button>
{/if}

<!-- Connection indicator for backend source -->
{#if source === 'backend' && cameraId}
  <div
    class="absolute top-1.5 right-1.5 z-10 flex items-center gap-1 pointer-events-none"
    aria-hidden="true"
  >
    <span
      class="inline-block w-1.5 h-1.5 rounded-full {wsConnected ? 'bg-green-400' : 'bg-red-400'}"
    ></span>
  </div>
{/if}
