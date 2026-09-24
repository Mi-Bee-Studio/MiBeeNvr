package middleware

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ui"
)

// scriptSrcHashesFor returns 'sha256-...' source entries for every inline
// <script> block in the given SPA document (#879). Hashing the document that
// is actually served keeps the policy correct across vite rebuilds — and, for
// gateway deployments, across injectBasePath()'s serve-time additions. When
// the document is empty (tests, UI-less builds) or has no inline scripts, it
// falls back to 'unsafe-inline' — the header must never break the UI it
// protects.
func scriptSrcHashesFor(indexHTML []byte) string {
	h := sha256.New()
	var b strings.Builder
	for _, m := range inlineScriptRe.FindAllStringSubmatch(string(indexHTML), -1) {
		if strings.Contains(m[1], "src=") {
			continue // external script tag, covered by 'self'
		}
		h.Reset()
		h.Write([]byte(m[2]))
		fmt.Fprintf(&b, " 'sha256-%s'", base64.StdEncoding.EncodeToString(h.Sum(nil)))
	}
	if b.Len() == 0 {
		return "'unsafe-inline'"
	}
	return b.String()
}

// embeddedScriptSrcHashes hashes the raw embedded SPA index.html — the
// document served when no base path is configured. Computed once at first
// use.
var embeddedScriptSrcHashes = sync.OnceValue(func() string {
	data, err := ui.StaticFS.ReadFile("static/index.html")
	if err != nil {
		return "'unsafe-inline'"
	}
	return scriptSrcHashesFor(data)
})

var inlineScriptRe = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)

// defaultFrameAncestors is used when no explicit frame-ancestors policy is
// configured. 'self' permits the app to be framed only by pages of its own
// origin — the same intent as the legacy X-Frame-Options: DENY default
// (nothing else frames it), but expressed via the modern CSP directive so it
// can be relaxed for trusted embedders (e.g. the fnOS desktop).
const defaultFrameAncestors = "'self'"

// SecurityHeaders returns a middleware that adds common security headers to
// every response. HSTS is intentionally omitted: on LAN HTTP deployments it
// bricks access for a year. If TLS is needed, use a reverse proxy
// (Caddy/nginx) and let it set HSTS.
//
// frameAncestors controls who may embed the UI in an <iframe> via the CSP
// frame-ancestors directive. It accepts a space-separated list of sources
// (e.g. "'self'", "http://192.168.1.10 http://192.168.1.11"). An empty value
// falls back to 'self' (no cross-origin framing). This replaces the legacy
// X-Frame-Options header, which cannot express a cross-origin allow-list and
// thus broke embedding in the fnOS desktop (the desktop page is served from a
// different origin than the NVR's :9090, so even SAMEORIGIN rejected it).
// indexHTML is the exact SPA document this deployment serves (nil = the raw
// embedded index.html). CSP script-src hashes are computed from it: under a
// gateway base path the document carries an injected window.__NVR_BASE__
// bootstrap script (#399), and a policy hashed from the raw document would
// silently block it — every in-app URL would then lose its prefix and the
// whole gateway deployment breaks (#399 × #879).
func SecurityHeaders(frameAncestors string, indexHTML []byte) func(http.Handler) http.Handler {
	ancestors := strings.TrimSpace(frameAncestors)
	if ancestors == "" {
		ancestors = defaultFrameAncestors
	}
	hashes := embeddedScriptSrcHashes()
	if indexHTML != nil {
		hashes = scriptSrcHashesFor(indexHTML)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			// X-Frame-Options is intentionally NOT set: CSP frame-ancestors is its
			// modern successor and is strictly more expressive (allow-list vs the
			// single-origin SAMEORIGIN / blanket DENY). Browsers that understand
			// frame-ancestors ignore X-Frame-Options anyway; setting both can only
			// make the policy stricter than intended.
			w.Header().Set("X-XSS-Protection", "1; mode=block")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			// CSP: script-src hashes the served index.html's inline scripts
			// (#879) — no blanket 'unsafe-inline' for scripts. Under a base
			// path the served document includes the injected bootstrap
			// script, which is why SecurityHeaders hashes the
			// caller-provided copy. Svelte 5 inline styles keep style-src
			// 'unsafe-inline' (not exploitable without a
			// script injection first).
			// wasm-unsafe-eval: required for libde265 WASM H.265 decoder (enables H.265
			// live playback on plain HTTP without WebCodecs/HTTPS) and ONNX Runtime Web
			// (browser-side AI inference). connect-src ws:/wss:: WebSocket live streaming.
			// worker-src blob:: WasmPlayer's decoder worker.
			// frame-ancestors: controls cross-origin embedding (e.g. fnOS desktop iframe).
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' "+hashes+" 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src blob: data: 'self'; media-src blob: 'self'; connect-src 'self' ws: wss:; worker-src 'self' blob:; frame-ancestors "+ancestors)
			next.ServeHTTP(w, r)
		})
	}
}
