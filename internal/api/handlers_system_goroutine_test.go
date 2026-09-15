package api

// The goroutine tripwire in /api/health was a fixed 1000 — but production
// shows ~85 goroutines per recording camera (recorder + streamhub +
// reconnect loops), so a 16-camera box idles at ~1365 and reported
// "unhealthy" forever (M5, 2026-09-15; Docker HEALTHCHECK consumers get a
// permanently sick container). The threshold must scale with the camera
// fleet so the tripwire sits at roughly 2x steady state for ANY deployment
// size — small boxes keep their sensitivity, a runaway leak still crosses.

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGoroutineCheckStatus_ScalesWithCameras(t *testing.T) {
	t.Parallel()

	// Small deployment: the tripwire stays in the old fixed-1000 ballpark
	// (4 cameras → 900).
	status, _ := goroutineCheckStatus(899, 4, 300, 150)
	require.Equal(t, "ok", status)
	status, _ = goroutineCheckStatus(901, 4, 300, 150) // 300+150×4 = 900
	require.Equal(t, "error", status)

	// The production case that filed this: 16 cameras idle at ~1365 —
	// healthy under the old fixed 1000 it never was.
	status, msg := goroutineCheckStatus(1365, 16, 300, 150) // threshold 2700
	require.Equal(t, "ok", status, "16-camera steady state must not be flagged")
	require.NotContains(t, msg, "error")

	// A leak still trips it: 2× steady state and beyond.
	status, _ = goroutineCheckStatus(4200, 16, 300, 150)
	require.Equal(t, "error", status)

	// Large fleets: threshold keeps scaling (32 cams → 5100).
	status, _ = goroutineCheckStatus(5000, 32, 300, 150)
	require.Equal(t, "ok", status)
	status, _ = goroutineCheckStatus(5101, 32, 300, 150)
	require.Equal(t, "error", status)

	// Zero/unknown camera count degrades to the conservative base rail.
	status, _ = goroutineCheckStatus(301, 0, 300, 150)
	require.Equal(t, "error", status)
}

// TestGoroutineCheckStatus_ExplicitCoefficients pins the configurable form:
// operators tune baseline and per-camera cost for heavier workloads
// (sub-stream consumers, cascade, relay targets raise the per-camera
// goroutine count above the observed ~85).
func TestGoroutineCheckStatus_ExplicitCoefficients(t *testing.T) {
	t.Parallel()

	// A relay-heavy deployment doubles the per-camera coefficient.
	status, _ := goroutineCheckStatus(1365, 16, 300, 300) // threshold 5100
	require.Equal(t, "ok", status)
	status, _ = goroutineCheckStatus(5101, 16, 300, 300)
	require.Equal(t, "error", status)

	// A pure-forwarder box lowers its baseline.
	status, _ = goroutineCheckStatus(151, 4, 100, 150) // threshold 700
	require.Equal(t, "ok", status)
	status, _ = goroutineCheckStatus(701, 4, 100, 150)
	require.Equal(t, "error", status)
}

// TestResolveGoroutinePolicy_DefaultsAndOverride pins the handler-side
// resolution: explicit config wins, nil/zero config falls back to the
// observed-workload defaults (baseline 300, per-camera 150).
func TestResolveGoroutinePolicy_DefaultsAndOverride(t *testing.T) {
	t.Parallel()

	b, p := resolveGoroutinePolicy(nil)
	require.Equal(t, 300, b)
	require.Equal(t, 150, p)

	b, p = resolveGoroutinePolicy(&config.Config{})
	require.Equal(t, 300, b, "zero values must fall back, not collapse the rail to 0")
	require.Equal(t, 150, p)

	b, p = resolveGoroutinePolicy(&config.Config{Health: config.HealthConfig{
		GoroutineBaseline:  500,
		GoroutinePerCamera: 250,
	}})
	require.Equal(t, 500, b)
	require.Equal(t, 250, p)
}
