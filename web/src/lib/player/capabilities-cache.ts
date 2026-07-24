/**
 * Cached device-capability snapshot for the PlayerOrchestrator.
 *
 * Capability detection (WebCodecs, WebGPU, WebGL2, MSE H.265, WASM H.265) is
 * fast individually but synchronous and some of it touches the GPU context.
 * Running it on every camera cell — or worse, in a reactive `$effect` — is
 * wasteful and can cause the kind of reactive loop that produced the WS storm.
 *
 * This module probes ONCE per page session, caches the result in
 * `sessionStorage` (so a tab refresh within the same session reuses it), and
 * hands every consumer the same {@link DeviceCaps} object. The
 * PlayerOrchestrator reads from here; `stream-selection.ts` is given the
 * `BrowserCaps` subset it needs.
 */

import {
  detectWebCodecs,
  detectHEVC,
  detectMSEH265,
  detectWebGPU,
  detectWebGL2,
  detectWasmH265,
} from '$lib/webcodecs-player/capabilities';

/**
 * The full set of browser capabilities the orchestrator cares about.
 * `BrowserCaps` (stream-selection.ts) is the subset that influences protocol
 * selection; this is the superset that also drives render-tier choice.
 */
export interface DeviceCaps {
  /** WebCodecs VideoDecoder present (HTTPS/localhost only). */
  webCodecs: boolean;
  /** WebCodecs can hardware-decode HEVC/H.265 (async-probed, best-effort). */
  hevcDecode: boolean;
  /** WebGPU adapter present (best render path). */
  webgpu: boolean;
  /** WebGL2 context acquirable (good render path). */
  webgl2: boolean;
  /** MSE can decode H.265 — gates FLV/HLS black-screen risk. */
  mseH265: boolean;
  /** libde265 WASM soft-decoder usable (H.265 on plain HTTP). */
  wasmH265: boolean;
  /** Epoch millis when this snapshot was taken. */
  probedAt: number;
}

const STORAGE_KEY = 'mibee_nvr_device_caps';
/** Re-probe if the cached snapshot is older than this (avoids stale caps after browser updates). */
const MAX_CACHE_AGE_MS = 1000 * 60 * 60; // 1 hour

/** The most-conservative caps (assume nothing works) — used before first probe. */
export const EMPTY_CAPS: DeviceCaps = {
  webCodecs: false,
  hevcDecode: false,
  webgpu: false,
  webgl2: false,
  mseH265: false,
  wasmH265: false,
  probedAt: 0,
};

let inMemory: DeviceCaps | null = null;

/**
 * Synchronously return the cached caps, or {@link EMPTY_CAPS} if not yet
 * probed. Safe to call in render paths — it never triggers detection.
 */
export function getCaps(): DeviceCaps {
  if (inMemory) return inMemory;
  // Best-effort restore from sessionStorage so a refresh doesn't re-probe
  // (and re-touch the GPU) before the async probe completes.
  const stored = readStored();
  if (stored) {
    inMemory = stored;
    return stored;
  }
  return EMPTY_CAPS;
}

/**
 * Probe device capabilities and cache the result. Runs the HEVC async probe;
 * all others are synchronous. Safe to call multiple times — concurrent callers
 * share the same in-flight probe.
 */
let probingPromise: Promise<DeviceCaps> | null = null;

export async function probeCaps(force = false): Promise<DeviceCaps> {
  if (!force && inMemory && Date.now() - inMemory.probedAt < MAX_CACHE_AGE_MS) {
    return inMemory;
  }
  if (probingPromise) return probingPromise;

  probingPromise = (async () => {
    const webCodecs = detectWebCodecs();
    // HEVC decode probe only makes sense when WebCodecs itself is available;
    // detectHEVC already short-circuits, but skip the await entirely otherwise.
    const hevcDecode = webCodecs ? await detectHEVC() : false;
    const caps: DeviceCaps = {
      webCodecs,
      hevcDecode,
      webgpu: detectWebGPU(),
      webgl2: detectWebGL2(),
      mseH265: detectMSEH265(),
      wasmH265: detectWasmH265(),
      probedAt: Date.now(),
    };
    inMemory = caps;
    writeStored(caps);
    probingPromise = null;
    return caps;
  })();

  return probingPromise;
}

/** Invalidate the cache (e.g. after a WebGPU device-loss that changes render path). */
export function invalidateCaps(): void {
  inMemory = null;
  try {
    sessionStorage.removeItem(STORAGE_KEY);
  } catch {
    /* sessionStorage unavailable (private mode) — in-memory cache only */
  }
}

// ─── sessionStorage helpers ──────────────────────────────────────────────────

function readStored(): DeviceCaps | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<DeviceCaps>;
    if (typeof parsed.probedAt !== 'number') return null;
    if (Date.now() - parsed.probedAt > MAX_CACHE_AGE_MS) return null;
    return { ...EMPTY_CAPS, ...parsed } as DeviceCaps;
  } catch {
    return null;
  }
}

function writeStored(caps: DeviceCaps): void {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(caps));
  } catch {
    /* sessionStorage full or unavailable — in-memory cache only */
  }
}
