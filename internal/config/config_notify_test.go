package config

import (
	"strings"
	"testing"
)

func notifyConfig() *Config {
	// Validate() assumes defaults were applied (the Load path does this) —
	// mirror it instead of hand-filling every default.
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.Notifications = NotificationsConfig{
		Enabled: true,
		Sinks: []NotificationSinkConfig{{
			Name:  "hub",
			URL:   "http://192.168.1.20:9080/api/ingest/webhook",
			Token: "tok",
		}},
	}
	return cfg
}

func TestValidate_Notifications(t *testing.T) {
	if err := Validate(notifyConfig()); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidate_NotificationsErrors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"no sinks", func(c *Config) { c.Notifications.Sinks = nil }, "sinks is empty"},
		{"empty url", func(c *Config) { c.Notifications.Sinks[0].URL = "" }, "url is required"},
		{"bad url scheme", func(c *Config) { c.Notifications.Sinks[0].URL = "ftp://x" }, "must be an http(s) URL"},
		{"empty token", func(c *Config) { c.Notifications.Sinks[0].Token = " " }, "token is required"},
		{"bad severity", func(c *Config) { c.Notifications.Sinks[0].SeverityMin = "loud" }, "severity_min"},
		{"unknown event", func(c *Config) { c.Notifications.Sinks[0].Events = []string{"camera.typo"} }, "unknown event type"},
		{"negative timeout", func(c *Config) { c.Notifications.Sinks[0].TimeoutS = -1 }, "timeout_s"},
		{"huge timeout", func(c *Config) { c.Notifications.Sinks[0].TimeoutS = 999 }, "timeout_s"},
		{"base_url trailing slash", func(c *Config) { c.Notifications.BaseURL = "http://x:9090/" }, "trailing slash"},
		{"base_url not absolute", func(c *Config) { c.Notifications.BaseURL = "nvr.local" }, "base_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := notifyConfig()
			tc.mut(cfg)
			err := Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidate_NotificationsDisabled_Ignored(t *testing.T) {
	cfg := notifyConfig()
	cfg.Notifications.Enabled = false
	cfg.Notifications.Sinks = nil // would fail if validated
	if err := Validate(cfg); err != nil {
		t.Fatalf("disabled section must not be validated: %v", err)
	}
}

func TestSensitiveFieldPaths_Notifications(t *testing.T) {
	cfg := notifyConfig()
	cfg.Notifications.APIKey = "mbv_plain"
	cfg.Notifications.Sinks = append(cfg.Notifications.Sinks, NotificationSinkConfig{
		Name: "two", URL: "http://x", Token: "plain-token",
	})
	paths := SensitiveFieldPaths(cfg)
	joined := strings.Join(paths, ";")
	for _, want := range []string{"notifications.api_key", "notifications.sinks[0].token", "notifications.sinks[1].token"} {
		if !strings.Contains(joined, want) {
			t.Errorf("sensitive paths missing %q: %v", want, paths)
		}
	}
	// ENC:-prefixed values are not flagged.
	cfg.Notifications.APIKey = "ENC:xxx"
	if paths := SensitiveFieldPaths(cfg); strings.Contains(strings.Join(paths, ";"), "notifications.api_key") {
		t.Error("encrypted api_key must not be flagged as plaintext")
	}
}
