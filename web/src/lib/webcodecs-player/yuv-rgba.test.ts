import { describe, expect, it } from 'vitest';
import { yuv420ToRgba } from './yuv-rgba';

// Build a YUV420P frame with per-plane strides and poison bytes in the
// padding columns — a tight-stride read would pick the poison up.
function frame(width: number, height: number, yStride: number, uvStride: number) {
  const y = new Uint8Array(yStride * height).fill(0xaa); // padding poison
  const u = new Uint8Array(uvStride * (height >> 1)).fill(0xaa);
  const v = new Uint8Array(uvStride * (height >> 1)).fill(0xaa);
  // Neutral chroma (128) → grayscale output driven purely by Y.
  for (let row = 0; row < height >> 1; row++) {
    for (let col = 0; col < width >> 1; col++) {
      u[row * uvStride + col] = 128;
      v[row * uvStride + col] = 128;
    }
  }
  return { y, u, v };
}

describe('yuv420ToRgba strides', () => {
  it('reads luma with the provided yStride (padding ignored)', () => {
    // 4x4, yStride 8 (4 padding bytes per row): row 0 dark, row 3 bright.
    const { y, u, v } = frame(4, 4, 8, 4);
    for (let col = 0; col < 4; col++) {
      y[col] = 16; // near-black
      y[3 * 8 + col] = 235; // near-white
    }
    const rgba = yuv420ToRgba(y, u, v, 4, 4, 8, 4, 4);
    expect(rgba[0]).toBeLessThanOrEqual(5); // row 0 ≈ black
    expect(rgba[(3 * 4 + 0) * 4]).toBeGreaterThanOrEqual(250); // row 3 ≈ white
    // Alpha always opaque.
    expect(rgba[3]).toBe(255);
  });

  it('handles independent chroma strides (u vs v)', () => {
    // 4x4 with different U/V strides and distinct chroma so a wrong-stride
    // read would flip the color.
    const { y, u, v } = frame(4, 4, 4, 6);
    for (let col = 0; col < 4; col++) y[col] = 128; // mid gray
    for (let row = 0; row < 2; row++)
      for (let col = 0; col < 2; col++) {
        u[row * 6 + col] = 180; // strong Cb → blue-ish
        v[row * 6 + col] = 90; // weak Cr
      }
    const rgba = yuv420ToRgba(y, u, v, 4, 4, 4, 6, 6);
    // With Cb>128 and Cr<128, blue must exceed red.
    expect(rgba[2]).toBeGreaterThan(rgba[0]);
  });

  it('defaults to tight strides (backwards compatibility)', () => {
    const { y, u, v } = frame(2, 2, 1, 1); // strides below width on purpose
    for (let col = 0; col < 2; col++) y[col] = 235;
    u[0] = 128;
    v[0] = 128;
    // Tight default: width 2 → yStride 2 — write via the same assumption.
    const y2 = new Uint8Array([235, 235, 235, 235]);
    const u2 = new Uint8Array([128]);
    const v2 = new Uint8Array([128]);
    const rgba = yuv420ToRgba(y2, u2, v2, 2, 2);
    expect(rgba[0]).toBeGreaterThan(240);
    expect(rgba.length).toBe(2 * 2 * 4);
  });
});
