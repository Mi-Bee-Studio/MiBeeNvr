/**
 * ONNX Runtime Web Integration — AiRuntime
 *
 * Dynamic import of onnxruntime-web with:
 * - WebGPU backend (preferred) → WASM SIMD fallback
 * - Cache API for model files (avoids re-download)
 * - Progress callback for model download
 * - Inference with named input/output tensors
 *
 * TDD: tested via runtime.test.ts with mocked onnxruntime-web.
 */

/** Cache API store name for AI model files. */
export const MODEL_CACHE_NAME = 'mibee-nvr-ai-models';

/** Default YOLOv11-nano model path (served from Go HTTP server). */
export const DEFAULT_MODEL_URL = '/models/yolo11n.onnx';

/** Session options type matching onnxruntime-web. */
interface SessionOptions {
  executionProviders: string[];
  graphOptimizationLevel: string;
}

/** Init options. */
export interface AiRuntimeInitOptions {
  /** Progress callback: (loaded, total). Total may be 0 if unknown. */
  onProgress?: (loaded: number, total: number) => void;
  /** Inference timeout in ms (default: 5000). */
  inferenceTimeoutMs?: number;
}

/** Run options. */
export interface AiRunOptions {
  /** Override inference timeout for this specific run. */
  timeoutMs?: number;
}

/** Result map from session.run(). */
export interface AiRunResult {
  [name: string]: {
    data: Float32Array;
    dims: number[];
    dispose: () => void;
  };
}

/**
 * Check if WebGPU is available.
 * Extracted as a function so tests can override it.
 */
function detectWebGPUBackend(): boolean {
  try {
    return typeof navigator !== 'undefined' && (navigator as any).gpu !== undefined;
  } catch {
    return false;
  }
}

/**
 * AI Runtime — lazy-loads onnxruntime-web, manages model download + caching + session.
 */
export class AiRuntime {
  private _session: any = null; // ort.InferenceSession (any to avoid importing ort at module level)
  private _ort: any = null;
  private _initialized = false;
  private _initPromise: Promise<void> | null = null;
  private _abortController: AbortController | null = null;
  private _inferenceTimeoutMs = 5000;

  /** Whether the runtime has been initialized and is ready for inference. */
  get initialized(): boolean {
    return this._initialized;
  }

  /**
   * Initialize the AI runtime — downloads model (with cache), loads onnxruntime-web,
   * creates inference session.
   *
   * @param modelUrl URL to .onnx model file (served from Go backend)
   * @param options Optional progress callback and inference timeout
   */
  async init(modelUrl: string = DEFAULT_MODEL_URL, options?: AiRuntimeInitOptions): Promise<void> {
    // Guard against concurrent init calls
    if (this._initPromise) {
      return this._initPromise;
    }

    this._inferenceTimeoutMs = options?.inferenceTimeoutMs ?? 5000;

    this._initPromise = this._doInit(modelUrl, options);
    try {
      await this._initPromise;
    } finally {
      this._initPromise = null;
    }
  }

  /**
   * Validate model URL against whitelist and SSRF rules.
   * - Relative URLs (same-origin) are allowed.
   * - Only HTTPS with whitelisted domains (github.com, raw.githubusercontent.com) allowed.
   * - Private/internal IPs are blocked.
   */
  private _validateModelUrl(url: string): void {
    // Allow relative URLs (no scheme) — same-origin, served by this app
    if (!url.startsWith('http://') && !url.startsWith('https://')) {
      return;
    }

    // Reject plain HTTP
    if (url.startsWith('http://')) {
      throw new Error(`Model URL rejected: HTTP not allowed. Use HTTPS: ${url}`);
    }

    // Parse hostname
    let hostname: string;
    try {
      hostname = new URL(url).hostname;
    } catch {
      throw new Error(`Model URL rejected: unable to parse: ${url}`);
    }

    // Whitelisted domains (exact or subdomain match)
    const allowedDomains = ['github.com', 'raw.githubusercontent.com'];
    for (const domain of allowedDomains) {
      if (hostname === domain || hostname.endsWith('.' + domain)) {
        return;
      }
    }

    // Block private/internal IPs (SSRF prevention)
    const ipv4Match = hostname.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
    if (ipv4Match) {
      const parts = ipv4Match.slice(1).map(Number);
      // 127.0.0.0/8 (loopback)
      if (parts[0] === 127) {
        throw new Error(`Model URL rejected: loopback IP not allowed: ${url}`);
      }
      // 169.254.0.0/16 (link-local)
      if (parts[0] === 169 && parts[1] === 254) {
        throw new Error(`Model URL rejected: link-local IP not allowed: ${url}`);
      }
      // 10.0.0.0/8
      if (parts[0] === 10) {
        throw new Error(`Model URL rejected: private IP (10.x.x.x) not allowed: ${url}`);
      }
      // 192.168.0.0/16
      if (parts[0] === 192 && parts[1] === 168) {
        throw new Error(`Model URL rejected: private IP (192.168.x.x) not allowed: ${url}`);
      }
      // 172.16.0.0/12
      if (parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) {
        throw new Error(`Model URL rejected: private IP (172.16.x.x-172.31.x.x) not allowed: ${url}`);
      }
    }

    // Non-whitelisted external domain
    throw new Error(
      `Model URL rejected: domain not in whitelist: ${hostname}. Allowed: github.com, raw.githubusercontent.com`,
    );
  }

