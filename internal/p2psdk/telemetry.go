// ICE path telemetry (research doc 07 §5.1): attribute the nominated ICE
// pair to the Welcome ICEServer entry that produced it, and report the
// selection + session totals to the server over the signaling WebSocket.
//
// Attribution works by matching the pion candidate stat's URL (the server
// URL that produced the srflx/relay candidate) against the Welcome's
// ice_servers entries: exact URL first, then host:port (transport params
// and case differences do not break the match). Unmatched URLs report
// provider "unknown" — never a guess.

package peer

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

// iceTagIndex maps the URLs of a Welcome's ICE entries to their provider
// tag. Nil-safe: a nil index attributes everything to "" (unknown).
//
// Attribution inputs, in order (research doc 07 §5.1):
//  1. candidate stat URL — exact, then host:port. pion/ice v4.4.7 does NOT
//     populate ICECandidateStats.URL (agent_stats.go leaves the field
//     commented out), so today this path only future-proofs the design.
//  2. relay candidate ADDRESS — a TURN allocation's relayed address carries
//     the TURN server's own IP, so resolving the Welcome's TURN hosts once
//     and matching candidate IPs attributes relay paths reliably. This is
//     the working path for TURN cost attribution.
//  3. srflx candidates report "" (unknown): the producing STUN server is
//     not observable through pion's stats today.
type iceTagIndex struct {
	exact    map[string]string
	hostPort map[string]string
	turnIPs  map[string]string
}

// turnResolveCache caches TURN host → IPs for relay-address attribution
// (process lifetime; TURN server IPs are effectively static per host).
var turnResolveCache sync.Map // host string -> []net.IP

// hostPortOfICEURL extracts "host:port" from a stun:/turn:/turns: URL,
// lowercased. Query params (transport=…) and scheme are dropped.
func hostPortOfICEURL(u string) string {
	s := u
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	// IPv6 literal form is "turn:[::1]:3478" — keep brackets; strip params.
	if i := strings.IndexAny(s, "?/"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// buildICETagIndex indexes a Welcome's ICE entries by URL.
func buildICETagIndex(welcome *Welcome) *iceTagIndex {
	if welcome == nil || len(welcome.ICEServers) == 0 {
		return nil
	}
	idx := &iceTagIndex{
		exact:    make(map[string]string, len(welcome.ICEServers)),
		hostPort: make(map[string]string, len(welcome.ICEServers)),
		turnIPs:  make(map[string]string),
	}
	for _, srv := range welcome.ICEServers {
		idx.indexServer(srv)
	}
	return idx
}

// indexServer adds one Welcome entry's URLs to the index.
func (idx *iceTagIndex) indexServer(srv ICEServer) {
	tag := srv.Tag
	if tag == "" {
		tag = "unknown"
	}
	for _, u := range srv.URLs {
		idx.indexURL(u, tag)
	}
}

// indexURL adds one URL to the index: exact form, host:port, and (for
// turn:/turns:) the resolved server IPs for relay-address attribution
// (best effort — resolution failure leaves those paths at "unknown").
func (idx *iceTagIndex) indexURL(rawURL, tag string) {
	u := strings.ToLower(strings.TrimSpace(rawURL))
	isTurn := strings.HasPrefix(u, "turn:") || strings.HasPrefix(u, "turns:")
	if _, dup := idx.exact[u]; !dup {
		idx.exact[u] = tag
	}
	hp := hostPortOfICEURL(u)
	if hp == "" {
		return
	}
	if _, dup := idx.hostPort[hp]; !dup {
		idx.hostPort[hp] = tag
	}
	if !isTurn {
		return
	}
	host := hp
	if i := strings.LastIndex(hp, ":"); i > 0 {
		host = host[:i]
	}
	for _, ip := range resolveTurnHost(host) {
		if _, dup := idx.turnIPs[ip.String()]; !dup {
			idx.turnIPs[ip.String()] = tag
		}
	}
}

// resolveTurnHost resolves a TURN hostname with a bounded timeout, using
// the process-wide cache. Bare IPs pass through without resolution.
func resolveTurnHost(host string) []net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}
	if cached, ok := turnResolveCache.Load(host); ok {
		if ips, okIPs := cached.([]net.IP); okIPs {
			return ips
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resolver := &net.Resolver{}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		turnResolveCache.Store(host, []net.IP(nil))
		return nil
	}
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.IP)
	}
	turnResolveCache.Store(host, out)
	return out
}

// providerFor attributes a selected local candidate to a provider tag.
// candidateURL is the (currently always-empty, see type comment) stat URL;
// candidateAddr is the candidate's address (relay: the TURN relayed
// address). Empty result means unknown.
func (idx *iceTagIndex) providerFor(candidateURL, candidateAddr string) string {
	if idx == nil {
		return ""
	}
	if tag := idx.providerForURL(candidateURL); tag != "" {
		return tag
	}
	return idx.providerForAddr(candidateAddr)
}

// providerForURL matches a candidate stat URL against the Welcome index
// (exact, then host:port).
func (idx *iceTagIndex) providerForURL(candidateURL string) string {
	if candidateURL == "" {
		return ""
	}
	u := strings.ToLower(strings.TrimSpace(candidateURL))
	if tag, ok := idx.exact[u]; ok {
		return tag
	}
	if hp := hostPortOfICEURL(u); hp != "" {
		if tag, ok := idx.hostPort[hp]; ok {
			return tag
		}
	}
	return ""
}

