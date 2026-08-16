package sip

import (
	"log/slog"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// alarmLinkage implements alarm-triggered streaming (#355): when an alarm
// notification arrives for a channel that is not streaming, INVITE it and
// hold the stream for a configured duration, then BYE. Channels whose camera
// wants recording keep the stream (the alarm merely ensured liveness).
// Repeated alarms extend the hold.
//
// The decision seams (invite/bye/streaming/recording-wanted) are injectable
// so tests can drive the state machine without SIP plumbing.
type alarmLinkage struct {
	mu    sync.Mutex
	holds map[string]*time.Timer // channelID → pending BYE timer

	inviteFn        func(deviceID, channelID string) error
	byeFn           func(deviceID, channelID string) error
	streamingFn     func(channelID string) bool
	recordingWanted func(deviceID, channelID string) bool
}

func newAlarmLinkage(inviteFn, byeFn func(deviceID, channelID string) error,
	streamingFn func(channelID string) bool, recordingWanted func(deviceID, channelID string) bool,
) *alarmLinkage {
	return &alarmLinkage{
		holds:           make(map[string]*time.Timer),
		inviteFn:        inviteFn,
		byeFn:           byeFn,
		streamingFn:     streamingFn,
		recordingWanted: recordingWanted,
	}
}

// Trigger applies the linkage policy for one alarm. cfg nil/disabled = no-op.
func (a *alarmLinkage) Trigger(deviceID, channelID string, cfg *config.GB28181AlarmLinkageConfig) {
	if cfg == nil || !cfg.Enabled {
		return
	}
	if a.recordingWanted != nil && a.recordingWanted(deviceID, channelID) {
		// The camera's recorder owns the session lifecycle (record loop
		// re-INVITEs on its own); only ensure it is streaming now.
		if !a.streamingFor(channelID) {
			_ = a.invite(deviceID, channelID) // logged inside; nothing to unwind
		}
		return
	}
	if a.streamingFor(channelID) {
		a.extend(deviceID, channelID, cfg.AlarmLinkageDuration())
		return
	}
	if a.invite(deviceID, channelID) != nil {
		return
	}
	a.extend(deviceID, channelID, cfg.AlarmLinkageDuration())
}

func (a *alarmLinkage) streamingFor(channelID string) bool {
	if a.streamingFn == nil {
		return false
	}
	return a.streamingFn(channelID)
}

func (a *alarmLinkage) invite(deviceID, channelID string) error {
	if err := a.inviteFn(deviceID, channelID); err != nil {
		slog.Warn("gb28181: alarm linkage INVITE failed", "device", deviceID, "channel", channelID, "error", err)
		return err
	}
	slog.Info("gb28181: alarm linkage — INVITE", "device", deviceID, "channel", channelID)
	return nil
}

// extend (re)arms the hold timer; a recording-owned session never gets one.
func (a *alarmLinkage) extend(deviceID, channelID string, d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.holds[channelID]; ok {
		t.Stop()
	}
	a.holds[channelID] = time.AfterFunc(d, func() {
		a.mu.Lock()
		delete(a.holds, channelID)
		a.mu.Unlock()
		slog.Info("gb28181: alarm linkage hold elapsed — BYE", "device", deviceID, "channel", channelID)
		if a.byeFn != nil {
			if err := a.byeFn(deviceID, channelID); err != nil {
				slog.Warn("gb28181: alarm linkage BYE failed", "device", deviceID, "channel", channelID, "error", err)
			}
		}
	})
}

// Stop cancels all pending holds (server shutdown).
func (a *alarmLinkage) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.holds {
		t.Stop()
	}
	a.holds = make(map[string]*time.Timer)
}
