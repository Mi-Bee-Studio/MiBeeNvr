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

/** Prefix a root-absolute in-app path with the runtime base. */
export function withBase(path: string): string {
  return APP_BASE + path;
}
