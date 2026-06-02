package onvif

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- Imaging SOAP mock helpers ---

const soapImagingNamespace = "http://www.onvif.org/ver20/imaging/wsdl"

// soapImagingSuccessResponse returns a successful SOAP response for an imaging action.
func soapImagingSuccessResponse(action string) string {
	return soapEnvelope(fmt.Sprintf(`<timg:%sResponse xmlns:timg="%s"/>`, action, soapImagingNamespace))
}

// soapImagingSettingsResponse returns a GetImagingSettings SOAP response with values.
func soapImagingSettingsResponse(brightness, saturation, contrast, sharpness float64) string {
	return soapEnvelope(fmt.Sprintf(`<timg:GetImagingSettingsResponse xmlns:timg="%s">
  <timg:ImagingSettings xmlns:tt="http://www.onvif.org/ver10/schema">
    <tt:Brightness>%f</tt:Brightness>
    <tt:ColorSaturation>%f</tt:ColorSaturation>
    <tt:Contrast>%f</tt:Contrast>
    <tt:Sharpness>%f</tt:Sharpness>
  </timg:ImagingSettings>
</timg:GetImagingSettingsResponse>`, soapImagingNamespace, brightness, saturation, contrast, sharpness))
}

// soapImagingOptionsResponse returns a GetOptions SOAP response with ranges.
func soapImagingOptionsResponse() string {
	return soapEnvelope(fmt.Sprintf(`<timg:GetOptionsResponse xmlns:timg="%s">
  <timg:ImagingOptions xmlns:tt="http://www.onvif.org/ver10/schema">
    <tt:Brightness>
      <tt:Min>0</tt:Min>
      <tt:Max>1</tt:Max>
    </tt:Brightness>
    <tt:ColorSaturation>
      <tt:Min>0</tt:Min>
      <tt:Max>1</tt:Max>
    </tt:ColorSaturation>
    <tt:Contrast>
      <tt:Min>0</tt:Min>
      <tt:Max>1</tt:Max>
    </tt:Contrast>
    <tt:Sharpness>
      <tt:Min>0</tt:Min>
      <tt:Max>1</tt:Max>
    </tt:Sharpness>
  </timg:ImagingOptions>
</timg:GetOptionsResponse>`, soapImagingNamespace))
}

// newImagingTestServer creates an httptest.Server that responds to raw SOAP requests.
func newImagingTestServer(t *testing.T, handler func(body string, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		bodyBytes, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		body := string(bodyBytes)

		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		handler(body, w)
	}))
}

// --- ImagingControllerImpl tests ---

func TestImagingController_ImplementsInterface(t *testing.T) {
	t.Helper()
	var _ ImagingController = (*ImagingControllerImpl)(nil)
}

func TestImagingController_GetImagingSettings_Success(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		require.Contains(t, body, "GetImagingSettings")
		w.Write([]byte(soapImagingSettingsResponse(0.5, 0.7, 0.3, 0.8)))
	})
	defer server.Close()

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint(server.URL)

	settings, err := ctrl.GetImagingSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0.5, settings.Brightness)
	require.Equal(t, 0.7, settings.Saturation)
	require.Equal(t, 0.3, settings.Contrast)
	require.Equal(t, 0.8, settings.Sharpness)
}

func TestImagingController_GetImagingSettings_NoEndpoint(t *testing.T) {
	ctrl := NewImagingController(nil, "profile1")
	// Don't set imaging endpoint

	_, err := ctrl.GetImagingSettings(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "imaging endpoint not configured")
}

func TestImagingController_GetImagingSettings_SOAPError(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		require.Contains(t, body, "GetImagingSettings")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(soapFault("Sender", "Invalid token")))
	})
	defer server.Close()

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint(server.URL)

	_, err := ctrl.GetImagingSettings(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "get imaging settings failed")
}

