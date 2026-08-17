package config

import "testing"

// NVR_STORAGE_CANDIDATES (#395): colon-separated container paths populate
// storage.candidates; blanks are dropped; unset env leaves the field alone.
func TestApplyDefaults_StorageCandidatesEnv(t *testing.T) {
	t.Helper()

	t.Run("parses colon list", func(t *testing.T) {
		t.Helper()
		t.Setenv("NVR_STORAGE_CANDIDATES", "/media/WDC:/media/SSD:")
		cfg := &Config{}
		cfg.ApplyDefaults()
		if len(cfg.Storage.Candidates) != 2 || cfg.Storage.Candidates[0] != "/media/WDC" || cfg.Storage.Candidates[1] != "/media/SSD" {
			t.Fatalf("candidates = %v", cfg.Storage.Candidates)
		}
	})

	t.Run("unset env keeps empty", func(t *testing.T) {
		t.Helper()
		t.Setenv("NVR_STORAGE_CANDIDATES", "")
		cfg := &Config{}
		cfg.ApplyDefaults()
		if len(cfg.Storage.Candidates) != 0 {
			t.Fatalf("candidates = %v, want empty", cfg.Storage.Candidates)
		}
	})
}
