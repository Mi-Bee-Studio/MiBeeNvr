// SPDX-License-Identifier: MIT
//
// Long-tail coverage for Client: endpoint accessors, raw-SOAP auth variants,
// capability-cache invalidation, sub-controller factories, GetStreamURIWithProtocol,
// ProbeSerial, and the WS-Security digest/device-time helpers.

package onvif

import (
	"context"
	"fmt"
	"net/http/httptest"
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
	require.Equal(t, "http://192.168.1.50:80/onvif/device_service", client.DeviceEndpoint())
	require.Equal(t, "http://192.168.1.50:80/onvif/device_service", client.GetEndpoint())
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
	require.Contains(t, err.Error(), "carried no Uri element")
}