  private async _doInit(modelUrl: string, options?: AiRuntimeInitOptions): Promise<void> {
    // Validate URL before loading
    this._validateModelUrl(modelUrl);

    // 1. Download model (with cache)
    this._abortController = new AbortController();
    const modelBuffer = await this._loadModel(modelUrl, options?.onProgress);
    this._abortController = null;

    // 2. Dynamic import onnxruntime-web (lazy, NOT in initial bundle)
    this._ort = await import('onnxruntime-web');

    // 3. Dispose previous session if re-initializing
    if (this._session) {
      try {
        await this._session.release();
      } catch {
        // Ignore errors on old session release
      }
      this._session = null;
    }

    // 4. Determine execution provider
    const executionProviders = detectWebGPUBackend() ? ['webgpu'] : ['wasm'];

    // 5. Create session
    const sessionOptions: SessionOptions = {
      executionProviders,
      graphOptimizationLevel: 'all',
    };

    try {
      this._session = await this._ort.InferenceSession.create(modelBuffer, sessionOptions);
    } catch (e) {
      // Self-heal once: a cached/truncated model produces "protobuf parsing
      // failed" / ERROR_CODE 7 from ORT (issue #109). Purge the Cache API entry
      // and re-fetch a fresh copy, then retry session creation exactly once.
      // Without this, a single bad download poisons the cache forever and the
      // user can never recover without manually clearing site data.
      if (!this._isCorruptModelError(e)) {
        throw e;
      }
      const healed = await this._healModel(modelUrl, sessionOptions, options?.onProgress);
      if (!healed) {
        throw e;
      }
      // _healModel assigned this._session on success.
    }
    this._initialized = true;
  }

  /**
   * Heuristically detect whether an ORT session-creation error indicates a
   * corrupt/truncated model (vs. a genuine runtime/WebGPU failure we can't fix
   * by re-downloading). Used to gate the self-heal path.
   */
  private _isCorruptModelError(e: unknown): boolean {
    const msg = e instanceof Error ? e.message : String(e);
    return /protobuf parsing failed|ERROR_CODE:?\s*7|Model download incomplete|invalid model/i.test(msg);
  }

