package camera

// ONVIF client family coverage (#594): the getters (PTZ / imaging / snapshot /
// device manager), the PullPoint event subscription lifecycle, rediscovery
// guard paths, and the codec/accessor long tail — all against an in-package
// SOAP fake so no camera hardware is needed.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/stretchr/testify/require"
)

// --- in-package SOAP fake (same routing approach as internal/onvif's mock) ---

const camSOAPCaps = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tds:GetCapabilitiesResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
      <tds:Capabilities>
        <tds:Media XAddr="http://127.0.0.1/onvif/media_service"/>
        <tds:PTZ XAddr="http://127.0.0.1/onvif/ptz_service"/>
      </tds:Capabilities>
    </tds:GetCapabilitiesResponse>
  </s:Body>
</s:Envelope>`

const camSOAPProfiles = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <trt:GetProfilesResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
      <trt:Profiles token="profile_main">
        <tt:Name xmlns:tt="http://www.onvif.org/ver10/schema">Main</tt:Name>
        <trt:VideoSourceConfiguration token="vsc_1">
          <tt:Name xmlns:tt="http://www.onvif.org/ver10/schema">VSC</tt:Name>
          <tt:SourceToken xmlns:tt="http://www.onvif.org/ver10/schema">VideoSrcToken1</tt:SourceToken>
        </trt:VideoSourceConfiguration>
        <trt:VideoEncoderConfiguration token="enc_1">
          <tt:Encoding xmlns:tt="http://www.onvif.org/ver10/schema">H264</tt:Encoding>
          <tt:Resolution xmlns:tt="http://www.onvif.org/ver10/schema">
            <tt:Width>1920</tt:Width><tt:Height>1080</tt:Height>
          </tt:Resolution>
        </trt:VideoEncoderConfiguration>
      </trt:Profiles>
    </trt:GetProfilesResponse>
  </s:Body>
</s:Envelope>`

const camSOAPProfilesEmpty = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <trt:GetProfilesResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
    </trt:GetProfilesResponse>
  </s:Body>
</s:Envelope>`

const camSOAPDeviceInfo = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tds:GetDeviceInformationResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
      <tds:Manufacturer>FakeMfg</tds:Manufacturer>
      <tds:Model>FC-100</tds:Model>
      <tds:FirmwareVersion>9.9</tds:FirmwareVersion>
      <tds:SerialNumber>SN-CAM-1</tds:SerialNumber>
      <tds:HardwareId>HW1</tds:HardwareId>
    </tds:GetDeviceInformationResponse>
  </s:Body>
</s:Envelope>`

const camSOAPSnapshotURI = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <trt:GetSnapshotUriResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
      <trt:MediaUri>
        <tt:Uri xmlns:tt="http://www.onvif.org/ver10/schema">http://127.0.0.1/snapshot.jpg</tt:Uri>
      </trt:MediaUri>
    </trt:GetSnapshotUriResponse>
  </s:Body>
</s:Envelope>`

const camSOAPCreatePullPoint = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tev:CreatePullPointSubscriptionResponse xmlns:tev="http://www.onvif.org/ver10/events/wsdl">
      <tev:SubscriptionReference><wsa:Address xmlns:wsa="http://www.w3.org/2005/08/addressing">SUB_REF</wsa:Address></tev:SubscriptionReference>
      <wsnt:TerminationTime xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">TERM</wsnt:TerminationTime>
    </tev:CreatePullPointSubscriptionResponse>
  </s:Body>
</s:Envelope>`

const camSOAPFault = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body><s:Fault><s:Code><s:Value>s:Sender</s:Value></s:Code>
  <s:Reason><s:Text>nope</s:Text></s:Reason></s:Fault></s:Body>
