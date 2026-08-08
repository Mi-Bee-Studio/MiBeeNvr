import { describe, it, expect } from 'vitest';
import {
  buildAdtsHeader,
  parseAascForAdts,
  adtsSampleRateIndex,
  ADTS_SAMPLE_RATE_INDEX,
  type AdtsParams,
} from './adts';

/**
 * The ADTS framer turns raw AAC access units (what the NVR recorder emits)
 * into ADTS-framed bytes (what FAAD2's high-level streaming API scans for).
 * These tests pin the exact header bit layout per ISO/IEC 13818-7 §6.2 — a
 * single wrong bit makes FAAD2 reject the frame, so every field is asserted.
 */

describe('adtsSampleRateIndex', () => {
  it('returns the index for known rates', () => {
    expect(adtsSampleRateIndex(44100)).toBe(4);
    expect(adtsSampleRateIndex(48000)).toBe(3);
    expect(adtsSampleRateIndex(8000)).toBe(11);
    expect(adtsSampleRateIndex(24000)).toBe(6);
  });

  it('returns 0x0f for rates not in the table', () => {
    expect(adtsSampleRateIndex(16000)).not.toBe(0x0f); // 16000 IS in the table (index 8)
    expect(adtsSampleRateIndex(22050)).toBe(7); // sanity: 22050 is index 7
    expect(adtsSampleRateIndex(12345)).toBe(0x0f);
  });

  it('table has exactly 13 entries', () => {
    expect(ADTS_SAMPLE_RATE_INDEX.length).toBe(13);
  });
});

describe('parseAascForAdts', () => {
  it('parses an AAC-LC 44100Hz stereo AASC', () => {
    // 00010 (LC) | 0100 (44100) | 0010 (stereo) = 0x12 0x10
    const aasc = new Uint8Array([0x12, 0x10]);
    const params = parseAascForAdts(aasc, 44100);
    expect(params).not.toBeNull();
    expect(params!.profile).toBe(2);
    expect(params!.sampleRateIndex).toBe(4);
    expect(params!.channelConfig).toBe(2);
  });

  it('parses an AAC-LC 48000Hz mono AASC', () => {
    // AAC-LC (aot 2) | freqIndex 3 | channelConfig 1
    // byte0 = (2<<3)|(3>>1) = 0x11 ; byte1 = ((3&1)<<7)|(1<<3) = 0x88
    const aasc = new Uint8Array([0x11, 0x88]);
    const params = parseAascForAdts(aasc, 48000);
    expect(params!.profile).toBe(2);
    expect(params!.sampleRateIndex).toBe(3);
    expect(params!.channelConfig).toBe(1);
  });

  it('returns null for a too-short AASC', () => {
    expect(parseAascForAdts(new Uint8Array([0x12]), 44100)).toBeNull();
  });

  it('defaults channelConfig to 1 when zero', () => {
    // AAC-LC, 44100, channelConfig 0 → should default to 1 (not 0)
    // byte0 = 0x12 ; byte1 = 0x00 (channelConfig nibble = 0000)
    const aasc = new Uint8Array([0x12, 0x00]);
    const params = parseAascForAdts(aasc, 44100);
    expect(params!.channelConfig).toBe(1);
  });
});

describe('buildAdtsHeader', () => {
  // Reference params: AAC-LC, 44100Hz (index 4), stereo (channelConfig 2).
  const params: AdtsParams = { profile: 2, sampleRateIndex: 4, channelConfig: 2 };

  it('produces exactly 7 bytes', () => {
    const h = buildAdtsHeader(params, 7 + 100);
    expect(h.length).toBe(7);
  });

  it('starts with the ADTS syncword 0xFFF0/0xFFF1 (protection_absent=1)', () => {
    const h = buildAdtsHeader(params, 7 + 100);
    expect(h[0]).toBe(0xff);
    expect(h[1] & 0xf0).toBe(0xf0); // syncword low nibble
    expect(h[1] & 0x01).toBe(0x01); // protection_absent (no CRC)
  });

  it('encodes profile-1, sampleRateIndex, and channelConfig high bit in byte 2', () => {
    const h = buildAdtsHeader(params, 7 + 100);
    // profile_ObjectType = profile-1 = 1 → top 2 bits = 01
    expect((h[2] >> 6) & 0x03).toBe(1);
    // sampling_frequency_index 4 → next 4 bits = 0100
    expect((h[2] >> 2) & 0x0f).toBe(4);
    // channel_config is a 3-bit field split across byte2 LSB (1 bit) and
    // byte3 top 2 bits. channelConfig 2 = binary 010 → MSB (bit 2) = 0.
    expect(h[2] & 0x01).toBe(0);
  });

  it('encodes channelConfig high bit in byte 2 for channelConfig 7', () => {
    // channelConfig 7 = binary 111 → MSB (bit 2) = 1 → byte2 LSB = 1.
    const p7: AdtsParams = { profile: 2, sampleRateIndex: 4, channelConfig: 7 };
    const h = buildAdtsHeader(p7, 7 + 100);
    expect(h[2] & 0x01).toBe(1);
    // low 2 bits of channelConfig (11) land in byte3 top 2 bits.
    expect((h[3] >> 6) & 0x03).toBe(3);
  });

  it('encodes channelConfig low bits in byte 3', () => {
    const h = buildAdtsHeader(params, 7 + 100);
    // channelConfig 2 = binary 10 → low 2 bits = 10
    expect((h[3] >> 6) & 0x03).toBe(2);
  });

  it('encodes frame_length across bytes 3-5 (13 bits big-endian)', () => {
    const frameLen = 7 + 255; // 262 — exercises all 13 bits
    const h = buildAdtsHeader(params, frameLen);
    // Reassemble: bits [3:2] of byte3 (high 2) | byte4 (mid 8) | [5:3-5] (low 3)
    const high = (h[3] & 0x03) << 11;
    const mid = h[4] << 3;
    const low = (h[5] >> 5) & 0x07;
    expect(high | mid | low).toBe(frameLen);
  });

  it('clamps frame length to the 13-bit ADTS maximum (0x1fff)', () => {
    const h = buildAdtsHeader(params, 0x4000); // exceeds 13 bits
    const high = (h[3] & 0x03) << 11;
    const mid = h[4] << 3;
    const low = (h[5] >> 5) & 0x07;
    expect(high | mid | low).toBe(0x1fff);
  });

  it('sets buffer_fullness to 0x7FF (VBR) and number_of_raw_data_blocks to 0', () => {
    const h = buildAdtsHeader(params, 7 + 100);
    // buffer_fullness is 11 bits split across byte5 low 5 (11111) + byte6 high 6 (111111)
    expect(h[5] & 0x1f).toBe(0x1f);
    expect((h[6] >> 2) & 0x3f).toBe(0x3f);
    // number_of_raw_data_blocks_in_frame = 0 (1 block)
    expect(h[6] & 0x03).toBe(0);
  });
});
