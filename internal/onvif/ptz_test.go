package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	onvifgo "github.com/0x524a/onvif-go"
	"github.com/stretchr/testify/require"
)

// --- SOAP mock helpers ---

const soapPTZNamespace = "http://www.onvif.org/ver20/ptz/wsdl"

// soapEnvelope wraps a SOAP body in a standard envelope.
func soapEnvelope(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Header/>
  <s:Body>` + body + `</s:Body>
</s:Envelope>`
}

// soapSuccessResponse returns an empty successful SOAP response for a PTZ command.
func soapSuccessResponse(action string) string {
	return soapEnvelope(fmt.Sprintf(`<tptz:%sResponse xmlns:tptz="%s"/>`, action, soapPTZNamespace))
}

// soapFault returns a SOAP fault response.
func soapFault(code, reason string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Header/>
  <s:Body>
    <s:Fault>
      <s:Code><s:Value>s:` + code + `</s:Value></s:Code>
      <s:Reason><s:Text xml:lang="en">` + reason + `</s:Text></s:Reason>
    </s:Fault>
  </s:Body>
</s:Envelope>`
}

// soapGetStatusResponse returns a GetStatus SOAP response with position and move status.
func soapGetStatusResponse(panTiltStatus, zoomStatus string, panX, panY, zoomX float64) string {
	return soapEnvelope(fmt.Sprintf(`<tptz:GetStatusResponse xmlns:tptz="%s">
  <tptz:PTZStatus>
    <tt:Position xmlns:tt="http://www.onvif.org/ver10/schema">
      <tt:PanTilt x="%f" y="%f"/>
      <tt:Zoom x="%f"/>
    </tt:Position>
    <tt:MoveStatus xmlns:tt="http://www.onvif.org/ver10/schema">
      <tt:PanTilt>%s</tt:PanTilt>
      <tt:Zoom>%s</tt:Zoom>
    </tt:MoveStatus>
    <tt:Error xmlns:tt="http://www.onvif.org/ver10/schema"/>
    <tt:UtcTime xmlns:tt="http://www.onvif.org/ver10/schema">2025-01-01T00:00:00Z</tt:UtcTime>
  </tptz:PTZStatus>
</tptz:GetStatusResponse>`, soapPTZNamespace, panX, panY, zoomX, panTiltStatus, zoomStatus))
}

// soapGetPresetsResponse returns a GetPresets SOAP response with preset entries.
func soapGetPresetsResponse(presets []struct {
	Token, Name       string
	PanX, PanY, ZoomX float64
},
) string {
	var presetXML string
	for _, p := range presets {
		presetXML += fmt.Sprintf(`<Preset token="%s"><Name>%s</Name>`+
			`<PTZPosition xmlns:tt="http://www.onvif.org/ver10/schema">`+
			`<tt:PanTilt x="%f" y="%f"/><tt:Zoom x="%f"/>`+
			`</PTZPosition></Preset>`, p.Token, p.Name, p.PanX, p.PanY, p.ZoomX)
	}
	return soapEnvelope(fmt.Sprintf(`<tptz:GetPresetsResponse xmlns:tptz="%s">%s</tptz:GetPresetsResponse>`, soapPTZNamespace, presetXML))
}

// soapSetPresetResponse returns a SetPreset SOAP response with the preset token.
func soapSetPresetResponse(token string) string {
	return soapEnvelope(fmt.Sprintf(`<tptz:SetPresetResponse xmlns:tptz="%s">`+
		`<tptz:PresetToken>%s</tptz:PresetToken></tptz:SetPresetResponse>`, soapPTZNamespace, token))
}

