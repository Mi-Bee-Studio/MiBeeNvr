package api

// ONVIF services happy-path coverage (#596): PTZ move/status/presets, imaging,
// device management (reboot/network/users), snapshot URI, profiles and
// capabilities — driven through a REAL camera.CameraManager whose ONVIF client
// connects to an in-test SOAP fake. onvif_services_test.go covers only the
// no-manager / not-found / non-ONVIF error branches; this file exercises the
// full request → SOAP → response chain hermetically (no camera hardware).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// --- SOAP fake (fixtures adapted from the proven internal/onvif test set) ---

const (
	apiPTZNS    = "http://www.onvif.org/ver20/ptz/wsdl"
	apiImgNS    = "http://www.onvif.org/ver20/imaging/wsdl"
	apiDeviceNS = "http://www.onvif.org/ver10/device/wsdl"
	apiSchemaNS = "http://www.onvif.org/ver10/schema"
)

func apiEnvelope(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Header/>
  <s:Body>` + body + `</s:Body>
</s:Envelope>`
}

func apiOpResponse(ns, action string) string {
	var prefix string
	switch ns {
	case apiPTZNS:
		prefix = "tptz"
	case apiImgNS:
		prefix = "timg"
	default:
		prefix = "tds"
	}
	return apiEnvelope(fmt.Sprintf(`<%s:%sResponse xmlns:%s="%s"/>`, prefix, action, prefix, ns))
}

// apiSOAPFake routes every ONVIF SOAP POST by request-body probe and counts
// calls. faults marks actions to answer with HTTP 400 + a SOAP Fault body
// (device-rejected shape used by the fault-tolerant handler branches).
type apiSOAPFake struct {
	mu     sync.Mutex
	calls  map[string]int
	faults map[string]bool
	srv    *httptest.Server
}

func newAPISOAPFake(t *testing.T) *apiSOAPFake {
	t.Helper()
	f := &apiSOAPFake{calls: make(map[string]int), faults: make(map[string]bool)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b := string(body)
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")

		action := ""
		for _, probe := range []string{
			"GetSystemDateAndTime", "GetCapabilities", "GetProfiles", "GetDeviceInformation",
			"GetSnapshotUri", "ContinuousMove", "AbsoluteMove", "RelativeMove", ":Stop",
			"GetStatus", "GetPresets", "SetPreset", "GotoPreset", "RemovePreset",
			"GetImagingSettings", "SetImagingSettings", "GetOptions",
			"SystemReboot", "GetNetworkInterfaces", "SetNetworkInterfaces",
			"GetUsers", "CreateUsers", "DeleteUsers", "SetUser",
		} {
			if strings.Contains(b, probe) {
				action = strings.TrimPrefix(probe, ":")
				break
			}
		}
		if action == "" {
			action = "other"
		}
		f.calls[action]++

		if f.faults[action] {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Reason><s:Text>operation not supported</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`)
			return
		}

		switch action {
		case "GetCapabilities":
			// XAddrs point back at this fake so PTZ/imaging ops route here too.
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<tds:GetCapabilitiesResponse xmlns:tds="%s"><tds:Capabilities>
<tds:Media XAddr="%s/onvif/media_service"/><tds:PTZ XAddr="%s/onvif/ptz_service"/>
</tds:Capabilities></tds:GetCapabilitiesResponse></s:Body></s:Envelope>`,
				apiDeviceNS, f.srv.URL, f.srv.URL)
		case "GetProfiles":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<trt:GetProfilesResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
<trt:Profiles token="profile_main">
<tt:Name xmlns:tt="%s">Main</tt:Name>
<trt:VideoSourceConfiguration token="vsc_1"><tt:SourceToken xmlns:tt="%s">VideoSrcToken1</tt:SourceToken></trt:VideoSourceConfiguration>
<trt:VideoEncoderConfiguration token="enc_1"><tt:Encoding xmlns:tt="%s">H264</tt:Encoding></trt:VideoEncoderConfiguration>
</trt:Profiles></trt:GetProfilesResponse></s:Body></s:Envelope>`,
				apiSchemaNS, apiSchemaNS, apiSchemaNS)
		case "GetDeviceInformation":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<tds:GetDeviceInformationResponse xmlns:tds="%s">
