package dlna

import (
	"bytes"
	"encoding/xml"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMSearch_Valid(t *testing.T) {
	req := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"ST: urn:schemas-upnp-org:service:ContentDirectory:1\r\n" +
		"MX: 3\r\n\r\n"
	st, ok := parseMSearch([]byte(req))
	require.True(t, ok)
	require.Equal(t, "urn:schemas-upnp-org:service:ContentDirectory:1", st)
}

func TestParseMSearch_HeaderCaseInsensitive(t *testing.T) {
	req := "m-search * HTTP/1.1\r\nhost: 239.255.255.250:1900\r\nman: \"ssdp:discover\"\nst: ssdp:all\r\n\r\n"
	st, ok := parseMSearch([]byte(req))
	require.True(t, ok)
	require.Equal(t, "ssdp:all", st)
}

func TestParseMSearch_RejectsNonDiscover(t *testing.T) {
	notify := "NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nNT: upnp:rootdevice\r\n\r\n"
	_, ok := parseMSearch([]byte(notify))
	require.False(t, ok)

	noMan := "M-SEARCH * HTTP/1.1\r\nST: ssdp:all\r\n\r\n"
	_, ok = parseMSearch([]byte(noMan))
	require.False(t, ok)
}

func TestBuildMSearchResponse(t *testing.T) {
	msg := string(buildMSearchResponse("upnp:rootdevice", "uuid:x::upnp:rootdevice", "http://192.0.2.10:49152/dlna/device.xml", "srv/1.0"))
	require.Contains(t, msg, "HTTP/1.1 200 OK\r\n")
	require.Contains(t, msg, "ST: upnp:rootdevice\r\n")
	require.Contains(t, msg, "USN: uuid:x::upnp:rootdevice\r\n")
	require.Contains(t, msg, "LOCATION: http://192.0.2.10:49152/dlna/device.xml\r\n")
	require.Contains(t, msg, "EXT:\r\n")
	require.Contains(t, msg, "CACHE-CONTROL: max-age=1800\r\n")
}

func TestBuildNotifyAliveAndByebye(t *testing.T) {
	alive := string(buildNotifyAlive("upnp:rootdevice", "uuid:x::upnp:rootdevice", "http://192.0.2.10:49152/dlna/device.xml", "srv/1.0"))
	require.Contains(t, alive, "NOTIFY * HTTP/1.1\r\n")
	require.Contains(t, alive, "NTS: ssdp:alive\r\n")
	require.Contains(t, alive, "NT: upnp:rootdevice\r\n")

	bye := string(buildNotifyByebye("upnp:rootdevice", "uuid:x::upnp:rootdevice"))
	require.Contains(t, bye, "NTS: ssdp:byebye\r\n")
	require.NotContains(t, bye, "LOCATION")
}

func TestSSDPSearchTargets_CoverDeviceAndServices(t *testing.T) {
	targets := ssdpSearchTargets("uuid:abc")
	require.Len(t, targets, 5)
	require.Equal(t, "upnp:rootdevice", targets[0][0])
	require.Equal(t, "uuid:abc", targets[1][0])
	require.Equal(t, "urn:schemas-upnp-org:device:MediaServer:1", targets[2][0])
	require.Equal(t, "urn:schemas-upnp-org:service:ContentDirectory:1", targets[3][0])
	require.Equal(t, "urn:schemas-upnp-org:service:ConnectionManager:1", targets[4][0])
}

func TestSSDPInterfaces_UnknownNameFails(t *testing.T) {
	_, err := ssdpInterfaces([]string{"definitely-not-an-interface-xyz"})
	require.Error(t, err)
}

func TestUDN_StableAndWellFormed(t *testing.T) {
	a, b := UDN("host|root"), UDN("host|root")
	require.Equal(t, a, b)
	require.NotEqual(t, a, UDN("other|root"))
	require.True(t, strings.HasPrefix(a, "uuid:"))
	// 8-4-4-4-12 hex layout (5 groups = 4 dashes)
	hex := strings.TrimPrefix(a, "uuid:")
	require.Equal(t, 4, strings.Count(hex, "-"))
}

func TestDeviceDescription_RequiredElements(t *testing.T) {
	doc := string(deviceDescription("Living Room <NVR>", "uuid:abc", "http://192.0.2.10:49152"))
	require.Contains(t, doc, "urn:schemas-upnp-org:device:MediaServer:1")
	require.Contains(t, doc, "Living Room &lt;NVR&gt;") // title escaped
	require.Contains(t, doc, "<UDN>uuid:abc</UDN>")
	require.Contains(t, doc, "<URLBase>http://192.0.2.10:49152</URLBase>")
	require.Contains(t, doc, "/dlna/ContentDirectory/scpd.xml")
	require.Contains(t, doc, "/dlna/ContentDirectory/control")
	require.Contains(t, doc, "/dlna/ConnectionManager/control")
	requireWellFormedXML(t, doc)
}

func TestSCPDs_WellFormed(t *testing.T) {
	requireWellFormedXML(t, string(scpdContentDirectory()))
	requireWellFormedXML(t, string(scpdConnectionManager()))
	require.Contains(t, string(scpdContentDirectory()), "<name>Browse</name>")
}

func TestDIDL_ItemAndContainer(t *testing.T) {
	doc := didlDocument([]string{
		didlContainer("0", "-1", "Root & Sons", "2", ""),
		didlItem("R1", "0", "12:00:00", "2026-10-01T12:00:00Z", didlResource{
			protocolInfo: "http-get:*:video/mp4:*",
			itemClass:    "object.item.videoItem",
			url:          "http://192.0.2.10:49152/dlna/item/r1",
			size:         1234,
			durationSec:  3661,
		}),
	})
	require.Contains(t, doc, `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"`)
	require.Contains(t, doc, "Root &amp; Sons")
	require.Contains(t, doc, `size="1234"`)
	require.Contains(t, doc, `duration="1:01:01"`)
	require.Contains(t, doc, ">http://192.0.2.10:49152/dlna/item/r1<")
	requireWellFormedXML(t, doc)
}

func TestMimeForFormat(t *testing.T) {
	video, class := mimeForFormat("h264")
	require.Equal(t, "video/mp4", video)
	require.Equal(t, "object.item.videoItem", class)
	video, _ = mimeForFormat("timelapse")
	require.Equal(t, "video/mp4", video)
	video, _ = mimeForFormat("mjpeg")
	require.Equal(t, "video/avi", video)
	audio, class := mimeForFormat("audio")
	require.Equal(t, "audio/mp4", audio)
	require.Equal(t, "object.item.audioItem.musicTrack", class)
}

func TestParseBrowse_NamespacedSOAP(t *testing.T) {
	body := `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:Browse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1">
      <ObjectID>0</ObjectID>
      <BrowseFlag>BrowseDirectChildren</BrowseFlag>
      <Filter>*</Filter>
      <StartingIndex>0</StartingIndex>
      <RequestedCount>10</RequestedCount>
      <SortCriteria></SortCriteria>
    </u:Browse>
  </s:Body>
</s:Envelope>`
	args, err := parseBrowse([]byte(body))
	require.NoError(t, err)
	require.Equal(t, "0", args.ObjectID)
	require.Equal(t, "BrowseDirectChildren", args.BrowseFlag)
	require.Equal(t, 0, args.StartingIndex)
	require.Equal(t, 10, args.RequestedCount)
}

func TestParseBrowse_EmptyBody(t *testing.T) {
	_, err := parseBrowse([]byte("<s:Envelope/>"))
	require.Error(t, err)
}

func TestSoapActionName(t *testing.T) {
	require.Equal(t, "Browse", soapActionName(`"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"`))
	require.Equal(t, "GetProtocolInfo", soapActionName("urn:schemas-upnp-org:service:ConnectionManager:1#GetProtocolInfo"))
}

func TestSoapFault_WellFormed(t *testing.T) {
	requireWellFormedXML(t, string(soapFault(701, "no such object")))
}

func TestPaginate(t *testing.T) {
	f, to := paginate(10, 0, 0) // no count → all
	require.Equal(t, 0, f)
	require.Equal(t, 10, to)
	f, to = paginate(10, 2, 3)
	require.Equal(t, 2, f)
	require.Equal(t, 5, to)
	f, to = paginate(10, 10, 5)
	require.Equal(t, 10, f)
	require.Equal(t, 10, to)
	f, to = paginate(10, 3, 100)
	require.Equal(t, 3, f)
	require.Equal(t, 10, to)
}

func TestFmtDuration(t *testing.T) {
	require.Equal(t, "0:00:00", fmtDuration(0))
	require.Equal(t, "0:01:05", fmtDuration(65))
	require.Equal(t, "1:01:01", fmtDuration(3661))
	require.Equal(t, "0:00:00", fmtDuration(-5))
}

// requireWellFormedXML asserts the document parses as XML at all — renderers
// reject malformed envelopes before reading a single element.
func requireWellFormedXML(t *testing.T, doc string) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader([]byte(doc)))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return
			}
			t.Fatalf("XML not well-formed: %v", err)
		}
	}
}

func TestStopWithoutStart_Idempotent(t *testing.T) {
	srv := NewServer(ServerConfig{}, nil, nil, "k")
	require.NoError(t, srv.Stop())
	require.NoError(t, srv.Stop()) // stopOnce guards double close
}

func TestLocationFor_PrefersSubnetMatch(t *testing.T) {
	lo, err := net.InterfaceByName(firstNonLoopbackInterface(t))
	require.NoError(t, err)
	srv := NewServer(ServerConfig{Port: 49152}, nil, nil, "k")
	// Subnet-contained source picks the matching interface's address.
	loc := srv.locationFor([]*net.Interface{lo}, &net.UDPAddr{IP: ipv4Addr(*lo)})
	require.Contains(t, loc, ":49152/dlna/device.xml")
	require.Contains(t, loc, "http://")

	// No interfaces → empty (reader gets no answer).
	require.Empty(t, srv.locationFor(nil, nil))
}

func firstNonLoopbackInterface(t *testing.T) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagLoopback == 0 && ipv4Addr(ifi) != nil {
			return ifi.Name
		}
	}
	t.Skip("no non-loopback IPv4 interface available")
	return ""
}
