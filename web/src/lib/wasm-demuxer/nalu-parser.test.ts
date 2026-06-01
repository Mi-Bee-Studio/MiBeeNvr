import { describe, it, expect } from 'vitest';
import {
  parseAccessUnit,
  isKeyframeNalus,
  InvalidNaluError,
  type NaluInfo,
  type H264NaluType,
  type H265NaluType,
} from './nalu-parser';

// ---------------------------------------------------------------------------
// Helper: build Annex B byte stream from NALUs
// ---------------------------------------------------------------------------

/** H.264: first byte = forbidden(1) | nal_ref_idc(2) | nal_unit_type(5). */
function h264Nalu(type: H264NaluType, payload = new Uint8Array([0xDE, 0xAD])): Uint8Array {
  const header = new Uint8Array([type & 0x3F]);
  const result = new Uint8Array(header.length + payload.length);
  result.set(header, 0);
  result.set(payload, header.length);
  return result;
}

/**
 * H.265: first two bytes = forbidden(1) | nal_unit_type(6) | nuh_layer_id(6) | nuh_temporal_id_plus1(3).
 * We set layer_id=0, temporal_id_plus1=1 for simplicity.
 */
function h265Nalu(type: H265NaluType, payload = new Uint8Array([0xCA, 0xFE])): Uint8Array {
  const header = new Uint8Array([(type << 1) & 0x7E, 0x01]);
  const result = new Uint8Array(header.length + payload.length);
  result.set(header, 0);
  result.set(payload, header.length);
  return result;
}

const SC4 = new Uint8Array([0x00, 0x00, 0x00, 0x01]); // 4-byte start code
const SC3 = new Uint8Array([0x00, 0x00, 0x01]);       // 3-byte start code

function annexB(...nalus: Uint8Array[]): Uint8Array {
  const parts: Uint8Array[] = [];
  nalus.forEach((n, i) => {
    parts.push(i === 0 ? SC4 : SC4); // always 4-byte for test clarity
    parts.push(n);
  });
  const total = parts.reduce((s, p) => s + p.length, 0);
  const result = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    result.set(p, off);
    off += p.length;
  }
  return result;
}

function annexBMixed(startCodes: ('3' | '4')[], nalus: Uint8Array[]): Uint8Array {
  const parts: Uint8Array[] = [];
  nalus.forEach((n, i) => {
    parts.push(startCodes[i] === '3' ? SC3 : SC4);
    parts.push(n);
  });
  const total = parts.reduce((s, p) => s + p.length, 0);
  const result = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    result.set(p, off);
    off += p.length;
  }
  return result;
}