func TestImagingController_SetImagingSettings_Success(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		require.Contains(t, body, "SetImagingSettings")
		require.Contains(t, body, "0.500000") // brightness
		require.Contains(t, body, "0.700000") // saturation
		w.Write([]byte(soapImagingSuccessResponse("SetImagingSettings")))
	})
	defer server.Close()

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint(server.URL)

	settings := ImagingSettings{
		Brightness: 0.5,
		Saturation: 0.7,
		Contrast:   0.3,
		Sharpness:  0.8,
		Exposure: ExposureSettings{
			Mode:         "manual",
			ExposureTime: 0.01,
			Gain:         0.5,
		},
		WhiteBalance: WhiteBalanceSettings{
			Mode:              "auto",
			ColorTemperature: 5500,
		},
	}
	err := ctrl.SetImagingSettings(context.Background(), settings)
	require.NoError(t, err)
}

func TestImagingController_SetImagingSettings_NoEndpoint(t *testing.T) {
	ctrl := NewImagingController(nil, "profile1")

	err := ctrl.SetImagingSettings(context.Background(), ImagingSettings{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "imaging endpoint not configured")
}

func TestImagingController_SetImagingSettings_SOAPError(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(soapFault("Receiver", "Internal error")))
	})
	defer server.Close()

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint(server.URL)

	err := ctrl.SetImagingSettings(context.Background(), ImagingSettings{Brightness: 0.5})
	require.Error(t, err)
	require.Contains(t, err.Error(), "set imaging settings failed")
}

func TestImagingController_GetImagingOptions_Success(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		require.Contains(t, body, "GetOptions")
		w.Write([]byte(soapImagingOptionsResponse()))
	})
	defer server.Close()

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint(server.URL)

	options, err := ctrl.GetImagingOptions(context.Background())
	require.NoError(t, err)
	require.NotNil(t, options.Brightness)
	require.Equal(t, 0.0, options.Brightness.Min)
	require.Equal(t, 1.0, options.Brightness.Max)
	require.NotNil(t, options.Saturation)
	require.NotNil(t, options.Contrast)
	require.NotNil(t, options.Sharpness)
}

func TestImagingController_GetImagingOptions_NoEndpoint(t *testing.T) {
	ctrl := NewImagingController(nil, "profile1")

	_, err := ctrl.GetImagingOptions(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "imaging endpoint not configured")
}

func TestImagingController_GetImagingOptions_SOAPError(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(soapFault("Sender", "Not supported")))
	})
	defer server.Close()

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint(server.URL)

	_, err := ctrl.GetImagingOptions(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "get imaging options failed")
}

func TestImagingController_SetCredentials(t *testing.T) {
	t.Helper()
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		w.Write([]byte(soapImagingSettingsResponse(0.5, 0.5, 0.5, 0.5)))
	}))
	defer server.Close()

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint(server.URL)
	ctrl.SetCredentials("admin", "secret123")

	_, err := ctrl.GetImagingSettings(context.Background())
	require.NoError(t, err)
	require.Contains(t, capturedAuth, "Basic ")
}

func TestImagingController_ConcurrentOperations(t *testing.T) {
	t.Helper()
	var callCount int
	mu := &sync.Mutex{}
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		time.Sleep(time.Millisecond)
		mu.Lock()
		callCount++
		mu.Unlock()
		w.Write([]byte(soapImagingSettingsResponse(0.5, 0.5, 0.5, 0.5)))
	})
	defer server.Close()

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint(server.URL)

	var wg sync.WaitGroup
	errs := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			if _, err := ctrl.GetImagingSettings(ctx); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 10, callCount)
}

func TestImagingController_InvalidXMLResponse(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		w.Write([]byte("not valid xml"))
	})
	defer server.Close()

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint(server.URL)

	_, err := ctrl.GetImagingSettings(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse imaging settings response")
}

