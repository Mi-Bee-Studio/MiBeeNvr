package peer

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTelemetrySender captures sendRaw calls (satisfies signalSender).
type fakeTelemetrySender struct {
	sent []Message
	mu   sync.Mutex
}

func (f *fakeTelemetrySender) sendRaw(msgType MessageType, payload any) error {
	data, err := Marshal(msgType, payload)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.sent = append(f.sent, Message{Type: msgType, Payload: json.RawMessage(data)})
	f.mu.Unlock()
	return nil
}

func (f *fakeTelemetrySender) reports(t *testing.T) []TelemetryReport {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]TelemetryReport, 0, len(f.sent))
	for _, m := range f.sent {
		require.Equal(t, MessageTypeTelemetry, m.Type)
		// The captured raw bytes are the full envelope; re-parse it.
		msg, err := ParseAndValidate(m.Payload)
		require.NoError(t, err)
		var tr TelemetryReport
		require.NoError(t, UnmarshalPayload(msg, &tr))
		out = append(out, tr)
	}
	return out
}

func telemetryTestWelcome() *Welcome {
	return &Welcome{ICEServers: []ICEServer{
		{URLs: []string{"stun:relay.example.com:3478"}, Tag: ICEServerTagSelf},
		{URLs: []string{"stun:stun.cloudflare.com:3478"}, Tag: ICEServerTagExtra},
		{
			URLs:     []string{"turn:relay.example.com:13478?transport=udp", "turns:relay.example.com:5349?transport=tcp"},
			Username: "1770000000:p:a:d:r", Credential: "h",
			Tag: ICEServerTagSelf,
		},
		{
			URLs:     []string{"turn:staticauth.openrelay.metered.ca:80"},
			Username: "1770000000:cafe", Credential: "h2",
			Tag: "openrelay",
		},
	}}
}

func TestBuildICETagIndexAttribution(t *testing.T) {
	idx := buildICETagIndex(telemetryTestWelcome())
	require.NotNil(t, idx)

	cases := []struct {
		url  string
		want string
	}{
		// Exact match.
		{"turn:staticauth.openrelay.metered.ca:80", "openrelay"},
		{"stun:relay.example.com:3478", ICEServerTagSelf},
		// Case / whitespace tolerance.
		{"TURN:StaticAuth.OpenRelay.Metered.CA:80", "openrelay"},
		// Transport-param variance falls back to host:port.
		{"turn:relay.example.com:13478?transport=tcp", ICEServerTagSelf},
		{"stun:stun.cloudflare.com:3478?foo=1", ICEServerTagExtra},
		// Unknown server → empty (never a guess).
		{"turn:unknown.example.net:3478", ""},
		// Empty URL → empty.
		{"", ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, idx.providerFor(tc.url, ""), "url=%q", tc.url)
	}

	// Nil index / nil welcome are inert.
	var nilIdx *iceTagIndex
	assert.Empty(t, nilIdx.providerFor("turn:x:1", "8.8.8.8:9999"))
	assert.Nil(t, buildICETagIndex(nil))
}

func TestBuildICETagIndexUntaggedServerReportsUnknown(t *testing.T) {
	idx := buildICETagIndex(&Welcome{ICEServers: []ICEServer{
		{URLs: []string{"stun:old.example.com:3478"}}, // pre-tag server
	}})
	assert.Equal(t, "unknown", idx.providerFor("stun:old.example.com:3478", ""))
}

func TestFilterWelcomeICE(t *testing.T) {
	w := telemetryTestWelcome()

	// Empty filter → untouched.
	assert.Len(t, filterWelcomeICE(w, nil), 4)
	assert.Len(t, filterWelcomeICE(w, []string{}), 4)

	// Pin to one external provider (linktest A/B).
	got := filterWelcomeICE(w, []string{"openrelay"})
	require.Len(t, got, 1)
	assert.Equal(t, "openrelay", got[0].Tag)

	// Case-insensitive; multiple tags ("self" matches BOTH self entries:
	// the STUN and the TURN — tags are per-entry).
	assert.Len(t, filterWelcomeICE(w, []string{"SELF", "extra"}), 3)

	// Unknown tag → nothing survives.
	assert.Empty(t, filterWelcomeICE(w, []string{"nope"}))

	// nil welcome safe.
	assert.Nil(t, filterWelcomeICE(nil, []string{"self"}))
}

