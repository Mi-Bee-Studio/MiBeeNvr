package app

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

// A gateway/proxy may serve the SPA document at "<base>" without the trailing
// slash; document-level refs must be prefix-absolute so assets resolve
// correctly regardless (#394).
func TestInjectBasePath(t *testing.T) {
	raw := []byte(`<!doctype html><html><head><title>MiBee NVR</title>` +
		`<link rel="icon" href="./favicon.svg">` +
		`<link rel="manifest" href="./manifest.json">` +
		`<link rel="stylesheet" href="./assets/index-abc.css">` +
		`</head><body><div id="app"></div>` +
		`<script type="module" src="./assets/index-abc.js"></script></body></html>`)

	out := string(injectBasePath(raw, "/app/mibee-nvr"))

	require.Contains(t, out, `window.__NVR_BASE__="/app/mibee-nvr"`)
	require.Contains(t, out, `href="/app/mibee-nvr/favicon.svg"`)
	require.Contains(t, out, `href="/app/mibee-nvr/manifest.json"`)
	require.Contains(t, out, `href="/app/mibee-nvr/assets/index-abc.css"`)
	require.Contains(t, out, `src="/app/mibee-nvr/assets/index-abc.js"`)
	require.NotContains(t, out, `"./`)
}

// The fallback anchor (<head>) must still work when </title> is absent, and
// refs without the "./" prefix must survive untouched.
func TestInjectBasePathFallbackAnchor(t *testing.T) {
	raw := []byte(`<html><head><meta charset="utf-8"></head><body></body></html>`)
	out := string(injectBasePath(raw, "/prefix"))
	require.Contains(t, out, `<head><script>window.__NVR_BASE__="/prefix";</script>`)
}

// TestCSPCoversInjectedBasePathScript pins the wiring between injectBasePath
// and the CSP middleware: the security headers served for gateway deployments
// must be hashed from the injected document, or the injected
// window.__NVR_BASE__ bootstrap script is silently blocked by the browser and
// every in-app URL loses its prefix (#399 × #879).
func TestCSPCoversInjectedBasePathScript(t *testing.T) {
	raw := []byte(`<!doctype html><html><head><title>MiBee NVR</title>` +
		`<script type="module" src="./assets/index-abc.js"></script>` +
		`</head><body></body></html>`)
	injected := injectBasePath(raw, "/app/mibee-nvr")

	sum := sha256.Sum256([]byte(`window.__NVR_BASE__="/app/mibee-nvr";`))
	wantHash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"

	handler := authmw.SecurityHeaders("", injected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Contains(t, w.Header().Get("Content-Security-Policy"), wantHash)
}