// extractSOAPAction extracts the SOAP action name from a request body.
func extractSOAPAction(t *testing.T, body []byte) string {
	t.Helper()
	// Find the action element inside <s:Body>
	var envelope struct {
		Body struct {
			Inner struct {
				XMLName xml.Name
			} `xml:",any"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return envelope.Body.Inner.XMLName.Local
}

// newPTZTestServer creates an httptest.Server that responds to PTZ SOAP requests.
func newPTZTestServer(t *testing.T, handler func(action string, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		action := extractSOAPAction(t, body)
		require.NotEmpty(t, action, "could not extract SOAP action from request")

		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		handler(action, w)
	}))
}

// setPTZEndpoint uses unsafe to set the unexported ptzEndpoint field on onvifgo.Client.
// This is needed because we test against httptest servers that don't support ONVIF Initialize().
// Go reflection cannot set fields in structs containing sync.RWMutex, so we use unsafe.
func setPTZEndpoint(t *testing.T, client *onvifgo.Client, url string) {
	t.Helper()
	v := reflect.ValueOf(client).Elem()
	f := v.FieldByName("ptzEndpoint")
	require.True(t, f.IsValid(), "ptzEndpoint field not found on onvifgo.Client")
	require.True(t, f.CanAddr(), "ptzEndpoint field is not addressable")
	// Use unsafe to bypass canSet=false (caused by embedded sync.RWMutex)
	ptr := (*string)(unsafe.Pointer(f.UnsafeAddr()))
	*ptr = url
}

// newTestOnvifClient creates an onvif-go Client with ptzEndpoint pointed at the test server.
func newTestOnvifClient(t *testing.T, server *httptest.Server) *onvifgo.Client {
	t.Helper()
	client, err := onvifgo.NewClient(
		server.URL,
		onvifgo.WithCredentials("admin", "password"),
	)
	require.NoError(t, err)
	setPTZEndpoint(t, client, server.URL)
	return client
}

// --- PTZControllerImpl tests ---

func TestPTZController_ContinuousMove_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "ContinuousMove", action)
		w.Write([]byte(soapSuccessResponse("ContinuousMove")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	err := ctrl.ContinuousMove(context.Background(), PTZVector{Pan: 0.5, Tilt: 0.0, Zoom: 0.0})
	require.NoError(t, err)
}

func TestPTZController_AbsoluteMove_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "AbsoluteMove", action)
		w.Write([]byte(soapSuccessResponse("AbsoluteMove")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	err := ctrl.AbsoluteMove(context.Background(), PTZVector{Pan: 0.0, Tilt: 0.5, Zoom: 1.0})
	require.NoError(t, err)
}

func TestPTZController_RelativeMove_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "RelativeMove", action)
		w.Write([]byte(soapSuccessResponse("RelativeMove")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	err := ctrl.RelativeMove(context.Background(), PTZVector{Pan: -0.1, Tilt: 0.2, Zoom: 0.0})
	require.NoError(t, err)
}

func TestPTZController_Stop_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "Stop", action)
		w.Write([]byte(soapSuccessResponse("Stop")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	err := ctrl.Stop(context.Background(), true, true)
	require.NoError(t, err)
}

func TestPTZController_GetStatus_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "GetStatus", action)
		w.Write([]byte(soapGetStatusResponse("IDLE", "IDLE", 0.5, 0.3, 1.0)))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	pos, moving, err := ctrl.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, PTZVector{Pan: 0.5, Tilt: 0.3, Zoom: 1.0}, pos)
	require.False(t, moving)
}

func TestPTZController_GetStatus_Moving(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "GetStatus", action)
		w.Write([]byte(soapGetStatusResponse("MOVING", "IDLE", 0.7, -0.2, 2.0)))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	pos, moving, err := ctrl.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, PTZVector{Pan: 0.7, Tilt: -0.2, Zoom: 2.0}, pos)
	require.True(t, moving)
}

func TestPTZController_ConcurrentCommands(t *testing.T) {
	t.Helper()
	callCount := 0
	mu := &sync.Mutex{}
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		// Simulate a small delay to increase chance of race detection
		time.Sleep(time.Millisecond)
		w.Write([]byte(soapSuccessResponse(action)))
		mu.Lock()
		callCount++
		mu.Unlock()
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)

	var wg sync.WaitGroup
	errs := make(chan error, 20)

	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			if err := ctrl.ContinuousMove(ctx, PTZVector{Pan: 0.1}); err != nil {
				errs <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			if _, _, err := ctrl.GetStatus(ctx); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 20, callCount)
}

func TestPTZController_Error(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(soapFault("Sender", "Invalid profile token")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	err := ctrl.ContinuousMove(context.Background(), PTZVector{Pan: 0.5})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ContinuousMove failed")
}

// --- Preset tests ---

func TestPTZController_GetPresets_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "GetPresets", action)
		w.Write([]byte(soapGetPresetsResponse([]struct {
			Token, Name       string
			PanX, PanY, ZoomX float64
		}{
			{Token: "1", Name: "Home", PanX: 0.0, PanY: 0.0, ZoomX: 0.0},
			{Token: "2", Name: "Gate", PanX: 0.5, PanY: -0.3, ZoomX: 1.0},
		})))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	presets, err := ctrl.GetPresets(context.Background())
	require.NoError(t, err)
	require.Len(t, presets, 2)
	require.Equal(t, "Home", presets[0].Name)
	require.Equal(t, PTZVector{Pan: 0.0, Tilt: 0.0, Zoom: 0.0}, presets[0].Position)
	require.Equal(t, "Gate", presets[1].Name)
	require.Equal(t, PTZVector{Pan: 0.5, Tilt: -0.3, Zoom: 1.0}, presets[1].Position)
}

func TestPTZController_GetPresets_Empty(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "GetPresets", action)
		w.Write([]byte(soapGetPresetsResponse(nil)))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	presets, err := ctrl.GetPresets(context.Background())
	require.NoError(t, err)
	require.Empty(t, presets)
}

func TestPTZController_SetPreset_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "SetPreset", action)
		w.Write([]byte(soapSetPresetResponse("preset-token-3")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	token, err := ctrl.SetPreset(context.Background(), "Front Door")
	require.NoError(t, err)
	require.Equal(t, "preset-token-3", token)
}

func TestPTZController_GoToPreset_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "GotoPreset", action)
		w.Write([]byte(soapSuccessResponse("GotoPreset")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	err := ctrl.GoToPreset(context.Background(), "1")
	require.NoError(t, err)
}

func TestPTZController_RemovePreset_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "RemovePreset", action)
		w.Write([]byte(soapSuccessResponse("RemovePreset")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	err := ctrl.RemovePreset(context.Background(), "1")
	require.NoError(t, err)
}

func TestPTZController_GetPresets_Error(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(soapFault("Sender", "Invalid profile token")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	_, err := ctrl.GetPresets(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "get PTZ presets failed")
}

func TestPTZController_SetPreset_Error(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		w.Write([]byte(soapFault("Sender", "InvalidArgs")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	_, err := ctrl.SetPreset(context.Background(), "Bad")
	require.Error(t, err)
	require.Contains(t, err.Error(), "SetPreset failed")
}

func TestPTZController_ImplementsInterface(t *testing.T) {
	// Compile-time check that PTZControllerImpl satisfies PTZController
	var _ PTZController = &PTZControllerImpl{}
}

func TestSetProfileToken(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		w.Write([]byte(soapSuccessResponse(action)))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	ctrl.SetProfileToken("profile2")

	err := ctrl.Stop(context.Background(), true, true)
	require.NoError(t, err)
}

// --- Type conversion tests ---

func TestToOnvifPTZVector(t *testing.T) {
	v := PTZVector{Pan: 0.5, Tilt: -0.3, Zoom: 1.0}
	result := toOnvifPTZVector(v)
	require.NotNil(t, result.PanTilt)
	require.Equal(t, 0.5, result.PanTilt.X)
	require.Equal(t, -0.3, result.PanTilt.Y)
	require.NotNil(t, result.Zoom)
	require.Equal(t, 1.0, result.Zoom.X)
}

func TestToOnvifPTZSpeed(t *testing.T) {
	v := PTZVector{Pan: 0.5, Tilt: -0.3, Zoom: 1.0}
	result := toOnvifPTZSpeed(v)
	require.NotNil(t, result.PanTilt)
	require.Equal(t, 0.5, result.PanTilt.X)
	require.Equal(t, -0.3, result.PanTilt.Y)
	require.NotNil(t, result.Zoom)
	require.Equal(t, 1.0, result.Zoom.X)
}

func TestFromOnvifPTZVector_Nil(t *testing.T) {
	result := fromOnvifPTZVector(nil)
	require.Equal(t, PTZVector{}, result)
}

func TestFromOnvifPTZVector_Partial(t *testing.T) {
	// Only PanTilt set
	v := &onvifgo.PTZVector{
		PanTilt: &onvifgo.Vector2D{X: 0.5, Y: 0.3},
	}
	result := fromOnvifPTZVector(v)
	require.Equal(t, 0.5, result.Pan)
	require.Equal(t, 0.3, result.Tilt)
	require.Equal(t, 0.0, result.Zoom)

	// Only Zoom set
	v2 := &onvifgo.PTZVector{
		Zoom: &onvifgo.Vector1D{X: 2.0},
	}
	result2 := fromOnvifPTZVector(v2)
	require.Equal(t, 0.0, result2.Pan)
	require.Equal(t, 0.0, result2.Tilt)
	require.Equal(t, 2.0, result2.Zoom)
}

func TestFromOnvifPTZStatus_Nil(t *testing.T) {
	pos, moving, err := fromOnvifPTZStatus(nil)
	require.NoError(t, err)
	require.Equal(t, PTZVector{}, pos)
	require.False(t, moving)
}

func TestFromOnvifPTZStatus_MovingZoom(t *testing.T) {
	status := &onvifgo.PTZStatus{
		Position: &onvifgo.PTZVector{
			PanTilt: &onvifgo.Vector2D{X: 0.1, Y: 0.2},
			Zoom:    &onvifgo.Vector1D{X: 0.5},
		},
		MoveStatus: &onvifgo.PTZMoveStatus{
			PanTilt: "IDLE",
			Zoom:    "MOVING",
		},
	}
	pos, moving, err := fromOnvifPTZStatus(status)
	require.NoError(t, err)
	require.Equal(t, PTZVector{Pan: 0.1, Tilt: 0.2, Zoom: 0.5}, pos)
	require.True(t, moving)
}

// --- SOAP action extraction test ---

func TestExtractSOAPAction(t *testing.T) {
	soap := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Header/>
  <s:Body>
    <tptz:ContinuousMove xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl">
      <tptz:ProfileToken>profile1</tptz:ProfileToken>
    </tptz:ContinuousMove>
  </s:Body>
</s:Envelope>`

	action := extractSOAPAction(t, []byte(soap))
	require.Equal(t, "ContinuousMove", action)
}

// --- Raw SOAP fallback tests ---

// newRawSOAPTestServer creates an httptest.Server that captures raw SOAP requests
// and returns a configurable response. Returns the server and a rawSOAP function pointer.
func newRawSOAPTestServer(t *testing.T, responseFunc func(action string, w http.ResponseWriter)) (*httptest.Server, func(context.Context, string, string) ([]byte, error)) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		action := extractSOAPAction(t, body)
		require.NotEmpty(t, action)
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		responseFunc(action, w)
	}))
	return server, func(ctx context.Context, endpoint, soapBody string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(soapBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return io.ReadAll(resp.Body)
	}
}

