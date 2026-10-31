package config

// observability.tracing + remote_log.format=otlp validation and defaults.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyDefaults_TracingSampleRatio(t *testing.T) {
	var cfg Config
	cfg.ApplyDefaults()
	require.InDelta(t, 0.1, cfg.Observability.Tracing.SampleRatio, 1e-9, "unset ratio defaults to 0.1")
}

func TestValidate_Tracing(t *testing.T) {
	newCfg := func(mutate func(*ObservabilityTracingConfig)) *Config {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Observability.Tracing.Enabled = true
		mutate(&cfg.Observability.Tracing)
		return cfg
	}

	require.NoError(t, Validate(newCfg(func(tr *ObservabilityTracingConfig) {
		tr.Endpoint = "http://collector:4318/v1/traces"
	})), "well-formed enabled tracing must validate")
	require.NoError(t, Validate(newCfg(func(tr *ObservabilityTracingConfig) {
		tr.Endpoint = "https://collector.example.com/v1/traces"
		tr.SampleRatio = 1
	})), "https + full sampling must validate")

	for _, bad := range []string{"", "not-a-url", "ftp://collector:4318/v1/traces", "http://"} {
		require.Error(t, Validate(newCfg(func(tr *ObservabilityTracingConfig) {
			tr.Endpoint = bad
		})), "endpoint %q must be rejected", bad)
	}
	require.Error(t, Validate(newCfg(func(tr *ObservabilityTracingConfig) {
		tr.Endpoint = "http://collector:4318/v1/traces"
		tr.SampleRatio = 1.5
	})), "sample_ratio above 1 must be rejected")
	require.Error(t, Validate(newCfg(func(tr *ObservabilityTracingConfig) {
		tr.Endpoint = "http://collector:4318/v1/traces"
		tr.SampleRatio = -0.1
	})), "negative sample_ratio must be rejected")

	// Disabled tracing never validates its endpoint.
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.Observability.Tracing.Endpoint = "bogus"
	require.NoError(t, Validate(cfg), "disabled tracing ignores endpoint shape")
}

func TestValidate_RemoteLogOTLPFormat(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.RemoteLog.Enabled = true
	cfg.RemoteLog.Endpoint = "http://victorialogs:9428/opentelemetry/v1/logs"
	cfg.RemoteLog.Format = "otlp"
	require.NoError(t, Validate(cfg), "otlp format with endpoint must validate")

	cfg.RemoteLog.Format = "banana"
	require.Error(t, Validate(cfg), "unknown format must be rejected")
}
