package onvif

import (
	"context"
	"testing"
)

// realESP32Hello is a real WS-Discovery Hello message captured from the
// ai-thinker-esp32-cam firmware (onvif_discovery.c build_hello_message output),
// with the IP redacted. This is the exact payload the HelloListener must parse
// in production — keeping a real sample guards against parser regressions that
// a hand-rolled minimal fixture might miss (namespace prefixes, scope formats).
const realESP32Hello = `<?xml version="1.0" encoding="UTF-8"?><soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:wsd="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:tns="http://www.onvif.org/ver10/network/wsdl"><soap:Header><wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Hello</wsa:Action><wsa:MessageID>urn:uuid:f472b01e-0000-1000-8000-c82e1845d868</wsa:MessageID><wsa:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</wsa:To></soap:Header><soap:Body><wsd:Hello><wsa:EndpointReference><wsa:Address>urn:uuid:f472b01e-0000-1000-8000-c82e1845d868</wsa:Address></wsa:EndpointReference><wsd:Types>tns:NetworkVideoTransmitter</wsd:Types><wsd:Scopes>onvif://www.onvif.org/type/video_encoder onvif://www.onvif.org/type/NetworkVideoTransmitter onvif://www.onvif.org/hardware/MiBeeCam onvif://www.onvif.org/name/MiBeeCam onvif://www.onvif.org/Profile/Streaming</wsd:Scopes><wsd:XAddrs>http://192.168.63.140:80/onvif/device_service</wsd:XAddrs><wsd:MetadataVersion>2</wsd:MetadataVersion></wsd:Hello></soap:Body></soap:Envelope>`

// realProbeMatches is a real ProbeMatches response (from a generic IPC) that
// arrives on the shared multicast socket when another client issues a Probe.
// The listener must also accept these as a discovery source.
const realProbeMatches = `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing"><s:Body><ProbeMatches xmlns="http://schemas.xmlsoap.org/ws/2005/04/discovery"><ProbeMatch><a:EndpointReference><a:Address>urn:uuid:abc123</a:Address></a:EndpointReference><Types>dp0:NetworkVideoTransmitter</Types><Scopes>onvif://www.onvif.org/name/IPC onvif://www.onvif.org/hardware/IPC</Scopes><XAddrs>http://192.168.1.50/onvif/device_service</XAddrs><MetadataVersion>1</MetadataVersion></ProbeMatch></ProbeMatches></s:Body></s:Envelope>`

func TestParseWSDMessage_Hello(t *testing.T) {
	t.Helper()
	dev := parseWSDMessage([]byte(realESP32Hello))
	if dev == nil {
		t.Fatal("parseWSDMessage returned nil for a valid Hello")
	}
	if dev.Endpoint != "http://192.168.63.140:80/onvif/device_service" {
		t.Errorf("Endpoint = %q, want the device_service XAddr", dev.Endpoint)
	}
	if dev.UUID != "urn:uuid:f472b01e-0000-1000-8000-c82e1845d868" {
		t.Errorf("UUID = %q", dev.UUID)
	}
	if dev.Name != "MiBeeCam" {
		t.Errorf("Name = %q, want MiBeeCam (from onvif://www.onvif.org/name/MiBeeCam)", dev.Name)
	}
	if dev.Hardware != "MiBeeCam" {
		t.Errorf("Hardware = %q, want MiBeeCam", dev.Hardware)
	}
	if len(dev.Scopes) != 5 {
		t.Errorf("Scopes len = %d, want 5", len(dev.Scopes))
	}
}

func TestParseWSDMessage_ProbeMatches(t *testing.T) {
	t.Helper()
	dev := parseWSDMessage([]byte(realProbeMatches))
	if dev == nil {
		t.Fatal("parseWSDMessage returned nil for a valid ProbeMatches")
	}
	if dev.Endpoint != "http://192.168.1.50/onvif/device_service" {
		t.Errorf("Endpoint = %q", dev.Endpoint)
	}
	if dev.Name != "IPC" {
		t.Errorf("Name = %q, want IPC", dev.Name)
	}
}

func TestParseWSDMessage_ByeIgnored(t *testing.T) {
	t.Helper()
	// A Bye message (device going offline) should not be treated as a discovery.
	// We ignore it for now — returning nil lets the caller skip it cleanly.
	const bye = `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><Bye xmlns="http://schemas.xmlsoap.org/ws/2005/04/discovery"><EndpointReference><Address>urn:uuid:bye</Address></EndpointReference></Bye></s:Body></s:Envelope>`
	if dev := parseWSDMessage([]byte(bye)); dev != nil {
		t.Errorf("Bye should be ignored, got %+v", dev)
	}
}

