/**
 * NALU Parser — extracts and classifies H.264 / H.265 NAL units from Annex B byte streams.
 *
 * Designed for WebSocket binary transport where the backend (StreamHub) delivers
 * raw NALUs. This parser handles:
 *   - Annex B start code splitting (4-byte `00 00 00 01` and 3-byte `00 00 01`)
 *   - H.264 NAL unit type classification (type in bits 0-4 of first byte)
 *   - H.265 NAL unit type classification (type in bits 1-6 of first two bytes)
 *   - Keyframe / SPS / PPS / VPS detection
 *
 * Zero external dependencies — pure TypeScript.
 */

// ─── Types ─────────────────────────────────────────────────────────────────

/** H.264 NAL unit type (0-28 defined by spec, 24-31 unspecified). */
export type H264NaluType =
  | 0  // Unspecified
  | 1  // Non-IDR slice
  | 2  // Slice data A
  | 3  // Slice data B
  | 4  // Slice data C
  | 5  // IDR slice
  | 6  // SEI
  | 7  // SPS
  | 8  // PPS
  | 9  // AUD
  | 10 // End of sequence
  | 11 // End of stream
  | 12 // Filler
  | 13 // SPS extension
  | 14 // Prefix NALU
  | 15 // Subset SPS
  | 19 // Coded slice of auxiliary coded picture
  | 20 // Coded slice extension
  | 21 // Coded slice extension for depth view
  | 22 | 23 | 24 | 25 | 26 | 27 | 28 | 29 | 30 | 31;

/** H.265 NAL unit type (0-63). */
export type H265NaluType =
  | 0  // TRAIL_N
  | 1  // TRAIL_R
  | 2  // TSA_N
  | 3  // TSA_R
  | 4  // STSA_N
  | 5  // STSA_R
  | 6  // RADL_N
  | 7  // RADL_R
  | 8  // RASL_N
  | 9  // RASL_R
  | 10 // Reserved
  | 11 | 12 | 13 | 14 | 15 // Reserved
  | 16 // BLA_W_LP
  | 17 // BLA_W_RADL
  | 18 // BLA_N_LP
  | 19 // IDR_W_RADL
  | 20 // IDR_N_LP
  | 21 // CRA_NUT
  | 22 | 23 // Reserved
  | 24 // RSV_IRAP_VCL23
  | 25 | 26 | 27 | 28 | 29 | 30 | 31
  | 32 // VPS
  | 33 // SPS
  | 34 // PPS
  | 35 // AUD
  | 36 // EOS
  | 37 // EOB
  | 38 // Filler
  | 39 // SEI prefixed
  | 40 // SEI suffix
  | 41 | 42 | 43 | 44 | 45 | 46 | 47 // Reserved
  | 48 | 49 | 50 | 51 | 52 | 53 | 54 | 55 | 56 | 57 | 58 | 59 | 60 | 61 | 62 | 63;

/** Parsed NAL unit metadata and raw data. */
export interface NaluInfo {
  /** NAL unit type (H.264 or H.265 depending on codec). */
  type: number;
  /** Raw NAL unit data (without start codes). */
  data: Uint8Array;
  /** Whether this NALU belongs to a keyframe access unit. */
  isKeyframe: boolean;
  /** True for H.264 SPS (type 7) or H.265 SPS (type 33). */
  isSPS: boolean;
  /** True for H.264 PPS (type 8) or H.265 PPS (type 34). */
  isPPS: boolean;
  /** True for H.265 VPS (type 32). Always false for H.264. */
  isVPS: boolean;
}

/** Codec identifier — matches protocol.ts CodecId values. */
export type Codec = 'h264' | 'h265';

// ─── Error Classes ───────────────────────────────────────────────────────────

/** Error thrown when a NAL unit has invalid/corrupted data (too short to extract type). */
export class InvalidNaluError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'InvalidNaluError';
  }
}


// ─── H.264 Keyframe NAL Types ──────────────────────────────────────────────

const H264_KEYFRAME_TYPES = new Set<number>([5, 7, 8]); // IDR, SPS, PPS
const H264_SPS_TYPE = 7;
const H264_PPS_TYPE = 8;

// ─── H.265 Keyframe NAL Types ──────────────────────────────────────────────

const H265_KEYFRAME_TYPES = new Set<number>([
  16, 17, 18,            // BLA variants
  19, 20,                // IDR variants
  21,                    // CRA
  32, 33, 34,            // VPS, SPS, PPS
]);
const H265_VPS_TYPE = 32;
const H265_SPS_TYPE = 33;
const H265_PPS_TYPE = 34;

// ─── Annex B Start Code Constants ────────────────────────────────────────────

const START_CODE_4 = new Uint8Array([0x00, 0x00, 0x00, 0x01]);
const START_CODE_3 = new Uint8Array([0x00, 0x00, 0x01]);

// ─── Annex B Splitting ─────────────────────────────────────────────────────

/**
 * Split Annex B byte stream into individual NAL unit payloads (without start codes).
 * Handles both 4-byte (00 00 00 01) and 3-byte (00 00 01) start codes.
 */
