import { describe, it, expect, vi, beforeEach } from 'vitest';
import { enableCamera, disableCamera } from '$lib/api/cameras.ts';
import { ApiRequestError } from '$lib/api/client.ts';

// Mock sessionStorage (not available by default in Vitest Node environment)
const mockStorage: Record<string, string> = {};
vi.stubGlobal('sessionStorage', {
  getItem: vi.fn((key: string) => mockStorage[key] ?? null),
  setItem: vi.fn((key: string, value: string) => { mockStorage[key] = value; }),
  removeItem: vi.fn((key: string) => { delete mockStorage[key]; }),
});

// -----------------------------------------------
// Unit tests for enableCamera / disableCamera
// -----------------------------------------------

const mockCamera = {
  id: 'front-door',
  name: 'Front Door',
  protocol: 'rtsp',
  url: 'rtsp://192.168.1.100:8554/stream',
  enabled: true,
  status: 'online',
};

describe('enableCamera', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    mockStorage['mibee_nvr_auth'] = btoa('admin:testpass');
  });

  it('calls PUT /api/cameras/{id} with enabled:true and returns the camera', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ ...mockCamera, enabled: true }),
    });

    const result = await enableCamera('front-door');

    expect(global.fetch).toHaveBeenCalledTimes(1);
    expect(global.fetch).toHaveBeenCalledWith(
      '/api/cameras/front-door',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ enabled: true }),
      }),
    );
    expect(result).toEqual({ ...mockCamera, enabled: true });
  });
});

describe('disableCamera', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    mockStorage['mibee_nvr_auth'] = btoa('admin:testpass');
  });

  it('calls PUT /api/cameras/{id} with enabled:false and returns the camera', async () => {
    const disabledCamera = { ...mockCamera, enabled: false };
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(disabledCamera),
    });

    const result = await disableCamera('front-door');

    expect(global.fetch).toHaveBeenCalledTimes(1);
    expect(global.fetch).toHaveBeenCalledWith(
      '/api/cameras/front-door',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ enabled: false }),
      }),
    );
    expect(result).toEqual(disabledCamera);
  });
});

describe('camera API error scenarios', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    mockStorage['mibee_nvr_auth'] = btoa('admin:testpass');
  });

  it('throws ApiRequestError on 401 Unauthorized', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 401,
      json: () => Promise.resolve({ error: 'Invalid credentials', code: 'UNAUTHORIZED' }),
    });

    await expect(enableCamera('front-door')).rejects.toThrow(ApiRequestError);
    await expect(enableCamera('front-door')).rejects.toThrow('Invalid credentials');
  });

  it('throws ApiRequestError on 404 Not Found', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 404,
      json: () => Promise.resolve({ error: 'Camera not found', code: 'NOT_FOUND' }),
    });

    await expect(disableCamera('nonexistent')).rejects.toThrow(ApiRequestError);
    await expect(disableCamera('nonexistent')).rejects.toThrow('Camera not found');
  });

  it('re-throws when fetch itself fails (network error)', async () => {
    global.fetch = vi.fn().mockRejectedValue(new Error('Failed to fetch'));

    await expect(enableCamera('front-door')).rejects.toThrow('Failed to fetch');
  });

  it('handles non-JSON error responses gracefully', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      json: () => Promise.reject(new Error('Invalid JSON')),
    });

    await expect(disableCamera('front-door')).rejects.toThrow(ApiRequestError);
    await expect(disableCamera('front-door')).rejects.toThrow('Unknown error');
  });
});