func TestParseImagingSettingsResponse_InvalidXML(t *testing.T) {
	_, err := parseImagingSettingsResponse([]byte("invalid xml"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse imaging settings response")
}

func TestParseImagingOptionsResponse_InvalidXML(t *testing.T) {
	_, err := parseImagingOptionsResponse([]byte("invalid xml"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse imaging options response")
}

func TestBuildExposureSettingsXML_AutoMode(t *testing.T) {
	t.Helper()
	xml := buildExposureSettingsXML(ExposureSettings{Mode: "auto", ExposureTime: 0.01, Gain: 0.5})
	require.Contains(t, xml, "AUTO")
	require.Contains(t, xml, "0.010000")
	require.Contains(t, xml, "0.500000")
}

func TestBuildExposureSettingsXML_ManualMode(t *testing.T) {
	t.Helper()
	xml := buildExposureSettingsXML(ExposureSettings{Mode: "manual", ExposureTime: 0.02, Gain: 1.0})
	require.Contains(t, xml, "MANUAL")
	require.Contains(t, xml, "0.020000")
	require.Contains(t, xml, "1.000000")
}

func TestBuildWhiteBalanceSettingsXML_AutoMode(t *testing.T) {
	t.Helper()
	xml := buildWhiteBalanceSettingsXML(WhiteBalanceSettings{Mode: "auto", ColorTemperature: 5500})
	require.Contains(t, xml, "AUTO")
	require.Contains(t, xml, "5500")
}

func TestBuildWhiteBalanceSettingsXML_ManualMode(t *testing.T) {
	t.Helper()
	xml := buildWhiteBalanceSettingsXML(WhiteBalanceSettings{Mode: "manual", ColorTemperature: 3200})
	require.Contains(t, xml, "MANUAL")
	require.Contains(t, xml, "3200")
}

func TestTruncateStr_Short(t *testing.T) {
	t.Helper()
	result := truncateStr("hello", 10)
	require.Equal(t, "hello", result)
}

func TestTruncateStr_ExactlyMax(t *testing.T) {
	t.Helper()
	result := truncateStr("hello", 5)
	require.Equal(t, "hello", result)
}

func TestTruncateStr_TooLong(t *testing.T) {
	t.Helper()
	result := truncateStr("hello world", 5)
	require.Equal(t, "hello...", result)
}

func TestImagingController_SOAPBodyContainsProfileToken(t *testing.T) {
	var capturedBody string
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		capturedBody = body
		w.Write([]byte(soapImagingSettingsResponse(0.5, 0.5, 0.5, 0.5)))
	})
	defer server.Close()

	ctrl := NewImagingController(nil, "my-video-token")
	ctrl.SetImagingEndpoint(server.URL)

	_, err := ctrl.GetImagingSettings(context.Background())
	require.NoError(t, err)
	require.Contains(t, capturedBody, "my-video-token")
}

func TestImagingController_SetImagingSettings_XMLStructure(t *testing.T) {
	var capturedBody string
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		capturedBody = body
		w.Write([]byte(soapImagingSuccessResponse("SetImagingSettings")))
	})
	defer server.Close()

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint(server.URL)

	err := ctrl.SetImagingSettings(context.Background(), ImagingSettings{
		Brightness: 0.75,
		Saturation: 0.50,
		Contrast:   0.25,
		Sharpness:  0.60,
	})
	require.NoError(t, err)
	require.Contains(t, capturedBody, "SetImagingSettings")
	require.Contains(t, capturedBody, "profile1")
	require.Contains(t, capturedBody, "0.750000")
	require.Contains(t, capturedBody, "0.500000")
	require.Contains(t, capturedBody, "0.250000")
	require.Contains(t, capturedBody, "0.600000")
}

func TestImagingController_ServerDown(t *testing.T) {
	t.Helper()
	// Use a port that won't be listening
	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint("http://127.0.0.1:1/imaging")

	_, err := ctrl.GetImagingSettings(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "get imaging settings failed")
}

func TestImagingController_ContextCancelled(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	ctrl := NewImagingController(nil, "profile1")
	ctrl.SetImagingEndpoint("http://127.0.0.1:1/imaging")

	_, err := ctrl.GetImagingSettings(ctx)
	require.Error(t, err)
}
