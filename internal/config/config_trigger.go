package config

// TriggerConfig groups external trigger sources that start/stop on-demand
// capture (record / stop / snapshot). Today: MQTT actions and the HTTP
// webhook endpoint (issue #709). Both feed the same action dispatcher, so
// their action semantics are identical by construction.
type TriggerConfig struct {
	Webhook WebhookTriggerConfig `yaml:"webhook"`
}

// WebhookTriggerConfig configures the HMAC-signed webhook trigger endpoint
// `POST /api/trigger/webhook/{camera_id}?action=record|stop|snapshot`
// (issue #709). The endpoint is mounted on the public rate-limited route
// group — it carries NO BasicAuth; the pre-shared secret's HMAC signature is
// the credential, so third-party systems only ever hold the minimal key.
type WebhookTriggerConfig struct {
	// Enabled mounts the endpoint. default false.
	Enabled bool `yaml:"enabled"`

	// Secret is the pre-shared HMAC-SHA256 key. Required when enabled.
	// Encrypted via encrypt-config like mqtt.password.
	Secret string `yaml:"secret"`

	// ReplayWindowS bounds how old (or how far in the future) the signed
	// timestamp may be. default 300 (5 minutes).
	ReplayWindowS int `yaml:"replay_window_s"`
}

// ReplayWindow resolves the replay window with its default (5 minutes;
// non-positive values fall back too — config validation rejects negatives
// but hand-edited files may still carry them).
func (c WebhookTriggerConfig) ReplayWindow() int {
	if c.ReplayWindowS <= 0 {
		return 300
	}
	return c.ReplayWindowS
}
