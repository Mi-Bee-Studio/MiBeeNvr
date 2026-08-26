/**
 * FLV ingest-clock reader (#481).
 *
 * The backend piggybacks each video tag's hub-ingest wallclock offset (ms
 * from the stream's clock base, exposed as flv_clock_ms on /api/streams and
 * X-Stream-Wallclock-Ms on the FLV response) in the tag header StreamID
 * field — spec receivers ignore the field, so mpegts.js plays the stream
 * unchanged. This module walks the raw FLV byte stream to extract the
 * latest (timestamp, ingestDelta) so the player can compute end-to-end
 * latency: now − (clockBase + delta).
 *
 * 0xFFFFFF is the "unknown" sentinel (source had no ingest stamp or the
 * offset exceeded the 3-byte range).
 */

export const FLV_INGEST_UNKNOWN = 0xffffff;

/** Incremental FLV tag walker. Feed arbitrary response chunks in order. */
export class FLVIngestParser {
  private buf = new Uint8Array(0);
  /** Latest video-tag ingest delta (ms from clock base), null before the
   *  first tag / after an unknown sentinel. */
  latestDeltaMs: number | null = null;

  feed(chunk: ArrayBuffer): void {
    if (chunk.byteLength === 0) return;
    const merged = new Uint8Array(this.buf.length + chunk.byteLength);
    merged.set(this.buf);
    merged.set(new Uint8Array(chunk), this.buf.length);
    this.buf = merged;
    this.consume();
  }

  private consume(): void {
    let off = 0;
    const b = this.buf;
    // Stream can start either with the 9-byte FLV header + PreviousTagSize0
    // or mid-stream after a reconnect; find tag starts by scanning for
    // valid tag headers (0x08/0x09 preceded by enough bytes).
    // Simplest robust approach: expect header first iff first bytes are "FLV".
    if (b.length >= 9 && b[0] === 0x46 && b[1] === 0x4c && b[2] === 0x56) {
      off = 9 + 4; // header + PreviousTagSize0
    }
    for (;;) {
      if (off + 11 > b.length) break;
      const tagType = b[off];
      if (tagType !== 0x08 && tagType !== 0x09 && tagType !== 0x12) {
        // Lost sync (mid-stream join) — resync heuristically: this parser is
        // best-effort latency metadata only; bail and wait for more data.
        break;
      }
      const dataSize = (b[off + 1] << 16) | (b[off + 2] << 8) | b[off + 3];
      const total = 11 + dataSize + 4;
      if (off + total > b.length) break; // wait for the rest of the tag
      if (tagType === 0x09) {
        const delta = (b[off + 8] << 16) | (b[off + 9] << 8) | b[off + 10];
        if (delta !== FLV_INGEST_UNKNOWN) this.latestDeltaMs = delta;
      }
      off += total;
    }
    if (off > 0) this.buf = b.slice(off);
    // Cap the carry-over buffer so a desynced stream can't grow unbounded.
    if (this.buf.length > 1 << 20) this.buf = this.buf.slice(-11);
  }
}

/**
 * Builds an mpegts.js customLoader that tees every response chunk to
 * `onChunk` while delegating all loading to mpegts's own fetch-stream
 * loader (headers, abort, reconnect semantics untouched).
 */
/**
 * Builds an mpegts.js customLoader (BaseLoader subclass) that streams the
 * FLV response via fetch and tees every chunk to `onChunk` for latency
 * parsing. mpegts 1.8 exports BaseLoader/LoaderStatus/LoaderErrors but NOT
 * its own FetchStreamLoader, so this is a self-contained fetch
 * implementation for the live-FLV case (no range seeks — live streams are
 * consumed sequentially). Auth headers must be passed in (the loader
 * config's `headers` only reach mpegts's built-in loaders).
 */
export function createTeeLoader(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  mpegtsNS: any,
  onChunk: (data: ArrayBuffer) => void,
  headers?: Record<string, string>,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
): any {
  const Base = mpegtsNS.BaseLoader;
  if (!Base || typeof fetch === 'undefined') return undefined;

  function TeeLoader(this: any, _seekHandler: unknown, _config: unknown) {
    if (!(this instanceof TeeLoader)) return new (TeeLoader as any)(_seekHandler, _config);
    Base.call(this, 'x-flv-tee-loader');
    this._needStash = true;
    this._controller = null as AbortController | null;
    this._abortRequested = false;
    this._receivedLength = 0;
  }
  TeeLoader.prototype = Object.create(Base.prototype);
  TeeLoader.prototype.constructor = TeeLoader;
  TeeLoader.isSupported = () => typeof ReadableStream !== 'undefined' && typeof fetch !== 'undefined';

  TeeLoader.prototype.open = function (this: any, dataSource: { url: string }, range: { from: number; to: number }) {
    this._status = mpegtsNS.LoaderStatus.kConnecting;
    const controller = new AbortController();
    this._controller = controller;
    const reqHeaders: Record<string, string> = { ...headers };
    if (range && range.from !== 0) {
      const to = range.to >= 0 ? range.to : '';
      reqHeaders.Range = `bytes=${range.from}-${to}`;
    }

    fetch(dataSource.url, { headers: reqHeaders, signal: controller.signal, credentials: 'same-origin' })
      .then(async (resp: Response) => {
        if (!resp.ok) {
          this._status = mpegtsNS.LoaderStatus.kError;
          this.onError(mpegtsNS.LoaderErrors.HTTP_STATUS_CODE_INVALID, {
            code: resp.status,
            msg: `HTTP ${resp.status}`,
          });
          return;
        }
        const len = resp.headers.get('Content-Length');
        if (len) this.onContentLengthKnown(parseInt(len, 10));
        if (!resp.body) {
          // No streaming body — read whole buffer once (tiny/edge servers).
          const buf = await resp.arrayBuffer();
          this._status = mpegtsNS.LoaderStatus.kBuffering;
          this._emit(buf);
          this._status = mpegtsNS.LoaderStatus.kComplete;
          this.onComplete(0, this._receivedLength - 1);
          return;
        }
        const reader = resp.body.getReader();
        this._status = mpegtsNS.LoaderStatus.kBuffering;
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          // Tee BEFORE handing to mpegts: latency metadata must never affect
          // playback, and errors are swallowed in _emit's onChunk wrapper.
          this._emit(value.buffer.slice(value.byteOffset, value.byteOffset + value.byteLength));
        }
        this._status = mpegtsNS.LoaderStatus.kComplete;
        this.onComplete(0, this._receivedLength - 1);
      })
      .catch((err: unknown) => {
        if (this._abortRequested || this._status === mpegtsNS.LoaderStatus.kComplete) return;
        this._status = mpegtsNS.LoaderStatus.kError;
        const msg = err instanceof Error ? err.message : String(err);
        this.onError(mpegtsNS.LoaderErrors.EXCEPTION, { code: -1, msg });
      });
  };

  TeeLoader.prototype._emit = function (this: any, chunk: ArrayBuffer) {
    const byteStart = this._receivedLength;
    this._receivedLength += chunk.byteLength;
    try {
      onChunk(chunk);
    } catch {
      /* latency metadata must never break playback */
    }
    this.onDataArrival(chunk, byteStart, this._receivedLength);
  };

  TeeLoader.prototype.abort = function (this: any) {
    this._abortRequested = true;
    this._controller?.abort();
    this._controller = null;
  };
  TeeLoader.prototype.destroy = function (this: any) {
    this.abort();
    Base.prototype.destroy?.call(this);
  };

  return TeeLoader;
}
