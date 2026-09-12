package config

import (
	"strings"
	"testing"
	"time"
)

func TestDurability_ValidateRejectsUnknown(t *testing.T) {
	for _, tier := range []string{"", "strict", "relaxed"} {
		cfg := &Config{}
		applyConfigDefaults(cfg)
		cfg.Storage.Durability = tier
		cfg.Storage.SegmentDuration = "2m"
		if err := Validate(cfg); err != nil {
			if strings.Contains(err.Error(), "durability") {
				t.Errorf("Validate(durability=%q) = %v, want accepted", tier, err)
			}
		}
	}
	cfg := &Config{}
	applyConfigDefaults(cfg)
	cfg.Storage.Durability = "ludicrous"
	cfg.Storage.SegmentDuration = "2m"
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "storage.durability") {
		t.Errorf("Validate(durability=%q) = %v, want storage.durability error", "ludicrous", err)
	}
}

func TestDurability_DefaultStrict(t *testing.T) {
	cfg := &Config{}
	applyConfigDefaults(cfg)
	if cfg.Storage.Durability != "" {
		t.Errorf("default durability = %q, want empty (strict)", cfg.Storage.Durability)
	}
	_ = time.Second
}
