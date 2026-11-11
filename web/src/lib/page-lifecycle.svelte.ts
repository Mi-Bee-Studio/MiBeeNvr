// Route keep-alive suspension signal (#perf D①).
//
// App.svelte keeps the heavy cross-navigation trio (dashboard / surveillance /
// recordings) mounted when hidden behind another keep-alive page — the
// inactive one is hidden with display:none instead of destroyed, which is
// what removes the teardown + skeleton + refetch stall on every back-and-forth.
//
// Mounted-but-hidden pages MUST stop their background work: polling pauses,
// streams release, timers hold. This module is that signal: App reports which
// keep-alive route is active; pages read their own suspension state
// reactively ($derived(() => isRouteSuspended('dashboard')) etc.).

export const KEEPALIVE_ROUTES = ['dashboard', 'surveillance', 'recordings'];

const activeRoute = $state({ route: '' });

export function setActiveKeepAliveRoute(route: string): void {
  activeRoute.route = route;
}

// Reactive: true when `route` is a keep-alive page that is currently NOT the
// active view (kept mounted but hidden). Non-keep-alive routes are never
// suspended — they are destroyed on leave, as always.
export function isRouteSuspended(route: string): boolean {
  // Boot: App reports the active route in its first effect flush; until then
  // mount-time effects must see the page as active (no suspension).
  if (activeRoute.route === '') return false;
  return KEEPALIVE_ROUTES.includes(route) && activeRoute.route !== route;
}
