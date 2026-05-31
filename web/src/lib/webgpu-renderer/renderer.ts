/**
 * WebGPU VideoFrame Renderer
 *
 * Renders WebCodecs VideoFrame to HTMLCanvasElement via WebGPU.
 * Two rendering paths:
 *   1. Zero-copy: GPUExternalTexture via importExternalTexture (preferred)
 *   2. Fallback:  copyExternalImageToTexture → staging GPUTexture
 *
 * The YUV→RGB conversion is handled by the browser when using texture_external
 * with textureSampleBaseClampToEdge(). The fallback path uses texture_2d with
 * standard texture sampling.
 *
 * Render loop: requestAnimationFrame coalesces multiple frames per vsync.
 * VideoFrame ownership: the renderer takes ownership and closes frames after use.
 */

// ─── WGSL Shaders ────────────────────────────────────────────────────────────

/** Shader for GPUExternalTexture (zero-copy path). */
const EXTERNAL_SHADER_CODE: string = /* wgsl */ `
struct VertexOutput {
  @builtin(position) position: vec4f,
  @location(0) texcoord: vec2f,
};

@vertex
fn vs(@builtin(vertex_index) vi: u32) -> VertexOutput {
  var positions = array<vec2f, 6>(
    vec2f(-1.0, -1.0), vec2f( 1.0, -1.0), vec2f(-1.0,  1.0),
    vec2f(-1.0,  1.0), vec2f( 1.0, -1.0), vec2f( 1.0,  1.0),
  );
  let p = positions[vi];
  var output: VertexOutput;
  output.position = vec4f(p, 0.0, 1.0);
  output.texcoord = vec2f(p.x * 0.5 + 0.5, 0.5 - p.y * 0.5);
  return output;
}

@group(0) @binding(0) var ourSampler: sampler;
@group(0) @binding(1) var ourTexture: texture_external;

@fragment
fn fs(input: VertexOutput) -> @location(0) vec4f {
  return textureSampleBaseClampToEdge(ourTexture, ourSampler, input.texcoord);
}
`;

/** Fallback shader for regular texture_2d (copyExternalImageToTexture path). */
const FALLBACK_SHADER_CODE: string = /* wgsl */ `
struct VertexOutput {
  @builtin(position) position: vec4f,
  @location(0) texcoord: vec2f,
};

@vertex
fn vs(@builtin(vertex_index) vi: u32) -> VertexOutput {
  var positions = array<vec2f, 6>(
    vec2f(-1.0, -1.0), vec2f( 1.0, -1.0), vec2f(-1.0,  1.0),
    vec2f(-1.0,  1.0), vec2f( 1.0, -1.0), vec2f( 1.0,  1.0),
  );
  let p = positions[vi];
  var output: VertexOutput;
  output.position = vec4f(p, 0.0, 1.0);
  output.texcoord = vec2f(p.x * 0.5 + 0.5, 0.5 - p.y * 0.5);
  return output;
}

@group(0) @binding(0) var ourSampler: sampler;
@group(0) @binding(1) var ourTexture: texture_2d<f32>;

@fragment
fn fs(input: VertexOutput) -> @location(0) vec4f {
  return textureSample(ourTexture, ourSampler, input.texcoord);
}
`;

// ─── Renderer ─────────────────────────────────────────────────────────────────
const FRAGMENT_SHADER_STAGE = 16; // GPUShaderStage.FRAGMENT

export class WebGPURenderer {
  private device: GPUDevice | null = null;
  private context: GPUCanvasContext | null = null;
  private format: GPUTextureFormat = 'bgra8unorm';
  private canvas: HTMLCanvasElement | null = null;

  private pipeline: GPURenderPipeline | null = null;
  private fallbackPipeline: GPURenderPipeline | null = null;
  private sampler: GPUSampler | null = null;

  private stagingTexture: GPUTexture | null = null;
  private stagingWidth = 0;
  private stagingHeight = 0;

  private destroyed = false;
  private deviceLost = false;
  private animationFrameId: number | null = null;
  private pendingFrame: VideoFrame | null = null;
  private useExternalTexture = true;

  private onDeviceLostCallback: (() => void) | null = null;

  constructor(onDeviceLost?: () => void) {
    this.onDeviceLostCallback = onDeviceLost || null;
  }

