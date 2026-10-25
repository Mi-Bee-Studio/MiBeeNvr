package config

// NotificationsConfig configures outbound alert forwarding to external
// webhook receivers (alerting hubs, chat robots, monitoring platforms).
// The NVR produces structured alerts — camera offline/frozen/recovered,
// storage health, motion, AI detections, doorbell rings — and POSTs them as
// a small self-describing JSON envelope; everything downstream (dedup,
// aggregation, escalation, channel routing, localization) belongs to the
// receiver. Requires a restart to apply (read once at startup).
type NotificationsConfig struct {
	// Enabled turns the dispatcher on. default false.
	Enabled bool `yaml:"enabled"`

	// BaseURL is the externally reachable base URL of THIS NVR, used to
	// build snapshot links embedded in alerts (e.g.
	// "http://192.168.1.10:9090"). Must be reachable from the receiver.
	// Falls back to server.advertise_url when empty; when both are empty,
	// alerts deliver without snapshot links.
	BaseURL string `yaml:"base_url"`

	// APIKey is a dedicated mbv_ API key embedded in snapshot URLs so the
	// receiver (or a user following the link) can fetch them. Use a
	// separate, revocable key — snapshot URLs are stored by receivers.
	// Encrypted via encrypt-config like mqtt.password. Empty = no snapshot
	// links even when base_url is set.
	APIKey string `yaml:"api_key"`

	// Sinks is the list of webhook receivers. Each gets its own bounded
	// queue and delivery worker; one dead receiver never affects another.
	Sinks []NotificationSinkConfig `yaml:"sinks"`
}

// NotificationSinkConfig is one webhook receiver.
type NotificationSinkConfig struct {
	// Name labels the sink in logs and the status endpoint.
	Name string `yaml:"name"`

	// URL is the receiver's ingest base URL; the token is appended as the
	// final path segment. Example: "http://192.168.1.20:9080/api/ingest/webhook".
	URL string `yaml:"url"`

	// Token is the path credential. Encrypted via encrypt-config.
	Token string `yaml:"token"`

	// Events filters which alert types this sink receives (see the notify
	// package's vocabulary). Empty = all types.
	Events []string `yaml:"events"`

	// SeverityMin drops alerts below this severity: info | warning | error
	// | critical. default info (no filter).
	SeverityMin string `yaml:"severity_min"`

	// TimeoutS is the per-request timeout. default 5.
	TimeoutS int `yaml:"timeout_s"`

	// BurstPerMin caps outbound posts per minute. default 30.
	BurstPerMin int `yaml:"burst_per_min"`
}
