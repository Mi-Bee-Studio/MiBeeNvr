package sip

// Sub-channel prober tests (#560): candidate arithmetic, vendor gating, the
// silent negative path (INVITE answered but no media → timeout → synthetic
// channel removed, camera untouched), and persisted-code re-registration.

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/ghettovoice/gosip/sip/parser"
	"github.com/stretchr/testify/require"
)

// nextRequest reads one datagram and returns it when it is a SIP REQUEST
// (server-initiated INVITE/catalog query); nil on timeout or non-request.
func (c *sipClient) nextRequest(timeout time.Duration) sip.Request {
	buf := make([]byte, 65535)
	if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil
	}
	n, _, err := c.conn.ReadFromUDP(buf)
	if err != nil {
		return nil
	}
	msg, err := parser.ParseMessage(buf[:n], log.NewDefaultLogrusLogger())
	if err != nil {
		return nil
	}
	if req, ok := msg.(sip.Request); ok {
		return req
	}
	return nil
}

// resetSubProbeMemo wipes the process-lifetime probe memoization. The tests
// below share one testDeviceID, and -count>1 (or any earlier test in the
// package) leaves "already probed" entries behind — without this reset the
// probe never fires and the tests fail on their second run (CI flake class).
func resetSubProbeMemo(t *testing.T) {
	t.Helper()
	subProbed.Range(func(key, _ any) bool {
		subProbed.Delete(key)
		return true
	})
}

// respond200 answers a server-initiated request with a bare 200 OK (no SDP —
// the probe then times out waiting for media, which is exactly the negative
// path under test). Raw-string construction mirroring gbsim's reply200 —
// gosip's response builder produces a message the server-side parser routes
// back through the request path (nil Uri panic in requestIDs).
func (c *sipClient) respond200(req sip.Request) {
	var b strings.Builder
	b.WriteString("SIP/2.0 200 OK\r\n")
	for _, h := range []string{"Via", "From", "To", "Call-ID", "CSeq", "Max-Forwards"} {
		for _, v := range req.GetHeaders(h) {
			b.WriteString(h + ": " + v.Value() + "\r\n")
		}
	}
	b.WriteString("Content-Length: 0\r\n\r\n")
	if _, err := c.conn.WriteToUDP([]byte(b.String()), c.addr); err != nil {
		c.t.Fatalf("respond200: write: %v", err)
	}
}

func TestOffsetChannelCode(t *testing.T) {
	t.Helper()
	cases := []struct {
		id     string
		offset int
		want   string
	}{
		{"34020000001320000001", 1, "34020000001320000002"},
		{"34020000001320000099", 1, "34020000001320000100"},  // carry
		{"34020000001320000099", 40, "34020000001320000139"}, // multi-carry
		{"99999999999999999999", 1, ""},                      // overflow past MSD
		{"34020000001320000001", 0, ""},                      // disabled
		{"3402000000132A000001", 1, ""},                      // non-digit
		{"", 1, ""},
	}
	for _, c := range cases {
		require.Equal(t, c.want, offsetChannelCode(c.id, c.offset), "id=%s offset=%d", c.id, c.offset)
	}
}

func TestKnownOffsetVendor(t *testing.T) {
	t.Helper()
	require.True(t, knownOffsetVendor("Hikvision"))
	require.True(t, knownOffsetVendor("HIKVISION DIGITAL TECHNOLOGY"))
	require.True(t, knownOffsetVendor("海康威视"))
	require.True(t, knownOffsetVendor("Dahua"))
	require.True(t, knownOffsetVendor("浙江大华"))
	require.False(t, knownOffsetVendor("gbsim"))
	require.False(t, knownOffsetVendor("mibee-rec"))
	require.False(t, knownOffsetVendor(""))
}

// TestProbeSubChannel_TimeoutSilent: a probe whose INVITE is answered but
// never delivers media times out silently — the synthetic channel is
// unregistered, nothing is persisted, and the negative result is memoized
// (a second trigger does not re-INVITE).
func TestProbeSubChannel_TimeoutSilent(t *testing.T) {
	t.Helper()
	resetSubProbeMemo(t)
	subProbeWait.Store(int64(20 * time.Millisecond))
	subProbeTimeout.Store(int64(200 * time.Millisecond))
	t.Cleanup(func() {
		subProbeWait.Store(int64(5 * time.Second))
		subProbeTimeout.Store(int64(6 * time.Second))
	})

	cfg := testConfig(t)
	cfg.SubChannelProbe = "on"
	cfg.SubChannelProbeOffset = 1
	srv, dm := startTestServer(t, cfg)
	fe := &fakeEnroller{}
	srv.SetCameraEnroller(fe)
	client := newSIPClient(t, cfg.SIPListen)

	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: deviceAddrOf(client)})

	const mainCh = "34020000001320000071"
	sendCatalog(t, client, cfg, []manscdp.Item{{DeviceID: mainCh, Name: "Probe Cam", Parental: 0}})

	const candidate = "34020000001320000072"
	// The probe INVITE arrives (device answers 200 with SDP but — critically —
	// no RTP ever follows). Drain it so the transaction completes.
	deadline := time.Now().Add(3 * time.Second)
	invited := false
	for time.Now().Before(deadline) {
		if req := client.nextRequest(200 * time.Millisecond); req != nil && string(req.Method()) == "INVITE" {
			if to, ok := req.To(); ok {
				if to.Address.User().String() == candidate {
					invited = true
				}
			}
			client.respond200(req)
		}
		if invited {
			break
		}
	}
	require.True(t, invited, "probe must INVITE the +1 candidate code")

	// Timeout elapses → synthetic channel removed, nothing persisted. The
	// 200ms probe timeout is a floor, not a ceiling: on a loaded CI runner the
	// INVITE transaction teardown (release → SIP BYE) can take seconds, so the
	// wait budget is generous — green runs still finish in ~200ms.
	require.Eventually(t, func() bool {
		_, ok := dm.FindChannel(testDeviceID, candidate)
		return !ok
	}, 10*time.Second, 50*time.Millisecond, "synthetic sub-channel must be unregistered after probe timeout")
	require.True(t, fe.subSetEmpty(), "no sub_channel_id may be persisted on probe timeout")

	// Memoized: re-running the probe path issues no second INVITE.
	srv.maybeProbeSubChannels(testDeviceID)
	time.Sleep(300 * time.Millisecond)
	require.Never(t, func() bool {
		if req := client.nextRequest(50 * time.Millisecond); req != nil && string(req.Method()) == "INVITE" {
			return true
		}
		return false
	}, 300*time.Millisecond, 50*time.Millisecond, "memoized probe must not re-INVITE")
}

