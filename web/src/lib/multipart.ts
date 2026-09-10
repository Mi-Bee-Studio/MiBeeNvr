/**
 * Streaming multipart/mixed parsing + frame-batch fetching.
 *
 * The backend serves JPEG frame batches as one multipart/mixed response
 * (`GET .../frames?offset=&limit=`), replacing the per-frame GET hot path of
 * the JPEG cycler. The parser below is a pure incremental byte scanner — feed
 * it network chunks in any split, get complete part payloads out.
 */
import { API_BASE, ApiRequestError, getAuthHeader } from '$lib/api/client';

/** Extracts the boundary parameter from a multipart Content-Type header. */
export function boundaryFromContentType(contentType: string | null): string | null {
  if (!contentType) return null;
  if (!/^multipart\//i.test(contentType)) return null;
  const m = /boundary="?([^";]+)"?/i.exec(contentType);
  return m ? m[1] : null;
}

export interface MultipartPartParser {
  /** Feed the next stream chunk; returns any part payloads that completed. */
  push(chunk: Uint8Array): Uint8Array[];
  /** Finalize the stream; returns any remaining complete parts. */
  flush(): Uint8Array[];
}

/**
 * Incremental multipart/mixed part-payload parser.
 *
 * Frame parts look like: `--B\r\n<headers>\r\n\r\n<payload>\r\n--B...`.
 * The parser prepends a virtual CRLF so the delimiter `\r\n--B` matches
 * uniformly at stream start. Payloads are returned as fresh copies; a false
 * delimiter match inside a payload is possible in theory but the backend's
 * hex boundaries make it negligible.
 */
export function createMultipartPartParser(boundary: string): MultipartPartParser {
  const delimiter = new TextEncoder().encode(`\r\n--${boundary}`);
  const headerEnd = new TextEncoder().encode('\r\n\r\n');
  // Start with the virtual leading CRLF so the opening delimiter matches
  // uniformly; trimmed as parts complete (buffer always starts at `\r\n--B`).
  let buf = new Uint8Array(2); // \r\n
  buf[0] = 13;
  buf[1] = 10;

  function indexOf(hay: Uint8Array, needle: Uint8Array, from: number): number {
    outer: for (let i = from; i <= hay.length - needle.length; i++) {
      for (let j = 0; j < needle.length; j++) {
        if (hay[i + j] !== needle[j]) continue outer;
      }
      return i;
    }
    return -1;
  }

  function drain(): Uint8Array[] {
    const parts: Uint8Array[] = [];
    for (;;) {
      // The buffer must begin with the current part's opening delimiter.
      if (indexOf(buf, delimiter, 0) !== 0) return parts;
      // Part headers live between the opening delimiter and the blank line.
      const hIdx = indexOf(buf, headerEnd, delimiter.length);
      if (hIdx < 0) return parts;
      const payloadStart = hIdx + headerEnd.length;
      // The payload ends at the NEXT delimiter (or the closing one).
      const nextD = indexOf(buf, delimiter, payloadStart);
      if (nextD < 0) return parts;
      // Need the 2 bytes after the delimiter to tell next-part from closing.
      const after = nextD + delimiter.length;
      if (after + 2 > buf.length) return parts;
      const isClosing = buf[after] === 45 /* - */ && buf[after + 1] === 45;
      parts.push(buf.slice(payloadStart, nextD));
      if (isClosing) {
        buf = new Uint8Array(0);
        return parts;
      }
      // Keep the delimiter: it is the NEXT part's opening. The buffer again
      // starts at `\r\n--B` for the next iteration.
      buf = buf.slice(nextD);
    }
  }

  return {
    push(chunk: Uint8Array): Uint8Array[] {
      const nb = new Uint8Array(buf.length + chunk.length);
      nb.set(buf);
      nb.set(chunk, buf.length);
      buf = nb;
      return drain();
    },
    flush(): Uint8Array[] {
      // A well-formed stream has no complete parts left after the closing
      // delimiter; drain once more defensively for streams cut mid-batch.
      return drain();
    },
  };
}

/** One batch of JPEG frames + range metadata from the response headers. */
export interface FrameBatch {
  frames: Blob[];
  /** Total frames available (X-Frame-Total). */
  total: number;
  /** Absolute index of frames[0] (X-Frame-Offset). */
  offset: number;
  /** Playback fps hint from the merge row, when present (X-Frame-Fps). */
  fps: number | null;
}

function intHeader(res: Response, name: string, fallback = 0): number {
  const v = res.headers.get(name);
  if (!v) return fallback;
  const n = Number.parseInt(v, 10);
  return Number.isFinite(n) ? n : fallback;
}

/**
 * Fetches a JPEG frame batch from a frame-batch endpoint (path relative to
 * /api) and streams it into decoded Blobs. One request returns up to `limit`
 * frames — the smooth-playback foundation (vs one GET per frame).
 */
export async function fetchFrameBatch(
  endpoint: string,
  offset: number,
  limit: number,
  signal?: AbortSignal,
): Promise<FrameBatch> {
  const url = `${API_BASE}${endpoint}?offset=${offset}&limit=${limit}`;
  const headers: Record<string, string> = {};
  const auth = getAuthHeader();
  if (auth) headers['Authorization'] = auth;

  const res = await fetch(url, {
    headers,
    signal: signal ?? AbortSignal.timeout(30_000),
  });
  if (!res.ok) {
    throw new ApiRequestError(`frame batch request failed: HTTP ${res.status}`, 'HTTP_ERROR');
  }
  const boundary = boundaryFromContentType(res.headers.get('Content-Type'));
  if (!boundary || !res.body) {
    throw new ApiRequestError('frame batch: unexpected content type', 'BAD_CONTENT_TYPE');
  }

  const parser = createMultipartPartParser(boundary);
  const reader = res.body.getReader();
  const frames: Blob[] = [];
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    if (!value) continue;
    for (const part of parser.push(value)) {
      frames.push(new Blob([part], { type: 'image/jpeg' }));
    }
  }
  for (const part of parser.flush()) {
    frames.push(new Blob([part], { type: 'image/jpeg' }));
  }

  return {
    frames,
    total: intHeader(res, 'X-Frame-Total'),
    offset: intHeader(res, 'X-Frame-Offset'),
    fps: res.headers.get('X-Frame-Fps') ? intHeader(res, 'X-Frame-Fps') : null,
  };
}
