// Package peer provides the MiBee P2P Go SDK.
//
// StatsCollector periodically calls pc.GetStats() and reports extracted ICE and
// DataChannel metrics via the OnStats callback. Its lifecycle is bound to the
// p2pSession — Start() when the PeerConnection is ready, Stop() on session close.
package peer

import (
	"log/slog"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// ipVersionOf classifies a candidate address literal: v6 contains ':'.
func ipVersionOf(addr string) string {
	switch {
	case addr == "":
		return ""
	case strings.Contains(addr, ":"):
		return "6"
	default:
		return "4"
	}
}

// StatsSnapshot contains ICE and DataChannel statistics extracted from a
// single GetStats() call.
type StatsSnapshot struct {
	// SessionID identifies the P2P session this snapshot belongs to (set by
	// the SDK before OnStats delivery; empty in hand-constructed snapshots).
	// A SignalingConfig.OnStats callback is shared by every concurrent
	// session of the signaling connection — without this key the consumer
	// cannot tell which client's transport a snapshot describes.
	SessionID string

	// ICERTTSeconds is the current round-trip time of the nominated ICE
	// candidate pair, in seconds. Zero when no nominated pair exists yet.
	ICERTTSeconds float64

	// ICEAvailableBitrateBits is the available outgoing bitrate for the
	// nominated ICE candidate pair, in bits per second. Zero when unknown.
	ICEAvailableBitrateBits float64

	// ICELocalCandidateType / ICERemoteCandidateType are the candidate types
	// of the nominated pair ("host", "srflx", "prflx", "relay"). A "relay"
	// type means the traffic traverses the TURN server instead of flowing
	// directly between the peers. Empty when no nominated pair exists yet.
	ICELocalCandidateType  string
	ICERemoteCandidateType string

	// ICELocalCandidateAddress / ICERemoteCandidateAddress are the IP
	// addresses of the nominated pair's candidates (empty when unavailable).
	// The local srflx address is this peer's public NAT mapping as observed
	// by the STUN/TURN server — useful for network diagnostics.
	ICELocalCandidateAddress  string
	ICERemoteCandidateAddress string

	// ICEIPVersion is "4" or "6" for the nominated pair's local address,
	// "" when no pair is nominated — makes IPv6 direct wins observable.
	ICEIPVersion string

	// ICERelayProtocol is the transport used toward the TURN server ("udp",
	// "tcp", "tls") when the local candidate of the nominated pair is a TURN
	// allocation. Empty otherwise.
	ICERelayProtocol string

	// DataChannelBytesSent is the total bytes sent over all DataChannels.
	DataChannelBytesSent uint64

	// DataChannelBytesReceived is the total bytes received over all DataChannels.
	DataChannelBytesReceived uint64

	// DataChannelMessagesSent is the total messages sent over all DataChannels.
	DataChannelMessagesSent uint64

	// DataChannelMessagesReceived is the total messages received over all DataChannels.
	DataChannelMessagesReceived uint64
}

// StatsCollector periodically calls pc.GetStats() and reports extracted
// values through the OnStats callback. Call Start() after the PeerConnection
// is established and Stop() from the session close path.
type StatsCollector struct {
	pc     *webrtc.PeerConnection
	logger *slog.Logger
	// nominatedLogged guards the one-time nominated-pair INFO log — the
	// address/IP-version of the winning pair is triage gold (v6 direct wins
	// must be visible in device journals without a dashboard).
	nominatedLogged bool
	stopCh          chan struct{}
	done            chan struct{}
	interval        time.Duration

	// OnStats is called with each collected snapshot. May be nil.
	OnStats func(StatsSnapshot)
}

// NewStatsCollector creates a StatsCollector for the given PeerConnection.
// The collector uses a 5-second polling interval by default.
func NewStatsCollector(pc *webrtc.PeerConnection, logger *slog.Logger) *StatsCollector {
	return &StatsCollector{
		pc:       pc,
		logger:   logger,
		stopCh:   make(chan struct{}),
		interval: 5 * time.Second,
	}
}

// Start launches a background goroutine that polls pc.GetStats() every
// interval. The goroutine exits when Stop() is called or stopCh is closed.
func (sc *StatsCollector) Start() {
	sc.done = make(chan struct{})
	go func() {
		defer close(sc.done)

		ticker := time.NewTicker(sc.interval)
		defer ticker.Stop()

		for {
			select {
			case <-sc.stopCh:
				return
			case <-ticker.C:
			}

			snapshot := sc.collect()
			if !sc.nominatedLogged && snapshot.ICEIPVersion != "" {
				sc.nominatedLogged = true
				sc.logger.Info("ICE nominated pair",
					"local", snapshot.ICELocalCandidateAddress,
					"remote", snapshot.ICERemoteCandidateAddress,
					"local_type", snapshot.ICELocalCandidateType,
					"ip_version", snapshot.ICEIPVersion)
			}
			if sc.OnStats != nil {
				sc.OnStats(snapshot)
			}
		}
	}()
}

// Stop signals the background goroutine to exit and waits for it to finish.
// Safe to call multiple times; subsequent calls are no-ops.
func (sc *StatsCollector) Stop() {
	if sc.done == nil {
		// Start was never called — nothing to stop.
		return
	}
	select {
	case <-sc.stopCh:
		// Already stopped.
		return
	default:
		close(sc.stopCh)
	}
	<-sc.done
}

// collect calls pc.GetStats() and extracts ICE and DataChannel metrics.
func (sc *StatsCollector) collect() StatsSnapshot {
	return snapshotFromReport(sc.pc.GetStats())
}

// snapshotFromReport extracts one StatsSnapshot from a stats report. Pure
// function so the nominated-pair extraction is unit-testable.
func snapshotFromReport(report webrtc.StatsReport) StatsSnapshot {
	var snap StatsSnapshot

	// First pass: index candidates by ID so the nominated pair's
	// Local/RemoteCandidateID can be resolved to candidate details.
	candidates := make(map[string]webrtc.ICECandidateStats, len(report))
	for _, stat := range report {
		if cs, ok := stat.(webrtc.ICECandidateStats); ok {
			candidates[cs.ID] = cs
		}
	}

	for _, stat := range report {
		switch s := stat.(type) {
		case webrtc.ICECandidatePairStats:
			if s.Nominated {
				snap.ICERTTSeconds = s.CurrentRoundTripTime
				snap.ICEAvailableBitrateBits = s.AvailableOutgoingBitrate
				if lc, ok := candidates[s.LocalCandidateID]; ok {
					snap.ICELocalCandidateType = lc.CandidateType.String()
					snap.ICELocalCandidateAddress = lc.IP
					snap.ICERelayProtocol = lc.RelayProtocol
				}
				if rc, ok := candidates[s.RemoteCandidateID]; ok {
					snap.ICERemoteCandidateType = rc.CandidateType.String()
					snap.ICERemoteCandidateAddress = rc.IP
				}
				snap.ICEIPVersion = ipVersionOf(snap.ICELocalCandidateAddress)
			}

		case webrtc.DataChannelStats:
			snap.DataChannelBytesSent += s.BytesSent
			snap.DataChannelBytesReceived += s.BytesReceived
			snap.DataChannelMessagesSent += uint64(s.MessagesSent)
			snap.DataChannelMessagesReceived += uint64(s.MessagesReceived)
		}
	}

	return snap
}
