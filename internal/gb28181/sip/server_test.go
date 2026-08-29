package sip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/ghettovoice/gosip/sip/parser"
	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
	"github.com/stretchr/testify/require"
)

const (
	testDeviceID = "34020000001320000001"
	testServerID = "34020000002000000001"
)

// freeUDPPort returns a free UDP port for the test server to bind.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("freeUDPPort: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// testConfig builds a GB28181 server config bound to a free local port.
func testConfig(t *testing.T) config.GB28181ServerConfig {
	t.Helper()
	return config.GB28181ServerConfig{
		SIPListen: fmt.Sprintf("127.0.0.1:%d", freeUDPPort(t)),
		Realm:     "test-realm",
		Password:  "test-password",
	}
}

// testPortBase hands each test server a disjoint 100-port window. A shared
// fixed range (30000..30100) let one test's not-yet-recycled receiver make
// the NEXT test's session bind fail — cascading failures (#571).
var testPortBase atomic.Int32

// startTestServer starts a Server on the config's address and registers a
// cleanup that stops it.
func startTestServer(t *testing.T, cfg config.GB28181ServerConfig) (*Server, *platform.DeviceManager) {
	t.Helper()
	base := int(20000 + 100*testPortBase.Add(1))
	dm := platform.NewDeviceManager(60 * time.Second)
	srv := NewServer(cfg, dm, platform.NewSessionManager(platform.NewPortManager(uint16(base), uint16(base+99)), cfg.ServerID), nil)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv, dm
}

// sipClient is a minimal raw-UDP SIP client for exercising the server.
type sipClient struct {
	t    *testing.T
	conn *net.UDPConn
	addr *net.UDPAddr
}

func newSIPClient(t *testing.T, serverAddr string) *sipClient {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("newSIPClient: %v", err)
	}
	addr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		t.Fatalf("newSIPClient: resolve %q: %v", serverAddr, err)
	}
	t.Cleanup(func() { conn.Close() })
	return &sipClient{t: t, conn: conn, addr: addr}
}

// localPort returns the client's bound UDP port, used as the Via sent-by so
// the server routes responses back to this socket.
func (c *sipClient) localPort() int {
	c.t.Helper()
	return c.conn.LocalAddr().(*net.UDPAddr).Port
}

// roundTrip sends a request and returns the first final (>= 200) response,
// skipping provisional responses such as 100 Trying.
func (c *sipClient) roundTrip(req sip.Request) sip.Response {
	c.t.Helper()
	callID := ""
	if id, ok := req.CallID(); ok {
		callID = id.String()
	}
	if _, err := c.conn.WriteToUDP([]byte(req.String()), c.addr); err != nil {
		c.t.Fatalf("roundTrip: write: %v", err)
	}
	buf := make([]byte, 65535)
	for {
		if err := c.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			c.t.Fatalf("roundTrip: set deadline: %v", err)
		}
		n, _, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			c.t.Fatalf("roundTrip: read: %v", err)
		}
		msg, err := parser.ParseMessage(buf[:n], log.NewDefaultLogrusLogger())
		if err != nil {
			c.t.Fatalf("roundTrip: parse response: %v", err)
		}
		// Skip unsolicited server-initiated requests (e.g. the catalog query
		// sent right after a successful REGISTER) — they are not responses.
		res, ok := msg.(sip.Response)
		if !ok {
			continue
		}
		// Match by CallID so late responses to earlier requests are ignored.
		if callID != "" {
			if rc, ok := res.CallID(); !ok || rc.String() != callID {
				continue
			}
		}
		if res.StatusCode() >= 200 {
			return res
		}
	}
}