<tds:Manufacturer>FakeMfg</tds:Manufacturer><tds:Model>FC-100</tds:Model>
<tds:FirmwareVersion>9.9</tds:FirmwareVersion><tds:SerialNumber>SN-1</tds:SerialNumber>
</tds:GetDeviceInformationResponse></s:Body></s:Envelope>`, apiDeviceNS)
		case "GetSnapshotUri":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<trt:GetSnapshotUriResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl"><trt:MediaUri>
<tt:Uri xmlns:tt="%s">http://127.0.0.1/snapshot.jpg</tt:Uri></trt:MediaUri>
</trt:GetSnapshotUriResponse></s:Body></s:Envelope>`, apiSchemaNS)
		case "GetStatus":
			fmt.Fprintf(w, apiEnvelope(`<tptz:GetStatusResponse xmlns:tptz="%s">
<tptz:PTZStatus><tt:Position xmlns:tt="%s"><tt:PanTilt x="0.25" y="-0.5"/><tt:Zoom x="1.5"/></tt:Position>
<tt:MoveStatus xmlns:tt="%s"><tt:PanTilt>MOVING</tt:PanTilt><tt:Zoom>IDLE</tt:Zoom></tt:MoveStatus>
<tt:UtcTime xmlns:tt="%s">2025-01-01T00:00:00Z</tt:UtcTime></tptz:PTZStatus>
</tptz:GetStatusResponse>`), apiPTZNS, apiSchemaNS, apiSchemaNS, apiSchemaNS)
		case "GetPresets":
			fmt.Fprintf(w, apiEnvelope(`<tptz:GetPresetsResponse xmlns:tptz="%s">
<Preset token="1"><Name>Home</Name><PTZPosition xmlns:tt="%s"><tt:PanTilt x="0" y="0"/><tt:Zoom x="0"/></PTZPosition></Preset>
<Preset token="2"><Name>Gate</Name><PTZPosition xmlns:tt="%s"><tt:PanTilt x="0.5" y="0"/><tt:Zoom x="0"/></PTZPosition></Preset>
</tptz:GetPresetsResponse>`), apiPTZNS, apiSchemaNS, apiSchemaNS)
		case "SetPreset":
			fmt.Fprintf(w, apiEnvelope(`<tptz:SetPresetResponse xmlns:tptz="%s"><tptz:PresetToken>7</tptz:PresetToken></tptz:SetPresetResponse>`), apiPTZNS)
		case "GetImagingSettings":
			fmt.Fprintf(w, apiEnvelope(`<timg:GetImagingSettingsResponse xmlns:timg="%s">
<timg:ImagingSettings xmlns:tt="%s"><tt:Brightness>0.5</tt:Brightness><tt:ColorSaturation>0.4</tt:ColorSaturation>
<tt:Contrast>0.3</tt:Contrast><tt:Sharpness>0.2</tt:Sharpness></timg:ImagingSettings>
</timg:GetImagingSettingsResponse>`), apiImgNS, apiSchemaNS)
		case "GetOptions":
			fmt.Fprintf(w, apiEnvelope(`<timg:GetOptionsResponse xmlns:timg="%s">
<timg:ImagingOptions xmlns:tt="%s"><tt:Brightness><tt:Min>0</tt:Min><tt:Max>1</tt:Max></tt:Brightness>
<tt:Contrast><tt:Min>0</tt:Min><tt:Max>1</tt:Max></tt:Contrast></timg:ImagingOptions>
</timg:GetOptionsResponse>`), apiImgNS, apiSchemaNS)
		case "GetUsers":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<tds:GetUsersResponse xmlns:tds="%s">
<tds:User><tt:Username>admin</tt:Username><tt:UserLevel>Administrator</tt:UserLevel></tds:User>
<tds:User><tt:Username>operator</tt:Username><tt:UserLevel>Operator</tt:UserLevel></tds:User>
</tds:GetUsersResponse></s:Body></s:Envelope>`, apiDeviceNS)
		case "GetNetworkInterfaces":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<tds:GetNetworkInterfacesResponse xmlns:tds="%s"><tds:NetworkInterfaces token="eth0">
<tt:Enabled>true</tt:Enabled><tt:Info><tt:Name>eth0</tt:Name></tt:Info>
<tt:IPv4><tt:Enabled>true</tt:Enabled><tt:Config><tt:DHCP>false</tt:DHCP>
<tt:Manual><tt:Address>192.168.1.100</tt:Address><tt:PrefixLength>24</tt:PrefixLength></tt:Manual></tt:Config></tt:IPv4>
</tds:NetworkInterfaces></tds:GetNetworkInterfacesResponse></s:Body></s:Envelope>`, apiDeviceNS)
		case "SystemReboot":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<tds:SystemRebootResponse xmlns:tds="%s"><tds:Message>Rebooting now</tds:Message>
