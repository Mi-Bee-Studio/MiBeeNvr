/**
 * Inference Client (#186 scheme 2) — main-thread facade over the shared
 * inference Web Worker.
 *
 * Why a shared single worker: loading an ORT InferenceSession is expensive
 * (model download + parse, ~1s+), and the model is global (one model_url for
 * all cameras). A single worker backing all cameras amortizes that cost and
 * serializes inference naturally (one inference at a time → no main-thread
 * pile-up, complementing the busy-guard + adaptive throttle from #187/#186).
 *
 * Per-camera detector state (EMA smoothing, frame counter, class filter) lives
 * INSIDE the worker, keyed by cameraId — the main thread only sends frames and
 * receives plain Detection[] to paint.
 */

import type { Detection } from './inference';
import type { InferenceWorkerRequest, InferenceWorkerResponse } from './inference-worker';

export interface PerCameraDetectOptions {
  confidenceThreshold: number;
  frameSkip: number;
  emaAlpha?: number;
  maxAge?: number;
  enabledClasses?: string[] | null;
}

interface PendingDetect {
  resolve: (detections: Detection[]) => void;
}

class InferenceClient {
  private worker: Worker | null = null;
  /** Resolves when the worker reports 'ready' (ORT session loaded). */
  private readyPromise: Promise<void> | null = null;
  private readyResolve: (() => void) | null = null;
  private readyReject: ((e: Error) => void) | null = null;
  /** Per-camera pending detect resolver, awaiting a 'detections' response. */
  private pending = new Map<string, PendingDetect>();
  private initError: string | null = null;

  /**
   * Create the shared worker if it doesn't exist yet and wire up its message
   * handler. Does NOT wait for readiness — call waitForReady() / init() for
   * that. Safe to call repeatedly; only the first call creates the worker.
   */
  private ensureWorker(): void {
    if (this.worker) return;
    // Set up the ready promise BEFORE creating the worker so a fast 'ready'
    // message can resolve it.
    this.readyPromise = new Promise<void>((resolve, reject) => {
      this.readyResolve = resolve;
      this.readyReject = reject;
    });
    const w = new Worker(new URL('./inference-worker.ts', import.meta.url), { type: 'module' });
    this.worker = w;
    w.onmessage = (event: MessageEvent<InferenceWorkerResponse>) => this.handleMessage(event.data);
    w.onerror = (e: ErrorEvent) => {
      // Worker crashed/failed to load — reject the pending ready promise.
      this.initError = e.message || 'inference worker failed to load';
      this.readyReject?.(new Error(this.initError));
      this.clearReady();
    };
  }

  private clearReady() {
    this.readyPromise = null;
    this.readyResolve = null;
    this.readyReject = null;
  }

  private handleMessage(msg: InferenceWorkerResponse) {
    switch (msg.type) {
      case 'ready': {
        this.initError = null;
        this.readyResolve?.();
        // Clear the ready handshake state. The already-resolved promise is still
        // awaited by any in-flight init() caller (they hold their own reference),
        // but clearing these fields lets a later init() see the session as live
        // (worker != null && readyPromise == null) and return immediately.
        this.clearReady();
        break;
      }
      case 'init-error': {
        this.initError = msg.error;
        this.readyReject?.(new Error(msg.error));
        this.clearReady();
        // Fail any in-flight detects.
        for (const [, p] of this.pending) p.resolve([]);
        this.pending.clear();
        break;
      }
      case 'registered': {
        // No-op: registration is fire-and-forget.
        break;
      }
      case 'detections': {
        const p = this.pending.get(msg.cameraId);
        if (p) {
          this.pending.delete(msg.cameraId);
          p.resolve(msg.detections);
        }
        break;
      }
      case 'error': {
        if (msg.cameraId) {
          const p = this.pending.get(msg.cameraId);
          if (p) {
            this.pending.delete(msg.cameraId);
            p.resolve([]);
          }
        }
        break;
      }
    }
  }

  private send(msg: InferenceWorkerRequest, transfer?: Transferable[]) {
    if (!this.worker) return;
    this.worker.postMessage(msg, transfer);
  }

  /**
   * Initialize the shared runtime with the backend-configured model URL.
   * Creates the worker, sends 'init', and awaits the worker's 'ready' (ORT
   * session loaded). Safe to call multiple times — a ready session returns
   * immediately.
   */
  async init(modelUrl?: string): Promise<void> {
    if (this.initError) {
      // Allow retry after a previous init failure.
      this.dispose();
    }
    // If already initialized and ready, no-op.
    if (this.worker && !this.readyPromise) return;
    this.ensureWorker();
    if (this.readyPromise) {
      // Send init then wait for 'ready'.
      this.send({ type: 'init', modelUrl, inferenceTimeoutMs: 10000 });
      await this.readyPromise;
    }
  }

  /** Register (or re-register) a camera's detector with the given options. */
  async register(cameraId: string, options: PerCameraDetectOptions): Promise<void> {
    // Ensure the worker exists and the session is ready before registering.
    await this.init();
    if (this.initError) return;
    this.send({ type: 'register', cameraId, options });
  }

  /** Update a camera's options (rebuilds its detector, resetting EMA state). */
  updateOptions(cameraId: string, options: PerCameraDetectOptions): void {
    if (this.initError) return;
    this.send({ type: 'update-options', cameraId, options });
  }

  /**
   * Run detection on a frame for a camera. The frame is TRANSFERRED to the
   * worker (zero-copy); the caller MUST NOT use it after this call.
   * Resolves with Detection[] (possibly empty if the worker is busy, errored,
   * or skipping this frame). Never rejects — detection is non-fatal.
   */
  detect(cameraId: string, frame: VideoFrame): Promise<Detection[]> {
    // If a previous detect for this camera is still in flight, drop this frame
    // (busy guard). Resolve immediately with [] so the caller keeps the last
    // painted boxes.
    if (this.pending.has(cameraId)) {
      try { frame.close(); } catch { /* already closed */ }
      return Promise.resolve([]);
    }
    if (!this.worker || this.initError) {
      try { frame.close(); } catch { /* already closed */ }
      return Promise.resolve([]);
    }
    return new Promise<Detection[]>((resolve) => {
      this.pending.set(cameraId, { resolve });
      this.send({ type: 'detect', cameraId, frame }, [frame]);
    });
  }

  /** Dispose a single camera's detector state. */
  disposeCamera(cameraId: string): void {
    this.pending.delete(cameraId);
    if (this.worker) this.send({ type: 'dispose', cameraId });
  }

  /** Tear down the worker entirely (terminates the shared session). */
  dispose(): void {
    if (this.worker) {
      this.send({ type: 'dispose-all' });
      this.worker.terminate();
      this.worker = null;
    }
    for (const [, p] of this.pending) p.resolve([]);
    this.pending.clear();
    this.clearReady();
    this.initError = null;
  }
}

/**
 * Process-wide singleton. All WasmPlayer instances share one inference worker /
 * one ORT session. Lazily created on first use.
 */
let _client: InferenceClient | null = null;

export function getInferenceClient(): InferenceClient {
  if (!_client) _client = new InferenceClient();
  return _client;
}
