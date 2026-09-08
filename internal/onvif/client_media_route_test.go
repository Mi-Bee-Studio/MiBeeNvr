// SPDX-License-Identifier: MIT

// Raw-SOAP media routing (#723): trt:* requests must go to the media endpoint
// the device advertises via GetServices. Minimal devices (ESP32 MiBeeCam)
// fault media actions on device_service, so the NVR's raw GetStreamUri path
// has to follow the advertisement instead of posting to the device endpoint.

package onvif

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const soapFaultMediaUnsupported = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <s:Fault>
      <s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:ActionNotSupported</s:Value></s:Subcode></s:Code>
      <s:Reason><s:Text xml:lang="en">Unsupported device action</s:Text></s:Reason>
    </s:Fault>
  </s:Body>
</s:Envelope>`

// mediaRouteServer is an ONVIF device that hosts the media service on a
// distinct path: GetServices on device_service advertises the media XAddr,
// and media actions are only answered there (device_service faults them —
// the behavior the ESP32 MiBeeCam firmware shows).
type mediaRouteServer struct {
	srv        *httptest.Server
	mediaXAddr atomic.Value // string

	getServices        atomic.Int32
	mediaHits          atomic.Int32 // GetStreamUri requests that correctly reached media_service
	deviceMediaRequest atomic.Int32 // GetStreamUri requests that reached device_service

	getServicesFails    bool // serve 500 for GetServices (fallback scenario)
	answerMediaOnDevice bool // tolerant device: answer trt:* on device_service too
}

func newMediaRouteServer(t *testing.T) *mediaRouteServer {
	t.Helper()
	m := &mediaRouteServer{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	m.srv = &httptest.Server{
		Listener: ln,
		Config:   &http.Server{Handler: http.HandlerFunc(m.serve)},
	}
	m.srv.Start()
	t.Cleanup(m.srv.Close)
	m.mediaXAddr.Store("http://" + ln.Addr().String() + "/onvif/media_service")
	return m
}

func (m *mediaRouteServer) deviceEndpoint() string {
	return "http://" + m.srv.Listener.Addr().String() + "/onvif/device_service"
}

func (m *mediaRouteServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b := string(body)
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")

	if strings.HasSuffix(r.URL.Path, "/onvif/media_service") {
		if strings.Contains(b, "GetStreamUri") {
			m.mediaHits.Add(1)
			fmt.Fprint(w, soapGetStreamURIResponse)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, soapFaultResponse)
		return
	}

	// device_service
	switch {
	case strings.Contains(b, "GetServices"):
		m.getServices.Add(1)
		if m.getServicesFails {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, soapFaultResponse)
			return
		}
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tcr:GetServicesResponse xmlns:tcr="http://www.onvif.org/ver10/device/wsdl">
      <tcr:Service><tcr:Namespace>http://www.onvif.org/ver10/device/wsdl</tcr:Namespace><tcr:XAddr>%s</tcr:XAddr></tcr:Service>
      <tcr:Service><tcr:Namespace>http://www.onvif.org/ver10/media/wsdl</tcr:Namespace><tcr:XAddr>%s</tcr:XAddr></tcr:Service>
    </tcr:GetServicesResponse>
  </s:Body>
</s:Envelope>`, m.deviceEndpoint(), m.mediaXAddr.Load().(string))
	case strings.Contains(b, "GetStreamUri"):
		m.deviceMediaRequest.Add(1)
		if m.answerMediaOnDevice {
			fmt.Fprint(w, soapGetStreamURIResponse)
			return
		}
		fmt.Fprint(w, soapFaultMediaUnsupported)
	case strings.Contains(b, "GetCapabilities"):
		fmt.Fprint(w, soapGetCapabilitiesResponse)
	case strings.Contains(b, "GetSystemDateAndTime"):
		fmt.Fprint(w, soapSystemDateAndTime(time.Now()))
	default:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, soapFaultResponse)
	}
}

func TestGetStreamURIWithProtocolRoutesToMediaEndpoint(t *testing.T) {
	m := newMediaRouteServer(t)
	client := NewClient(m.deviceEndpoint(), "admin", "pw")
	require.NoError(t, client.Connect(context.Background()))

	info, err := client.GetStreamURIWithProtocol(context.Background(), "profile_1", "HTTP")
	require.NoError(t, err)
	require.Equal(t, "rtsp://192.168.1.100:554/stream1", info.URI)

	require.Equal(t, int32(1), m.mediaHits.Load(), "GetStreamUri must land on media_service")
	require.Equal(t, int32(0), m.deviceMediaRequest.Load(), "GetStreamUri must not hit device_service")
	require.GreaterOrEqual(t, m.getServices.Load(), int32(1), "routing must consult GetServices")

	// Second call reuses the cached route without a fresh GetServices.
	_, err = client.GetStreamURIWithProtocol(context.Background(), "profile_1", "RTSP")
	require.NoError(t, err)
	require.Equal(t, int32(2), m.mediaHits.Load())
	require.Equal(t, int32(1), m.getServices.Load())
}