  /**
   * Initialize WebGPU device, canvas context, render pipelines, and sampler.
   * Returns true on success, false if WebGPU is unavailable or init fails.
   */
  async init(canvas: HTMLCanvasElement): Promise<boolean> {
    this.canvas = canvas;

    try {
      const gpu = (navigator as Record<string, unknown>).gpu as GPU | undefined;
      if (!gpu) return false;

      const adapter = await gpu.requestAdapter();
      if (!adapter) return false;

      const device = await adapter.requestDevice();
      if (!device) return false;

      // Detect preferred format BEFORE touching canvas context
      const format = navigator.gpu.getPreferredCanvasFormat();

      const externalModule = device.createShaderModule({ code: EXTERNAL_SHADER_CODE });
      const fallbackModule = device.createShaderModule({ code: FALLBACK_SHADER_CODE });

      const bindGroupLayoutEntries = [
        { binding: 0, visibility: FRAGMENT_SHADER_STAGE, sampler: {} },
        { binding: 1, visibility: FRAGMENT_SHADER_STAGE, externalTexture: {} },
      ];
      const fallbackBindGroupLayoutEntries = [
        { binding: 0, visibility: FRAGMENT_SHADER_STAGE, sampler: {} },
        { binding: 1, visibility: FRAGMENT_SHADER_STAGE, texture: {} },
      ];

      const pipeline = device.createRenderPipeline({
        layout: device.createBindGroupLayout({ entries: bindGroupLayoutEntries }),
        vertex: { module: externalModule, entryPoint: 'vs' },
        fragment: {
          module: externalModule,
          entryPoint: 'fs',
          targets: [{ format }],
        },
        primitive: { topology: 'triangle-list' },
      });

      const fallbackPipeline = device.createRenderPipeline({
        layout: device.createBindGroupLayout({ entries: fallbackBindGroupLayoutEntries }),
        vertex: { module: fallbackModule, entryPoint: 'vs' },
        fragment: {
          module: fallbackModule,
          entryPoint: 'fs',
          targets: [{ format }],
        },
        primitive: { topology: 'triangle-list' },
      });

      const sampler = device.createSampler({
        magFilter: 'linear',
        minFilter: 'linear',
        addressModeU: 'clamp-to-edge',
        addressModeV: 'clamp-to-edge',
      });

      // All resources created successfully — NOW claim the canvas context.
      // This must be the LAST step so that on failure, the canvas is untouched
      // and remains available for WebGL2 fallback.
      const ctx = canvas.getContext('webgpu') as GPUCanvasContext | null;
      if (!ctx) {
        // Extremely unlikely: all GPU resources created but context unavailable.
        // Clean up device resources.
        device.destroy();
        return false;
      }

      this.device = device;
      this.context = ctx;
      this.format = format;
      this.pipeline = pipeline;
      this.fallbackPipeline = fallbackPipeline;
      this.sampler = sampler;

      device.lost.then((info: GPUDeviceLostInfo) => {
        this.deviceLost = true;
        this.onDeviceLostCallback?.();
      });

      ctx.configure({
        device,
        format: this.format,
        alphaMode: 'opaque',
      });

      return true;
    } catch {
      return false;
    }
  }

  /**
   * Queue a VideoFrame for rendering. Takes ownership — closes the frame
   * after rendering or if superseded by a newer frame.
   *
   * Uses requestAnimationFrame for vsync-aligned rendering.
   * Multiple frames before the next vsync: only the latest is rendered.
   */
  render(videoFrame: VideoFrame): void {
    if (this.destroyed || this.deviceLost || !this.device || !this.context || !this.canvas) {
      videoFrame.close();
      return;
    }

    if (this.pendingFrame) {
      this.pendingFrame.close();
    }
    this.pendingFrame = videoFrame;

    if (this.animationFrameId === null) {
      this.animationFrameId = requestAnimationFrame(() => this.renderLoop());
    }
  }

  private renderLoop(): void {
    this.animationFrameId = null;

    if (this.destroyed || this.deviceLost || !this.device || !this.context || !this.canvas) {
      if (this.pendingFrame) {
        this.pendingFrame.close();
        this.pendingFrame = null;
      }
      return;
    }

    const frame = this.pendingFrame;
    this.pendingFrame = null;

    if (!frame) return;

    try {
      this.doRender(frame);
    } catch {
      // Render failure — frame is still closed in finally
    } finally {
      frame.close();
    }

    if (this.pendingFrame) {
      this.animationFrameId = requestAnimationFrame(() => this.renderLoop());
    }
  }

