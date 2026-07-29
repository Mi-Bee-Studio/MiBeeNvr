/**
 * AI Inference Web Worker (#186 scheme 2)
 *
 * Moves ONNX inference OFF the main thread. The main thread sends a cloned
 * VideoFrame (transferable, zero-copy); this worker runs preprocess + ORT +
 * postprocess and returns plain serializable Detection[] data. EMA smoothing
 * and class filtering stay here, keyed per cameraId, so the main thread only
 * paints boxes.
 *
 * Why a worker: ORT's WASM backend runs compute on the calling thread. On the
 * main thread that blocks rendering → UI stutter (#187). On edge devices
 * (RPi/Banana Pi WASM SIMD ≈100ms/infer) the pile-up froze the whole page.
 * This worker keeps that compute off the render loop. NOTE: ORT is still
 * single-threaded here (no COOP/COEP → no SharedArrayBuffer → numThreads=1),
 * so this does NOT make inference faster — it just stops it from freezing the
 * UI. The adaptive throttle (#186 scheme 1, still active) handles raw speed.
 *
 * ORT is loaded via dynamic `import('/ort/ort.all.bundle.min.mjs')` — the
 * all-backends ESM bundle already copied to dist/ort/ by ortAssetsPlugin. This
 * sidesteps two traps: (1) `document` is undefined in a worker so the main-
 * thread `_loadOrtUmd()` (<script> injection) cannot run here; (2) importing
 * `onnxruntime-web` through Vite triggers the code-splitting bug that produces
 * INVALID_PROTOBUF / ERROR_CODE 7 (issue #109). The bundle build inlines the
 * wasm-JS glue, so the sibling ort-wasm-simd-threaded.jsep.{mjs,wasm} pair at
 * /ort/ is resolved correctly.
 */

/// <reference lib="webworker" />

import { AiRuntime } from './runtime';
import { ObjectDetector, type Detection } from './inference';

// ─── Message protocol (main thread ↔ worker) ─────────────────────────────────

export type InferenceWorkerRequest =
  | {
      type: 'init';
      modelUrl?: string;
      inferenceTimeoutMs?: number;
    }
  | {
      type: 'register';
      cameraId: string;
      options: {
        confidenceThreshold: number;
        frameSkip: number;
        emaAlpha?: number;
        maxAge?: number;
        enabledClasses?: string[] | null;
      };
    }
  | {
      type: 'update-options';
      cameraId: string;
      options: {
        confidenceThreshold: number;
        frameSkip: number;
        emaAlpha?: number;
        maxAge?: number;
        enabledClasses?: string[] | null;
      };
    }
  | {
      type: 'detect';
      cameraId: string;
      /** A cloned VideoFrame, transferred (zero-copy) from the main thread. */
      frame: VideoFrame;
    }
  | { type: 'dispose'; cameraId: string }
  | { type: 'dispose-all' };

export type InferenceWorkerResponse =
  | { type: 'ready' }
  | { type: 'init-error'; error: string }
  | { type: 'detections'; cameraId: string; detections: Detection[] }
  | { type: 'registered'; cameraId: string }
  | { type: 'error'; cameraId?: string; error: string };

// ─── Worker state ─────────────────────────────────────────────────────────────

let runtime: AiRuntime | null = null;
let initPromise: Promise<void> | null = null;
/** Per-camera detector instances (EMA smoothing state is per-camera). */
const detectors = new Map<string, ObjectDetector>();
/** Frame counter per camera so frameSkip is applied inside the worker. */
const frameCounts = new Map<string, number>();

function post(msg: InferenceWorkerResponse, transfer?: Transferable[]) {
  (self as unknown as Worker).postMessage(msg, transfer);
}

async function ensureRuntime(modelUrl?: string, inferenceTimeoutMs?: number): Promise<void> {
  if (runtime) return;
  if (initPromise) return initPromise;
  initPromise = (async () => {
    const rt = new AiRuntime();
    await rt.init(modelUrl, { inferenceTimeoutMs });
    runtime = rt;
  })();
  try {
    await initPromise;
  } finally {
    initPromise = null;
  }
}

function ensureDetector(
  cameraId: string,
  options: {
    confidenceThreshold: number;
    frameSkip: number;
    emaAlpha?: number;
    maxAge?: number;
    enabledClasses?: string[] | null;
  },
): ObjectDetector {
  if (!runtime) throw new Error('runtime not initialized');
  let det = detectors.get(cameraId);
  if (!det) {
    det = new ObjectDetector(runtime, options);
    detectors.set(cameraId, det);
    frameCounts.set(cameraId, 0);
  }
  return det;
}

async function handleDetect(cameraId: string, frame: VideoFrame): Promise<void> {
  const det = detectors.get(cameraId);
  if (!det) {
    // Unknown camera — close the frame and ignore.
    try { frame.close(); } catch { /* already closed */ }
    return;
  }
  // Apply frameSkip inside the worker (mirrors ObjectDetector's internal skip,
  // but we also drop the frame early to save a preprocess when skipping).
  const count = (frameCounts.get(cameraId) ?? 0) + 1;
  frameCounts.set(cameraId, count);
  try {
    const detections = await det.detect(frame);
    post({ type: 'detections', cameraId, detections });
  } catch (e) {
    post({ type: 'error', cameraId, error: e instanceof Error ? e.message : String(e) });
  } finally {
    // The detector closes the frame internally on the detect path, but be
    // defensive: a thrown error could leave it open.
    try { frame.close(); } catch { /* already closed */ }
  }
}

// ─── Message handler ──────────────────────────────────────────────────────────

self.onmessage = async (event: MessageEvent<InferenceWorkerRequest>) => {
  const msg = event.data;
  if (!msg) return;
  try {
    switch (msg.type) {
      case 'init': {
        await ensureRuntime(msg.modelUrl, msg.inferenceTimeoutMs);
        post({ type: 'ready' });
        break;
      }
      case 'register': {
        await ensureRuntime();
        ensureDetector(msg.cameraId, msg.options);
        post({ type: 'registered', cameraId: msg.cameraId });
        break;
      }
      case 'update-options': {
        // Rebuild the detector with new options (preserves no state worth
        // keeping — a confidence/class change should reset EMA smoothing).
        const old = detectors.get(msg.cameraId);
        if (old) old.dispose();
        detectors.delete(msg.cameraId);
        frameCounts.delete(msg.cameraId);
        if (runtime) ensureDetector(msg.cameraId, msg.options);
        break;
      }
      case 'detect': {
        await handleDetect(msg.cameraId, msg.frame);
        break;
      }
      case 'dispose': {
        const d = detectors.get(msg.cameraId);
        if (d) d.dispose();
        detectors.delete(msg.cameraId);
        frameCounts.delete(msg.cameraId);
        break;
      }
      case 'dispose-all': {
        for (const d of detectors.values()) d.dispose();
        detectors.clear();
        frameCounts.clear();
        break;
      }
    }
  } catch (e) {
    const errMsg = e instanceof Error ? e.message : String(e);
    if (msg && (msg.type === 'init' || msg.type === 'register')) {
      post({ type: 'init-error', error: errMsg });
    } else {
      post({ type: 'error', cameraId: msg && 'cameraId' in msg ? msg.cameraId : undefined, error: errMsg });
    }
  }
};
