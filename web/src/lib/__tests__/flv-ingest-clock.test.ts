import { describe, it, expect } from 'vitest';
import { FLVIngestParser, FLV_INGEST_UNKNOWN } from '$lib/flv-ingest-clock';

/** Build a minimal FLV byte stream: header + PreviousTagSize0 + tags. */
function flvStream(tags: { type: number; ts: number; delta: number; payloadLen: number }[]): Uint8Array {
  const chunks: number[] = [
    0x46, 0x4c, 0x56, 0x01, 0x05, 0x00, 0x00, 0x00, 0x09, // FLV header
    0x00, 0x00, 0x00, 0x00, // PreviousTagSize0
  ];
  for (const tag of tags) {
    const size = tag.payloadLen;
    chunks.push(
      tag.type,
      (size >> 16) & 0xff, (size >> 8) & 0xff, size & 0xff,
      (tag.ts >> 16) & 0xff, (tag.ts >> 8) & 0xff, tag.ts & 0xff, (tag.ts >> 24) & 0xff,
      (tag.delta >> 16) & 0xff, (tag.delta >> 8) & 0xff, tag.delta & 0xff, // StreamID = ingest delta
    );
    for (let i = 0; i < size; i++) chunks.push(0x00);
    const prev = 11 + size;
    chunks.push((prev >>> 24) & 0xff, (prev >>> 16) & 0xff, (prev >>> 8) & 0xff, prev & 0xff);
  }
  return new Uint8Array(chunks);
}

describe('FLVIngestParser', () => {
  it('extracts the latest video tag ingest delta', () => {
    const p = new FLVIngestParser();
    p.feed(flvStream([
      { type: 0x09, ts: 0, delta: 100, payloadLen: 4 },
      { type: 0x09, ts: 1000, delta: 1100, payloadLen: 4 },
    ]).buffer);
    expect(p.latestDeltaMs).toBe(1100);
  });

  it('ignores audio and script tags', () => {
    const p = new FLVIngestParser();
    p.feed(flvStream([
      { type: 0x08, ts: 0, delta: 999, payloadLen: 2 },
      { type: 0x09, ts: 0, delta: 250, payloadLen: 2 },
    ]).buffer);
    expect(p.latestDeltaMs).toBe(250);
  });

  it('keeps the last known value on unknown sentinel', () => {
    const p = new FLVIngestParser();
    p.feed(flvStream([{ type: 0x09, ts: 0, delta: 300, payloadLen: 2 }]).buffer);
    p.feed(flvStream([{ type: 0x09, ts: 40, delta: FLV_INGEST_UNKNOWN, payloadLen: 2 }]).buffer.slice(13));
    expect(p.latestDeltaMs).toBe(300);
  });

  it('handles chunked delivery across tag boundaries', () => {
    const bytes = flvStream([{ type: 0x09, ts: 0, delta: 4200, payloadLen: 64 }]);
    const p = new FLVIngestParser();
    // Feed in 7-byte slices.
    for (let off = 0; off < bytes.length; off += 7) {
      p.feed(bytes.slice(off, off + 7).buffer);
    }
    expect(p.latestDeltaMs).toBe(4200);
  });
});
