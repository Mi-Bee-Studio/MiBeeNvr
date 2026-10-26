package recorder

import (
	"net"
	"testing"
	"time"
)

// ResolveStreamHost policy tests (#1076): the device-advertised stream address
// wins while its RTSP port answers; only an unreachable advertised host is
// rewritten to the ONVIF endpoint's host.

// listenTCP opens a loopback listener and returns its host:port URL base.
func listenTCP(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

func TestResolveStreamHost_ReachableAdvertisedHostKept(t *testing.T) {
	addr := listenTCP(t)
	rtsp := "rtsp://" + addr + "/live0.264"
	endpoint := "http://192.0.2.1/onvif/device_service" // different host on purpose

	got, rewritten := ResolveStreamHost(rtsp, endpoint)
	if rewritten {
		t.Errorf("reachable advertised host must be kept, got rewrite to %s", got)
	}
	if got != rtsp {
		t.Errorf("got %q, want the original %q", got, rtsp)
	}
}

func TestResolveStreamHost_UnreachableAdvertisedHostRewritten(t *testing.T) {
	// The endpoint host listens; the advertised stream host does not.
	endpointAddr := listenTCP(t)
	endpoint := "http://" + endpointAddr + "/onvif/device_service"
	rtsp := "rtsp://192.0.2.99:554/live0.264"

	got, rewritten := ResolveStreamHost(rtsp, endpoint)
	if !rewritten {
		t.Fatalf("unreachable advertised host must be rewritten, kept %q", got)
	}
	// The rewrite keeps the endpoint's HOSTNAME and the stream's own port.
	want := "rtsp://" + hostOnly(endpointAddr) + ":554/live0.264"
	if got != want {
		t.Errorf("got %q, want %q (endpoint host, stream port preserved)", got, want)
	}
}

func TestResolveStreamHost_HostsAgreeUntouched(t *testing.T) {
	rtsp := "rtsp://192.0.2.50:554/live0.264"
	endpoint := "http://192.0.2.50/onvif/device_service"

	got, rewritten := ResolveStreamHost(rtsp, endpoint)
	if rewritten {
		t.Errorf("agreeing hosts must never rewrite, got %q", got)
	}
	if got != rtsp {
		t.Errorf("got %q, want %q", got, rtsp)
	}
}

func TestRtspHostReachable_NoPortDoesNotPanic(t *testing.T) {
	addr := listenTCP(t)
	// A portless URL probes :554; against the loopback listener's host this
	// simply fails — the point is no panic and a clean false.
	if rtspHostReachable("rtsp://"+hostOnly(addr)+"/live", 100*time.Millisecond) {
		t.Error("portless URL probing :554 must not report reachable here")
	}
}

func TestRtspHostReachable_MalformedURL(t *testing.T) {
	if rtspHostReachable("://bad", 100*time.Millisecond) {
		t.Error("malformed URL must not report reachable")
	}
}

func hostOnly(hostPort string) string {
	h, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return hostPort
	}
	return h
}
