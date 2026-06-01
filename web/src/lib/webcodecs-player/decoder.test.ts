import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { Decoder, prependAnnexB, buildH264CodecString, buildH265CodecString } from './decoder';
import type { CodecInfo } from './protocol';

// ─── Mock helpers ───────────────────────────────────────────────────────────

interface MockDecoderInstance {
  configure: ReturnType<typeof vi.fn>;
  decode: ReturnType<typeof vi.fn>;
  reset: ReturnType<typeof vi.fn>;
  close: ReturnType<typeof vi.fn>;
  state: string;
  decodeQueueSize: number;
}

let mockDecoderInstances: MockDecoderInstance[] = [];
let mockIsConfigSupported: ReturnType<typeof vi.fn>;

function createMockDecoderClass(): any {
  return class MockVideoDecoder {
    configure: ReturnType<typeof vi.fn>;
    decode: ReturnType<typeof vi.fn>;
    reset: ReturnType<typeof vi.fn>;
    close: ReturnType<typeof vi.fn>;
    state = 'unconfigured';
    decodeQueueSize = 0;

    constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
      this.configure = vi.fn().mockImplementation(async (config: any) => {
        this.state = 'configured';
      });
      this.decode = vi.fn().mockImplementation((chunk: any) => {
        // Simulate synchronous decode by invoking output callback
        if (chunk.type === 'key' || chunk.type === 'delta') {
          this.decodeQueueSize++;
          // Simulate async decode completion
          queueMicrotask(() => {
            this.decodeQueueSize = Math.max(0, this.decodeQueueSize - 1);
          });
        }
      });
      this.reset = vi.fn().mockImplementation(() => {
        this.state = 'unconfigured';
        this.decodeQueueSize = 0;
      });
      this.close = vi.fn().mockImplementation(() => {
        this.state = 'closed';
        this.decodeQueueSize = 0;
      });

      const instance: MockDecoderInstance = this as unknown as MockDecoderInstance;
      mockDecoderInstances.push(instance);
    }

    static isConfigSupported = mockIsConfigSupported;
  };
}

// ─── Sample data ────────────────────────────────────────────────────────────

const H264_SPS = new Uint8Array([
  0x67, 0x42, 0xC0, 0x1E, 0xD9, 0x00, 0xA0, 0x47, 0xFE, 0x88,
]);
const H264_PPS = new Uint8Array([0x68, 0xCE, 0x38, 0x80]);
const H265_VPS = new Uint8Array([0x40, 0x01, 0x0C, 0x01, 0xFF, 0xFF, 0x01, 0x60]);
const H265_SPS = new Uint8Array([0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x7B, 0xAC, 0x09]);
const H265_PPS = new Uint8Array([0x44, 0x01, 0xC1, 0x72, 0xB4, 0x62, 0x40]);

function makeH264CodecInfo(): CodecInfo {
  return {
    codec: 'h264',
    profile: 0x42, // 66 = High
    level: 0x1E,   // 30 = Level 3.0
    sps: H264_SPS,
    pps: H264_PPS,
  };
}

function makeH265CodecInfo(): CodecInfo {
  return {
    codec: 'h265',
    profile: 1,    // Main
    level: 0x5D,   // 93 = Level 3.1
    sps: H265_SPS,
    pps: H265_PPS,
    vps: H265_VPS,
  };
}

// ─── Setup / Teardown ────────────────────────────────────────────────────

beforeEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  mockDecoderInstances = [];
  mockIsConfigSupported = vi.fn().mockResolvedValue({ supported: true });
  vi.stubGlobal('VideoDecoder', createMockDecoderClass());
  vi.stubGlobal('EncodedVideoChunk', class MockEncodedVideoChunk {
    type: string;
    timestamp: number;
    data: Uint8Array;
    constructor(opts: { type: string; timestamp: number; data: Uint8Array }) {
      this.type = opts.type;
      this.timestamp = opts.timestamp;
      this.data = opts.data;
    }
  });
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  mockDecoderInstances = [];
});

