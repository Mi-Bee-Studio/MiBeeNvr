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

	"github.com/stretchr/testify/require"
)

func TestGoroutineCheckStatus_ScalesWithCameras(t *testing.T) {
	t.Parallel()

	// Small deployment: the tripwire stays in the old fixed-1000 ballpark
	// (4 cameras → 900).
	status, _ := goroutineCheckStatus(899, 4)
	require.Equal(t, "ok", status)
	status, _ = goroutineCheckStatus(901, 4) // 300+150×4 = 900
	require.Equal(t, "error", status)

	// The production case that filed this: 16 cameras idle at ~1365 —
	// healthy under the old fixed 1000 it never was.
	status, msg := goroutineCheckStatus(1365, 16) // threshold 2700
	require.Equal(t, "ok", status, "16-camera steady state must not be flagged")
	require.NotContains(t, msg, "error")

	// A leak still trips it: 2× steady state and beyond.
	status, _ = goroutineCheckStatus(4200, 16)
	require.Equal(t, "error", status)

	// Large fleets: threshold keeps scaling (32 cams → 5100).
	status, _ = goroutineCheckStatus(5000, 32)
	require.Equal(t, "ok", status)
	status, _ = goroutineCheckStatus(5101, 32)
	require.Equal(t, "error", status)

	// Zero/unknown camera count degrades to the conservative base rail.
	status, _ = goroutineCheckStatus(301, 0)
	require.Equal(t, "error", status)
}
