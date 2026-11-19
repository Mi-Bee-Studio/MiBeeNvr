package peer

// WebRTC peer set-up for the P2P SDK: STUN URL derivation, ICE tuning and
// renaming, SCTP hardening, the advertise-address override and the
// PeerConnection constructor plus its data-channel wrapper. Split out of
// p2p.go.

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

// stunURLs flattens the Welcome's ICE server URLs (STUN entries only).
func stunURLs(welcome *Welcome) []string {
	if welcome == nil {
		return nil
	}
	var urls []string
	for _, srv := range welcome.ICEServers {
		for _, u := range srv.URLs {
			if strings.HasPrefix(u, "stun:") {
				urls = append(urls, u)
			}
		}
	}
	return urls
}

// applyICETuning installs the optional ICE timer/MTU overrides from
// SignalingConfig onto the SettingEngine (must run before NewPeerConnection;
// pion ignores later changes). Zero ICETimeouts struct = untouched engine
// (byte-identical behavior to the pre-knob build); individual zero fields
// fall back to the pion/ice defaults recorded next to ICETimeouts.
func applyICETuning(se *webrtc.SettingEngine, cfg SignalingConfig) {
	if to := cfg.ICETimeouts; to != (ICETimeouts{}) {
		ka, disc, failed := to.KeepaliveInterval, to.DisconnectedTimeout, to.FailedTimeout
		if ka == 0 {
			ka = ICEDefaultKeepaliveInterval
		}
		if disc == 0 {
			disc = ICEDefaultDisconnectedTimeout
		}
		if failed == 0 {
			failed = ICEDefaultFailedTimeout
		}
		se.SetICETimeouts(disc, failed, ka)
	}
	if cfg.ICEReceiveMTU > 0 {
		se.SetReceiveMTU(uint(cfg.ICEReceiveMTU)) //nolint:gosec // G115: validated ≤65535 at config layer
	}
}

// sctpRTOMaxDefault caps the SCTP retransmission-timeout ceiling. pion's
// stock ceiling is RFC-4960-scale (seconds); on healthy low-RTT paths one
// spurious no-progress event then parks the sender for hundreds of
// milliseconds per occurrence (#109: the fleet 63.52→62.40 direction
// measured 17–23 such stalls per 3.3MB leg, 81→20 Mbps, while RTT/loss
// stayed flat and the reverse direction stayed clean). A 600ms ceiling
// bounds the wait and restored full throughput in the on-path bench
// matrix (6/6 legs, zero stalls); it stays above any realistic P2P RTT so
// it cannot push RTO below path RTT. MIBEE_SCTP_RTO_MAX_MS overrides the
// value; setting it to 0 restores stock pion behavior.
const sctpRTOMaxDefault = 600 * time.Millisecond

func envDurationMs(name string) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return -1
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return -1
	}
	return time.Duration(n) * time.Millisecond
}

func envUint32(name string) (uint32, bool) {
	v := os.Getenv(name)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || int64(n) > math.MaxUint32 {
		return 0, false
	}
	return uint32(n), true //nolint:gosec // G115: bounds checked above
}

// iceRenominationDefault: automatic renomination interval. #104 A/B
// (2026-10-25, mesh fleet 62.40↔63.52, netem delay 150ms/loss 20% on the
// nominated direct pair): renomination OFF pinned the session to the degraded
// pair (RTT p50 152ms, throughput 0, three failed cycles, recovery only via
// linktest's dead-streak rebuild); ON switched to the peer's other healthy
// host pair within one cycle (RTT 2.4ms, 60-86 Mbps, no session rebuild).
// Default ON at 3s (browser-scale re-evaluation); MIBEE_ICE_RENOMINATION_MS=0
// disables, N>0 overrides the interval.
const iceRenominationDefault = 3000 * time.Millisecond

// applyICERenomination installs automatic renomination
// (#104 task 2, draft-thatcher-ice-renomination): the controlling side
// re-evaluates candidate pairs and switches to a better one without a full
// ICE restart — quality-triggered path adaptation (the restart/abandon ladder
// stays as the failure-triggered backstop). Interop: controlling-side
// behavior only; the extra STUN attribute (0xC001) is ignored by
// RFC-compliant agents that don't know the draft, and controlled/non-pion
// peers just see ordinary nominations — no SDP negotiation involved.
func applyICERenomination(se *webrtc.SettingEngine, logger *slog.Logger) {
	d := envDurationMs("MIBEE_ICE_RENOMINATION_MS")
	switch {
	case d == 0: // explicit off
		return
	case d < 0: // unset/invalid → default
		d = iceRenominationDefault
	}
	// logger may be nil（newPeerConnection 的测试路径传 nil——对齐包内
	// 其它 nil-safe 日志约定）。
	if err := se.SetICERenomination(webrtc.WithRenominationInterval(d)); err != nil {
		if logger != nil {
			logger.Warn("ice renomination enable failed", errKey, err)
		}
	} else if logger != nil {
		logger.Info("ice automatic renomination enabled", intervalMsKey, d.Milliseconds())
	}
}

