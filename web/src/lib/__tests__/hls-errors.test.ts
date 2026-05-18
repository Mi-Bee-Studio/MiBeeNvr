import { describe, it, expect, vi, beforeEach } from 'vitest';
import { checkStreamAvailable, MAX_RECREATE_ATTEMPTS, ZOMBIE_READYSTATE_DURATION_MS, ZOMBIE_FRAG_GAP_MS } from '$lib/hls-errors';

describe('checkStreamAvailable', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it('should return true without making network requests', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch');
    const result = await checkStreamAvailable('/api/cameras/test/stream/index.m3u8');
    expect(result).toBe(true);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('should return true for empty URL', async () => {
    const result = await checkStreamAvailable('');
    expect(result).toBe(true);
  });

  it('should return true for any URL', async () => {
    const result = await checkStreamAvailable('http://any-url.example/stream.m3u8');
    expect(result).toBe(true);
  });
});

describe('Error recovery thresholds', () => {
  it('should have MAX_RECREATE_ATTEMPTS of at least 5', () => {
    expect(MAX_RECREATE_ATTEMPTS).toBeGreaterThanOrEqual(5);
  });

  it('should have ZOMBIE_READYSTATE_DURATION_MS of at least 20s for RPi slow networks', () => {
    expect(ZOMBIE_READYSTATE_DURATION_MS).toBeGreaterThanOrEqual(20_000);
  });

  it('should have ZOMBIE_FRAG_GAP_MS of at least 60s for RPi slow networks', () => {
    expect(ZOMBIE_FRAG_GAP_MS).toBeGreaterThanOrEqual(60_000);
  });
});
