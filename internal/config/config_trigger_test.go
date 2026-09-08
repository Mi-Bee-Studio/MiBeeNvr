package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Webhook trigger config (#709): enabled-without-secret fails validation, the
// replay window defaults to 5 minutes, and the YAML section round-trips.
func TestWebhookTriggerConfig(t *testing.T) {
	t.Parallel()

	t.Run("enabled without secret rejected", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Trigger: TriggerConfig{Webhook: WebhookTriggerConfig{Enabled: true}}}
		err := Validate(cfg)
		require.ErrorContains(t, err, "trigger.webhook.secret")
	})

	t.Run("disabled without secret is fine", func(t *testing.T) {
		t.Parallel()
		// A zero Config trips unrelated validation (ftp port, …) — the point
		// here is only that the webhook section adds NO complaint of its own.
		err := Validate(&Config{})
		if err != nil {
			require.NotContains(t, err.Error(), "trigger.webhook")
		}
	})

	t.Run("replay window defaults", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, 300, (WebhookTriggerConfig{}).ReplayWindow())
		require.Equal(t, 300, (WebhookTriggerConfig{ReplayWindowS: -1}).ReplayWindow())
		require.Equal(t, 60, (WebhookTriggerConfig{ReplayWindowS: 60}).ReplayWindow())
	})

	t.Run("yaml round trip", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "cfg.yaml")
		yaml := `
trigger:
  webhook:
    enabled: true
    secret: "whsec_abc123"
    replay_window_s: 120
`
		require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))
		cfg, err := Load(path)
		require.NoError(t, err)
		require.True(t, cfg.Trigger.Webhook.Enabled)
		require.Equal(t, "whsec_abc123", cfg.Trigger.Webhook.Secret)
		require.Equal(t, 120, cfg.Trigger.Webhook.ReplayWindowS)
		require.NoError(t, Validate(cfg))
	})
}