// buildRequest constructs a SIP request addressed to the server.
func buildRequest(t *testing.T, method sip.RequestMethod, deviceID, serverID, serverAddr string, clientPort int, body string, extra ...sip.Header) sip.Request {
	t.Helper()
	host, portStr, err := net.SplitHostPort(serverAddr)
	if err != nil {
		t.Fatalf("buildRequest: bad server addr %q: %v", serverAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("buildRequest: bad port %q: %v", portStr, err)
	}
	portVal := sip.Port(port)

	from := &sip.Address{
		DisplayName: sip.String{Str: deviceID},
		Uri: &sip.SipUri{
			FUser: sip.String{Str: deviceID},
			FHost: host,
		},
	}
	to := &sip.Address{
		DisplayName: sip.String{Str: serverID},
		Uri: &sip.SipUri{
			FUser: sip.String{Str: serverID},
			FHost: host,
		},
	}
	recipient := &sip.SipUri{
		FUser: sip.String{Str: serverID},
		FHost: host,
		FPort: &portVal,
	}

	rb := sip.NewRequestBuilder()
	rb.SetMethod(method)
	rb.SetFrom(from)
	rb.SetTo(to)
	rb.SetRecipient(recipient)
	rb.SetHost(host)
	clientPortVal := sip.Port(clientPort)
	rb.AddVia(&sip.ViaHop{
		Host:   host,
		Port:   &clientPortVal,
		Params: sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}),
	})
	cid := sip.CallID(fmt.Sprintf("%d-%s", time.Now().UnixNano(), deviceID))
	rb.SetCallID(&cid)
	rb.SetSeqNo(1)
	mf := sip.MaxForwards(70)
	rb.SetMaxForwards(&mf)
	if body != "" {
		rb.SetBody(body)
		ct := sip.ContentType("Application/MANSCDP+xml")
		rb.SetContentType(&ct)
	}
	for _, h := range extra {
		rb.AddHeader(h)
	}
	req, err := rb.Build()
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	return req
}

// digestAuth computes the Authorization header for a request given the
// WWW-Authenticate challenge received from the server.
func digestAuth(t *testing.T, challenge *sip.GenericHeader, req sip.Request, password string) *sip.GenericHeader {
	t.Helper()
	auth := sip.AuthFromValue(challenge.Contents)
	from, _ := req.From()
	auth.SetUsername(from.Address.User().String())
	auth.SetMethod(string(req.Method()))
	auth.SetUri(req.Recipient().String())
	auth.SetPassword(password)
	if auth.Qop() == "auth" {
		auth.SetNc("00000001")
		auth.SetCNonce("abcdef")
	}
	auth.SetResponse(auth.CalcResponse())
	return &sip.GenericHeader{HeaderName: "Authorization", Contents: auth.String()}
}

// getChallenge extracts the WWW-Authenticate header from a 401 response.
func getChallenge(t *testing.T, res sip.Response) *sip.GenericHeader {
	t.Helper()
	for _, h := range res.GetHeaders("WWW-Authenticate") {
		if gh, ok := h.(*sip.GenericHeader); ok {
			return gh
		}
	}
	t.Fatalf("no WWW-Authenticate header in response")
	return nil
}

func TestServer_Name(t *testing.T) {
	srv := NewServer(config.GB28181ServerConfig{}, platform.NewDeviceManager(time.Minute), platform.NewSessionManager(platform.NewPortManager(30000, 30100), ""), nil)
	if got := srv.Name(); got != "gb28181" {
		t.Fatalf("Name() = %q, want %q", got, "gb28181")
	}
}

func TestServer_StartStop(t *testing.T) {
	cfg := testConfig(t)
	srv, _ := startTestServer(t, cfg)
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := srv.Stop(); err != nil { // idempotent
		t.Fatalf("second Stop: %v", err)
	}
}

func TestServer_Start_Twice(t *testing.T) {
	cfg := testConfig(t)
	srv, _ := startTestServer(t, cfg)
	if err := srv.Start(context.Background()); err != nil { // idempotent
		t.Fatalf("second Start: %v", err)
	}
}

