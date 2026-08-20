/**
 * YUV420P to RGBA conversion.
 *
 * Converts planar YUV (Y+Cb+Cr) data to interleaved RGBA pixels.
 * Uses BT.601 full-range conversion coefficients.
 * Output is suitable for WebGL2 texImage2D or VideoFrame construction.
 */

/**
 * Convert YUV420P planar data to RGBA.
 *
 * @param yPlane  Luma plane (full resolution)
 * @param uPlane  Cb plane (half width/height for 420)
 * @param vPlane  Cr plane (half width/height for 420)
 * @param width   Frame width in pixels
 * @param height  Frame height in pixels
 * @returns Uint8ClampedArray of RGBA pixels (width * height * 4 bytes)
 */
export function yuv420ToRgba(
  yPlane: Uint8Array,
  uPlane: Uint8Array,
  vPlane: Uint8Array,
  width: number,
  height: number,
  yStride: number = width,
  uStride: number = width >> 1,
  vStride: number = width >> 1,
): Uint8ClampedArray {
  // Strides are the ROW BYTE LENGTHS of the source planes — decoders may pad
  // rows beyond width (alignment). Passing the real stride is mandatory for
  // padded planes; the defaults keep the historical tight-packing behavior.
  const rgba = new Uint8ClampedArray(width * height * 4);

  for (let row = 0; row < height; row++) {
    const yRowOffset = row * yStride;
    const uvRowOffset = (row >> 1) * uStride;
    const rgbaRowOffset = row * width * 4;

    for (let col = 0; col < width; col++) {
      const yIdx = yRowOffset + col;
      const uvIdx = uvRowOffset + (col >> 1);

      // BT.601 full-range YUV → RGB
      const y = yPlane[yIdx] - 16;
      const u = uPlane[uvIdx] - 128;
      const v = vPlane[(row >> 1) * vStride + (col >> 1)] - 128;

      let r = 1.164 * y + 1.596 * v;
      let g = 1.164 * y - 0.392 * u - 0.813 * v;
      let b = 1.164 * y + 2.017 * u;

      // Clamp to [0, 255]
      r = r < 0 ? 0 : r > 255 ? 255 : r;
      g = g < 0 ? 0 : g > 255 ? 255 : g;
      b = b < 0 ? 0 : b > 255 ? 255 : b;

      const outIdx = rgbaRowOffset + col * 4;
      rgba[outIdx] = r;
      rgba[outIdx + 1] = g;
      rgba[outIdx + 2] = b;
      rgba[outIdx + 3] = 255; // Alpha
    }
  }

  return rgba;
}
