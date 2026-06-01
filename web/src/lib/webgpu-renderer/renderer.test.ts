import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WebGPURenderer } from './renderer';

// ─── Mock factories ──────────────────────────────────────────────────────────

function createMockExternalTexture() {
  return { destroy: vi.fn() };
}

function createMockTextureView() {
  return {};
}

function createMockTexture(width: number, height: number) {
  return {
    createView: vi.fn().mockReturnValue(createMockTextureView()),
    destroy: vi.fn(),
    width,
    height,
  };
}

function createMockPipeline() {
  const layout = {};
  return {
    getBindGroupLayout: vi.fn().mockReturnValue(layout),
  };
}

function createMockVideoFrame(
  displayWidth = 1920,
  displayHeight = 1080,
): VideoFrame {
  return {
    displayWidth,
    displayHeight,
    close: vi.fn(),
  } as unknown as VideoFrame;
}

// ─── Mock state ──────────────────────────────────────────────────────────────

let externalTextures: ReturnType<typeof createMockExternalTexture>[];
let stagingTextures: ReturnType<typeof createMockTexture>[];
let renderPassEncoder: {
  setPipeline: ReturnType<typeof vi.fn>;
  setBindGroup: ReturnType<typeof vi.fn>;
  draw: ReturnType<typeof vi.fn>;
  end: ReturnType<typeof vi.fn>;
};
let commandBuffer: {};
let commandEncoder: {
  beginRenderPass: ReturnType<typeof vi.fn>;
  copyExternalImageToTexture: ReturnType<typeof vi.fn>;
  finish: ReturnType<typeof vi.fn>;
};
let canvasContext: {
  configure: ReturnType<typeof vi.fn>;
  getCurrentTexture: ReturnType<typeof vi.fn>;
  unconfigure: ReturnType<typeof vi.fn>;
};
let device: {
  createShaderModule: ReturnType<typeof vi.fn>;
  createRenderPipeline: ReturnType<typeof vi.fn>;
  createBindGroupLayout: ReturnType<typeof vi.fn>;
  createSampler: ReturnType<typeof vi.fn>;
  createBindGroup: ReturnType<typeof vi.fn>;
  createCommandEncoder: ReturnType<typeof vi.fn>;
  createTexture: ReturnType<typeof vi.fn>;
  importExternalTexture: ReturnType<typeof vi.fn>;
  queue: { submit: ReturnType<typeof vi.fn> };
  destroy: ReturnType<typeof vi.fn>;
  lost: { then: ReturnType<typeof vi.fn> };
};
let mockRequestAdapter: ReturnType<typeof vi.fn>;
let mockAdapterRequestDevice: ReturnType<typeof vi.fn>;
let capturedExternalSource: unknown;
let shouldThrowOnImportExternal = false;

function setupMocks() {
  vi.restoreAllMocks();

  // Mock WebGPU globals not available in jsdom
  Object.defineProperty(globalThis, 'GPUTextureUsage', {
    value: {
      COPY_SRC: 0x01,
      COPY_DST: 0x02,
      TEXTURE_BINDING: 0x04,
      STORAGE_BINDING: 0x08,
      RENDER_ATTACHMENT: 0x10,
    },
    writable: true,
    configurable: true,
  });

  externalTextures = [];
  stagingTextures = [];
  capturedExternalSource = null;
  shouldThrowOnImportExternal = false;

  renderPassEncoder = {
    setPipeline: vi.fn(),
    setBindGroup: vi.fn(),
    draw: vi.fn(),
    end: vi.fn(),
  };

  commandBuffer = {};
  commandEncoder = {
    beginRenderPass: vi.fn().mockReturnValue(renderPassEncoder),
    copyExternalImageToTexture: vi.fn(),
    finish: vi.fn().mockReturnValue(commandBuffer),
  };

  const canvasTexture = createMockTexture(800, 600);
  canvasContext = {
    configure: vi.fn(),
    getCurrentTexture: vi.fn().mockReturnValue(canvasTexture),
    unconfigure: vi.fn(),
  };

  device = {
    createShaderModule: vi.fn().mockReturnValue({}),
    createRenderPipeline: vi.fn().mockImplementation(() => createMockPipeline()),
    createBindGroupLayout: vi.fn().mockReturnValue({}),
    createSampler: vi.fn().mockReturnValue({}),
    createBindGroup: vi.fn().mockReturnValue({}),
    createCommandEncoder: vi.fn().mockReturnValue(commandEncoder),
    createTexture: vi.fn().mockImplementation((desc: { size: number[] }) => {
      const tex = createMockTexture(desc.size[0], desc.size[1]);
      stagingTextures.push(tex);
      return tex;
    }),
    importExternalTexture: vi.fn().mockImplementation((desc: { source: unknown }) => {
      capturedExternalSource = desc.source;
      if (shouldThrowOnImportExternal) {
        throw new DOMException('Operation not supported', 'OperationError');
      }
      const ext = createMockExternalTexture();
      externalTextures.push(ext);
      return ext;
    }),
    queue: { submit: vi.fn() },
    destroy: vi.fn(),
    lost: { then: vi.fn() },
  };

  mockAdapterRequestDevice = vi.fn().mockResolvedValue(device);
  mockRequestAdapter = vi.fn().mockResolvedValue({ requestDevice: mockAdapterRequestDevice });

  const gpu = {
    requestAdapter: mockRequestAdapter,
    getPreferredCanvasFormat: vi.fn().mockReturnValue('bgra8unorm'),
  };

  Object.defineProperty(navigator, 'gpu', {
    value: gpu,
    writable: true,
    configurable: true,
  });

  const originalGetContext = HTMLCanvasElement.prototype.getContext;
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(
    function (this: HTMLCanvasElement, contextId: string, ...args: unknown[]) {
      if (contextId === 'webgpu') return canvasContext;
      return originalGetContext.call(this, contextId, ...args);
    },
  );
}