func TestServer_Register_Flow(t *testing.T) {
	cfg := testConfig(t)
	_, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	// First REGISTER without credentials → 401 + challenge.
	req := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "")
	res := client.roundTrip(req)
	if res.StatusCode() != 401 {
		t.Fatalf("first REGISTER status = %d, want 401", res.StatusCode())
	}
	challenge := getChallenge(t, res)

	// Second REGISTER with digest → 200, device registered.
	auth := digestAuth(t, challenge, req, cfg.Password)
	req2 := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "", auth)
	res2 := client.roundTrip(req2)
	if res2.StatusCode() != 200 {
		t.Fatalf("authed REGISTER status = %d, want 200", res2.StatusCode())
	}

	dev, ok := dm.Device(testDeviceID)
	if !ok {
		t.Fatalf("device %s not registered", testDeviceID)
	}
	if dev.Status.Load() != platform.DeviceOnline {
		t.Fatalf("device status = %d, want online", dev.Status.Load())
	}
	dev.Mu.RLock()
	netAddr := dev.NetAddr
	dev.Mu.RUnlock()
	if !strings.Contains(netAddr, "127.0.0.1") {
		t.Fatalf("device NetAddr = %q, want client addr", netAddr)
	}
}

func TestServer_Register_UnallowedDevice(t *testing.T) {
	cfg := testConfig(t)
	cfg.AllowedDeviceIDs = []string{"34020000001320000002"}
	startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	req := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "")
	res := client.roundTrip(req)
	if res.StatusCode() != 403 {
		t.Fatalf("status = %d, want 403", res.StatusCode())
	}
}

func TestServer_Register_BadDigest(t *testing.T) {
	cfg := testConfig(t)
	startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	req := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "")
	res := client.roundTrip(req)
	challenge := getChallenge(t, res)

	auth := digestAuth(t, challenge, req, "wrong-password")
	req2 := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "", auth)
	res2 := client.roundTrip(req2)
	if res2.StatusCode() != 403 {
		t.Fatalf("status = %d, want 403", res2.StatusCode())
	}
}

func TestServer_Register_NoPassword(t *testing.T) {
	cfg := testConfig(t)
	cfg.Password = ""
	_, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	req := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "")
	res := client.roundTrip(req)
	if res.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode())
	}
	if _, ok := dm.Device(testDeviceID); !ok {
		t.Fatalf("device not registered")
	}
}

func TestServer_Register_ExpiresZero(t *testing.T) {
	cfg := testConfig(t)
	_, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	// Register.
	req := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "")
	res := client.roundTrip(req)
	challenge := getChallenge(t, res)
	auth := digestAuth(t, challenge, req, cfg.Password)
	req2 := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "", auth)
	if res2 := client.roundTrip(req2); res2.StatusCode() != 200 {
		t.Fatalf("register status = %d, want 200", res2.StatusCode())
	}
	if _, ok := dm.Device(testDeviceID); !ok {
		t.Fatalf("device not registered")
	}

	// Unregister with Expires: 0.
	exp := sip.Expires(0)
	req3 := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "", auth, &exp)
	if res3 := client.roundTrip(req3); res3.StatusCode() != 200 {
		t.Fatalf("unregister status = %d, want 200", res3.StatusCode())
	}
	if _, ok := dm.Device(testDeviceID); ok {
		t.Fatalf("device still registered after Expires: 0")
	}
}

func TestServer_Message_Keepalive(t *testing.T) {
	cfg := testConfig(t)
	_, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	// Register the device directly (MESSAGE bodies are not authenticated).
	dm.Register(&platform.Device{ID: testDeviceID, NetAddr: "127.0.0.1:9999"})

	body, err := manscdp.Encode(manscdp.Keepalive{
		CmdType:  manscdp.CmdKeepalive,
		SN:       1,
		DeviceID: testDeviceID,
		Status:   "OK",
	})
	if err != nil {
		t.Fatalf("Encode keepalive: %v", err)
	}

	req := buildRequest(t, sip.MESSAGE, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), string(body))
	res := client.roundTrip(req)
	if res.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode())
	}
	dev, ok := dm.Device(testDeviceID)
	if !ok {
		t.Fatalf("device not registered")
	}
	if dev.Status.Load() != platform.DeviceOnline {
		t.Fatalf("device status = %d, want online", dev.Status.Load())
	}
}

