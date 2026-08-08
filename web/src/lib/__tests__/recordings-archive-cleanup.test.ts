import { describe, it, expect, vi, beforeEach } from 'vitest';
import { getArchiveCleanupStatus } from '$lib/api/recordings';
import type { ArchiveCleanupStatus, ArchiveCleanupTask } from '$lib/api/recordings';

function mockJSONResponse(body: unknown, ok = true, status = 200): Response {
  return {
    ok,
    status,
    json: async () => body,
    headers: new Headers(),
  } as unknown as Response;
}

describe('getArchiveCleanupStatus', () => {
  beforeEach(() => vi.restoreAllMocks());

  it('GETs /archives/cleanup-status and returns the parsed status', async () => {
    const mockResponse: ArchiveCleanupStatus = {
      active: [
        {
          camera_id: 'cam-1',
          camera_name: 'Front Door',
          recording_count: 150,
          total_size: 1073741824,
          status: 'pending',
          created_at: '2026-08-08T10:30:00Z',
        },
      ],
      recent: [
        {
          camera_id: 'cam-2',
          camera_name: 'Back Yard',
          recording_count: 75,
          total_size: 536870912,
          status: 'done',
          created_at: '2026-08-08T09:00:00Z',
          completed_at: '2026-08-08T09:05:00Z',
        },
      ],
    };

    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(mockJSONResponse(mockResponse));

    const result = await getArchiveCleanupStatus();

    expect(fetchSpy.mock.calls[0][0]).toBe('/api/archives/cleanup-status');
    expect(result.active).toHaveLength(1);
    expect(result.active[0].camera_id).toBe('cam-1');
    expect(result.active[0].status).toBe('pending');
    expect(result.recent).toHaveLength(1);
    expect(result.recent[0].camera_id).toBe('cam-2');
    expect(result.recent[0].status).toBe('done');
    expect(result.recent[0].completed_at).toBe('2026-08-08T09:05:00Z');
  });

  it('returns empty arrays when no tasks exist', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      mockJSONResponse({ active: [], recent: [] })
    );

    const result = await getArchiveCleanupStatus();

    expect(result.active).toEqual([]);
    expect(result.recent).toEqual([]);
  });

  it('includes failed tasks in recent list with error message', async () => {
    const mockResponse: ArchiveCleanupStatus = {
      active: [],
      recent: [
        {
          camera_id: 'cam-failed',
          camera_name: 'Failed Camera',
          recording_count: 10,
          total_size: 1048576,
          status: 'failed',
          created_at: '2026-08-08T08:00:00Z',
          completed_at: '2026-08-08T08:01:00Z',
          error: 'permission denied',
        },
      ],
    };

    vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse(mockResponse));

    const result = await getArchiveCleanupStatus();

    expect(result.recent).toHaveLength(1);
    expect(result.recent[0].status).toBe('failed');
    expect(result.recent[0].error).toBe('permission denied');
  });

  it('includes running tasks in active list', async () => {
    const mockResponse: ArchiveCleanupStatus = {
      active: [
        {
          camera_id: 'cam-running',
          camera_name: 'Running Camera',
          recording_count: 200,
          total_size: 2147483648,
          status: 'running',
          created_at: '2026-08-08T10:00:00Z',
        },
      ],
      recent: [],
    };

    vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse(mockResponse));

    const result = await getArchiveCleanupStatus();

    expect(result.active).toHaveLength(1);
    expect(result.active[0].status).toBe('running');
  });

  it('passes AbortSignal to the request', async () => {
    const abortController = new AbortController();
    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(mockJSONResponse({ active: [], recent: [] }));

    await getArchiveCleanupStatus(abortController.signal);

    expect(fetchSpy).toHaveBeenCalled();
    const options = fetchSpy.mock.calls[0][1] as RequestInit | undefined;
    expect(options?.signal).toBe(abortController.signal);
  });

  it('throws on non-OK response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      mockJSONResponse({ error: 'internal server error' }, false, 500)
    );

    await expect(getArchiveCleanupStatus()).rejects.toThrow('internal server error');
  });

  it('includes both active and recent tasks when both exist', async () => {
    const mockResponse: ArchiveCleanupStatus = {
      active: [
        {
          camera_id: 'cam-active-1',
          camera_name: 'Active 1',
          recording_count: 100,
          total_size: 1073741824,
          status: 'pending',
          created_at: '2026-08-08T11:00:00Z',
        },
        {
          camera_id: 'cam-active-2',
          camera_name: 'Active 2',
          recording_count: 50,
          total_size: 536870912,
          status: 'running',
          created_at: '2026-08-08T10:30:00Z',
        },
      ],
      recent: [
        {
          camera_id: 'cam-recent-1',
          camera_name: 'Recent 1',
          recording_count: 200,
          total_size: 2147483648,
          status: 'done',
          created_at: '2026-08-08T09:00:00Z',
          completed_at: '2026-08-08T09:10:00Z',
        },
      ],
    };

    vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse(mockResponse));

    const result = await getArchiveCleanupStatus();

    expect(result.active).toHaveLength(2);
    expect(result.recent).toHaveLength(1);
  });
});