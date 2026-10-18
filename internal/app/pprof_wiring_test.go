package app

// End-to-end guard for the remote pprof diagnostics mount (#470, acceptance
// for #644): observability.enable_pprof must gate /debug/pprof/* behind the
// main auth middleware — a profile leak is a memory-data leak.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const pprofTestPassword = "pprof-test-pass"

func buildPprofRouter(t *testing.T, enabled bool) http.Handler {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.Observability.EnablePprof = enabled
	hash, err := bcrypt.GenerateFromPassword([]byte(pprofTestPassword), bcrypt.MinCost)
	require.NoError(t, err)
	cfg.Auth.PasswordHash = string(hash)
	cfg.Auth.Password = ""

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NotNil(t, deps.router)
	return deps.router
}

func pprofRequest(t *testing.T, h http.Handler, path, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if password != "" {
		req.SetBasicAuth("admin", password)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestPprof_Disabled_NotMounted(t *testing.T) {
	t.Helper()
	h := buildPprofRouter(t, false)

	// Route absent → 404, never a profile payload.
	rr := pprofRequest(t, h, "/debug/pprof/goroutine?debug=1", pprofTestPassword)
	assert.Equal(t, http.StatusNotFound, rr.Code, "pprof must stay unmounted with enable_pprof=false")
}

func TestPprof_Enabled_RequiresAuth(t *testing.T) {
	t.Helper()
	h := buildPprofRouter(t, true)

	rr := pprofRequest(t, h, "/debug/pprof/goroutine?debug=1", "")
	assert.Equal(t, http.StatusUnauthorized, rr.Code, "pprof must sit behind the auth middleware")
}

func TestPprof_Enabled_ServesProfiles(t *testing.T) {
	t.Helper()
	h := buildPprofRouter(t, true)

	rr := pprofRequest(t, h, "/debug/pprof/goroutine?debug=1", pprofTestPassword)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "goroutine", "goroutine profile text expected")

	rr = pprofRequest(t, h, "/debug/pprof/heap", pprofTestPassword)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotEmpty(t, rr.Body.Bytes(), "heap profile payload expected")

	// 1s CPU profile (default 30s would stall the test).
	rr = pprofRequest(t, h, "/debug/pprof/profile?seconds=1", pprofTestPassword)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotEmpty(t, rr.Body.Bytes(), "CPU profile payload expected")
}
