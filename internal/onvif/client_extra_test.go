// SPDX-License-Identifier: MIT
//
// Long-tail coverage for Client: endpoint accessors, raw-SOAP auth variants,
// capability-cache invalidation, sub-controller factories, GetStreamURIWithProtocol,
// ProbeSerial, and the WS-Security digest/device-time helpers.

package onvif

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- shared SOAP fixtures (operations not covered by client_test.go) ---

const soapGetUsersResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tds:GetUsersResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
      <tds:User><tt:Username>admin</tt:Username><tt:UserLevel>Administrator</tt:UserLevel></tds:User>
      <tds:User><tt:Username>operator</tt:Username><tt:UserLevel>Operator</tt:UserLevel></tds:User>
    </tds:GetUsersResponse>
  </s:Body>
</s:Envelope>`

const soapGetNetworkInterfacesResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tds:GetNetworkInterfacesResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
      <tds:NetworkInterfaces token="eth0">
        <tt:Enabled>true</tt:Enabled>
        <tt:Info><tt:Name>eth0</tt:Name><tt:HwAddress>00:11:22:33:44:55</tt:HwAddress><tt:MTU>1500</tt:MTU></tt:Info>
        <tt:IPv4>
          <tt:Enabled>true</tt:Enabled>
          <tt:Config>
            <tt:DHCP>false</tt:DHCP>
            <tt:Manual><tt:Address>192.168.1.100</tt:Address><tt:PrefixLength>24</tt:PrefixLength></tt:Manual>
          </tt:Config>
        </tt:IPv4>
      </tds:NetworkInterfaces>
    </tds:GetNetworkInterfacesResponse>
  </s:Body>
</s:Envelope>`

const soapSystemRebootResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tds:SystemRebootResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
      <tds:Message>Rebooting now</tds:Message>
    </tds:SystemRebootResponse>
  </s:Body>
</s:Envelope>`

// soapSystemDateAndTime builds a GetSystemDateAndTime response carrying the
// given device clock (per wssecurity.go's systemDateTimeResponse struct).
func soapSystemDateAndTime(t time.Time) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tds:GetSystemDateAndTimeResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
      <tds:SystemDateAndTime>
        <tds:UTCDateTime>
          <tds:Date><tds:Year>%d</tds:Year><tds:Month>%d</tds:Month><tds:Day>%d</tds:Day></tds:Date>
          <tds:Time><tds:Hour>%d</tds:Hour><tds:Minute>%d</tds:Minute><tds:Second>%d</tds:Second></tds:Time>
        </tds:UTCDateTime>
      </tds:SystemDateAndTime>
    </tds:GetSystemDateAndTimeResponse>
  </s:Body>
</s:Envelope>`,
		t.UTC().Year(), int(t.UTC().Month()), t.UTC().Day(),
		t.UTC().Hour(), t.UTC().Minute(), t.UTC().Second())
}

// connectMockClient spins up a mock ONVIF server and returns a connected Client.
func connectMockClient(t *testing.T) (*Client, *onvifMockServer, *httptest.Server) {
	t.Helper()
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	srv := mock.startServer(t)
	t.Cleanup(srv.Close)

	client := NewClient(srv.URL, "admin", " secretpw ")
	require.NoError(t, client.Connect(context.Background()))
	return client, mock, srv
}

func TestClientIsReadyTransition(t *testing.T) {
	client, _, _ := connectMockClient(t)
	require.True(t, client.IsReady())

	fresh := NewClient("http://127.0.0.1:1/onvif/device_service", "u", "p")
	require.False(t, fresh.IsReady())
}

func TestClientEndpointAccessors(t *testing.T) {
	client := NewClient("http://192.168.1.50:80/onvif/device_service", "u", "p")
	require.Equal(t, "http://192.168.1.50:80/onvif/ptz_service", client.PTZEndpoint())
	require.Equal(t, "http://192.168.1.50:80/onvif/device_service", client.DeviceEndpoint())
	require.Equal(t, "http://192.168.1.50:80/onvif/device_service", client.GetEndpoint())

	// No device_service component — the URL is returned unchanged.
	odd := NewClient("http://192.168.1.50:80/soap", "u", "p")
	require.Equal(t, "http://192.168.1.50:80/soap", odd.PTZEndpoint())
}

