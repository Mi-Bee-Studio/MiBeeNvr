<script lang="ts">
  // REAL waveform visualization for any <audio> element: a Web Audio
  // AnalyserNode taps the element's output (MediaElementSource) and every
  // animation frame draws per-bar RMS amplitude — mirrored bars with
  // peak-hold caps that decay. Live listening and recording playback run
  // through the same path, so what you see is exactly what is being decoded:
  // silence is flat, nothing is simulated. When Web Audio is unavailable
  // (jsdom, very old browsers) the canvas falls back to a calm baseline so
  // the surrounding page keeps working.
  interface Props {
    /** The audio element to visualize. Re-render safe: one graph per element. */
    audio?: HTMLAudioElement | null;
    height?: number;
    bars?: number;
    /** aria-label for the canvas. */
    label?: string;
  }
  let { audio = null, height = 72, bars = 56, label = '' }: Props = $props();

  let canvas: HTMLCanvasElement | undefined = $state();
  let raf = 0;

  // One Web Audio graph per media element, for the element's whole life:
  // createMediaElementSource may only be called once per element, so remounts
  // (route changes) must reuse the existing nodes rather than create new ones.
  interface wired {
    ctx: AudioContext;
    analyser: AnalyserNode;
    data: Uint8Array;
  }
  const wiredEls = new WeakMap<HTMLAudioElement, wired>();
  let graph: wired | null = null;
  let levels: number[] = [];
  let peaks: number[] = [];
  let accent = '#7c3aed';

  function ensureGraph(el: HTMLAudioElement): wired | null {
    const existing = wiredEls.get(el);
    if (existing) {
      if (existing.ctx.state === 'suspended') void existing.ctx.resume();
      return existing;
    }
    const AC: typeof AudioContext | undefined =
      window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (!AC) return null;
    try {
      const ctx = new AC();
      const source = ctx.createMediaElementSource(el);
      const analyser = ctx.createAnalyser();
      analyser.fftSize = 2048;
      analyser.smoothingTimeConstant = 0.55;
      source.connect(analyser);
      analyser.connect(ctx.destination); // keep the element audible
      const g: wired = { ctx, analyser, data: new Uint8Array(analyser.fftSize) };
      wiredEls.set(el, g);
      return g;
    } catch {
      return null; // e.g. cross-origin element — same-origin here, defensive
    }
  }

  function readAccent(): string {
    const v = getComputedStyle(document.documentElement).getPropertyValue('--color-primary').trim();
    return v || accent;
  }

  function draw(): void {
    raf = requestAnimationFrame(draw);
    const cvs = canvas;
    if (!cvs) return;
    const dpr = window.devicePixelRatio || 1;
    const w = cvs.clientWidth;
    const h = cvs.clientHeight;
    if (w === 0 || h === 0) return;
    if (cvs.width !== Math.round(w * dpr) || cvs.height !== Math.round(h * dpr)) {
      cvs.width = Math.round(w * dpr);
      cvs.height = Math.round(h * dpr);
      accent = readAccent();
    }
    const g2d = cvs.getContext('2d');
    if (!g2d) return;
    g2d.setTransform(dpr, 0, 0, dpr, 0, 0);
    g2d.clearRect(0, 0, w, h);

    if (levels.length !== bars) {
      levels = new Array(bars).fill(0);
      peaks = new Array(bars).fill(0);
    }

    // Sample the analyser: per-bar RMS of the time-domain signal.
    if (graph) {
      graph.analyser.getByteTimeDomainData(graph.data);
      const n = graph.data.length;
      const per = n / bars;
      for (let i = 0; i < bars; i++) {
        const from = Math.floor(i * per);
        const to = Math.max(from + 1, Math.floor((i + 1) * per));
        let acc = 0;
        for (let j = from; j < to; j++) {
          const v = (graph.data[j] - 128) / 128;
          acc += v * v;
        }
        const rms = Math.sqrt(acc / (to - from));
        // Gain ~2.4 keeps speech at a lively mid-height; clamp to full scale.
        const target = Math.min(1, rms * 2.4);
        // Fast attack, slow release — bars jump on sound, ease down after.
        levels[i] = Math.max(target, levels[i] * 0.82);
        peaks[i] = Math.max(peaks[i] * 0.955, levels[i]);
      }
    } else {
      for (let i = 0; i < bars; i++) {
        levels[i] = 0;
        peaks[i] = peaks[i] * 0.955;
      }
    }

    const mid = h / 2;
    const barW = w / bars;
    const grad = g2d.createLinearGradient(0, mid - h / 2, 0, mid + h / 2);
    grad.addColorStop(0, accent);
    grad.addColorStop(0.5, accent);
    grad.addColorStop(1, accent);
    g2d.fillStyle = grad;
    for (let i = 0; i < bars; i++) {
      const barH = Math.max(2, levels[i] * (h / 2 - 2)); // never fully vanish: 2px pulse dots
      const x = i * barW + 1;
      const bw = Math.max(1.5, barW - 2.5);
      g2d.globalAlpha = 0.92;
      g2d.beginPath();
      const r = Math.min(bw / 2, 2.5);
      // Mirror-symmetric rounded bar around the center line.
      roundRect(g2d, x, mid - barH, bw, barH * 2, r);
      g2d.fill();
      // Peak-hold cap, above and below.
      const capY = Math.max(1.5, peaks[i] * (h / 2 - 2));
      g2d.globalAlpha = 0.4;
      g2d.fillRect(x, mid - capY - 1.5, bw, 1.5);
      g2d.fillRect(x, mid + capY, bw, 1.5);
    }
    g2d.globalAlpha = 1;
    g2d.fillStyle = accent;
    g2d.globalAlpha = 0.25;
    g2d.fillRect(0, mid - 0.5, w, 1); // center axis
    g2d.globalAlpha = 1;
  }

  function roundRect(g2d: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number): void {
    if (typeof g2d.roundRect === 'function') {
      g2d.roundRect(x, y, w, h, r);
      return;
    }
    g2d.moveTo(x + r, y);
    g2d.arcTo(x + w, y, x + w, y + h, r);
    g2d.arcTo(x + w, y + h, x, y + h, r);
    g2d.arcTo(x, y + h, x, y, r);
    g2d.arcTo(x, y, x + w, y, r);
    g2d.closePath();
  }

  $effect(() => {
    const el = audio;
    if (el) graph = ensureGraph(el);
    levels = [];
    peaks = [];
  });

  $effect(() => {
    raf = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(raf);
  });
</script>

<canvas
  bind:this={canvas}
  class="w-full block rounded-lg th-bg-tertiary"
  style="height: {height}px"
  role="img"
  aria-label={label || undefined}
></canvas>
