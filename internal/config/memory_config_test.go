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

func TestMemoryConfig_AutoParamsDefaults(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	if cfg.Memory.AutoPhysicalPercent != 45 || cfg.Memory.AutoCapBytes != 1<<30 || cfg.Memory.AutoCgroupPercent != 80 {
		t.Errorf("auto defaults = %d/%d/%d, want 45/1GiB/80",
			cfg.Memory.AutoPhysicalPercent, cfg.Memory.AutoCapBytes, cfg.Memory.AutoCgroupPercent)
	}

	cfg = &Config{}
	cfg.Memory.AutoPhysicalPercent = 30
	applyConfigDefaults(cfg)
	if cfg.Memory.AutoPhysicalPercent != 30 {
		t.Errorf("explicit auto_physical_percent = %d, want preserved 30", cfg.Memory.AutoPhysicalPercent)
	}
}

func TestMemoryConfig_AutoParamsValidate(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	cfg.Memory.AutoPhysicalPercent = 99
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "auto_physical_percent") {
		t.Errorf("Validate(auto_physical_percent=99) = %v, want range error", err)
	}

	cfg = &Config{}
	applyConfigDefaults(cfg)
	cfg.Memory.AutoCgroupPercent = 0 // explicitly zero → "use default" is legal (defaults fill it)
	cfg.Memory.AutoPhysicalPercent = 45
	if err := Validate(cfg); err != nil {
		if strings.Contains(err.Error(), "auto_") {
			t.Errorf("Validate(zero auto cgroup) = %v, want no auto error", err)
		}
	}
}
