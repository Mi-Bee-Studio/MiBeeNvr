// SPDX-License-Identifier: MIT
//
// Quality auto-fallback/restore state machine tests (issue #502):
//   - defect A: no-media failures separated by stable-streaming windows must
//     NOT accumulate to a downgrade
//   - defect B: a stable SD connection earns a bounded HD probe; the probe
//     budget prevents downgrade/upgrade oscillation
//   - quality transitions land in the health event stream

package xiaomi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// fakeHealthDB captures InsertHealthEvent calls.
type fakeHealthDB struct {
	events []model.HealthEvent
	err    error
}

func (f *fakeHealthDB) InsertHealthEvent(_ context.Context, e model.HealthEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func makeQualityTestRecorder(t *testing.T) *XiaomiRecorder {
	t.Helper()
	r := makeTestRecorder(t)
	// Shrink the windows so tests reason about time explicitly, not real minutes.
	r.stableResetWindow = 5 * time.Minute
	r.upgradeStableWindow = 10 * time.Minute
	r.maxUpgradeAttempts = 2
	r.currentQuality = "hd"
	return r
}

func noMediaErr() error {
	return fmt.Errorf("miss read: cs2: no media data for %v", 15*time.Second)
}

// --- defect A: counter semantics ---

func TestQualityFailuresSeparatedByStableWindowsNeverDowngrade(t *testing.T) {
	t.Helper()
	r := makeQualityTestRecorder(t)

	// Three no-media failures, each preceded by a connection that streamed
	// past the stable window — every failure starts a fresh sequence (count 1).
	for i := range 3 {
		r.handleQualityFailure(noMediaErr(), true)
		require.Equal(t, 1, r.noMediaFailCount, "iteration %d: counter should reset to 0 then count 1", i)
	}
	require.Equal(t, "hd", r.currentQuality, "stable-separated failures must not downgrade")
}

func TestQualityConsecutiveRapidFailuresDowngrade(t *testing.T) {
	t.Helper()
	r := makeQualityTestRecorder(t)
	hdb := &fakeHealthDB{}
	r.cfg.HealthDB = hdb

	// Three rapid no-media failures (no stable window between them).
	r.handleQualityFailure(noMediaErr(), false)
	r.handleQualityFailure(noMediaErr(), false)
	require.Equal(t, "hd", r.currentQuality)
	r.handleQualityFailure(noMediaErr(), false)

	require.Equal(t, "sd", r.currentQuality)
	require.Equal(t, 0, r.noMediaFailCount, "counter resets after the downgrade")

	require.Len(t, hdb.events, 1)
	evt := hdb.events[0]
	require.Equal(t, string(model.HealthEventQualityChanged), evt.EventType)
	require.Equal(t, string(model.HealthStatusWarning), evt.Status)
	require.Contains(t, evt.Message, "hd→sd")

	var meta map[string]string
	require.NoError(t, json.Unmarshal([]byte(evt.Metadata), &meta))
	require.Equal(t, "hd", meta["from"])
	require.Equal(t, "sd", meta["to"])
}

func TestQualityDowngradeOnlyInAutoMode(t *testing.T) {
	t.Helper()
	r := makeQualityTestRecorder(t)
	r.cfg.Quality = "hd" // pinned — auto fallback disarmed

	for range 5 {
		r.handleQualityFailure(noMediaErr(), false)
	}
	require.Equal(t, "hd", r.currentQuality, "pinned hd must not auto-downgrade")
}

func TestQualityNonNoMediaFailuresDoNotAccumulate(t *testing.T) {
	t.Helper()
	r := makeQualityTestRecorder(t)

	for range 5 {
		r.handleQualityFailure(errors.New("cs2: EOF"), false)
	}
	require.Equal(t, 0, r.noMediaFailCount, "EOF failures are not no-media failures")
	require.Equal(t, "hd", r.currentQuality)
}

func TestQualityStableNonNoMediaFailureResetsCounter(t *testing.T) {
	t.Helper()
	r := makeQualityTestRecorder(t)

	r.handleQualityFailure(noMediaErr(), false) // count 1
	require.Equal(t, 1, r.noMediaFailCount)
	// A long-lived connection dying of EOF resets the sequence.
	r.handleQualityFailure(errors.New("cs2: EOF"), true)
	require.Equal(t, 0, r.noMediaFailCount)
}

// --- defect B: SD→HD recovery probe ---

func TestShouldProbeUpgrade(t *testing.T) {
	t.Helper()
	r := makeQualityTestRecorder(t)
	start := time.Now()
	r.mediaStart = start

	require.False(t, r.shouldProbeUpgrade(start.Add(r.upgradeStableWindow)),
		"hd quality never probes")

	r.currentQuality = "sd"
	require.False(t, r.shouldProbeUpgrade(start.Add(r.upgradeStableWindow-time.Second)),
		"window not yet reached")
	require.True(t, r.shouldProbeUpgrade(start.Add(r.upgradeStableWindow)),
		"stable SD past the window probes")

	r.cfg.Quality = "sd" // pinned — auto recovery disarmed
	require.False(t, r.shouldProbeUpgrade(start.Add(r.upgradeStableWindow)))
	r.cfg.Quality = ""

	r.upgradeAttempts = r.maxUpgradeAttempts
	require.False(t, r.shouldProbeUpgrade(start.Add(r.upgradeStableWindow)),
		"probe budget exhausted")

	r.upgradeAttempts = 0
	r.mediaStart = time.Time{}
	require.False(t, r.shouldProbeUpgrade(start.Add(r.upgradeStableWindow)),
		"never-connected recorder does not probe")
}

// The probe budget bounds the downgrade→upgrade cycle: after
// maxUpgradeAttempts failed probes the recorder stays at SD for the rest of
// its lifecycle instead of oscillating (the 2K PTZ camera's 131 SPS changes
// in one day show what oscillation costs).
func TestQualityUpgradeOscillationBounded(t *testing.T) {
	t.Helper()
	r := makeQualityTestRecorder(t)

	cycles := 0
	for {
		// Simulate the probe path: SD connection stable past the window.
		r.currentQuality = "sd"
		r.mediaStart = time.Now().Add(-r.upgradeStableWindow)
		if !r.shouldProbeUpgrade(time.Now()) {
			break
		}
		r.upgradeAttempts++ // run loop's probe branch consumes the budget
		r.currentQuality = "hd"

		// HD refuses again: three rapid no-media failures downgrade.
		r.mediaStart = time.Time{}
		for range 3 {
			r.handleQualityFailure(noMediaErr(), false)
		}
		require.Equal(t, "sd", r.currentQuality)
		cycles++
		require.Less(t, cycles, 10, "oscillation not bounded")
	}

	require.Equal(t, r.maxUpgradeAttempts, cycles)
	require.Equal(t, "sd", r.currentQuality, "after the budget is spent, SD sticks")
}

func TestQualityProbeSentinelIsPlannedNotFailure(t *testing.T) {
	t.Helper()
	require.True(t, errors.Is(errQualityUpgradeProbe, errQualityUpgradeProbe))
	// The sentinel must not match the no-media string check — the run loop
	// handles it before handleQualityFailure and must never count it.
	require.NotContains(t, errQualityUpgradeProbe.Error(), "no media data")
}

// --- health event stream ---

func TestRecordQualityChangeUpgradeIsHealthy(t *testing.T) {
	t.Helper()
	r := makeQualityTestRecorder(t)
	r.cfg.Model = "isa.camera.hlc8"
	hdb := &fakeHealthDB{}
	r.cfg.HealthDB = hdb

	r.recordQualityChange("sd", "hd", "stable SD streaming for 10m0s, probe attempt 1/2")

	require.Len(t, hdb.events, 1)
	evt := hdb.events[0]
	require.Equal(t, "test-cam", evt.CameraID)
	require.Equal(t, string(model.HealthStatusHealthy), evt.Status)
	require.Contains(t, evt.Message, "sd→hd")

	var meta map[string]string
	require.NoError(t, json.Unmarshal([]byte(evt.Metadata), &meta))
	require.Equal(t, "isa.camera.hlc8", meta["model"])
}

func TestRecordQualityChangeModelFallsBackToDID(t *testing.T) {
	t.Helper()
	r := makeQualityTestRecorder(t)
	hdb := &fakeHealthDB{}
	r.cfg.HealthDB = hdb

	r.recordQualityChange("hd", "sd", "3 consecutive no-media failures")

	var meta map[string]string
	require.NoError(t, json.Unmarshal([]byte(hdb.events[0].Metadata), &meta))
	require.Equal(t, "test-device", meta["model"], "empty model falls back to DID")
}

func TestRecordQualityChangeDBErrorDoesNotPanic(t *testing.T) {
	t.Helper()
	r := makeQualityTestRecorder(t)
	r.cfg.HealthDB = &fakeHealthDB{err: errors.New("db unavailable")}

	require.NotPanics(t, func() {
		r.recordQualityChange("hd", "sd", "3 consecutive no-media failures")
	})
}