// ─── Setup / Teardown ─────────────────────────────────────────────────────────

beforeEach(() => {
  setupMocks();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

// ─── Tests ───────────────────────────────────────────────────────────────────

describe('WebGPURenderer', () => {
  describe('init', () => {
    it('initializes WebGPU device and configures canvas context', async () => {
      const renderer = new WebGPURenderer();
      const canvas = document.createElement('canvas');

      const result = await renderer.init(canvas);

      expect(result).toBe(true);
      expect(mockRequestAdapter).toHaveBeenCalled();
      expect(mockAdapterRequestDevice).toHaveBeenCalled();
      expect(canvasContext.configure).toHaveBeenCalledWith(
        expect.objectContaining({
          device,
          alphaMode: 'opaque',
        }),
      );
      renderer.destroy();
    });

    it('creates shader modules and render pipelines', async () => {
      const renderer = new WebGPURenderer();
      const canvas = document.createElement('canvas');

      await renderer.init(canvas);

      expect(device.createShaderModule).toHaveBeenCalledTimes(2);
      expect(device.createRenderPipeline).toHaveBeenCalledTimes(2);
      expect(device.createBindGroupLayout).toHaveBeenCalledTimes(2);
      expect(device.createSampler).toHaveBeenCalledTimes(1);
      renderer.destroy();
    });

    it('returns false when WebGPU is not available', async () => {
      Object.defineProperty(navigator, 'gpu', {
        value: undefined,
        writable: true,
        configurable: true,
      });

      const renderer = new WebGPURenderer();
      const canvas = document.createElement('canvas');

      expect(await renderer.init(canvas)).toBe(false);
    });

    it('returns false when adapter request returns null', async () => {
      mockRequestAdapter.mockResolvedValue(null);

      const renderer = new WebGPURenderer();
      expect(await renderer.init(document.createElement('canvas'))).toBe(false);
    });

    it('returns false when device request returns null', async () => {
      mockAdapterRequestDevice.mockResolvedValue(null);

      const renderer = new WebGPURenderer();
      expect(await renderer.init(document.createElement('canvas'))).toBe(false);
    });

    it('returns false when canvas context is null', async () => {
      vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);

      const renderer = new WebGPURenderer();
      expect(await renderer.init(document.createElement('canvas'))).toBe(false);
    });

    it('returns false when init throws', async () => {
      mockRequestAdapter.mockRejectedValue(new Error('GPU access denied'));

      const renderer = new WebGPURenderer();
      expect(await renderer.init(document.createElement('canvas'))).toBe(false);
    });
  });

  describe('render (external texture path)', () => {
    it('imports VideoFrame as GPUExternalTexture', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      const frame = createMockVideoFrame(1920, 1080);
      renderer.render(frame);
      await vi.advanceTimersByTimeAsync(17);

      expect(device.importExternalTexture).toHaveBeenCalledWith(
        expect.objectContaining({ source: frame }),
      );
      expect(capturedExternalSource).toBe(frame);
      renderer.destroy();
    });

    it('resizes canvas to match video dimensions on first frame', async () => {
      const canvas = document.createElement('canvas');
      const renderer = new WebGPURenderer();
      await renderer.init(canvas);

      expect(canvas.width).toBe(300);

      renderer.render(createMockVideoFrame(1920, 1080));
      await vi.advanceTimersByTimeAsync(17);

      expect(canvas.width).toBe(1920);
      expect(canvas.height).toBe(1080);
      renderer.destroy();
    });

    it('resizes canvas when frame dimensions change between renders', async () => {
      const canvas = document.createElement('canvas');
      const renderer = new WebGPURenderer();
      await renderer.init(canvas);

      renderer.render(createMockVideoFrame(1920, 1080));
      await vi.advanceTimersByTimeAsync(17);
      expect(canvas.width).toBe(1920);
      expect(canvas.height).toBe(1080);

      renderer.render(createMockVideoFrame(640, 480));
      await vi.advanceTimersByTimeAsync(17);
      expect(canvas.width).toBe(640);
      expect(canvas.height).toBe(480);

      renderer.destroy();
    });

    it('destroys GPUExternalTexture after each render', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      renderer.render(createMockVideoFrame());
      await vi.advanceTimersByTimeAsync(17);

      expect(externalTextures.length).toBe(1);
      expect(externalTextures[0].destroy).toHaveBeenCalledTimes(1);
      renderer.destroy();
    });

    it('closes the VideoFrame after rendering', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      const frame = createMockVideoFrame();
      const closeSpy = vi.spyOn(frame, 'close');
      renderer.render(frame);
      await vi.advanceTimersByTimeAsync(17);

      expect(closeSpy).toHaveBeenCalledTimes(1);
      renderer.destroy();
    });

    it('draws a full-screen quad (6 vertices) with correct pipeline calls', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      renderer.render(createMockVideoFrame());
      await vi.advanceTimersByTimeAsync(17);

      expect(renderPassEncoder.setPipeline).toHaveBeenCalled();
      expect(renderPassEncoder.setBindGroup).toHaveBeenCalledWith(0, expect.any(Object));
      expect(renderPassEncoder.draw).toHaveBeenCalledWith(6);
      expect(renderPassEncoder.end).toHaveBeenCalled();
      renderer.destroy();
    });

    it('submits command buffer to GPU queue', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      renderer.render(createMockVideoFrame());
      await vi.advanceTimersByTimeAsync(17);

      expect(commandEncoder.finish).toHaveBeenCalled();
      expect(device.queue.submit).toHaveBeenCalledWith([commandBuffer]);
      renderer.destroy();
    });

    it('coalesces multiple frames — only latest is rendered', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      const frame1 = createMockVideoFrame(1920, 1080);
      const frame2 = createMockVideoFrame(1920, 1080);
      const closeSpy1 = vi.spyOn(frame1, 'close');

      renderer.render(frame1);
      renderer.render(frame2);
      await vi.advanceTimersByTimeAsync(17);

      expect(closeSpy1).toHaveBeenCalledTimes(1);
      expect(device.importExternalTexture).toHaveBeenCalledTimes(1);
      expect(capturedExternalSource).toBe(frame2);
      renderer.destroy();
    });

    it('is no-op when not initialized', () => {
      const renderer = new WebGPURenderer();
      const frame = createMockVideoFrame();
      const closeSpy = vi.spyOn(frame, 'close');
      expect(() => renderer.render(frame)).not.toThrow();
      expect(closeSpy).toHaveBeenCalledTimes(1);
    });

    it('is no-op after destroy', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));
      renderer.destroy();

      const frame = createMockVideoFrame();
      const closeSpy = vi.spyOn(frame, 'close');
      expect(() => renderer.render(frame)).not.toThrow();
      expect(closeSpy).toHaveBeenCalledTimes(1);
    });
    it('catches render errors and closes frame', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      // Simulate a GPU error during rendering
      canvasContext.getCurrentTexture = vi.fn().mockImplementation(() => {
        throw new Error('GPU context lost');
      });

      const frame = createMockVideoFrame();
      const closeSpy = vi.spyOn(frame, 'close');
      renderer.render(frame);
      await vi.advanceTimersByTimeAsync(17);

      expect(closeSpy).toHaveBeenCalledTimes(1);
      renderer.destroy();
    });
  });

  describe('render (fallback copyExternalImageToTexture path)', () => {
    it('falls back when importExternalTexture throws', async () => {
      shouldThrowOnImportExternal = true;

      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      const frame = createMockVideoFrame(1280, 720);
      renderer.render(frame);
      await vi.advanceTimersByTimeAsync(17);

      expect(device.importExternalTexture).toHaveBeenCalled();
      expect(device.createTexture).toHaveBeenCalledWith(
        expect.objectContaining({ size: [1280, 720] }),
      );
      expect(commandEncoder.copyExternalImageToTexture).toHaveBeenCalledWith(
        expect.objectContaining({ source: frame }),
        expect.objectContaining({ texture: expect.any(Object) }),
        expect.arrayContaining([1280, 720]),
      );
      renderer.destroy();
    });

    it('reuses staging texture when dimensions match', async () => {
      shouldThrowOnImportExternal = true;

      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      renderer.render(createMockVideoFrame(1280, 720));
      await vi.advanceTimersByTimeAsync(17);

      renderer.render(createMockVideoFrame(1280, 720));
      await vi.advanceTimersByTimeAsync(17);

      expect(device.createTexture).toHaveBeenCalledTimes(1);
      renderer.destroy();
    });

    it('recreates staging texture when dimensions change', async () => {
      shouldThrowOnImportExternal = true;

      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      renderer.render(createMockVideoFrame(1280, 720));
      await vi.advanceTimersByTimeAsync(17);

      renderer.render(createMockVideoFrame(1920, 1080));
      await vi.advanceTimersByTimeAsync(17);

      expect(device.createTexture).toHaveBeenCalledTimes(2);
      expect(stagingTextures[0].destroy).toHaveBeenCalled();
      renderer.destroy();
    });

    it('uses fallback pipeline after first importExternalTexture failure', async () => {
      shouldThrowOnImportExternal = true;

      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      renderer.render(createMockVideoFrame(1280, 720));
      await vi.advanceTimersByTimeAsync(17);

      const initialCallCount = device.importExternalTexture.mock.calls.length;

      renderer.render(createMockVideoFrame(1280, 720));
      await vi.advanceTimersByTimeAsync(17);

      // After first failure, should skip importExternalTexture entirely
      expect(device.importExternalTexture.mock.calls.length).toBe(initialCallCount);
      renderer.destroy();
    });
  });

  describe('destroy', () => {
    it('cleans up all GPU resources', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      renderer.render(createMockVideoFrame());
      await vi.advanceTimersByTimeAsync(17);

      renderer.destroy();

      expect(device.destroy).toHaveBeenCalled();
      expect(canvasContext.unconfigure).toHaveBeenCalled();
    });

    it('cancels pending animation frame', async () => {
      const cancelSpy = vi.spyOn(window, 'cancelAnimationFrame');

      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      renderer.render(createMockVideoFrame());
      renderer.destroy();

      expect(cancelSpy).toHaveBeenCalled();
    });

    it('is safe to call multiple times', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      renderer.destroy();
      renderer.destroy();
      renderer.destroy();

      expect(device.destroy).toHaveBeenCalledTimes(1);
    });

    it('is safe to call without init', () => {
      const renderer = new WebGPURenderer();
      expect(() => renderer.destroy()).not.toThrow();
    });

    it('closes pending frame on destroy', async () => {
      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      const frame = createMockVideoFrame();
      const closeSpy = vi.spyOn(frame, 'close');
      renderer.render(frame);
      renderer.destroy();

      expect(closeSpy).toHaveBeenCalled();
    });

    it('destroys staging texture on destroy', async () => {
      shouldThrowOnImportExternal = true;

      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      renderer.render(createMockVideoFrame(1280, 720));
      await vi.advanceTimersByTimeAsync(17);

      renderer.destroy();

      expect(stagingTextures[0].destroy).toHaveBeenCalled();
    });
  });

  describe('device lost', () => {
    it('handles device lost gracefully — render becomes no-op', async () => {
      let lostCallback: ((info: { reason: string }) => void) | null = null;
      device.lost.then = vi.fn().mockImplementation((cb: (info: { reason: string }) => void) => {
        lostCallback = cb;
      });

      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      if (lostCallback) {
        lostCallback({ reason: 'destroyed' });
      }

      const frame = createMockVideoFrame();
      expect(() => renderer.render(frame)).not.toThrow();
      renderer.destroy();
    });

    it('closes pending frame when device is lost before rAF fires', async () => {
      let lostCallback: ((info: { reason: string }) => void) | null = null;
      device.lost.then = vi.fn().mockImplementation((cb: (info: { reason: string }) => void) => {
        lostCallback = cb;
      });

      const renderer = new WebGPURenderer();
      await renderer.init(document.createElement('canvas'));

      const frame = createMockVideoFrame();
      const closeSpy = vi.spyOn(frame, 'close');
      renderer.render(frame);

      if (lostCallback) {
        lostCallback({ reason: 'destroyed' });
      }

      await vi.advanceTimersByTimeAsync(17);

      expect(closeSpy).toHaveBeenCalled();
      renderer.destroy();
    });
  });
});