func TestMediaRouteStaleHostIsRewritten(t *testing.T) {
	m := newMediaRouteServer(t)
	// Advertise the media XAddr with a stale host (camera roamed): the route
	// must be rewritten to the device endpoint's host, not dialed as-is.
	advertised, ok := m.mediaXAddr.Load().(string)
	require.True(t, ok)
	u, err := url.Parse(advertised)
	require.NoError(t, err)
	u.Host = "192.0.2.99" + portSuffix(u)
	m.mediaXAddr.Store(u.String())

	client := NewClient(m.deviceEndpoint(), "admin", "pw")
	require.NoError(t, client.Connect(context.Background()))

	info, err := client.GetStreamURIWithProtocol(context.Background(), "profile_1", "HTTP")
	require.NoError(t, err)
	require.Equal(t, "rtsp://192.168.1.100:554/stream1", info.URI)
	require.Equal(t, int32(1), m.mediaHits.Load(), "rewritten route must reach the real server")
	require.Equal(t, int32(0), m.deviceMediaRequest.Load())
}

func TestMediaRouteFallsBackToDeviceEndpointWhenUnadvertised(t *testing.T) {
	m := newMediaRouteServer(t)
	m.getServicesFails = true    // device has no usable GetServices
	m.answerMediaOnDevice = true // …and tolerates trt:* on device_service

	client := NewClient(m.deviceEndpoint(), "admin", "pw")
	require.NoError(t, client.Connect(context.Background()))

	info, err := client.GetStreamURIWithProtocol(context.Background(), "profile_1", "HTTP")
	require.NoError(t, err)
	require.Equal(t, "rtsp://192.168.1.100:554/stream1", info.URI)
	require.Equal(t, int32(0), m.mediaHits.Load())
	require.Equal(t, int32(1), m.deviceMediaRequest.Load())
	require.Empty(t, client.mediaRoute, "failed resolution must not be cached as a route")

	// A failed resolution is retried on the next raw media call (transient
	// GetServices errors are not treated as a definitive absence).
	_, err = client.GetStreamURIWithProtocol(context.Background(), "profile_1", "RTSP")
	require.NoError(t, err)
	require.Equal(t, int32(2), m.getServices.Load())
	require.Equal(t, int32(2), m.deviceMediaRequest.Load())
}

func TestResolveMediaEndpointPicksVer20WhenVer10Missing(t *testing.T) {
	var mediaHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "GetServices") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		mediaHits.Add(1)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body><GetServicesResponse xmlns="http://www.onvif.org/ver10/device/wsdl">
    <Service><Namespace>http://www.onvif.org/ver20/media/wsdl</Namespace><XAddr>http://%s/onvif/media2</XAddr></Service>
  </GetServicesResponse></s:Body>
</s:Envelope>`, r.Host)
		w.Header().Set("Content-Type", "application/soap+xml")
	}))
	t.Cleanup(srv.Close)

	got, err := resolveMediaEndpoint(context.Background(), srv.URL+"/onvif/device_service")
	require.NoError(t, err)
	require.Equal(t, "http://"+srv.Listener.Addr().String()+"/onvif/media2", got)
}

func TestRewriteStaleHost(t *testing.T) {
	cases := []struct {
		name   string
		xaddr  string
		device string
		want   string
	}{
		{"same host untouched", "http://1.2.3.4:80/onvif/media_service", "http://1.2.3.4:80/onvif/device_service", "http://1.2.3.4:80/onvif/media_service"},
		{"stale host rewritten", "http://192.0.2.99:80/onvif/media_service", "http://10.0.0.7:80/onvif/device_service", "http://10.0.0.7:80/onvif/media_service"},
		{"unparseable xaddr untouched", "::::", "http://10.0.0.7:80/onvif/device_service", "::::"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, rewriteStaleHost(tc.xaddr, tc.device))
		})
	}
}

// portSuffix returns ":port" (empty when the URL has no explicit port).
func portSuffix(u *url.URL) string {
	if u.Port() == "" {
		return ""
	}
	return ":" + u.Port()
}