func TestServer_Message_Catalog(t *testing.T) {
	cfg := testConfig(t)
	_, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	dm.Register(&platform.Device{ID: testDeviceID, NetAddr: "127.0.0.1:9999"})

	body, err := manscdp.Encode(manscdp.Catalog{
		CmdType:  manscdp.CmdCatalog,
		SN:       1,
		DeviceID: testDeviceID,
		SumNum:   2,
		Item: []manscdp.Item{
			{DeviceID: "34020000001320000011", Name: "Front Door", Parental: 0},
			{DeviceID: "34020000001320000012", Name: "Back Yard", Parental: 0},
		},
	})
	if err != nil {
		t.Fatalf("Encode catalog: %v", err)
	}

	req := buildRequest(t, sip.MESSAGE, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), string(body))
	res := client.roundTrip(req)
	if res.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode())
	}

	channels := dm.Channels(testDeviceID)
	if len(channels) != 2 {
		t.Fatalf("channels = %d, want 2", len(channels))
	}
	if channels[0].ID != "34020000001320000011" || channels[0].Name != "Front Door" {
		t.Fatalf("channel[0] = %+v", channels[0])
	}
}

func TestServer_Message_DeviceInfo(t *testing.T) {
	cfg := testConfig(t)
	_, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	dm.Register(&platform.Device{ID: testDeviceID, NetAddr: "127.0.0.1:9999"})

	body, err := manscdp.Encode(manscdp.DeviceInfo{
		CmdType:      manscdp.CmdDeviceInfo,
		SN:           1,
		DeviceID:     testDeviceID,
		DeviceName:   "Hikvision NVR",
		Manufacturer: "Hikvision",
		Model:        "DS-7608",
		Firmware:     "V4.30",
	})
	if err != nil {
		t.Fatalf("Encode device info: %v", err)
	}

	req := buildRequest(t, sip.MESSAGE, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), string(body))
	res := client.roundTrip(req)
	if res.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode())
	}

	dev, ok := dm.Device(testDeviceID)
	if !ok {
		t.Fatalf("device not registered")
	}
	dev.Mu.RLock()
	name, manufacturer, model := dev.Name, dev.Manufacturer, dev.Model
	dev.Mu.RUnlock()
	if name != "Hikvision NVR" || manufacturer != "Hikvision" || model != "DS-7608" {
		t.Fatalf("device metadata = name=%q manufacturer=%q model=%q", name, manufacturer, model)
	}
}

func TestServer_Message_InvalidXML(t *testing.T) {
	cfg := testConfig(t)
	startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	req := buildRequest(t, sip.MESSAGE, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "not xml at all")
	res := client.roundTrip(req)
	if res.StatusCode() != 400 {
		t.Fatalf("status = %d, want 400", res.StatusCode())
	}
}

func TestServer_Message_EmptyBody(t *testing.T) {
	cfg := testConfig(t)
	startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	req := buildRequest(t, sip.MESSAGE, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "")
	res := client.roundTrip(req)
	if res.StatusCode() != 400 {
		t.Fatalf("status = %d, want 400", res.StatusCode())
	}
}

func TestServer_Invite_NoHook(t *testing.T) {
	cfg := testConfig(t)
	startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	req := buildRequest(t, sip.INVITE, testDeviceID, "34020000001320000011", cfg.SIPListen, client.localPort(), "")
	res := client.roundTrip(req)
	if res.StatusCode() != 486 {
		t.Fatalf("status = %d, want 486", res.StatusCode())
	}
}

func TestServer_Invite_Hook(t *testing.T) {
	cfg := testConfig(t)
	srv, _ := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	got := make(chan [2]string, 1)
	srv.SetInviteHook(func(deviceID, channelID string) {
		got <- [2]string{deviceID, channelID}
	})

	req := buildRequest(t, sip.INVITE, testDeviceID, "34020000001320000011", cfg.SIPListen, client.localPort(), "")
	if _, err := client.conn.WriteToUDP([]byte(req.String()), client.addr); err != nil {
		t.Fatalf("write INVITE: %v", err)
	}
	select {
	case ids := <-got:
		if ids[0] != testDeviceID || ids[1] != "34020000001320000011" {
			t.Fatalf("hook got %v", ids)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("hook not called")
	}
}

func TestServer_Bye_NoHook(t *testing.T) {
	cfg := testConfig(t)
	startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	req := buildRequest(t, sip.BYE, testDeviceID, "34020000001320000011", cfg.SIPListen, client.localPort(), "")
	res := client.roundTrip(req)
	if res.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode())
	}
}