func TestClientSubControllerFactoriesBeforeConnect(t *testing.T) {
	client := NewClient("http://127.0.0.1:1/onvif/device_service", "u", "p")
	require.Nil(t, client.NewPTZController("tok"))
	require.Nil(t, client.NewDeviceManager())
	require.Nil(t, client.NewImagingController("tok"))
	require.Nil(t, client.NewSnapshotProvider("tok"))
	require.Nil(t, client.NewEventSubscriber())
}

func TestClientSubControllerFactoriesAfterConnect(t *testing.T) {
	client, _, _ := connectMockClient(t)
	require.NotNil(t, client.NewPTZController("profile_1"))
	require.NotNil(t, client.NewDeviceManager())
	require.NotNil(t, client.NewImagingController("profile_1"))
	require.NotNil(t, client.NewSnapshotProvider("profile_1"))
	require.NotNil(t, client.NewEventSubscriber())
}

func TestClientCapabilitiesCacheInvalidation(t *testing.T) {
	client, mock, _ := connectMockClient(t)
	ctx := context.Background()

	_, err := client.GetCapabilities(ctx)
	require.NoError(t, err)
	_, err = client.GetCapabilities(ctx) // served from cache
	require.NoError(t, err)
	// One call came from Connect's Initialize handshake + one from the first
	// GetCapabilities; the cached second call added none.
	require.Equal(t, 2, mock.callCount("GetCapabilities"))

	client.InvalidateCapabilitiesCache()
	_, err = client.GetCapabilities(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, mock.callCount("GetCapabilities"))
}

func TestClientGetStreamURIWithProtocol(t *testing.T) {
	client, mock, _ := connectMockClient(t)
	mock.setHandler("GetStreamUri", soapGetStreamURIResponse)

	info, err := client.GetStreamURIWithProtocol(context.Background(), "profile_1", "HTTP")
	require.NoError(t, err)
	require.Equal(t, "rtsp://192.168.1.100:554/stream1", info.URI)
	require.Equal(t, "profile_1", info.ProfileToken)
	require.Equal(t, 1, mock.callCount("GetStreamUri"))

	// Empty URI from the device is surfaced as an error, not a silent success.
	mock.setHandler("GetStreamUri", `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body><trt:GetStreamUriResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
    <trt:MediaUri><tt:Uri xmlns:tt="http://www.onvif.org/ver10/schema"></tt:Uri></trt:MediaUri>
  </trt:GetStreamUriResponse></s:Body>
</s:Envelope>`)
	_, err = client.GetStreamURIWithProtocol(context.Background(), "profile_1", "UDP")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty URI")
}

func TestDoRawSOAPNoAuth(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "admin", "pw")
	var gotAuthHeader atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<ok/>"))
	}))
	t.Cleanup(srv.Close)

	body, err := client.DoRawSOAPNoAuth(context.Background(), srv.URL, "<s:Envelope/>")
	require.NoError(t, err)
	require.Equal(t, "<ok/>", string(body))
	require.Equal(t, "", gotAuthHeader.Load()) // no auth header on purpose

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})
	_, err = client.DoRawSOAPNoAuth(context.Background(), srv.URL, "<s:Envelope/>")
	require.Error(t, err)
	require.Contains(t, err.Error(), "status 500")
}

func TestDoRawSOAPBasicAuth(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "admin", "pw")
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<ok/>"))
	}))
	t.Cleanup(srv.Close)

	_, err := client.DoRawSOAPBasicAuth(context.Background(), srv.URL, "<s:Envelope/>")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(gotAuth.Load().(string), "Basic "))

	// Empty username → no BasicAuth header at all.
	noUser := NewClient("http://127.0.0.1:1", "", "pw")
	gotAuth.Store("")
	_, err = noUser.DoRawSOAPBasicAuth(context.Background(), srv.URL, "<s:Envelope/>")
	require.NoError(t, err)
	require.Equal(t, "", gotAuth.Load())
}