// TestProbeSubChannels_VendorGateAutoMode: in "auto" mode a device whose
// manufacturer is unknown never gets probed.
func TestProbeSubChannels_VendorGateAutoMode(t *testing.T) {
	t.Helper()
	resetSubProbeMemo(t)
	subProbeWait.Store(int64(10 * time.Millisecond))
	t.Cleanup(func() { subProbeWait.Store(int64(5 * time.Second)) })

	cfg := testConfig(t) // SubChannelProbe defaults empty → "auto" via ApplyDefaults? tests set explicit:
	cfg.SubChannelProbe = "auto"
	cfg.SubChannelProbeOffset = 1
	srv, dm := startTestServer(t, cfg)
	fe := &fakeEnroller{}
	srv.SetCameraEnroller(fe)
	client := newSIPClient(t, cfg.SIPListen)

	// gbsim-like manufacturer — not a known offset vendor.
	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: deviceAddrOf(client), Manufacturer: "mibee-rec", Model: "X1"})

	sendCatalog(t, client, cfg, []manscdp.Item{{DeviceID: "34020000001320000081", Name: "Gate Cam", Parental: 0}})
	time.Sleep(200 * time.Millisecond)

	require.Never(t, func() bool {
		if req := client.nextRequest(50 * time.Millisecond); req != nil && string(req.Method()) == "INVITE" {
			return true
		}
		return false
	}, 300*time.Millisecond, 50*time.Millisecond, "auto mode must skip unknown vendors entirely")
}

// TestProbeSubChannels_PersistedCodeReregisters: a camera that already carries
// a sub_channel_id gets its synthetic channel re-registered (post-restart
// state) without any probe INVITE.
func TestProbeSubChannels_PersistedCodeReregisters(t *testing.T) {
	t.Helper()
	resetSubProbeMemo(t)
	subProbeWait.Store(int64(10 * time.Millisecond))
	t.Cleanup(func() { subProbeWait.Store(int64(5 * time.Second)) })

	cfg := testConfig(t)
	cfg.SubChannelProbe = "on"
	cfg.SubChannelProbeOffset = 1
	srv, dm := startTestServer(t, cfg)

	const mainCh = "34020000001320000091"
	const subCh = "34020000001320000101"
	fe := &fakeEnroller{subPersisted: map[string]string{testDeviceID + "/" + mainCh: subCh}}
	srv.SetCameraEnroller(fe)
	client := newSIPClient(t, cfg.SIPListen)

	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: deviceAddrOf(client), Manufacturer: "Hikvision"})

	sendCatalog(t, client, cfg, []manscdp.Item{{DeviceID: mainCh, Name: "Persist Cam", Parental: 0}})

	require.Eventually(t, func() bool {
		_, ok := dm.FindChannel(testDeviceID, subCh)
		return ok
	}, 3*time.Second, 50*time.Millisecond, "persisted sub code must be re-registered after restart")

	require.Never(t, func() bool {
		if req := client.nextRequest(50 * time.Millisecond); req != nil && string(req.Method()) == "INVITE" {
			return true
		}
		return false
	}, 300*time.Millisecond, 50*time.Millisecond, "persisted code must not be re-probed")
}

// deviceAddrOf returns the fake device's own socket address (the sipClient's
// addr field is the SERVER's address — the probe INVITE must come TO the
// device socket, not loop back to the server).
func deviceAddrOf(c *sipClient) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(c.localPort()))
}

// sendCatalog delivers a catalog MESSAGE for testDeviceID and awaits the 200.
func sendCatalog(t *testing.T, client *sipClient, cfg config.GB28181ServerConfig, items []manscdp.Item) {
	t.Helper()
	body, err := manscdp.Encode(manscdp.Catalog{
		CmdType:  manscdp.CmdCatalog,
		SN:       1,
		DeviceID: testDeviceID,
		SumNum:   len(items),
		Item:     items,
	})
	require.NoError(t, err)
	req := buildRequest(t, sip.MESSAGE, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), string(body))
	res := client.roundTrip(req)
	require.EqualValues(t, 200, res.StatusCode())
}
