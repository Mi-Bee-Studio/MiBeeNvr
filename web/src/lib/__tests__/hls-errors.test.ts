import { describe, it, expect, vi, beforeEach } from 'vitest';
import { checkStreamAvailable, MAX_RECREATE_ATTEMPTS, ZOMBIE_READYSTATE_DURATION_MS, ZOMBIE_FRAG_GAP_MS, createAutoRetryScheduler, AUTO_RETRY_DELAYS, MAX_AUTO_RETRIES } from '$lib/hls-errors';

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

describe('createAutoRetryScheduler', () => {
  it('should call onRetry after first delay', () => {
    vi.useFakeTimers();
    const onRetry = vi.fn();
    const scheduler = createAutoRetryScheduler(onRetry);
    scheduler.schedule();
    expect(onRetry).not.toHaveBeenCalled();
    vi.advanceTimersByTime(AUTO_RETRY_DELAYS[0]);
    expect(onRetry).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('should increment count on each schedule', () => {
    vi.useFakeTimers();
    const onRetry = vi.fn();
    const scheduler = createAutoRetryScheduler(onRetry);
    expect(scheduler.getCount()).toBe(0);
    scheduler.schedule();
    expect(scheduler.getCount()).toBe(1);
    vi.advanceTimersByTime(AUTO_RETRY_DELAYS[0]);
    scheduler.schedule();
    expect(scheduler.getCount()).toBe(2);
    vi.useRealTimers();
  });

  it('should stop after MAX_AUTO_RETRIES', () => {
    vi.useFakeTimers();
    const onRetry = vi.fn();
    const scheduler = createAutoRetryScheduler(onRetry);
    for (let i = 0; i < MAX_AUTO_RETRIES + 2; i++) {
      scheduler.schedule();
      vi.advanceTimersByTime(AUTO_RETRY_DELAYS[Math.min(i, AUTO_RETRY_DELAYS.length - 1)]);
    }
    expect(onRetry).toHaveBeenCalledTimes(MAX_AUTO_RETRIES);
    vi.useRealTimers();
  });

  it('should reset count on clear', () => {
    const onRetry = vi.fn();
    const scheduler = createAutoRetryScheduler(onRetry);
    scheduler.schedule();
    scheduler.clear();
    expect(scheduler.getCount()).toBe(0);
  });

  it('should use exponential backoff delays', () => {
    expect(AUTO_RETRY_DELAYS).toEqual([5000, 10000, 20000, 40000]);
  });

  it('should have MAX_AUTO_RETRIES of 4', () => {
    expect(MAX_AUTO_RETRIES).toBe(4);
  });
});
