import { describe, it, expect } from 'vitest';
import { buildTranscodingPayload } from '$lib/camera-form-payload';

describe('buildTranscodingPayload', () => {
  const params = { targetCodec: 'h264', preset: 'faster', bitrate: '4M', crf: 20 };

  it('enabled → full per-camera config', () => {
    expect(buildTranscodingPayload({ enabled: true, wasEnabled: false, ...params })).toEqual({
      enabled: true,
      target_codec: 'h264',
      preset: 'faster',
      bitrate: '4M',
      crf: 20,
    });
  });

  it('never enabled → undefined (nil = leave unset, camera keeps following the global switch)', () => {
    expect(buildTranscodingPayload({ enabled: false, wasEnabled: false, ...params })).toBeUndefined();
  });

  it('unchecked after being enabled → explicit {enabled:false} (turns it off)', () => {
    expect(buildTranscodingPayload({ enabled: false, wasEnabled: true, ...params })).toEqual({
      enabled: false,
    });
  });

  it('re-saving an explicit off stays stable (wasEnabled=false after reload)', () => {
    expect(buildTranscodingPayload({ enabled: false, wasEnabled: false, ...params })).toBeUndefined();
  });
});
