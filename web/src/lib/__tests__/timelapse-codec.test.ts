import { describe, it, expect, vi, beforeEach } from 'vitest';
import { probeMergedRecordingCodec, clearMergedCodecCache } from '$lib/api/recordings';

// probeMergedRecordingCodec issues a HEAD request and reads the
// X-Timelapse-Codec header. These tests stub global fetch to verify the
// happy paths (h264/h265/mjpeg) and the null-result paths (404, network fail,
// missing header) without hitting the backend.

function mockHeadResponse(opts: { ok: boolean; status?: number; codecHeader?: string | null }) {
  return {
    ok: opts.ok,
    status: opts.status ?? 200,
    headers: {
      get: (name: string) => (name.toLowerCase() === 'x-timelapse-codec' ? (opts.codecHeader ?? null) : null),
    },
  } as unknown as Response;
}

describe('probeMergedRecordingCodec', () => {
  beforeEach(() => {
    clearMergedCodecCache();
    vi.restoreAllMocks();
  });

  it('returns "h264" when the backend reports the H.264 codec', async () => {
    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(mockHeadResponse({ ok: true, codecHeader: 'h264' }));
    const codec = await probeMergedRecordingCodec('rec-1');
    expect(codec).toBe('h264');
    expect(fetchSpy).toHaveBeenCalledOnce();
    // Confirms the request is a HEAD (not GET) so the body is never downloaded.
    expect(fetchSpy.mock.calls[0][1]).toMatchObject({ method: 'HEAD' });
  });

  it('returns "h265" when the backend reports the H.265 codec', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockHeadResponse({ ok: true, codecHeader: 'h265' }));
    expect(await probeMergedRecordingCodec('rec-2')).toBe('h265');
  });

  it('returns "mjpeg" for mjpa outputs (JPEG frame cycler path)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockHeadResponse({ ok: true, codecHeader: 'mjpeg' }));
    expect(await probeMergedRecordingCodec('rec-3')).toBe('mjpeg');
  });

  it('returns null when the merged file is absent (404)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockHeadResponse({ ok: false, status: 404 }));
    expect(await probeMergedRecordingCodec('rec-4')).toBeNull();
  });

  it('returns null when the backend did not set the header', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockHeadResponse({ ok: true, codecHeader: null }));
    expect(await probeMergedRecordingCodec('rec-5')).toBeNull();
  });

  it('returns null on network failure (fetch throws)', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('network down'));
    expect(await probeMergedRecordingCodec('rec-6')).toBeNull();
  });

  it('caches the result per recordingId across calls', async () => {
    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(mockHeadResponse({ ok: true, codecHeader: 'h264' }));
    await probeMergedRecordingCodec('rec-cache');
    await probeMergedRecordingCodec('rec-cache');
    expect(fetchSpy).toHaveBeenCalledOnce();
  });

  it('clearMergedCodecCache(id) invalidates one entry, forcing a re-probe', async () => {
    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(mockHeadResponse({ ok: true, codecHeader: 'h264' }))
      .mockResolvedValueOnce(mockHeadResponse({ ok: true, codecHeader: 'h265' }));
    expect(await probeMergedRecordingCodec('rec-invalidate')).toBe('h264');
    clearMergedCodecCache('rec-invalidate');
    expect(await probeMergedRecordingCodec('rec-invalidate')).toBe('h265');
    expect(fetchSpy).toHaveBeenCalledTimes(2);
  });
});
