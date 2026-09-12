package config

import (
	"strings"
	"testing"
)

func TestMemoryConfig_DefaultsOff(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	if cfg.Memory.SoftLimitBytes != 0 {
		t.Errorf("default soft_limit_bytes = %d, want 0 (auto)", cfg.Memory.SoftLimitBytes)
	}
	if cfg.Memory.DisableAutoLimit {
		t.Error("default disable_auto_limit should be false (auto heuristic on)")
	}
}

func TestMemoryConfig_Validate(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	cfg.Memory.SoftLimitBytes = -1
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "memory.soft_limit_bytes") {
		t.Errorf("Validate(negative soft limit) = %v, want memory.soft_limit_bytes error", err)
	}

	cfg = &Config{}
	applyConfigDefaults(cfg)
	cfg.Memory.SoftLimitBytes = 16 << 20 // below the 64MiB floor
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "memory.soft_limit_bytes") {
		t.Errorf("Validate(sub-floor soft limit) = %v, want guidance error", err)
	}

	cfg = &Config{}
	applyConfigDefaults(cfg)
	cfg.Memory.DisableAutoLimit = true
	if err := Validate(cfg); err != nil {
		if strings.Contains(err.Error(), "memory.") {
			t.Errorf("Validate(disable only) = %v, want no memory error", err)
		}
	}
}
