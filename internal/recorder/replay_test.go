package recorder

import (
	"testing"
	"time"
)

// TestReplayAdaptive_MeasuresTLShare (#639): a mostly-static series with a
// mid-file activity burst replays to a high TL share with at least one mode
// switch; a pure-burst series stays near zero.
func TestReplayAdaptive_MeasuresTLShare(t *testing.T) {
	// 20s static @20fps (400 frames of 800B) → enters TL (60s threshold NOT
	// met... use CalmThreshold 5s), then 1s burst, then static again.
	cfg := AdaptiveConfig{CalmThreshold: 5 * time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 5.0, MaxGOPBuffer: 1 << 20}
	var frames []ReplayFrame
	for range 1200 { // 60s static
		frames = append(frames, ReplayFrame{Size: 800})
	}
	for range 20 { // 1s of clustered motion bursts
		frames = append(frames, ReplayFrame{Size: 30000})
	}
	for range 600 { // 30s static again
		frames = append(frames, ReplayFrame{Size: 800})
	}
	st := ReplayAdaptive(frames, 50*time.Millisecond, cfg)
	if st.Frames != len(frames) {
		t.Fatalf("frames = %d, want %d", st.Frames, len(frames))
	}
	if st.TLShare < 0.5 {
		t.Fatalf("mostly-static series should replay with TLShare ≥ 0.5, got %.2f", st.TLShare)
	}
	if st.Switches < 1 {
		t.Fatal("activity burst must cause at least one mode switch in replay")
	}

	// Continuous busy series: clustered spike pairs every 3s (real-motion
	// shape) keep resetting the calm window, so TL is never entered.
	var busy []ReplayFrame
	for i := range 1200 {
		size := 800
		if i%60 == 0 || i%60 == 1 { // pair of spikes within 100ms
			size = 30000
		}
		busy = append(busy, ReplayFrame{Size: size})
	}
	stBusy := ReplayAdaptive(busy, 50*time.Millisecond, cfg)
	if stBusy.TLShare > 0.2 {
		t.Fatalf("busy series should stay mostly NORMAL, TLShare=%.2f", stBusy.TLShare)
	}
}

// TestReplayAdaptive_NoVideoExitStaysSparse (#638 via replay): the same busy
// series under NoVideoExit replays near-100% TL — the corpus-visible shape
// of resident timelapse.
func TestReplayAdaptive_NoVideoExitStaysSparse(t *testing.T) {
	cfg := AdaptiveConfig{CalmThreshold: 5 * time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 5.0, MaxGOPBuffer: 1 << 20, NoVideoExit: true}
	var frames []ReplayFrame
	for i := range 1500 {
		size := 800
		if i%10 < 4 {
			size = 30000 // dense bursts throughout
		}
		frames = append(frames, ReplayFrame{Size: size})
	}
	st := ReplayAdaptive(frames, 50*time.Millisecond, cfg)
	if st.TLShare < 0.9 {
		t.Fatalf("NoVideoExit replay should stay ~100%% TL, got %.2f", st.TLShare)
	}
	if st.Switches > 1 {
		t.Fatalf("NoVideoExit replay should have at most the initial entry switch, got %d", st.Switches)
	}
}