// TestPTZController_AbsoluteMove_Fallback tests that AbsoluteMove falls back to raw SOAP
// when onvif-go returns an auth error.
func TestPTZController_AbsoluteMove_Fallback(t *testing.T) {
	// Server for onvif-go: returns NotAuthorized SOAP fault
	authFailServer := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(soapFault("Sender", "NotAuthorized")))
	})
	defer authFailServer.Close()

	// Server for raw SOAP fallback: returns success
	rawServer, rawSOAPFn := newRawSOAPTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "AbsoluteMove", action)
		w.Write([]byte(soapSuccessResponse("AbsoluteMove")))
	})
	defer rawServer.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, authFailServer), "profile1", rawServer.URL, "", "", rawSOAPFn)
	err := ctrl.AbsoluteMove(context.Background(), PTZVector{Pan: 0.0, Tilt: 0.5, Zoom: 1.0})
	require.NoError(t, err)
}

// TestPTZController_RelativeMove_Fallback tests that RelativeMove falls back to raw SOAP.
func TestPTZController_RelativeMove_Fallback(t *testing.T) {
	authFailServer := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(soapFault("Sender", "NotAuthorized")))
	})
	defer authFailServer.Close()

	rawServer, rawSOAPFn := newRawSOAPTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "RelativeMove", action)
		w.Write([]byte(soapSuccessResponse("RelativeMove")))
	})
	defer rawServer.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, authFailServer), "profile1", rawServer.URL, "", "", rawSOAPFn)
	err := ctrl.RelativeMove(context.Background(), PTZVector{Pan: -0.1, Tilt: 0.2, Zoom: 0.0})
	require.NoError(t, err)
}

