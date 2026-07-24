<script lang="ts">
  import { onDestroy, getContext } from 'svelte';
  import { t } from '$lib/i18n';
  import { Maximize, Minimize, AlertCircle, RefreshCw, Volume2, VolumeX } from 'lucide-svelte';
  import { getAuthHeader } from '$lib/api';
  import type { StreamState } from '$lib/hls-errors';
  import { getPlaybackTier, detectWebCodecs, detectWebGL2 } from '$lib/webcodecs-player/capabilities';
  import { decodeVideoFrame } from '$lib/webcodecs-player/protocol';
  import { ConnectionManager, type ConnectionState } from '$lib/webcodecs-player/connection';
  import { AudioPlayer } from '$lib/audio-player';
  import type { ReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';
import { WebGPURenderer } from '$lib/webgpu-renderer';
  import AiOverlay from './AiOverlay.svelte';
  import { AiRuntime } from '$lib/ai-detection/runtime';
  import { ObjectDetector, type Detection } from '$lib/ai-detection/inference';
  import { getAiSettings, getAIZones, type AiDetectionSettings, type Zone } from '$lib/api/ai';

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

  // Web Worker
  let worker: Worker | null = null;

  // Freeze frame — prevents black flash during reconnection
  let frozenFrameUrl: string | null = $state(null);
  let showFrozenFrame = $state(false);
  let freezeClearTimer: ReturnType<typeof setTimeout> | null = null;
  // AI detection overlay state
  let detections: Detection[] = $state([]);
  let aiOverlayVisible = $derived(detections.length > 0);
  let canvasWidth = $state(0);
  let canvasHeight = $state(0);
  // AI detection engine
  let aiRuntime: AiRuntime | null = null;
  let aiDetector: ObjectDetector | null = null;
  let aiInitializing = $state(false);
  let aiError: string | null = $state(null);
  let aiZones: Zone[] = $state([]);

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
  const MAX_DECODE_ERRORS = 10;
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
    const event = new CustomEvent('statechange', {
      bubbles: true,
      detail: { cameraId, state },
    });
    canvasEl?.parentElement?.dispatchEvent(event);
  }

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
          // Track canvas dimensions for AI overlay
          if (canvasEl) {
            if (canvasWidth !== frame.displayWidth || canvasHeight !== frame.displayHeight) {
              canvasWidth = frame.displayWidth;
              canvasHeight = frame.displayHeight;
            }
          }
          // AI detection — must happen before frame.close()
          if (aiDetector) {
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
          renderWasmFrame(msg.data);
          if (streamState !== 'playing') {
            updateState('playing');
          }
        } else if (msg.type === 'error') {
          if (import.meta.env.DEV) console.warn(`WasmPlayer worker error: ${msg.error}`);
          decodeErrorCount++;
          // Reset counter window on each error
          if (decodeErrorTimer) clearTimeout(decodeErrorTimer);
          decodeErrorTimer = setTimeout(() => { decodeErrorCount = 0; }, 5000);
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
    let url = `${proto}//${location.host}/api/cameras/${cameraId}/stream/ws`;
    const authHeader = getAuthHeader();
    if (authHeader) {
      const token = authHeader.startsWith('Basic ') ? authHeader.slice(6) : authHeader;
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
        // Pre-create AudioPlayer (but don't init until user clicks unmute)
        audioPlayer = new AudioPlayer(info.codec, info.sampleRate, info.channels);
      },
      onAudioFrame: (frame) => {
        if (audioPlayer?.initialized) {
          audioPlayer.pushFrame(frame.data);
        }
      },
      onFrame: (data: ArrayBuffer) => {
        if (!worker) return;
        try {
          const frame = decodeVideoFrame(data);
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
  }

  function disconnectConnection() {
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
    if (aiDetector || aiInitializing) return;
    aiInitializing = true;
    aiError = null;
    try {
      const settings = getAiSettings();
      if (!settings.enabled) return;

      aiRuntime = new AiRuntime();
      await aiRuntime.init(undefined, {
        inferenceTimeoutMs: 10000, // Higher for edge devices
      });

      aiDetector = new ObjectDetector(aiRuntime, {
        confidenceThreshold: settings.confidenceThreshold,
        frameSkip: settings.frameSkip,
      });
    } catch (e) {
      console.warn('[WasmPlayer] AI init failed:', e);
      aiError = e instanceof Error ? e.message : 'AI init failed';
      aiRuntime?.dispose();
      aiRuntime = null;
      aiDetector = null;
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
    if (!aiDetector) return;
    try {
      // Clone frame because detect() is async and frame may be closed
      const cloned = new VideoFrame(frame);
      const newDetections = await aiDetector.detect(cloned);
      cloned.close();
      detections = filterDetectionsByZones(newDetections);
    } catch (e) {
      // Non-fatal — keep showing last detections
      if (import.meta.env.DEV) console.warn('[WasmPlayer] AI detection error:', e);
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
    };
  });

  // Visibility: pause when the tab is hidden, resume when visible. This effect
  // MUST depend ONLY on `tabVisible` — never on `streamState`. The previous
  // version read `streamState !== 'loading'` too, and since cm.connect() sets
  // state to 'loading' internally, the effect re-ran in a loop: connect→loading
  // →(state change to buffering)→effect re-run→connect again→"WebSocket closed
  // before the connection is established" storm (92k+ log lines). The
  // ConnectionManager.connect() is now idempotent (no-op if already
  // OPEN/CONNECTING), so it's safe to call on every visibility transition
  // without re-checking streamState.
  $effect(() => {
    const visible = tabVisible;
    if (destroyed || !cm) return;
    if (!visible) {
      cm.disconnect();
    } else {
      captureFreezeFrame();
      cm.connect();
    }
  });

  // ─── Cleanup ───────────────────────────────────────────────────────────

onDestroy(() => {
    destroyed = true;
    if (freezeClearTimer) { clearTimeout(freezeClearTimer); freezeClearTimer = null; }
    if (decodeErrorTimer) { clearTimeout(decodeErrorTimer); decodeErrorTimer = null; }
    if (frozenFrameUrl) { URL.revokeObjectURL(frozenFrameUrl); frozenFrameUrl = null; }
    if (webgpuRenderer) { webgpuRenderer.destroy(); webgpuRenderer = null; }
    if (cm) { cm.destroy(); cm = null; }
    if (coordinator) coordinator.cancelRequest(cameraId);
    terminateWorker();
    cleanupWebGL2();
    if (aiDetector) { aiDetector.dispose(); aiDetector = null; }
    if (aiRuntime) { aiRuntime.dispose(); aiRuntime = null; }
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
            ? 'Camera Offline'
            : streamState === 'disconnected'
              ? t('live.webrtc.connecting') || 'Disconnected'
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
        <span class="text-white/70 text-xs">Camera Offline</span>
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
      {#if aiInitializing}
        <span class="text-yellow-400/70 text-xs flex items-center gap-1">
          <span class="w-1.5 h-1.5 rounded-full bg-yellow-400 animate-pulse"></span>
          {t('settings.ai.loading')}
        </span>
      {:else if aiError}
        <span class="text-red-400/70 text-xs" title={aiError}>AI ✗</span>
      {:else if aiDetector}
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
        if (!audioPlayer.initialized) {
          await audioPlayer.init();
        }
        audioMuted = !audioMuted;
        audioPlayer.setMuted(audioMuted);
      }}
      class="absolute top-2 right-{expanded ? '10' : '10'} p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all z-10"
      title={audioMuted ? t('live.unmute') || 'Unmute' : t('live.mute') || 'Mute'}
    >
      {#if audioMuted}
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
