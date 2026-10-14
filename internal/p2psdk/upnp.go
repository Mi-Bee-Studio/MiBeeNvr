package peer

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Minimal UPnP IGD client, stdlib only (ported from the linktest toolkit,
// where it is field-tested on OpenWrt/iStoreOS miniupnpd): SSDP discovery →
// device description → WANIPConnection/WANPPPConnection SOAP. Purpose:
// AdvertiseAddr "auto" asks the gateway for a UDP mapping on a fixed port —
// the mapping doubles as the WAN firewall pinhole on OpenWrt, giving
// consumer-router deployments a zero-config direct-connection path.

const (
	ssdpAddr     = "239.255.255.250:1900"
	ssdpMXSec    = 2
	upnpDescName = "mibee-sdk"
)

// ssdpTargets: several ST values — devices answer inconsistently.
var ssdpTargets = []string{
	"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
	"urn:schemas-upnp-org:device:WANConnectionDevice:1",
	"upnp:rootdevice",
}

// DiscoverGatewayUPnP SSDP-discovers an IGD on any local interface. Routers
// run miniupnpd on the LAN side, so the M-SEARCH must egress every up
// interface (the default route may point at the WAN, where no IGD listens).
// ssdpProbe is one interface-bound SSDP socket with its gateway-facing
// local address.
type ssdpProbe struct {
	sock net.PacketConn
	lip  string
}

// ssdpProbes opens one UDP socket per up IPv4 interface.
func ssdpProbes() ([]ssdpProbe, error) {
	var probes []ssdpProbe
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs() //nolint:errcheck // best effort per interface
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
				continue
			}
			sock, lerr := net.ListenPacket("udp4", ipn.IP.To4().String()+":0")
			if lerr != nil {
				continue
			}
			probes = append(probes, ssdpProbe{sock, ipn.IP.To4().String()})
		}
	}
	return probes, nil
}

// ssdpFanOut sends an M-SEARCH for every target ST from every probe socket.
func ssdpFanOut(probes []ssdpProbe) error {
	dst, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return err
	}
	var searches []string
	for _, st := range ssdpTargets {
		searches = append(searches, "M-SEARCH * HTTP/1.1\r\n"+
			"HOST: "+ssdpAddr+"\r\n"+
			"MAN: \"ssdp:discover\"\r\n"+
			fmt.Sprintf("MX: %d\r\n", ssdpMXSec)+
			"ST: "+st+"\r\n")
	}
	for _, p := range probes {
		for _, s := range searches {
			_, _ = p.sock.WriteTo([]byte(s), dst) //nolint:errcheck // best effort fan-out
		}
	}
	return nil
}

// DiscoverGatewayUPnP SSDP-discovers an InternetGatewayDevice on any local
// interface and returns a SOAP client for its WAN connection service. See the
// package comment for why every interface must be probed.
func DiscoverGatewayUPnP(timeout time.Duration) (*UPnPGateway, error) {
	probes, err := ssdpProbes()
	if err != nil || len(probes) == 0 {
		return nil, errors.New("upnp: no usable IPv4 interface for SSDP")
	}
	defer func() {
		for _, p := range probes {
			_ = p.sock.Close()
		}
	}()
	if err := ssdpFanOut(probes); err != nil {
		return nil, err
	}

	buf := make([]byte, 4096)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, p := range probes {
			_ = p.sock.SetReadDeadline(deadline)
			n, _, rerr := p.sock.ReadFrom(buf)
			if rerr != nil || n == 0 {
				continue
			}
			location := parseSSDPLocation(string(buf[:n]))
			if location == "" {
				continue
			}
			gw, gerr := newIGDClient(location, p.lip, timeout)
			if gerr == nil {
				return gw, nil
			}
		}
	}
	return nil, errors.New("upnp: SSDP found no InternetGatewayDevice (service off or unsupported gateway)")
}

func parseSSDPLocation(resp string) string {
	for _, line := range strings.Split(resp, "\r\n") {
		if len(line) > 10 && strings.EqualFold(line[:9], "LOCATION:") {
			return strings.TrimSpace(line[9:])
		}
	}
	return ""
}

// ── device description walk ────────────────────────────────────────────────

type upnpService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

type upnpDevice struct {
	ServiceList struct {
		Service []upnpService `xml:"service"`
	} `xml:"serviceList"`
	DeviceList struct {
		Device []upnpDevice `xml:"device"` // encoding/xml resolves recursion
	} `xml:"deviceList"`
}

type upnpRoot struct {
	URLBase string     `xml:"URLBase"`
	Device  upnpDevice `xml:"device"`
}

// WAN service types we can drive (v1/v2 IP + PPP — home routers vary).
var wantedServiceTypes = []string{
	"urn:schemas-upnp-org:service:WANIPConnection:2",
	"urn:schemas-upnp-org:service:WANIPConnection:1",
	"urn:schemas-upnp-org:service:WANPPPConnection:1",
}

