import { describe, it, expect } from 'vitest';
import { aascToCodecString, aascSampleRate } from './aac-webcodecs-decoder';

/**
 * Unit tests for the AAC WebCodecs decoder's pure helpers:
 *   - aascToCodecString: AASC → mp4a.40.x codec string (audioObjectType bits)
 *   - aascSampleRate: AASC → sampling frequency index lookup
 *
 * The full AudioDecoder lifecycle is browser/WebCodecs-only and is not
 * exercised here (no jsdom polyfill for AudioDecoder); the bit-parsing logic
 * is the part that can regress silently, so it gets dedicated coverage.
 */

describe('aascToCodecString', () => {
  it('maps AAC-LC (audioObjectType 2) to mp4a.40.2', () => {
    // AAC-LC, 44100Hz, stereo: 00010 | 0100 | 0010 | ... = 0x12 0x10
    const aasc = new Uint8Array([0x12, 0x10]);
    expect(aascToCodecString(aasc)).toBe('mp4a.40.2');
  });

  it('maps HE-AAC v1 (audioObjectType 5) to mp4a.40.5', () => {
    // audioObjectType 5 = 00101 → first byte high bits 00101xxx
    const aasc = new Uint8Array([0x2b, 0x80]);
    expect(aascToCodecString(aasc)).toBe('mp4a.40.5');
  });

  it('maps HE-AAC v2 (audioObjectType 29) to mp4a.40.29', () => {
    // audioObjectType 29 = 11101 → first byte high bits 11101xxx
    const aasc = new Uint8Array([0xeb, 0x80]);
    expect(aascToCodecString(aasc)).toBe('mp4a.40.29');
  });

  it('falls back to AAC-LC for an empty AASC', () => {
    expect(aascToCodecString(new Uint8Array(0))).toBe('mp4a.40.2');
  });

  it('falls back to AAC-LC for unknown audioObjectType', () => {
    // audioObjectType 1 (AAC Main) → not in switch, falls through to default
    const aasc = new Uint8Array([0x09, 0x80]);
    expect(aascToCodecString(aasc)).toBe('mp4a.40.2');
  });
});

describe('aascSampleRate', () => {
  it('resolves 44100 Hz from sampling_frequency_index 4', () => {
    // AAC-LC (00010) | freqIndex 4 (0100) | channelConfig 2 (0010) = 0x12 0x10
    expect(aascSampleRate(new Uint8Array([0x12, 0x10]))).toBe(44100);
  });

  it('resolves 48000 Hz from sampling_frequency_index 3', () => {
    // AAC-LC (aot 2) | freqIndex 3 | channelConfig 1
    // byte0 = (2<<3)|(3>>1) = 0x11 ; byte1 = ((3&1)<<7)|(1<<3) = 0x88
    expect(aascSampleRate(new Uint8Array([0x11, 0x88]))).toBe(48000);
  });

  it('resolves 8000 Hz from sampling_frequency_index 11', () => {
    // AAC-LC (aot 2) | freqIndex 11 | channelConfig 1
    // byte0 = (2<<3)|(11>>1) = 0x15 ; byte1 = ((11&1)<<7)|(1<<3) = 0x88
    expect(aascSampleRate(new Uint8Array([0x15, 0x88]))).toBe(8000);
  });

  it('returns null for HE-AAC (SBR overrides base rate)', () => {
    // audioObjectType 5 → returns null regardless of other bits
    const aasc = new Uint8Array([0x2b, 0x80]);
    expect(aascSampleRate(aasc)).toBeNull();
  });

  it('returns null for too-short AASC', () => {
    expect(aascSampleRate(new Uint8Array([0x12]))).toBeNull();
  });

  it('returns null for explicit 24-bit rate (freqIndex 0x0f)', () => {
    // freqIndex 15 = 1111 → first byte low 3 bits + high bit of byte1
    // AAC-LC(00010) | 1111 | ... → 0x1f 0x80
    expect(aascSampleRate(new Uint8Array([0x1f, 0x80]))).toBeNull();
  });
});