function splitAnnexB(data: Uint8Array): Uint8Array[] {
  if (data.length < 3) return [];

  // Check if data contains any start codes
  let hasStartCode = false;
  for (let i = 0; i <= data.length - 3; i++) {
    if (data[i] === 0 && data[i + 1] === 0 && (data[i + 2] === 1 || (data[i + 2] === 0 && i + 3 < data.length && data[i + 3] === 1))) {
      hasStartCode = true;
      break;
    }
  }

  if (!hasStartCode) {
    // No start codes — treat entire data as a single raw NALU
    if (data.length === 0) return [];
    return [data];
  }

  const nalus: Uint8Array[] = [];
  let naluStart = -1;  // Start of NALU payload (after start code)
  let i = 0;

  // Skip leading zero bytes before first start code
  while (i < data.length - 2 && data[i] === 0 && data[i + 1] === 0) {
    // Check if we found a start code
    if (data[i + 2] === 1) {
      naluStart = i + 3;
      i += 3;
      break;
    }
    if (data[i + 2] === 0 && i + 3 < data.length && data[i + 3] === 1) {
      naluStart = i + 4;
      i += 4;
      break;
    }
    i++;
  }

  if (naluStart === -1) return [];

  // Scan for start codes
  while (i < data.length) {
    // Look for 00 00 00 01 or 00 00 01
    let found = false;
    if (i <= data.length - 3 && data[i] === 0 && data[i + 1] === 0) {
      if (i + 2 >= data.length) break;
      if (data[i + 2] === 1) {
        // 3-byte start code
        if (naluStart < i) {
          nalus.push(data.subarray(naluStart, i));
        }
        naluStart = i + 3;
        i += 3;
        found = true;
      } else if (i <= data.length - 4 && data[i + 2] === 0) {
        if (i + 3 >= data.length) break;
        if (data[i + 3] === 1) {
          // 4-byte start code
          if (naluStart < i) {
            nalus.push(data.subarray(naluStart, i));
          }
          naluStart = i + 4;
          i += 4;
          found = true;
        }
      }
    }

    if (!found) i++;
  }

  // Emit the last NALU (everything after the last start code)
  if (naluStart < data.length) {
    nalus.push(data.subarray(naluStart));
  }

  // Filter out zero-length NALUs
  return nalus.filter((n) => n.length > 0);
}

// ─── H.264 NAL Type Extraction ─────────────────────────────────────────────

/**
 * Extract H.264 NAL unit type from the first byte of the NALU.
 * Format: forbidden_zero_bit(1) | nal_ref_idc(2) | nal_unit_type(5)
 * Type is in bits 0-4 of the first byte.
 */
function getH264NaluType(data: Uint8Array): number {
  if (data.length === 0) {
    throw new InvalidNaluError('H.264 NALU data is empty');
  }
  return data[0] & 0x1F;
}

// ─── H.265 NAL Type Extraction ─────────────────────────────────────────────

/**
 * Extract H.265 NAL unit type from the first two bytes of the NALU.
 * Format: forbidden_zero_bit(1) | nal_unit_type(6) | nuh_layer_id(6) | nuh_temporal_id_plus1(3)
 * Type is in bits 1-6 of the first byte: (byte0 >> 1) & 0x3F
 */
function getH265NaluType(data: Uint8Array): number {
  if (data.length < 2) {
    throw new InvalidNaluError(`H.265 NALU requires at least 2 bytes, got ${data.length}`);
  }
  return (data[0] >> 1) & 0x3F;
}

// ─── Public API ────────────────────────────────────────────────────────────

/**
 * Parse an access unit (byte stream) into individual classified NAL units.
 *
 * Splits by Annex B start codes (if present), then classifies each NALU
 * by its type for the given codec.
 *
 * @param data - Raw byte data (with or without Annex B start codes)
 * @param codec - 'h264' or 'h265'
 * @returns Array of classified NAL unit info
 */
export function parseAccessUnit(data: Uint8Array, codec: Codec): NaluInfo[] {
  const rawNalus = splitAnnexB(data);
  const keyframeTypes = codec === 'h264' ? H264_KEYFRAME_TYPES : H265_KEYFRAME_TYPES;
  const spsType = codec === 'h264' ? H264_SPS_TYPE : H265_SPS_TYPE;
  const ppsType = codec === 'h264' ? H264_PPS_TYPE : H265_PPS_TYPE;
  const vpsType = H265_VPS_TYPE;
  const getNaluType = codec === 'h264' ? getH264NaluType : getH265NaluType;

  const result: NaluInfo[] = [];
  for (const naluData of rawNalus) {
    let type: number;
    try {
      type = getNaluType(naluData);
    } catch (e) {
      if (e instanceof InvalidNaluError) {
        console.warn(`Skipping invalid NALU: ${e.message}`);
        continue;
      }
      throw e;
    }
    result.push({
      type,
      data: naluData,
      isKeyframe: keyframeTypes.has(type),
      isSPS: type === spsType,
      isPPS: type === ppsType,
      isVPS: codec === 'h265' && type === vpsType,
    });
  }
  return result;
}

/**
 * Check if a set of NALUs contains at least one keyframe.
 * Useful for determining if an access unit is a keyframe for seeking.
 */
export function isKeyframeNalus(nalus: NaluInfo[]): boolean {
  return nalus.length > 0 && nalus.some((n) => n.isKeyframe);
}