func applySCTPHardening(se *webrtc.SettingEngine) {
	if d := envDurationMs("MIBEE_SCTP_RTO_MAX_MS"); d >= 0 {
		se.SetSCTPRTOMax(d)
	} else {
		se.SetSCTPRTOMax(sctpRTOMaxDefault)
	}
	// Experimental knobs for fleet A/B benching (#109 matrix), unset by
	// default: MinCwnd / FastRtxWnd / CwndCAStep.
	if n, ok := envUint32("MIBEE_SCTP_MIN_CWND"); ok {
		se.SetSCTPMinCwnd(n)
	}
	if n, ok := envUint32("MIBEE_SCTP_FAST_RTX"); ok {
		se.SetSCTPFastRtxWnd(n)
	}
	if n, ok := envUint32("MIBEE_SCTP_CA_STEP"); ok {
		se.SetSCTPCwndCAStep(n)
	}
}

func newPeerConnection(cfg SignalingConfig, welcome *Welcome, logger *slog.Logger) (*webrtc.PeerConnection, error) {
	config := webrtc.Configuration{
		BundlePolicy:  webrtc.BundlePolicyMaxBundle,
		RTCPMuxPolicy: webrtc.RTCPMuxPolicyRequire,
	}

	if welcome != nil && len(welcome.ICEServers) > 0 {
		filtered := filterWelcomeICE(welcome, cfg.ICEProviderTags)
		iceServers := make([]webrtc.ICEServer, 0, len(filtered))
		// ICEProviderTags can pin the session to specific providers
		// (linktest A/B: force all relay attempts through one external
		// TURN). Unfiltered = every entry, ordering untouched.
		for _, srv := range filtered {
			is := webrtc.ICEServer{URLs: srv.URLs}
			if srv.Username != "" {
				is.Username = srv.Username
				is.Credential = srv.Credential
				is.CredentialType = webrtc.ICECredentialTypePassword
			}
			iceServers = append(iceServers, is)
		}
		config.ICEServers = iceServers
	}

	se := hardenedSettingEngine("")
	applyICETuning(&se, cfg)
	applyICERenomination(&se, logger)
	applySCTPHardening(&se)
	// "auto"/"auto:N" resolve per session build via the UPnP→STUN ladder
	// (autoendpoint.go); resolution failure degrades to "" (no advertisement,
	// stock ICE + relay fallback) rather than failing the connection. Ladder
	// execution and per-rung failures are logged (fresh runs only).
	res := resolveAdvertiseDetailed(cfg.AdvertiseAddr, stunURLs(welcome))
	logAdvertiseResolution(logger, res)
	if err := applyAdvertiseAddr(&se, res.addr); err != nil {
		return nil, err
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	return api.NewPeerConnection(config)
}

// applyAdvertiseAddr turns SignalingConfig.AdvertiseAddr into pion settings:
// a single-port ephemeral range pins every host-candidate socket to the
// advertised port, and NAT1To1 host mode replaces host addresses with the
// public IP. mDNS is disabled explicitly — its .local candidate advertising
// is mutually exclusive with NAT1To1 host mode.
func applyAdvertiseAddr(se *webrtc.SettingEngine, advertiseAddr string) error {
	if advertiseAddr == "" {
		return nil
	}
	host, portStr, err := net.SplitHostPort(advertiseAddr)
	if err != nil {
		return fmt.Errorf("advertise_addr %q: %w", advertiseAddr, err)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("advertise_addr %q: udp port must be 1-65535", advertiseAddr)
	}
	if pinErr := se.SetEphemeralUDPPortRange(uint16(port), uint16(port)); pinErr != nil {
		return fmt.Errorf("advertise_addr %q: pin port: %w", advertiseAddr, pinErr)
	}
	//nolint:staticcheck // SA1019: pion deprecates SetNAT1To1IPs in favor of
	// SetICEAddressRewriteRules, but the rewrite rules change mDNS/candidate
	// semantics; migrating is a deliberate behavior change, not a lint fix.
	se.SetNAT1To1IPs([]string{host}, webrtc.ICECandidateTypeHost)
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	return nil
}

// ── DataChannel wrapper ─────────────────────────────────────────────────────

func wrapDataChannel(dc *webrtc.DataChannel) DataChannel {
	var mu sync.Mutex
	var msgHandler func([]byte)
	var pending [][]byte

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		mu.Lock()
		h := msgHandler
		if h == nil {
			// The remote peer may send tunnel frames before the local
			// application registers a handler (e.g. tunnel.Serve() not yet
			// started). Buffer in arrival order and replay on registration.
			pending = append(pending, msg.Data)
			mu.Unlock()
			return
		}
		mu.Unlock()
		h(msg.Data)
	})

	return DataChannel{
		Label: dc.Label(),
		OnMessage: func(h func([]byte)) {
			mu.Lock()
			msgHandler = h
			buffered := pending
			pending = nil
			mu.Unlock()
			for _, data := range buffered {
				h(data)
			}
		},
		Send: func(data []byte) error {
			return dc.Send(data)
		},
		// Observable SCTP backlog arms the transport's strict-priority
		// egress scheduler (WebRTC-path HOL fix). pion maintains this per
		// DataChannel; nil consumers simply keep passthrough sends.
		BufferedAmount: dc.BufferedAmount,
		Close: func() error {
			return dc.Close()
		},
		OnClose: func(cb func()) {
			dc.OnClose(func() { cb() })
		},
	}
}

// ── sendRaw helpers on signalingClient ──────────────────────────────────────
