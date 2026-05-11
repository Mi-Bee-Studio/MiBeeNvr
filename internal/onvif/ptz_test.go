package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"unsafe"
	"sync"
	"testing"
	"time"

	onvifgo "github.com/0x524a/onvif-go"
	"github.com/stretchr/testify/require"
)

// --- Existing Client stub tests ---

func newConnectedClient(t *testing.T) *Client {
	t.Helper()
	// Use a mock ONVIF server that returns valid GetCapabilities (required by Initialize)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tds:GetCapabilitiesResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><tds:Capabilities><tds:Media XAddr="http://host/media"/></tds:Capabilities></tds:GetCapabilitiesResponse></s:Body></s:Envelope>`)
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL, "admin", "password")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	return client
}

func TestPTZNotConnected(t *testing.T) {
	client := NewClient("http://localhost:8080/onvif/device_service", "admin", "password")
	ctx := context.Background()

	err := client.PTZContinuousMove(ctx, "profile1", PTZVector{Pan: 0.5})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")
}

func TestPTZStopNotConnected(t *testing.T) {
	client := NewClient("http://localhost:8080/onvif/device_service", "admin", "password")
	ctx := context.Background()

	err := client.PTZStop(ctx, "profile1")
	require.Error(t, err)
}

func TestPTZGetStatusNotImplemented(t *testing.T) {
	client := newConnectedClient(t)
	ctx := context.Background()

	_, err := client.PTZGetStatus(ctx, "profile1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not yet implemented")
}

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

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1")
	err := ctrl.ContinuousMove(context.Background(), PTZVector{Pan: 0.5, Tilt: 0.0, Zoom: 0.0})
	require.NoError(t, err)
}

func TestPTZController_AbsoluteMove_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "AbsoluteMove", action)
		w.Write([]byte(soapSuccessResponse("AbsoluteMove")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1")
	err := ctrl.AbsoluteMove(context.Background(), PTZVector{Pan: 0.0, Tilt: 0.5, Zoom: 1.0})
	require.NoError(t, err)
}

func TestPTZController_RelativeMove_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "RelativeMove", action)
		w.Write([]byte(soapSuccessResponse("RelativeMove")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1")
	err := ctrl.RelativeMove(context.Background(), PTZVector{Pan: -0.1, Tilt: 0.2, Zoom: 0.0})
	require.NoError(t, err)
}

func TestPTZController_Stop_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "Stop", action)
		w.Write([]byte(soapSuccessResponse("Stop")))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1")
	err := ctrl.Stop(context.Background(), true, true)
	require.NoError(t, err)
}

func TestPTZController_GetStatus_Success(t *testing.T) {
	server := newPTZTestServer(t, func(action string, w http.ResponseWriter) {
		require.Equal(t, "GetStatus", action)
		w.Write([]byte(soapGetStatusResponse("IDLE", "IDLE", 0.5, 0.3, 1.0)))
	})
	defer server.Close()

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1")
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

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1")
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

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1")

	var wg sync.WaitGroup
	errs := make(chan error, 20)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := ctrl.ContinuousMove(ctx, PTZVector{Pan: 0.1}); err != nil {
				errs <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1")
	err := ctrl.ContinuousMove(context.Background(), PTZVector{Pan: 0.5})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ContinuousMove failed")
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

	ctrl := NewPTZController(newTestOnvifClient(t, server), "profile1")
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
