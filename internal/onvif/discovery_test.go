package onvif

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- Discover() tests ---

func TestDiscover_NoNetwork_ReturnsEmptyList(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	devices, err := Discover(ctx, 2*time.Second)
	require.NoError(t, err, "Discover must not return error even without network")
	require.NotNil(t, devices, "Discover must return non-nil slice")
}

func TestDiscover_DefaultTimeout(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Should not panic with 0 timeout (uses default 5s)
	devices, err := Discover(ctx, 0)
	require.NoError(t, err)
	require.NotNil(t, devices)
}

func TestDiscover_ContextCancelled_ReturnsImmediately(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	start := time.Now()
	devices, err := Discover(ctx, 10*time.Second)
	elapsed := time.Since(start)

	require.NoError(t, err, "cancelled context must not return error")
	require.NotNil(t, devices)
	require.Less(t, elapsed, 2*time.Second, "cancelled context should return immediately")
}

// --- ProbeDevice() tests ---

// validProbeMatchResponse is a realistic ONVIF WS-Discovery ProbeMatches response.
const validProbeMatchResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing"
            xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
  <s:Header>
    <a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</a:Action>
    <a:RelatesTo>uuid:test-message-id</a:RelatesTo>
  </s:Header>
  <s:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <a:EndpointReference>
          <a:Address>uuid:device-abc-123</a:Address>
        </a:EndpointReference>
        <d:Types>dn:NetworkVideoTransmitter</d:Types>
        <d:Scopes>onvif://www.onvif.org/name/TestCamera onvif://www.onvif.org/hardware/ModelXYZ</d:Scopes>
        <d:XAddrs>http://192.168.1.100:8080/onvif/device_service</d:XAddrs>
        <d:MetadataVersion>1</d:MetadataVersion>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </s:Body>
</s:Envelope>`

// validProbeMatchMultiXAddr has multiple XAddrs (main + sub stream).
const validProbeMatchMultiXAddr = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing"
            xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
  <s:Header>
    <a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</a:Action>
  </s:Header>
  <s:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <a:EndpointReference>
          <a:Address>uuid:multi-addr-device</a:Address>
        </a:EndpointReference>
        <d:Types>dn:NetworkVideoTransmitter</d:Types>
        <d:Scopes>onvif://www.onvif.org/name/FrontDoor onvif://www.onvif.org/location/Front</d:Scopes>
        <d:XAddrs>http://10.0.0.1:80/onvif/device_service http://10.0.0.1:80/onvif/device_service</d:XAddrs>
        <d:MetadataVersion>2</d:MetadataVersion>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </s:Body>
</s:Envelope>`

func testServerAddr(t *testing.T, ts *httptest.Server) (string, int) {
	t.Helper()
	addr := ts.Listener.Addr().String()
	host, portStr, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, port
}

func TestProbeDevice_ValidResponse(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Contains(t, r.Header.Get("Content-Type"), "soap+xml")
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, validProbeMatchResponse)
	}))
	defer server.Close()

	host, port := testServerAddr(t, server)
	device, err := ProbeDevice(context.Background(), host, port, 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, device)
	require.Equal(t, "uuid:device-abc-123", device.UUID)
	require.Equal(t, "TestCamera", device.Name)
	require.Equal(t, "ModelXYZ", device.Hardware)
	require.Contains(t, device.XAddrs, "http://192.168.1.100:8080/onvif/device_service")
	require.Contains(t, device.Scopes, "onvif://www.onvif.org/name/TestCamera")
	require.Equal(t, "http://192.168.1.100:8080/onvif/device_service", device.Endpoint)
}

func TestProbeDevice_MultipleXAddrs(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		fmt.Fprint(w, validProbeMatchMultiXAddr)
	}))
	defer server.Close()

	host, port := testServerAddr(t, server)
	device, err := ProbeDevice(context.Background(), host, port, 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, device)
	require.Equal(t, "uuid:multi-addr-device", device.UUID)
	require.Equal(t, "FrontDoor", device.Name)
	require.Len(t, device.XAddrs, 2)
	// Endpoint should be first XAddr
	require.Equal(t, "http://10.0.0.1:80/onvif/device_service", device.Endpoint)
}

func TestProbeDevice_NonONVIFDevice_ReturnsNil(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body>Not an ONVIF device</body></html>")
	}))
	defer server.Close()

	host, port := testServerAddr(t, server)
	device, err := ProbeDevice(context.Background(), host, port, 5*time.Second)
	require.NoError(t, err, "non-ONVIF device should not return error")
	require.Nil(t, device)
}

func TestProbeDevice_HTTPError_ReturnsNil(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	host, port := testServerAddr(t, server)
	device, err := ProbeDevice(context.Background(), host, port, 5*time.Second)
	require.NoError(t, err, "HTTP error should not return error")
	require.Nil(t, device)
}

func TestProbeDevice_EmptyProbeMatches_ReturnsNil(t *testing.T) {
	t.Helper()
	emptyResponse := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
  <s:Body>
    <d:ProbeMatches></d:ProbeMatches>
  </s:Body>
</s:Envelope>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		fmt.Fprint(w, emptyResponse)
	}))
	defer server.Close()

	host, port := testServerAddr(t, server)
	device, err := ProbeDevice(context.Background(), host, port, 5*time.Second)
	require.NoError(t, err)
	require.Nil(t, device)
}

func TestProbeDevice_ConnectionRefused_ReturnsError(t *testing.T) {
	t.Helper()
	// Use a port that's definitely not listening
	device, err := ProbeDevice(context.Background(), "127.0.0.1", 1, 1*time.Second)
	require.Error(t, err)
	require.Nil(t, device)
	require.Contains(t, err.Error(), "probe request failed")
}

func TestProbeDevice_ContextCancelled_ReturnsError(t *testing.T) {
	t.Helper()
	// Slow server that never responds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	host, port := testServerAddr(t, server)
	start := time.Now()
	device, err := ProbeDevice(ctx, host, port, 10*time.Second)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Nil(t, device)
	require.Less(t, elapsed, 2*time.Second, "cancelled context should fail fast")
}

func TestProbeDevice_DefaultTimeout(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		fmt.Fprint(w, validProbeMatchResponse)
	}))
	defer server.Close()

	host, port := testServerAddr(t, server)
	// Pass 0 timeout — should use default
	device, err := ProbeDevice(context.Background(), host, port, 0)
	require.NoError(t, err)
	require.NotNil(t, device)
}

func TestProbeDevice_SendsSOAPProbe(t *testing.T) {
	t.Helper()
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		ct := r.Header.Get("Content-Type")
		require.True(t, strings.Contains(ct, "soap+xml"), "expected soap+xml content type, got %s", ct)
		require.Equal(t, "/onvif/device_service", r.URL.Path)

		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		require.Contains(t, receivedBody, "Probe")
		require.Contains(t, receivedBody, "NetworkVideoTransmitter")
		require.Contains(t, receivedBody, "uuid:")

		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		fmt.Fprint(w, validProbeMatchResponse)
	}))
	defer server.Close()

	host, port := testServerAddr(t, server)
	device, err := ProbeDevice(context.Background(), host, port, 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, device)
	require.NotEmpty(t, receivedBody, "should have sent a SOAP body")
}
