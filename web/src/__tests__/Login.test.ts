import { render, cleanup, waitFor } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

const { gatewayMock, authedMock } = vi.hoisted(() => ({
  gatewayMock: vi.fn(),
  authedMock: vi.fn(() => false),
}));

vi.mock('$lib/api', () => ({
  login: vi.fn(),
  isAuthenticated: () => authedMock(),
  tryGatewaySession: () => gatewayMock(),
}));

import Login from '../routes/Login.svelte';

// The login page doubles as the expiry-recovery path for backgrounded windows
// (fnOS desktop app hidden past the 2h token TTL): on mount it retries the
// gateway SSO bootstrap and bounces straight back into the app when the NAS
// session is still alive, instead of demanding credentials from an SSO user.

beforeEach(() => {
  window.location.hash = '';
  localStorage.clear();
  sessionStorage.clear();
  // jsdom has no matchMedia; ThemeToggle queries prefers-color-scheme.
  vi.stubGlobal('matchMedia', vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })));
  gatewayMock.mockReset();
  authedMock.mockReset().mockReturnValue(false);
});

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

describe('Login gateway SSO recovery', () => {
  it('re-mints via gateway SSO on mount and returns to the app', async () => {
    gatewayMock.mockResolvedValue(true);
    render(Login);
    await waitFor(() => expect(window.location.hash).toBe('#/surveillance'));
    expect(gatewayMock).toHaveBeenCalledTimes(1);
  });

  it('stays on the form when the gateway refuses (direct-port deployments)', async () => {
    gatewayMock.mockResolvedValue(false);
    render(Login);
    await new Promise((r) => setTimeout(r, 30));
    expect(window.location.hash).not.toBe('#/surveillance');
    expect(document.querySelector('input[type="password"]')).toBeTruthy();
  });

  it('does not attempt SSO when already authenticated', async () => {
    authedMock.mockReturnValue(true);
    render(Login);
    await new Promise((r) => setTimeout(r, 30));
    expect(gatewayMock).not.toHaveBeenCalled();
  });
});