func TestServer_Bye_Hook(t *testing.T) {
	cfg := testConfig(t)
	srv, _ := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	got := make(chan [2]string, 1)
	srv.SetByeHook(func(deviceID, channelID string) {
		got <- [2]string{deviceID, channelID}
	})

	req := buildRequest(t, sip.BYE, testDeviceID, "34020000001320000011", cfg.SIPListen, client.localPort(), "")
	if _, err := client.conn.WriteToUDP([]byte(req.String()), client.addr); err != nil {
		t.Fatalf("write BYE: %v", err)
	}
	select {
	case ids := <-got:
		if ids[0] != testDeviceID || ids[1] != "34020000001320000011" {
			t.Fatalf("hook got %v", ids)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("hook not called")
	}
}

func TestHostOfAddr(t *testing.T) {
	cases := map[string]string{
		"192.168.63.152:36115": "192.168.63.152",
		"10.0.0.1:5060":        "10.0.0.1",
		"bare-addr":            "bare-addr",
	}
	for in, want := range cases {
		if got := hostOfAddr(in); got != want {
			t.Fatalf("hostOfAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServer_Register_PortRotationKeepsAddressStable(t *testing.T) {
	// SIP-over-UDP stacks may open a fresh socket per REGISTER. A source-port
	// change alone must NOT be treated as a device address change (which
	// recycles media sessions); only an IP change may.
	cfg := testConfig(t)
	_, dm := startTestServer(t, cfg)

	register := func(client *sipClient) {
		req := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "")
		res := client.roundTrip(req)
		if res.StatusCode() != 401 {
			t.Fatalf("first REGISTER status = %d, want 401", res.StatusCode())
		}
		auth := digestAuth(t, getChallenge(t, res), req, cfg.Password)
		req2 := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "", auth)
		if res2 := client.roundTrip(req2); res2.StatusCode() != 200 {
			t.Fatalf("authed REGISTER status = %d, want 200", res2.StatusCode())
		}
	}

	register(newSIPClient(t, cfg.SIPListen))
	// A second socket — same IP, different source port — re-REGISTERs.
	register(newSIPClient(t, cfg.SIPListen))

	dev, ok := dm.Device(testDeviceID)
	if !ok {
		t.Fatalf("device %s no longer registered after port rotation", testDeviceID)
	}
	dev.Mu.RLock()
	netAddr := dev.NetAddr
	dev.Mu.RUnlock()
	if !strings.Contains(netAddr, "127.0.0.1") {
		t.Fatalf("device NetAddr = %q, want the newest client addr", netAddr)
	}
}

// fakeEnroller stubs CameraEnroller for pseudo-channel lifecycle tests.
// Mutex-guarded: mergeCatalogChannels calls it from spawned goroutines while
// assertions read the recorded calls (-race clean).
type fakeEnroller struct {
	mu           sync.Mutex
	enrolled     []string // "deviceID/channelID"
	archived     []string // "deviceID/channelID"
	subSet       []string // "deviceID/channelID->subChannelID"
	subPersisted map[string]string
	// pbSink, when non-nil, is returned by NewGB28181PlaybackSink (playback
	// fetch tests).
	pbSink platform.AUWriter
	// naluWriter, when non-nil, is returned by GB28181NALUWriter (live
	// InviteChannel tests).
	naluWriter func(au [][]byte, ptsTicks int64, isIDR bool)
}

func (f *fakeEnroller) EnsureGB28181Camera(deviceID, channelID, name, sourceIP string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enrolled = append(f.enrolled, deviceID+"/"+channelID)
	return nil
}

func (f *fakeEnroller) GB28181CameraIDByChannel(deviceID, channelID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.enrolled {
		if e == deviceID+"/"+channelID {
			return "cam-" + channelID, true
		}
	}
	return "", false
}

func (f *fakeEnroller) GB28181NALUWriter(string) func([][]byte, int64, bool) { return f.naluWriter }
func (f *fakeEnroller) GB28181AudioWriter(string) func(string, []byte, []byte, int64, int) {
	return nil
}
func (f *fakeEnroller) OnGB28181Invite(string) {}
func (f *fakeEnroller) OnGB28181Bye(string)    {}
func (f *fakeEnroller) NewGB28181PlaybackSink(string) (platform.AUWriter, error) {
	if f.pbSink != nil {
		return f.pbSink, nil
	}
	return nil, errors.New("not implemented in fake")
}

func (f *fakeEnroller) GB28181PlaybackAudioWriter(string) func(string, []byte, []byte, int64, int) {
	return nil
}
func (f *fakeEnroller) UpdateGB28181DeviceMeta(string, string, string) error { return nil }
func (f *fakeEnroller) ArchiveGB28181Camera(deviceID, channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archived = append(f.archived, deviceID+"/"+channelID)
	return nil
}

func (f *fakeEnroller) archivedContains(entry string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.archived, entry)
}

func (f *fakeEnroller) enrolledEmpty() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.enrolled) == 0
}

