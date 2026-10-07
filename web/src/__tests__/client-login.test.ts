import { afterEach, describe, expect, it, vi } from 'vitest';

// login() carries credentials in the JSON body behind a unified gateway
// (fnOS "/app/mibee-nvr") because the gateway claims ANY Authorization header
// as its own session credential and refuses it (200 + "invalid token").
// Direct access keeps the classic Basic header.
//
// APP_BASE is derived from window.__NVR_BASE__ at module load, so each case
// sets the flag and re-imports the client with a fresh module registry.

async function importClient(gateway: boolean) {
  (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__ = gateway ? '/app/mibee-nvr' : '';
  vi.resetModules();
  return await import('../lib/api/client');
}

function stubFetch(): Array<{ url: string; init: RequestInit }> {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  const fn = vi.fn(async (url: string | URL, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    return new Response(JSON.stringify({ status: 'ok', token: 'mbs_test', expires_at: '2099-01-01T00:00:00Z' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  });
  vi.stubGlobal('fetch', fn);
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
  sessionStorage.clear();
  delete (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__;
});

describe('login transport per deployment mode', () => {
  it('gateway mode: credentials in the JSON body, no Authorization header', async () => {
    const calls = stubFetch();
    const { login } = await importClient(true);

    await login('admin', 'secret');

    expect(calls).toHaveLength(1);
    const { url, init } = calls[0];
    expect(url).toBe('/app/mibee-nvr/api/auth/login');
    const headers = init.headers as Record<string, string>;
    expect(headers['Authorization']).toBeUndefined();
    expect(headers['Content-Type']).toBe('application/json');
    expect(JSON.parse(init.body as string)).toEqual({ username: 'admin', password: 'secret' });
  });

  it('direct access: credentials as the Basic Authorization header, no body', async () => {
    const calls = stubFetch();
    const { login } = await importClient(false);

    await login('admin', 'secret');

    expect(calls).toHaveLength(1);
    const { url, init } = calls[0];
    expect(url).toBe('/api/auth/login');
    const headers = init.headers as Record<string, string>;
    expect(headers['Authorization']).toBe(`Basic ${btoa('admin:secret')}`);
    expect(init.body).toBeUndefined();
  });

  it('stores the minted token on success (gateway mode)', async () => {
    stubFetch();
    const { login, getToken } = await importClient(true);

    await login('admin', 'secret');
    expect(getToken()).toBe('mbs_test');
  });
});