// providerForCandidate attributes a stats snapshot's selected local
// candidate. Address matching applies ONLY to relay candidates: a
// public-IP peer's srflx address equals its own IP, which can coincide
// with the TURN server's IP (e.g. a probe co-located with the server) and
// must not be attributed to the TURN provider.
func (idx *iceTagIndex) providerForCandidate(snap StatsSnapshot) string {
	url := snap.ICELocalCandidateURL
	addr := ""
	if snap.ICELocalCandidateType == "relay" {
		addr = snap.ICELocalCandidateAddress
	}
	return idx.providerFor(url, addr)
}

// providerForAddr matches a candidate address (host or host:port) against
// the resolved TURN server IPs — the working relay attribution path.
func (idx *iceTagIndex) providerForAddr(candidateAddr string) string {
	if candidateAddr == "" || len(idx.turnIPs) == 0 {
		return ""
	}
	host := candidateAddr
	if h, _, err := net.SplitHostPort(candidateAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	return idx.turnIPs[ip.String()]
}

// filterWelcomeICE narrows a Welcome's ICE entries to those tagged with one
// of tags (case-insensitive). Empty tag list = no filtering. Used by
// SignalingConfig.ICEProviderTags to pin sessions to a specific provider
// (linktest A/B: force all relay attempts through one external provider).
func filterWelcomeICE(welcome *Welcome, tags []string) []ICEServer {
	if welcome == nil || len(tags) == 0 || len(welcome.ICEServers) == 0 {
		if welcome == nil {
			return nil
		}
		return welcome.ICEServers
	}
	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[strings.ToLower(strings.TrimSpace(t))] = true
	}
	var out []ICEServer
	for _, srv := range welcome.ICEServers {
		if want[strings.ToLower(srv.Tag)] {
			out = append(out, srv)
		}
	}
	return out
}

// pairKeyOf identifies a nominated pair for change detection.
func pairKeyOf(snap StatsSnapshot) string {
	if snap.ICELocalCandidateType == "" {
		return ""
	}
	return snap.ICELocalCandidateType + "|" + snap.ICELocalCandidateAddress +
		"|" + snap.ICERemoteCandidateType + "|" + snap.ICERemoteCandidateAddress
}

// initTelemetryLocked arms per-session telemetry state. Caller holds p.mu
// (startAsOfferer / startAsAnswerer).
func (p *p2pSession) initTelemetryLocked(cfg SignalingConfig, welcome *Welcome) {
	p.telOn = !cfg.DisableTelemetry
	p.iceTags = buildICETagIndex(welcome)
	p.startedAt = time.Now()
}

// telemetryEnabled reports whether this session reports telemetry.
func (p *p2pSession) telemetryEnabled() bool {
	return p.telOn
}

// maybeSendIceSelected reports a nomination change over the signaling link
// (best effort — telemetry loss is acceptable by design). Caller: the stats
// collector tick.
func (p *p2pSession) maybeSendIceSelected(snap StatsSnapshot) {
	if !p.telemetryEnabled() {
		return
	}
	key := pairKeyOf(snap)
	if key == "" {
		return
	}
	p.telMu.Lock()
	if key == p.lastPairKey {
		p.lastSnap = snap
		p.telMu.Unlock()
		return
	}
	p.lastPairKey = key
	p.lastSnap = snap
	p.telMu.Unlock()

	p.sendTelemetry(TelemetryReport{
		Event:         "ice_selected",
		SessionID:     p.sessionID,
		LocalType:     snap.ICELocalCandidateType,
		RemoteType:    snap.ICERemoteCandidateType,
		RelayProtocol: snap.ICERelayProtocol,
		Provider:      p.iceTags.providerForCandidate(snap),
		RTTMS:         snap.ICERTTSeconds * 1000,
		AvailMbps:     snap.ICEAvailableBitrateBits / 1_000_000,
	})
}

// setTelemetryCloseReason records the teardown reason for the final
// session_stats report (teardownWithReason knows it; close() only sends).
func (p *p2pSession) setTelemetryCloseReason(reason string) {
	p.telMu.Lock()
	p.closeReason = reason
	p.telMu.Unlock()
}

// emitSessionStats sends the final session summary if the session ever
// reported a nominated pair or carried bytes. Caller: close().
func (p *p2pSession) emitSessionStats() {
	if !p.telemetryEnabled() {
		return
	}
	p.telMu.Lock()
	snap := p.lastSnap
	reported := p.lastPairKey != ""
	reason := p.closeReason
	startedAt := p.startedAt
	p.telMu.Unlock()
	if !reported && snap.DataChannelBytesSent == 0 && snap.DataChannelBytesReceived == 0 {
		return
	}
	var dur float64
	if !startedAt.IsZero() {
		dur = time.Since(startedAt).Seconds()
	}
	p.sendTelemetry(TelemetryReport{
		Event:       "session_stats",
		SessionID:   p.sessionID,
		LocalType:   snap.ICELocalCandidateType,
		Provider:    p.iceTags.providerForCandidate(snap),
		DurationSec: dur,
		BytesSent:   snap.DataChannelBytesSent,
		BytesRecv:   snap.DataChannelBytesReceived,
		CloseReason: reason,
	})
}

// sendTelemetry is a non-blocking best-effort send: telemetry must never
// perturb session lifecycle, so errors are swallowed at debug level.
func (p *p2pSession) sendTelemetry(report TelemetryReport) {
	if p.signal == nil {
		return
	}
	if err := p.signal.sendRaw(MessageTypeTelemetry, report); err != nil {
		p.log().Debug("telemetry send failed", errorKey, err)
	}
}
