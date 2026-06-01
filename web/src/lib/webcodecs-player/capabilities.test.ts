import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  detectWebCodecs,
  detectHEVC,
  detectWebGPU,
  detectWebGL2,
  detectOffscreenCanvas,
  detectSharedArrayBuffer,
  detectWasmSimd,
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
    const spy = vi
      .spyOn(HTMLCanvasElement.prototype, 'getContext')
      .mockReturnValue({} as unknown as RenderingContext);
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
// detectSharedArrayBuffer
// ---------------------------------------------------------------------------
describe('detectSharedArrayBuffer', () => {
  it('should return true when SharedArrayBuffer is available', () => {
    vi.stubGlobal('SharedArrayBuffer', class Mock {});
    expect(detectSharedArrayBuffer()).toBe(true);
  });

  it('should return false when SharedArrayBuffer is not available', () => {
    vi.stubGlobal('SharedArrayBuffer', undefined);
    expect(detectSharedArrayBuffer()).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// detectWasmSimd
// ---------------------------------------------------------------------------
describe('detectWasmSimd', () => {
  it('should return true when WebAssembly.validate succeeds with SIMD binary', () => {
    vi.spyOn(WebAssembly, 'validate').mockReturnValue(true);
    expect(detectWasmSimd()).toBe(true);
  });

  it('should return false when WebAssembly.validate fails', () => {
    vi.spyOn(WebAssembly, 'validate').mockReturnValue(false);
    expect(detectWasmSimd()).toBe(false);
  });

  it('should return false when WebAssembly is not available', () => {
    vi.stubGlobal('WebAssembly', undefined);
    expect(detectWasmSimd()).toBe(false);
  });

  it('should return false when WebAssembly.validate is not a function', () => {
    vi.stubGlobal('WebAssembly', { validate: 'not-a-function' });
    expect(detectWasmSimd()).toBe(false);
  });

  it('should return false on error during validation', () => {
    vi.spyOn(WebAssembly, 'validate').mockImplementation(() => {
      throw new Error('validation error');
    });
    expect(detectWasmSimd()).toBe(false);
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
