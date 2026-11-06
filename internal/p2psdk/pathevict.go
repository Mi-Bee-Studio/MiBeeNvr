package peer

import (
	"net"
)

// Pathological-pair thresholds (#82).
const (
	// prflxPrivateRTTLimit bounds a peer-reflexive PRIVATE remote pair. A
	// prflx candidate was never signaled by the peer — learned from an
	// inbound check — and a private one (docker bridge, RFC1918, CGNAT) on
	// the nominated pair is the cross-wiring artifact: genuine same-LAN
	// prflx pairs answer in single-digit milliseconds, the artifact
	// measured 360ms+.
	prflxPrivateRTTLimit = 0.1
	// hostPrivateRTTLimit bounds a SIGNALED private host pair. These occur
	// legitimately (same-LAN wiring, routed cross-subnet LANs, CGNAT-space
	// VPN overlays), so the bar is higher: the docker-bridge artifact
	// (sibling container's bridge address — checks squeak through, data
	// path dead) measured 468-751ms while working overlays sit well under
	// 300ms.
	hostPrivateRTTLimit = 0.3
	// pathologicalStreakMin is the number of consecutive pathological
	// samples before eviction (stats tick 5s → ~15s sustained). One flaky
	// sample on an otherwise healthy pair — a WiFi jitter spike crossing
	// the threshold — resets long before it ever counts.
	pathologicalStreakMin = 3
)

// pathologicalDirectPair reports whether the nominated pair described by a
// stats snapshot is a pathological direct pair that should be evicted to
// the relay (#82). Two shapes qualify, both with terrible round trips:
//
//   - prflx remote + private/unroutable address (a sibling container's
//     probe packet cross-wiring into our socket), or
//   - host remote + private address (a docker-bridge candidate signaled by
//     a container that is unreachable from outside its own bridge network).
//
// Everything else stays direct: public remotes (relay/srflx/public host),
// private remotes with healthy RTTs (the legit LAN/overlay cases), and
// remote types the rule does not model.
func pathologicalDirectPair(snap StatsSnapshot) bool {
	switch snap.ICERemoteCandidateType {
	case "prflx", "host":
	default:
		return false
	}
	ip := net.ParseIP(snap.ICERemoteCandidateAddress)
	if ip == nil || advertiseableIP(ip) {
		return false
	}
	limit := prflxPrivateRTTLimit
	if snap.ICERemoteCandidateType == "host" {
		limit = hostPrivateRTTLimit
	}
	return snap.ICERTTSeconds == 0 || snap.ICERTTSeconds > limit
}

// checkPathEviction switches the session onto the relay transport when the
// live stats reveal a pathological nominated pair, sustained for
// pathologicalStreakMin consecutive samples (#82). Once per session; no-op
// when no relay is available or the session is already on relay. The
// adoption re-fires the DataChannel callbacks, replacing the dead direct
// channel with the relay one.
func (p *p2pSession) checkPathEviction(snap StatsSnapshot) {
	p.mu.Lock()
	rc := p.relayClient
	relayActive := p.relayActive
	sig := p.watchSignal
	cfg := p.watchCfg
	if pathologicalDirectPair(snap) {
		p.evictStreak++
	} else {
		p.evictStreak = 0
	}
	streak := p.evictStreak
	p.mu.Unlock()
	if rc == nil || relayActive || sig == nil {
		return
	}
	if streak < pathologicalStreakMin {
		return
	}
	p.evictOnce.Do(func() {
		p.log().Warn("pathological direct pair evicted to relay",
			sessionIDKey, p.sessionID,
			remoteCandKey, snap.ICERemoteCandidateType,
			remoteAddrKey, snap.ICERemoteCandidateAddress,
			iceRttMsKey, int(snap.ICERTTSeconds*1000))
		p.adoptRelayTransport(sig, cfg, rc)
	})
}
