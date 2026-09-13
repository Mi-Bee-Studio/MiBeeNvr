package config

import (
	"strings"
	"testing"
)

// rediscoveryTestConfig returns a minimal valid config for validation tests.
func rediscoveryTestConfig(t *testing.T) *Config {
	t.Helper()
	cfg := &Config{}
	applyConfigDefaults(cfg)
	return cfg
}

// TestApplyDefaults_ProbePortsDefault: an unset probe_ports list gets the
// default sweep table (80 standard, 8080 MiBeeCam/Hisilicon-style, 8899
// TVT/视通-style); an explicit list is preserved untouched.
func TestApplyDefaults_ProbePortsDefault(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	if len(cfg.Health.Rediscovery.ProbePorts) != 3 {
		t.Fatalf("expected default probe_ports [80 8080 8899], got %v", cfg.Health.Rediscovery.ProbePorts)
	}

	cfg2 := &Config{}
	cfg2.Health.Rediscovery.ProbePorts = []int{9000}
	applyConfigDefaults(cfg2)
	if p := cfg2.Health.Rediscovery.ProbePorts; len(p) != 1 || p[0] != 9000 {
		t.Fatalf("explicit probe_ports must be preserved, got %v", p)
	}
}

// TestValidateRediscovery_ProbePorts: probe_ports entries must be valid TCP
// ports (1-65535) and the list is capped to protect the RPi scan budget
// (candidates × ports × probe_timeout against max_duration).
func TestValidateRediscovery_ProbePorts(t *testing.T) {
	cfg := rediscoveryTestConfig(t)
	cfg.Health.Rediscovery.ProbePorts = []int{80, 0}
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for port 0")
	}

	cfg = rediscoveryTestConfig(t)
	cfg.Health.Rediscovery.ProbePorts = []int{80, 65536}
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for port 65536")
	}

	cfg = rediscoveryTestConfig(t)
	cfg.Health.Rediscovery.ProbePorts = make([]int, 9)
	for i := range cfg.Health.Rediscovery.ProbePorts {
		cfg.Health.Rediscovery.ProbePorts[i] = 9000 + i
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatalf("expected error for probe_ports longer than 8")
	}
	if !strings.Contains(err.Error(), "probe_ports") {
		t.Fatalf("error should mention probe_ports, got: %v", err)
	}

	// Valid list passes.
	cfg = rediscoveryTestConfig(t)
	cfg.Health.Rediscovery.ProbePorts = []int{80, 8080, 8899}
	if err := Validate(cfg); err != nil {
		t.Fatalf("valid probe_ports rejected: %v", err)
	}
}
