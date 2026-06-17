package middleware

import "github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"

// authMetrics holds the optional metrics instance for auth event tracking.
// Set via SetAuthMetrics() from main.go. When nil, auth events are not counted
// (no panic). This avoids threading *metrics.Metrics through NewAuthMiddleware's
// signature, which is called in multiple places.
var authMetrics *metrics.Metrics

// SetAuthMetrics injects the Prometheus metrics instance for auth tracking.
// Call once during startup (before serving requests).
func SetAuthMetrics(m *metrics.Metrics) {
	authMetrics = m
}

// recordAuthAttempt records an auth attempt result.
// result: "success", "failure", or "no_password".
func recordAuthAttempt(result string) {
	if authMetrics != nil {
		authMetrics.AuthAttemptsTotal.WithLabelValues(result).Inc()
	}
}

// recordAuthRateLimited records a rate-limited request.
func recordAuthRateLimited() {
	if authMetrics != nil {
		authMetrics.AuthRateLimitedTotal.Inc()
	}
}
