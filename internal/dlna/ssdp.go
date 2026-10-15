// Package dlna implements an optional UPnP AV / DLNA media server: SSDP
// discovery (M-SEARCH responses + periodic alive/byebye NOTIFYs), the device
// description with ContentDirectory:1 and ConnectionManager:1, and a content
// tree that maps cameras → day containers → recording items whose res URLs
// serve the MP4/AVI files straight from the recording store. Default off,
// bound only to the configured LAN interfaces — never across relay/p2p
// tunnels (#970).
package dlna

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// SSDP multicast endpoint (IPv4 site-local administration scope).
const ssdpAddr = "239.255.255.250:1900"

// ssdpMaxAge is the CACHE-CONTROL max-age advertised in alive NOTIFYs and
// M-SEARCH responses; announcements repeat at roughly half that interval.
const ssdpMaxAge = 1800

// ssdpSearchTargets returns the (ST/NT, USN) pairs a media server with the
// given UDN answers for, in the order the UPnP Device Architecture requires
// root device, device UUID, device type, then each service type.
func ssdpSearchTargets(udn string) [][2]string {
	return [][2]string{
		{"upnp:rootdevice", udn + "::upnp:rootdevice"},
		{udn, udn},
		{"urn:schemas-upnp-org:device:MediaServer:1", udn + "::urn:schemas-upnp-org:device:MediaServer:1"},
		{"urn:schemas-upnp-org:service:ContentDirectory:1", udn + "::urn:schemas-upnp-org:service:ContentDirectory:1"},
		{"urn:schemas-upnp-org:service:ConnectionManager:1", udn + "::urn:schemas-upnp-org:service:ConnectionManager:1"},
	}
}

// buildMSearchResponse renders a unicast 200 OK answering an M-SEARCH for st.
func buildMSearchResponse(st, usn, location, server string) []byte {
	var b bytes.Buffer
	b.WriteString("HTTP/1.1 200 OK\r\n")
	b.WriteString("CACHE-CONTROL: max-age=" + strconv.Itoa(ssdpMaxAge) + "\r\n")
	b.WriteString("DATE: " + time.Now().UTC().Format(httpTimeFormat) + "\r\n")
	b.WriteString("EXT:\r\n")
	b.WriteString("LOCATION: " + location + "\r\n")
	b.WriteString("SERVER: " + server + "\r\n")
	b.WriteString("ST: " + st + "\r\n")
	b.WriteString("USN: " + usn + "\r\n")
	b.WriteString("OPT: \"http://schemas.upnp.org/upnp/1/0/\"; ns=01\r\n")
	b.WriteString("01-BOOTID.UPNP.ORG: 1\r\n")
	b.WriteString("CONFIGID.UPNP.ORG: 1\r\n") // UDA 1.1 header: no config update support
	b.WriteString("\r\n")
	return b.Bytes()
}

// buildNotifyAlive renders an ssdp:alive advertisement for nt.
func buildNotifyAlive(nt, usn, location, server string) []byte {
	var b bytes.Buffer
	b.WriteString("NOTIFY * HTTP/1.1\r\n")
	b.WriteString("HOST: " + ssdpAddr + "\r\n")
	b.WriteString("CACHE-CONTROL: max-age=" + strconv.Itoa(ssdpMaxAge) + "\r\n")
	b.WriteString("LOCATION: " + location + "\r\n")
	b.WriteString("NT: " + nt + "\r\n")
	b.WriteString("NTS: ssdp:alive\r\n")
	b.WriteString("SERVER: " + server + "\r\n")
	b.WriteString("USN: " + usn + "\r\n")
	b.WriteString("BOOTID.UPNP.ORG: 1\r\n")
	b.WriteString("CONFIGID.UPNP.ORG: 1\r\n")
	b.WriteString("\r\n")
	return b.Bytes()
}

// buildNotifyByebye renders an ssdp:byebye advertisement for nt.
func buildNotifyByebye(nt, usn string) []byte {
	var b bytes.Buffer
	b.WriteString("NOTIFY * HTTP/1.1\r\n")
	b.WriteString("HOST: " + ssdpAddr + "\r\n")
	b.WriteString("NT: " + nt + "\r\n")
	b.WriteString("NTS: ssdp:byebye\r\n")
	b.WriteString("USN: " + usn + "\r\n")
	b.WriteString("\r\n")
	return b.Bytes()
}

// parseMSearch extracts the ST of an M-SEARCH request. SSDP traffic is
// header-case-insensitive; only requests with MAN "ssdp:discover" get
// answered. ok is false for anything else (NOTIFYs, stray HTTP, malformed).
func parseMSearch(data []byte) (st string, ok bool) {
	r := bytes.NewReader(data)
	br := bufio.NewReader(r)
	reqLine, err := br.ReadString('\n')
	if err != nil {
		return "", false
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(reqLine)), "M-SEARCH") {
		return "", false
	}
	headers := readHeaders(br)
	man := headers.get("MAN")
	if !strings.Contains(man, "ssdp:discover") {
		return "", false
	}
	return headers.get("ST"), true
}

// headerMap is a case-insensitive SSDP/HTTP header set.
type headerMap map[string]string

func readHeaders(br *bufio.Reader) headerMap {
	h := headerMap{}
	for {
		line, err := br.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return h
		}
		if i := strings.Index(trimmed, ":"); i > 0 {
			h[strings.ToUpper(strings.TrimSpace(trimmed[:i]))] = strings.TrimSpace(trimmed[i+1:])
		}
		if err != nil {
			return h
		}
	}
}

func (h headerMap) get(name string) string {
	return h[strings.ToUpper(name)]
}

// ssdpInterfaces resolves the interfaces the server should announce on.
// names empty = auto: every non-loopback IPv4 interface that is up and
// multicast-capable. Named interfaces are matched by name or address;
// a name that resolves to nothing fails the whole resolution — an explicitly
// misconfigured dlna section should be loud, not silently broad.
func ssdpInterfaces(names []string) ([]*net.Interface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		var out []*net.Interface
		for _, ifi := range all {
			if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagMulticast == 0 {
				continue
			}
			if ipv4Addr(ifi) == nil {
				continue
			}
			out = append(out, &ifi)
		}
		return out, nil
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[strings.TrimSpace(n)] = true
	}
	var out []*net.Interface
	for _, ifi := range all {
		if !want[ifi.Name] {
			continue
		}
		if ipv4Addr(ifi) == nil {
			return nil, fmt.Errorf("dlna: interface %s has no IPv4 address", ifi.Name)
		}
		out = append(out, &ifi)
	}
	if len(out) != len(want) {
		return nil, fmt.Errorf("dlna: unknown interface(s) in dlna.interfaces (resolved %d of %d)", len(out), len(want))
	}
	return out, nil
}

// ipv4Addr returns the first IPv4 unicast address of an interface.
func ipv4Addr(ifi net.Interface) net.IP {
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 != nil && !ip4.IsLoopback() {
			return ip4
		}
	}
	return nil
}

const httpTimeFormat = "Mon, 02 Jan 2006 15:04:05 GMT"