// UPnPGateway is a SOAP client for one IGD's WAN connection service.
type UPnPGateway struct {
	controlURL  string
	serviceType string
	internalIP  string // gateway-facing local address (mapping target)
	hc          *http.Client
}

func newIGDClient(location, localIP string, timeout time.Duration) (*UPnPGateway, error) {
	base, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: timeout}
	resp, err := hc.Get(location) //nolint:noctx // discovery probe, bounded by client timeout
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // probe teardown
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var root upnpRoot
	if err := xml.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	svc := findWANService(root.Device)
	if svc == nil {
		return nil, errors.New("upnp: device description has no WANIP/WANPPPConnection service")
	}
	cu, err := base.Parse(svc.ControlURL)
	if err != nil {
		return nil, err
	}
	if root.URLBase != "" {
		if ub, err := url.Parse(root.URLBase); err == nil {
			if ref, err := ub.Parse(svc.ControlURL); err == nil {
				cu = ref
			}
		}
	}
	return &UPnPGateway{
		controlURL:  cu.String(),
		serviceType: svc.ServiceType,
		internalIP:  localIP,
		hc:          hc,
	}, nil
}

func findWANService(d upnpDevice) *upnpService {
	var found []*upnpService
	var walk func(dev upnpDevice)
	walk = func(dev upnpDevice) {
		for i := range dev.ServiceList.Service {
			found = append(found, &dev.ServiceList.Service[i])
		}
		for _, sub := range dev.DeviceList.Device {
			walk(sub)
		}
	}
	walk(d)
	for _, want := range wantedServiceTypes {
		for _, s := range found {
			if s.ServiceType == want {
				return s
			}
		}
	}
	return nil
}

// ── SOAP actions ───────────────────────────────────────────────────────────

func (g *UPnPGateway) soap(action string, inner string) (string, error) {
	envelope := "<?xml version=\"1.0\"?>" +
		"<s:Envelope xmlns:s=\"http://schemas.xmlsoap.org/soap/envelope/\" " +
		"s:encodingStyle=\"http://schemas.xmlsoap.org/soap/encoding/\">" +
		"<s:Body>" + inner + "</s:Body></s:Envelope>"
	//nolint:noctx // discovery/action probe, bounded by client timeout
	req, err := http.NewRequest(http.MethodPost, g.controlURL, strings.NewReader(envelope))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", fmt.Sprintf(`"%s#%s"`, g.serviceType, action))
	resp, err := g.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // probe teardown
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return string(body), fmt.Errorf("upnp SOAP %s → HTTP %d: %s", action, resp.StatusCode, upnpErrorCode(string(body)))
	}
	return string(body), nil
}

func upnpErrorCode(body string) string {
	i := strings.Index(body, "<errorCode>")
	if i < 0 {
		return shorten(body, 120)
	}
	rest := body[i+len("<errorCode>"):]
	if j := strings.Index(rest, "<"); j >= 0 {
		return "errorCode " + rest[:j]
	}
	return "errorCode?"
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ExternalIP asks the IGD for its current WAN address.
func (g *UPnPGateway) ExternalIP() (string, error) {
	body, err := g.soap("GetExternalIPAddress",
		fmt.Sprintf("<u:GetExternalIPAddress xmlns:u=%q/>", g.serviceType))
	if err != nil {
		return "", err
	}
	i := strings.Index(body, "<NewExternalIPAddress>")
	if i < 0 {
		return "", errors.New("upnp: response lacks NewExternalIPAddress")
	}
	rest := body[i+len("<NewExternalIPAddress>"):]
	j := strings.Index(rest, "<")
	if j < 0 {
		return "", errors.New("upnp: malformed response")
	}
	ip := strings.TrimSpace(rest[:j])
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("upnp: non-IP answer %q", ip)
	}
	return ip, nil
}

// AddUDPMapping creates/refreshes a UDP port mapping (upsert semantics on
// miniupnpd). Lease 0 = permanent on gateways that reject finite leases
// (UPnP error 725).
func (g *UPnPGateway) AddUDPMapping(extPort, intPort int, leaseSec int) error {
	mapping := func(lease int) string {
		return fmt.Sprintf(
			"<u:AddPortMapping xmlns:u=\"%s\">"+
				"<NewRemoteHost></NewRemoteHost>"+
				"<NewExternalPort>%d</NewExternalPort>"+
				"<NewProtocol>UDP</NewProtocol>"+
				"<NewInternalPort>%d</NewInternalPort>"+
				"<NewInternalClient>%s</NewInternalClient>"+
				"<NewEnabled>1</NewEnabled>"+
				"<NewPortMappingDescription>%s</NewPortMappingDescription>"+
				"<NewLeaseDuration>%d</NewLeaseDuration>"+
				"</u:AddPortMapping>",
			g.serviceType, extPort, intPort, g.internalIP, upnpDescName, lease)
	}
	_, err := g.soap("AddPortMapping", mapping(leaseSec))
	if err != nil && strings.Contains(err.Error(), "725") {
		_, err = g.soap("AddPortMapping", mapping(0))
	}
	return err
}
