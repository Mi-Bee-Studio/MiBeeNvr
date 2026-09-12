package config

import (
	"strings"
	"testing"
)

func TestIOConfig_DefaultsDisabled(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	if cfg.IO.BudgetBytesPerSec != 0 {
		t.Errorf("default budget_bytes_per_sec = %d, want 0 (off)", cfg.IO.BudgetBytesPerSec)
	}
	if cfg.IO.BudgetBurstBytes != 0 {
		t.Errorf("default budget_burst_bytes = %d, want 0 (unspecified)", cfg.IO.BudgetBurstBytes)
	}
}

func TestIOConfig_BurstDefaultsToOneSecondOfRate(t *testing.T) {
	cfg := &Config{}
	cfg.IO.BudgetBytesPerSec = 50 << 20 // 50 MiB/s
	applyConfigDefaults(cfg)
	if cfg.IO.BudgetBurstBytes != 50<<20 {
		t.Errorf("default burst = %d, want %d (one second of rate)", cfg.IO.BudgetBurstBytes, 50<<20)
	}
}

func TestIOConfig_ExplicitBurstPreserved(t *testing.T) {
	cfg := &Config{}
	cfg.IO.BudgetBytesPerSec = 50 << 20
	cfg.IO.BudgetBurstBytes = 8 << 20
	applyConfigDefaults(cfg)
	if cfg.IO.BudgetBurstBytes != 8<<20 {
		t.Errorf("explicit burst = %d, want preserved 8MiB", cfg.IO.BudgetBurstBytes)
	}
}

func TestIOConfig_ValidateRejectsNegative(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	cfg.IO.BudgetBytesPerSec = -1
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "io.budget_bytes_per_sec") {
		t.Errorf("Validate(negative rate) = %v, want io.budget_bytes_per_sec error", err)
	}

	cfg = &Config{}
	applyConfigDefaults(cfg)
	cfg.IO.BudgetBytesPerSec = 1000
	cfg.IO.BudgetBurstBytes = -5
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "io.budget_burst_bytes") {
		t.Errorf("Validate(negative burst) = %v, want io.budget_burst_bytes error", err)
	}
}

func TestIOConfig_ValidateAcceptsDisabled(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	if err := Validate(cfg); err != nil {
		if !strings.Contains(err.Error(), "io.") {
			t.Logf("unrelated validation error (acceptable): %v", err)
		} else {
			t.Errorf("Validate(disabled io) = %v, want no io error", err)
		}
	}
}
