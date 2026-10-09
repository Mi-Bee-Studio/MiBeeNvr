/**
 * Runtime base path for reverse-proxy / unified-gateway deployments (#394).
 *
 * When the NVR is served under a URL prefix (fnOS unified gateway:
 * "/app/mibee-nvr"), the backend injects `window.__NVR_BASE__` into
 * index.html. Every absolute in-app URL (API calls, stream endpoints, ORT
 * assets, service worker) must be prefixed with it, because the browser's
 * origin is the proxy (e.g. the NAS web UI), not the NVR itself.
 *
 * Served normally at "/", APP_BASE is "" and all URLs are unchanged.
 */
export const APP_BASE: string =
  (typeof window !== 'undefined' && (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__) || '';

/**
 * Web Workers have no `window`, so APP_BASE reads '' inside them and every
 * root-absolute asset URL (ORT bundle, wasm paths, model files) would lose
 * the gateway prefix (#942). The main thread forwards the base with the
 * inference worker's init message and registers it here before anything
 * fetches; until then the worker behaves like a no-prefix deployment.
 */
let workerBase = '';

/**
 * Register the runtime base for Web Worker contexts. Only accepts root-absolute
 * prefixes — anything else falls back to '' (no prefix) rather than trusting a
 * malformed value.
 */
export function setWorkerBase(base: string | undefined): void {
  workerBase = typeof base === 'string' && base.startsWith('/') ? base : '';
}

/** The base that applies in the current context (window bootstrap or worker registration). */
export function activeBase(): string {
  if (typeof window !== 'undefined') {
    // Read live rather than baking in the module-load APP_BASE: same value in
    // production (the bootstrap script runs before any module loads) and
    // straightforward to exercise in tests.
    return (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__ || '';
  }
  return workerBase;
}

/** Prefix a root-absolute in-app path with the runtime base. */
export function withBase(path: string): string {
  return activeBase() + path;
}
