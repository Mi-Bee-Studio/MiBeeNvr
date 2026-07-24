import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  detectWebCodecs,
  detectHEVC,
  detectMSEH265,
  detectWebGPU,
  detectWebGL2,
  detectOffscreenCanvas,
  getPlaybackTier,
  type PlaybackTier,
} from './capabilities';

beforeEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ---------------------------------------------------------------------------
// detectWebCodecs
// ---------------------------------------------------------------------------
describe('detectWebCodecs', () => {
  it('should return true when VideoDecoder is available', () => {
    vi.stubGlobal('VideoDecoder', class Mock {});
    expect(detectWebCodecs()).toBe(true);
  });

  it('should return false when VideoDecoder is not available', () => {
    vi.stubGlobal('VideoDecoder', undefined);
    expect(detectWebCodecs()).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// detectHEVC
// ---------------------------------------------------------------------------
describe('detectHEVC', () => {
  it('should return true when isConfigSupported returns supported:true', async () => {
    const mockIsConfigSupported = vi.fn().mockResolvedValue({ supported: true });
    vi.stubGlobal('VideoDecoder', { isConfigSupported: mockIsConfigSupported });
    expect(await detectHEVC()).toBe(true);
    expect(mockIsConfigSupported).toHaveBeenCalledWith({
      codec: 'hvc1.1.6.L93.B0',
      codedWidth: 1920,
      codedHeight: 1080,
    });
  });

  it('should return false when isConfigSupported returns supported:false', async () => {
    vi.stubGlobal('VideoDecoder', {
      isConfigSupported: vi.fn().mockResolvedValue({ supported: false }),
    });
    expect(await detectHEVC()).toBe(false);
  });

  it('should return false when VideoDecoder is undefined', async () => {
    vi.stubGlobal('VideoDecoder', undefined);
    expect(await detectHEVC()).toBe(false);
  });

  it('should return false when VideoDecoder is not a proper object', async () => {
    vi.stubGlobal('VideoDecoder', null);
    expect(await detectHEVC()).toBe(false);
  });

  it('should return false on error from isConfigSupported', async () => {
    vi.stubGlobal('VideoDecoder', {
      isConfigSupported: vi.fn().mockRejectedValue(new Error('unsupported')),
    });
    expect(await detectHEVC()).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// detectMSEH265 / probeMSEH265
// ---------------------------------------------------------------------------
// detectMSEH265() is now a thin sync accessor over the cached result of the
// authoritative async probeMSEH265() — it must NOT trust isTypeSupported
// (which is a known false positive for MSE H.265 on Chromium/Edge). Until the
// probe runs it conservatively returns false so H.265 is routed to the wasm
// player rather than to the black-screen HLS/MSE path.
describe('detectMSEH265', () => {
  beforeEach(() => {
    // Reset the in-module cache between tests by reloading — but the module
    // caches at module scope, so we accept the default-false baseline here and
    // test the cached-accessor semantics.
    vi.stubGlobal('MediaSource', {
      isTypeSupported: vi.fn().mockReturnValue(true),
    });
  });

  it('returns false before any probe has run (conservative — avoids HLS black screen)', () => {
    // isTypeSupported claims true, but detectMSEH265 must NOT reflect that lie
    // until probeMSEH265() has confirmed MSE actually buffers hvc1.
    expect(detectMSEH265()).toBe(false);
  });

  it('returns false when MediaSource is undefined', () => {
    vi.stubGlobal('MediaSource', undefined);
    expect(detectMSEH265()).toBe(false);
  });

  it('returns false when MediaSource is null', () => {
    vi.stubGlobal('MediaSource', null);
    expect(detectMSEH265()).toBe(false);
  });
});

describe('probeMSEH265', () => {
  it('returns false when MediaSource is undefined', async () => {
    vi.stubGlobal('MediaSource', undefined);
    const { probeMSEH265 } = await import('./capabilities');
    expect(await probeMSEH265(true)).toBe(false);
  });

  it('returns false when isTypeSupported returns false (fast path)', async () => {
    vi.stubGlobal('MediaSource', {
      isTypeSupported: vi.fn().mockReturnValue(false),
    });
    const { probeMSEH265 } = await import('./capabilities');
    expect(await probeMSEH265(true)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// detectWebGPU
// ---------------------------------------------------------------------------
describe('detectWebGPU', () => {
  it('should return true when navigator.gpu exists', () => {
    const origGpu = Object.getOwnPropertyDescriptor(navigator, 'gpu');
    try {
      Object.defineProperty(navigator, 'gpu', {
        value: {},
        configurable: true,
        writable: true,
      });
      expect(detectWebGPU()).toBe(true);
    } finally {
      if (origGpu) {
        Object.defineProperty(navigator, 'gpu', origGpu);
      } else {
        delete (navigator as Record<string, unknown>).gpu;
      }
    }
  });

  it('should return false when navigator.gpu is undefined', () => {
    const origGpu = Object.getOwnPropertyDescriptor(navigator, 'gpu');
    try {
      Object.defineProperty(navigator, 'gpu', {
        value: undefined,
        configurable: true,
        writable: true,
      });
      expect(detectWebGPU()).toBe(false);
    } finally {
      if (origGpu) {
        Object.defineProperty(navigator, 'gpu', origGpu);
      } else {
        delete (navigator as Record<string, unknown>).gpu;
      }
    }
  });
});

// ---------------------------------------------------------------------------
// detectWebGL2
// ---------------------------------------------------------------------------
describe('detectWebGL2', () => {
  it('should return true when webgl2 context is available', () => {
    const spy = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({} as unknown as RenderingContext);
    expect(detectWebGL2()).toBe(true);
    expect(spy).toHaveBeenCalledWith('webgl2');
  });

  it('should return false when getContext returns null', () => {
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
    expect(detectWebGL2()).toBe(false);
  });

  it('should return false when document.createElement is not available', () => {
    const origCreateElement = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation(() => {
      throw new Error('no create');
    });
    expect(detectWebGL2()).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// detectOffscreenCanvas
// ---------------------------------------------------------------------------
describe('detectOffscreenCanvas', () => {
  it('should return true when OffscreenCanvas is available', () => {
    vi.stubGlobal('OffscreenCanvas', class Mock {});
    expect(detectOffscreenCanvas()).toBe(true);
  });

  it('should return false when OffscreenCanvas is not available', () => {
    vi.stubGlobal('OffscreenCanvas', undefined);
    expect(detectOffscreenCanvas()).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// getPlaybackTier
// ---------------------------------------------------------------------------
describe('getPlaybackTier', () => {
  it('should return tier1 when WebCodecs and WebGPU are available', () => {
    vi.stubGlobal('VideoDecoder', class Mock {});
    const origGpu = Object.getOwnPropertyDescriptor(navigator, 'gpu');
    try {
      Object.defineProperty(navigator, 'gpu', {
        value: {},
        configurable: true,
        writable: true,
      });
      expect(getPlaybackTier()).toBe('tier1');
    } finally {
      if (origGpu) {
        Object.defineProperty(navigator, 'gpu', origGpu);
      } else {
        delete (navigator as Record<string, unknown>).gpu;
      }
    }
  });

  it('should return tier2 when WebCodecs + WebGL2 (no WebGPU)', () => {
    vi.stubGlobal('VideoDecoder', class Mock {});
    const origGpu = Object.getOwnPropertyDescriptor(navigator, 'gpu');
    try {
      Object.defineProperty(navigator, 'gpu', {
        value: undefined,
        configurable: true,
        writable: true,
      });
      vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({} as unknown as RenderingContext);
      expect(getPlaybackTier()).toBe('tier2');
    } finally {
      if (origGpu) {
        Object.defineProperty(navigator, 'gpu', origGpu);
      } else {
        delete (navigator as Record<string, unknown>).gpu;
      }
    }
  });

  it('should return tier2 when WebCodecs + OffscreenCanvas (no WebGPU, no WebGL2)', () => {
    vi.stubGlobal('VideoDecoder', class Mock {});
    vi.stubGlobal('OffscreenCanvas', class Mock {});
    const origGpu = Object.getOwnPropertyDescriptor(navigator, 'gpu');
    try {
      Object.defineProperty(navigator, 'gpu', {
        value: undefined,
        configurable: true,
        writable: true,
      });
      vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
      expect(getPlaybackTier()).toBe('tier2');
    } finally {
      if (origGpu) {
        Object.defineProperty(navigator, 'gpu', origGpu);
      } else {
        delete (navigator as Record<string, unknown>).gpu;
      }
    }
  });

  it('should return tier3 when WebCodecs is not available', () => {
    vi.stubGlobal('VideoDecoder', undefined);
    const origGpu = Object.getOwnPropertyDescriptor(navigator, 'gpu');
    try {
      Object.defineProperty(navigator, 'gpu', {
        value: {},
        configurable: true,
        writable: true,
      });
      expect(getPlaybackTier()).toBe('tier3');
    } finally {
      if (origGpu) {
        Object.defineProperty(navigator, 'gpu', origGpu);
      } else {
        delete (navigator as Record<string, unknown>).gpu;
      }
    }
  });

  it('should return tier3 when nothing is available', () => {
    vi.stubGlobal('VideoDecoder', undefined);
    vi.stubGlobal('OffscreenCanvas', undefined);
    const origGpu = Object.getOwnPropertyDescriptor(navigator, 'gpu');
    try {
      Object.defineProperty(navigator, 'gpu', {
        value: undefined,
        configurable: true,
        writable: true,
      });
      vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
      expect(getPlaybackTier()).toBe('tier3');
    } finally {
      if (origGpu) {
        Object.defineProperty(navigator, 'gpu', origGpu);
      } else {
        delete (navigator as Record<string, unknown>).gpu;
      }
    }
  });
});
