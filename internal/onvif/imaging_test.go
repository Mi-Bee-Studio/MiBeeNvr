package onvif

import (
	"context"
	"reflect"
	"strings"
	"unsafe"

	"fmt"
	onvifgo "github.com/mickeyzzc/onvif-go/v2/onvif"
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

// setImagingEndpoint pins the unexported imagingEndpoint field on an
// onvifgo.Client (tests run against httptest servers that don't support the
// full Initialize handshake — same technique as the PTZ tests).
func setImagingEndpoint(t *testing.T, client *onvifgo.Client, url string) {
	t.Helper()
	v := reflect.ValueOf(client).Elem()
	f := v.FieldByName("imagingEndpoint")
	require.True(t, f.IsValid(), "imagingEndpoint field not found on onvifgo.Client")
	ptr := (*string)(unsafe.Pointer(f.UnsafeAddr()))
	*ptr = url
}

// newImagingTestClient creates an onvif-go Client with imagingEndpoint pointed
// at the test server.
func newImagingTestClient(t *testing.T, server *httptest.Server) *onvifgo.Client {
	t.Helper()
	client, err := onvifgo.NewClient(
		server.URL,
		onvifgo.WithCredentials("admin", "password"),
		onvifgo.WithAuthFallback(onvifgo.AuthPasswordText, onvifgo.AuthHTTPBasic, onvifgo.AuthNone),
	)
	require.NoError(t, err)
	setImagingEndpoint(t, client, server.URL)
	return client
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

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

	settings, err := ctrl.GetImagingSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0.5, settings.Brightness)
	require.Equal(t, 0.7, settings.Saturation)
	require.Equal(t, 0.3, settings.Contrast)
	require.Equal(t, 0.8, settings.Sharpness)
}

func TestImagingController_NilClientGuard(t *testing.T) {
	ctrl := NewImagingController(nil, "profile1")

	_, err := ctrl.GetImagingSettings(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no ONVIF client")

	err = ctrl.SetImagingSettings(context.Background(), ImagingSettings{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no ONVIF client")

	_, err = ctrl.GetImagingOptions(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no ONVIF client")
}

func TestImagingController_GetImagingSettings_SOAPError(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		require.Contains(t, body, "GetImagingSettings")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(soapFault("Sender", "Invalid token")))
	})
	defer server.Close()

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

	_, err := ctrl.GetImagingSettings(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "get imaging settings failed")
}

func TestImagingController_SetImagingSettings_Success(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		require.Contains(t, body, "SetImagingSettings")
		require.Contains(t, body, "0.5") // brightness
		require.Contains(t, body, "0.7") // saturation
		w.Write([]byte(soapImagingSuccessResponse("SetImagingSettings")))
	})
	defer server.Close()

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

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
			Mode:             "auto",
			ColorTemperature: 5500,
		},
	}
	err := ctrl.SetImagingSettings(context.Background(), settings)
	require.NoError(t, err)
}

func TestImagingController_SetImagingSettings_NoClientGuarded(t *testing.T) {
	ctrl := NewImagingController(nil, "profile1")

	err := ctrl.SetImagingSettings(context.Background(), ImagingSettings{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no ONVIF client")
}

func TestImagingController_SetImagingSettings_SOAPError(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(soapFault("Receiver", "Internal error")))
	})
	defer server.Close()

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

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

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

	options, err := ctrl.GetImagingOptions(context.Background())
	require.NoError(t, err)
	require.NotNil(t, options.Brightness)
	require.Equal(t, 0.0, options.Brightness.Min)
	require.Equal(t, 1.0, options.Brightness.Max)
	require.NotNil(t, options.Saturation)
	require.NotNil(t, options.Contrast)
	require.NotNil(t, options.Sharpness)
}

func TestImagingController_GetImagingOptions_NoClientGuarded(t *testing.T) {
	ctrl := NewImagingController(nil, "profile1")

	_, err := ctrl.GetImagingOptions(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no ONVIF client")
}

func TestImagingController_GetImagingOptions_SOAPError(t *testing.T) {
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(soapFault("Sender", "Not supported")))
	})
	defer server.Close()

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

	_, err := ctrl.GetImagingOptions(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "get imaging options failed")
}

func TestImagingController_SetCredentials(t *testing.T) {
	t.Helper()
	// With credentials set, doRawSOAP should prefer WS-Security (UsernameToken in
	// the SOAP body). A cooperating server (200, no Fault) succeeds on the first try.
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		capturedBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		w.Write([]byte(soapImagingSettingsResponse(0.5, 0.5, 0.5, 0.5)))
	}))
	defer server.Close()

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

	_, err := ctrl.GetImagingSettings(context.Background())
	require.NoError(t, err)
	// WS-Security UsernameToken should be present in the SOAP envelope.
	require.Contains(t, capturedBody, "UsernameToken")
	require.Contains(t, capturedBody, "admin")
}

func TestImagingController_AuthFallbackToBasic(t *testing.T) {
	t.Helper()
	// The device rejects every WS-Security-shaped body (both digest and
	// PasswordText carry a UsernameToken) but accepts HTTP Basic Auth.
	var attempt int
	var lastAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		attempt++
		lastAuthHeader = r.Header.Get("Authorization")
		if strings.Contains(string(bodyBytes), "UsernameToken") {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`<error>unauthorized</error>`))
			return
		}
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		w.Write([]byte(soapImagingSettingsResponse(0.5, 0.5, 0.5, 0.5)))
	}))
	defer server.Close()

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

	_, err := ctrl.GetImagingSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, attempt, "ladder: digest, then PasswordText, then HTTP Basic")
	require.Contains(t, lastAuthHeader, "Basic ")
}

