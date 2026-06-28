import { describe, it, expect } from 'vitest';
import { decodeMuLaw, decodeALaw } from './g711-decoder';

describe('decodeMuLaw', () => {
  it('should decode a single sample', () => {
    // μ-law 0xff → highest positive value
    const input = new Uint8Array([0xff]);
    const result = decodeMuLaw(input);
    expect(result.length).toBe(1);
    expect(result[0]).toBe(63);
  });

  it('should decode near-silence sample', () => {
    // μ-law 0xFF ≈ near silence (small positive value)
    const input = new Uint8Array([0xff]);
    const result = decodeMuLaw(input);
    expect(result.length).toBe(1);
    expect(result[0]).toBe(63); // small positive, near zero
  });

  it('should decode all zeros', () => {
    const input = new Uint8Array(16);
    const result = decodeMuLaw(input);
    expect(result.length).toBe(16);
    // Index 0 in μ-law table is -8031
    expect(result[0]).toBe(-8031);
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
  it('should decode a single sample', () => {
    // A-law 0xd5 → after XOR 0x55 = 0x80 → index 128
    const input = new Uint8Array([0xd5]);
    const result = decodeALaw(input);
    expect(result.length).toBe(1);
    // Index 128 in A-law table is first of positive half
    expect(result[0]).toBe(5504);
  });

  it('should decode A-law zero (0x55)', () => {
    // A-law 0x55 → after XOR 0x55 = 0x00 → index 0
    const input = new Uint8Array([0x55]);
    const result = decodeALaw(input);
    expect(result.length).toBe(1);
    // Index 0 in A-law table is -5504
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