// ---------------------------------------------------------------------------
// prependAnnexB
// ---------------------------------------------------------------------------
describe('prependAnnexB', () => {
  it('should prepend 00 00 00 01 before each NALU', () => {
    const nalu1 = new Uint8Array([0x67, 0x42]);
    const nalu2 = new Uint8Array([0x65, 0xAA, 0xBB]);
    const result = prependAnnexB([nalu1, nalu2]);

    // Expected: 4 + 2 + 4 + 3 = 13 bytes
    expect(result.length).toBe(13);
    expect(result[0]).toBe(0x00);
    expect(result[1]).toBe(0x00);
    expect(result[2]).toBe(0x00);
    expect(result[3]).toBe(0x01);
    expect(result[4]).toBe(0x67);
    expect(result[5]).toBe(0x42);
    expect(result[6]).toBe(0x00);
    expect(result[7]).toBe(0x00);
    expect(result[8]).toBe(0x00);
    expect(result[9]).toBe(0x01);
    expect(result[10]).toBe(0x65);
    expect(result[11]).toBe(0xAA);
    expect(result[12]).toBe(0xBB);
  });

  it('should handle single NALU', () => {
    const nalu = new Uint8Array([0x67, 0x42, 0xC0]);
    const result = prependAnnexB([nalu]);
    expect(result.length).toBe(7);
    expect(result[0]).toBe(0x00);
    expect(result[3]).toBe(0x01);
    expect(result[4]).toBe(0x67);
  });

  it('should handle empty NALU array', () => {
    const result = prependAnnexB([]);
    expect(result.length).toBe(0);
  });

  it('should handle empty NALUs in array', () => {
    const nalu = new Uint8Array([0x67]);
    const result = prependAnnexB([nalu, new Uint8Array(0)]);
    // 4+1 + 4+0 = 9
    expect(result.length).toBe(9);
  });
});

// ---------------------------------------------------------------------------
// buildH264CodecString
// ---------------------------------------------------------------------------
describe('buildH264CodecString', () => {
  it('should build avc1.42C01E for profile=0x42, level=0x1E', () => {
    const sps = new Uint8Array([0x67, 0x42, 0xC0, 0x1E, 0x00]);
    const result = buildH264CodecString(sps, 0x42, 0x1E);
    expect(result).toBe('avc1.42C01E');
  });

  it('should build avc1.42001E for profile=0x42 with constraint=0x00', () => {
    const sps = new Uint8Array([0x67, 0x42, 0x00, 0x1E, 0x00]);
    const result = buildH264CodecString(sps, 0x42, 0x1E);
    expect(result).toBe('avc1.42001E');
  });

  it('should fallback to avc1.42001E when sps is too short', () => {
    const sps = new Uint8Array([0x67]);
    const result = buildH264CodecString(sps, 0x42, 0x1E);
    expect(result).toBe('avc1.42001E');
  });

  it('should fallback when sps is empty', () => {
    const result = buildH264CodecString(new Uint8Array(0), 0x42, 0x1E);
    expect(result).toBe('avc1.42001E');
  });
});

// ---------------------------------------------------------------------------
// buildH265CodecString
// ---------------------------------------------------------------------------
describe('buildH265CodecString', () => {
  it('should build hvc1.1.6.L93.B0 for Main profile L3.1', () => {
    // SPS byte[1] has general_profile_space(2) + general_tier_flag(1) + general_profile_idc(5)
    // 0x01 = 00 0 00001 → profile_space=0, tier=0, profile_idc=1 (Main)
    const sps = new Uint8Array([0x42, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5D]);
    const result = buildH265CodecString(sps, 93);
    expect(result).toBe('hvc1.1.6.L93.B0');
  });

  it('should build hvc1.2.H.L120.B0 for tier=1, profile=2', () => {
    // byte[1] = 0b SS T PPPPP = profile_space(2) + tier(1) + profile_idc(5)
    // For profile_space=0, tier=1, profile_idc=2: 0b 00 1 00010 = 0x22
    const sps = new Uint8Array([0x42, 0x22, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x78]);
    const result = buildH265CodecString(sps, 120);
    expect(result).toBe('hvc1.2.6.H120.B0');
  });

  it('should fallback when sps is too short', () => {
    const result = buildH265CodecString(new Uint8Array([0x42]), 93);
    expect(result).toBe('hvc1.1.6.L93.B0');
  });

  it('should fallback when sps is empty', () => {
    const result = buildH265CodecString(new Uint8Array(0), 93);
    expect(result).toBe('hvc1.1.6.L93.B0');
  });
});