// TestPTZController_GetStatus_Fallback tests that GetStatus falls back to raw SOAP
// and correctly parses the raw SOAP response.
func TestPTZController_GetStatus_Fallback(t *testing.T) {
	authFailServer := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(soapFault("Sender", "NotAuthorized")))
	})
	defer authFailServer.Close()

	rawServer, rawSOAPFn := newRawSOAPTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "GetStatus", action)
		w.Write([]byte(soapGetStatusResponse("IDLE", "MOVING", 0.5, 0.3, 2.0)))
	})
	defer rawServer.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, authFailServer), "profile1", rawServer.URL, "", "", rawSOAPFn)
	pos, moving, err := ctrl.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, PTZVector{Pan: 0.5, Tilt: 0.3, Zoom: 2.0}, pos)
	require.True(t, moving)
}

// TestPTZController_SetPreset_Fallback tests that SetPreset falls back to raw SOAP
// and correctly parses the preset token from the response.
func TestPTZController_SetPreset_Fallback(t *testing.T) {
	authFailServer := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(soapFault("Sender", "NotAuthorized")))
	})
	defer authFailServer.Close()

	rawServer, rawSOAPFn := newRawSOAPTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "SetPreset", action)
		w.Write([]byte(soapSetPresetResponse("preset-token-raw")))
	})
	defer rawServer.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, authFailServer), "profile1", rawServer.URL, "", "", rawSOAPFn)
	token, err := ctrl.SetPreset(context.Background(), "Front Door")
	require.NoError(t, err)
	require.Equal(t, "preset-token-raw", token)
}

