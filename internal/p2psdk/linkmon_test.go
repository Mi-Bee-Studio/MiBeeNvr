package peer

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLinkDegradedWindowLogic pins the per-window evaluation (#134⑤): a
// fresh app-fed loss sample is the primary signal (a healthy one
// short-circuits — RTT spikes under queuing are not loss), and without a
// fresh sample the ICE RTT fallback applies.
func TestLinkDegradedWindowLogic(t *testing.T) {
	cfg := &linkMonCfg{lossPct: 20, rttMs: 400}
	badRTTSnap := StatsSnapshot{ICERTTSeconds: 0.5}   // 500ms > 400ms
	goodRTTSnap := StatsSnapshot{ICERTTSeconds: 0.02} // 20ms

	t.Run("fresh loss sample above threshold degrades", func(t *testing.T) {
		p := &p2pSession{}
		p.linkLoss.Store(&linkQuality{lossPct: 30, at: time.Now()})
		degraded, cause := p.linkDegraded(cfg, goodRTTSnap)
		assert.True(t, degraded)
		assert.Equal(t, "loss", cause)
	})

	t.Run("fresh healthy loss short-circuits even with bad RTT", func(t *testing.T) {
		p := &p2pSession{}
		p.linkLoss.Store(&linkQuality{lossPct: 5, at: time.Now()})
		degraded, cause := p.linkDegraded(cfg, badRTTSnap)
		assert.False(t, degraded)
		assert.Empty(t, cause)
	})

	t.Run("stale loss sample falls back to RTT", func(t *testing.T) {
		p := &p2pSession{}
		p.linkLoss.Store(&linkQuality{lossPct: 90, at: time.Now().Add(-2 * linkLossFreshnessWindow)})
		degraded, cause := p.linkDegraded(cfg, badRTTSnap)
		assert.True(t, degraded)
		assert.Equal(t, "rtt", cause)
	})

	t.Run("no sample + healthy RTT stays calm", func(t *testing.T) {
		p := &p2pSession{}
		degraded, cause := p.linkDegraded(cfg, goodRTTSnap)
		assert.False(t, degraded)
		assert.Empty(t, cause)
	})

	t.Run("zero RTT (no nominated pair) never degrades", func(t *testing.T) {
		p := &p2pSession{}
		degraded, cause := p.linkDegraded(cfg, StatsSnapshot{})
		assert.False(t, degraded)
		assert.Empty(t, cause)
	})
}

// linkMonTestEnv pins the monitor knobs (identical in every test that uses
// t.Setenv — linkMonConfig parses once per process, so the FIRST call
// freezes the configuration for the whole test binary; keeping the values
// uniform keeps every test deterministic).
func linkMonTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MIBEE_LINKMON_LOSS_PCT", "20")
	t.Setenv("MIBEE_LINKMON_WINDOWS", "2")
	t.Setenv("MIBEE_LINKMON_RTT_MS", "400")
	t.Setenv("MIBEE_LINKMON_COOLDOWN_MS", "30000")
	t.Setenv("MIBEE_LINKMON_MAX_ESCALATIONS", "2")
}

// TestCheckLinkQualityEscalation: two consecutive degraded windows fire
// OnLinkDegraded exactly once and arm the offerer restart (attempted —
// failure-accounted, the fixture has no PC so the round errors, which is
// the same semantics as the failure ladder). A single degraded window and
// cooldown-blocked repeats must not re-escalate.
func TestCheckLinkQualityEscalation(t *testing.T) {
	linkMonTestEnv(t)
	s := &Session{
		logger:  slog.Default(),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{},
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	p := &p2pSession{
		sessionID: "linkmon-1",
		owner:     s,
		isOfferer: true,
	}
	s.peers[p.sessionID] = p

	events := make(chan string, 8)
	s.OnLinkDegraded = func(sessionID, cause string) { events <- sessionID + "/" + cause }

	lossy := func() StatsSnapshot {
		p.linkLoss.Store(&linkQuality{lossPct: 35, at: time.Now()})
		return StatsSnapshot{ICERTTSeconds: 0.02}
	}

	// Window 1: degraded but below the escalation threshold.
	p.checkLinkQuality(lossy())
	require.Equal(t, 1, p.linkStreak)
	select {
	case e := <-events:
		t.Fatalf("single degraded window must not escalate, got %s", e)
	default:
	}

	// Window 2: threshold reached — one event, restart attempt armed.
	p.checkLinkQuality(lossy())
	select {
	case e := <-events:
		assert.Equal(t, "linkmon-1/loss", e)
	case <-time.After(2 * time.Second):
		t.Fatal("two degraded windows must escalate")
	}
	require.Equal(t, 0, p.linkStreak, "streak resets on escalation")
	require.Equal(t, 1, p.linkEscalations)
	require.False(t, p.linkLastEscalation.IsZero())

	// Windows 3+4: still degraded — cooldown blocks the second escalation.
	p.checkLinkQuality(lossy())
	p.checkLinkQuality(lossy())
	select {
	case e := <-events:
		t.Fatalf("cooldown must block the second escalation, got %s", e)
	default:
	}
	assert.Equal(t, 1, p.linkEscalations)
}

// TestCheckLinkQualityOffererOnlyRestart pins the anti-glare discipline:
// an answerer escalates the EVENT but never attempts a restart (attemptICERestart
// is offerer-gated; with no signal sender this test would panic if the gate
// were missing).
func TestCheckLinkQualityOffererOnlyRestart(t *testing.T) {
	linkMonTestEnv(t)
	s := &Session{
		logger:  slog.Default(),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{},
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	p := &p2pSession{sessionID: "linkmon-2", owner: s, isOfferer: false}
	s.peers[p.sessionID] = p

	events := make(chan string, 4)
	s.OnLinkDegraded = func(sessionID, cause string) { events <- cause }

	for range 2 {
		p.linkLoss.Store(&linkQuality{lossPct: 50, at: time.Now()})
		p.checkLinkQuality(StatsSnapshot{})
	}
	select {
	case c := <-events:
		assert.Equal(t, "loss", c)
	case <-time.After(2 * time.Second):
		t.Fatal("answerer must still surface the degradation event")
	}
}

// TestReportLinkQualityClamp pins the public feed API: clamping to
// [0,100] and unknown-session erroring.
func TestReportLinkQualityClamp(t *testing.T) {
	s := &Session{
		logger:  slog.Default(),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{},
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	p := &p2pSession{sessionID: "clamp-1", owner: s}
	s.peers[p.sessionID] = p

	require.NoError(t, s.ReportLinkQuality("clamp-1", 150))
	assert.InDelta(t, 100.0, p.linkLoss.Load().lossPct, 0.001)

	require.NoError(t, s.ReportLinkQuality("clamp-1", -5))
	assert.InDelta(t, 0.0, p.linkLoss.Load().lossPct, 0.001)

	err := s.ReportLinkQuality("ghost", 10)
	require.ErrorIs(t, err, ErrSessionNotFound)
}