</tds:SystemRebootResponse></s:Body></s:Envelope>`, apiDeviceNS)
		case "ContinuousMove", "AbsoluteMove", "RelativeMove", "Stop",
			"GotoPreset", "RemovePreset", "SetImagingSettings",
			"SetNetworkInterfaces", "CreateUsers", "DeleteUsers", "SetUser":
			fmt.Fprint(w, apiOpResponse(pickNS(action), action))
		default:
			fmt.Fprint(w, apiEnvelope(""))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func pickNS(action string) string {
	switch action {
	case "ContinuousMove", "AbsoluteMove", "RelativeMove", "Stop", "GotoPreset", "RemovePreset":
		return apiPTZNS
	case "SetImagingSettings":
		return apiImgNS
	default:
		return apiDeviceNS
	}
}

func (f *apiSOAPFake) callCount(action string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[action]
}

func (f *apiSOAPFake) setFault(action string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults[action] = on
}

// apiONVIFEnv wires a Handler + real CameraManager against the SOAP fake.
type apiONVIFEnv struct {
	fake *apiSOAPFake
	h    *Handler
}

func newAPIONVIFEnv(t *testing.T) (*apiONVIFEnv, *camera.CameraManager) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	f := newAPISOAPFake(t)

	endpoint := f.srv.URL + "/onvif/device_service"
	cfg := &config.Config{Cameras: []config.CameraConfig{{
		ID:            "onvif-cam",
		Name:          "Fake ONVIF",
		Protocol:      "onvif",
		Encoding:      "h264",
		ONVIFEndpoint: endpoint,
		Username:      "admin",
		Password:      "pw",
	}}}
	cm := camera.NewCameraManager(cfg, store, db, filepath.Join(t.TempDir(), "cameras.yaml"))
	require.NoError(t, db.UpsertCamera(context.Background(), "onvif-cam", "Fake ONVIF", "onvif", "h264", "", "admin", "pw", endpoint, "", "", ""))

	h := TestHandler(db, store)
	t.Cleanup(h.Close)
	h.camMgr = cm
	return &apiONVIFEnv{fake: f, h: h}, cm
}

func (e *apiONVIFEnv) do(t *testing.T, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.h.Routes().ServeHTTP(w, req)
	return w
}

func TestAPIONVIFProfilesAndCapabilities(t *testing.T) {
	t.Parallel()
	env, _ := newAPIONVIFEnv(t)

	w := env.do(t, http.MethodGet, "/api/cameras/onvif-cam/onvif/profiles", "")
	require.Equal(t, http.StatusOK, w.Code)
	var profilesResp struct {
		Profiles []struct {
			Token string `json:"token"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &profilesResp))
	require.Len(t, profilesResp.Profiles, 1)
	require.Equal(t, "profile_main", profilesResp.Profiles[0].Token)

	w = env.do(t, http.MethodGet, "/api/cameras/onvif-cam/onvif/capabilities", "")
	require.Equal(t, http.StatusOK, w.Code)
	var capsResp struct {
		DeviceInfo struct {
			SerialNumber string `json:"serial_number"`
		} `json:"device_info"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &capsResp))
	require.Equal(t, "SN-1", capsResp.DeviceInfo.SerialNumber, "cached device info must be attached")
}

func TestAPIONVIFPTZMoveStopStatus(t *testing.T) {
	t.Parallel()
	env, _ := newAPIONVIFEnv(t)

	for _, mode := range []string{"continuous", "absolute", "relative"} {
		w := env.do(t, http.MethodPost, "/api/cameras/onvif-cam/ptz/move", `{"mode":"`+mode+`","pan":0.5,"tilt":-0.2,"zoom":0.1}`)
		require.Equal(t, http.StatusOK, w.Code, mode)
		require.Equal(t, 1, env.fake.callCount(map[string]string{
			"continuous": "ContinuousMove", "absolute": "AbsoluteMove", "relative": "RelativeMove",
		}[mode]))
	}
	// Validation: bad body / bad mode.
	require.Equal(t, http.StatusBadRequest, env.do(t, http.MethodPost, "/api/cameras/onvif-cam/ptz/move", "not json").Code)
	require.Equal(t, http.StatusBadRequest, env.do(t, http.MethodPost, "/api/cameras/onvif-cam/ptz/move", `{"mode":"warp"}`).Code)

	w := env.do(t, http.MethodPost, "/api/cameras/onvif-cam/ptz/stop", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, env.fake.callCount("Stop"))

	// Status: position mirrored from the fixture; MOVING pan/tilt ⇒ moving=true.
	w = env.do(t, http.MethodGet, "/api/cameras/onvif-cam/ptz/status", "")
	require.Equal(t, http.StatusOK, w.Code)
	var st struct {
		Pan    float64 `json:"pan"`
		Tilt   float64 `json:"tilt"`
		Zoom   float64 `json:"zoom"`
		Moving bool    `json:"moving"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &st))
	require.InDelta(t, 0.25, st.Pan, 1e-9)
	require.InDelta(t, -0.5, st.Tilt, 1e-9)
	require.InDelta(t, 1.5, st.Zoom, 1e-9)
	require.True(t, st.Moving)
}