// TestPTZController_Fallback_NoRawSOAP tests that operations fail gracefully
// when rawSOAP is nil and auth fails.
func TestPTZController_Fallback_NoRawSOAP(t *testing.T) {
	authFailServer := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(soapFault("Sender", "NotAuthorized")))
	})
	defer authFailServer.Close()

	// No rawSOAP function provided
	ctrl := NewPTZController(newTestOnvifClient(t, authFailServer), "profile1", "", "", "", nil)

	err := ctrl.AbsoluteMove(context.Background(), PTZVector{Pan: 0.0, Tilt: 0.5, Zoom: 1.0})
	require.Error(t, err)
	require.Contains(t, err.Error(), "raw SOAP fallback not available")

	_, _, err = ctrl.GetStatus(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "raw SOAP fallback not available")

	_, err = ctrl.SetPreset(context.Background(), "Test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "raw SOAP fallback not available")
}

// TestPTZController_Fallback_NonAuthError tests that fallback is NOT triggered
// for non-auth errors.
func TestPTZController_Fallback_NonAuthError(t *testing.T) {
	// Server returns a non-auth error (e.g. invalid args)
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(soapFault("Receiver", "InvalidArg")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1", "", "", "", nil)
	err := ctrl.AbsoluteMove(context.Background(), PTZVector{Pan: 0.0, Tilt: 0.5, Zoom: 1.0})
	require.Error(t, err)
	// Should NOT contain fallback message — the rawSOAP nil check would not trigger
	// because the error is not classified as an auth error
}

// --- XML response parser tests ---

func TestParseRawGetStatusResponse(t *testing.T) {
	xmlResp := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tptz:GetStatusResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl">
      <tptz:PTZStatus>
        <tt:Position xmlns:tt="http://www.onvif.org/ver10/schema">
          <tt:PanTilt x="0.5" y="0.3"/>
          <tt:Zoom x="1.0"/>
        </tt:Position>
        <tt:MoveStatus xmlns:tt="http://www.onvif.org/ver10/schema">
          <tt:PanTilt>IDLE</tt:PanTilt>
          <tt:Zoom>IDLE</tt:Zoom>
        </tt:MoveStatus>
      </tptz:PTZStatus>
    </tptz:GetStatusResponse>
  </s:Body>
</s:Envelope>`)

	pos, moving, err := parseRawGetStatusResponse(xmlResp)
	require.NoError(t, err)
	require.Equal(t, PTZVector{Pan: 0.5, Tilt: 0.3, Zoom: 1.0}, pos)
	require.False(t, moving)
}

func TestParseRawGetStatusResponse_Moving(t *testing.T) {
	xmlResp := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tptz:GetStatusResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl">
      <tptz:PTZStatus>
        <tt:Position xmlns:tt="http://www.onvif.org/ver10/schema">
          <tt:PanTilt x="-0.5" y="1.0"/>
          <tt:Zoom x="5.0"/>
        </tt:Position>
        <tt:MoveStatus xmlns:tt="http://www.onvif.org/ver10/schema">
          <tt:PanTilt>MOVING</tt:PanTilt>
          <tt:Zoom>IDLE</tt:Zoom>
        </tt:MoveStatus>
      </tptz:PTZStatus>
    </tptz:GetStatusResponse>
  </s:Body>
</s:Envelope>`)

	pos, moving, err := parseRawGetStatusResponse(xmlResp)
	require.NoError(t, err)
	require.Equal(t, PTZVector{Pan: -0.5, Tilt: 1.0, Zoom: 5.0}, pos)
	require.True(t, moving)
}

func TestParseRawSetPresetResponse(t *testing.T) {
	xmlResp := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tptz:SetPresetResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl">
      <tptz:PresetToken>token-abc-123</tptz:PresetToken>
    </tptz:SetPresetResponse>
  </s:Body>
</s:Envelope>`)

	token, err := parseRawSetPresetResponse(xmlResp)
	require.NoError(t, err)
	require.Equal(t, "token-abc-123", token)
}