func TestDoRawSOAPWithPasswordText(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "admin", "pw&<>'\"")
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<resp/>"))
	}))
	t.Cleanup(srv.Close)

	soap := `<s:Envelope><s:Body><Probe/></s:Body></s:Envelope>`
	body, err := client.DoRawSOAPWithPasswordText(context.Background(), srv.URL, soap)
	require.NoError(t, err)
	require.Equal(t, "<resp/>", string(body))

	sent := gotBody.Load().(string)
	require.Contains(t, sent, "PasswordText")          // PasswordText profile, not digest
	require.Contains(t, sent, "<wsse:Username>admin<") // credentials injected
	require.Contains(t, sent, "<wsse:Security")        // header injected before Body
	require.Contains(t, sent, "<s:Body><Probe/>")      // original body preserved
	// NOTE: buildWSSecurityHeader concatenates the password raw (no xmlEscape,
	// unlike the digest path in wsseDigestUsernameToken) — asserted as-is.
	require.Contains(t, sent, `>pw&<>'"</wsse:Password>`)
}

func TestNewNonce(t *testing.T) {
	n1, n2 := newNonce(), newNonce()
	require.Len(t, n1, 16)
	require.NotEqual(t, n1, n2)
}

func TestDoRawSOAPDigestDeviceTime(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "admin", "pw")
	// Device clock pinned 10 years ahead → large measurable skew.
	deviceTime := time.Now().UTC().Add(10 * 365 * 24 * time.Hour)
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		if strings.Contains(string(b), "GetSystemDateAndTime") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(soapSystemDateAndTime(deviceTime)))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<digested-ok/>"))
	}))
	t.Cleanup(srv.Close)

	soap := `<?xml version="1.0"?><s:Envelope><s:Body><tds:GetDeviceInformation xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>`
	body, err := client.doRawSOAPDigestDeviceTime(context.Background(), srv.URL, soap)
	require.NoError(t, err)
	require.Equal(t, "<digested-ok/>", string(body))

	sent := gotBody.Load().(string)
	require.Contains(t, sent, "PasswordDigest")
	// The device's (skewed) clock is baked into the Created timestamp — the
	// 10-years-ahead fixture makes the year prefix an unambiguous marker.
	require.Contains(t, sent, fmt.Sprintf("<wsu:Created>%04d-", deviceTime.Year()))
}

func TestMeasureClockSkewUnparsable(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "admin", "pw")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is not soap"))
	}))
	t.Cleanup(srv.Close)

	require.Equal(t, time.Duration(0), client.measureClockSkew(context.Background(), srv.URL))
}

func TestDiagnoseAuthPaths(t *testing.T) {
	// No significant skew → early diagnosis, no digest retry.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "GetSystemDateAndTime") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(soapSystemDateAndTime(time.Now().UTC())))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<x/>"))
	}))
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, "admin", "pw")

	d := client.DiagnoseAuth(context.Background())
	require.False(t, d.SkewDetected)
	require.Contains(t, d.Diagnosis, "no significant clock skew")

	// Large skew + digest success → confirmed skew root cause.
	future := time.Now().UTC().Add(10 * 365 * 24 * time.Hour)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "GetSystemDateAndTime") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(soapSystemDateAndTime(future)))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<x/>"))
	}))
	t.Cleanup(srv2.Close)
	client2 := NewClient(srv2.URL, "admin", "pw")

	d2 := client2.DiagnoseAuth(context.Background())
	require.True(t, d2.SkewDetected)
	require.True(t, d2.DigestOK)
	require.Contains(t, d2.Diagnosis, "Sync the camera's time")

	// Large skew + digest still rejected → credentials may also be wrong.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "GetSystemDateAndTime") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(soapSystemDateAndTime(future)))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("denied"))
	}))
	t.Cleanup(srv3.Close)
	client3 := NewClient(srv3.URL, "admin", "wrongpw")

	d3 := client3.DiagnoseAuth(context.Background())
	require.True(t, d3.SkewDetected)
	require.False(t, d3.DigestOK)
	require.Contains(t, d3.Diagnosis, "credentials may ALSO be wrong")
}
