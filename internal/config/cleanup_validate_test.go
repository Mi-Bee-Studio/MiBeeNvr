package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// CleanupConfig.Validate is the single source of truth for the cleanup
// bounds — the startup load and the settings PUT both route through it
// (#867). The field-test crash value (disk_threshold_percent=20) must stay
// rejected here forever; these cases pin both windows to the load-side
// contract.
func TestCleanupConfigValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		cleanup CleanupConfig
		wantErr string // "" = must pass
	}{
		{"defaults", CleanupConfig{RetentionDays: 30, DiskThresholdPercent: 85}, ""},
		{"retention lower bound", CleanupConfig{RetentionDays: 1, DiskThresholdPercent: 85}, ""},
		{"retention upper bound", CleanupConfig{RetentionDays: 3650, DiskThresholdPercent: 85}, ""},
		{"retention zero", CleanupConfig{RetentionDays: 0, DiskThresholdPercent: 85}, "retention_days"},
		{"retention over max", CleanupConfig{RetentionDays: 3651, DiskThresholdPercent: 85}, "retention_days"},
		{"threshold lower bound", CleanupConfig{RetentionDays: 30, DiskThresholdPercent: 50}, ""},
		{"threshold upper bound", CleanupConfig{RetentionDays: 30, DiskThresholdPercent: 99}, ""},
		{"threshold just below", CleanupConfig{RetentionDays: 30, DiskThresholdPercent: 49}, "disk_threshold_percent"},
		{"threshold just over", CleanupConfig{RetentionDays: 30, DiskThresholdPercent: 100}, "disk_threshold_percent"},
		{"field-test crash value", CleanupConfig{RetentionDays: 30, DiskThresholdPercent: 20}, "disk_threshold_percent"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.cleanup.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
			require.True(t, strings.HasPrefix(err.Error(), "cleanup."), "error must name the config section")
		})
	}
}
