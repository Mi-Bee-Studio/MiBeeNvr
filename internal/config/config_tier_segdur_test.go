package config

import "testing"

// TestValidateTierSegmentDuration pins the cameras[].tier_segment_duration
// bounds: empty ok, 10s..6h ok, below/above/garbage rejected.
func TestValidateTierSegmentDuration(t *testing.T) {
	valid := []string{"", "10s", "5m", "1h", "6h"}
	invalid := map[string]string{
		"9s":   "below floor",
		"6h1s": "above ceiling",
		"nope": "unparseable",
		"-5m":  "negative",
		"0s":   "zero",
	}
	for _, v := range valid {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Cameras = []CameraConfig{{
			ID: "cam-1", Protocol: "onvif", URL: "http://127.0.0.1/onvif/device_service",
			RecordingTier: "tiered", TierSegmentDuration: v,
		}}
		if err := Validate(cfg); err != nil {
			t.Errorf("value %q should be valid, got: %v", v, err)
		}
	}
	for v, why := range invalid {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Cameras = []CameraConfig{{
			ID: "cam-1", Protocol: "onvif", URL: "http://127.0.0.1/onvif/device_service",
			RecordingTier: "tiered", TierSegmentDuration: v,
		}}
		if err := Validate(cfg); err == nil {
			t.Errorf("value %q (%s) should be rejected", v, why)
		}
	}
}
