<script lang="ts">
  import { onDestroy, getContext } from 'svelte';
  import { t } from '$lib/i18n';
  import { Maximize, Minimize, AlertCircle, RefreshCw, Volume2, VolumeX, Volume } from 'lucide-svelte';
  import { getTokenForUrl, API_BASE } from '$lib/api';
  import { sendTelemetry } from '$lib/telemetry';
  import type { StreamState } from '$lib/hls-errors';
  import { getPlaybackTier, detectWebCodecs, detectWebGL2 } from '$lib/webcodecs-player/capabilities';
  import { decodeVideoFrame } from '$lib/webcodecs-player/protocol';
  import { ConnectionManager, type ConnectionState } from '$lib/webcodecs-player/connection';
  import { AudioPlayer } from '$lib/audio-player';
  import type { ReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';
  import { createStateDispatcher } from '$lib/player/dispatch';
import { WebGPURenderer } from '$lib/webgpu-renderer';
  import AiOverlay from './AiOverlay.svelte';
  import { type Detection } from '$lib/ai-detection/inference';
  import { getInferenceClient } from '$lib/ai-detection/inference-client';
  import { getAIZones, getAiStatus, resolveAiSettings, getPerCameraAiSettings, type AiDetectionSettings, type Zone } from '$lib/api/ai';

  let {
    cameraId,
    cameraName,
    codec = 'h264',
    expanded = false,
    tabVisible = true,
    onFallbackNeeded,
  }: {
    cameraId: string;
    cameraName: string;
    /** Camera codec ('h264' | 'h265' | 'mjpeg'). Determines whether the WASM
     *  libde265 fallback path is available on plain HTTP (no WebCodecs). */
    codec?: string;
    expanded?: boolean;
    tabVisible?: boolean;
    onFallbackNeeded?: (fallback: 'hls') => void;
  } = $props();

  // Reconnection coordinator from Dashboard context
  const coordinator = getContext<ReconnectCoordinator | undefined>('reconnect-coordinator');

  type PlayerState = StreamState | 'loading' | 'disconnected' | 'offline';

  let streamState: PlayerState = $state('loading');
  let canvasEl: HTMLCanvasElement | undefined = $state();
  let unsupportedMsg: string | null = $state(null);
  let destroyed = false;

  // WebGL2 rendering
  let gl: WebGL2RenderingContext | null = null;
  let glProgram: WebGLProgram | null = null;
  let glTexture: WebGLTexture | null = null;
  let glVao: WebGLVertexArrayObject | null = null;

  // Dedicated Canvas2D context for the WASM (libde265) H.265 path on plain HTTP.
  // A canvas can only hold ONE context type — if WebGL2 was ever acquired on it
  // (e.g. from a prior protocol session or a probe), getContext('2d') returns
  // null forever. So on the WASM path we lazily create a FRESH 2D context the
  // first time we need it, and reuse it for every frame. This decouples WASM
  // rendering from any WebGL2 state on the main canvas.
  let wasm2d: CanvasRenderingContext2D | null = null;

// WebGPU renderer (tier 1)
let webgpuRenderer: WebGPURenderer | null = null;

  // Connection manager (WebSocket + reconnect + zombie detection)
  let cm: ConnectionManager | null = null;

  // Audio playback
  let audioPlayer: AudioPlayer | null = null;
  let hasAudio = $state(false);
  let audioMuted = $state(true); // Start muted per autoplay policy
  // Mirrors AudioPlayer.unavailableReason so the audio button can render a
  // disabled state + degradation hint when the codec has no decode path.
  let audioUnavailableReason = $state<import('$lib/audio-player').AudioDecodeUnavailableReason | null>(null);

  function audioTooltip(): string {
    switch (audioUnavailableReason) {
      case 'unsupported_codec':
        return t('live.audioUnsupported');
      case 'webcodecs_unavailable':
        return t('live.audioWebCodecsUnavailable');
      case 'decoder_error':
        return t('live.audioDecodeError');
      default:
        return '';
    }
  }

  // Web Worker
  let worker: Worker | null = null;

  // Freeze frame — prevents black flash during reconnection
  let frozenFrameUrl: string | null = $state(null);
  let showFrozenFrame = $state(false);
  let freezeClearTimer: ReturnType<typeof setTimeout> | null = null;
  // Hard no-media timeout: if the WS connects but no video frame arrives within
  // NO_MEDIA_TIMEOUT_MS, give up on this protocol and fall back to HLS. This is
  // a belt-and-suspenders guard on TOP of the ConnectionManager's zombie/handshake
  // caps — simpler and more reliable than counting reconnects. Covers cameras
  // whose WS handshake succeeds (200 OK) but the recorder never feeds media
  // (Xiaomi CS2, some H.265 ONVIF) — the exact scenario that caused the storm.
  let noMediaTimer: ReturnType<typeof setTimeout> | null = null;
  // 30s for the no-media watchdog. Xiaomi CS2 cameras need time to establish
  // the P2P connection before frames flow — 10s was too aggressive and caused
  // premature demotion to HLS (which can't play H.265 in most browsers → black).
  const NO_MEDIA_TIMEOUT_MS = 30000;
  // End-to-end live latency (#469): EMA over (browser now − hub ingest stamp)
  // relayed in each VideoFrame; reported to /api/telemetry every 10s.
  let liveLatencyMs = $state<number | null>(null);
  let lastLatencyReport = 0;

  function trackLiveLatency(ingestAtMs?: number): void {
    if (!ingestAtMs) return;
    const now = Date.now();
    const sample = now - ingestAtMs;
    if (sample < 0 || sample > 60_000) return; // clock-skew / stale-replay guard
    liveLatencyMs = liveLatencyMs == null ? sample : liveLatencyMs * 0.9 + sample * 0.1;
    if (now - lastLatencyReport >= 10_000) {
      lastLatencyReport = now;
      sendTelemetry('live_latency', cameraId, Math.round(liveLatencyMs), { protocol: 'ws' });
    }
  }
  // AI detection overlay state
  let detections: Detection[] = $state([]);
  let aiOverlayVisible = $derived(detections.length > 0);
  let canvasWidth = $state(0);
  let canvasHeight = $state(0);
  // AI detection engine. As of #186 scheme 2, inference runs in a shared Web
  // Worker (see inference-client.ts); this component no longer holds an
  // AiRuntime/ObjectDetector directly — it registers with the client and sends
  // frames. `aiRegistered` tracks whether this camera has a live detector in
  // the worker; `aiAiEnabled` gates the whole path.
  let aiRegistered = false;
  let aiInitializing = $state(false);
  let aiError: string | null = $state(null);
  let aiZones: Zone[] = $state([]);
  let aiEnabled = false;
  // Busy guard (#187): a per-camera in-flight detect is tracked inside the
  // client (inference-client.ts pending map), so this flag now guards against
  // re-entry from the worker.onmessage 'frame' branch before the client even
  // sees the call. Kept as a cheap local short-circuit.
  let aiBusy = false;

  // ─── Zone filtering ──────────────────────────────────────────────

  function pointInPolygon(px: number, py: number, polygon: number[][]): boolean {
    let inside = false;
    for (let i = 0, j = polygon.length - 1; i < polygon.length; j = i++) {
      const xi = polygon[i][0], yi = polygon[i][1];
      const xj = polygon[j][0], yj = polygon[j][1];
      if ((yi > py) !== (yj > py) && px < ((xj - xi) * (py - yi)) / (yj - yi) + xi) {
        inside = !inside;
      }
    }
    return inside;
  }

  function filterDetectionsByZones(detections: Detection[]): Detection[] {
    if (canvasWidth === 0 || canvasHeight === 0) return detections;
    const enabledZones = aiZones.filter((z) => z.camera_id === cameraId && z.enabled);
    if (enabledZones.length === 0) return detections;
    return detections.filter((d) => {
      const cx = (d.bbox[0] + d.bbox[2]) / (2 * canvasWidth);
      const cy = (d.bbox[1] + d.bbox[3]) / (2 * canvasHeight);
      return enabledZones.some((z) => pointInPolygon(cx, cy, z.points));
    });
  }
  // Decode error tracking for mid-stream fallback
  let decodeErrorCount = 0;
  // Raised from 10→50: H.265 WebCodecs decoding can produce intermittent
  // errors (corrupted NALU, profile quirks) without the stream being
  // fundamentally broken. 10 errors in 5s was too aggressive and demoted
  // working WebCodecs streams to HLS (which can't play H.265 reliably).
  // 50 errors in 10s means only a truly broken decoder triggers fallback.
  const MAX_DECODE_ERRORS = 50;
  let decodeErrorTimer: ReturnType<typeof setTimeout> | null = null;

  // ─── Freeze frame helpers ──────────────────────────────────────────────

  function captureFreezeFrame() {
    if (!canvasEl) return;
    if (frozenFrameUrl) URL.revokeObjectURL(frozenFrameUrl);
    try {
      frozenFrameUrl = canvasEl.toDataURL('image/jpeg', 0.8);
      showFrozenFrame = true;
    } catch {
      // canvas may be empty
    }
  }

  function clearFreezeFrame() {
    if (freezeClearTimer) { clearTimeout(freezeClearTimer); freezeClearTimer = null; }
    showFrozenFrame = false;
    freezeClearTimer = setTimeout(() => {
      frozenFrameUrl = null;
      freezeClearTimer = null;
    }, 350);
  }

  // ─── State dispatch ────────────────────────────────────────────────────

  function dispatchStateChange(state: PlayerState) {
    // Routes through the debounced+deduped dispatcher so a burst of
    // connection-state transitions collapses to one event per window
    // (issue #107). 'playing' (recovery) still flushes immediately.
    stateDispatcher.report(state);
  }

  // Per-instance dispatcher. Emits the real CustomEvent on the canvas root;
  // trailing-edge debounce is cleared on destroy.
  const stateDispatcher = createStateDispatcher((state) => {
    const event = new CustomEvent('statechange', {
      bubbles: true,
      detail: { cameraId, state },
    });
    canvasEl?.parentElement?.dispatchEvent(event);
  });

  $effect(() => {
    dispatchStateChange(streamState);
  });

  function updateState(newState: PlayerState) {
    // Capture frame before leaving 'playing'
    if (streamState === 'playing' && newState !== 'playing') {
      captureFreezeFrame();
    }
    // Fade out freeze frame after stream resumes
    if (newState === 'playing' && frozenFrameUrl) {
      clearFreezeFrame();
    }
    // NOTE: no manual dedupe guard — Svelte 5 treats reassigning an identical
    // primitive to a $state as a no-op (no effect re-run). An earlier revision
    // added `if (newState === streamState) return;` but that altered timing in
    // a way that contributed to effect_update_depth_exceeded when combined with
    // the dispatcher. Keep main's behavior; let Svelte dedupe primitives.
    streamState = newState;
  }

  // ─── WebGL2 setup ─────────────────────────────────────────────────────

  function initWebGL2(): boolean {
    if (!canvasEl) return false;

    const ctx = canvasEl.getContext('webgl2', {
      alpha: false,
      antialias: false,
      depth: false,
      stencil: false,
      preserveDrawingBuffer: true, // needed for freeze-frame capture
    });
    if (!ctx) return false;
    gl = ctx;

    // Vertex shader — full-screen quad
    const vsSource = `#version 300 es
      in vec2 aPosition;
      out vec2 vTexCoord;
      void main() {
        // Map [-1,1] quad to [0,1] texture coords
        vTexCoord = aPosition * 0.5 + 0.5;
        gl_Position = vec4(aPosition, 0.0, 1.0);
      }
    `;

    // Fragment shader — sample VideoFrame texture
    const fsSource = `#version 300 es
      precision mediump float;
      in vec2 vTexCoord;
      uniform sampler2D uTexture;
      out vec4 fragColor;
      void main() {
        // Flip Y coordinate (WebGL origin is bottom-left, video is top-left)
        fragColor = texture(uTexture, vec2(vTexCoord.x, 1.0 - vTexCoord.y));
      }
    `;

    const vs = compileShader(gl, gl.VERTEX_SHADER, vsSource);
    const fs = compileShader(gl, gl.FRAGMENT_SHADER, fsSource);
    if (!vs || !fs) return false;

    const program = gl.createProgram()!;
    gl.attachShader(program, vs);
    gl.attachShader(program, fs);
    gl.linkProgram(program);
    if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
      if (import.meta.env.DEV) console.warn('WebGL2 program link failed:', gl.getProgramInfoLog(program));
      return false;
    }
    glProgram = program;

    // Full-screen quad (two triangles)
    const vertices = new Float32Array([
      -1, -1,
       1, -1,
      -1,  1,
      -1,  1,
       1, -1,
       1,  1,
    ]);

    const vao = gl.createVertexArray()!;
    glVao = vao;
    gl.bindVertexArray(vao);

    const vbo = gl.createBuffer()!;
    gl.bindBuffer(gl.ARRAY_BUFFER, vbo);
    gl.bufferData(gl.ARRAY_BUFFER, vertices, gl.STATIC_DRAW);

    const aPos = gl.getAttribLocation(program, 'aPosition');
    gl.enableVertexAttribArray(aPos);
    gl.vertexAttribPointer(aPos, 2, gl.FLOAT, false, 0, 0);

    // Texture for VideoFrame
    glTexture = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, glTexture);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);

    gl.bindVertexArray(null);

    return true;
  }

  function compileShader(
    glCtx: WebGL2RenderingContext,
    type: number,
    source: string,
  ): WebGLShader | null {
    const shader = glCtx.createShader(type);
    if (!shader) return null;
    glCtx.shaderSource(shader, source);
    glCtx.compileShader(shader);
    if (!glCtx.getShaderParameter(shader, glCtx.COMPILE_STATUS)) {
      if (import.meta.env.DEV) console.warn('WebGL2 shader compile failed:', gl.getShaderInfoLog(shader));
      glCtx.deleteShader(shader);
      return null;
    }
    return shader;
  }

  function renderFrame(frame: VideoFrame) {
    if (!gl || !glProgram || !glTexture || !glVao || !canvasEl) return;

    // Resize canvas to match frame if needed
    if (canvasEl.width !== frame.displayWidth || canvasEl.height !== frame.displayHeight) {
      canvasEl.width = frame.displayWidth;
      canvasEl.height = frame.displayHeight;
      canvasWidth = frame.displayWidth;
      canvasHeight = frame.displayHeight;
      gl.viewport(0, 0, canvasEl.width, canvasEl.height);
    }

    gl.useProgram(glProgram);

    // Upload VideoFrame directly to texture — WebGL2 supports VideoFrame in texImage2D
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, glTexture);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, frame);

    // Draw full-screen quad
    gl.bindVertexArray(glVao);
    gl.drawArrays(gl.TRIANGLES, 0, 6);
    gl.bindVertexArray(null);
  }

  function cleanupWebGL2() {
    if (gl) {
      if (glTexture) { gl.deleteTexture(glTexture); glTexture = null; }
      if (glProgram) { gl.deleteProgram(glProgram); glProgram = null; }
      if (glVao) { gl.deleteVertexArray(glVao); glVao = null; }
      gl = null;
    }
  }

  /**
   * Render a raw RGBA frame (WASM libde265 output) via Canvas2D putImageData.
   * Used on plain HTTP where WebCodecs VideoFrame is unavailable — this is the
   * HTTP H.265 playback path.
   *
   * A canvas element can hold only ONE context type for its lifetime. If the
   * main canvas ever acquired a WebGL2 context (prior protocol session, a
   * capability probe, etc.), getContext('2d') permanently returns null. To stay
   * robust against that, we lazily create a DEDICATED offscreen 2D canvas for
   * WASM rendering, then blit it onto whatever context the main canvas currently
   * exposes (2d if available, else WebGL2). This decouples WASM decoding from
   * main-canvas context ownership entirely.
   */
  function renderWasmFrame(frame: { rgba: Uint8Array; width: number; height: number }) {
    if (!canvasEl) return;

    // Lazily create the offscreen 2D canvas for WASM frames (done once).
    if (!wasm2dCanvas) wasm2dCanvas = document.createElement('canvas');
    if (wasm2dCanvas.width !== frame.width || wasm2dCanvas.height !== frame.height) {
      wasm2dCanvas.width = frame.width;
      wasm2dCanvas.height = frame.height;
      canvasWidth = frame.width;
      canvasHeight = frame.height;
    }
    // Draw RGBA → offscreen 2D canvas
    const offCtx = wasm2dCanvas.getContext('2d');
    if (!offCtx) return;
    const imageData = new ImageData(
      new Uint8ClampedArray(frame.rgba.buffer, frame.rgba.byteOffset, frame.rgba.byteLength),
      frame.width,
      frame.height,
    );
    offCtx.putImageData(imageData, 0, 0);

    // Blit onto the main canvas. Prefer its 2D context; if a WebGL2 context
    // owns the canvas, upload the offscreen canvas as a texture instead.
    if (canvasEl.width !== frame.width || canvasEl.height !== frame.height) {
      canvasEl.width = frame.width;
      canvasEl.height = frame.height;
      if (gl) gl.viewport(0, 0, canvasEl.width, canvasEl.height);
    }
    const main2d = canvasEl.getContext('2d');
    if (main2d) {
      main2d.drawImage(wasm2dCanvas, 0, 0);
    } else if (gl && glProgram && glTexture && glVao) {
      // WebGL2 path — reuse the existing program/texture to draw the frame.
      gl.useProgram(glProgram);
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, glTexture);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, wasm2dCanvas);
      gl.bindVertexArray(glVao);
      gl.drawArrays(gl.TRIANGLES, 0, 6);
      gl.bindVertexArray(null);
    }
  }

  // Dedicated offscreen 2D canvas for WASM frame rendering (see renderWasmFrame).
  let wasm2dCanvas: HTMLCanvasElement | null = null;

