package api

import (
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// notifyTestConfig builds a config with the given expose_origin switch and
// sinks (URL + token pairs are fixed per case by the caller).
func notifyTestConfig(expose bool, sinks ...config.NotificationSinkConfig) *config.Config {
	return &config.Config{
		Notifications: config.NotificationsConfig{
			Enabled:      true,
			ExposeOrigin: expose,
			Sinks:        sinks,
		},
	}
}

// TestHealth_NotifyReceiversExposed pins the #1121 contract: with
// notifications.expose_origin explicitly on, the PUBLIC health payload
// carries the deduplicated sink origins — scheme://host:port only. The
// token and the URL path (which the token is appended to on delivery)
// must never appear in the unauthenticated response.
func TestHealth_NotifyReceiversExposed(t *testing.T) {
	t.Parallel()
	h := setupHealthHandler(t, nil)
	h.config = notifyTestConfig(true,
		config.NotificationSinkConfig{URL: "http://192.168.1.30:9080/api/ingest/webhook", Token: "secret-token-a"},
		config.NotificationSinkConfig{URL: "https://alert.lan.example:9443/hook", Token: "secret-token-b"},
	)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	body := rr.Body.String()
	require.Contains(t, body, `"notify_receivers":["http://192.168.1.30:9080","https://alert.lan.example:9443"]`,
		"origins must be scheme://host:port, in sink order")
	require.NotContains(t, body, "secret-token-a")
	require.NotContains(t, body, "secret-token-b")
	require.NotContains(t, body, "/api/ingest/webhook", "the sink path must not leak into the public payload")
	require.NotContains(t, body, "/hook")
}

// TestHealth_NotifyReceiversOmittedByDefault: the field is opt-in — with
// the switch off (default) the public payload must be byte-shape identical
// to today's (no notify_receivers key at all).
func TestHealth_NotifyReceiversOmittedByDefault(t *testing.T) {
	t.Parallel()
	h := setupHealthHandler(t, nil)
	h.config = notifyTestConfig(false,
		config.NotificationSinkConfig{URL: "http://192.168.1.30:9080/api/ingest/webhook", Token: "secret-token-a"},
	)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "notify_receivers")
}

// TestHealth_NotifyReceiversNoSinks: switch on but no sinks configured —
// the field stays omitted (nothing to expose).
func TestHealth_NotifyReceiversNoSinks(t *testing.T) {
	t.Parallel()
	h := setupHealthHandler(t, nil)
	h.config = notifyTestConfig(true)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "notify_receivers")
}

// TestHealth_NotifyReceiversSkipsMalformedURLs: sink URLs that fail to
// parse into a usable origin (no scheme / empty host) are skipped without
// affecting the rest; when no sink yields a valid origin the field is
// omitted entirely.
func TestHealth_NotifyReceiversSkipsMalformedURLs(t *testing.T) {
	t.Parallel()
	h := setupHealthHandler(t, nil)
	h.config = notifyTestConfig(true,
		config.NotificationSinkConfig{URL: "192.168.1.30:9080/no-scheme", Token: "t1"}, // no scheme
		config.NotificationSinkConfig{URL: "http:///path-only", Token: "t2"},           // empty host
		config.NotificationSinkConfig{URL: "http://good.lan:8080/ingest", Token: "t3"}, // valid
	)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"notify_receivers":["http://good.lan:8080"]`)

	// All-malformed variant: field omitted, not an empty array.
	h2 := setupHealthHandler(t, nil)
	h2.config = notifyTestConfig(true,
		config.NotificationSinkConfig{URL: "not a url at all", Token: "t1"},
	)
	rr2 := doRequest(t, h2.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr2.Code)
	require.NotContains(t, rr2.Body.String(), "notify_receivers")
}

// TestHealth_NotifyReceiversDedupOrigins: two sinks on the same origin
// collapse to one entry (origin identity, not sink count, is the signal).
func TestHealth_NotifyReceiversDedupOrigins(t *testing.T) {
	t.Parallel()
	h := setupHealthHandler(t, nil)
	h.config = notifyTestConfig(true,
		config.NotificationSinkConfig{URL: "http://192.168.1.30:9080/api/ingest/one", Token: "t1"},
		config.NotificationSinkConfig{URL: "http://192.168.1.30:9080/api/ingest/two", Token: "t2"},
	)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"notify_receivers":["http://192.168.1.30:9080"]`)
}
