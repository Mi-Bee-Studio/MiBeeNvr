package peer

import (
	"strconv"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// #132: TURN credentials minted at welcome carry an expiry (turn-rest
// username = "<expiry>:<product>:<account>:<device>:<rand>"). Sessions
// outliving that TTL lose their relay path at the next allocation
// refresh, and an ICE restart re-gathers with the same stale material —
// the relay fallback silently dies for long-lived sessions. The SDK
// schedules a mid-life refresh: it asks the server over the live signal
// link, applies the fresh list with SetConfiguration (subsequent
// gathering/restarts carry unexpired credentials), and — as the offerer —
// immediately re-gathers via ICE restart so a TURN-bound session re-mints
// its allocation while the old one is still valid. A brief re-gather once
// per credential lifetime trades a second of re-negotiation for an
// unbroken relay path.

// turnExpiryFromUsername parses the leading expiry timestamp of a
// turn-rest username ("<expiry>:<product>:<account>:<device>:<rand>").
// Zero return = the username carries no parsable expiry (static
// credentials never expire — nothing to schedule).
func turnExpiryFromUsername(username string) time.Time {
	head, _, _ := strings.Cut(username, ":")
	expiry, err := strconv.ParseInt(head, 10, 64)
	if err != nil || expiry <= 0 {
		return time.Time{}
	}
	return time.Unix(expiry, 0)
}

// earliestTURNExpiry scans an ICE list for the soonest TURN credential
// expiry. Zero = no expiring credential present.
func earliestTURNExpiry(servers []ICEServer) time.Time {
	var earliest time.Time
	for _, srv := range servers {
		exp := turnExpiryFromUsername(srv.Username)
		if exp.IsZero() {
			continue
		}
		if earliest.IsZero() || exp.Before(earliest) {
			earliest = exp
		}
	}
	return earliest
}

// iceRefreshHalfLife is the refresh delay for a credential expiring at
// `expiry`: half the remaining lifetime, floored at 30s so pathological
// clocks still yield a sane schedule. Zero = nothing to schedule.
func iceRefreshHalfLife(expiry, now time.Time) time.Duration {
	if !expiry.After(now) {
		return 0 // already expired or expiring now
	}
	d := expiry.Sub(now) / 2
	if d < 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// scheduleICERefresh arms the mid-life renewal timer from the current
// welcome ICE list. Called at connect and re-armed on every refresh.
// Static credentials (no parsable expiry) schedule nothing.
func (s *Session) scheduleICERefresh() {
	if s.welcome == nil {
		return
	}
	expiry := earliestTURNExpiry(s.welcome.ICEServers)
	if expiry.IsZero() {
		return
	}
	delay := iceRefreshHalfLife(expiry, time.Now())
	if delay <= 0 {
		return
	}
	s.mu.Lock()
	if s.iceRefreshTimer != nil {
		s.iceRefreshTimer.Stop()
	}
	s.iceRefreshTimer = time.AfterFunc(delay, func() {
		s.requestIceRefresh()
	})
	s.mu.Unlock()
	s.logger.Info("TURN credential refresh scheduled",
		expiresKey, time.Until(expiry).Round(time.Second).String(),
		refreshInKey, delay.Round(time.Second).String(),
	)
}

// requestIceRefresh asks the server for fresh scoped ICE material (#132).
func (s *Session) requestIceRefresh() {
	if err := s.signal.sendRaw(MessageTypeIceRefresh, IceRefresh{}); err != nil {
		s.logger.Warn("ice_refresh request failed", errorKey, err)
	}
}

// handleIceRefresh applies the server's freshly minted ICE list: every
// live peer connection gets SetConfiguration (subsequent gathering and
// restart offers carry the new credentials), the offerer immediately
// re-gathers via ICE restart so a live TURN allocation is re-minted, and
// the renewal timer re-arms from the new list's expiry.
func (s *Session) handleIceRefresh(msg *Message) {
	var rf IceRefresh
	if err := UnmarshalPayload(msg, &rf); err != nil {
		s.logger.Warn("bad ice_refresh", errorKey, err)
		return
	}
	if len(rf.ICEServers) == 0 {
		s.logger.Debug("ice_refresh carried no servers (provider disabled)")
		return
	}
	// Keep the session-level snapshot coherent (telemetry attribution
	// reads the welcome ICE list).
	s.mu.Lock()
	if s.welcome != nil {
		s.welcome.ICEServers = rf.ICEServers
	}
	peers := make([]*p2pSession, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.mu.Unlock()

	converted := webrtcICEServers(rf.ICEServers, s.cfg.ICEProviderTags)
	if len(converted) == 0 {
		return
	}
	cfg := webrtc.Configuration{ICEServers: converted}
	for _, p := range peers {
		p.mu.Lock()
		pc := p.pc
		isOfferer := p.isOfferer
		relayActive := p.relayActive
		p.mu.Unlock()
		if pc == nil {
			continue
		}
		if err := pc.SetConfiguration(cfg); err != nil {
			p.log().Warn("ice_refresh: SetConfiguration failed", errorKey, err)
			continue
		}
		if isOfferer && !relayActive {
			// Re-gather now: the old TURN allocation dies at its refresh
			// with the stale credential — better to re-mint while valid.
			go p.attemptICERestart()
		}
	}
	// Re-arm from the fresh list.
	s.scheduleICERefresh()
}

// webrtcICEServers converts protocol ICE entries to pion's form — the
// credential-bearing subset only. Factored from newPeerConnection so the
// refresh path converts identically to session setup.
func webrtcICEServers(servers []ICEServer, tags []string) []webrtc.ICEServer {
	w := &Welcome{ICEServers: servers}
	filtered := filterWelcomeICE(w, tags)
	out := make([]webrtc.ICEServer, 0, len(filtered))
	for _, srv := range filtered {
		is := webrtc.ICEServer{URLs: srv.URLs}
		if srv.Username != "" {
			is.Username = srv.Username
			is.Credential = srv.Credential
			is.CredentialType = webrtc.ICECredentialTypePassword
		}
		out = append(out, is)
	}
	return out
}