func (f *fakeEnroller) archivedEmpty() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.archived) == 0
}

// TestServer_Register_SkipsDeviceSelfWhenCatalogChannelsPersisted (#352):
// after a restart, a device whose catalog channels were persisted must NOT
// get its device-self pseudo-channel (and camera) re-created.
func TestServer_Register_SkipsDeviceSelfWhenCatalogChannelsPersisted(t *testing.T) {
	cfg := testConfig(t)
	dbPath := filepath.Join(t.TempDir(), "sip.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { _ = db.Close() })

	dm := platform.NewDeviceManager(60 * time.Second)
	srv := NewServer(cfg, dm, platform.NewSessionManager(platform.NewPortManager(30000, 30100), cfg.ServerID), db)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() { _ = srv.Stop() })

	// A catalog channel persisted for this device from before the "restart".
	require.NoError(t, db.UpsertGB28181Channel(context.Background(), storage.GB28181Channel{
		ID: testDeviceID + "1", DeviceID: testDeviceID, Status: "idle", UpdatedAt: time.Now(),
	}))

	fe := &fakeEnroller{}
	srv.SetCameraEnroller(fe)

	client := newSIPClient(t, cfg.SIPListen)
	req := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "")
	res := client.roundTrip(req)
	require.EqualValues(t, 401, res.StatusCode())
	auth := digestAuth(t, getChallenge(t, res), req, cfg.Password)
	req2 := buildRequest(t, sip.REGISTER, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "", auth)
	res2 := client.roundTrip(req2)
	require.EqualValues(t, 200, res2.StatusCode())

	_, ok := dm.FindChannel(testDeviceID, testDeviceID)
	require.False(t, ok, "device-self pseudo-channel must not be created when catalog channels are persisted")
	require.True(t, fe.enrolledEmpty(), "device-self camera must not be enrolled")
}

// TestRetireDeviceSelfChannel: a catalog with real channels retires the idle
// device-self pseudo-channel and archives its camera (#352).
func TestRetireDeviceSelfChannel(t *testing.T) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)
	fe := &fakeEnroller{}
	srv.SetCameraEnroller(fe)

	dm.Register(&platform.Device{ID: testDeviceID, NetAddr: "127.0.0.1:60000"})
	dm.RegisterChannel(testDeviceID, &platform.Channel{ID: testDeviceID, DeviceID: testDeviceID})

	srv.mergeCatalogChannels(testDeviceID, []manscdp.Item{
		{DeviceID: testDeviceID + "1", Name: "Real Channel"},
	})

	_, ok := dm.FindChannel(testDeviceID, testDeviceID)
	require.False(t, ok, "idle device-self channel must be retired when real channels exist")
	require.True(t, fe.archivedContains(testDeviceID+"/"+testDeviceID), "auto-enrolled device-self camera must be archived")
}

