import { describe, it, expect } from 'vitest';
import { createHlsConfig } from '$lib/hls-config';

describe('createHlsConfig', () => {
  const config = createHlsConfig();

  it('should have enableWorker disabled for RPi browser compatibility', () => {
    expect(config.enableWorker).toBe(false);
  });

  it('should have maxBufferLength at most 10 for RPi memory', () => {
    expect(config.maxBufferLength).toBeLessThanOrEqual(10);
  });

  it('should have maxMaxBufferLength at most 20 for RPi memory', () => {
    expect(config.maxMaxBufferLength).toBeLessThanOrEqual(20);
  });

  it('should have maxBufferSize at most 15MB for RPi memory', () => {
    expect(config.maxBufferSize).toBeLessThanOrEqual(15 * 1024 * 1024);
  });

  it('should have backBufferLength at most 3', () => {
    expect(config.backBufferLength).toBeLessThanOrEqual(3);
  });

  it('should have xhrSetup function defined', () => {
    expect(config.xhrSetup).toBeTypeOf('function');
  });

  it('should have fetchSetup function defined for HLS.js 1.6+ fetch-based loader', () => {
    expect(config.fetchSetup).toBeTypeOf('function');
  });
  it('should have liveSyncDurationCount of 3', () => {
    expect(config.liveSyncDurationCount).toBe(3);
  });

  it('should have liveDurationInfinity enabled', () => {
    expect(config.liveDurationInfinity).toBe(true);
  });

  it('should have progressive mode enabled', () => {
    expect(config.progressive).toBe(true);
  });
});
