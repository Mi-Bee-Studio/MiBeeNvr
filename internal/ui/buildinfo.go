package ui

import "sync"

// spaBuildInfo is the build fingerprint emitted into the SPA bundle by the
// vite build (web/vite.config.js spaBuildInfoPlugin writes dist/build-info.json,
// which lands in the embedded static tree). It answers "which frontend is
// actually deployed" in one glance — the 2026-09-25 outage was a stale SPA
// embedded into a fresh binary, and the version API offered no way to tell.
//
// "missing" means the embedded tree has no index.html at all (a binary
// compiled without the SPA build output — see VerifyEmbeddedSPA). It must be
// reported ahead of any stale build-info.json: a leftover fingerprint file
// with no SPA behind it once made an empty-UI deployment look like a valid
// older build. "unknown" means the SPA predates the fingerprinting (or a dev
// checkout without a frontend build) — itself a useful signal.
var spaBuildInfo = sync.OnceValue(func() string {
	if _, err := StaticFS.ReadFile("static/index.html"); err != nil {
		return "missing"
	}
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
// ("git@timestamp"), "missing" when the SPA document itself is absent, or
// "unknown" when the bundle carries no fingerprint.
func SPABuildInfo() string {
	return spaBuildInfo()
}
