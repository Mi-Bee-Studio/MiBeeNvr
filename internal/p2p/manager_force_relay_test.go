package p2p

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
)

func newForceRelayManager(t *testing.T, fr config.P2PForceRelay) *Manager {
	t.Helper()
	return New(config.P2PConfig{ForceRelay: fr}, "127.0.0.1:9090",
		filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
}

// TestTransportDecision pins the arbitration-pin matrix (#18): explicit
// true always pins; auto pins only on a v4 symmetric verdict WITHOUT
// global IPv6 (the dual-stack exemption); default never pins — byte-for-byte
// the historical direct-first behavior.
func TestTransportDecision(t *testing.T) {
	obs := func(class peer.NATClass, dual bool) *peer.NatObservation {
		return &peer.NatObservation{Class: class, DualStack: dual}
	}

	cases := []struct {
		name   string
		cfg    config.P2PForceRelay
		obs    *peer.NatObservation
		pinned bool
		reason string
	}{
		{"default stays direct even symmetric", config.ForceRelayDefault, obs(peer.NATSymmetric, false), false, "default"},
		{"default no obs", config.ForceRelayDefault, nil, false, "default"},
		{"true pins without obs", config.ForceRelayAlways, nil, true, "force_relay"},
		{"true pins over cone", config.ForceRelayAlways, obs(peer.NATCone, false), true, "force_relay"},
		{"auto no obs stays direct", config.ForceRelayAuto, nil, false, "default"},
		{"auto symmetric v4 pins", config.ForceRelayAuto, obs(peer.NATSymmetric, false), true, "nat_symmetric"},
		{"auto symmetric dual-stack exempt", config.ForceRelayAuto, obs(peer.NATSymmetric, true), false, "default"},
		{"auto cone stays direct", config.ForceRelayAuto, obs(peer.NATCone, false), false, "default"},
		{"auto unknown stays direct", config.ForceRelayAuto, obs(peer.NATUnknown, false), false, "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newForceRelayManager(t, c.cfg)
			if c.obs != nil {
				m.natObs.Store(c.obs)
			}
			pinned, reason := m.transportDecision()
			if pinned != c.pinned || reason != c.reason {
				t.Fatalf("decision = (%v,%q), want (%v,%q)", pinned, reason, c.pinned, c.reason)
			}
			snap := m.Status()
			wantDecision := "direct"
			if c.pinned {
				wantDecision = "relay"
			}
			if snap.TransportDecision != wantDecision || snap.DecisionReason != c.reason {
				t.Fatalf("status = (%q,%q), want (%q,%q)",
					snap.TransportDecision, snap.DecisionReason, wantDecision, c.reason)
			}
		})
	}
}

// TestNATSnapshotDualStack：观测视图携带 dual_stack（解释「为什么这台
// 设备没被 auto 钉 relay」）。
func TestNATSnapshotDualStack(t *testing.T) {
	m := newForceRelayManager(t, config.ForceRelayAuto)
	if got := m.natSnapshot(); got != nil {
		t.Fatalf("nil observation must snapshot nil, got %+v", got)
	}
	m.onNATObservation(peer.NatObservation{Class: peer.NATSymmetric, DualStack: true})
	got := m.natSnapshot()
	if got == nil || !got.DualStack || !got.PreferRelay {
		t.Fatalf("dual-stack symmetric snapshot = %+v", got)
	}
}

// TestTunnelEchoStatusFields：T2 探测观测进 Status（#87 设备侧出口）。
func TestTunnelEchoStatusFields(t *testing.T) {
	m := newForceRelayManager(t, config.ForceRelayDefault)
	if got := m.tunnelEchoSnapshot(); got != nil {
		t.Fatalf("no probe yet must snapshot nil, got %+v", got)
	}
	m.echoObs.Store(&TunnelEchoObs{NormalMS: 42, PrioMS: 9, LaneUsed: true, WantBytes: 16384})
	snap := m.Status()
	if snap.TunnelEcho == nil || snap.TunnelEcho.NormalMS != 42 || snap.TunnelEcho.PrioMS != 9 ||
		!snap.TunnelEcho.LaneUsed || snap.TunnelEcho.WantBytes != 16384 {
		t.Fatalf("tunnel echo snapshot = %+v", snap.TunnelEcho)
	}
}
