/**
 * Lazy loader for @yume-chan/libde265 WASM module.
 *
 * Singleton pattern — WASM module is loaded once and cached.
 * Must run inside a Web Worker (compute-intensive, blocks thread).
 *
 * Vite note: This package must be added to optimizeDeps.exclude
 * because Vite's dependency optimizer breaks the WASM loading.
 */

import type { MainModule } from '@yume-chan/libde265';

let cachedModule: MainModule | null = null;
let initPromise: Promise<MainModule> | null = null;

/**
 * Initialize and cache the libde265 WASM module.
 * Safe to call multiple times — returns cached instance.
 */
export async function loadLibde265(): Promise<MainModule> {
  if (cachedModule) return cachedModule;
  if (initPromise) return initPromise;

  initPromise = (async () => {
    // Dynamic import so Vite can code-split the WASM module
    const factory = (await import('@yume-chan/libde265')).default;
    const mod = await factory();
    cachedModule = mod;
    return mod;
  })();

  return initPromise;
}

/**
 * Get the cached module — throws if not loaded yet.
 */
export function getLibde265(): MainModule {
  if (!cachedModule) throw new Error('libde265 WASM not loaded — call loadLibde265() first');
  return cachedModule;
}

/**
 * Check if the WASM module has been loaded.
 */
export function isLibde265Loaded(): boolean {
  return cachedModule !== null;
}
