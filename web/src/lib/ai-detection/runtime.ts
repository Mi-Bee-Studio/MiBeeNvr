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

import { APP_BASE, withBase } from '$lib/base-path';

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

  /**
   * Lazily load the onnxruntime-web UMD bundle from /ort.min.js (served by
   * ortAssetsPlugin) and return its module. The loaded module is cached on
   * globalThis.ort by the UMD bundle itself, so repeated calls are cheap.
   *
   * Why a UMD script and not `import('onnxruntime-web')`: Vite code-splits the
   * ESM entry into a hashed vendor chunk whose internal `import.meta.url`-based
   * wasm/worker path resolution breaks onnxruntime-web 1.27's backend init,
   * producing a misleading INVALID_PROTOBUF (ERROR_CODE 7) on model load
   * (issue #109). Loading the stock UMD bundle via a plain <script> tag keeps
   * ORT's own asset resolution intact, and pointing `env.wasmPaths='/ort/'`
   * (set in _doInit) locates the sibling .mjs/.wasm pair.
   */
  private static _ortUmdPromise: Promise<any> | null = null;
  private _loadOrtUmd(): Promise<any> {
    // Fast path: ORT already loaded (covers repeat calls, the unit-test stub,
    // and a worker that already imported the bundle). globalThis.ort is set by
    // both the UMD bundle (main thread) and the ESM bundle (worker import).
    const w = globalThis as any;
    if (w.ort) return Promise.resolve(w.ort);
    if (AiRuntime._ortUmdPromise) return AiRuntime._ortUmdPromise;

    // Worker path (#186 scheme 2): a Web Worker has no `document`, so the
    // <script>-injection path below cannot run. Use a dynamic ESM import of the
    // all-backends bundle instead — it's already copied to /ort/ by
    // ortAssetsPlugin (vite.config.js). The .bundle. build inlines the wasm-JS
    // glue, sidestepping the separate ort-wasm-simd-threaded.jsep.{mjs,wasm}
    // resolution that Vite code-splitting breaks (issue #109). After import the
    // module's default export is the ORT namespace; also mirror it to
    // globalThis.ort so subsequent fast-path calls and nested workers resolve it.
    const isWorker =
      typeof document === 'undefined' && typeof (self as any).importScripts !== 'undefined';

    if (isWorker) {
      AiRuntime._ortUmdPromise = (async () => {
        // Build the URL via a variable so Vite's static import-analysis cannot
        // see the literal path (it only exists at runtime after ortAssetsPlugin
        // copies it into dist/ort/ — not during tests). `@vite-ignore` alone does
        // not stop the analyzer; a non-literal specifier does.
        const bundleUrl = APP_BASE + '/ort/ort.all.bundle.min.mjs';
        const mod: any = await import(/* @vite-ignore */ bundleUrl);
        const ort = mod?.default ?? mod;
        if (!ort) throw new Error('ort.all.bundle.min.mjs imported but module export is undefined');
        w.ort = ort;
        return ort;
      })();
      return AiRuntime._ortUmdPromise;
    }

    // Main-thread path: load the UMD bundle via a <script> tag (served at
    // /ort.min.js by ortAssetsPlugin). NOT via Vite's bundled
    // `import('onnxruntime-web')` — Vite's code-splitting breaks ORT 1.27's
    // internal wasm/worker path resolution (issue #109, INVALID_PROTOBUF).
    AiRuntime._ortUmdPromise = new Promise<any>((resolve, reject) => {
      const script = document.createElement('script');
      script.src = APP_BASE + '/ort.min.js';
      script.async = true;
      script.onload = () => {
        const ort = (globalThis as any).ort;
        if (ort) {
          resolve(ort);
        } else {
          AiRuntime._ortUmdPromise = null;
          reject(new Error('ort.min.js loaded but window.ort is undefined'));
        }
      };
      script.onerror = () => {
        AiRuntime._ortUmdPromise = null;
        reject(new Error('Failed to load /ort.min.js — run `make build` to embed the ORT UMD bundle'));
      };
      document.head.appendChild(script);
    });
    return AiRuntime._ortUmdPromise;
  }

  private async _doInit(modelUrl: string, options?: AiRuntimeInitOptions): Promise<void> {
    // Validate URL before loading
    this._validateModelUrl(modelUrl);

    // 1. Download model (with cache)
    this._abortController = new AbortController();
    const modelBuffer = await this._loadModel(modelUrl, options?.onProgress);
    this._abortController = null;

    // 2. Load onnxruntime-web via the stock UMD bundle (served at /ort.min.js),
    // NOT via Vite's bundled `import('onnxruntime-web')`. Vite's code-splitting
    // breaks ORT 1.27's internal wasm/worker path resolution: the bundled chunk
    // resolves the .wasm via its own hashed URL and skips the .mjs worker, then
    // fails model parsing with a misleading INVALID_PROTOBUF (ERROR_CODE 7) —
    // issue #109. The stock UMD bundle, loaded fresh and pointed at /ort/ via
    // wasmPaths, initializes the WASM backend correctly. Verified on Banana Pi
    // M5 via an isolated test page using this exact UMD + wasmPaths setup.
    this._ort = await this._loadOrtUmd();

    // Point ORT at /ort/ where ortAssetsPlugin (vite.config.js) serves the
    // sibling ort-wasm-simd-threaded.jsep.{mjs,wasm} pair.
    //
    // IMPORTANT: the path is `env.wasm.wasmPaths` (nested under the `wasm`
    // sub-object), NOT `env.wasmPaths`. ORT's wasm-factory.ts reads
    // `flags.wasmPaths` where `flags` is `env.wasm` (passed as
    // `initializeWebAssembly(env.wasm)` in proxy-wrapper.ts). Setting the
    // top-level `env.wasmPaths` is a silent no-op — ORT keeps using
    // `document.currentScript.src`'s directory (root, since ort.min.js is
    // served at /ort.min.js) to resolve the dynamically-imported
    // `ort-wasm-simd-threaded.jsep.mjs`, producing
    // "TypeError: Failed to fetch dynamically imported module
    // http://host/ort-wasm-simd-threaded.jsep.mjs" (root, not /ort/).
    if (this._ort.env) {
      // Ensure the `wasm` sub-object exists; some mocks omit it.
      if (!this._ort.env.wasm) this._ort.env.wasm = {};
      this._ort.env.wasm.wasmPaths = `${APP_BASE}/ort/`;
      // Single-threaded: crossOriginIsolated is false on our deployment (no
      // COOP/COEP headers), so SharedArrayBuffer is unavailable. ORT detects
      // this and falls back anyway, but set it explicitly so the proxy worker
      // doesn't attempt a thread-pool init that can't work.
      this._ort.env.wasm.numThreads = 1;
    }

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
    // Root-absolute in-app paths (default "/models/…", or the value from
    // /api/ai/status) must carry the runtime base path so the fetch hits the
    // NVR through the proxy/gateway origin instead of the embedder (#394).
    if (modelUrl.startsWith('/')) {
      modelUrl = withBase(modelUrl);
    }

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

    // Fetch from network. cache: 'no-store' forces a full, unconditional GET
    // (no If-None-Match). Without this, the browser's HTTP cache serves a 304
    // when the ETag matches, and in some SW/Cache-API combinations the 304's
    // empty body leaks through to arrayBuffer() → ORT gets 0 bytes →
    // "protobuf parsing failed" (issue #109). We have our own integrity-gated
    // Cache API layer above, so bypassing the browser HTTP cache here is safe
    // and correct.
    const response = await fetch(modelUrl, {
      signal: this._abortController?.signal,
      cache: 'no-store',
    });

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
