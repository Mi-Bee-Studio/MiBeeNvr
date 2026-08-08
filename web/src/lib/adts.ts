/**
 * ADTS (Audio Data Transport Stream) frame framing helper.
 *
 * The NVR recorder emits raw AAC access units (no container), but the FAAD2
 * WASM decoder's high-level API expects ADTS-framed input (it scans for the
 * 0xFFF0 syncword and reads the per-frame length from the ADTS header). This
 * module synthesizes the 7-byte ADTS header for each raw AAC frame so the
 * WASM decoder accepts it. The header carries no CRC, one AAC frame per ADTS
 * frame, with the sample-rate index and channel count derived from the
 * AudioSpecificConfig.
 *
 * Reference: ISO/IEC 13818-7 §6.2 (ADTS header layout).
 */

/** Sampling frequency index → rate (ISO 14496-3 Table 1.16). 13 entries. */
export const ADTS_SAMPLE_RATE_INDEX: ReadonlyArray<number> = [
  96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
  16000, 12000, 11025, 8000, 7350,
];

/** Resolve the ADTS sampling_frequency_index for a given Hz rate. */
export function adtsSampleRateIndex(rate: number): number {
  const idx = ADTS_SAMPLE_RATE_INDEX.indexOf(rate);
  return idx >= 0 ? idx : 0x0f; // 0x0f = explicit rate in the AASC (rare)
}

/** Resolved ADTS header parameters derived from the AASC. */
export interface AdtsParams {
  /** audioObjectType (e.g. 2 = AAC-LC). */
  profile: number;
  /** sampling_frequency_index (0..12). */
  sampleRateIndex: number;
  /** channelConfiguration (1=mono, 2=stereo). */
  channelConfig: number;
}

/**
 * Parse the AudioSpecificConfig into the ADTS-relevant fields.
 *
 * AASC layout (AAC-LC): 5 bits audioObjectType | 4 bits samplingFrequencyIndex
 * | 4 bits channelConfiguration | GASpecificConfig...
 * For HE-AAC (aot 5/29) the base layer rate is still what ADTS needs.
 * Returns null if the AASC is too short to parse.
 */
export function parseAascForAdts(aasc: Uint8Array, sampleRate: number): AdtsParams | null {
  if (aasc.length < 2) return null;
  const aot = (aasc[0] >> 3) & 0x1f;
  // For SBR (aot 5/29) the samplingFrequencyIndex embedded in the base layer
  // is the half-rate; the authoritative output rate arrives in the
  // AudioCodecInfo, so use it to look up the ADTS index directly.
  const sampleRateIndex = adtsSampleRateIndex(sampleRate);
  // channelConfiguration is a 4-bit field at AASC bit positions 9-12. byte1
  // covers bits 8-15, so bits 9-12 land at byte1 bit offsets 1-4 → the value
  // is (byte1 >> 3) & 0x0f. (Not >> 4: bit 8 is the samplingFrequencyIndex LSB.)
  const channelConfig = (aasc[1] >> 3) & 0x0f;
  // For HE-AAC (SBR) the "real" audioObjectType is the base type embedded after
  // the SBR extension; fall back to AAC-LC (2) if that can't be read.
  const profile =
    aot === 5 || aot === 29 ? aasc.length > 1 ? (aasc[1] & 0x07) || 2 : 2 : aot;
  return {
    profile,
    sampleRateIndex,
    channelConfig: channelConfig > 0 ? channelConfig : 1,
  };
}

/**
 * Build a 7-byte ADTS header (no CRC) for one raw AAC access unit.
 *
 * `frameLen` is the total ADTS frame length: header (7) + raw payload length.
 * The returned Uint8Array shares no memory with the input.
 */
export function buildAdtsHeader(params: AdtsParams, frameLen: number): Uint8Array {
  // ADTS frame_length is a 13-bit field — clamp to the legal range.
  const len = Math.min(Math.max(frameLen, 7), 0x1fff);
  const profile = params.profile <= 0 ? 2 : params.profile;
  const h = new Uint8Array(7);
  // Byte 0: syncword high 8 bits (all 1).
  h[0] = 0xff;
  // Byte 1: syncword low 4 (1111) | ID 0=MPEG-4 (0) | layer 00 (00) | protection_absent 1=no CRC (1).
  h[1] = 0xf1;
  // Byte 2: profile_ObjectType (2 bits, = profile-1) | sampling_frequency_index (4) | private (0) | channel_config high bit (1).
  h[2] =
    ((profile - 1) << 6) |
    ((params.sampleRateIndex & 0x0f) << 2) |
    ((params.channelConfig >> 2) & 0x01);
  // Byte 3: channel_config low 2 bits (2) | original_copy/home (00) | copyright (0) | copyright_start (0) | frame_length high 2 bits.
  h[3] = ((params.channelConfig & 0x03) << 6) | ((len >> 11) & 0x03);
  // Byte 4: frame_length middle 8 bits.
  h[4] = (len >> 3) & 0xff;
  // Byte 5: frame_length low 3 bits | buffer_fullness high 5 bits (all 1 = VBR).
  h[5] = ((len & 0x07) << 5) | 0x1f;
  // Byte 6: buffer_fullness low 6 bits (all 1) | number_of_raw_data_blocks_in_frame (2, 00 = 1 block).
  h[6] = 0xfc;
  return h;
}