// ---------------------------------------------------------------------------
// Decoder class — configure
// ---------------------------------------------------------------------------
describe('Decoder', () => {
  describe('configure', () => {
    it('should configure VideoDecoder with H.264 codec string', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      expect(mockDecoderInstances.length).toBe(1);
      const instance = mockDecoderInstances[0];
      expect(instance.configure).toHaveBeenCalledWith(
        expect.objectContaining({
          codec: expect.stringMatching(/^avc1\.\w{6}$/),
        }),
      );
      decoder.close();
    });

    it('should configure VideoDecoder with H.265 codec string', async () => {
      const decoder = new Decoder();
      const ci = makeH265CodecInfo();
      await decoder.configure(ci);

      expect(mockDecoderInstances.length).toBe(1);
      const instance = mockDecoderInstances[0];
      expect(instance.configure).toHaveBeenCalledWith(
        expect.objectContaining({
          codec: expect.stringMatching(/^hvc1\./),
        }),
      );
      decoder.close();
    });

    it('should include codedWidth and codedHeight in config', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      const instance = mockDecoderInstances[0];
      expect(instance.configure).toHaveBeenCalledWith(
        expect.objectContaining({
          codedWidth: 1920,
          codedHeight: 1080,
        }),
      );
      decoder.close();
    });

    it('should include description (SPS+PPS) for H.264 in config', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      const instance = mockDecoderInstances[0];
      const callArgs = instance.configure.mock.calls[0][0] as VideoDecoderConfig;
      expect(callArgs.description).toBeDefined();
      expect(callArgs.description).toBeInstanceOf(Uint8Array);
      decoder.close();
    });

    it('should include description (VPS+SPS+PPS) for H.265 in config', async () => {
      const decoder = new Decoder();
      const ci = makeH265CodecInfo();
      await decoder.configure(ci);

      const instance = mockDecoderInstances[0];
      const callArgs = instance.configure.mock.calls[0][0] as VideoDecoderConfig;
      expect(callArgs.description).toBeDefined();
      expect(callArgs.description).toBeInstanceOf(Uint8Array);
      decoder.close();
    });

    it('should reject with error for unsupported codec', async () => {
      mockIsConfigSupported = vi.fn().mockResolvedValue({ supported: false });
      vi.stubGlobal('VideoDecoder', createMockDecoderClass());

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await expect(decoder.configure(ci)).rejects.toThrow('Unsupported');
    });
  });

  // ---------------------------------------------------------------------------
  // decode
  // ---------------------------------------------------------------------------
  describe('decode', () => {
    it('should create EncodedVideoChunk and call decoder.decode', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      const nalu1 = new Uint8Array([0x65, 0xAA, 0xBB]);
      const nalu2 = new Uint8Array([0x01, 0xCC, 0xDD]);
      decoder.decode([nalu1, nalu2], 3000, false);

      const instance = mockDecoderInstances[0];
      expect(instance.decode).toHaveBeenCalledTimes(1);
      const chunk = instance.decode.mock.calls[0][0];
      expect(chunk).toBeDefined();
      expect(chunk.type).toBe('delta');
      expect(chunk.timestamp).toBe(3000);
      expect(chunk.data).toBeInstanceOf(Uint8Array);
      decoder.close();
    });

    it('should create key chunk for keyframes', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      decoder.decode([new Uint8Array([0x65])], 0, true);

      const instance = mockDecoderInstances[0];
      const chunk = instance.decode.mock.calls[0][0];
      expect(chunk.type).toBe('key');
      decoder.close();
    });

    it('should prepend Annex B start codes to NALU data', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      const nalu = new Uint8Array([0x65, 0x01]);
      decoder.decode([nalu], 1000, false);

      const instance = mockDecoderInstances[0];
      const chunk = instance.decode.mock.calls[0][0];
      // 4 start code bytes + 2 payload bytes
      expect(chunk.data.length).toBe(6);
      expect(chunk.data[0]).toBe(0x00);
      expect(chunk.data[3]).toBe(0x01);
      expect(chunk.data[4]).toBe(0x65);
      decoder.close();
    });

    it('should not decode when decoder is not configured', () => {
      const decoder = new Decoder();
      // Not calling configure
      decoder.decode([new Uint8Array([0x65])], 0, false);
      // Should not crash, silently skip
      expect(mockDecoderInstances.length).toBe(0);
    });
  });

  // ---------------------------------------------------------------------------
  // onFrame callback
  // ---------------------------------------------------------------------------
  describe('onFrame', () => {
    it('should call onFrame callback when decoder produces output', async () => {
      // Override mock to actually invoke output callback
      let outputCallback: ((frame: any) => void) | null = null;
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn().mockImplementation((_chunk: any) => {
          if (outputCallback) {
            const mockFrame = { close: vi.fn() };
            outputCallback(mockFrame);
          }
        });
        reset = vi.fn();
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          outputCallback = init.output;
          mockDecoderInstances.push(this as unknown as MockDecoderInstance);
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      const frameCallback = vi.fn();
      decoder.onFrame(frameCallback);

      decoder.decode([new Uint8Array([0x65])], 1000, false);

      expect(frameCallback).toHaveBeenCalledTimes(1);
      decoder.close();
    });

    it('should close VideoFrame after onFrame callback returns', async () => {
      let outputCallback: ((frame: any) => void) | null = null;
      const mockFrame = { close: vi.fn() };
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn().mockImplementation(() => {
          if (outputCallback) outputCallback(mockFrame);
        });
        reset = vi.fn();
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          outputCallback = init.output;
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      decoder.onFrame((frame: any) => { frame.close(); });
      decoder.decode([new Uint8Array([0x65])], 0, false);

      // Frame should be closed after callback
      expect(mockFrame.close).toHaveBeenCalledTimes(1);
      decoder.close();
    });
  });

  // ---------------------------------------------------------------------------
  // onError callback
  // ---------------------------------------------------------------------------
  describe('onError', () => {
    it('should call onError callback when decoder error occurs', async () => {
      const decoderError = new Error('decoder hardware error');
      let errorCb: ((e: Error) => void) | null = null;
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn().mockImplementation(() => {
          // Fire error during decode, not construction
          if (errorCb) errorCb(decoderError);
        });
        reset = vi.fn();
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          errorCb = init.error;
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      const errorCallback = vi.fn();
      decoder.onError(errorCallback);

      decoder.decode([new Uint8Array([0x65])], 0, false);

      // Error fires synchronously during decode
      expect(errorCallback).toHaveBeenCalledTimes(1);
      expect(errorCallback).toHaveBeenCalledWith(decoderError);
      decoder.close();
    });
  });

  // ---------------------------------------------------------------------------
  // Error recovery
  // ---------------------------------------------------------------------------
  describe('error recovery', () => {
    it('should auto-reset and re-configure on decoder error', async () => {
      const decoderError = new Error('fatal decode error');
      let errorCb: ((e: Error) => void) | null = null;

      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => { this.state = 'configured'; });
        decode = vi.fn();
        reset = vi.fn().mockImplementation(() => { this.state = 'unconfigured'; });
        close = vi.fn().mockImplementation(() => { this.state = 'closed'; });
        state = 'unconfigured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          errorCb = init.error;
          mockDecoderInstances.push(this as unknown as MockDecoderInstance);
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      // Simulate decoder error
      if (errorCb) errorCb(decoderError);

      // Wait for error recovery to process (it's async via queueMicrotask)
      await vi.waitFor(() => {
        expect(mockDecoderInstances.length).toBeGreaterThanOrEqual(2);
      });

      // First decoder should have been reset
      expect(mockDecoderInstances[0].reset).toHaveBeenCalled();
      // Second decoder should have been configured
      expect(mockDecoderInstances[1].configure).toHaveBeenCalled();

      decoder.close();
    });
  });

  // ---------------------------------------------------------------------------
  // reset
  // ---------------------------------------------------------------------------
  describe('reset', () => {
    it('should call reset on the underlying VideoDecoder', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      decoder.reset();

      const instance = mockDecoderInstances[0];
      expect(instance.reset).toHaveBeenCalled();
      decoder.close();
    });

    it('should be safe to call when not configured', () => {
      const decoder = new Decoder();
      // Should not throw
      decoder.reset();
    });
  });

  // ---------------------------------------------------------------------------
  // close
  // ---------------------------------------------------------------------------
  describe('close', () => {
    it('should call close on the underlying VideoDecoder', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      decoder.close();

      const instance = mockDecoderInstances[0];
      expect(instance.close).toHaveBeenCalled();
    });

    it('should be safe to call multiple times', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      decoder.close();
      decoder.close();
      decoder.close();

      const instance = mockDecoderInstances[0];
      expect(instance.close).toHaveBeenCalledTimes(1);
    });

    it('should not decode after close', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);
      decoder.close();

      decoder.decode([new Uint8Array([0x65])], 0, false);

      // Should not have called decode on the closed decoder
      const instance = mockDecoderInstances[0];
      expect(instance.decode).not.toHaveBeenCalled();
    });
  });

  // ---------------------------------------------------------------------------
  // H.265 description building
  // ---------------------------------------------------------------------------
  describe('H.265 description (VPS+SPS+PPS)', () => {
    it('should build description with VPS+SPS+PPS for H.265', async () => {
      const decoder = new Decoder();
      const ci = makeH265CodecInfo();
      await decoder.configure(ci);

      const instance = mockDecoderInstances[0];
      const callArgs = instance.configure.mock.calls[0][0] as VideoDecoderConfig;
      const desc = callArgs.description as Uint8Array;

      // VPS (4 start code + 8 VPS) + SPS (4 start code + 21 SPS) + PPS (4 start code + 7 PPS) = 48
      expect(desc.length).toBe(48);
      decoder.close();
    });
  });

  // ---------------------------------------------------------------------------
  // H.264 description building
  // ---------------------------------------------------------------------------
  describe('H.264 description (SPS+PPS)', () => {
    it('should build description with SPS+PPS for H.264', async () => {
      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      const instance = mockDecoderInstances[0];
      const callArgs = instance.configure.mock.calls[0][0] as VideoDecoderConfig;
      const desc = callArgs.description as Uint8Array;

      // SPS (4 start code + 10 SPS) + PPS (4 start code + 4 PPS) = 22
      expect(desc.length).toBe(22);
      decoder.close();
    });
  });

  // -------------------------------------------------------------------------
  // Pending frame cleanup
  // -------------------------------------------------------------------------
  describe('pending frame cleanup', () => {
    it('should close frame when onFrame callback throws', async () => {
      let outputCallback: ((frame: any) => void) | null = null;
      const mockFrame = { close: vi.fn() };
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn().mockImplementation(() => {
          if (outputCallback) outputCallback(mockFrame);
        });
        reset = vi.fn();
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          outputCallback = init.output;
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      decoder.onFrame((_frame: any) => {
        throw new Error('transfer failed');
      });
      decoder.decode([new Uint8Array([0x65])], 0, false);

      expect(mockFrame.close).toHaveBeenCalledTimes(1);
      decoder.close();
    });

    it('should close frames that arrive asynchronously after close()', async () => {
      // When close() is called, frames queued inside the VideoDecoder may still arrive
      // via async output callbacks. handleOutput must still close them.
      const pendingFrames = [{ close: vi.fn() }, { close: vi.fn() }];
      let outputCallback: ((frame: any) => void) | null = null;
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn();
        reset = vi.fn();
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          outputCallback = init.output;
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);
      decoder.onFrame(() => { /* noop — frame not closed by callback */ });

      // Schedule output via setTimeout (macrotask, runs after close())
      if (outputCallback) {
        setTimeout(() => outputCallback!(pendingFrames[0]), 0);
        setTimeout(() => outputCallback!(pendingFrames[1]), 0);
      }

      decoder.close();

      // Let macrotasks run — handleOutput processes frames on closed decoder
      await new Promise(resolve => setTimeout(resolve, 50));

      // Frames should have been closed by handleOutput (no callback registered after close)
      expect(pendingFrames[0].close).toHaveBeenCalledTimes(1);
      expect(pendingFrames[1].close).toHaveBeenCalledTimes(1);
    });

    it('should close frames that arrive asynchronously after error recovery', async () => {
      const decoderError = new Error('hardware error');
      let errorCb: ((e: Error) => void) | null = null;
      let outputCb: ((frame: any) => void) | null = null;
      const pendingFrames = [{ close: vi.fn() }, { close: vi.fn() }];

      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => { this.state = 'configured'; });
        decode = vi.fn();
        reset = vi.fn().mockImplementation(() => { this.state = 'unconfigured'; });
        close = vi.fn().mockImplementation(() => { this.state = 'closed'; });
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          outputCb = init.output;
          errorCb = init.error;
          mockDecoderInstances.push(this as unknown as MockDecoderInstance);
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);
      decoder.onFrame(() => { /* noop */ });

      // Capture the original output callback BEFORE error recovery triggers.
      // The mock constructor reassigns outputCb when the recovery decoder is created,
      // so setTimeout must use a saved reference to the stale decoder's output.
      const oldOutputCb = outputCb;

      // Schedule async output via setTimeout (using old callback with stale epoch)
      if (oldOutputCb) {
        setTimeout(() => oldOutputCb!(pendingFrames[0]), 0);
        setTimeout(() => oldOutputCb!(pendingFrames[1]), 0);
      }

      // Trigger error recovery before macrotasks run
      if (errorCb) errorCb(decoderError);

      // Wait for async recovery + macrotasks to settle
      await vi.waitFor(() => {
        expect(mockDecoderInstances.length).toBeGreaterThanOrEqual(2);
      });
      await new Promise(resolve => setTimeout(resolve, 50));

      // Frames delivered after error should still be closed by handleOutput
      expect(pendingFrames[0].close).toHaveBeenCalledTimes(1);
      expect(pendingFrames[1].close).toHaveBeenCalledTimes(1);
      decoder.close();
    });
});

  // ---------------------------------------------------------------------------
  // Backpressure
  // ---------------------------------------------------------------------------
  describe('backpressure', () => {
    it('should decode normally when under threshold', async () => {
      let outputCallback: ((frame: any) => void) | null = null;
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn().mockImplementation(() => {
          if (outputCallback) outputCallback({ close: vi.fn() });
        });
        reset = vi.fn();
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          outputCallback = init.output;
          mockDecoderInstances.push(this as unknown as MockDecoderInstance);
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      // Decode 4 frames (under threshold of 5)
      for (let i = 0; i < 4; i++) {
        decoder.decode([new Uint8Array([0x65])], i * 1000, false);
      }

      expect(decoder.frameDropCount).toBe(0);
      expect(decoder.pendingDecodeCount).toBe(0); // All processed synchronously
      decoder.close();
    });

    it('should skip frames when decode queue exceeds threshold', async () => {
      let outputCallback: ((frame: any) => void) | null = null;
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn(); // Don't invoke output — simulates slow decoder
        reset = vi.fn();
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          outputCallback = init.output;
          mockDecoderInstances.push(this as unknown as MockDecoderInstance);
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      // Send 5 frames (fills queue to threshold)
      for (let i = 0; i < 5; i++) {
        decoder.decode([new Uint8Array([0x65])], i * 1000, false);
      }
      expect(decoder.pendingDecodeCount).toBe(5);
      expect(decoder.frameDropCount).toBe(0);

      // 6th frame should be dropped
      decoder.decode([new Uint8Array([0x65])], 5000, false);
      expect(decoder.pendingDecodeCount).toBe(5);
      expect(decoder.frameDropCount).toBe(1);

      // 7th frame should also be dropped
      decoder.decode([new Uint8Array([0x65])], 6000, false);
      expect(decoder.frameDropCount).toBe(2);

      decoder.close();
    });

    it('should emit backpressure signal when threshold exceeded', async () => {
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn(); // Slow decoder — never invokes output
        reset = vi.fn();
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          mockDecoderInstances.push(this as unknown as MockDecoderInstance);
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      const bpCallback = vi.fn();
      decoder.onBackpressure(bpCallback);

      // Fill queue to threshold
      for (let i = 0; i < 5; i++) {
        decoder.decode([new Uint8Array([0x65])], i * 1000, false);
      }
      expect(bpCallback).not.toHaveBeenCalled();

      // Exceed threshold — should emit backpressure signal
      decoder.decode([new Uint8Array([0x65])], 5000, false);
      expect(bpCallback).toHaveBeenCalledWith(true);
      expect(bpCallback).toHaveBeenCalledTimes(1);

      // Subsequent drops should NOT re-emit
      decoder.decode([new Uint8Array([0x65])], 6000, false);
      expect(bpCallback).toHaveBeenCalledTimes(1);

      decoder.close();
    });

    it('should emit resume signal when pending count drops below threshold', async () => {
      let outputCallback: ((frame: any) => void) | null = null;
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn();
        reset = vi.fn();
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          outputCallback = init.output;
          mockDecoderInstances.push(this as unknown as MockDecoderInstance);
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      const bpCallback = vi.fn();
      decoder.onBackpressure(bpCallback);

      // Fill queue to threshold + exceed
      for (let i = 0; i < 6; i++) {
        decoder.decode([new Uint8Array([0x65])], i * 1000, false);
      }
      expect(bpCallback).toHaveBeenCalledWith(true);

      // Process one frame (output fires)
      if (outputCallback) outputCallback({ close: vi.fn() });
      expect(decoder.pendingDecodeCount).toBe(4);
      expect(bpCallback).toHaveBeenCalledWith(false);
      expect(bpCallback).toHaveBeenCalledTimes(2);

      decoder.close();
    });

    it('should reset pending count and backpressure on reset()', async () => {
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn();
        reset = vi.fn().mockImplementation(() => { this.state = 'unconfigured'; });
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          mockDecoderInstances.push(this as unknown as MockDecoderInstance);
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      const bpCallback = vi.fn();
      decoder.onBackpressure(bpCallback);

      // Fill and exceed threshold
      for (let i = 0; i < 6; i++) {
        decoder.decode([new Uint8Array([0x65])], i * 1000, false);
      }
      expect(decoder.pendingDecodeCount).toBe(5);

      // Reset should flush everything
      decoder.reset();
      expect(decoder.pendingDecodeCount).toBe(0);
      expect(bpCallback).toHaveBeenCalledWith(false);

      decoder.close();
    });

    it('should not crash when decoder is slow and many frames arrive', async () => {
      const mockClass = class MockVideoDecoder {
        configure = vi.fn().mockImplementation(async () => {});
        decode = vi.fn(); // Never processes
        reset = vi.fn();
        close = vi.fn();
        state = 'configured';
        decodeQueueSize = 0;
        constructor(init: { output: (frame: any) => void; error: (e: Error) => void }) {
          mockDecoderInstances.push(this as unknown as MockDecoderInstance);
        }
        static isConfigSupported = mockIsConfigSupported;
      };
      vi.stubGlobal('VideoDecoder', mockClass);

      const decoder = new Decoder();
      const ci = makeH264CodecInfo();
      await decoder.configure(ci);

      // Send 100 frames rapidly
      for (let i = 0; i < 100; i++) {
        decoder.decode([new Uint8Array([0x65])], i * 33, i % 30 === 0);
      }

      expect(decoder.frameDropCount).toBe(95);
      expect(decoder.pendingDecodeCount).toBe(5);
      expect(() => decoder.close()).not.toThrow();
    });
  });
});