func newTelemetrySession(fake *fakeTelemetrySender) *p2pSession {
	p := &p2pSession{
		sessionID: "sess-1",
		signal:    fake,
		telOn:     true,
		iceTags:   buildICETagIndex(telemetryTestWelcome()),
		startedAt: time.Now().Add(-time.Minute),
	}
	return p
}

func TestMaybeSendIceSelectedOnChangeOnly(t *testing.T) {
	fake := &fakeTelemetrySender{}
	p := newTelemetrySession(fake)

	relaySnap := StatsSnapshot{
		ICELocalCandidateType:   "relay",
		ICERemoteCandidateType:  "srflx",
		ICELocalCandidateURL:    "turn:staticauth.openrelay.metered.ca:80",
		ICERelayProtocol:        "tcp",
		ICERTTSeconds:           0.0875,
		ICEAvailableBitrateBits: 4_200_000,
		DataChannelBytesSent:    100,
	}
	// No nominated pair yet → no report.
	p.maybeSendIceSelected(StatsSnapshot{})
	assert.Empty(t, fake.reports(t))

	p.maybeSendIceSelected(relaySnap)
	p.maybeSendIceSelected(relaySnap) // same pair → suppressed
	p.maybeSendIceSelected(relaySnap)
	reports := fake.reports(t)
	require.Len(t, reports, 1, "同一 pair 只报一次")

	r := reports[0]
	assert.Equal(t, "ice_selected", r.Event)
	assert.Equal(t, "sess-1", r.SessionID)
	assert.Equal(t, "relay", r.LocalType)
	assert.Equal(t, "srflx", r.RemoteType)
	assert.Equal(t, "tcp", r.RelayProtocol)
	assert.Equal(t, "openrelay", r.Provider, "归因 = Welcome tag")
	assert.InDelta(t, 87.5, r.RTTMS, 0.001)
	assert.InDelta(t, 4.2, r.AvailMbps, 0.001)

	// Pair change → second report. The pair key is types+addresses (a
	// URL-only change with identical addresses is the same path, so change
	// the remote side too — as a real TURN switch would).
	relaySnap.ICELocalCandidateURL = "turn:relay.example.com:13478?transport=udp"
	relaySnap.ICERemoteCandidateAddress = "203.0.113.9"
	p.maybeSendIceSelected(relaySnap)
	reports = fake.reports(t)
	require.Len(t, reports, 2)
	assert.Equal(t, ICEServerTagSelf, reports[1].Provider)
}

func TestMaybeSendIceSelectedDisabled(t *testing.T) {
	fake := &fakeTelemetrySender{}
	p := newTelemetrySession(fake)
	p.telOn = false
	p.maybeSendIceSelected(StatsSnapshot{
		ICELocalCandidateType: "host", ICEIPVersion: "4",
	})
	assert.Empty(t, fake.reports(t), "DisableTelemetry 关闭上报")
}

func TestCloseEmitsSessionStats(t *testing.T) {
	fake := &fakeTelemetrySender{}
	p := newTelemetrySession(fake)

	// Simulate an observed session: nomination + bytes + teardown reason.
	snap := StatsSnapshot{
		ICELocalCandidateType:    "relay",
		ICELocalCandidateURL:     "turn:staticauth.openrelay.metered.ca:80",
		DataChannelBytesSent:     1_000,
		DataChannelBytesReceived: 3_000,
	}
	p.maybeSendIceSelected(snap)
	p.setTelemetryCloseReason("ice_restart_failed")
	require.NoError(t, p.close())

	reports := fake.reports(t)
	require.Len(t, reports, 2, "ice_selected + session_stats")
	final := reports[1]
	assert.Equal(t, "session_stats", final.Event)
	assert.Equal(t, "openrelay", final.Provider)
	assert.Equal(t, uint64(1000), final.BytesSent)
	assert.Equal(t, uint64(3000), final.BytesRecv)
	assert.Equal(t, "ice_restart_failed", final.CloseReason)
	assert.Greater(t, final.DurationSec, float64(0))
}

