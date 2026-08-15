package sip

import (
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/ghettovoice/gosip/sip/parser"
)

// readServerRequest reads from the client socket until a server-initiated
// REQUEST of the wanted method arrives (skipping responses to earlier calls).
func readServerRequest(t *testing.T, c *sipClient, method sip.RequestMethod) sip.Request {
	t.Helper()
	buf := make([]byte, 65535)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatalf("readServerRequest: deadline: %v", err)
		}
		n, _, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("readServerRequest: read: %v", err)
		}
		msg, err := parser.ParseMessage(buf[:n], log.NewDefaultLogrusLogger())
		if err != nil {
			continue
		}
		if req, ok := msg.(sip.Request); ok && req.Method() == method {
			return req
		}
	}
	t.Fatalf("readServerRequest: timed out waiting for %s", method)
	return nil
}

// TestServer_Message_TimeSync_Query verifies the platform answers a device
// clock query with a MANSCDP TimeSync Response carrying its wall clock.
func TestServer_Message_TimeSync_Query(t *testing.T) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	// NetAddr points at the client socket so the async TimeSync response
	// MESSAGE is receivable here.
	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: client.conn.LocalAddr().String()})

	body, err := manscdp.Encode(manscdp.TimeSyncQuery{
		CmdType:  manscdp.CmdTimeSync,
		SN:       7,
		DeviceID: testDeviceID,
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	req := buildRequest(t, sip.MESSAGE, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), string(body))
	if res := client.roundTrip(req); res.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode())
	}

	resp := readServerRequest(t, client, sip.MESSAGE)
	if rb := resp.Body(); !strings.Contains(rb, "<CmdType>TimeSync</CmdType>") || !strings.Contains(rb, "<Time>") {
		t.Fatalf("time sync response body: %s", rb)
	}
	if !strings.Contains(resp.Body(), "<SN>7</SN>") {
		t.Fatalf("SN not echoed: %s", resp.Body())
	}
	_ = srv
}

// TestServer_Notify_Catalog verifies a catalog-change NOTIFY merges channels.
func TestServer_Notify_Catalog(t *testing.T) {
	cfg := testConfig(t)
	_, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: "127.0.0.1:9999"})

	body, err := manscdp.Encode(manscdp.CatalogNotify{
		CmdType:  manscdp.CmdCatalog,
		SN:       2,
		DeviceID: testDeviceID,
		SumNum:   1,
		Item: []manscdp.Item{
			{DeviceID: "34020000001320000021", Name: "Notified Channel", Parental: 0, Status: "ON"},
		},
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	req := buildRequest(t, sip.NOTIFY, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), string(body))
	if res := client.roundTrip(req); res.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode())
	}

	channels := dm.Channels(testDeviceID)
	if len(channels) != 1 || channels[0].ID != "34020000001320000021" || channels[0].Name != "Notified Channel" {
		t.Fatalf("channels after notify: %+v", channels)
	}
}

// TestServer_Notify_Alarm verifies an alarm NOTIFY lands on the event bus and
// in the per-device ring.
func TestServer_Notify_Alarm(t *testing.T) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	bus := event.NewEventBus(16)
	srv.SetEventBus(bus)
	evtCh := make(chan event.Event, 4)
	if err := bus.Subscribe(event.TopicGB28181Alarm, evtCh, 0); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: "127.0.0.1:9999"})
	dm.RegisterChannel(testDeviceID, &gb28181.Channel{ID: "34020000001320000021"})

	body, err := manscdp.Encode(manscdp.Alarm{
		CmdType:          manscdp.CmdAlarm,
		SN:               3,
		DeviceID:         "34020000001320000021",
		AlarmPriority:    "2",
		AlarmMethod:      "5",
		AlarmTime:        "2026-08-16T10:00:00",
		AlarmDescription: "motion",
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	req := buildRequest(t, sip.NOTIFY, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), string(body))
	if res := client.roundTrip(req); res.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode())
	}

	select {
	case evt := <-evtCh:
		alarm, ok := evt.Data.(event.GB28181AlarmEvent)
		if !ok {
			t.Fatalf("event data type %T", evt.Data)
		}
		if alarm.AlarmMethod != "5" || alarm.AlarmDescription != "motion" {
			t.Fatalf("alarm event: %+v", alarm)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no alarm event published")
	}

	alarms := srv.GB28181Alarms(testDeviceID)
	if len(alarms) != 1 || alarms[0].AlarmDescription != "motion" {
		t.Fatalf("alarm ring: %+v", alarms)
	}
}