</s:Envelope>`

// camSOAPFake routes ONVIF SOAP posts by body content. emptyProfiles switches
// the GetProfiles response; faulting marks every op as a SOAP fault.
type camSOAPFake struct {
	mu            sync.Mutex
	calls         map[string]int
	emptyProfiles bool
	faulting      bool
	srv           *httptest.Server
}

func newCamSOAPFake(t *testing.T) *camSOAPFake {
	t.Helper()
	f := &camSOAPFake{calls: make(map[string]int)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b := string(body)
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")

		action := func() string {
			for _, probe := range []string{
				"GetCapabilities", "GetProfiles", "GetDeviceInformation",
				"GetSnapshotUri", "CreatePullPointSubscription", "Unsubscribe", "GetSystemDateAndTime",
			} {
				if strings.Contains(b, probe) {
					return probe
				}
			}
			return "other"
		}()
		f.calls[action]++

		if f.faulting {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, camSOAPFault)
			return
		}
		switch action {
		case "GetCapabilities":
			fmt.Fprint(w, camSOAPCaps)
		case "GetProfiles":
			if f.emptyProfiles {
				fmt.Fprint(w, camSOAPProfilesEmpty)
			} else {
				fmt.Fprint(w, camSOAPProfiles)
			}
		case "GetDeviceInformation":
			fmt.Fprint(w, camSOAPDeviceInfo)
		case "GetSnapshotUri":
			fmt.Fprint(w, camSOAPSnapshotURI)
		case "CreatePullPointSubscription":
			resp := strings.ReplaceAll(camSOAPCreatePullPoint, "SUB_REF", f.srv.URL+"/sub-1")
			resp = strings.ReplaceAll(resp, "TERM", time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339))
			fmt.Fprint(w, resp)
		case "Unsubscribe":
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><wsnt:UnsubscribeResponse xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"/></s:Body></s:Envelope>`)
		default:
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body/></s:Envelope>`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *camSOAPFake) callCount(action string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[action]
}

func (f *camSOAPFake) setEmptyProfiles(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emptyProfiles = v
}

func (f *camSOAPFake) setFaulting(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faulting = v
}

func (f *camSOAPFake) onvifCamera(id string) config.CameraConfig {
	return config.CameraConfig{
		ID: id, Name: id, Protocol: "onvif", Encoding: "h264",
		ONVIFEndpoint: f.srv.URL + "/onvif/device_service",
		Username:      "admin", Password: "pw",
	}
}

// newCamManagerWith builds a manager whose config contains the given cameras
// (no recorder assertions — recorders start asynchronously against the fake).
func newCamManagerWith(t *testing.T, cams []config.CameraConfig) *CameraManager {
	t.Helper()
	cfg := testConfig()
	cfg.Cameras = cams
	cm, _, _, _ := newTestManagerWithCfg(t, cfg)
	return cm
}

func TestONVIFClientGettersHappyPath(t *testing.T) {
	t.Parallel()
	fake := newCamSOAPFake(t)
	cm := newCamManagerWith(t, []config.CameraConfig{fake.onvifCamera("onvif-happy")})
	ctx := context.Background()

	client, err := cm.GetONVIFClient(ctx, "onvif-happy")
	require.NoError(t, err)
	require.NotNil(t, client)

	// Cached client reused on the second getter call (single Connect).
	_, err = cm.GetONVIFClient(ctx, "onvif-happy")
	require.NoError(t, err)

	// Device info fetched once, then served from the cache.
	info := cm.GetCachedDeviceInfo(ctx, "onvif-happy")
	require.NotNil(t, info)
	require.Equal(t, "FakeMfg", info.Manufacturer)
	require.Equal(t, "SN-CAM-1", info.SerialNumber)
	require.NotNil(t, cm.GetCachedDeviceInfo(ctx, "onvif-happy"))
	require.Equal(t, 1, fake.callCount("GetDeviceInformation"))

	ptz, err := cm.GetONVIFPTZController(ctx, "onvif-happy")
	require.NoError(t, err)
	require.NotNil(t, ptz)

	img, err := cm.GetImagingController(ctx, "onvif-happy")
	require.NoError(t, err)
	require.NotNil(t, img)

	sp, err := cm.GetSnapshotProvider(ctx, "onvif-happy")
	require.NoError(t, err)
	require.NotNil(t, sp)
	uri, err := sp.GetSnapshotUri(ctx)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1/snapshot.jpg", uri)

	dm, err := cm.GetDeviceManager(ctx, "onvif-happy")
	require.NoError(t, err)
	require.NotNil(t, dm)

	// Closing evicts the cache; the next getter re-connects.
	cm.CloseONVIFClient("onvif-happy")
	_, err = cm.GetONVIFClient(ctx, "onvif-happy")
	require.NoError(t, err)
}

func TestONVIFGettersNoProfiles(t *testing.T) {
	t.Parallel()
	fake := newCamSOAPFake(t)
	fake.setEmptyProfiles(true)
	cm := newCamManagerWith(t, []config.CameraConfig{fake.onvifCamera("onvif-empty")})
	ctx := context.Background()

	for _, fn := range []func() error{
		func() error { _, err := cm.GetONVIFPTZController(ctx, "onvif-empty"); return err },
		func() error { _, err := cm.GetImagingController(ctx, "onvif-empty"); return err },
		func() error { _, err := cm.GetSnapshotProvider(ctx, "onvif-empty"); return err },
	} {
		require.Error(t, fn(), "empty profile list must fail the getter")
	}
}

func TestSubscribeONVIFEventsLifecycle(t *testing.T) {
	t.Parallel()
	fake := newCamSOAPFake(t)
	cm := newCamManagerWith(t, []config.CameraConfig{fake.onvifCamera("onvif-evt")})
	ctx := context.Background()

	require.NoError(t, cm.SubscribeONVIFEvents(ctx, "onvif-evt", func(onvif.ONVIFEvent) {}))
	// Double subscribe is a no-op success; only one PullPoint was created.
	require.NoError(t, cm.SubscribeONVIFEvents(ctx, "onvif-evt", func(onvif.ONVIFEvent) {}))
	require.Equal(t, 1, fake.callCount("CreatePullPointSubscription"))

	require.NoError(t, cm.UnsubscribeONVIFEvents(ctx, "onvif-evt"))
	require.Equal(t, 1, fake.callCount("Unsubscribe"))
	// Unsubscribing again is a no-op.
	require.NoError(t, cm.UnsubscribeONVIFEvents(ctx, "onvif-evt"))
	require.Equal(t, 1, fake.callCount("Unsubscribe"))

	// StopAll drains any remaining subscribers without error.
	require.NoError(t, cm.SubscribeONVIFEvents(ctx, "onvif-evt", func(onvif.ONVIFEvent) {}))
	cm.StopAllONVIFEvents(ctx)

	// A faulting device surfaces the subscription error.
	fake.setFaulting(true)
	err := cm.SubscribeONVIFEvents(ctx, "onvif-evt", func(onvif.ONVIFEvent) {})
	require.Error(t, err)
}

func TestRediscoverAndReconnectGuards(t *testing.T) {
	t.Parallel()
	fake := newCamSOAPFake(t)
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		fake.onvifCamera("onvif-redis"),
		{ID: "rtsp-cam", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x"},
	}
	// Kill the scan before any probe fires: MaxDuration=1ms expires the scan
	// context immediately → ErrNotFound → (false, nil) without touching the LAN.
	cfg.Health.Rediscovery.MaxDuration = "1ms"
	cfg.Health.Rediscovery.ProbeTimeout = "100ms"
	cm, _, _, _ := newTestManagerWithCfg(t, cfg)
	ctx := context.Background()

	// Unknown camera.
	found, err := cm.RediscoverAndReconnect(ctx, "ghost")
	require.Error(t, err)
	require.False(t, found)

	// Non-ONVIF protocol → skipped, no error.
	found, err = cm.RediscoverAndReconnect(ctx, "rtsp-cam")
	require.NoError(t, err)
	require.False(t, found)

	// ONVIF but no stable_id → skipped, no error.
	found, err = cm.RediscoverAndReconnect(ctx, "onvif-redis")
	require.NoError(t, err)
	require.False(t, found)

	// ONVIF + stable_id but the scan budget is exhausted → not found, no error.
	// (StableID set directly under configMu — the field ensureStableID
	// populates asynchronously in production.)
	cm.configMu.Lock()
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == "onvif-redis" {
			cm.cfg.Cameras[i].StableID = "SN-CAM-1"
		}
	}
	cm.configMu.Unlock()
	found, err = cm.RediscoverAndReconnect(ctx, "onvif-redis")
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, 0, fake.callCount("GetDeviceInformation"), "no probe may leave the process in the guard tests")
}

func TestAccessorLongtail(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "cam-a", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/a"},
		{ID: "cam-b", Protocol: "rtsp", Encoding: "h265", URL: "rtsp://127.0.0.1:1/b"},
	}
	cm := newCamManagerWith(t, cfg.Cameras)

	require.Equal(t, 2, cm.CameraCount())
	require.Nil(t, cm.GetTimelapseMergeMgr())
	require.Nil(t, cm.GetIngestRecorder("cam-a"))
	require.Nil(t, cm.GetIngestRecorder("ghost"))

	// Codec accessors work off the registered recorder (async reconnect means
	// the recorder exists even though the URL is dead).
	require.Equal(t, "", cm.GetSourceCodec("ghost"))
	require.Equal(t, model.CodecInfo{}, cm.GetCodecInfo("ghost"))

	// Config-backed stream URL for RTSP cameras.
	require.Equal(t, "rtsp://127.0.0.1:1/a", cm.GetStreamURL("cam-a"))
	require.Equal(t, "", cm.GetStreamURL("ghost"))
}