// windowsWSDHello mirrors the WS-Discovery Hello a Windows/WSD host sends when
// it joins the network: Types "wsdiscovery:Device", vendor scopes, an XAddr on
// :5000/wsd/. Before #554 these auto-enrolled as camera shells that could
// never connect (the mickeybeessd/mickeybeehome zombies on M5).
const windowsWSDHello = `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><Hello xmlns="http://schemas.xmlsoap.org/ws/2005/04/discovery"><EndpointReference><Address>urn:uuid:a5d3d2d8-41ed-4a64-8c8b-0cc9cdbe62f8</Address></EndpointReference><Types>wsdiscovery:Device</Types><Scopes>http://schemas.xmlsoap.org/ws/2005/04/discovery/contract</Scopes><XAddrs>http://mickeybeessd:5000/wsd/a5d3d2d8-41ed-4a64-8c8b-0cc9cdbe62f8</XAddrs><MetadataVersion>1</MetadataVersion></Hello></s:Body></s:Envelope>`

func TestParseWSDMessage_NonONVIFHelloDropped(t *testing.T) {
	t.Helper()
	if dev := parseWSDMessage([]byte(windowsWSDHello)); dev != nil {
		t.Errorf("non-ONVIF WSD Hello must be dropped at the parse boundary (#554), got %+v", dev)
	}
}

func TestParseWSDMessage_NeitherSignalDropped(t *testing.T) {
	t.Helper()
	// No Types and no ONVIF scope: a responder that advertises nothing ONVIF-ish.
	const neither = `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><Hello xmlns="http://schemas.xmlsoap.org/ws/2005/04/discovery"><EndpointReference><Address>urn:uuid:x</Address></EndpointReference><XAddrs>http://192.168.1.99:80/onvif/device_service</XAddrs></Hello></s:Body></s:Envelope>`
	if dev := parseWSDMessage([]byte(neither)); dev != nil {
		t.Errorf("Hello without any ONVIF signal must be dropped, got %+v", dev)
	}
}

func TestParseWSDMessage_ScopeOnlySignalKept(t *testing.T) {
	t.Helper()
	// Marginal implementations may leave Types empty but populate ONVIF scopes;
	// matching either signal keeps the device (permissive gate, mirrors #266).
	const scopeOnly = `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><Hello xmlns="http://schemas.xmlsoap.org/ws/2005/04/discovery"><EndpointReference><Address>urn:uuid:y</Address></EndpointReference><Scopes>onvif://www.onvif.org/name/MarginalCam</Scopes><XAddrs>http://192.168.1.98:80/onvif/device_service</XAddrs></Hello></s:Body></s:Envelope>`
	dev := parseWSDMessage([]byte(scopeOnly))
	if dev == nil {
		t.Fatal("Hello with an ONVIF scope but empty Types must be kept")
	}
	if dev.Name != "MarginalCam" {
		t.Errorf("Name = %q, want MarginalCam", dev.Name)
	}
}

func TestParseWSDMessage_GarbageReturnsNil(t *testing.T) {
	t.Helper()
	cases := [][]byte{
		nil,
		{},
		[]byte("not xml at all"),
		[]byte("<xml>partial"),
	}
	for _, tc := range cases {
		if dev := parseWSDMessage(tc); dev != nil {
			t.Errorf("parseWSDMessage(%q) = %+v, want nil", tc, dev)
		}
	}
}

func TestExtractScopeInfo(t *testing.T) {
	t.Helper()
	cases := []struct {
		scopes       string
		wantName     string
		wantHardware string
	}{
		{
			scopes:       "onvif://www.onvif.org/name/MiBeeCam onvif://www.onvif.org/hardware/MiBeeCam",
			wantName:     "MiBeeCam",
			wantHardware: "MiBeeCam",
		},
		{
			scopes:       "onvif://www.onvif.org/name/IPC onvif://www.onvif.org/hardware/IPC onvif://www.onvif.org/Profile/Streaming",
			wantName:     "IPC",
			wantHardware: "IPC",
		},
		{
			scopes:       "",
			wantName:     "",
			wantHardware: "",
		},
		{
			// Only name present, no hardware.
			scopes:       "onvif://www.onvif.org/name/FrontDoor",
			wantName:     "FrontDoor",
			wantHardware: "",
		},
	}
	for _, tc := range cases {
		name, hardware := extractScopeInfo(tc.scopes)
		if name != tc.wantName || hardware != tc.wantHardware {
			t.Errorf("extractScopeInfo(%q) = (%q, %q), want (%q, %q)",
				tc.scopes, name, hardware, tc.wantName, tc.wantHardware)
		}
	}
}

func TestEnrichDevice_NoEndpointNoOp(t *testing.T) {
	t.Helper()
	// A device with no endpoint and no XAddrs cannot be enriched; EnrichDevice
	// must be a no-op (not panic, not error). This is the Hello-without-XAddrs
	// edge case some firmware emits.
	dev := &DiscoveredDevice{}
	EnrichDevice(context.Background(), dev)
	if dev.Manufacturer != "" || dev.Serial != "" {
		t.Errorf("EnrichDevice mutated a device with no endpoint: %+v", dev)
	}
}
