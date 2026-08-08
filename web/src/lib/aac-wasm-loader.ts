/**
 * Lazy loader for the FAAD2-based AAC WASM decoder (@audio/decode-aac).
 *
 * Singleton pattern — the WASM module is loaded once and cached.
 * Must be triggered lazily (only when an AAC stream arrives over plain HTTP,
 * where the WebCodecs AudioDecoder is unavailable) so the ~150KB-gzipped WASM
 * payload is never fetched for G.711-only or HTTPS deployments.
 *
 * Vite note: `@audio/decode-aac` is added to optimizeDeps.exclude in
 * vite.config.ts because its conditional `createRequire`/`import()` of a
 * `.wasm.cjs` chunk confuses the dependency optimizer — same pattern as
 * `@yume-chan/libde265` (see webcodecs-player/libde265-loader.ts).
 */

/** Decoded PCM result shape returned by the FAAD2 decoder. */
export interface FaadDecodeResult {
  channelData: Float32Array[];
  sampleRate: number;
}

/** Streaming FAAD2 decoder instance produced by the WASM module's decoder(). */
export interface FaadDecoder {
  /** Feed ADTS-framed bytes; returns decoded PCM or empty when buffering. */
  decode(data: Uint8Array): FaadDecodeResult;
  /** Signal end of stream; flush any held-over partial frame. */
  flush(): FaadDecodeResult;
  /** Release native (WASM) resources. Safe to call once at teardown. */
  free(): void;
}

/** Factory exposed by @audio/decode-aac; resolved from the dynamic import. */
interface FaadModule {
  /** Create a streaming decoder instance. */
  decoder(): Promise<FaadDecoder>;
}

let cachedModule: FaadModule | null = null;
let initPromise: Promise<FaadModule> | null = null;

/**
 * Initialize and cache the FAAD2 WASM module. Safe to call repeatedly —
 * returns the cached instance. Rejects if the module fails to load.
 */
export function loadFaad(): Promise<FaadModule> {
  if (cachedModule) return Promise.resolve(cachedModule);
  if (initPromise) return initPromise;
  initPromise = (async () => {
    // Dynamic import so Vite code-splits the WASM payload into its own chunk.
    const mod = (await import('@audio/decode-aac')) as unknown as FaadModule;
    cachedModule = mod;
    return mod;
  })();
  return initPromise;
}

/** Whether the FAAD2 WASM module has been loaded. */
export function isFaadLoaded(): boolean {
  return cachedModule !== null;
}
