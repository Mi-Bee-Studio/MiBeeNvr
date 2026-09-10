import { describe, expect, it } from 'vitest';
import { createMultipartPartParser, boundaryFromContentType } from './multipart';

function enc(s: string): Uint8Array {
  return new TextEncoder().encode(s);
}

function concat(chunks: Uint8Array[]): Uint8Array {
  const len = chunks.reduce((n, c) => n + c.length, 0);
  const out = new Uint8Array(len);
  let off = 0;
  for (const c of chunks) {
    out.set(c, off);
    off += c.length;
  }
  return out;
}

/** Feed a multipart body through the parser in arbitrary chunk splits. */
function parseChunked(body: string, boundary: string, splits: number[]): Uint8Array[] {
  const parser = createMultipartPartParser(boundary);
  const bytes = enc(body);
  const parts: Uint8Array[] = [];
  const cuts = [0, ...splits.filter((s) => s > 0 && s < bytes.length), bytes.length];
  for (let i = 0; i < cuts.length - 1; i++) {
    parts.push(...parser.push(bytes.slice(cuts[i], cuts[i + 1])));
  }
  parts.push(...parser.flush());
  return parts;
}

describe('createMultipartPartParser', () => {
  const boundary = 'mibeebatch123';
  const body =
    `--${boundary}\r\n` +
    'Content-Type: image/jpeg\r\n' +
    'X-Frame-Index: 0\r\n' +
    '\r\n' +
    '\xFF\xD8 frame zero \xFF\xD9' +
    `\r\n--${boundary}\r\n` +
    'Content-Type: image/jpeg\r\n' +
    '\r\n' +
    '\xFF\xD8 frame one \xFF\xD9' +
    `\r\n--${boundary}--\r\n`;

  it('parses all parts from a single chunk', () => {
    const parts = parseChunked(body, boundary, []);
    expect(parts).toHaveLength(2);
    expect(new TextDecoder().decode(parts[0])).toBe('\xFF\xD8 frame zero \xFF\xD9');
    expect(new TextDecoder().decode(parts[1])).toBe('\xFF\xD8 frame one \xFF\xD9');
  });

  it('parses parts when chunks split mid-delimiter and mid-payload', () => {
    // Split at tricky offsets: inside the first boundary, inside a payload,
    // and inside the closing delimiter.
    const parts = parseChunked(body, boundary, [5, 12, 40, 70, body.length - 5]);
    expect(parts).toHaveLength(2);
    expect(new TextDecoder().decode(parts[0])).toBe('\xFF\xD8 frame zero \xFF\xD9');
    expect(new TextDecoder().decode(parts[1])).toBe('\xFF\xD8 frame one \xFF\xD9');
  });

  it('returns empty for an empty batch (immediate closing delimiter)', () => {
    const empty = `--${boundary}\r\n--${boundary}--\r\n`;
    const parts = parseChunked(empty, boundary, []);
    expect(parts).toHaveLength(0);
  });

  it('handles payload that itself contains boundary-like bytes after split', () => {
    const tricky = `--${boundary}\r\nContent-Type: image/jpeg\r\n\r\nA--${boundary.slice(0, 4)}B\r\n--${boundary}--\r\n`;
    const parts = parseChunked(tricky, boundary, [20]);
    expect(parts).toHaveLength(1);
    expect(new TextDecoder().decode(parts[0])).toBe(`A--${boundary.slice(0, 4)}B`);
  });

  it('is pure: same output regardless of chunking', () => {
    const oneShot = concat(parseChunked(body, boundary, []));
    const splattered = concat(parseChunked(body, boundary, [1, 3, 7, 11, 23, 37, 51, 67, 83, 97]));
    expect(oneShot).toEqual(splattered);
  });
});

describe('boundaryFromContentType', () => {
  it('extracts boundary from a multipart/mixed content type', () => {
    expect(boundaryFromContentType('multipart/mixed; boundary=abc123')).toBe('abc123');
    expect(boundaryFromContentType('multipart/mixed; boundary="quoted-boundary"')).toBe('quoted-boundary');
  });
  it('returns null for non-multipart types', () => {
    expect(boundaryFromContentType('application/json')).toBeNull();
    expect(boundaryFromContentType('')).toBeNull();
  });
});
