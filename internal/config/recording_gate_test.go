package config

import (
	"testing"
)

func TestRecordingGate(t *testing.T) {
	on, off := true, false
	tests := []struct {
		name       string
		cfg        *Config
		perCamera  *bool
		wantRecord bool
	}{
		{"nil config nil camera", nil, nil, true},
		{"default config nil camera", &Config{}, nil, true},
		{"global off nil camera", &Config{Recording: RecordingConfig{DefaultEnabled: &off}}, nil, false},
		{"global on nil camera", &Config{Recording: RecordingConfig{DefaultEnabled: &on}}, nil, true},
		{"explicit true beats global off", &Config{Recording: RecordingConfig{DefaultEnabled: &off}}, &on, true},
		{"explicit false beats global on", &Config{Recording: RecordingConfig{DefaultEnabled: &on}}, &off, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.RecordingGate(tc.perCamera); got != tc.wantRecord {
				t.Fatalf("RecordingGate() = %v, want %v", got, tc.wantRecord)
			}
		})
	}
}
