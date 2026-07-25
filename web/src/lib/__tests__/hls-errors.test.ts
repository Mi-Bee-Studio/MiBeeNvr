import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  checkStreamAvailable,
  MAX_RECREATE_ATTEMPTS,
  ZOMBIE_READYSTATE_DURATION_MS,
  ZOMBIE_FRAG_GAP_MS,
  createAutoRetryScheduler,
  AUTO_RETRY_DELAYS,
  MAX_AUTO_RETRIES,
} from '$lib/hls-errors';

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

  // ─── Issue #112: permanent give-up terminal state ─────────────────────────
  it('fires onGiveUp once when the retry budget is exhausted', () => {
    vi.useFakeTimers();
    const onRetry = vi.fn();
    const onGiveUp = vi.fn();
    const scheduler = createAutoRetryScheduler(onRetry, onGiveUp);
    // Exhaust the budget: schedule + advance for each of the MAX_AUTO_RETRIES
    // retries, then one more schedule() that should trip onGiveUp.
    for (let i = 0; i < MAX_AUTO_RETRIES; i++) {
      scheduler.schedule();
      vi.advanceTimersByTime(AUTO_RETRY_DELAYS[i]);
    }
    expect(onGiveUp).not.toHaveBeenCalled();
    scheduler.schedule(); // this is the (MAX_AUTO_RETRIES+1)th call → give up
    expect(onGiveUp).toHaveBeenCalledTimes(1);
    expect(scheduler.hasGivenUp()).toBe(true);
    vi.useRealTimers();
  });

  it('does NOT reset give-up on clear() (only a fresh scheduler resets it)', () => {
    // This is the key anti-flap invariant: visibilitychange clears/rebuilds,
    // but a cleared scheduler that had given up must not silently restart.
    // The caller must construct a NEW scheduler (which VideoPlayer does on
    // streamUrl change) to get a fresh budget.
    vi.useFakeTimers();
    const onRetry = vi.fn();
    const onGiveUp = vi.fn();
    const scheduler = createAutoRetryScheduler(onRetry, onGiveUp);
    for (let i = 0; i < MAX_AUTO_RETRIES; i++) {
      scheduler.schedule();
      vi.advanceTimersByTime(AUTO_RETRY_DELAYS[i]);
    }
    scheduler.schedule(); // give up
    expect(scheduler.hasGivenUp()).toBe(true);
    scheduler.clear();
    expect(scheduler.hasGivenUp()).toBe(true, 'clear() must not reset give-up');
    scheduler.schedule(); // no-op after give-up
    expect(onRetry).toHaveBeenCalledTimes(MAX_AUTO_RETRIES);
    vi.useRealTimers();
  });
});
