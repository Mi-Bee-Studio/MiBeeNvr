package config

import "testing"

// TestApplyDefaults_ListenPortEnvOverride verifies NVR_LISTEN_PORT drives
// server.listen (issue #269). Env wins over both the default (:9090) and an
// explicit config-file value; an empty env leaves the config/default in place.
func TestApplyDefaults_ListenPortEnvOverride(t *testing.T) {
	t.Helper()

	// Default applies when nothing is set.
	t.Run("default_when_env_unset", func(t *testing.T) {
		t.Setenv("NVR_LISTEN_PORT", "")
		var cfg Config
		cfg.ApplyDefaults()
		if cfg.Server.Listen != ":9090" {
			t.Fatalf("expected default :9090, got %q", cfg.Server.Listen)
		}
	})

	// "9091" form → ":9091".
	t.Run("bare_port_form", func(t *testing.T) {
		t.Setenv("NVR_LISTEN_PORT", "9091")
		var cfg Config
		cfg.ApplyDefaults()
		if cfg.Server.Listen != ":9091" {
			t.Fatalf("expected :9091, got %q", cfg.Server.Listen)
		}
	})

	// ":9092" form passes through unchanged.
	t.Run("colon_port_form", func(t *testing.T) {
		t.Setenv("NVR_LISTEN_PORT", ":9092")
		var cfg Config
		cfg.ApplyDefaults()
		if cfg.Server.Listen != ":9092" {
			t.Fatalf("expected :9092, got %q", cfg.Server.Listen)
		}
	})

	// Env wins over an explicit config-file value (12-factor precedence).
	t.Run("env_overrides_config_value", func(t *testing.T) {
		t.Setenv("NVR_LISTEN_PORT", "9093")
		cfg := Config{Server: ServerConfig{Listen: ":8080"}}
		cfg.ApplyDefaults()
		if cfg.Server.Listen != ":9093" {
			t.Fatalf("env should override config; expected :9093, got %q", cfg.Server.Listen)
		}
	})

	// Whitespace around the value is tolerated.
	t.Run("trims_whitespace", func(t *testing.T) {
		t.Setenv("NVR_LISTEN_PORT", "  9094  ")
		var cfg Config
		cfg.ApplyDefaults()
		if cfg.Server.Listen != ":9094" {
			t.Fatalf("expected :9094 after trim, got %q", cfg.Server.Listen)
		}
	})
}
