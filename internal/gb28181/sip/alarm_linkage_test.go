package sip

import (
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

type linkageCalls struct {
	mu        sync.Mutex
	invited   []string
	byes      []string
	streaming map[string]bool
	recording map[string]bool
}

func (c *linkageCalls) invite(deviceID, channelID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invited = append(c.invited, channelID)
	return nil
}

func (c *linkageCalls) bye(deviceID, channelID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byes = append(c.byes, channelID)
	return nil
}

func (c *linkageCalls) isStreaming(channelID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streaming[channelID]
}

func (c *linkageCalls) wanted(deviceID, channelID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recording[channelID]
}

func newTestLinkage() (*alarmLinkage, *linkageCalls) {
	c := &linkageCalls{streaming: map[string]bool{}, recording: map[string]bool{}}
	return newAlarmLinkage(c.invite, c.bye, c.isStreaming, c.wanted), c
}

func TestAlarmLinkage_DisabledNoop(t *testing.T) {
	a, c := newTestLinkage()
	a.Trigger("dev", "ch", nil)
	a.Trigger("dev", "ch", &config.GB28181AlarmLinkageConfig{Enabled: false})
	require.Empty(t, c.invited)
	require.Empty(t, c.byes)
}

func TestAlarmLinkage_InvitesAndHolds(t *testing.T) {
	a, c := newTestLinkage()
	a.Trigger("dev", "ch", &config.GB28181AlarmLinkageConfig{Enabled: true, Duration: "100ms"})
	require.Equal(t, []string{"ch"}, c.invited)
	require.Empty(t, c.byes, "hold must not elapse yet")

	// Simulate the INVITE establishing the stream, then wait out the hold.
	c.mu.Lock()
	c.streaming["ch"] = true
	c.mu.Unlock()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.byes) == 1
	}, 2*time.Second, 20*time.Millisecond, "BYE must fire after the hold elapses")
}

func TestAlarmLinkage_AlarmExtendsHold(t *testing.T) {
	a, c := newTestLinkage()
	cfg := &config.GB28181AlarmLinkageConfig{Enabled: true, Duration: "150ms"}
	a.Trigger("dev", "ch", cfg)
	c.mu.Lock()
	c.streaming["ch"] = true
	c.mu.Unlock()

	time.Sleep(80 * time.Millisecond)
	a.Trigger("dev", "ch", cfg)        // second alarm extends the hold
	time.Sleep(100 * time.Millisecond) // 180ms since the FIRST trigger
	c.mu.Lock()
	byes := len(c.byes)
	c.mu.Unlock()
	require.Zero(t, byes, "extended hold must not have fired yet")

	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.byes) == 1
	}, 2*time.Second, 20*time.Millisecond)
}

func TestAlarmLinkage_RecordingWantedNeverByes(t *testing.T) {
	a, c := newTestLinkage()
	c.mu.Lock()
	c.recording["ch"] = true
	c.mu.Unlock()

	a.Trigger("dev", "ch", &config.GB28181AlarmLinkageConfig{Enabled: true, Duration: "50ms"})
	require.Equal(t, []string{"ch"}, c.invited, "recording camera still gets liveness INVITE when not streaming")

	time.Sleep(120 * time.Millisecond)
	require.Empty(t, c.byes, "recording-owned sessions must never be alarm-BYE'd")
}

func TestAlarmLinkage_RecordingWantedStreamingNoInvite(t *testing.T) {
	a, c := newTestLinkage()
	c.mu.Lock()
	c.recording["ch"] = true
	c.streaming["ch"] = true
	c.mu.Unlock()

	a.Trigger("dev", "ch", &config.GB28181AlarmLinkageConfig{Enabled: true, Duration: "50ms"})
	require.Empty(t, c.invited, "already-streaming recording camera needs no INVITE")
	require.Empty(t, c.byes)
}

func TestAlarmLinkage_StreamingNonRecordingExtends(t *testing.T) {
	a, c := newTestLinkage()
	c.mu.Lock()
	c.streaming["ch"] = true // e.g. live-view session or a prior alarm hold
	c.mu.Unlock()

	a.Trigger("dev", "ch", &config.GB28181AlarmLinkageConfig{Enabled: true, Duration: "80ms"})
	require.Empty(t, c.invited, "already streaming: no duplicate INVITE")

	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.byes) == 1
	}, 2*time.Second, 20*time.Millisecond)
}

func TestGB28181AlarmLinkageDurationDefaults(t *testing.T) {
	require.Zero(t, (*config.GB28181AlarmLinkageConfig)(nil).AlarmLinkageDuration())
	require.Equal(t, 60*time.Second, (&config.GB28181AlarmLinkageConfig{Enabled: true}).AlarmLinkageDuration())
	require.Equal(t, 30*time.Second, (&config.GB28181AlarmLinkageConfig{Enabled: true, Duration: "30s"}).AlarmLinkageDuration())
	require.Equal(t, 60*time.Second, (&config.GB28181AlarmLinkageConfig{Duration: "bogus"}).AlarmLinkageDuration())
	require.Equal(t, 500*time.Millisecond, (&config.GB28181AlarmLinkageConfig{Duration: "500ms"}).AlarmLinkageDuration())
	require.Equal(t, 60*time.Second, (&config.GB28181AlarmLinkageConfig{Duration: "-5s"}).AlarmLinkageDuration(), "negative durations fall back")
}