// TestServer_Notify_MobilePosition verifies position reports fill the ring.
func TestServer_Notify_MobilePosition(t *testing.T) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: "127.0.0.1:9999"})

	body, err := manscdp.Encode(manscdp.MobilePosition{
		CmdType:   manscdp.CmdMobilePosition,
		SN:        4,
		DeviceID:  testDeviceID,
		Time:      "2026-08-16T10:00:00",
		Longitude: "116.397428",
		Latitude:  "39.909230",
		Speed:     "12.5",
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	req := buildRequest(t, sip.NOTIFY, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), string(body))
	if res := client.roundTrip(req); res.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode())
	}

	positions := srv.GB28181Positions(testDeviceID)
	if len(positions) != 1 || positions[0].Longitude != "116.397428" || positions[0].Latitude != "39.909230" {
		t.Fatalf("positions: %+v", positions)
	}
}

// TestSubscriptionSubjects verifies the config toggle resolution (nil = on
// when enabled; explicit false opts out).
func TestSubscriptionSubjects(t *testing.T) {
	off, on := false, true
	s := &Server{cfg: testConfigForSubjects(true, nil, nil, false)}
	got := s.subscriptionSubjects()
	if len(got) != 2 || got[0] != manscdp.CmdCatalog || got[1] != manscdp.CmdAlarm {
		t.Fatalf("default subjects = %v", got)
	}
	s = &Server{cfg: testConfigForSubjects(true, &off, &on, true)}
	got = s.subscriptionSubjects()
	if len(got) != 2 || got[0] != manscdp.CmdAlarm || got[1] != manscdp.CmdMobilePosition {
		t.Fatalf("opt-out subjects = %v", got)
	}
	s = &Server{cfg: testConfigForSubjects(false, nil, nil, false)}
	if got := s.subscriptionSubjects(); len(got) != 0 {
		t.Fatalf("disabled server subjects = %v", got)
	}
}

func testConfigForSubjects(enabled bool, catalog, alarm *bool, mobilePos bool) config.GB28181ServerConfig {
	return config.GB28181ServerConfig{
		Enabled:                 enabled,
		SubscribeCatalog:        catalog,
		SubscribeAlarm:          alarm,
		SubscribeMobilePosition: mobilePos,
	}
}

// TestSDPAudioAddress verifies the talk answer SDP parser.
func TestSDPAudioAddress(t *testing.T) {
	host, port, ok := sdpAudioAddress([]byte("v=0\r\no=x 0 0 IN IP4 192.168.1.10\r\ns=Play\r\nc=IN IP4 192.168.1.10\r\nt=0 0\r\nm=audio 15062 RTP/AVP 8\r\na=recvonly\r\n"))
	if !ok || host != "192.168.1.10" || port != 15062 {
		t.Fatalf("audio addr = %s:%d ok=%v", host, port, ok)
	}
	if _, _, ok := sdpAudioAddress([]byte("v=0\r\nc=IN IP4 192.168.1.10\r\nm=video 15060 RTP/AVP 96\r\n")); ok {
		t.Fatalf("video SDP must not parse as audio")
	}
	if _, _, ok := sdpAudioAddress([]byte("v=0\r\n")); ok {
		t.Fatalf("empty SDP must not parse")
	}
}
