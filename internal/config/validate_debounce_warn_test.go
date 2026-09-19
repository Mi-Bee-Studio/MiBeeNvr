package config

import (
	"strings"
	"testing"
)

// TestRollingDebounce_LowValueWarns (#851 direction 1): a sub-2s debounce
// silently defeats fold batching — M5 ran 500ms through a flap storm (36
// folds/min) and the "tuning" looked deliberate. Mirror the #758
// segment_duration footgun: warn at validation, never block (test
// scenarios stay legal). #852's fragment hold queue covers <30s fragments
// on its own, but healthy segments still fold per dispatch.
func TestRollingDebounce_LowValueWarns(t *testing.T) {
	h := captureWarns(t)
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.Merge.RollingDebounce = "500ms"
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	found := false
	for _, w := range h.warns {
		if strings.Contains(w, "rolling_debounce") {
			found = true
		}
	}
	if !found {
		t.Error("500ms rolling_debounce must produce a warning")
	}

	// 5s (and the empty default, which resolves to 5s) stays silent.
	h2 := captureWarns(t)
	cfg2 := &Config{}
	cfg2.ApplyDefaults()
	cfg2.Merge.RollingDebounce = "5s"
	if err := Validate(cfg2); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, w := range h2.warns {
		if strings.Contains(w, "rolling_debounce") {
			t.Errorf("5s rolling_debounce must not warn, got: %s", w)
		}
	}

	cfg3 := &Config{}
	cfg3.ApplyDefaults()
	h3 := captureWarns(t)
	if err := Validate(cfg3); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, w := range h3.warns {
		if strings.Contains(w, "rolling_debounce") {
			t.Errorf("unset rolling_debounce must not warn, got: %s", w)
		}
	}
}