func TestCloseWithoutTrafficEmitsNothing(t *testing.T) {
	fake := &fakeTelemetrySender{}
	p := newTelemetrySession(fake)
	require.NoError(t, p.close())
	assert.Empty(t, fake.reports(t), "从未提名/无流量的会话不发 session_stats")
}

func TestSnapshotFromReportExtractsCandidateURL(t *testing.T) {
	// End-to-end attribution chain at pure-function level: stats report →
	// snapshot URL extraction → Welcome tag attribution.
	report := webrtc.StatsReport{
		"lc": webrtc.ICECandidateStats{
			ID: "lc", CandidateType: webrtc.ICECandidateTypeRelay,
			IP: "10.0.0.5", URL: "turn:staticauth.openrelay.metered.ca:80",
			RelayProtocol: "tcp",
		},
		"rc": webrtc.ICECandidateStats{
			ID: "rc", CandidateType: webrtc.ICECandidateTypeSrflx, IP: "203.0.113.7",
		},
		"pair": webrtc.ICECandidatePairStats{
			LocalCandidateID: "lc", RemoteCandidateID: "rc",
			Nominated: true, CurrentRoundTripTime: 0.05,
		},
	}
	snap := snapshotFromReport(report)
	assert.Equal(t, "turn:staticauth.openrelay.metered.ca:80", snap.ICELocalCandidateURL)
	assert.Equal(t, "relay", snap.ICELocalCandidateType)
	assert.Equal(t, "tcp", snap.ICERelayProtocol)
	assert.Equal(t, "srflx", snap.ICERemoteCandidateType)

	idx := buildICETagIndex(telemetryTestWelcome())
	assert.Equal(t, "openrelay", idx.providerFor(snap.ICELocalCandidateURL, snap.ICELocalCandidateAddress),
		"report URL 必须能归因到 Welcome tag")
}

// relay 候选地址 IP 归因（pion/ice v4.4.7 不填候选 stats URL——地址匹配
// 是 relay 成本归因的工作路径）。
func TestProviderForRelayAddressAttribution(t *testing.T) {
	idx := buildICETagIndex(&Welcome{ICEServers: []ICEServer{
		{
			URLs: []string{"turn:203.0.113.10:3478?transport=udp"},
			Tag:  ICEServerTagSelf,
		},
		{
			URLs: []string{"turn:198.51.100.20:443?transport=tcp"},
			Tag:  "openrelay",
		},
	}})

	cases := []struct {
		addr string
		want string
	}{
		{"203.0.113.10:49152", ICEServerTagSelf}, // 自建 TURN 中继地址
		{"198.51.100.20:53000", "openrelay"},     // 第三方中继地址
		{"192.0.2.99:40000", ""},                 // 非已知 TURN IP
		{"203.0.113.10", ICEServerTagSelf},       // 无端口形态
		{"", ""},                                 // host 直连候选
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, idx.providerFor("", tc.addr), "addr=%q", tc.addr)
	}
}

// 公网 peer 的 srflx 地址可能等于 TURN 服务器 IP（探针与服务器同机
// 部署时必现）——地址匹配只允许 relay 候选走，srflx 不归因。
func TestProviderForCandidateSrflxNotAddrMatched(t *testing.T) {
	idx := buildICETagIndex(&Welcome{ICEServers: []ICEServer{
		{URLs: []string{"turn:203.0.113.10:3478"}, Tag: ICEServerTagSelf},
	}})

	relaySnap := StatsSnapshot{
		ICELocalCandidateType:    "relay",
		ICELocalCandidateAddress: "203.0.113.10:49152",
	}
	assert.Equal(t, ICEServerTagSelf, idx.providerForCandidate(relaySnap))

	srflxSnap := StatsSnapshot{
		ICELocalCandidateType:    "srflx",
		ICELocalCandidateAddress: "203.0.113.10:60000", // == TURN 服务器 IP
	}
	assert.Empty(t, idx.providerForCandidate(srflxSnap), "srflx 不得按地址误归因")
}
