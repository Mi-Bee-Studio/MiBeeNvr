package onvif

import (
	"context"
	"strings"
	"testing"

	"github.com/mickeyzzc/onvif-go/v2/discovery"
	"github.com/mickeyzzc/onvif-go/v2/wsdiscovery"
)

// The datagram vectors below are the field dialects the NVR's original
// in-tree listener was hardened against (#266/#554/#555). They now pin the
// onvif-go discovery path this package delegates to: same keep/drop
// decisions through the library's public parse + ONVIF gate, and the same
// name/hardware scope extraction through the shared mapper.

const helloONVIFTypes = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <d:Hello xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
      <a:EndpointReference xmlns:a="http://www.w3.org/2005/08/addressing"><a:Address>urn:uuid:cam-1</a:Address></a:EndpointReference>
      <d:Types>dn:NetworkVideoTransmitter</d:Types>
      <d:Scopes>onvif://www.onvif.org/name/MiBeeCam onvif://www.onvif.org/hardware/MiBeeCam</d:Scopes>
      <d:XAddrs>http://192.0.2.20:80/onvif/device_service</d:XAddrs>
      <d:MetadataVersion>1</d:MetadataVersion>
    </d:Hello>
  </s:Body>
</s:Envelope>`

const helloScopeOnly = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <d:Hello xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
      <a:EndpointReference xmlns:a="http://www.w3.org/2005/08/addressing"><a:Address>urn:uuid:cam-2</a:Address></a:EndpointReference>
      <d:Types></d:Types>
      <d:Scopes>onvif://www.onvif.org/name/IPC onvif://www.onvif.org/Profile/Streaming</d:Scopes>
      <d:XAddrs>http://192.0.2.21:80/onvif/device_service</d:XAddrs>
      <d:MetadataVersion>1</d:MetadataVersion>
    </d:Hello>
  </s:Body>
</s:Envelope>`

const helloNonONVIF = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <d:Hello xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
      <a:EndpointReference xmlns:a="http://www.w3.org/2005/08/addressing"><a:Address>urn:uuid:win-host</a:Address></a:EndpointReference>
      <d:Types>wsdiscovery:Device</d:Types>
      <d:Scopes>http://schemas.xmlsoap.org/ws/2005/04/discovery/stack</d:Scopes>
      <d:XAddrs>http://192.0.2.99:5357/wsd/</d:XAddrs>
      <d:MetadataVersion>1</d:MetadataVersion>
    </d:Hello>
  </s:Body>
</s:Envelope>`

// parseAndGate mirrors the library listener's receive path with its public
// pieces: ParseAnnouncement decodes the datagram, IsONVIFResponder applies
// the signal gate (the listener runs with the gate on by default).
func parseAndGate(t *testing.T, data string) *discovery.Device {
	t.Helper()
	match := wsdiscovery.ParseAnnouncement([]byte(data))
	if match == nil {
		return nil
	}
	dev := &discovery.Device{
		EndpointRef: match.EndpointRef,
		XAddrs:      strings.Fields(match.XAddrs),
		Types:       strings.Fields(match.Types),
		Scopes:      strings.Fields(match.Scopes),
	}
	if !discovery.IsONVIFResponder(dev) {
		return nil
	}
	return dev
}

func TestListenerEquivalence_KeepsONVIFSignals(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"types signal":    helloONVIFTypes,
		"scope-only kept": helloScopeOnly,
	} {
		dev := parseAndGate(t, body)
		if dev == nil {
			t.Fatalf("%s: ONVIF Hello must be kept", name)
		}
		mapped := MapDiscoveredDevice(dev)
		if mapped.Endpoint == "" || mapped.UUID == "" {
			t.Errorf("%s: mapped device incomplete: %+v", name, mapped)
		}
	}
}

func TestListenerEquivalence_DropsNonONVIF(t *testing.T) {
	t.Parallel()
	// The Windows/WSD zombie-host shape (#554/#555): generic Types, no ONVIF
	// scope — must be dropped at the discovery boundary.
	if dev := parseAndGate(t, helloNonONVIF); dev != nil {
		t.Fatalf("non-ONVIF Hello must be dropped, got %+v", dev)
	}
}

func TestListenerEquivalence_ByeAndGarbage(t *testing.T) {
	t.Parallel()
	bye := `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<d:Bye xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"><a:EndpointReference xmlns:a="http://www.w3.org/2005/08/addressing"><a:Address>urn:uuid:gone</a:Address></a:EndpointReference></d:Bye>
</s:Body></s:Envelope>`
	if match := wsdiscovery.ParseAnnouncement([]byte(bye)); match != nil {
		// Bye announcements never reach a device callback; the codec may or
		// may not surface them — assert they are not ONVIF-kept.
		dev := &discovery.Device{EndpointRef: match.EndpointRef, XAddrs: strings.Fields(match.XAddrs), Types: strings.Fields(match.Types), Scopes: strings.Fields(match.Scopes)}
		if discovery.IsONVIFResponder(dev) {
			t.Errorf("Bye must not pass the ONVIF gate: %+v", match)
		}
	}
	for _, garbage := range []string{"", "not xml at all", "<xml>partial"} {
		if match := wsdiscovery.ParseAnnouncement([]byte(garbage)); match != nil {
			t.Errorf("ParseAnnouncement(%q) = %+v, want nil", garbage, match)
		}
	}
}

func TestListenerEquivalence_ScopeExtraction(t *testing.T) {
	t.Parallel()
	// name/hardware scope tokens must survive the shared mapper.
	dev := parseAndGate(t, helloONVIFTypes)
	if dev == nil {
		t.Fatal("ONVIF Hello must parse")
	}
	mapped := MapDiscoveredDevice(dev)
	if mapped.Name != "MiBeeCam" {
		t.Errorf("name scope: got %q, want MiBeeCam", mapped.Name)
	}
	if mapped.Hardware != "MiBeeCam" {
		t.Errorf("hardware scope: got %q, want MiBeeCam", mapped.Hardware)
	}
}

func TestEnrichDevice_NoEndpointNoOp(t *testing.T) {
	t.Parallel()
	// A device with no endpoint and no XAddrs cannot be enriched; EnrichDevice
	// must be a no-op (not panic, not error). This is the Hello-without-XAddrs
	// edge case some firmware emits.
	dev := &DiscoveredDevice{}
	EnrichDevice(context.Background(), dev)
	if dev.Manufacturer != "" || dev.Serial != "" {
		t.Errorf("EnrichDevice mutated a device with no endpoint: %+v", dev)
	}
}
