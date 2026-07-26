import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { AiRuntime, MODEL_CACHE_NAME, DEFAULT_MODEL_URL } from './runtime';

// ─── Mock types ──────────────────────────────────────────────────────────────

interface MockTensor {
  data: Float32Array;
  dims: number[];
  dispose: ReturnType<typeof vi.fn>;
}

interface MockSession {
  run: ReturnType<typeof vi.fn>;
  release: ReturnType<typeof vi.fn>;
  inputNames: string[];
  outputNames: string[];
}

// ─── Mock state ───────────────────────────────────────────────────────────────

let mockSession: MockSession;
let mockOrtModule: {
  InferenceSession: {
    create: ReturnType<typeof vi.fn>;
  };
  Tensor: ReturnType<typeof vi.fn>;
};
let mockCacheStore: Map<string, ArrayBuffer>;
let mockFetchImpl: ReturnType<typeof vi.fn>;

function createMockTensor(data: Float32Array, dims: number[]): MockTensor {
  return {
    data,
    dims,
    dispose: vi.fn(),
  };
}

function createMockSession(inputNames: string[], outputNames: string[]): MockSession {
  return {
    run: vi.fn().mockResolvedValue({
      [outputNames[0]]: createMockTensor(new Float32Array([0.9, 0.1]), [1, 2]),
      [outputNames[1]]: createMockTensor(new Float32Array([1, 0]), [1, 2]),
      [outputNames[2]]: createMockTensor(new Float32Array([0.8, 0.2]), [1, 2]),
    }),
    release: vi.fn().mockResolvedValue(undefined),
    inputNames,
    outputNames,
  };
}

function setupOrtMock(session?: MockSession) {
  const s = session ?? createMockSession(['images'], ['output0', 'output1', 'boxes']);
  mockSession = s;

  mockOrtModule = {
    InferenceSession: {
      create: vi.fn().mockResolvedValue(s),
    },
    Tensor: vi.fn().mockImplementation(function (this: any, data: Float32Array, dims: number[]) {
      this.data = data;
      this.dims = dims;
      this.dispose = vi.fn();
    }),
    // env is accessed by _doInit to set wasmPaths. Provide a minimal mock.
    env: { wasmPaths: undefined as string | undefined },
  };
  // runtime.ts reads ORT from globalThis.ort (_loadOrtUmd fast-path). Keep the
  // stub in sync whenever the mock is rebuilt mid-test.
  if (typeof vi !== 'undefined') {
    vi.stubGlobal('ort', mockOrtModule);
  }
}

function setupCacheMock() {
  mockCacheStore = new Map();

  const mockCache = {
    match: vi.fn().mockImplementation(async (url: string) => {
      const cached = mockCacheStore.get(url);
      if (cached) {
        return new Response(new Blob([cached]), { status: 200 });
      }
      return undefined;
    }),
    put: vi.fn().mockImplementation(async (_url: string, response: Response) => {
      const buf = await response.arrayBuffer();
      mockCacheStore.set(_url, buf);
    }),
    delete: vi.fn().mockImplementation(async (url: string) => {
      return mockCacheStore.delete(url);
    }),
  };

  vi.stubGlobal('caches', { open: vi.fn().mockResolvedValue(mockCache) });
}

function setupFetchWithResponse(modelData: ArrayBuffer) {
  mockFetchImpl = vi
    .fn()
    .mockImplementation(() => Promise.resolve(new Response(modelData.slice(0), { status: 200, statusText: 'OK' })));
  vi.stubGlobal('fetch', mockFetchImpl);
}

function setupFetchWithError(error: Error) {
  mockFetchImpl = vi.fn().mockRejectedValue(error);
  vi.stubGlobal('fetch', mockFetchImpl);
}

function setupFetchWithStatus(status: number, statusText: string) {
  mockFetchImpl = vi.fn().mockResolvedValue(new Response(null, { status, statusText }));
  vi.stubGlobal('fetch', mockFetchImpl);
}

// ─── Test setup / teardown ─────────────────────────────────────────────────────

beforeEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();

  // Default: WebGPU available
  vi.stubGlobal('navigator', {
    gpu: { requestAdapter: vi.fn() },
  });

  setupOrtMock();
  setupCacheMock();

  // Default: fetch returns a valid model response
  setupFetchWithResponse(new ArrayBuffer(1024));

  // runtime.ts loads ORT via _loadOrtUmd(), which reads globalThis.ort (the
  // UMD bundle exposes itself there). Stub it directly so _loadOrtUmd's
  // fast-path resolves without injecting a real <script>.
  vi.stubGlobal('ort', mockOrtModule);
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ─── Tests ─────────────────────────────────────────────────────────────────────

describe('AiRuntime', () => {
  describe('constants', () => {
    it('has correct default model URL', () => {
      expect(DEFAULT_MODEL_URL).toBe('/models/yolo11n.onnx');
    });

    it('has correct cache name', () => {
      expect(MODEL_CACHE_NAME).toBe('mibee-nvr-ai-models');
    });
  });

  describe('constructor', () => {
    it('creates instance without throwing', () => {
      expect(() => new AiRuntime()).not.toThrow();
    });

    it('starts in uninitialized state', () => {
      const rt = new AiRuntime();
      expect(rt.initialized).toBe(false);
    });
  });

  describe('init()', () => {
    it('dynamically imports onnxruntime-web', async () => {
      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');

      expect(mockOrtModule.InferenceSession.create).toHaveBeenCalled();
      expect(rt.initialized).toBe(true);
    });

    it('creates session with WebGPU when available', async () => {
      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');

      expect(mockOrtModule.InferenceSession.create).toHaveBeenCalledWith(
        expect.any(ArrayBuffer),
        expect.objectContaining({
          executionProviders: ['webgpu'],
        }),
      );
    });

    it('falls back to WASM when WebGPU unavailable', async () => {
      vi.stubGlobal('navigator', {});

      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');

      expect(mockOrtModule.InferenceSession.create).toHaveBeenCalledWith(
        expect.any(ArrayBuffer),
        expect.objectContaining({
          executionProviders: ['wasm'],
        }),
      );
    });

    it('caches model after first download', async () => {
      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');

      // Cache should have the model now
      expect(mockCacheStore.has('/models/test.onnx')).toBe(true);

      // Second call — fetch should NOT be called again
      mockFetchImpl.mockClear();
      await rt.init('/models/test.onnx');

      expect(mockFetchImpl).not.toHaveBeenCalled();
    });

    it('calls onProgress during model download', async () => {
      const progressSpy = vi.fn();

      // Build a streaming response with a ReadableStream body
      const chunks = [new Uint8Array(512), new Uint8Array(512)];
      const stream = new ReadableStream({
        async start(controller) {
          for (const chunk of chunks) {
            controller.enqueue(chunk);
          }
          controller.close();
        },
      });

      mockFetchImpl = vi.fn().mockResolvedValue(
        new Response(stream, {
          status: 200,
          statusText: 'OK',
          headers: new Headers({ 'content-length': '1024' }),
        }),
      );
      vi.stubGlobal('fetch', mockFetchImpl);

      const rt = new AiRuntime();
      await rt.init('/models/test.onnx', { onProgress: progressSpy });

      expect(progressSpy).toHaveBeenCalled();
    });

    it('throws on model download failure', async () => {
      setupFetchWithError(new Error('Network error'));

      const rt = new AiRuntime();
      await expect(rt.init('/models/broken.onnx')).rejects.toThrow('Network error');
      expect(rt.initialized).toBe(false);
    });

    it('throws on non-OK HTTP response', async () => {
      setupFetchWithStatus(404, 'Not Found');

      const rt = new AiRuntime();
      await expect(rt.init('/models/missing.onnx')).rejects.toThrow();
      expect(rt.initialized).toBe(false);
    });

    it('throws on session creation failure', async () => {
      mockOrtModule.InferenceSession.create.mockRejectedValue(new Error('Session creation failed'));

      const rt = new AiRuntime();
      await expect(rt.init('/models/test.onnx')).rejects.toThrow('Session creation failed');
      expect(rt.initialized).toBe(false);
    });

    it('releases previous session on re-init', async () => {
      const rt = new AiRuntime();
      await rt.init('/models/test-v1.onnx');
      expect(mockSession.release).not.toHaveBeenCalled();

      await rt.init('/models/test-v2.onnx');
      expect(mockSession.release).toHaveBeenCalledTimes(1);
    });

    // ─── Issue #109: Content-Length integrity + self-heal ───────────────────

    it('rejects a truncated download (Content-Length mismatch)', async () => {
      // Server claims 1024 bytes but the stream closes after 512.
      const stream = new ReadableStream({
        start(controller) {
          controller.enqueue(new Uint8Array(512));
          controller.close();
        },
      });
      mockFetchImpl = vi.fn().mockResolvedValue(
        new Response(stream, {
          status: 200,
          statusText: 'OK',
          headers: new Headers({ 'content-length': '1024' }),
        }),
      );
      vi.stubGlobal('fetch', mockFetchImpl);

      const rt = new AiRuntime();
      await expect(rt.init('/models/test.onnx')).rejects.toThrow(/incomplete/i);
      expect(rt.initialized).toBe(false);
    });

    it('does not cache a truncated download', async () => {
      // Same truncation as above; verify the bad bytes never enter the cache
      // (otherwise it would poison every subsequent load — the #109 root cause).
      const stream = new ReadableStream({
        start(controller) {
          controller.enqueue(new Uint8Array(512));
          controller.close();
        },
      });
      mockFetchImpl = vi.fn().mockResolvedValue(
        new Response(stream, {
          status: 200,
          statusText: 'OK',
          headers: new Headers({ 'content-length': '1024' }),
        }),
      );
      vi.stubGlobal('fetch', mockFetchImpl);

      const rt = new AiRuntime();
      await expect(rt.init('/models/test.onnx')).rejects.toThrow();

      const cache = await caches.open(MODEL_CACHE_NAME);
      expect(cache.put).not.toHaveBeenCalled();
    });

    it('self-heals: purges corrupt cached model and re-fetches on protobuf error', async () => {
      // Seed the cache with a "corrupt" model (wrong bytes).
      const cache = await caches.open(MODEL_CACHE_NAME);
      const corruptBytes = new ArrayBuffer(1024);
      await cache.put('/models/test.onnx', new Response(corruptBytes.slice(0)));
      expect(mockCacheStore.has('/models/test.onnx')).toBe(true);

      // First session.create fails with the protobuf error (corrupt model).
      // Second session.create (after re-fetch) succeeds.
      const goodSession = createMockSession(['images'], ['output0']);
      let createCalls = 0;
      mockOrtModule.InferenceSession.create = vi.fn().mockImplementation(async () => {
        createCalls++;
        if (createCalls === 1) {
          throw new Error("Can't create a session. ERROR_CODE: 7, protobuf parsing failed.");
        }
        return goodSession;
      });

      // Fresh fetch returns a valid (complete) model.
      const goodModel = new ArrayBuffer(2048);
      setupFetchWithResponse(goodModel);

      const rt = new AiRuntime();
      await rt.init('/models/test.onnx'); // should NOT throw — self-healed

      expect(rt.initialized).toBe(true);
      // Cache was purged once, then re-populated with the good model.
      expect(cache.delete).toHaveBeenCalledWith('/models/test.onnx');
      expect(mockFetchImpl).toHaveBeenCalled(); // re-fetched after purge
      expect(mockOrtModule.InferenceSession.create).toHaveBeenCalledTimes(2);
    });

    it('self-heals only once: gives up if the retry also fails', async () => {
      // Every session.create fails with the protobuf error.
      mockOrtModule.InferenceSession.create = vi
        .fn()
        .mockRejectedValue(new Error('ERROR_CODE: 7, protobuf parsing failed'));
      setupFetchWithResponse(new ArrayBuffer(1024));

      const rt = new AiRuntime();
      await expect(rt.init('/models/test.onnx')).rejects.toThrow(/ERROR_CODE: 7|protobuf/i);
      expect(rt.initialized).toBe(false);
      // Exactly 2 create calls: initial + 1 heal attempt (no infinite loop).
      expect(mockOrtModule.InferenceSession.create).toHaveBeenCalledTimes(2);
    });

    it('does NOT self-heal on non-corrupt session errors', async () => {
      // A generic WebGPU/runtime failure must NOT trigger a re-download.
      mockOrtModule.InferenceSession.create = vi
        .fn()
        .mockRejectedValue(new Error('WebGPU adapter unavailable'));
      setupFetchWithResponse(new ArrayBuffer(1024));

      const rt = new AiRuntime();
      await expect(rt.init('/models/test.onnx')).rejects.toThrow('WebGPU adapter unavailable');
      // Only the initial create call — no heal attempt, no second fetch.
      expect(mockOrtModule.InferenceSession.create).toHaveBeenCalledTimes(1);
      const cache = await caches.open(MODEL_CACHE_NAME);
      expect(cache.delete).not.toHaveBeenCalled();
    });
  });

  describe('run()', () => {
    it('creates tensor from input data', async () => {
      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');

      const input = new Float32Array(640 * 640 * 3);
      const dims = [1, 3, 640, 640];
      const results = await rt.run(input, dims);

      expect(mockOrtModule.Tensor).toHaveBeenCalled();
      expect(results).toBeDefined();
    });

    it('passes named feeds to session.run', async () => {
      const s = createMockSession(['images'], ['output0', 'output1', 'boxes']);
      setupOrtMock(s);

      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');

      const input = new Float32Array(10);
      await rt.run(input, [1, 3, 10, 10]);

      expect(s.run).toHaveBeenCalledWith(
        expect.objectContaining({
          images: expect.objectContaining({ data: input }),
        }),
      );
    });

    it('returns named outputs', async () => {
      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');

      const input = new Float32Array(10);
      const results = await rt.run(input, [1, 3, 10, 10]);

      expect(results).toHaveProperty('output0');
      expect(results).toHaveProperty('output1');
      expect(results).toHaveProperty('boxes');
    });

    it('throws if not initialized', async () => {
      const rt = new AiRuntime();
      await expect(rt.run(new Float32Array(10), [1, 3, 10, 10])).rejects.toThrow();
    });

    it('throws on inference timeout', async () => {
      const s = createMockSession(['images'], ['output0']);
      s.run.mockImplementation(
        () => new Promise<never>((_, reject) => setTimeout(() => reject(new Error('Inference timed out')), 100)),
      );
      setupOrtMock(s);

      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');

      await expect(rt.run(new Float32Array(10), [1, 3, 10, 10], { timeoutMs: 50 })).rejects.toThrow();
    });
  });

  describe('dispose()', () => {
    it('releases session', async () => {
      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');
      rt.dispose();

      expect(mockSession.release).toHaveBeenCalled();
      expect(rt.initialized).toBe(false);
    });

    it('is safe to call multiple times', async () => {
      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');
      rt.dispose();
      rt.dispose();

      // release called once (second dispose is no-op)
      expect(mockSession.release).toHaveBeenCalledTimes(1);
    });

    it('is safe to call before init', () => {
      const rt = new AiRuntime();
      expect(() => rt.dispose()).not.toThrow();
    });

    it('aborts pending download on dispose', async () => {
      // Simulate a never-resolving fetch
      mockFetchImpl = vi.fn().mockReturnValue(new Promise(() => {}));
      vi.stubGlobal('fetch', mockFetchImpl);

      const rt = new AiRuntime();
      const initPromise = rt.init('/models/slow.onnx');

      // Give it a tick, then dispose
      await new Promise((r) => setTimeout(r, 0));
      rt.dispose();

      // Key: dispose doesn't hang
      expect(rt.initialized).toBe(false);

      // Clean up — the init promise may eventually reject or hang forever
      initPromise.catch(() => {});
    });
  });

  describe('graphOptimizationLevel', () => {
    it('sets graphOptimizationLevel to all', async () => {
      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');

      expect(mockOrtModule.InferenceSession.create).toHaveBeenCalledWith(
        expect.any(ArrayBuffer),
        expect.objectContaining({
          graphOptimizationLevel: 'all',
        }),
      );
    });
  });

  // onnxruntime-web 1.27's threaded WASM backend loads a sibling
  // ort-wasm-simd-threaded.jsep.{mjs,wasm} pair. Without wasmPaths, ORT resolves
  // them against its bundle URL and (under Vite code-splitting) 404s the .mjs,
  // producing a misleading "protobuf parsing failed" (issue #109).
  describe('wasmPaths', () => {
    it('configures ort.env.wasmPaths to the /ort/ asset dir before session create', async () => {
      const rt = new AiRuntime();
      await rt.init('/models/test.onnx');

      expect(mockOrtModule.env.wasmPaths).toBe('/ort/');
    });
  });
});
