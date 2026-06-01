import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  sendTelemetry,
  optInTelemetry,
  isTelemetryOptedIn,
  __resetOptIn,
} from '$lib/telemetry';

const AUTH_KEY = 'mibee_nvr_auth';
const TEST_CRED = btoa('admin:admin123');

function mockSendBeacon(): ReturnType<typeof vi.fn> {
  const spy = vi.fn().mockReturnValue(true);
  Object.defineProperty(navigator, 'sendBeacon', {
    value: spy,
    writable: true,
    configurable: true,
  });
  return spy;
}

function removeSendBeacon(): void {
  Object.defineProperty(navigator, 'sendBeacon', {
    value: undefined,
    writable: true,
    configurable: true,
  });
}

beforeEach(() => {
  sessionStorage.setItem(AUTH_KEY, TEST_CRED);
  __resetOptIn();
});

afterEach(() => {
  vi.restoreAllMocks();
  sessionStorage.clear();
});

describe('opt-in state management', () => {
  it('should start with opted out', () => {
    expect(isTelemetryOptedIn()).toBe(false);
  });

  it('should opt in after calling optInTelemetry', () => {
    optInTelemetry();
    expect(isTelemetryOptedIn()).toBe(true);
  });

  it('should reset after calling __resetOptIn', () => {
    optInTelemetry();
    __resetOptIn();
    expect(isTelemetryOptedIn()).toBe(false);
  });
});

describe('sendTelemetry in dev mode', () => {
  it('should call navigator.sendBeacon with URL containing token', () => {
    const sendBeacon = mockSendBeacon();

    sendTelemetry('playback_start', 'cam-1');

    expect(sendBeacon).toHaveBeenCalledTimes(1);
    const [url] = sendBeacon.mock.calls[0];
    expect(url).toContain('/api/telemetry');
    expect(url).toContain('token=');
    expect(url).toContain(encodeURIComponent(TEST_CRED));
  });

  it('should call navigator.sendBeacon with a Blob payload', () => {
    const sendBeacon = mockSendBeacon();

    sendTelemetry('playback_start', 'cam-1');

    expect(sendBeacon).toHaveBeenCalledTimes(1);
    const [, blob] = sendBeacon.mock.calls[0];
    expect(blob).toBeInstanceOf(Blob);
  });

  it('should include optional params in payload', () => {
    const sendBeacon = mockSendBeacon();

    sendTelemetry('playback_start', 'cam-1', 1500, { quality: 'hd' });

    const [, blob] = sendBeacon.mock.calls[0];
    return blob.text().then((text) => {
      const payload = JSON.parse(text);
      expect(payload.event).toBe('playback_start');
      expect(payload.camera_id).toBe('cam-1');
      expect(payload.duration_ms).toBe(1500);
      expect(payload.details).toEqual({ quality: 'hd' });
    });
  });

  it('should work without optional durationMs and details', () => {
    const sendBeacon = mockSendBeacon();

    sendTelemetry('playback_error', 'cam-1');

    const [, blob] = sendBeacon.mock.calls[0];
    return blob.text().then((text) => {
      const payload = JSON.parse(text);
      expect(payload.event).toBe('playback_error');
      expect(payload.camera_id).toBe('cam-1');
      expect(payload.duration_ms).toBeUndefined();
      expect(payload.details).toBeUndefined();
    });
  });

  it('should silently skip if sendBeacon is not available', () => {
    removeSendBeacon();

    expect(() => sendTelemetry('playback_start', 'cam-1')).not.toThrow();
  });

  it('should silently skip if auth credentials are missing', () => {
    sessionStorage.clear();
    const sendBeacon = mockSendBeacon();

    sendTelemetry('playback_start', 'cam-1');

    expect(sendBeacon).not.toHaveBeenCalled();
  });

  it('should not throw if sendBeacon throws', () => {
    const sendBeacon = vi.fn().mockImplementation(() => {
      throw new Error('network fail');
    });
    Object.defineProperty(navigator, 'sendBeacon', {
      value: sendBeacon,
      writable: true,
      configurable: true,
    });

    expect(() => sendTelemetry('playback_start', 'cam-1')).not.toThrow();
  });
});
