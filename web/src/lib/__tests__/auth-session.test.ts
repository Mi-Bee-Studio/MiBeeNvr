import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { storeToken, getToken, logout, clearToken, tryGatewaySession } from '$lib/api';

// Backgrounded-window expiry (fnOS desktop app, >2h hidden — no requests, no
// sliding renewal) must be recoverable by re-running the gateway SSO bootstrap
// — but an explicit logout must NOT be bounced straight back in.

function fetchOk(body: Record<string, unknown>) {
  return { ok: true, json: async () => body } as unknown as Response;
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  vi.unstubAllGlobals();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('explicit-logout guard', () => {
  it('logout() arms the logged-out flag', () => {
    storeToken('mbs_t1');
    logout();
    expect(sessionStorage.getItem('mibee_nvr_logged_out')).toBe('1');
    expect(getToken()).toBeNull();
  });

  it('tryGatewaySession skips the network entirely after an explicit logout', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    logout();
    await expect(tryGatewaySession()).resolves.toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(getToken()).toBeNull();
  });

  it('storeToken clears the flag (any successful mint = logged back in)', () => {
    logout();
    expect(sessionStorage.getItem('mibee_nvr_logged_out')).toBe('1');
    storeToken('mbs_t2');
    expect(sessionStorage.getItem('mibee_nvr_logged_out')).toBeNull();
    expect(getToken()).toBe('mbs_t2');
  });

  it('clearToken alone does NOT clear the flag (only a real mint does)', () => {
    logout();
    clearToken();
    expect(sessionStorage.getItem('mibee_nvr_logged_out')).toBe('1');
  });
});

describe('gateway SSO re-mint (expiry recovery)', () => {
  it('mints and stores a fresh token when the gateway answers', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      fetchOk({ status: 'ok', token: 'mbs_fresh', expires_at: new Date(Date.now() + 3600_000).toISOString() }),
    );
    vi.stubGlobal('fetch', fetchMock);
    await expect(tryGatewaySession()).resolves.toBe(true);
    expect(getToken()).toBe('mbs_fresh');
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('does not hit the network when a valid token is already stored', async () => {
    storeToken('mbs_live', new Date(Date.now() + 3600_000).toISOString());
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    await expect(tryGatewaySession()).resolves.toBe(true);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('returns false without storing anything when the gateway refuses', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, status: 401 } as unknown as Response);
    vi.stubGlobal('fetch', fetchMock);
    await expect(tryGatewaySession()).resolves.toBe(false);
    expect(getToken()).toBeNull();
  });
});
