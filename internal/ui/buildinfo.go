package ui

import "sync"

// spaBuildInfo is the build fingerprint emitted into the SPA bundle by the
// vite build (web/vite.config.js spaBuildInfoPlugin writes dist/build-info.json,
// which lands in the embedded static tree). It answers "which frontend is
// actually deployed" in one glance — the 2026-09-25 outage was a stale SPA
// embedded into a fresh binary, and the version API offered no way to tell.
//
// Empty/"unknown" means the SPA predates the fingerprint (or a dev checkout
// without a frontend build) — itself a useful signal.
var spaBuildInfo = sync.OnceValue(func() string {
	b, err := StaticFS.ReadFile("static/build-info.json")
	if err != nil {
		return "unknown"
	}
	info, err := parseBuildInfo(b)
	if err != nil || info == "" {
		return "unknown"
	}
	return info
})

// SPABuildInfo returns the embedded SPA's build fingerprint
// ("git@timestamp"), or "unknown" when the bundle carries none.
func SPABuildInfo() string {
	return spaBuildInfo()
}