  /**
   * Self-heal: purge any cached copy of the model, re-fetch it fresh (with the
   * Content-Length integrity check), and retry session creation once.
   * Returns true if a valid session was created; false if the retry also failed
   * (caller throws the original error).
   */
  private async _healModel(
    modelUrl: string,
    sessionOptions: SessionOptions,
    onProgress?: (loaded: number, total: number) => void,
  ): Promise<boolean> {
    try {
      await this._purgeCachedModel(modelUrl);
    } catch {
      // Cache purge failure is non-fatal — proceed to re-fetch anyway.
    }
    let freshBuffer: ArrayBuffer;
    try {
      this._abortController = new AbortController();
      freshBuffer = await this._loadModel(modelUrl, onProgress);
      this._abortController = null;
    } catch {
      return false;
    }
    try {
      this._session = await this._ort.InferenceSession.create(freshBuffer, sessionOptions);
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Remove a model URL from the Cache API store. Used by the self-heal path to
   * discard a corrupt/truncated cached copy before re-fetching.
   */
  private async _purgeCachedModel(modelUrl: string): Promise<void> {
    try {
      const cache = await caches.open(MODEL_CACHE_NAME);
      await cache.delete(modelUrl);
    } catch {
      // Cache unavailable — nothing to purge.
    }
  }

  /**
   * Load model from Cache API or fetch from network.
   * Caches the response for subsequent loads.
   */
  private async _loadModel(
    modelUrl: string,
    onProgress?: (loaded: number, total: number) => void,
  ): Promise<ArrayBuffer> {
    // Check cache first
    try {
      const cache = await caches.open(MODEL_CACHE_NAME);
      const cached = await cache.match(modelUrl);
      if (cached) {
        return await cached.arrayBuffer();
      }
    } catch {
      // Cache unavailable (e.g. private browsing) — fall through to fetch
    }

    // Fetch from network
    const response = await fetch(modelUrl, { signal: this._abortController?.signal });

    if (!response.ok) {
      throw new Error(`Model download failed: ${response.status} ${response.statusText}`);
    }

    // Expected total from Content-Length. When the server advertises a size, we
    // STRICTLY verify the bytes received match — a truncated transfer (the root
    // cause of issue #109's "protobuf parsing failed") must never be cached or
    // handed to ORT. Content-Length = 0 means "unknown" (chunked encoding) → we
    // can't verify and accept whatever arrives.
    const expectedTotal = parseInt(response.headers.get('content-length') || '0', 10);

    // Track progress if streaming body available
    let modelBuffer: ArrayBuffer;
    if (onProgress && response.body) {
      modelBuffer = await this._readWithProgress(response, expectedTotal, onProgress);
    } else {
      modelBuffer = await response.arrayBuffer();
    }

    // Strict integrity gate: if the server told us the size and we got fewer
    // bytes, the transfer was truncated. Reject rather than caching a corrupt
    // model that would poison every subsequent load (issue #109).
    if (expectedTotal > 0 && modelBuffer.byteLength !== expectedTotal) {
      throw new Error(
        `Model download incomplete: got ${modelBuffer.byteLength} bytes, expected ${expectedTotal} (truncated transfer)`,
      );
    }

    // Store in cache (clone the response). Only reached after the integrity
    // check passes, so a corrupt model can never enter the cache.
    try {
      const cache = await caches.open(MODEL_CACHE_NAME);
      const cloned = new Response(modelBuffer.slice(0));
      await cache.put(modelUrl, cloned);
    } catch {
      // Cache write failure is non-fatal
    }

    return modelBuffer;
  }

  /**
   * Read response body with progress tracking.
   * Falls back to arrayBuffer() if streaming is not available.
   */
  private async _readWithProgress(
    response: Response,
    total: number,
    onProgress: (loaded: number, total: number) => void,
  ): Promise<ArrayBuffer> {
    if (!response.body) {
      const buf = await response.arrayBuffer();
      onProgress(buf.byteLength, total);
      return buf;
    }

    const reader = response.body.getReader();
    const chunks: Uint8Array[] = [];
    let loaded = 0;

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      chunks.push(value);
      loaded += value.byteLength;
      onProgress(loaded, total);
    }

    // Combine chunks into single ArrayBuffer
    const result = new Uint8Array(loaded);
    let offset = 0;
    for (const chunk of chunks) {
      result.set(chunk, offset);
      offset += chunk.byteLength;
    }

    return result.buffer as ArrayBuffer;
  }

  /**
   * Run inference on the model.
   *
   * @param inputData Float32Array of input tensor data
   * @param dims Tensor dimensions (e.g. [1, 3, 640, 640] for YOLO)
   * @param options Optional per-run timeout override
   * @returns Map of output name → { data, dims, dispose }
   */
  async run(inputData: Float32Array, dims: number[], options?: AiRunOptions): Promise<AiRunResult> {
    if (!this._initialized || !this._session) {
      throw new Error('AiRuntime not initialized — call init() first');
    }

    const inputName = this._session.inputNames[0];
    const tensor = new this._ort.Tensor(inputData, dims);

    const feeds: Record<string, any> = {
      [inputName]: tensor,
    };

    const timeout = options?.timeoutMs ?? this._inferenceTimeoutMs;

    const result = await Promise.race([
      this._session.run(feeds),
      new Promise<never>((_, reject) =>
        setTimeout(() => reject(new Error(`Inference timed out after ${timeout}ms`)), timeout),
      ),
    ]);

    return result;
  }

  /**
   * Dispose the runtime — releases session, aborts pending downloads.
   * Safe to call multiple times and before init().
   */
  dispose(): void {
    if (this._abortController) {
      this._abortController.abort();
      this._abortController = null;
    }

    if (this._session) {
      this._session.release().catch(() => {});
      this._session = null;
    }

    this._initialized = false;
    this._ort = null;
  }
}