// TestAPIONVIFPTZStatusFaultTolerance: a device that advertises PTZ but has no
// PTZ node answers GetStatus with 400 + Fault — the handler must return a
// default idle status, not a 500.
func TestAPIONVIFPTZStatusFaultTolerance(t *testing.T) {
	t.Parallel()
	env, _ := newAPIONVIFEnv(t)
	env.fake.setFault("GetStatus", true)

	w := env.do(t, http.MethodGet, "/api/cameras/onvif-cam/ptz/status", "")
	require.Equal(t, http.StatusOK, w.Code)
	var st struct {
		Moving bool `json:"moving"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &st))
	require.False(t, st.Moving)
}

func TestAPIONVIFPTZPresets(t *testing.T) {
	t.Parallel()
	env, _ := newAPIONVIFEnv(t)

	w := env.do(t, http.MethodGet, "/api/cameras/onvif-cam/ptz/presets", "")
	require.Equal(t, http.StatusOK, w.Code)
	var presetsResp struct {
		Presets []struct {
			Token string `json:"token"`
			Name  string `json:"name"`
		} `json:"presets"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &presetsResp))
	require.Len(t, presetsResp.Presets, 2)
	require.Equal(t, "Home", presetsResp.Presets[0].Name)

	w = env.do(t, http.MethodPost, "/api/cameras/onvif-cam/ptz/presets", `{"name":"porch"}`)
	require.Equal(t, http.StatusOK, w.Code)
	var created struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.Equal(t, "7", created.Token)
	// Validation: name required.
	require.Equal(t, http.StatusBadRequest, env.do(t, http.MethodPost, "/api/cameras/onvif-cam/ptz/presets", `{"name":""}`).Code)

	require.Equal(t, http.StatusOK, env.do(t, http.MethodPost, "/api/cameras/onvif-cam/ptz/presets/7/goto", "").Code)
	require.Equal(t, 1, env.fake.callCount("GotoPreset"))
	require.Equal(t, http.StatusOK, env.do(t, http.MethodDelete, "/api/cameras/onvif-cam/ptz/presets/7", "").Code)
	require.Equal(t, 1, env.fake.callCount("RemovePreset"))

	// Device-rejected SetPreset ⇒ 500 with the operation-specific message.
	env.fake.setFault("SetPreset", true)
	require.Equal(t, http.StatusInternalServerError, env.do(t, http.MethodPost, "/api/cameras/onvif-cam/ptz/presets", `{"name":"x"}`).Code)
}

