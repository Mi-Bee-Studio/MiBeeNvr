import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  listTimelapseMerges,
  getTimelapseMerge,
  getTimelapseMergeDownloadUrl,
  deleteTimelapseMerge,
} from '$lib/api/timelapse-merges';

// Verifies the API client builds correct query strings, URLs, and HTTP methods
// against the WS2 backend endpoints (/api/timelapse/merges...).

function mockJSONResponse(body: unknown, ok = true, status = 200): Response {
  return {
    ok,
    status,
    json: async () => body,
    headers: new Headers(),
  } as unknown as Response;
}

describe('listTimelapseMerges', () => {
  beforeEach(() => vi.restoreAllMocks());

  it('builds a query string from all provided filters', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ merges: [], total: 0 }));
    await listTimelapseMerges({
      camera_id: 'cam-1',
      start: '2026-07-21T00:00:00Z',
      end: '2026-07-22T00:00:00Z',
      duration: '24h',
      status: 'completed',
      limit: 50,
      offset: 10,
    });
    const url = fetchSpy.mock.calls[0][0] as string;
    expect(url).toContain('/timelapse/merges?');
    expect(url).toContain('camera_id=cam-1');
    expect(url).toContain('duration=24h');
    expect(url).toContain('status=completed');
    expect(url).toContain('limit=50');
    expect(url).toContain('offset=10');
  });

  it('omits the query string when no filters are provided', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ merges: [], total: 0 }));
    await listTimelapseMerges();
    expect(fetchSpy.mock.calls[0][0]).toBe('/api/timelapse/merges');
  });

  it('returns the parsed merges + total', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      mockJSONResponse({
        merges: [{ id: 42, camera_id: 'cam-x', status: 'completed' }],
        total: 1,
      }),
    );
    const result = await listTimelapseMerges({ camera_id: 'cam-x' });
    expect(result.total).toBe(1);
    expect(result.merges[0].id).toBe(42);
    expect(result.merges[0].status).toBe('completed');
  });
});

describe('getTimelapseMerge', () => {
  beforeEach(() => vi.restoreAllMocks());

  it('GETs /timelapse/merges/{id} and returns the parsed row', async () => {
    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(mockJSONResponse({ id: 7, camera_id: 'cam-a', codec: 'h265', status: 'completed' }));
    const merge = await getTimelapseMerge(7);
    expect(fetchSpy.mock.calls[0][0]).toBe('/api/timelapse/merges/7');
    expect(merge.id).toBe(7);
    expect(merge.codec).toBe('h265');
  });

  it('accepts string ids (route params are strings)', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ id: 7, camera_id: 'cam-a' }));
    await getTimelapseMerge('7');
    expect(fetchSpy.mock.calls[0][0]).toBe('/api/timelapse/merges/7');
  });
});

describe('getTimelapseMergeDownloadUrl', () => {
  it('builds the playback URL with the numeric id', () => {
    expect(getTimelapseMergeDownloadUrl(42)).toBe('/api/timelapse/merges/42/download');
  });

  it('accepts string ids', () => {
    expect(getTimelapseMergeDownloadUrl('42')).toBe('/api/timelapse/merges/42/download');
  });
});

describe('deleteTimelapseMerge', () => {
  beforeEach(() => vi.restoreAllMocks());

  it('issues a DELETE request to /timelapse/merges/{id}', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ status: 'deleted' }));
    await deleteTimelapseMerge(99);
    expect(fetchSpy.mock.calls[0][0]).toBe('/api/timelapse/merges/99');
    expect(fetchSpy.mock.calls[0][1]).toMatchObject({ method: 'DELETE' });
  });

  it('throws on non-OK response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ error: 'not found' }, false, 404));
    await expect(deleteTimelapseMerge(99)).rejects.toThrow('not found');
  });
});
