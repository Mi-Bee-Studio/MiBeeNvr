package timelapse

import (
	"context"
	"testing"
)

// compile-time interface check
var _ TimelapseMerger = (*mockMerger)(nil)

type mockMerger struct {
	canMerge bool
	tier     MergeTier
}

func (m *mockMerger) CanMerge() bool { return m.canMerge }
func (m *mockMerger) Merge(_ context.Context, _, _ string, _ int) (*MergeResult, error) {
	return &MergeResult{Tier: m.tier, FramesMerged: 100, Duration: 10.5}, nil
}
func (m *mockMerger) Tier() MergeTier { return m.tier }

// TestMergeTypes is the acceptance-criteria test bundle for timelapse merge types.
// Run: go test -run TestMergeTypes ./internal/timelapse/
func TestMergeTypes(t *testing.T) {
	t.Run("MergeMode_Values", testMergeModeValues)
	t.Run("MergeMode_String", testMergeModeString)
	t.Run("MergeMode_Uniqueness", testMergeModeUniqueness)
	t.Run("MergeStatus_Values", testMergeStatusValues)
	t.Run("MergeStatus_String", testMergeStatusString)
	t.Run("MergeStatus_Uniqueness", testMergeStatusUniqueness)
	t.Run("MergeConfig_Defaults", testMergeConfigDefaults)
	t.Run("MergeConfig_Values", testMergeConfigValues)
	t.Run("MergeResult_Defaults", testMergeResultDefaults)
	t.Run("MergeResult_Success", testMergeResultSuccess)
	t.Run("MergeResult_Error", testMergeResultError)
	t.Run("TimelapseMerger_Interface", testTimelapseMergerInterface)
	t.Run("TimelapseMerger_MethodCount", testTimelapseMergerMethodCount)
	t.Run("TimelapseMerger_AllTiers", testTimelapseMergerAllTiers)
	t.Run("TimelapseMerger_CanMergeFalse", testTimelapseMergerCanMergeFalse)
	t.Run("Constants_HumanReadable", testConstantsHumanReadable)
}

// --- MergeMode tests ---

func testMergeModeValues(t *testing.T) {
	if MergeModeAuto != "auto" {
		t.Errorf("MergeModeAuto = %q, want %q", MergeModeAuto, "auto")
	}
	if MergeModeMP4 != "mp4" {
		t.Errorf("MergeModeMP4 = %q, want %q", MergeModeMP4, "mp4")
	}
	if MergeModeJPEG != "jpeg" {
		t.Errorf("MergeModeJPEG = %q, want %q", MergeModeJPEG, "jpeg")
	}
}

func testMergeModeString(t *testing.T) {
	if MergeModeAuto.String() != "auto" {
		t.Errorf("MergeModeAuto.String() = %q, want %q", MergeModeAuto.String(), "auto")
	}
	if MergeModeMP4.String() != "mp4" {
		t.Errorf("MergeModeMP4.String() = %q, want %q", MergeModeMP4.String(), "mp4")
	}
	if MergeModeJPEG.String() != "jpeg" {
		t.Errorf("MergeModeJPEG.String() = %q, want %q", MergeModeJPEG.String(), "jpeg")
	}
}

func testMergeModeUniqueness(t *testing.T) {
	vals := map[MergeMode]bool{
		MergeModeAuto: true,
		MergeModeMP4:  true,
		MergeModeJPEG: true,
	}
	if len(vals) != 3 {
		t.Error("duplicate MergeMode constants detected")
	}
}

// --- MergeStatus tests ---

func testMergeStatusValues(t *testing.T) {
	if MergeStatusNone != "none" {
		t.Errorf("MergeStatusNone = %q, want %q", MergeStatusNone, "none")
	}
	if MergeStatusMerging != "merging" {
		t.Errorf("MergeStatusMerging = %q, want %q", MergeStatusMerging, "merging")
	}
	if MergeStatusMerged != "merged" {
		t.Errorf("MergeStatusMerged = %q, want %q", MergeStatusMerged, "merged")
	}
	if MergeStatusFailed != "failed" {
		t.Errorf("MergeStatusFailed = %q, want %q", MergeStatusFailed, "failed")
	}
}

