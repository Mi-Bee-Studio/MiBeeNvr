package peer

import (
	"context"
	"net"
	"time"
)

// NAT mapping classes reported by NetCheck (RFC 4787 vocabulary, following
// the Tailscale netcheck convention of splitting on
// MappingVariesByDestIP rather than the older cone taxonomy).
const (
	// NetMappingEIM: every STUN server observed the same endpoint for the
	// probe socket — endpoint-independent mapping. Ordinary ICE hole
	// punching works with peers behind similar-or-easier NATs.
	NetMappingEIM = "eim"
	// NetMappingEDM: the observed endpoint varies by destination — the
	// learned srflx candidate is useless for other peers (symmetric-style
	// NAT). Relay (TURN/DERP) is the dependable path.
	NetMappingEDM = "edm"
	// NetMappingInconclusive: fewer than two servers answered, so the
	// comparison cannot be made.
	NetMappingInconclusive = "inconclusive"
)

// NetCheckReport is the outcome of a NAT behaviour probe (#106): ONE UDP
// socket interrogates several STUN servers and compares the ip:port each
// one observes for that same socket. Sharing the socket is what makes the
// comparison meaningful — per-destination differences then reflect the
// NAT's mapping behaviour instead of local port randomization.
type NetCheckReport struct {
	// MappedAddrs holds the latest observed endpoint per server, keyed by
	// the server address given to NetCheck. (Pointer fields lead the
	// struct to keep the GC-scan prefix tight.)
	MappedAddrs map[string]string
	// Mapping is one of NetMappingEIM / NetMappingEDM /
	// NetMappingInconclusive.
	Mapping string
	// Elapsed is the total probe wall time.
	Elapsed time.Duration
	// Responded / Queried: server counts for observability.
	Responded int
	Queried   int
	// UDPBlocked: no server answered at all. Either UDP egress is blocked
	// or every server was down; treat as "relay-only network" until a
	// re-probe proves otherwise.
	UDPBlocked bool
	// MappingChanged: at least one server observed a different endpoint on
	// the second round than on the first — the NAT mapping epoch flipped
	// during the probe (router restart / pool rotation). Consumers that
	// cache advertised endpoints should rebuild them.
	MappingChanged bool
}

// NetCheck probes servers from a single shared UDP socket in two rounds
// (the second round doubles as the mapping-stability check). timeout is
// per server per round; <=0 means 2s. The ctx cancels between probes.
func NetCheck(ctx context.Context, servers []string, timeout time.Duration) NetCheckReport {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	report := NetCheckReport{
		Mapping:     NetMappingInconclusive,
		MappedAddrs: map[string]string{},
		Queried:     len(servers),
	}
	start := time.Now()
	defer func() { report.Elapsed = time.Since(start) }()

	if len(servers) == 0 {
		return report
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		report.UDPBlocked = true
		return report
	}
	defer conn.Close() //nolint:errcheck // probe teardown

	rounds := make([]map[string]string, 2)
	for round := range rounds {
		rounds[round] = map[string]string{}
		for _, server := range servers {
			if ctx.Err() != nil {
				return classifyNetCheck(report, rounds)
			}
			mapped, queryErr := stunQueryOnConn(conn, server, timeout)
			if queryErr == nil {
				rounds[round][server] = mapped
				report.MappedAddrs[server] = mapped
			}
		}
	}
	return classifyNetCheck(report, rounds)
}

// NetCheckFromWelcome derives the STUN server list from the signal
// Welcome message (the same servers the platform handed out for ICE).
func NetCheckFromWelcome(ctx context.Context, welcome *Welcome, timeout time.Duration) NetCheckReport {
	return NetCheck(ctx, stunURLs(welcome), timeout)
}

func classifyNetCheck(report NetCheckReport, rounds []map[string]string) NetCheckReport {
	report.Responded = len(rounds[1]) + countOnlyFirstRound(rounds)
	for server, second := range rounds[1] {
		if first, ok := rounds[0][server]; ok && first != second {
			report.MappingChanged = true
		}
	}
	latest := rounds[1]
	if len(latest) == 0 {
		latest = rounds[0]
	}
	switch {
	case len(latest) == 0:
		report.UDPBlocked = true
	case len(latest) >= 2:
		var reference string
		report.Mapping = NetMappingEIM
		for _, mapped := range latest {
			if reference == "" {
				reference = mapped
				continue
			}
			if mapped != reference {
				report.Mapping = NetMappingEDM
			}
		}
	}
	return report
}

func countOnlyFirstRound(rounds []map[string]string) int {
	n := 0
	for server := range rounds[0] {
		if _, ok := rounds[1][server]; !ok {
			n++
		}
	}
	return n
}
