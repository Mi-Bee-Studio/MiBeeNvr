package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	onvifgo "github.com/0x524a/onvif-go"

	"github.com/stretchr/testify/require"
)

// --- SOAP response templates for mock ONVIF server ---

const soapGetCapabilitiesResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body>
    <tds:GetCapabilitiesResponse>
      <tds:Capabilities>
        <tds:Media XAddr="http://192.168.1.100/onvif/media_service">
          <tt:StreamingCapabilities xmlns:tt="http://www.onvif.org/ver10/schema">
            <tt:RTPMulticast>true</tt:RTPMulticast>
            <tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP>
          </tt:StreamingCapabilities>
        </tds:Media>
        <tds:PTZ XAddr="http://192.168.1.100/onvif/ptz_service"/>
      </tds:Capabilities>
    </tds:GetCapabilitiesResponse>
  </s:Body>
</s:Envelope>`

const soapGetCapabilitiesNoPTZ = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body>
    <tds:GetCapabilitiesResponse>
      <tds:Capabilities>
        <tds:Media XAddr="http://192.168.1.100/onvif/media_service">
          <tt:StreamingCapabilities xmlns:tt="http://www.onvif.org/ver10/schema">
            <tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP>
          </tt:StreamingCapabilities>
        </tds:Media>
      </tds:Capabilities>
    </tds:GetCapabilitiesResponse>
  </s:Body>
</s:Envelope>`

const soapGetDeviceInformationResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body>
    <tds:GetDeviceInformationResponse>
      <tds:Manufacturer>TestMfg</tds:Manufacturer>
      <tds:Model>CamModel-X</tds:Model>
      <tds:FirmwareVersion>2.1.0</tds:FirmwareVersion>
      <tds:SerialNumber>SN12345</tds:SerialNumber>
      <tds:HardwareId>HW001</tds:HardwareId>
    </tds:GetDeviceInformationResponse>
  </s:Body>
</s:Envelope>`

const soapGetProfilesResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
  <s:Body>
    <trt:GetProfilesResponse>
      <trt:Profiles token="profile_1">
        <tt:Name xmlns:tt="http://www.onvif.org/ver10/schema">HD_Profile</tt:Name>
        <trt:VideoEncoderConfiguration token="enc_1">
          <tt:Name xmlns:tt="http://www.onvif.org/ver10/schema">H264_Enc</tt:Name>
          <tt:Encoding xmlns:tt="http://www.onvif.org/ver10/schema">H264</tt:Encoding>
          <tt:Resolution xmlns:tt="http://www.onvif.org/ver10/schema">
            <tt:Width>1920</tt:Width>
            <tt:Height>1080</tt:Height>
          </tt:Resolution>
        </trt:VideoEncoderConfiguration>
      </trt:Profiles>
      <trt:Profiles token="profile_2">
        <tt:Name xmlns:tt="http://www.onvif.org/ver10/schema">SD_Profile</tt:Name>
        <trt:VideoEncoderConfiguration token="enc_2">
          <tt:Name xmlns:tt="http://www.onvif.org/ver10/schema">H265_Enc</tt:Name>
          <tt:Encoding xmlns:tt="http://www.onvif.org/ver10/schema">H265</tt:Encoding>
          <tt:Resolution xmlns:tt="http://www.onvif.org/ver10/schema">
            <tt:Width>640</tt:Width>
            <tt:Height>480</tt:Height>
          </tt:Resolution>
        </trt:VideoEncoderConfiguration>
      </trt:Profiles>
    </trt:GetProfilesResponse>
  </s:Body>
</s:Envelope>`

const soapGetProfilesEmptyResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
  <s:Body>
    <trt:GetProfilesResponse>
    </trt:GetProfilesResponse>
  </s:Body>
</s:Envelope>`

const soapGetStreamURIResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
  <s:Body>
    <trt:GetStreamUriResponse>
      <trt:MediaUri>
        <tt:Uri xmlns:tt="http://www.onvif.org/ver10/schema">rtsp://192.168.1.100:554/stream1</tt:Uri>
        <tt:InvalidAfterConnect xmlns:tt="http://www.onvif.org/ver10/schema">false</tt:InvalidAfterConnect>
        <tt:InvalidAfterReboot xmlns:tt="http://www.onvif.org/ver10/schema">false</tt:InvalidAfterReboot>
        <tt:Timeout xmlns:tt="http://www.onvif.org/ver10/schema">PT60S</tt:Timeout>
      </trt:MediaUri>
    </trt:GetStreamUriResponse>
  </s:Body>
</s:Envelope>`

const soapFaultResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:sbc="http://www.onvif.org/ver10/schema">
  <s:Body>
    <s:Fault>
      <s:Code>
        <s:Value>s:Sender</s:Value>
        <s:Subcode>
          <s:value>ter:InvalidArgVal</s:value>
        </s:Subcode>
      </s:Code>
      <s:Reason>
        <s:Text xml:lang="en">Invalid argument value</s:Text>
      </s:Reason>
    </s:Fault>
  </s:Body>
</s:Envelope>`

// --- Mock ONVIF server helpers ---

// onvifMockServer routes SOAP requests to appropriate handlers based on SOAP action.
type onvifMockServer struct {
	mu       sync.Mutex
	handlers map[string]string // SOAP action -> response body
	calls    map[string]int    // SOAP action -> call count
}

func newOnvifMockServer(t *testing.T) *onvifMockServer {
	t.Helper()
	return &onvifMockServer{
		handlers: make(map[string]string),
		calls:    make(map[string]int),
	}
}

func (s *onvifMockServer) setHandler(action, response string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[action] = response
}

func (s *onvifMockServer) callCount(action string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[action]
}

// startServer creates an httptest.Server that routes ONVIF SOAP requests.
func (s *onvifMockServer) startServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		// Read body to extract SOAP action
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		bodyStr := string(body[:n])

		// Determine action from SOAP body content
		action := clientExtractSOAPAction(bodyStr)
		s.calls[action]++

		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")

		response, ok := s.handlers[action]
		if !ok {
			// Return a SOAP fault for unrecognized actions
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, soapFaultResponse)
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, response)
	}))
}

// clientExtractSOAPAction determines the ONVIF action from the SOAP body.
func clientExtractSOAPAction(body string) string {
	type soapEnvelope struct {
		Body struct {
			Inner struct {
				XMLName xml.Name
			} `xml:",any"`
		} `xml:"Body"`
	}
	var env soapEnvelope
	_ = xml.Unmarshal([]byte(body), &env)
	return env.Body.Inner.XMLName.Local
}

// helperSetupConnectedClient creates a Client connected to a mock ONVIF server
// with GetCapabilities handler pre-configured (required for Initialize).
func helperSetupConnectedClient(t *testing.T, mock *onvifMockServer) (*Client, *httptest.Server) {
	t.Helper()

	// Set up the required GetCapabilities response (called by Initialize)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)

	server := mock.startServer(t)
	t.Cleanup(server.Close)

	client := NewClient(server.URL, "admin", "password")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Connect(ctx)
	require.NoError(t, err)
	require.True(t, client.ready)

	return client, server
}

// --- Existing tests (updated) ---

func TestNewClient(t *testing.T) {
	client := NewClient("http://192.168.1.100:80/onvif/device_service", "admin", "password")
	require.NotNil(t, client)
	require.Equal(t, "http://192.168.1.100:80/onvif/device_service", client.endpoint)
	require.Equal(t, "admin", client.username)
	require.Equal(t, "password", client.password)
	require.False(t, client.ready)
}

func TestClientNotConnected(t *testing.T) {
	client := NewClient("http://localhost:8080/onvif/device_service", "admin", "password")
	ctx := context.Background()

	_, err := client.GetDeviceInformation(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")

	_, err = client.GetProfiles(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")

	_, err = client.GetStreamURI(ctx, "profile1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")

	_, err = client.GetCapabilities(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")
}

func TestClientConnect_Success(t *testing.T) {
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Connect(ctx)
	require.NoError(t, err)
	require.True(t, client.ready)
	require.NotNil(t, client.client)
	require.Equal(t, 1, mock.callCount("GetCapabilities"))
}

func TestClientConnect_AuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "wrongpassword")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Connect(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "initialize")
	require.False(t, client.ready)
}

func TestClientConnect_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, soapFaultResponse)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Connect(ctx)
	require.Error(t, err)
	require.False(t, client.ready)
}

// --- GetDeviceInformation tests ---

func TestGetDeviceInformation_Success(t *testing.T) {
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	mock.setHandler("GetDeviceInformation", soapGetDeviceInformationResponse)
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))

	info, err := client.GetDeviceInformation(ctx)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, "TestMfg", info.Manufacturer)
	require.Equal(t, "CamModel-X", info.Model)
	require.Equal(t, "2.1.0", info.Firmware)
	require.Equal(t, "SN12345", info.SerialNumber)
	require.Equal(t, "HW001", info.HardwareID)
	require.Equal(t, 1, mock.callCount("GetDeviceInformation"))
}

func TestGetDeviceInformation_DeviceError(t *testing.T) {
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	mock.setHandler("GetDeviceInformation", soapFaultResponse)
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))

	info, err := client.GetDeviceInformation(ctx)
	require.Error(t, err)
	require.Nil(t, info)
	require.Contains(t, err.Error(), "get device information")
}

// --- GetProfiles tests ---

func TestGetProfiles_Success(t *testing.T) {
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	mock.setHandler("GetProfiles", soapGetProfilesResponse)
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))

	profiles, err := client.GetProfiles(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, 2)

	// First profile: H264 1920x1080
	require.Equal(t, "profile_1", profiles[0].Token)
	require.Equal(t, "HD_Profile", profiles[0].Name)
	require.Equal(t, "H264", profiles[0].Encoding)
	require.Equal(t, 1920, profiles[0].Width)
	require.Equal(t, 1080, profiles[0].Height)

	// Second profile: H265 640x480
	require.Equal(t, "profile_2", profiles[1].Token)
	require.Equal(t, "SD_Profile", profiles[1].Name)
	require.Equal(t, "H265", profiles[1].Encoding)
	require.Equal(t, 640, profiles[1].Width)
	require.Equal(t, 480, profiles[1].Height)

	require.Equal(t, 1, mock.callCount("GetProfiles"))
}

func TestGetProfiles_Empty(t *testing.T) {
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	mock.setHandler("GetProfiles", soapGetProfilesEmptyResponse)
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))

	profiles, err := client.GetProfiles(ctx)
	require.NoError(t, err)
	require.NotNil(t, profiles)
	require.Empty(t, profiles)
}

func TestGetProfiles_DeviceError(t *testing.T) {
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	mock.setHandler("GetProfiles", soapFaultResponse)
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))

	profiles, err := client.GetProfiles(ctx)
	require.Error(t, err)
	require.Nil(t, profiles)
	require.Contains(t, err.Error(), "get profiles")
}

// --- GetStreamURI tests ---

func TestGetStreamURI_Success(t *testing.T) {
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	mock.setHandler("GetStreamUri", soapGetStreamURIResponse)
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))

	stream, err := client.GetStreamURI(ctx, "profile_1")
	require.NoError(t, err)
	require.NotNil(t, stream)
	require.Equal(t, "rtsp://192.168.1.100:554/stream1", stream.URI)
	require.Equal(t, "RTSP", stream.Protocol)
	require.Equal(t, "profile_1", stream.ProfileToken)
	require.Equal(t, 1, mock.callCount("GetStreamUri"))
}

func TestGetStreamURI_DeviceError(t *testing.T) {
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	mock.setHandler("GetStreamUri", soapFaultResponse)
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))

	stream, err := client.GetStreamURI(ctx, "profile_1")
	require.Error(t, err)
	require.Nil(t, stream)
	require.Contains(t, err.Error(), "get stream URI")
}

// --- GetCapabilities tests ---

func TestGetCapabilities_PTZSupported(t *testing.T) {
	mock := newOnvifMockServer(t)
	// Initialize uses GetCapabilities — return PTZ caps
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	// Subsequent GetCapabilities calls also return the same response
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))

	caps, err := client.GetCapabilities(ctx)
	require.NoError(t, err)
	require.NotNil(t, caps)
	require.True(t, caps.PTZ, "PTZ should be supported when PTZ capability is present")
	require.True(t, caps.Streaming, "Streaming should be supported when Media capability is present")
	// GetCapabilities called twice: once during Connect (Initialize), once explicitly
	require.Equal(t, 2, mock.callCount("GetCapabilities"))
}

func TestGetCapabilities_NoPTZ(t *testing.T) {
	mock := newOnvifMockServer(t)
	// First call (Initialize) — returns capabilities with PTZ so media endpoint is discovered
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))

	// Now switch to no-PTZ response for the explicit GetCapabilities call
	mock.setHandler("GetCapabilities", soapGetCapabilitiesNoPTZ)

	caps, err := client.GetCapabilities(ctx)
	require.NoError(t, err)
	require.NotNil(t, caps)
	require.False(t, caps.PTZ, "PTZ should not be supported when PTZ capability is absent")
	require.True(t, caps.Streaming, "Streaming should be supported when Media capability is present")
}

// --- Interface compliance ---

func TestClientImplementsDeviceClient(t *testing.T) {
	t.Helper()
	var _ DeviceClient = &Client{}
}

// --- Mapping function tests ---

func TestMapDeviceInfo(t *testing.T) {
	t.Helper()
	info := &onvifgo.DeviceInformation{
		Manufacturer:    "Hikvision",
		Model:           "DS-2CD2143G2-I",
		FirmwareVersion: "5.7.1",
		SerialNumber:    "HK123456789",
		HardwareID:      "hw-v2",
	}

	result := mapDeviceInfo(info)
	require.Equal(t, "Hikvision", result.Manufacturer)
	require.Equal(t, "DS-2CD2143G2-I", result.Model)
	require.Equal(t, "5.7.1", result.Firmware)
	require.Equal(t, "HK123456789", result.SerialNumber)
	require.Equal(t, "hw-v2", result.HardwareID)
}

func TestMapCapabilities(t *testing.T) {
	t.Helper()

	// With PTZ and Media
	caps := &onvifgo.Capabilities{
		PTZ:   &onvifgo.PTZCapabilities{XAddr: "http://host/ptz"},
		Media: &onvifgo.MediaCapabilities{XAddr: "http://host/media"},
	}
	result := mapCapabilities(caps)
	require.True(t, result.PTZ)
	require.True(t, result.Streaming)

	// Without PTZ
	capsNoPTZ := &onvifgo.Capabilities{
		Media: &onvifgo.MediaCapabilities{XAddr: "http://host/media"},
	}
	resultNoPTZ := mapCapabilities(capsNoPTZ)
	require.False(t, resultNoPTZ.PTZ)
	require.True(t, resultNoPTZ.Streaming)

	// Without Media
	capsNoMedia := &onvifgo.Capabilities{
		PTZ: &onvifgo.PTZCapabilities{XAddr: "http://host/ptz"},
	}
	resultNoMedia := mapCapabilities(capsNoMedia)
	require.True(t, resultNoMedia.PTZ)
	require.False(t, resultNoMedia.Streaming)

	// Empty capabilities
	capsEmpty := &onvifgo.Capabilities{}
	resultEmpty := mapCapabilities(capsEmpty)
	require.False(t, resultEmpty.PTZ)
	require.False(t, resultEmpty.Streaming)
}

func TestMapProfile(t *testing.T) {
	t.Helper()

	// Full profile with encoding
	profile := &onvifgo.Profile{
		Token: "profile_1",
		Name:  "HD",
		VideoEncoderConfiguration: &onvifgo.VideoEncoderConfiguration{
			Encoding: "H264",
			Resolution: &onvifgo.VideoResolution{
				Width:  1920,
				Height: 1080,
			},
		},
	}
	result := mapProfile(profile)
	require.Equal(t, "profile_1", result.Token)
	require.Equal(t, "HD", result.Name)
	require.Equal(t, "H264", result.Encoding)
	require.Equal(t, 1920, result.Width)
	require.Equal(t, 1080, result.Height)

	// Profile without video encoder config
	profileNoEnc := &onvifgo.Profile{
		Token: "profile_2",
		Name:  "AudioOnly",
	}
	resultNoEnc := mapProfile(profileNoEnc)
	require.Equal(t, "profile_2", resultNoEnc.Token)
	require.Equal(t, "AudioOnly", resultNoEnc.Name)
	require.Empty(t, resultNoEnc.Encoding)
	require.Equal(t, 0, resultNoEnc.Width)
	require.Equal(t, 0, resultNoEnc.Height)

	// Profile with nil resolution
	profileNilRes := &onvifgo.Profile{
		Token: "profile_3",
		Name:  "NoRes",
		VideoEncoderConfiguration: &onvifgo.VideoEncoderConfiguration{
			Encoding:   "JPEG",
			Resolution: nil,
		},
	}
	resultNilRes := mapProfile(profileNilRes)
	require.Equal(t, "JPEG", resultNilRes.Encoding)
	require.Equal(t, 0, resultNilRes.Width)
	require.Equal(t, 0, resultNilRes.Height)
}

func TestMapStreamURI(t *testing.T) {
	t.Helper()
	uri := &onvifgo.MediaURI{
		URI:                 "rtsp://192.168.1.100:554/stream",
		InvalidAfterConnect: false,
		InvalidAfterReboot:  false,
	}

	result := mapStreamURI(uri, "profile_1")
	require.Equal(t, "rtsp://192.168.1.100:554/stream", result.URI)
	require.Equal(t, "RTSP", result.Protocol)
	require.Equal(t, "profile_1", result.ProfileToken)
	require.Empty(t, result.Encoding)
}