function handleWebGpuLost() {
  if (!webgpuRenderer) return;
  webgpuRenderer.destroy();
  webgpuRenderer = null;

  // Fallback to WebGL2
  if (!initWebGL2()) {
    streamState = 'error';
    unsupportedMsg = 'WebGPU device lost and WebGL2 init failed';
  }
}
  // ─── Web Worker ────────────────────────────────────────────────────────

  function initWorker(): boolean {
    try {
      worker = new Worker(
        new URL('../lib/webcodecs-player/worker.ts', import.meta.url),
        { type: 'module' },
      );

      worker.onmessage = (event: MessageEvent) => {
        const msg = event.data;
        if (!msg) return;

        if (msg.type === 'frame' && msg.data instanceof VideoFrame) {
          const frame = msg.data;
          // Decoder produced output → the stall watchdog's reconnect tally no
          // longer applies; reset it so a future stall starts fresh.
          cm?.resetDecodeStallCount();
          // Track canvas dimensions for AI overlay
          if (canvasEl) {
            if (canvasWidth !== frame.displayWidth || canvasHeight !== frame.displayHeight) {
              canvasWidth = frame.displayWidth;
              canvasHeight = frame.displayHeight;
            }
          }
          // AI detection — must happen before frame.close()
          if (aiRegistered) {
            processAiDetection(frame);
          }
          if (webgpuRenderer) {
            webgpuRenderer.render(frame); // Takes ownership and closes frame
          } else {
            renderFrame(frame);
            frame.close(); // Memory safety — always close after rendering
          }
          if (streamState !== 'playing') {
            updateState('playing');
          }
        } else if (msg.type === 'wasm-frame' && msg.data) {
          // HTTP H.265 path: raw RGBA from libde265 WASM (no VideoFrame).
          cm?.resetDecodeStallCount();
          renderWasmFrame(msg.data);
          if (streamState !== 'playing') {
            updateState('playing');
          }
        } else if (msg.type === 'error') {
          if (import.meta.env.DEV) console.warn(`WasmPlayer worker error: ${msg.error}`);
          decodeErrorCount++;
          // Reset counter window on each error
          if (decodeErrorTimer) clearTimeout(decodeErrorTimer);
          decodeErrorTimer = setTimeout(() => { decodeErrorCount = 0; }, 10000);
          // Persistent decode errors → fallback to HLS
          if (decodeErrorCount >= MAX_DECODE_ERRORS) {
            if (import.meta.env.DEV) console.warn('[WasmPlayer] Max decode errors reached, falling back to HLS');
            if (decodeErrorTimer) { clearTimeout(decodeErrorTimer); decodeErrorTimer = null; }
            onFallbackNeeded?.('hls');
          }
        } else if (msg.type === 'backpressure') {
          if (cm) {
            cm.setPaused(msg.paused);
          }
        } else if (msg.type === 'codec-ready') {
          // Decoder configured — resume frame delivery (paused in onCodecInfo).
          if (cm) {
            cm.setPaused(false);
          }
        } else if (msg.type === 'decode-stall') {
          // Decoder configured but produced no output within DECODE_STALL_MS
          // (long-GOP camera feeding P-frames with no keyframe). Let the
          // ConnectionManager decide: reconnect (to grab a keyframe on the fresh
          // stream) or, after repeated stalls, report offline so the orchestrator
          // demotes to another protocol.
          sendTelemetry('playback_stall', cameraId, undefined, { protocol: 'ws', kind: 'decode' });
          if (cm) {
            cm.handleDecoderStall();
          }
        }
      };

      worker.onerror = (event: ErrorEvent) => {
        if (import.meta.env.DEV) console.warn('WasmPlayer worker error:', event.message);
      };

      return true;
    } catch (e) {
      if (import.meta.env.DEV) console.warn('Failed to create Web Worker:', e);
      return false;
    }
  }

  function terminateWorker() {
    if (worker) {
      worker.postMessage({ type: 'close' });
      worker.terminate();
      worker = null;
    }
  }

  // ─── Connection management ────────────────────────────────────────────

  function buildWsUrl(): string {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    let url = `${proto}//${location.host}${API_BASE}/cameras/${cameraId}/stream/ws`;
    // ?token= carries the bare session token (mbs_...), NOT a "Bearer ..." header —
    // the backend auth middleware reads ?token= directly. getTokenForUrl returns
    // the token without the "Bearer " prefix.
    const token = getTokenForUrl();
    if (token) {
      url += `?token=${encodeURIComponent(token)}`;
    }
    return url;
  }

  function initConnection() {
    if (cm) return;
    cm = new ConnectionManager({
      url: buildWsUrl(),
      onStateChange: (state: ConnectionState) => {
        updateState(state);
      },
      onCodecInfo: (ci) => {
        if (!worker) return;
        // Pause frame delivery until the worker signals codec-ready. The decoder
        // configure (esp. libde265 WASM init on HTTP) is async; frames arriving
        // before it finishes are silently dropped by the worker. Holding here
        // avoids wasting bandwidth and ensures the next IDR after resume is the
        // first frame decoded (no partial-state black screen).
        cm?.setPaused(true);
        worker.postMessage({
          type: 'codec-info',
          data: {
            codec: ci.codec,
            profile: ci.profile,
            level: ci.level,
            sps: ci.sps,
            pps: ci.pps,
            vps: ci.vps,
          },
        });
      },
      onAudioCodecInfo: (info) => {
        hasAudio = true;
        // Pre-create AudioPlayer (but don't init until user clicks unmute).
        // Pass the codec config (AAC AASC / Opus channel blob) so the decoder
        // backend can be configured when init() runs.
        audioPlayer = new AudioPlayer(info.codec, info.sampleRate, info.channels, info.config);
        audioUnavailableReason = null; // reset; re-evaluated on init()
      },
      onAudioFrame: (frame) => {
        if (audioPlayer?.initialized) {
          audioPlayer.pushFrame(frame.data);
        }
      },
      onFrame: (data: ArrayBuffer) => {
        if (!worker) return;
        // Media is flowing — clear the no-media watchdog.
        if (noMediaTimer) { clearTimeout(noMediaTimer); noMediaTimer = null; }
        try {
          const frame = decodeVideoFrame(data);
          trackLiveLatency(frame.ingestAtMs);
          worker.postMessage({
            type: 'video-frame',
            data: {
              pts: frame.pts,
              isKeyframe: frame.isKeyframe,
              nalus: frame.nalus,
            },
          });
        } catch (e) {
          if (import.meta.env.DEV) console.warn('WasmPlayer: failed to decode VideoFrame:', e);
        }
      },
      onFreezeFrame: () => {
        captureFreezeFrame();
      },
      onCameraOffline: () => {
        // EOS received — connection.ts already set state to 'offline'
        // Stop zombie detection and capture freeze frame
        captureFreezeFrame();
      },
      coordinator: coordinator ?? undefined,
      cameraId,
    });
    cm.connect();
    // Arm the no-media watchdog: if no frame arrives within NO_MEDIA_TIMEOUT_MS
    // after connecting, this protocol can't serve media for this camera → fall
    // back to HLS. Cleared in onFrame/onCodecInfo (media flowing) and on disconnect.
    if (noMediaTimer) clearTimeout(noMediaTimer);
    noMediaTimer = setTimeout(() => {
      noMediaTimer = null;
      if (!destroyed) {
        if (import.meta.env.DEV) console.warn('[WasmPlayer] no media within 10s, falling back to HLS');
        onFallbackNeeded?.('hls');
      }
    }, NO_MEDIA_TIMEOUT_MS);
  }

  function disconnectConnection() {
    if (noMediaTimer) { clearTimeout(noMediaTimer); noMediaTimer = null; }
    if (cm) {
      cm.disconnect();
      cm = null;
    }
    // Clean up audio player
    if (audioPlayer) {
      audioPlayer.destroy();
      audioPlayer = null;
    }
    hasAudio = false;
    audioMuted = true;
  }


  function handleReconnect() {
    captureFreezeFrame();
    disconnectConnection();
    terminateWorker();
    initWorker();
    initConnection();
  }

  // ─── AI Detection ────────────────────────────────────────────────────────

  async function initAiDetection() {
    if (aiRegistered || aiInitializing) return;
    aiInitializing = true;
    aiError = null;
    try {
      // Single source of truth (#182): the backend YAML config is authoritative.
      const settings = await resolveAiSettings();
      if (!settings.enabled) return;

      // Per-camera override (#179): per-camera settings win over global defaults.
      const perCam = getPerCameraAiSettings()[cameraId];
      if (perCam && perCam.enabled === false) {
        return;
      }

      // Fetch the backend-configured model_url (#185 / #109). The shared
      // inference worker loads the model once; all cameras reuse that session.
      let modelUrl: string | undefined;
      try {
        const status = await getAiStatus();
        if (status && typeof status.model_url === 'string' && status.model_url.trim()) {
          modelUrl = status.model_url.trim();
        }
      } catch {
        // Non-fatal: the worker falls back to DEFAULT_MODEL_URL in AiRuntime.init.
      }

      // #186 scheme 2: inference runs in a shared Web Worker. init() loads the
      // ORT session (idempotent across cameras); register() creates this
      // camera's detector with per-camera-overridden options. EMA smoothing,
      // class filtering, and adaptive throttle all live inside the worker.
      const client = getInferenceClient();
      await client.init(modelUrl);
      await client.register(cameraId, {
        confidenceThreshold: perCam?.confidenceThreshold ?? settings.confidenceThreshold,
        frameSkip: perCam?.frameSkip ?? settings.frameSkip,
        emaAlpha: settings.emaAlpha,
        maxAge: settings.maxAge,
        enabledClasses: settings.enabledClasses,
      });
      aiRegistered = true;
      aiEnabled = true;
    } catch (e) {
      // AI is a non-fatal overlay — never abort the video. Common failures are
      // deployment issues (404 on model, corrupt model → ERROR_CODE 7), so log
      // quietly in dev rather than alarming users with a full stack trace.
      const msg = e instanceof Error ? e.message : 'AI init failed';
      const isDeployIssue = /Model download failed: 404|protobuf parsing failed|ERROR_CODE: 7/i.test(msg);
      if (import.meta.env.DEV) {
        if (isDeployIssue) {
          console.info('[WasmPlayer] AI model unavailable (' + msg + ') — AI disabled. Run "mibee-nvr download-model" or re-download a valid model.');
        } else {
          console.warn('[WasmPlayer] AI init failed:', e);
        }
      }
      aiError = msg;
      aiRegistered = false;
    } finally {
      aiInitializing = false;
    }

    // Fetch zones for filtering (non-fatal)
    try {
      const data = await getAIZones();
      aiZones = data.zones || [];
    } catch {
      // Zones are non-critical
    }
  }

  async function processAiDetection(frame: VideoFrame) {
    // Gating order matters: visibility/playing checks run BEFORE the busy guard
    // so a frame arriving while hidden doesn't block the next visible inference.

    // Visibility gate (#187): when the tab is hidden, skip inference entirely.
    // The decode worker still posts frames (no <video> to auto-pause), but
    // running ONNX in a background tab burns CPU and is the direct cause of
    // "switching pages feels laggy". CRITICAL: only skip AI here — never touch
    // the WS connection/protocol (the old visibility $effect that did caused a
    // reconnect storm).
    if (document.hidden) return;
    // Playing-state gate: don't infer before the stream is live.
    if (streamState !== 'playing') return;
    if (!aiRegistered || !aiEnabled) return;
    // Busy guard: the inference client ALSO guards per-camera in-flight detects,
    // but this local flag short-circuits before the clone cost.
    if (aiBusy) return;

    aiBusy = true;
    try {
      // Clone the frame — the original is consumed/closed by the renderer below.
      // The clone is TRANSFERRED to the inference worker (zero-copy); the worker
      // owns and closes it after inference. This is the same clone that happened
      // on the main thread before #186 scheme 2; only its destination changed.
      const cloned = new VideoFrame(frame);
      const client = getInferenceClient();
      const newDetections = await client.detect(cameraId, cloned);
      // cloned is closed inside the worker; do NOT close it here (it was
      // transferred and is no longer accessible on the main thread).
      detections = filterDetectionsByZones(newDetections);
    } catch (e) {
      // Non-fatal — keep showing last detections
      if (import.meta.env.DEV) console.warn('[WasmPlayer] AI detection error:', e);
    } finally {
      aiBusy = false;
    }
  }


  // ─── Tier detection ────────────────────────────────────────────────────

  function checkTier(): string | null {
    const tier = getPlaybackTier();
    if (tier === 'tier3') {
      // No WebCodecs (plain HTTP, non-localhost). The WASM libde265 decoder can
      // still decode H.265 via Canvas2D putImageData — allow it for H.265 so
      // HTTP environments aren't forced to HLS (which can't play H.265 either).
      // H.264 on tier3 has no working decode path (needs WebCodecs), so fall back.
      if (codec !== 'h265') {
        onFallbackNeeded?.('hls');
        return null; // Don't set error state — parent will switch protocol
      }
      // H.265 + tier3: proceed with WASM-only rendering (Canvas2D). WebGL2/WebGPU
      // are unavailable here, but Canvas2D putImageData needs no special context.
      return null;
    }
    if (!detectWebGL2()) {
      return 'WebGL2 is required for rendering';
    }
    return null;
  }

  // ─── Main lifecycle ────────────────────────────────────────────────────

  $effect(() => {
    const _id = cameraId;
    if (!_id) return;

    // Tier detection at mount
    const msg = checkTier();
    if (msg) {
      unsupportedMsg = msg;
      streamState = 'error';
      return;
    }
    // If checkTier() triggered fallback, stop initialization.
    // On tier3 (no WebCodecs) for H.265, we proceed — the WASM libde265 path
    // renders via Canvas2D (no WebGL2/WebGPU needed). For all other tier3
    // cases checkTier already triggered the HLS fallback above.
    if (!detectWebCodecs() && codec !== 'h265') {
      return;
    }
    unsupportedMsg = null;

    // Initialize renderer + Worker + WebSocket
    let cancelled = false;
    const timer = setTimeout(async () => {
      if (destroyed || cancelled) return;

      // Try WebGPU first for tier 1
      if (canvasEl) {
        const tier = getPlaybackTier();
        if (import.meta.env.DEV) console.log(`[WasmPlayer] tier=${tier}, canvas=${!!canvasEl}`);
        if (tier === 'tier1') {
          const wgpuRenderer = new WebGPURenderer(() => {
            handleWebGpuLost();
          });
          if (destroyed || cancelled) { wgpuRenderer.destroy(); return; }

          const wgpuOk = await wgpuRenderer.init(canvasEl);
          if (destroyed || cancelled) { wgpuRenderer.destroy(); return; }

          if (wgpuOk) {
            if (import.meta.env.DEV) console.log('[WasmPlayer] WebGPU init success');
            webgpuRenderer = wgpuRenderer;
          } else {
            if (import.meta.env.DEV) console.warn('[WasmPlayer] WebGPU init failed, falling back to WebGL2');
            wgpuRenderer.destroy();
          }
        }
      }

      // WebGL2 initialization. We initialize WebGL2 for ALL paths where it's
      // available, including the tier3 + H.265 (WASM libde265) path: the WASM
      // decoder produces RGBA, which we blit onto the main canvas via an
      // offscreen 2D canvas. If the main canvas has WebGL2, we upload the frame
      // as a texture (GPU blit); if it only has 2D, we drawImage. Either way the
      // main canvas needs a context, so try WebGL2 first (best quality/scaling)
      // and let renderWasmFrame adapt to whatever context the canvas exposes.
      const isTier3Wasm = getPlaybackTier() === 'tier3' && codec === 'h265';
      if (!webgpuRenderer) {
        if (!initWebGL2()) {
          // WebGL2 unavailable — on the WASM path this is fine (renderWasmFrame
          // falls back to a 2D context). For WebCodecs paths WebGL2 is required.
          if (!isTier3Wasm) {
            if (import.meta.env.DEV) console.error('[WasmPlayer] WebGL2 init failed');
            unsupportedMsg = 'Failed to initialize WebGL2';
            streamState = 'error';
            return;
          }
          if (import.meta.env.DEV) console.warn('[WasmPlayer] WebGL2 init failed on WASM path — using 2D blit');
        }
      }

      if (!initWorker()) {
        unsupportedMsg = 'Failed to initialize Web Worker';
        streamState = 'error';
        return;
      }

      initConnection();
      initAiDetection();
    }, 50);

    return () => {
      cancelled = true;
      clearTimeout(timer);
      if (webgpuRenderer) {
        webgpuRenderer.destroy();
        webgpuRenderer = null;
      }
      disconnectConnection();
      terminateWorker();
      cleanupWebGL2();
      // Release this camera's detector in the shared inference worker (#187/#186).
      // The shared worker + ORT session persist for other cameras; only this
      // camera's per-camera detector state is dropped.
      if (aiRegistered) {
        getInferenceClient().disposeCamera(cameraId);
        aiRegistered = false;
      }
      aiBusy = false;
    };
  });

  // Visibility: REMOVED. There used to be a $effect here that read `tabVisible`
  // and called cm.disconnect()/cm.connect() on every visibility change. But
  // that created a multi-way conflict:
  //   1. this effect (tabVisible prop → cm.disconnect/connect)
  //   2. ConnectionManager's own _bindVisibility (document.visibilitychange)
  //   3. the Player Orchestrator's setTabVisible (attemptUpgrade on all cameras)
  // All three fired on the same visibilitychange event, each closing/reopening
  // the WS or switching protocols → reactive loop → "closed before established"
  // storm + console freeze. Visibility is now owned SOLELY by the orchestrator
  // (Surveillance.svelte's visibilityHandler calls orchestrator.setTabVisible,
  // which pauses/resumes via protocol-level decisions, not per-player WS
  // toggling). ConnectionManager._bindVisibility was also removed. Do NOT
  // re-add per-player visibility handling.

  // ─── Cleanup ───────────────────────────────────────────────────────────

