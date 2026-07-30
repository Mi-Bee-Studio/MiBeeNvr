import { describe, it, expect, afterEach, vi } from 'vitest';
import { copyText } from './clipboard';

// copyText exists because navigator.clipboard is undefined on plain HTTP origins
// (the NVR's default Docker/LAN deployment), which silently broke the "copy
// push-out URL" and "copy API key" buttons (issue #197). These tests pin both the
// secure-context path and the execCommand fallback.

describe('copyText', () => {
  const originalClipboard = navigator.clipboard;
  const originalExecCommand = document.execCommand;

  afterEach(() => {
    vi.restoreAllMocks();
    // Restore navigator.clipboard and document.execCommand for other test files.
    Object.defineProperty(navigator, 'clipboard', {
      value: originalClipboard,
      configurable: true,
    });
    document.execCommand = originalExecCommand;
    Object.defineProperty(window, 'isSecureContext', {
      value: false,
      configurable: true,
    });
  });

  it('uses the async Clipboard API on a secure context', async () => {
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    document.execCommand = vi.fn(() => true);

    const ok = await copyText('rtmp://example/live');
    expect(ok).toBe(true);
    expect(writeText).toHaveBeenCalledWith('rtmp://example/live');
    // Must NOT fall through to the legacy path when the async API succeeds.
    expect(document.execCommand).not.toHaveBeenCalled();
  });

  it('falls back to execCommand on a non-secure context (plain HTTP + IP)', async () => {
    // This is the issue #197 scenario: Docker/LAN deploy via http://<ip>:9090.
    Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true });
    // navigator.clipboard may be entirely absent on non-secure contexts.
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    const exec = vi.fn(() => true);
    document.execCommand = exec;

    const ok = await copyText('http://192.168.1.10:9090/key');
    expect(ok).toBe(true);
    expect(exec).toHaveBeenCalledWith('copy');
  });

  it('returns false when both clipboard API and execCommand fail', async () => {
    Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true });
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    document.execCommand = vi.fn(() => false);

    const ok = await copyText('nope');
    expect(ok).toBe(false);
  });

  it('falls back to execCommand when the Clipboard API rejects (e.g. permission denied)', async () => {
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
    const writeText = vi.fn().mockRejectedValue(new Error('permission denied'));
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const exec = vi.fn(() => true);
    document.execCommand = exec;

    const ok = await copyText('retry-via-fallback');
    expect(ok).toBe(true);
    expect(writeText).toHaveBeenCalled();
    expect(exec).toHaveBeenCalledWith('copy');
  });
});
