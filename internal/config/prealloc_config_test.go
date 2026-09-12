package config

import (
	"strings"
	"testing"
)

func TestPreallocDefaults(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	if !cfg.Storage.PreallocEnabled || cfg.Storage.PreallocHeadroomPercent != 10 ||
		cfg.Storage.PreallocMinBytes != 4<<20 || cfg.Storage.PreallocMaxBytes != 512<<20 {
		t.Errorf("prealloc defaults = %v/%d/%d/%d, want true/10/4MiB/512MiB",
			cfg.Storage.PreallocEnabled, cfg.Storage.PreallocHeadroomPercent,
			cfg.Storage.PreallocMinBytes, cfg.Storage.PreallocMaxBytes)
	}

	// Explicit disable stays disabled.
	cfg = &Config{}
	cfg.Storage.PreallocEnabled = false
	cfg.Storage.PreallocHeadroomPercent = 10 // explicit any knob marks the section as set
	applyConfigDefaults(cfg)
	if cfg.Storage.PreallocEnabled {
		t.Error("explicit prealloc_enabled=false must stay false")
	}
}

func TestPreallocValidate(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	cfg.Storage.PreallocHeadroomPercent = 500
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "prealloc_headroom_percent") {
		t.Errorf("Validate(headroom=500) = %v, want range error", err)
	}

	cfg = &Config{}
	applyConfigDefaults(cfg)
	cfg.Storage.PreallocMinBytes = 600 << 20 // > max
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "prealloc_min_bytes") {
		t.Errorf("Validate(min>max) = %v, want ordering error", err)
	}
}