onDestroy(() => {
    destroyed = true;
    if (freezeClearTimer) { clearTimeout(freezeClearTimer); freezeClearTimer = null; }
    if (decodeErrorTimer) { clearTimeout(decodeErrorTimer); decodeErrorTimer = null; }
    if (noMediaTimer) { clearTimeout(noMediaTimer); noMediaTimer = null; }
    stateDispatcher.dispose();
    if (frozenFrameUrl) { URL.revokeObjectURL(frozenFrameUrl); frozenFrameUrl = null; }
    if (webgpuRenderer) { webgpuRenderer.destroy(); webgpuRenderer = null; }
    if (cm) { cm.destroy(); cm = null; }
    if (coordinator) coordinator.cancelRequest(cameraId);
    terminateWorker();
    cleanupWebGL2();
    // Final safety net: release this camera's worker detector if the $effect
    // cleanup didn't run (idempotent — disposeCamera is safe to call twice).
    if (aiRegistered) {
      getInferenceClient().disposeCamera(cameraId);
      aiRegistered = false;
    }
  });

  // ─── Derived ───────────────────────────────────────────────────────────

  let showOverlay = $derived(
    streamState === 'loading' || streamState === 'error' || streamState === 'buffering' || streamState === 'disconnected' || streamState === 'offline',
  );
  let overlayClass = $derived(
    streamState === 'loading'
      ? 'opacity-100'
      : streamState === 'error'
        ? 'opacity-100'
        : streamState === 'offline'
          ? 'opacity-100'
          : streamState === 'buffering'
            ? 'opacity-60'
            : streamState === 'disconnected'
              ? 'opacity-60'
              : 'opacity-0 pointer-events-none',
  );

  let dotColor = $derived(
    streamState === 'playing'
      ? 'bg-green-500'
      : streamState === 'buffering'
        ? 'bg-yellow-500 animate-pulse'
        : streamState === 'error'
          ? 'bg-red-500'
          : streamState === 'offline'
            ? 'bg-orange-500'
            : streamState === 'disconnected'
              ? 'bg-gray-500'
              : 'bg-gray-400',
  );
  let dotTitle = $derived(
    streamState === 'playing'
      ? t('dashboard.live')
      : streamState === 'buffering'
        ? t('dashboard.buffering')
        : streamState === 'error'
          ? t('dashboard.errorState')
          : streamState === 'offline'
            ? t('dashboard.cameraOffline')
            : streamState === 'disconnected'
              ? t('live.webrtc.disconnected')
              : t('dashboard.snapshotMode'),
  );
