package config

import (
	"strings"
	"testing"
)

// TestRollingFragmentHoldAccessor — *int semantics (#852): nil = default-on
// (300s), explicit 0 = batching off, any other value passes through. A bare
// int could not distinguish "unset" from "off", which is why the field is a
// pointer (same pattern as RollingEnabled).
func TestRollingFragmentHoldAccessor(t *testing.T) {
	var zero MergeConfig
	if got := zero.RollingFragmentHoldValue(); got != 300 {
		t.Fatalf("nil pointer must resolve to the default 300s, got %d", got)
	}
	if DefaultRollingFragmentHoldS != 300 {
		t.Fatalf("DefaultRollingFragmentHoldS drifted from the documented 300s: %d", DefaultRollingFragmentHoldS)
	}
	off := MergeConfig{RollingFragmentHoldS: intPtrConfig(0)}
	if got := off.RollingFragmentHoldValue(); got != 0 {
		t.Fatalf("explicit 0 must disable batching, got %d", got)
	}
	custom := MergeConfig{RollingFragmentHoldS: intPtrConfig(120)}
	if got := custom.RollingFragmentHoldValue(); got != 120 {
		t.Fatalf("explicit value must pass through, got %d", got)
	}
}

// TestValidateRollingFragmentHoldRange — 0..3600 accepted, outside rejected
// (rolling merge defaults ON, so the block runs for typical configs).
func TestValidateRollingFragmentHoldRange(t *testing.T) {
	mk := func(v int) *Config {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Merge.RollingFragmentHoldS = intPtrConfig(v)
		return cfg
	}
	for _, ok := range []int{0, 1, 300, 3600} {
		if err := Validate(mk(ok)); err != nil {
			t.Fatalf("hold=%d must validate, got %v", ok, err)
		}
	}
	for _, bad := range []int{-1, 3601, 99999} {
		err := Validate(mk(bad))
		if err == nil || !strings.Contains(err.Error(), "rolling_fragment_hold_s") {
			t.Fatalf("hold=%d must fail validation with a rolling_fragment_hold_s error, got %v", bad, err)
		}
	}
}

// TestResolveMergeConfigFragmentHoldOverlay — a non-nil per-camera value
// (0 OR positive) overrides the global, mirroring RollingEnabled's pointer
// overlay so a camera can opt out of batching entirely.
func TestResolveMergeConfigFragmentHoldOverlay(t *testing.T) {
	global := MergeConfig{RollingFragmentHoldS: intPtrConfig(600)}
	perCam := &MergeConfig{RollingFragmentHoldS: intPtrConfig(0)}
	if got := ResolveMergeConfig(global, perCam).RollingFragmentHoldValue(); got != 0 {
		t.Fatalf("per-camera 0 must override global 600, got %d", got)
	}
	if got := ResolveMergeConfig(global, nil).RollingFragmentHoldValue(); got != 600 {
		t.Fatalf("nil per-camera keeps global, got %d", got)
	}
}

func intPtrConfig(i int) *int { return &i }
