<script lang="ts">
  import type { Detection } from '$lib/ai-detection/inference';

  let {
    detections = [],
    visible = true,
    width = 0,
    height = 0,
  }: {
    detections?: Detection[];
    visible?: boolean;
    width?: number;
    height?: number;
  } = $props();

  let canvasEl: HTMLCanvasElement | undefined = $state();

  // ─── Color mapping by COCO class category ──────────────────────────────

  function getClassColor(classId: number): string {
    if (classId === 0) return '#22c55e'; // person → green
    if (classId >= 1 && classId <= 8) return '#3b82f6'; // vehicle (bicycle, car, motorcycle, airplane, bus, train, truck, boat) → blue
    if (classId >= 15 && classId <= 25) return '#f97316'; // animal (cat, dog, horse, sheep, cow, elephant, bear, zebra, giraffe, bird) → orange
    return '#eab308'; // other → yellow
  }

  // ─── Canvas rendering ──────────────────────────────────────────────────

  $effect(() => {
    // Read reactive deps so Svelte tracks them
    const _d = detections;
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