// TestRetireDeviceSelfChannel_KeptWhenStreamingOrListed: the pseudo-channel
// survives when it is actively playing or when the catalog lists the device
// ID itself (single-channel devices whose channel equals the device ID).
func TestRetireDeviceSelfChannel_KeptWhenStreamingOrListed(t *testing.T) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)
	fe := &fakeEnroller{}
	srv.SetCameraEnroller(fe)

	// Playing device-self: kept.
	dm.Register(&platform.Device{ID: "dev-a", NetAddr: "127.0.0.1:60001"})
	chA := &platform.Channel{ID: "dev-a", DeviceID: "dev-a"}
	chA.Status.Store(int32(platform.ChannelPlaying))
	dm.RegisterChannel("dev-a", chA)
	srv.mergeCatalogChannels("dev-a", []manscdp.Item{{DeviceID: "dev-a-ch1"}})
	_, ok := dm.FindChannel("dev-a", "dev-a")
	require.True(t, ok, "playing device-self channel must be kept")

	// Catalog lists the device ID itself: kept.
	dm.Register(&platform.Device{ID: "dev-b", NetAddr: "127.0.0.1:60002"})
	dm.RegisterChannel("dev-b", &platform.Channel{ID: "dev-b", DeviceID: "dev-b"})
	srv.mergeCatalogChannels("dev-b", []manscdp.Item{{DeviceID: "dev-b"}})
	_, ok = dm.FindChannel("dev-b", "dev-b")
	require.True(t, ok, "device-self listed in catalog must be kept")
	require.True(t, fe.archivedEmpty())
}

// TestRetireDeviceSelfChannel_NoRealChannels: parental-only catalogs (org
// trees) must not retire the device-self channel.
func TestRetireDeviceSelfChannel_NoRealChannels(t *testing.T) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)
	srv.SetCameraEnroller(&fakeEnroller{})

	dm.Register(&platform.Device{ID: "dev-c", NetAddr: "127.0.0.1:60003"})
	dm.RegisterChannel("dev-c", &platform.Channel{ID: "dev-c", DeviceID: "dev-c"})
	srv.mergeCatalogChannels("dev-c", []manscdp.Item{{DeviceID: "org-1", Parental: 1}})
	_, ok := dm.FindChannel("dev-c", "dev-c")
	require.True(t, ok, "parental-only catalog must not retire the device-self channel")
}

func (f *fakeEnroller) GB28181RecordingWanted(deviceID, channelID string) bool { return false }

func (f *fakeEnroller) GB28181SubChannelID(deviceID, channelID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subPersisted[deviceID+"/"+channelID]
}

func (f *fakeEnroller) SetGB28181SubChannel(deviceID, channelID, subChannelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subSet = append(f.subSet, deviceID+"/"+channelID+"->"+subChannelID)
	return nil
}

func (f *fakeEnroller) subSetEmpty() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subSet) == 0
}

// TestServer_InviteChannel_DefersWhenRecorderNotRunning: a channel bound to
// a camera whose recorder isn't up yet must NOT be INVITE'd. The media would
// feed an orphan hub nobody consumes while the receiver keeps draining, so no
// watchdog ever recycles the dead-wired session (the boot race where a lower
// platform hammering re-REGISTER gets its catalog-driven INVITE in before
// camera-manager startup finishes).
func TestServer_InviteChannel_DefersWhenRecorderNotRunning(t *testing.T) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)

	const chID = "34020000001320000021"
	dm.Register(&platform.Device{ID: testDeviceID, NetAddr: "127.0.0.1:9999"})
	dm.RegisterChannel(testDeviceID, &platform.Channel{DeviceID: testDeviceID, ID: chID})

	// Bound camera (fakeEnroller resolves the binding) but no recorder:
	// GB28181NALUWriter always returns nil.
	fe := &fakeEnroller{}
	_ = fe.EnsureGB28181Camera(testDeviceID, chID, "", "")
	srv.SetCameraEnroller(fe)

	err := srv.InviteChannel(testDeviceID, chID)
	require.ErrorContains(t, err, "not running")
	// Nothing half-open must be left behind.
	require.Nil(t, srv.sessionMgr.GetReceiver(chID))
}
