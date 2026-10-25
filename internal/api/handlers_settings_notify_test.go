package api

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

func TestSettings_NotificationsRoundTrip(t *testing.T) {
	t.Parallel()
	h := setupSettingsHandler(t)
	cfg := h.config

	cfg.Notifications = configNotificationsForTest()

	// GET: section present, secrets masked.
	rr := doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, `"token_configured":true`)
	require.Contains(t, body, `"api_key_configured":true`)
	require.NotContains(t, body, "sink-secret", "the GET must never return sink tokens")
	require.NotContains(t, body, "mbv_snapshot_key", "the GET must never return the API key")

	// PUT: whole-sink-table replace; blank token keeps the stored one for a
	// same-named sink, blank api_key keeps the existing key.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"notifications":{
			"enabled":true,
			"base_url":"http://10.0.0.5:9090",
			"api_key":"",
			"sinks":[
				{"name":"hub","url":"http://10.0.0.6:9080/api/ingest/webhook","token":"","events":["camera.connection"],"severity_min":"warning","timeout_s":5,"burst_per_min":30},
				{"name":"sms","url":"http://10.0.0.7:9080/api/ingest/webhook","token":"fresh-token","events":[],"severity_min":"critical","timeout_s":10,"burst_per_min":5}
			]
		}}`)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"restart_required":true`)

	require.True(t, cfg.Notifications.Enabled)
	require.Equal(t, "http://10.0.0.5:9090", cfg.Notifications.BaseURL)
	require.Equal(t, "mbv_snapshot_key", cfg.Notifications.APIKey, "blank api_key must keep the stored one")
	require.Len(t, cfg.Notifications.Sinks, 2)
	require.Equal(t, "sink-secret", cfg.Notifications.Sinks[0].Token, "blank token must keep the stored one")
	require.Equal(t, "http://10.0.0.6:9080/api/ingest/webhook", cfg.Notifications.Sinks[0].URL)
	require.Equal(t, []string{"camera.connection"}, cfg.Notifications.Sinks[0].Events)
	require.Equal(t, "warning", cfg.Notifications.Sinks[0].SeverityMin)
	require.Equal(t, "fresh-token", cfg.Notifications.Sinks[1].Token)
	require.Equal(t, "critical", cfg.Notifications.Sinks[1].SeverityMin)

	// The post-save GET view reflects the merge and masks the new secret.
	rr = doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "fresh-token")
	require.Contains(t, rr.Body.String(), `"name":"sms"`)
}

func TestSettings_NotificationsValidationRejectsBadConfig(t *testing.T) {
	t.Parallel()
	h := setupSettingsHandler(t)
	cfg := h.config
	cfg.Notifications = configNotificationsForTest()
	prevSinks := cfg.Notifications.Sinks

	// Unknown event type in a sink filter → 400, config unchanged.
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings",
		bytes.NewReader([]byte(`{"notifications":{"enabled":true,"sinks":[
			{"name":"hub","url":"http://x:9080/ingest","token":"t","events":["camera.typo"]}
		]}}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "unknown event type")
	require.Equal(t, prevSinks, cfg.Notifications.Sinks, "rejected PUT must not mutate config")
}

func configNotificationsForTest() config.NotificationsConfig {
	return config.NotificationsConfig{
		Enabled: true,
		BaseURL: "http://192.168.1.10:9090",
		APIKey:  "mbv_snapshot_key",
		Sinks: []config.NotificationSinkConfig{
			{Name: "hub", URL: "http://192.168.1.20:9080/api/ingest/webhook", Token: "sink-secret"},
		},
	}
}
