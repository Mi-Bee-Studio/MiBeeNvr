import { describe, it, expect } from 'vitest';
import { decodeMuLaw, decodeALaw } from './g711-decoder';

describe('decodeMuLaw', () => {
  it('should decode silence (0xFF → 0)', () => {
    const input = new Uint8Array([0xff]);
    const result = decodeMuLaw(input);
    expect(result.length).toBe(1);
    expect(result[0]).toBe(0);
  });

  it('should decode max negative (0x00 → -32124)', () => {
    const input = new Uint8Array([0x00]);
    const result = decodeMuLaw(input);
    expect(result[0]).toBe(-32124);
  });

  it('should decode max positive (0x80 → 32124)', () => {
    const input = new Uint8Array([0x80]);
    const result = decodeMuLaw(input);
    expect(result[0]).toBe(32124);
  });

  it('should decode near-silence negative (0x7F → -1)', () => {
    const input = new Uint8Array([0x7f]);
    const result = decodeMuLaw(input);
    expect(result[0]).toBe(-1);
  });

  it('should decode all zeros as max negative', () => {
    const input = new Uint8Array(16);
    const result = decodeMuLaw(input);
    expect(result.length).toBe(16);
    expect(result[0]).toBe(-32124);
  });

  it('should handle empty input', () => {
    const input = new Uint8Array(0);
    const result = decodeMuLaw(input);
    expect(result.length).toBe(0);
  });

  it('should produce same length output as input', () => {
    const input = new Uint8Array([0x00, 0x80, 0xff, 0x7f, 0x55, 0xaa]);
    const result = decodeMuLaw(input);
    expect(result.length).toBe(6);
  });
});

describe('decodeALaw', () => {
  it('should decode near-silence positive (0xD5 → 8)', () => {
    const input = new Uint8Array([0xd5]);
    const result = decodeALaw(input);
    expect(result.length).toBe(1);
    expect(result[0]).toBe(8);
  });

  it('should decode near-silence negative (0x55 → -8)', () => {
    const input = new Uint8Array([0x55]);
    const result = decodeALaw(input);
    expect(result[0]).toBe(-8);
  });

  it('should decode a mid-range value (0x00 → -5504)', () => {
    const input = new Uint8Array([0x00]);
    const result = decodeALaw(input);
    expect(result[0]).toBe(-5504);
  });

  it('should handle empty input', () => {
    const input = new Uint8Array(0);
    const result = decodeALaw(input);
    expect(result.length).toBe(0);
  });

  it('should produce same length output as input', () => {
    const input = new Uint8Array([0x00, 0x55, 0xff, 0xaa, 0x7f]);
    const result = decodeALaw(input);
    expect(result.length).toBe(5);
  });
});
