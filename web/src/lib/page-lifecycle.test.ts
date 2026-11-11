import { describe, expect, it } from 'vitest';
import { KEEPALIVE_ROUTES, isRouteSuspended, setActiveKeepAliveRoute } from './page-lifecycle.svelte';

// Module-level $state persists across tests in one file — every test sets the
// active route explicitly first, so ordering does not matter.
describe('page-lifecycle suspension signal', () => {
  it('boot default: nothing reported yet → nothing suspended (mount safety)', () => {
    setActiveKeepAliveRoute('');
    expect(isRouteSuspended('dashboard')).toBe(false);
    expect(isRouteSuspended('surveillance')).toBe(false);
    expect(isRouteSuspended('cameras')).toBe(false);
  });

  it('active keep-alive route is not suspended; its siblings are', () => {
    setActiveKeepAliveRoute('surveillance');
    expect(isRouteSuspended('surveillance')).toBe(false);
    expect(isRouteSuspended('dashboard')).toBe(true);
    expect(isRouteSuspended('recordings')).toBe(true);
  });

  it('a non-keep-alive active route suspends the whole trio', () => {
    setActiveKeepAliveRoute('cameras');
    for (const r of KEEPALIVE_ROUTES) {
      expect(isRouteSuspended(r)).toBe(true);
    }
    expect(isRouteSuspended('cameras')).toBe(false);
    expect(isRouteSuspended('live')).toBe(false);
    expect(isRouteSuspended('login')).toBe(false);
  });

  it('trio membership is exactly the allowlist', () => {
    expect(KEEPALIVE_ROUTES).toEqual(['dashboard', 'surveillance', 'recordings']);
  });
});
