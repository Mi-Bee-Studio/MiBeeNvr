package app

import "testing"

// TestBuildAppDeps_RollingTranscodeRailFollowsTaskCreator guards the #817
// round-2 wiring: the rolling merge age rail must resolve transcode
// enablement through cfg.ResolveTranscodingConfig — the SAME resolution the
// transcode task creator uses. The constructor's per-camera-block-only
// lookup misclassified DB-managed cameras (absent from the yaml snapshot,
// resolved against the global switch) and silently disabled the rail while
// their tasks kept flowing (M5 production 2026-09-16: task pending 3.8s
// before its input was folded). The wiring-bug class: a resolution seam
// drifting from its producer's semantics — same lesson as #653's
// onAction=nil. Per-camera-override semantics are pinned separately by
// config.ResolveTranscodingConfig's own tests.
func TestBuildAppDeps_RollingTranscodeRailFollowsTaskCreator(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.Transcoding.Enabled = true // global on; camera deliberately NOT in cfg.Cameras (DB-managed)

	deps, cleanupFn, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanupFn()

	if deps.recordRollingMergeMgr == nil {
		t.Fatal("deps.recordRollingMergeMgr is nil")
	}
	if !deps.recordRollingMergeMgr.CameraTranscodeEnabled("cam-db-only") {
		t.Error("age rail disabled for a DB-managed camera with global transcoding on — " +
			"the rail must follow the task creator's global+per-camera resolution (#817 round-2 race)")
	}
}
