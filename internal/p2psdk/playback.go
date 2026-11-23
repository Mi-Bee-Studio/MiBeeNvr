package peer

// Playback tunnel-only policy (#136, 2026-11-10 product decision).
//
// The product's public positioning is 数据本地自治不上云 (local data
// sovereignty, nothing on the cloud). Playback is part of that promise: the
// recorded video must flow end-to-end inside the user's own networks, so
// playback sessions may ONLY ride a direct P2P tunnel (host/srflx nominated
// pair). The WSS relay transport and TURN-relayed ICE pairs both put a
// third-party server in the data path and are therefore forbidden for
// playback — and the degradation must be EXPLICIT (OnPlaybackDegraded +
// session teardown), never a silent demotion.
//
// Live preview keeps the standard degradation chain (direct → TURN →
// relay) — the policy is purpose-scoped, not global. Enforcement is
// symmetric on both sides: the purpose is declared in connect_request
// (additive JSON — old peers ignore it) and each session enforces locally.
//
// Gray-release switch: SignalingConfig.PlaybackTunnelOnly (default false =
// byte-identical legacy behavior). Degradation reasons are stable strings
// so embedders can branch on them.

// SessionPurpose declares why a session exists (ConnectOptions.Purpose /
// connect_request purpose). Empty = default (live preview and everything
// else, full degradation chain).
type SessionPurpose string

// Session purpose constants (wire values).
const (
	// PurposeDefault covers every session without a special policy.
	PurposeDefault SessionPurpose = ""
	// PurposePlayback marks remote-recording playback sessions: tunnel-only
	// under PlaybackTunnelOnly (data-local-sovereignty policy, #136).
	PurposePlayback SessionPurpose = "playback"
)

// Playback degradation reasons carried by OnPlaybackDegraded. Stable wire/
// branch values.
const (
	// PlaybackDegradedRelay: the session adopted (or was about to adopt) the
	// WSS relay transport.
	PlaybackDegradedRelay = "path_relay_transport"
	// PlaybackDegradedTurn: the nominated ICE pair rides a TURN allocation.
	PlaybackDegradedTurn = "path_turn_pair"
	// PlaybackDegradedNoDirect: ICE never produced a direct path within the
	// transport timeout, so no policy-legal path exists.
	PlaybackDegradedNoDirect = "no_direct_path"
)

// playbackTeardownReason is the session_close reason for policy teardowns.
const playbackTeardownReason = "playback_tunnel_only"

// PathClass classifies the session's current media path: "relay" (WSS
// relay transport), "turn" (nominated pair rides a TURN allocation), or
// "direct" (host/srflx pair). Before the first stats tick the class is
// optimistic ("direct") — the first tick (≤5s) corrects it. Exposed for
// embedders that want path-aware UI on non-playback sessions too.
func (s *Session) PathClass(sessionID string) (string, error) {
	p := s.getP2P(sessionID)
	if p == nil {
		return "", ErrSessionNotFound
	}
	return p.pathClass(p.lastSnapSafe()), nil
}

// lastSnapSafe returns the most recent stats snapshot (zero value before
// the first tick).
func (p *p2pSession) lastSnapSafe() StatsSnapshot {
	p.telMu.Lock()
	defer p.telMu.Unlock()
	return p.lastSnap
}

// pathClass classifies from a snapshot + transport state.
func (p *p2pSession) pathClass(snap StatsSnapshot) string {
	if p.relayActiveSnapshot() {
		return "relay"
	}
	if snap.ICELocalCandidateType == "relay" || snap.ICERemoteCandidateType == "relay" {
		return "turn"
	}
	return "direct"
}

// playbackTunnelOnlyArmed reports whether this session is under the
// tunnel-only policy (playback purpose + product switch on).
func (p *p2pSession) playbackTunnelOnlyArmed() bool {
	return p.purpose == PurposePlayback && p.owner.sessionConfig().PlaybackTunnelOnly
}

// checkPlaybackPolicy enforces the tunnel-only policy off the stats tick:
// any TURN/relay path is a policy violation → explicit degradation
// (event + teardown). Direct passes untouched.
func (p *p2pSession) checkPlaybackPolicy(snap StatsSnapshot) {
	if !p.playbackTunnelOnlyArmed() {
		return
	}
	switch p.pathClass(snap) {
	case "relay":
		p.degradePlayback(PlaybackDegradedRelay)
	case "turn":
		p.degradePlayback(PlaybackDegradedTurn)
	}
}

// degradePlayback fires the one-shot degradation: OnPlaybackDegraded (UI
// notice semantics — the embedder shows "当前网络环境不支持远程回放" and
// offers no relay fallback) and tears the session down with reason
// playback_tunnel_only. Teardown runs async: this may be called from the
// stats-collector goroutine, whose Stop() a synchronous close() would
// join-deadlock (same discipline as the PC-closed watcher).
func (p *p2pSession) degradePlayback(reason string) {
	p.playbackDegradedOnce.Do(func() {
		p.log().Warn("playback tunnel-only policy violated — explicit degradation (#136)",
			reasonKey, reason, pathClassKey, p.pathClass(p.lastSnapSafe()))
		if cb := p.owner.playbackDegradedCallback(); cb != nil {
			cb(p.sessionID, reason)
		}
		go p.teardownWithReason(playbackTeardownReason)
	})
}
