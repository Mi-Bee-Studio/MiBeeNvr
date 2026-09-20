import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readJson, ApiRequestError } from '$lib/api';

// fnOS unified-gateway plain-text auth rejection (field report 2026-09-20):
// with an expired desktop session the gateway answers API paths with
// HTTP 200 + body "invalid token" (Content-Type text/plain, verified with
// curl on the test box). Every OK-path response.json() then threw a raw
// SyntaxError ("Unexpected token 'i'") — readJson must instead surface a
// typed GATEWAY_AUTH error and tell App via the window event.

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
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('readJson gateway plain-text tolerance', () => {
  it('parses a normal JSON body unchanged', async () => {
    await expect(readJson<{ a: 1 }>(jsonOk({ a: 1 }))).resolves.toEqual({ a: 1 });
  });

  it('turns the gateway 200+"invalid token" into a typed GATEWAY_AUTH error + event', async () => {
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

  it('non-JSON garbage without the gateway signature throws BAD_JSON, no event', async () => {
    const events: Event[] = [];
    const listener = (e: Event) => events.push(e);
    window.addEventListener('nvr-gateway-auth', listener);
    try {
      await readJson(garbageOk());
      expect.unreachable('readJson must throw on non-JSON');
    } catch (e) {
      expect((e as ApiRequestError).code).toBe('BAD_JSON');
    }
    expect(events).toHaveLength(0);
    window.removeEventListener('nvr-gateway-auth', listener);
  });
});
