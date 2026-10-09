import { afterEach, describe, expect, it, vi } from 'vitest';

// getAuthHeader() must suppress the Authorization header ONLY when the page
// load is actually fronted by the unified gateway — learned from the
// /api/health `gateway` flag (#938) — not merely when a base path is
// configured. A base-path deployment (fnOS package) serves DIRECT access
// from the same listener with the same injected document; there the Bearer
// header is required. Gating on APP_BASE instead made every direct login
// bounce: login succeeded, the first authenticated call went out headerless,
// 401'd, and the route guard dumped the user back on the login page.
//
// Module state (gatewayFronted) is per-module-load, so each case sets the
// window flag, re-imports the client with a fresh module registry, and
// replays the boot health probe via checkLocalBypass().

interface FetchLog {
  url: string;
  headers: Record<string, string>;
}

function stubFetch(gatewayFlag: boolean | 'fail'): FetchLog[] {
  const calls: FetchLog[] = [];
  const respond = (url: string): Response => {
    if (url.includes('/api/health')) {
      if (gatewayFlag === 'fail') {
        return new Response('gateway down', { status: 500 });
      }
      return new Response(JSON.stringify({ status: 'ok', checks: {}, uptime: '1m', gateway: gatewayFlag }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  };
  const fn = vi.fn(async (url: string | URL, init?: RequestInit) => {
    const headers: Record<string, string> = {};
    const h = init?.headers;
    if (h instanceof Headers) h.forEach((v, k) => (headers[k] = v));
    else if (Array.isArray(h)) for (const [k, v] of h) headers[k] = String(v);
    else if (h) Object.assign(headers, h);
    calls.push({ url: String(url), headers });
    return respond(String(url));
  });
  vi.stubGlobal('fetch', fn);
  return calls;
}

function storeSession() {
  localStorage.setItem(
    'mibee_nvr_token',
    JSON.stringify({ token: 'mbs_test_token', expiresAt: Date.now() + 60 * 60 * 1000 }),
  );
}

async function importClient(basePath: string) {
  (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__ = basePath;
  vi.resetModules();
  return await import('../lib/api/client');
}

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
  sessionStorage.clear();
  delete (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__;
});

describe('Authorization header per access path (#938)', () => {
  it('direct access to a base-path deployment: Bearer header IS sent', async () => {
    const calls = stubFetch(false);
    storeSession();
    const client = await importClient('/app/mibee-nvr');
    await client.checkLocalBypass();
    expect(client.isGatewayFronted()).toBe(false);

    await client.apiRequest('/settings');
    const api = calls.find((c) => c.url.includes('/api/settings'));
    expect(api).toBeDefined();
    expect(api!.headers['Authorization']).toBe('Bearer mbs_test_token');
  });

  it('gateway-fronted access: no Authorization header', async () => {
    const calls = stubFetch(true);
    storeSession();
    const client = await importClient('/app/mibee-nvr');
    await client.checkLocalBypass();
    expect(client.isGatewayFronted()).toBe(true);

    await client.apiRequest('/settings');
    const api = calls.find((c) => c.url.includes('/api/settings'));
    expect(api).toBeDefined();
    expect(api!.headers['Authorization']).toBeUndefined();
  });

  it('health probe failure fails open to the direct-access behavior (header sent)', async () => {
    const calls = stubFetch('fail');
    storeSession();
    const client = await importClient('/app/mibee-nvr');
    await client.checkLocalBypass();
    expect(client.isGatewayFronted()).toBe(false);

    await client.apiRequest('/settings');
    const api = calls.find((c) => c.url.includes('/api/settings'));
    expect(api).toBeDefined();
    expect(api!.headers['Authorization']).toBe('Bearer mbs_test_token');
  });

  it('root deployment without a stored token: no header', async () => {
    const calls = stubFetch(false);
    const client = await importClient('');
    await client.checkLocalBypass();

    await client.apiRequest('/settings');
    const api = calls.find((c) => c.url.includes('/api/settings'));
    expect(api).toBeDefined();
    expect(api!.headers['Authorization']).toBeUndefined();
  });
});