  private doRender(frame: VideoFrame): void {
    const device = this.device!;
    const ctx = this.context!;
    const canvas = this.canvas!;

    if (canvas.width !== frame.displayWidth || canvas.height !== frame.displayHeight) {
      canvas.width = frame.displayWidth;
      canvas.height = frame.displayHeight;
    }

    const textureView = ctx.getCurrentTexture().createView();
    const encoder = device.createCommandEncoder();

    if (this.useExternalTexture) {
      this.prepareExternalTexture(device, frame);
    }

    if (!this.useExternalTexture) {
      this.prepareFallbackTexture(device, frame, encoder);
    }

    const pass = encoder.beginRenderPass({
      colorAttachments: [
        {
          view: textureView,
          clearValue: { r: 0, g: 0, b: 0, a: 1 },
          loadOp: 'clear',
          storeOp: 'store',
        },
      ],
    });

    if (this.useExternalTexture) {
      this.drawExternalTexture(device, frame, pass);
    } else {
      this.drawFallbackTexture(device, pass);
    }

    pass.end();
    const commandBuffer = encoder.finish();
    device.queue.submit([commandBuffer]);
  }

  private externalTexture: GPUExternalTexture | null = null;

  private prepareExternalTexture(device: GPUDevice, frame: VideoFrame): void {
    try {
      this.externalTexture = device.importExternalTexture({ source: frame });
    } catch {
      this.useExternalTexture = false;
      this.externalTexture = null;
    }
  }

  private prepareFallbackTexture(device: GPUDevice, frame: VideoFrame, encoder: GPUCommandEncoder): void {
    const w = frame.displayWidth;
    const h = frame.displayHeight;

    if (!this.stagingTexture || this.stagingWidth !== w || this.stagingHeight !== h) {
      if (this.stagingTexture) {
        this.stagingTexture.destroy();
      }
      this.stagingTexture = device.createTexture({
        size: [w, h],
        format: 'rgba8unorm',
        usage: GPUTextureUsage.RENDER_ATTACHMENT | GPUTextureUsage.COPY_DST | GPUTextureUsage.TEXTURE_BINDING,
      });
      this.stagingWidth = w;
      this.stagingHeight = h;
    }

    encoder.copyExternalImageToTexture(
      { source: frame },
      { texture: this.stagingTexture },
      [w, h],
    );
  }

  private drawExternalTexture(device: GPUDevice, frame: VideoFrame, pass: GPURenderPassEncoder): void {
    if (!this.externalTexture) {
      this.drawFallbackTexture(device, pass);
      return;
    }

    try {
      const bindGroup = device.createBindGroup({
        layout: this.pipeline!.getBindGroupLayout(0),
        entries: [
          { binding: 0, resource: this.sampler! },
          { binding: 1, resource: this.externalTexture },
        ],
      });

      pass.setPipeline(this.pipeline!);
      pass.setBindGroup(0, bindGroup);
      pass.draw(6);
    } finally {
      this.externalTexture.destroy();
      this.externalTexture = null;
    }
  }

  private drawFallbackTexture(device: GPUDevice, pass: GPURenderPassEncoder): void {
    const bindGroup = device.createBindGroup({
      layout: this.fallbackPipeline!.getBindGroupLayout(0),
      entries: [
        { binding: 0, resource: this.sampler! },
        { binding: 1, resource: this.stagingTexture!.createView() },
      ],
    });

    pass.setPipeline(this.fallbackPipeline!);
    pass.setBindGroup(0, bindGroup);
    pass.draw(6);
  }

  /**
   * Release all GPU resources and stop the render loop.
   * Safe to call multiple times.
   */
  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;

    if (this.animationFrameId !== null) {
      cancelAnimationFrame(this.animationFrameId);
      this.animationFrameId = null;
    }

    if (this.pendingFrame) {
      this.pendingFrame.close();
      this.pendingFrame = null;
    }

    if (this.stagingTexture) {
      this.stagingTexture.destroy();
      this.stagingTexture = null;
    }

    if (this.context) {
      this.context.unconfigure();
      this.context = null;
    }

    if (this.device) {
      this.device.destroy();
      this.device = null;
    }

    this.pipeline = null;
    this.fallbackPipeline = null;
    this.sampler = null;
    this.canvas = null;
  }
}
