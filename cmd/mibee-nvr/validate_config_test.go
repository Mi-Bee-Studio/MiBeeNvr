package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// TestRunValidateConfigExitCodes pins the CLI contract of the pre-deploy
// validation subcommand: 0 for a config that would boot, 1 for anything that
// would brick the NVR at startup. This is the prevention tool for the
// 2026-09-11 crash-loop class — hand-edited YAML can now be checked BEFORE
// systemctl restart.
func TestRunValidateConfigExitCodes(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good.yaml")
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	if err := config.Save(good, cfg); err != nil {
		t.Fatal(err)
	}

	// retention_days outside the 1..3650 range fails Validate() fatally.
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("cleanup:\n  retention_days: 99999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"valid config exits 0", []string{"mibee-nvr", "validate-config", "--config", good}, 0},
		{"invalid config exits 1", []string{"mibee-nvr", "validate-config", "--config", bad}, 1},
		{"missing config exits 1", []string{"mibee-nvr", "validate-config", "--config", filepath.Join(dir, "nope.yaml")}, 1},
		{"default path falls back to ./mibee-nvr.yaml", []string{"mibee-nvr", "validate-config"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runValidateConfig(tt.args); got != tt.want {
				t.Errorf("runValidateConfig(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}