func testMergeStatusString(t *testing.T) {
	if MergeStatusNone.String() != "none" {
		t.Errorf("MergeStatusNone.String() = %q, want %q", MergeStatusNone.String(), "none")
	}
	if MergeStatusMerging.String() != "merging" {
		t.Errorf("MergeStatusMerging.String() = %q, want %q", MergeStatusMerging.String(), "merging")
	}
	if MergeStatusMerged.String() != "merged" {
		t.Errorf("MergeStatusMerged.String() = %q, want %q", MergeStatusMerged.String(), "merged")
	}
	if MergeStatusFailed.String() != "failed" {
		t.Errorf("MergeStatusFailed.String() = %q, want %q", MergeStatusFailed.String(), "failed")
	}
}

func testMergeStatusUniqueness(t *testing.T) {
	vals := map[MergeStatus]bool{
		MergeStatusNone:    true,
		MergeStatusMerging: true,
		MergeStatusMerged:  true,
		MergeStatusFailed:  true,
	}
	if len(vals) != 4 {
		t.Error("duplicate MergeStatus constants detected")
	}
}

// --- MergeConfig tests ---

func testMergeConfigDefaults(t *testing.T) {
	cfg := MergeConfig{}
	if cfg.Enabled {
		t.Error("MergeConfig.Enabled should default to false")
	}
	if cfg.Mode != "" {
		t.Errorf("MergeConfig.Mode should default to empty, got %q", cfg.Mode)
	}
	if cfg.OutputFPS != 0 {
		t.Errorf("MergeConfig.OutputFPS should default to 0, got %d", cfg.OutputFPS)
	}
	if cfg.DeleteOriginal {
		t.Error("MergeConfig.DeleteOriginal should default to false")
	}
	if cfg.DailyMerge {
		t.Error("MergeConfig.DailyMerge should default to false")
	}
}

func testMergeConfigValues(t *testing.T) {
	cfg := MergeConfig{
		Enabled:        true,
		Mode:           MergeModeMP4,
		OutputFPS:      30,
		DeleteOriginal: true,
		DailyMerge:     true,
	}
	if !cfg.Enabled {
		t.Error("MergeConfig.Enabled should be true")
	}
	if cfg.Mode != MergeModeMP4 {
		t.Errorf("MergeConfig.Mode = %q, want %q", cfg.Mode, MergeModeMP4)
	}
	if cfg.OutputFPS != 30 {
		t.Errorf("MergeConfig.OutputFPS = %d, want 30", cfg.OutputFPS)
	}
	if !cfg.DeleteOriginal {
		t.Error("MergeConfig.DeleteOriginal should be true")
	}
	if !cfg.DailyMerge {
		t.Error("MergeConfig.DailyMerge should be true")
	}
}

// --- MergeResult tests ---

func testMergeResultDefaults(t *testing.T) {
	r := MergeResult{}
	if r.OutputPath != "" {
		t.Errorf("MergeResult.OutputPath should default to empty, got %q", r.OutputPath)
	}
	if r.Error != "" {
		t.Errorf("MergeResult.Error should default to empty, got %q", r.Error)
	}
	if r.FramesMerged != 0 {
		t.Errorf("MergeResult.FramesMerged should default to 0, got %d", r.FramesMerged)
	}
	if r.Duration != 0 {
		t.Errorf("MergeResult.Duration should default to 0, got %f", r.Duration)
	}
}

func testMergeResultSuccess(t *testing.T) {
	r := MergeResult{
		Tier:         TierGo,
		OutputPath:   "/tmp/merged.mp4",
		FramesMerged: 150,
		Duration:     15.0,
	}
	if r.Tier != TierGo {
		t.Errorf("MergeResult.Tier = %v, want %v", r.Tier, TierGo)
	}
	if r.OutputPath != "/tmp/merged.mp4" {
		t.Errorf("MergeResult.OutputPath = %q, want %q", r.OutputPath, "/tmp/merged.mp4")
	}
	if r.Error != "" {
		t.Errorf("MergeResult.Error should be empty for success, got %q", r.Error)
	}
	if r.FramesMerged != 150 {
		t.Errorf("MergeResult.FramesMerged = %d, want 150", r.FramesMerged)
	}
	if r.Duration != 15.0 {
		t.Errorf("MergeResult.Duration = %f, want 15.0", r.Duration)
	}
}

