import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readJson, ApiRequestError } from '$lib/api';

// fnOS unified-gateway plain-text auth rejection (field report 2026-09-20):
// the gateway answers API paths with HTTP 200 + body "invalid token"
// (Content-Type text/plain, verified with curl on the test box) both for a
// dead desktop session AND as a per-request bounce under burst load while
// the session is alive (observed live: sibling requests succeed in the same
// millisecond). readJson must (1) never surface a raw SyntaxError, (2) retry
// the transient bounce once via the caller's refetch closure without any
// UI signal, and (3) only after a second consecutive rejection surface a
// typed GATEWAY_AUTH error + the window event for App.

function gatewayReject(): Response {
  return {
    ok: true,
    status: 200,
    headers: new Headers({ 'Content-Type': 'text/plain' }),
    text: async () => 'invalid token',
    json: async () => {
      throw new SyntaxError("Unexpected token 'i', \"invalid token\" is not valid JSON");
    },
  } as unknown as Response;
}

function jsonOk(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    headers: new Headers({ 'Content-Type': 'application/json' }),
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response;
}

function garbageOk(): Response {
  return {
    ok: true,
    status: 200,
    headers: new Headers({ 'Content-Type': 'application/json' }),
    text: async () => '\x00\x01 not json at all',
  } as unknown as Response;
}

beforeEach(() => {
  vi.unstubAllGlobals();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('readJson gateway plain-text tolerance', () => {
  it('parses a normal JSON body unchanged', async () => {
    await expect(readJson<{ a: 1 }>(jsonOk({ a: 1 }))).resolves.toEqual({ a: 1 });
  });

  it('without refetch: gateway rejection throws GATEWAY_AUTH + dispatches the event once', async () => {
    const events: CustomEvent[] = [];
    const listener = (e: Event) => events.push(e as CustomEvent);
    window.addEventListener('nvr-gateway-auth', listener);
    try {
      await readJson(gatewayReject());
      expect.unreachable('readJson must throw on the plain-text gateway body');
    } catch (e) {
      expect(e).toBeInstanceOf(ApiRequestError);
      const err = e as ApiRequestError;
      expect(err.code).toBe('GATEWAY_AUTH');
      expect(err.message).toContain('invalid token');
    }
    expect(events).toHaveLength(1);
    expect(events[0].detail.body).toBe('invalid token');
    window.removeEventListener('nvr-gateway-auth', listener);
  });

  it('transient bounce: first rejection + successful refetch resolves silently, no event', async () => {
    const events: Event[] = [];
    const listener = (e: Event) => events.push(e);
    window.addEventListener('nvr-gateway-auth', listener);
    let calls = 0;
    const refetch = async (): Promise<Response> => {
      calls++;
      return jsonOk({ recovered: true });
    };
    // Drive the internal retry delay with fake timers.
    const promise = readJson<{ recovered: boolean }>(gatewayReject(), refetch);
    await vi.advanceTimersByTimeAsync(1500);
    await expect(promise).resolves.toEqual({ recovered: true });
    expect(calls).toBe(1);
    expect(events).toHaveLength(0);
    window.removeEventListener('nvr-gateway-auth', listener);
  });

  it('slow bounce window: second retry succeeds → still silent, no event', async () => {
    const events: Event[] = [];
    const listener = (e: Event) => events.push(e);
    window.addEventListener('nvr-gateway-auth', listener);
    let calls = 0;
    const refetch = async (): Promise<Response> => {
      calls++;
      return calls < 2 ? gatewayReject() : jsonOk({ late: true });
    };
    const promise = readJson<{ late: boolean }>(gatewayReject(), refetch);
    await vi.advanceTimersByTimeAsync(5000);
    await expect(promise).resolves.toEqual({ late: true });
    expect(calls).toBe(2);
    expect(events).toHaveLength(0);
    window.removeEventListener('nvr-gateway-auth', listener);
  });

  it('persistent rejection: every retry bounces → one event + GATEWAY_AUTH after both retries', async () => {
    const events: CustomEvent[] = [];
    const listener = (e: Event) => events.push(e as CustomEvent);
    window.addEventListener('nvr-gateway-auth', listener);
    let calls = 0;
    const refetch = async (): Promise<Response> => {
      calls++;
      return gatewayReject();
    };
    const promise = readJson(gatewayReject(), refetch);
    const assertion = expect(promise).rejects.toMatchObject({ code: 'GATEWAY_AUTH' });
    await vi.advanceTimersByTimeAsync(5000);
    await assertion;
    expect(calls).toBe(2);
    expect(events).toHaveLength(1);
    expect(events[0].detail.body).toBe('invalid token');
    window.removeEventListener('nvr-gateway-auth', listener);
  });

  it('non-JSON garbage without the gateway signature throws BAD_JSON, no retry, no event', async () => {
    const events: Event[] = [];
    const listener = (e: Event) => events.push(e);
    window.addEventListener('nvr-gateway-auth', listener);
    let calls = 0;
    const refetch = async (): Promise<Response> => {
      calls++;
      return jsonOk({});
    };
    try {
      await readJson(garbageOk(), refetch);
      expect.unreachable('readJson must throw on non-JSON');
    } catch (e) {
      expect((e as ApiRequestError).code).toBe('BAD_JSON');
    }
    expect(calls).toBe(0);
    expect(events).toHaveLength(0);
    window.removeEventListener('nvr-gateway-auth', listener);
  });
});