// ---------------------------------------------------------------------------
// H.264 Annex B parsing
// ---------------------------------------------------------------------------
describe('parseAccessUnit — H.264 with Annex B', () => {
  it('should parse a single SPS NALU', () => {
    const data = annexB(h264Nalu(7));
    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(7);
    expect(result[0].isSPS).toBe(true);
    expect(result[0].isPPS).toBe(false);
    expect(result[0].isKeyframe).toBe(true);
    expect(result[0].data).toBeInstanceOf(Uint8Array);
    expect(result[0].data.length).toBe(3); // 1 byte header + 2 payload
  });

  it('should parse a single PPS NALU', () => {
    const data = annexB(h264Nalu(8));
    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(8);
    expect(result[0].isPPS).toBe(true);
    expect(result[0].isSPS).toBe(false);
    expect(result[0].isKeyframe).toBe(true);
  });

  it('should parse a single IDR NALU', () => {
    const data = annexB(h264Nalu(5));
    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(5);
    expect(result[0].isKeyframe).toBe(true);
    expect(result[0].isSPS).toBe(false);
    expect(result[0].isPPS).toBe(false);
  });

  it('should parse a single non-IDR slice (P-frame)', () => {
    const data = annexB(h264Nalu(1));
    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(1);
    expect(result[0].isKeyframe).toBe(false);
    expect(result[0].isSPS).toBe(false);
    expect(result[0].isPPS).toBe(false);
  });

  it('should parse an access unit with SPS + PPS + IDR', () => {
    const sps = h264Nalu(7, new Uint8Array([0x67, 0x42]));
    const pps = h264Nalu(8, new Uint8Array([0x68, 0xCE]));
    const idr = h264Nalu(5, new Uint8Array([0x65, 0xB8]));
    const data = annexB(sps, pps, idr);

    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(3);

    expect(result[0].type).toBe(7);
    expect(result[0].isSPS).toBe(true);
    expect(result[0].data[1]).toBe(0x67); // check payload preserved

    expect(result[1].type).toBe(8);
    expect(result[1].isPPS).toBe(true);

    expect(result[2].type).toBe(5);
    expect(result[2].isKeyframe).toBe(true);
  });

  it('should parse multiple non-keyframe slices', () => {
    const slice1 = h264Nalu(1);
    const slice2 = h264Nalu(1);
    const data = annexB(slice1, slice2);

    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(2);
    expect(result.every((n) => n.isKeyframe === false)).toBe(true);
  });

  it('should classify SEI as non-keyframe', () => {
    const data = annexB(h264Nalu(6));
    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(6);
    expect(result[0].isKeyframe).toBe(false);
  });

  it('should classify AUD (type 9) as non-keyframe', () => {
    const data = annexB(h264Nalu(9));
    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(9);
    expect(result[0].isKeyframe).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// H.265 Annex B parsing
// ---------------------------------------------------------------------------
describe('parseAccessUnit — H.265 with Annex B', () => {
  it('should parse a single VPS NALU', () => {
    const data = annexB(h265Nalu(32));
    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(32);
    expect(result[0].isVPS).toBe(true);
    expect(result[0].isSPS).toBe(false);
    expect(result[0].isPPS).toBe(false);
    expect(result[0].isKeyframe).toBe(true);
  });

  it('should parse a single SPS NALU', () => {
    const data = annexB(h265Nalu(33));
    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(33);
    expect(result[0].isSPS).toBe(true);
    expect(result[0].isVPS).toBe(false);
    expect(result[0].isKeyframe).toBe(true);
  });

  it('should parse a single PPS NALU', () => {
    const data = annexB(h265Nalu(34));
    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(34);
    expect(result[0].isPPS).toBe(true);
    expect(result[0].isKeyframe).toBe(true);
  });

  it('should parse a single IDR_W_RADL (type 19)', () => {
    const data = annexB(h265Nalu(19));
    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(19);
    expect(result[0].isKeyframe).toBe(true);
    expect(result[0].isSPS).toBe(false);
    expect(result[0].isPPS).toBe(false);
    expect(result[0].isVPS).toBe(false);
  });

  it('should parse a single IDR_N_LP (type 20) as keyframe', () => {
    const data = annexB(h265Nalu(20));
    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(20);
    expect(result[0].isKeyframe).toBe(true);
  });

  it('should parse a non-IDR slice (type 1) as non-keyframe', () => {
    const data = annexB(h265Nalu(1));
    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(1);
    expect(result[0].isKeyframe).toBe(false);
  });

  it('should parse a CRA_NUT (type 21) as keyframe', () => {
    const data = annexB(h265Nalu(21));
    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(21);
    expect(result[0].isKeyframe).toBe(true);
  });

  it('should parse BLA_N_LP (type 16) as keyframe', () => {
    const data = annexB(h265Nalu(16));
    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(16);
    expect(result[0].isKeyframe).toBe(true);
  });

  it('should parse an access unit with VPS + SPS + PPS + IDR', () => {
    const vps = h265Nalu(32, new Uint8Array([0x40, 0x01]));
    const sps = h265Nalu(33, new Uint8Array([0x42, 0x01]));
    const pps = h265Nalu(34, new Uint8Array([0x44, 0x01]));
    const idr = h265Nalu(19, new Uint8Array([0x26, 0x01]));
    const data = annexB(vps, sps, pps, idr);

    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(4);

    expect(result[0].type).toBe(32);
    expect(result[0].isVPS).toBe(true);
    expect(result[1].type).toBe(33);
    expect(result[1].isSPS).toBe(true);
    expect(result[2].type).toBe(34);
    expect(result[2].isPPS).toBe(true);
    expect(result[3].type).toBe(19);
    expect(result[3].isKeyframe).toBe(true);
  });

  it('should classify SEI (type 39) as non-keyframe', () => {
    const data = annexB(h265Nalu(39));
    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(39);
    expect(result[0].isKeyframe).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Annex B start code handling
// ---------------------------------------------------------------------------
describe('Annex B start code splitting', () => {
  it('should split on 4-byte start codes (00 00 00 01)', () => {
    const nalu1 = h264Nalu(1);
    const nalu2 = h264Nalu(5);
    const data = annexB(nalu1, nalu2); // all 4-byte

    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(2);
    expect(result[0].type).toBe(1);
    expect(result[1].type).toBe(5);
  });

  it('should split on 3-byte start codes (00 00 01)', () => {
    const nalu1 = h264Nalu(7);
    const nalu2 = h264Nalu(8);
    const data = annexBMixed(['3', '3'], [nalu1, nalu2]);

    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(2);
    expect(result[0].type).toBe(7);
    expect(result[1].type).toBe(8);
  });

  it('should handle mixed 3-byte and 4-byte start codes', () => {
    const nalu1 = h264Nalu(7);
    const nalu2 = h264Nalu(8);
    const nalu3 = h264Nalu(5);
    const data = annexBMixed(['4', '3', '4'], [nalu1, nalu2, nalu3]);

    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(3);
    expect(result[0].type).toBe(7);
    expect(result[1].type).toBe(8);
    expect(result[2].type).toBe(5);
  });

  it('should not confuse 00 00 01 pattern inside NALU data with start code', () => {
    // Build NALUs where payload contains bytes that look like start code fragments
    // but NOT preceded by 00 00 (true start code requires 00 00 00 01 or 00 00 01)
    const payload = new Uint8Array([0x00, 0x01, 0x02, 0x03]);
    const nalu1 = h264Nalu(1, payload);
    const nalu2 = h264Nalu(5, payload);
    const data = annexB(nalu1, nalu2);

    const result = parseAccessUnit(data, 'h264');
    expect(result).toHaveLength(2);
    expect(result[0].data.length).toBe(5); // 1 header + 4 payload
    expect(result[1].data.length).toBe(5);
  });
});

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------
describe('Edge cases', () => {
  it('should return empty array for empty data', () => {
    const result = parseAccessUnit(new Uint8Array(0), 'h264');
    expect(result).toHaveLength(0);
  });

  it('should return empty array for data shorter than 3 bytes', () => {
    expect(parseAccessUnit(new Uint8Array([0x00]), 'h264')).toHaveLength(0);
    expect(parseAccessUnit(new Uint8Array([0x00, 0x00]), 'h264')).toHaveLength(0);
  });

  it('should treat raw NALU without start codes as single NALU', () => {
    const rawNalu = h264Nalu(5, new Uint8Array([0xAA, 0xBB]));
    const result = parseAccessUnit(rawNalu, 'h264');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(5);
    expect(result[0].isKeyframe).toBe(true);
  });

  it('should handle single NALU without start codes for H.265', () => {
    const rawNalu = h265Nalu(19, new Uint8Array([0xCC, 0xDD]));
    const result = parseAccessUnit(rawNalu, 'h265');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(19);
    expect(result[0].isKeyframe).toBe(true);
  });

  it('should handle start code at end of data (trailing start code)', () => {
    // Data: start code + NALU + trailing start code with no data after
    const nalu = h264Nalu(5);
    const buf = new Uint8Array(SC4.length + nalu.length + SC4.length);
    buf.set(SC4, 0);
    buf.set(nalu, SC4.length);
    buf.set(SC4, SC4.length + nalu.length);

    const result = parseAccessUnit(buf, 'h264');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(5);
  });

  it('should handle data that is only start codes', () => {
    const buf = new Uint8Array([...SC4, ...SC4]);
    const result = parseAccessUnit(buf, 'h264');
    expect(result).toHaveLength(0);
  });

  it('should handle zero-length NALU between start codes', () => {
    // start code, zero bytes, start code, NALU
    const nalu = h264Nalu(5);
    const buf = new Uint8Array(SC4.length + SC4.length + nalu.length);
    buf.set(SC4, 0);
    buf.set(SC4, SC4.length);
    buf.set(nalu, SC4.length * 2);

    const result = parseAccessUnit(buf, 'h264');
    // The zero-length NALU should be skipped
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(5);
  });

  it('should handle data ending with incomplete 4-byte start code [0, 0, 0]', () => {
    // Valid NALU followed by truncated 4-byte start code (00 00 00)
    const nalu = h264Nalu(5);
    const buf = new Uint8Array(SC4.length + nalu.length + 3);
    buf.set(SC4, 0);
    buf.set(nalu, SC4.length);
    buf.set(new Uint8Array([0x00, 0x00, 0x00]), SC4.length + nalu.length);

    const result = parseAccessUnit(buf, 'h264');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(5);
  });

  it('should handle data ending with incomplete 3-byte start code [0, 0]', () => {
    // Valid NALU followed by truncated 3-byte start code (00 00)
    const nalu = h264Nalu(7);
    const buf = new Uint8Array(SC4.length + nalu.length + 2);
    buf.set(SC4, 0);
    buf.set(nalu, SC4.length);
    buf.set(new Uint8Array([0x00, 0x00]), SC4.length + nalu.length);

    const result = parseAccessUnit(buf, 'h264');
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(7);
  });
});

// ---------------------------------------------------------------------------
// Invalid NALU handling
// ---------------------------------------------------------------------------
describe('Invalid NALU handling', () => {
  it('InvalidNaluError should be throwable with correct name', () => {
    const err = new InvalidNaluError('test error');
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe('InvalidNaluError');
    expect(err.message).toBe('test error');
  });

  it('should skip short H.265 NALU (< 2 bytes) and continue with valid ones', () => {
    // Build stream: start-code + 1-byte invalid NALU + start-code + valid VPS (2 bytes)
    const invalidNalu = new Uint8Array([0xA5]); // 1 byte — too short for H.265
    const vps = h265Nalu(32); // valid VPS
    const data = annexBMixed(['4', '4'], [invalidNalu, vps]);

    const result = parseAccessUnit(data, 'h265');
    // Invalid NALU should be skipped, only VPS remains
    expect(result).toHaveLength(1);
    expect(result[0].type).toBe(32);
    expect(result[0].isVPS).toBe(true);
  });

  it('should return empty array when all NALUs are invalid (H.265)', () => {
    // Only invalid short NALUs between start codes
    const invalid1 = new Uint8Array([0xB0]);
    const invalid2 = new Uint8Array([0xC0]);
    const data = annexBMixed(['4', '4', '4'], [invalid1, invalid2, invalid1]);

    const result = parseAccessUnit(data, 'h265');
    expect(result).toHaveLength(0);
  });

  it('should not crash with mixed valid/invalid H.265 NALUs', () => {
    // Interleave invalid short NALUs with valid ones
    const invalid = new Uint8Array([0xA5]);
    const sps = h265Nalu(33);
    const pps = h265Nalu(34);
    const idr = h265Nalu(19);
    const data = annexBMixed(['4', '4', '4', '4', '4'], [invalid, sps, invalid, pps, idr]);

    const result = parseAccessUnit(data, 'h265');
    // Should have 3 valid NALUs, 2 invalid skipped
    expect(result).toHaveLength(3);
    expect(result[0].type).toBe(33);
    expect(result[1].type).toBe(34);
    expect(result[2].type).toBe(19);
    expect(result[2].isKeyframe).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// isKeyframeNalus
// ---------------------------------------------------------------------------
describe('isKeyframeNalus', () => {
  it('should return true for H.264 IDR', () => {
    const nalus: NaluInfo[] = [{ type: 5, data: new Uint8Array(), isKeyframe: true, isSPS: false, isPPS: false, isVPS: false }];
    expect(isKeyframeNalus(nalus)).toBe(true);
  });

  it('should return true for H.264 SPS', () => {
    const nalus: NaluInfo[] = [{ type: 7, data: new Uint8Array(), isKeyframe: true, isSPS: true, isPPS: false, isVPS: false }];
    expect(isKeyframeNalus(nalus)).toBe(true);
  });

  it('should return false for P-frame slices', () => {
    const nalus: NaluInfo[] = [{ type: 1, data: new Uint8Array(), isKeyframe: false, isSPS: false, isPPS: false, isVPS: false }];
    expect(isKeyframeNalus(nalus)).toBe(false);
  });

  it('should return true for H.265 IDR', () => {
    const nalus: NaluInfo[] = [{ type: 19, data: new Uint8Array(), isKeyframe: true, isSPS: false, isPPS: false, isVPS: false }];
    expect(isKeyframeNalus(nalus)).toBe(true);
  });

  it('should return true for H.265 VPS', () => {
    const nalus: NaluInfo[] = [{ type: 32, data: new Uint8Array(), isKeyframe: true, isSPS: false, isPPS: false, isVPS: true }];
    expect(isKeyframeNalus(nalus)).toBe(true);
  });

  it('should return true for H.265 CRA', () => {
    const nalus: NaluInfo[] = [{ type: 21, data: new Uint8Array(), isKeyframe: true, isSPS: false, isPPS: false, isVPS: false }];
    expect(isKeyframeNalus(nalus)).toBe(true);
  });

  it('should return false for empty array', () => {
    expect(isKeyframeNalus([])).toBe(false);
  });

  it('should return true if any NALU is a keyframe', () => {
    const nalus: NaluInfo[] = [
      { type: 1, data: new Uint8Array(), isKeyframe: false, isSPS: false, isPPS: false, isVPS: false },
      { type: 5, data: new Uint8Array(), isKeyframe: true, isSPS: false, isPPS: false, isVPS: false },
    ];
    expect(isKeyframeNalus(nalus)).toBe(true);
  });

  it('should return false when all NALUs are non-keyframe', () => {
    const nalus: NaluInfo[] = [
      { type: 1, data: new Uint8Array(), isKeyframe: false, isSPS: false, isPPS: false, isVPS: false },
      { type: 6, data: new Uint8Array(), isKeyframe: false, isSPS: false, isPPS: false, isVPS: false },
    ];
    expect(isKeyframeNalus(nalus)).toBe(false);
  });
});
