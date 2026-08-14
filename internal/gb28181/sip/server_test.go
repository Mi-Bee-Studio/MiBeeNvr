package sip

import (
	"context"
	"fmt"
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

// startTestServer starts a Server on the config's address and registers a
// cleanup that stops it.
func startTestServer(t *testing.T, cfg config.GB28181ServerConfig) (*Server, *gb28181.DeviceManager) {
	t.Helper()
	dm := gb28181.NewDeviceManager(60 * time.Second)
	srv := NewServer(cfg, dm, gb28181.NewSessionManager(gb28181.NewPortManager(30000, 30100), cfg.ServerID), nil)
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
		res, ok := msg.(sip.Response)
		if !ok {
			c.t.Fatalf("roundTrip: expected response, got %T", msg)
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
	srv := NewServer(config.GB28181ServerConfig{}, gb28181.NewDeviceManager(time.Minute), gb28181.NewSessionManager(gb28181.NewPortManager(30000, 30100), ""), nil)
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
	if dev.Status.Load() != gb28181.DeviceOnline {
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
	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: "127.0.0.1:9999"})

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
	if dev.Status.Load() != gb28181.DeviceOnline {
		t.Fatalf("device status = %d, want online", dev.Status.Load())
	}
}

func TestServer_Message_Catalog(t *testing.T) {
	cfg := testConfig(t)
	_, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: "127.0.0.1:9999"})

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

	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: "127.0.0.1:9999"})

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
