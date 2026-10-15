package peer

import (
	"net"
)

// pathologicalDirectPair reports whether the nominated pair described by a
// stats snapshot is a pathological direct pair that should be evicted to
// the relay (#82). The observed artifact: a remote candidate that is prflx
// (never signaled by the peer — learned from an inbound check) AND carries
// a private/unroutable address (docker bridge, RFC1918, CGNAT) — e.g. a
// sibling container's probe packet cross-wiring into our socket. The ICE
// checks pass on such a pair, but the data path is unusable (0 throughput,
// dead heartbeats), and without eviction the session rides it forever.
//
// The rule is deliberately conservative on behalf of genuine same-LAN
// prflx pairs: the round trip must also be terrible — a real LAN pair
// answers in single-digit milliseconds, the artifact measured 360ms+.
func pathologicalDirectPair(snap StatsSnapshot) bool {
	if snap.ICERemoteCandidateType != "prflx" {
		return false
	}
	ip := net.ParseIP(snap.ICERemoteCandidateAddress)
	if ip == nil || advertiseableIP(ip) {
		return false
	}
	return snap.ICERTTSeconds == 0 || snap.ICERTTSeconds > 0.1
}

// checkPathEviction switches the session onto the relay transport when the
// live stats reveal a pathological nominated pair. Once per session; no-op
// when no relay is available or the session is already on relay. The
// adoption re-fires the DataChannel callbacks, replacing the dead direct
// channel with the relay one.
func (p *p2pSession) checkPathEviction(snap StatsSnapshot) {
	p.mu.Lock()
	rc := p.relayClient
	relayActive := p.relayActive
	sig := p.watchSignal
	cfg := p.watchCfg
	p.mu.Unlock()
	if rc == nil || relayActive || sig == nil {
		return
	}
	if !pathologicalDirectPair(snap) {
		return
	}
	p.evictOnce.Do(func() {
		p.log().Warn("pathological direct pair evicted to relay",
			"session_id", p.sessionID,
			"remote_cand", snap.ICERemoteCandidateType,
			"remote_addr", snap.ICERemoteCandidateAddress,
			"ice_rtt_ms", int(snap.ICERTTSeconds*1000))
		p.adoptRelayTransport(sig, cfg, rc)
	})
}