func TestImagingController_AuthFallbackToNone(t *testing.T) {
	t.Helper()
	// The device rejects every WS-Security-shaped body AND HTTP Basic —
	// only the unauthenticated attempt passes.
	var attempt int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		attempt++
		if strings.Contains(string(bodyBytes), "UsernameToken") || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`<error>unauthorized</error>`))
			return
		}
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		w.Write([]byte(soapImagingSettingsResponse(0.5, 0.5, 0.5, 0.5)))
	}))
	defer server.Close()

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

	_, err := ctrl.GetImagingSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 4, attempt, "ladder: digest, PasswordText, Basic, then NoAuth")
}

func TestImagingController_NoCredentialsSkipsAuthStrategies(t *testing.T) {
	t.Helper()
	// Without credentials, only the no-auth strategy is attempted.
	var attempt int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		w.Write([]byte(soapImagingSettingsResponse(0.5, 0.5, 0.5, 0.5)))
	}))
	defer server.Close()

	bare, err := onvifgo.NewClient(server.URL)
	require.NoError(t, err)
	setImagingEndpoint(t, bare, server.URL)
	ctrl := NewImagingController(bare, "profile1")

	_, err = ctrl.GetImagingSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, attempt, "without credentials the single digest attempt carries no header")
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

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

	var wg sync.WaitGroup
	errs := make(chan error, 10)

	for range 10 {
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

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

	_, err := ctrl.GetImagingSettings(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "get imaging settings failed")
}

func TestToOnvifImagingSettings_ModeNormalization(t *testing.T) {
	t.Helper()
	mapped := toOnvifImagingSettings(ImagingSettings{
		Exposure:     ExposureSettings{Mode: "manual", ExposureTime: 0.02, Gain: 1},
		WhiteBalance: WhiteBalanceSettings{Mode: "auto"},
	})
	require.Equal(t, "MANUAL", mapped.Exposure.Mode)
	require.Equal(t, 0.02, mapped.Exposure.ExposureTime)
	require.Equal(t, 1.0, mapped.Exposure.Gain)
	require.Equal(t, "AUTO", mapped.WhiteBalance.Mode)
}

// TestToOnvifImagingSettings_IndependentGains ensures CrGain and CbGain stay
// independent values (they must not be forced equal — red and blue channel
// gains are distinct).
func TestToOnvifImagingSettings_IndependentGains(t *testing.T) {
	t.Helper()
	mapped := toOnvifImagingSettings(ImagingSettings{
		WhiteBalance: WhiteBalanceSettings{Mode: "manual", CrGain: 1.5, CbGain: 2.5},
	})
	require.Equal(t, 1.5, mapped.WhiteBalance.CrGain)
	require.Equal(t, 2.5, mapped.WhiteBalance.CbGain)
}

// TestToOnvifImagingSettings_ColorTemperatureFallback preserves legacy
// behavior: when only ColorTemperature is set (CrGain/CbGain both zero), it is
// used for both gains.
func TestToOnvifImagingSettings_ColorTemperatureFallback(t *testing.T) {
	t.Helper()
	mapped := toOnvifImagingSettings(ImagingSettings{
		WhiteBalance: WhiteBalanceSettings{Mode: "auto", ColorTemperature: 5500},
	})
	require.Equal(t, 5500.0, mapped.WhiteBalance.CrGain)
	require.Equal(t, 5500.0, mapped.WhiteBalance.CbGain)
}

func TestImagingController_SOAPBodyContainsProfileToken(t *testing.T) {
	var capturedBody string
	server := newImagingTestServer(t, func(body string, w http.ResponseWriter) {
		capturedBody = body
		w.Write([]byte(soapImagingSettingsResponse(0.5, 0.5, 0.5, 0.5)))
	})
	defer server.Close()

	ctrl := NewImagingController(newImagingTestClient(t, server), "my-video-token")

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

	ctrl := NewImagingController(newImagingTestClient(t, server), "profile1")

	err := ctrl.SetImagingSettings(context.Background(), ImagingSettings{
		Brightness: 0.75,
		Saturation: 0.50,
		Contrast:   0.25,
		Sharpness:  0.60,
	})
	require.NoError(t, err)
	require.Contains(t, capturedBody, "SetImagingSettings")
	require.Contains(t, capturedBody, "profile1")
	require.Contains(t, capturedBody, "0.75")
	require.Contains(t, capturedBody, "0.5")
	require.Contains(t, capturedBody, "0.25")
	require.Contains(t, capturedBody, "0.6")
}

func TestImagingController_ServerDown(t *testing.T) {
	t.Helper()
	// Use a port that won't be listening
	dead := &httptest.Server{URL: "http://127.0.0.1:1/imaging"}
	ctrl := NewImagingController(newImagingTestClient(t, dead), "profile1")

	_, err := ctrl.GetImagingSettings(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "get imaging settings failed")
}

func TestImagingController_ContextCancelled(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	dead := &httptest.Server{URL: "http://127.0.0.1:1/imaging"}
	ctrl := NewImagingController(newImagingTestClient(t, dead), "profile1")

	_, err := ctrl.GetImagingSettings(ctx)
	require.Error(t, err)
}