</script>

<!-- svelte-ignore binding_property_non_reactive -->
<div class="relative w-full h-full bg-black overflow-hidden group">
  <!-- Freeze frame — last good frame shown during reconnection -->
  {#if frozenFrameUrl}
    <img
      src={frozenFrameUrl}
      alt=""
      class="absolute inset-0 w-full h-full object-contain transition-opacity duration-300 {showFrozenFrame ? 'opacity-100' : 'opacity-0 pointer-events-none'}"
      aria-hidden="true"
    />
  {/if}

  <!-- WebGL2 canvas -->
  <canvas
    bind:this={canvasEl}
    class="w-full h-full object-contain"
    aria-label="{cameraName} — {dotTitle}"
  ></canvas>

  <!-- AI detection overlay -->
  <AiOverlay {detections} visible={aiOverlayVisible} width={canvasWidth} height={canvasHeight} />
  <!-- Overlay layer with CSS transition -->
  <div
    class="absolute inset-0 flex items-center justify-center transition-opacity duration-200 {overlayClass}"
  >
    {#if unsupportedMsg}
      <!-- Unsupported browser -->
      <div class="absolute inset-0 bg-black/70"></div>
      <div class="relative flex flex-col items-center gap-3 z-10">
        <AlertCircle size={28} class="text-red-400" />
        <span class="text-white/70 text-xs text-center px-4">{unsupportedMsg}</span>
      </div>
    {:else if streamState === 'loading'}
      <!-- Shimmer loading animation -->
      <div class="absolute inset-0 overflow-hidden">
        <div
          class="absolute inset-0"
          style="background: linear-gradient(90deg, transparent 0%, rgba(255,255,255,0.04) 40%, rgba(255,255,255,0.08) 50%, rgba(255,255,255,0.04) 60%, transparent 100%); background-size: 200% 100%; animation: shimmer 1.8s ease-in-out infinite;"
        ></div>
      </div>
    {:else if streamState === 'error'}
      <!-- Error overlay -->
      <div class="absolute inset-0 bg-black/70"></div>
      <div class="relative flex flex-col items-center gap-3 z-10">
        <AlertCircle size={28} class="text-red-400" />
        <span class="text-white/70 text-xs">{t('live.streamErrorRetries')}</span>
        <button
          onclick={handleReconnect}
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-white/10 text-white/80 text-xs hover:bg-white/20 transition-colors"
        >
          <RefreshCw size={12} />
          {t('common.retry')}
        </button>
      </div>
    {:else if streamState === 'offline'}
      <!-- Camera offline overlay -->
      <div class="absolute inset-0 bg-black/70"></div>
      <div class="relative flex flex-col items-center gap-3 z-10">
        <AlertCircle size={28} class="text-orange-400" />
        <span class="text-white/70 text-xs">{t('dashboard.cameraOffline')}</span>
        <button
          onclick={() => cm?.reconnect()}
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-white/10 text-white/80 text-xs hover:bg-white/20 transition-colors"
        >
          <RefreshCw size={12} />
          {t('common.retry')}
        </button>
      </div>
    {:else if streamState === 'buffering' || streamState === 'disconnected'}

      <!-- Semi-transparent buffering — small indicator, don't fully block video -->
      <div class="relative flex items-center gap-2">
        <div class="w-3 h-3 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
        <span class="text-white/50 text-xs">{t('live.loading')}</span>
      </div>
    {/if}
  </div>

  <!-- Stream state indicator dot (top-left) -->
  <span
    class="absolute top-2 left-2 w-2 h-2 {dotColor} rounded-full z-10"
    title={dotTitle}
  ></span>

  <!-- Camera name + status bar (bottom) -->
  <div
    class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2 z-10"
  >
    <div class="flex items-center gap-2">
      <span class="text-white text-sm font-medium truncate">{cameraName || cameraId}</span>
      <span class="text-white/50 text-xs">WebCodecs</span>
      {#if liveLatencyMs != null}
        <span
          class="text-xs tabular-nums {liveLatencyMs > 3000
            ? 'text-red-400/80'
            : liveLatencyMs > 1000
              ? 'text-yellow-400/80'
              : 'text-green-400/70'}"
          title={t('flow.liveLatency')}
        >
          {(liveLatencyMs / 1000).toFixed(1)}s
        </span>
      {/if}
      {#if aiInitializing}
        <span class="text-yellow-400/70 text-xs flex items-center gap-1">
          <span class="w-1.5 h-1.5 rounded-full bg-yellow-400 animate-pulse"></span>
          {t('settings.ai.loading')}
        </span>
      {:else if aiError}
        <span class="text-red-400/70 text-xs" title={aiError}>AI ✗</span>
      {:else if aiRegistered}
        <span class="text-green-400/70 text-xs flex items-center gap-1">
          <span class="w-1.5 h-1.5 rounded-full bg-green-400"></span>
          {t('settings.ai.ready')}
        </span>
      {/if}
    </div>
  </div>

  <!-- Expand/Shrink button (top-right) -->
  <!-- Audio mute/unmute button (only shown when audio is available) -->
  {#if hasAudio}
    <button
      onclick={async (e: MouseEvent) => {
        e.stopPropagation();
        if (!audioPlayer) return;
        // If this codec has no usable decode path (e.g. AAC/Opus over plain
        // HTTP without WebCodecs/WASM), do nothing — the tooltip explains why.
        if (audioUnavailableReason) return;
        if (!audioPlayer.initialized) {
          await audioPlayer.init();
          audioUnavailableReason = audioPlayer.unavailableReason ?? null;
          if (audioUnavailableReason) return;
        }
        audioMuted = !audioMuted;
        audioPlayer.setMuted(audioMuted);
      }}
      disabled={!!audioUnavailableReason}
      class="absolute top-2 right-{expanded ? '10' : '10'} p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all z-10"
      title={audioUnavailableReason ? audioTooltip() : audioMuted ? t('live.unmute') || 'Unmute' : t('live.mute') || 'Mute'}
    >
      {#if audioUnavailableReason}
        <Volume size={16} class="opacity-50" />
      {:else if audioMuted}
        <VolumeX size={16} />
      {:else}
        <Volume2 size={16} />
      {/if}
    </button>
  {/if}

  {#if expanded}
    <button
      onclick={(e: MouseEvent) => {
        e.stopPropagation();
        canvasEl?.parentElement?.dispatchEvent(new CustomEvent('shrink', { bubbles: true, detail: { cameraId } }));
      }}
      class="absolute top-2 right-2 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all z-10"
      title={t('dashboard.backToGrid')}
    >
      <Minimize size={16} />
    </button>
  {:else}
    <button
      onclick={(e: MouseEvent) => {
        e.stopPropagation();
        canvasEl?.parentElement?.dispatchEvent(new CustomEvent('expand', { bubbles: true, detail: { cameraId } }));
      }}
      class="absolute top-2 right-2 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all opacity-0 group-hover:opacity-100 z-10"
      title={t('dashboard.fullscreen')}
    >
      <Maximize size={16} />
    </button>
  {/if}
</div>

<style>
  @keyframes shimmer {
    0% {
      background-position: -200% 0;
    }
    100% {
      background-position: 200% 0;
    }
  }
</style>
