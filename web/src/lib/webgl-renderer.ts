// Minimal WebGL2 YUV/RGBA frame renderer used by the WebCodecs/WASM player.
// Extracted from WasmPlayer.svelte so the GL boilerplate (shader compile,
// quad setup, texture upload) is reusable and testable independently of the
// component's connection/decode state machine.

export class Webgl2Renderer {
  private gl: WebGL2RenderingContext | null = null;
  private program: WebGLProgram | null = null;
  private texture: WebGLTexture | null = null;
  private vao: WebGLVertexArrayObject | null = null;

  init(canvas: HTMLCanvasElement): boolean {
    const ctx = canvas.getContext('webgl2', {
      alpha: false,
      antialias: false,
      depth: false,
      stencil: false,
      preserveDrawingBuffer: true, // needed for freeze-frame capture
    });
    if (!ctx) return false;
    this.gl = ctx;

    // Vertex shader — full-screen quad
    const vsSource = `#version 300 es
      in vec2 aPosition;
      out vec2 vTexCoord;
      void main() {
        // Map [-1,1] quad to [0,1] texture coords
        vTexCoord = aPosition * 0.5 + 0.5;
        gl_Position = vec4(aPosition, 0.0, 1.0);
      }
    `;

    // Fragment shader — sample VideoFrame texture
    const fsSource = `#version 300 es
      precision mediump float;
      in vec2 vTexCoord;
      uniform sampler2D uTexture;
      out vec4 fragColor;
      void main() {
        // Flip Y coordinate (WebGL origin is bottom-left, video is top-left)
        fragColor = texture(uTexture, vec2(vTexCoord.x, 1.0 - vTexCoord.y));
      }
    `;

    const vs = this.compileShader(ctx, ctx.VERTEX_SHADER, vsSource);
    const fs = this.compileShader(ctx, ctx.FRAGMENT_SHADER, fsSource);
    if (!vs || !fs) return false;

    const program = ctx.createProgram()!;
    ctx.attachShader(program, vs);
    ctx.attachShader(program, fs);
    ctx.linkProgram(program);
    if (!ctx.getProgramParameter(program, ctx.LINK_STATUS)) {
      if (import.meta.env.DEV) console.warn('WebGL2 program link failed:', ctx.getProgramInfoLog(program));
      return false;
    }
    this.program = program;

    // Full-screen quad (two triangles)
    const vertices = new Float32Array([-1, -1, 1, -1, -1, 1, -1, 1, 1, -1, 1, 1]);

    const vao = ctx.createVertexArray()!;
    this.vao = vao;
    ctx.bindVertexArray(vao);

    const vbo = ctx.createBuffer()!;
    ctx.bindBuffer(ctx.ARRAY_BUFFER, vbo);
    ctx.bufferData(ctx.ARRAY_BUFFER, vertices, ctx.STATIC_DRAW);

    const aPos = ctx.getAttribLocation(program, 'aPosition');
    ctx.enableVertexAttribArray(aPos);
    ctx.vertexAttribPointer(aPos, 2, ctx.FLOAT, false, 0, 0);

    // Texture for VideoFrame
    this.texture = ctx.createTexture();
    ctx.bindTexture(ctx.TEXTURE_2D, this.texture);
    ctx.texParameteri(ctx.TEXTURE_2D, ctx.TEXTURE_WRAP_S, ctx.CLAMP_TO_EDGE);
    ctx.texParameteri(ctx.TEXTURE_2D, ctx.TEXTURE_WRAP_T, ctx.CLAMP_TO_EDGE);
    ctx.texParameteri(ctx.TEXTURE_2D, ctx.TEXTURE_MIN_FILTER, ctx.LINEAR);
    ctx.texParameteri(ctx.TEXTURE_2D, ctx.TEXTURE_MAG_FILTER, ctx.LINEAR);

    ctx.bindVertexArray(null);

    return true;
  }

  private compileShader(glCtx: WebGL2RenderingContext, type: number, source: string): WebGLShader | null {
    const shader = glCtx.createShader(type);
    if (!shader) return null;
    glCtx.shaderSource(shader, source);
    glCtx.compileShader(shader);
    if (!glCtx.getShaderParameter(shader, glCtx.COMPILE_STATUS)) {
      if (import.meta.env.DEV) console.warn('WebGL2 shader compile failed:', glCtx.getShaderInfoLog(shader));
      glCtx.deleteShader(shader);
      return null;
    }
    return shader;
  }

  /** Render a WebCodecs VideoFrame. Resizes the canvas when the frame size changes. */
  renderFrame(canvas: HTMLCanvasElement, frame: VideoFrame, onResize?: (width: number, height: number) => void): void {
    const gl = this.gl;
    if (!gl || !this.program || !this.texture || !this.vao) return;

    // Resize canvas to match frame if needed
    if (canvas.width !== frame.displayWidth || canvas.height !== frame.displayHeight) {
      canvas.width = frame.displayWidth;
      canvas.height = frame.displayHeight;
      onResize?.(frame.displayWidth, frame.displayHeight);
      gl.viewport(0, 0, canvas.width, canvas.height);
    }

    gl.useProgram(this.program);

    // Upload VideoFrame directly to texture — WebGL2 supports VideoFrame in texImage2D
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, this.texture);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, frame);

    this.drawQuad(gl);
  }

  /**
   * Blit a source canvas (the offscreen WASM 2D canvas) onto this renderer's
   * canvas — the WebGL2 branch of the WASM playback path.
   */
  renderCanvasSource(
    canvas: HTMLCanvasElement,
    source: HTMLCanvasElement,
    width: number,
    height: number,
    onResize?: (w: number, h: number) => void,
  ): void {
    const gl = this.gl;
    if (!gl || !this.program || !this.texture || !this.vao) return;
    if (canvas.width !== width || canvas.height !== height) {
      canvas.width = width;
      canvas.height = height;
      onResize?.(width, height);
      gl.viewport(0, 0, canvas.width, canvas.height);
    }
    gl.useProgram(this.program);
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, this.texture);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, source);
    this.drawQuad(gl);
  }

  private drawQuad(gl: WebGL2RenderingContext): void {
    gl.bindVertexArray(this.vao);
    gl.drawArrays(gl.TRIANGLES, 0, 6);
    gl.bindVertexArray(null);
  }

  cleanup(): void {
    const gl = this.gl;
    if (gl) {
      if (this.texture) {
        gl.deleteTexture(this.texture);
        this.texture = null;
      }
      if (this.program) {
        gl.deleteProgram(this.program);
        this.program = null;
      }
      if (this.vao) {
        gl.deleteVertexArray(this.vao);
        this.vao = null;
      }
      this.gl = null;
    }
  }
}
