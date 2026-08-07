package config

import (
	"testing"
)

// TestIsValidStableID covers the real-world serial formats observed in the
// field (must accept) and the dirty values that have frozen rediscovery in
// production (must reject — see #216).
func TestIsValidStableID(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want bool
	}{
		// Accept — real formats seen on production cameras.
		{"lowercase 12-hex MAC", "744dbd988218", true},
		{"uppercase efuse MAC", "88492D665CCF", true},
		{"colon-separated MAC", "74:4d:bd:98:82:18", true},
		{"vendor serial with dash", "SN-AAA", true},
		{"xiaomi-style serial", "XIAOMI-CAM-001", true},
		{"underscore separator", "MiBee_Cam_001", true},
		{"alphanumeric only", "ABC123", true},
		{"minimum length 3", "abc", true},
		{"maximum length 64", "0123456789012345678901234567890123456789012345678901234567890123", true},

		// Reject — dirty values that broke rediscovery in production (#216).
		{"IPv4 address", "192.168.63.148", false}, // contains '.', not in class
		{"URL", "http://192.168.63.148", false},   // contains '/', ':', '.'
		{"all-zero MAC", "000000000000", false},   // all-same-character
		{"all-zero colon MAC", "00:00:00:00:00:00", false},
		{"all-f MAC", "ffffffffffff", false}, // all-same-character
		{"all-X serial", "XXXXXXXXXXXX", false},

		// Reject — structural violations.
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"too short (2 chars)", "ab", false},
		{"too long (65 chars)", "01234567890123456789012345678901234567890123456789012345678901234", false},
		{"contains space", "ABC 123", false},
		{"contains dot", "ABC.123", false},
		{"contains slash", "ABC/123", false},
		{"contains @", "ABC@123", false},
		{"contains chinese", "摄像头001", false},

		// Whitespace is trimmed — a valid serial with surrounding spaces passes.
		{"padded valid serial", "  ABC123  ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidStableID(tt.s); got != tt.want {
				t.Errorf("IsValidStableID(%q) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}

// TestValidateDirtyStableIDDoesNotBlockStartup is the regression guard for the
// production outage that happened when #216's config-time validation hard-failed
// on a pre-existing dirty stable_id (an IP persisted by a prior firmware glitch).
// Validate() MUST tolerate a dirty value — log a warning, defer to the async
// self-heal path — so that an upgrade never bricks startup on legacy data.
// The hard rejection happens at the API write boundary instead.
func TestValidateDirtyStableIDDoesNotBlockStartup(t *testing.T) {
	dirtyCases := []string{
		"192.168.63.148",   // IP (the actual production dirty value)
		"000000000000",     // all-zero MAC
		"http://cam/onvif", // URL
	}
	for _, dirty := range dirtyCases {
		t.Run(dirty, func(t *testing.T) {
			cfg := &Config{Cameras: []CameraConfig{{
				ID:       "cam-test",
				URL:      "rtsp://x",
				Protocol: "rtsp",
				StableID: dirty,
			}}}
			cfg.ApplyDefaults()
			if err := Validate(cfg); err != nil {
				t.Fatalf("Validate() must not reject a pre-existing dirty stable_id (would brick startup on legacy data, #216): %v", err)
			}
			// The dirty value is left in place for the async self-heal path to
			// overwrite — Validate does not mutate it.
			if cfg.Cameras[0].StableID != dirty {
				t.Fatalf("Validate() must not mutate stable_id (self-heal is async): got %q, want %q", cfg.Cameras[0].StableID, dirty)
			}
		})
	}
}

// TestValidateValidStableIDAccepted confirms a well-formed stable_id passes
// Validate() without issue (no false-positive rejection of legit serials).
func TestValidateValidStableIDAccepted(t *testing.T) {
	validCases := []string{
		"744dbd988218", // 12-hex MAC
		"88492D665CCF", // uppercase efuse MAC
		"SN-AAA",       // vendor serial
		"",             // empty (ONVIF auto-populates later) — also fine
	}
	for _, valid := range validCases {
		t.Run(valid, func(t *testing.T) {
			cfg := &Config{Cameras: []CameraConfig{{
				ID:       "cam-test",
				URL:      "rtsp://x",
				Protocol: "rtsp",
				StableID: valid,
			}}}
			cfg.ApplyDefaults()
			if err := Validate(cfg); err != nil {
				t.Fatalf("Validate() rejected a valid stable_id %q: %v", valid, err)
			}
		})
	}
}