func TestAPIONVIFSnapshotUri(t *testing.T) {
	t.Parallel()
	env, _ := newAPIONVIFEnv(t)

	w := env.do(t, http.MethodGet, "/api/cameras/onvif-cam/snapshot/uri", "")
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		URI string `json:"uri"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "http://127.0.0.1/snapshot.jpg", resp.URI)

	// Limited devices: GetSnapshotUri answered with a Fault ⇒ 404 "not supported".
	env.fake.setFault("GetSnapshotUri", true)
	require.Equal(t, http.StatusNotFound, env.do(t, http.MethodGet, "/api/cameras/onvif-cam/snapshot/uri", "").Code)
}

func TestAPIONVIFImaging(t *testing.T) {
	t.Parallel()
	env, _ := newAPIONVIFEnv(t)

	w := env.do(t, http.MethodGet, "/api/cameras/onvif-cam/imaging/settings", "")
	require.Equal(t, http.StatusOK, w.Code)
	var settings struct {
		Brightness float64 `json:"brightness"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &settings))
	require.InDelta(t, 0.5, settings.Brightness, 1e-9)

	require.Equal(t, http.StatusOK, env.do(t, http.MethodPut, "/api/cameras/onvif-cam/imaging/settings", `{"brightness":0.7,"contrast":0.2}`).Code)
	require.Equal(t, 1, env.fake.callCount("SetImagingSettings"))

	require.Equal(t, http.StatusOK, env.do(t, http.MethodGet, "/api/cameras/onvif-cam/imaging/options", "").Code)

	// Device failure surfaces as 502 Bad Gateway.
	env.fake.setFault("GetImagingSettings", true)
	require.Equal(t, http.StatusBadGateway, env.do(t, http.MethodGet, "/api/cameras/onvif-cam/imaging/settings", "").Code)
}

func TestAPIONVIFDeviceManagement(t *testing.T) {
	t.Parallel()
	env, _ := newAPIONVIFEnv(t)

	require.Equal(t, http.StatusOK, env.do(t, http.MethodPost, "/api/cameras/onvif-cam/onvif/reboot", "").Code)

	w := env.do(t, http.MethodGet, "/api/cameras/onvif-cam/onvif/network", "")
	require.Equal(t, http.StatusOK, w.Code)
	var netResp struct {
		Interfaces []struct {
			Name string `json:"name"`
		} `json:"interfaces"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &netResp))
	require.Len(t, netResp.Interfaces, 1)
	require.Equal(t, "eth0", netResp.Interfaces[0].Name)

	// SetNetworkInterfaces is a stub that always reports ErrUnsupported on
	// this onvif-go version — the handler must surface 501, not a 500/502.
	require.Equal(t, http.StatusNotImplemented, env.do(t, http.MethodPut, "/api/cameras/onvif-cam/onvif/network", `{"interfaces":[{"name":"eth0","enabled":true}]}`).Code)

	w = env.do(t, http.MethodGet, "/api/cameras/onvif-cam/onvif/users", "")
	require.Equal(t, http.StatusOK, w.Code)
	var usersResp struct {
		Users []struct {
			Username string `json:"username"`
		} `json:"users"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &usersResp))
	require.Len(t, usersResp.Users, 2)

	require.Equal(t, http.StatusOK, env.do(t, http.MethodPost, "/api/cameras/onvif-cam/onvif/users", `{"users":[{"username":"newuser","level":"User"}]}`).Code)
	require.Equal(t, http.StatusOK, env.do(t, http.MethodDelete, "/api/cameras/onvif-cam/onvif/users", `{"usernames":["newuser"]}`).Code)
	require.Equal(t, http.StatusOK, env.do(t, http.MethodPut, "/api/cameras/onvif-cam/onvif/users/admin", `{"password":"np"}`).Code)

	// Device-side reboot failure ⇒ 502 with context.
	env.fake.setFault("SystemReboot", true)
	require.Equal(t, http.StatusBadGateway, env.do(t, http.MethodPost, "/api/cameras/onvif-cam/onvif/reboot", "").Code)
}

// TestAPIONVIFConnectionError: with the device gone, the client connect fails
// and the handlers map ONVIFConnectionError to 502 Bad Gateway.
func TestAPIONVIFConnectionError(t *testing.T) {
	t.Parallel()
	env, cm := newAPIONVIFEnv(t)
	cm.CloseONVIFClient("onvif-cam")
	env.fake.srv.Close()

	require.Equal(t, http.StatusBadGateway, env.do(t, http.MethodGet, "/api/cameras/onvif-cam/ptz/status", "").Code)
}