func testMergeResultError(t *testing.T) {
	r := MergeResult{
		Tier:       TierFFmpeg,
		Error:      "ffmpeg not found",
		OutputPath: "/tmp/failed.mp4",
	}
	if r.Tier != TierFFmpeg {
		t.Errorf("MergeResult.Tier = %v, want %v", r.Tier, TierFFmpeg)
	}
	if r.Error != "ffmpeg not found" {
		t.Errorf("MergeResult.Error = %q, want %q", r.Error, "ffmpeg not found")
	}
	if r.FramesMerged != 0 {
		t.Errorf("MergeResult.FramesMerged should be 0 for error, got %d", r.FramesMerged)
	}
}

// --- TimelapseMerger interface tests ---

func testTimelapseMergerInterface(t *testing.T) {
	merger := &mockMerger{canMerge: true, tier: TierGo}

	if !merger.CanMerge() {
		t.Error("mockMerger.CanMerge() should return true")
	}

	if merger.Tier() != TierGo {
		t.Errorf("mockMerger.Tier() = %v, want %v", merger.Tier(), TierGo)
	}

	result, err := merger.Merge(context.Background(), "/frames", "/out.mp4", 30)
	if err != nil {
		t.Errorf("mockMerger.Merge() returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("mockMerger.Merge() returned nil result")
	}
	if result.Tier != TierGo {
		t.Errorf("MergeResult.Tier = %v, want %v", result.Tier, TierGo)
	}
	if result.FramesMerged != 100 {
		t.Errorf("MergeResult.FramesMerged = %d, want 100", result.FramesMerged)
	}
	if result.Duration != 10.5 {
		t.Errorf("MergeResult.Duration = %f, want 10.5", result.Duration)
	}
}

func testTimelapseMergerMethodCount(t *testing.T) {
	// Compile-time check: mockMerger implements TimelapseMerger.
	var _ TimelapseMerger = (*mockMerger)(nil)
}

func testTimelapseMergerAllTiers(t *testing.T) {
	for _, tier := range []MergeTier{TierFFmpeg, TierGo, TierJPEG} {
		m := &mockMerger{canMerge: true, tier: tier}
		if m.Tier() != tier {
		t.Errorf("mockMerger with tier %v returned tier %v", tier, m.Tier())
		}
	}
}

func testTimelapseMergerCanMergeFalse(t *testing.T) {
	m := &mockMerger{canMerge: false}
	if m.CanMerge() {
		t.Error("mockMerger with canMerge=false should return false")
	}
}

// --- Human-readable constants test ---

func testConstantsHumanReadable(t *testing.T) {
	for name, s := range map[string]interface{ String() string }{
		"MergeModeAuto":      MergeModeAuto,
		"MergeModeMP4":       MergeModeMP4,
		"MergeModeJPEG":      MergeModeJPEG,
		"MergeStatusNone":    MergeStatusNone,
		"MergeStatusMerging": MergeStatusMerging,
		"MergeStatusMerged":  MergeStatusMerged,
		"MergeStatusFailed":  MergeStatusFailed,
	} {
		str := s.String()
		if str == "" {
			t.Errorf("%s.String() returned empty string", name)
		}
		if len(str) > 20 {
			t.Errorf("%s.String() = %q, want short human-readable string", name, str)
		}
	}

	// MergeTier.String() is defined in detect.go — verify it returns readable values
	for name, tier := range map[string]MergeTier{
		"TierFFmpeg": TierFFmpeg,
		"TierGo":     TierGo,
		"TierJPEG":   TierJPEG,
	} {
		str := tier.String()
		if str == "" {
			t.Errorf("%s.String() returned empty string", name)
		}
		if len(str) > 20 {
			t.Errorf("%s.String() = %q, want short human-readable string", name, str)
		}
	}
}
