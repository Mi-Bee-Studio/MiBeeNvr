import { describe, it, expect } from 'vitest';
import { parseSubnetHints, profileOptionLabel, fmtSkew } from '../lib/camera-form-helpers';

describe('parseSubnetHints', () => {
  it('splits on newlines and commas, trimming each entry', () => {
    expect(parseSubnetHints('192.168.1.0/24\n10.0.0.0/24')).toEqual(['192.168.1.0/24', '10.0.0.0/24']);
    expect(parseSubnetHints('192.168.1.0/24, 10.0.0.0/24')).toEqual(['192.168.1.0/24', '10.0.0.0/24']);
  });

  it('drops blank lines and surrounding whitespace', () => {
    expect(parseSubnetHints('  192.168.1.0/24 \n\n  \n10.0.0.0/24  ')).toEqual(['192.168.1.0/24', '10.0.0.0/24']);
  });

  it('returns undefined when nothing usable remains', () => {
    expect(parseSubnetHints('')).toBeUndefined();
    expect(parseSubnetHints('   \n , \n ')).toBeUndefined();
  });
});

describe('profileOptionLabel', () => {
  it('joins name, resolution and codec', () => {
    expect(profileOptionLabel({ name: 'Main', token: 't1', width: 1920, height: 1080, encoding: 'H264' }))
      .toBe('Main · 1920×1080 · H264');
  });

  it('falls back to the token when the profile has no name', () => {
    expect(profileOptionLabel({ token: 'SecondStreamProfileToken', encoding: 'H265' }))
      .toBe('SecondStreamProfileToken · H265');
  });

  it('omits missing parts without leaving separators', () => {
    expect(profileOptionLabel({ name: 'Main', token: 't' })).toBe('Main');
  });
});

describe('fmtSkew', () => {
  it('uses seconds below a minute', () => {
    expect(fmtSkew(5.25)).toBe('5.3 s');
    expect(fmtSkew(-5.25)).toBe('-5.3 s');
  });

  it('uses minutes below an hour', () => {
    expect(fmtSkew(90)).toBe('1.5 min');
  });

  it('uses hours from an hour up', () => {
    expect(fmtSkew(5400)).toBe('1.5 h');
  });
});
