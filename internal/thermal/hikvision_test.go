package thermal

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // HTTP Digest mandates MD5
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/require"
)

// hikMultipart renders a radiometric reply the way a Hikvision DS-2TB2617
// (V5.5.38) frames it: boundary=boundary, JSON descriptor, thermal JPEG,
// float32 matrix, then (optionally) the visible JPEG.
func hikMultipart(cols, rows int, values []float32, frozen, visible bool) (string, []byte) {
	var b bytes.Buffer
	meta := fmt.Sprintf(`{
	"JpegPictureWithAppendData":	{
		"channel":	2,
		"jpegPicLen":	4,
		"jpegPicWidth":	%d,
		"jpegPicHeight":	%d,
		"p2pDataLen":	%d,
		"isFreezedata":	%t,
		"temperatureDataLength":	4,
		"visiblePicLen":	4,
		"ThermalValidRect":	{"height": 1.000, "width": 1.000, "x": 0.000, "y": 0.000},
		"VisibleValidRect":	{"height": 0.858, "width": 0.646, "x": 0.149, "y": 0.110}
	}
}`, cols, rows, cols*rows*4, frozen)
	fmt.Fprintf(&b, "--boundary\r\nContent-Type: application/json; charset=\"UTF-8\"\r\nContent-Length: %d\r\n\r\n%s\r\n", len(meta), meta)
	fmt.Fprintf(&b, "--boundary\r\nContent-Disposition: form-data;\r\nContent-Type: image/pjpeg\r\nContent-Length:4\r\n\r\n\xff\xd8\xff\xd9\r\n")
	raw := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(v))
	}
	fmt.Fprintf(&b, "--boundary\r\nContent-Disposition: form-data;\r\nContent-Type: application/octet-stream\r\nContent-Length:%d\r\n\r\n", len(raw))
	b.Write(raw)
	b.WriteString("\r\n")
	if visible {
		fmt.Fprintf(&b, "--boundary\r\nContent-Disposition: form-data;\r\nContent-Type: image/pjpeg\r\nContent-Length:4\r\n\r\n\xff\xd8\xff\xd9\r\n")
	}
	b.WriteString("--boundary--\r\n")
	return "multipart/form-data; boundary=boundary", b.Bytes()
}

// scene builds a cols×rows matrix at 25 °C with one hot and one cold cell.
func scene(cols, rows int) []float32 {
	v := make([]float32, cols*rows)
	for i := range v {
		v[i] = 25
	}
	v[min(10, rows-1)*cols+min(20, cols-1)] = 61.4 // hot: x=20 y=10 (clamped on tiny frames)
	v[(rows-1)*cols] = 9.6                         // cold: x=0 y=rows-1
	return v
}

func TestParseHikvisionFrame(t *testing.T) {
	ct, body := hikMultipart(160, 120, scene(160, 120), false, true)
	f, err := ParseHikvisionFrame(ct, bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, 160, f.Cols)
	require.Equal(t, 120, f.Rows)
	require.False(t, f.Frozen)
	require.NotNil(t, f.VisibleRect)
	require.InDelta(t, 0.149, f.VisibleRect.X, 1e-9)
	require.InDelta(t, 0.858, f.VisibleRect.H, 1e-9)

	r, ok := f.Reading("cam-t")
	require.True(t, ok)
	require.InDelta(t, 61.4, *r.MaxC, 1e-9)
	require.InDelta(t, 9.6, *r.MinC, 1e-9)
	require.Len(t, r.Spots, 1)
	require.Equal(t, "center", r.Spots[0].Name)
	require.InDelta(t, 25, r.Spots[0].TempC, 1e-9)

	p := f.FramePayload()
	grid := p["grid"].(map[string]any)
	require.Equal(t, 160, grid["cols"])
	require.Equal(t, 120, grid["rows"])
	require.Len(t, grid["values"], 160*120)
	require.Equal(t, map[string]any{"value_c": 61.4, "x": 20, "y": 10}, p["max"])
	require.Equal(t, map[string]any{"value_c": 9.6, "x": 0, "y": 119}, p["min"])
	require.Equal(t, f.VisibleRect, p["visible_rect"])
}

func TestParseHikvisionFrame_Rejects(t *testing.T) {
	// Not multipart.
	_, err := ParseHikvisionFrame("application/json", strings.NewReader("{}"))
	require.Error(t, err)

	// 2-byte matrices are not self-describing: refused with a hint.
	ct, body := hikMultipart(4, 3, scene(4, 3), false, false)
	body = bytes.Replace(body, []byte(`"temperatureDataLength":	4`), []byte(`"temperatureDataLength":	2`), 1)
	_, err = ParseHikvisionFrame(ct, bytes.NewReader(body))
	require.ErrorContains(t, err, "temperatureDataLength 2")

	// Truncated matrix.
	ct, body = hikMultipart(4, 3, scene(4, 3), false, false)
	cut := bytes.Index(body, []byte("octet-stream"))
	_, err = ParseHikvisionFrame(ct, bytes.NewReader(body[:cut+60]))
	require.Error(t, err)
}

// digestDevice serves the radiometric endpoint behind RFC 7616 Digest
// (qop=auth), counting challenges and verifying every response hash.
type digestDevice struct {
	user, pass string
	nonce      atomic.Value
	challenges atomic.Int32
	ok         atomic.Int32
	frozen     bool
}

func (d *digestDevice) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nonce, _ := d.nonce.Load().(string)
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Digest ") || !d.valid(auth[len("Digest "):], r, nonce) {
			d.challenges.Add(1)
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest qop="auth", realm="IP Camera(TEST)", nonce=%q, stale="FALSE"`, nonce))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		d.ok.Add(1)
		ct, body := hikMultipart(32, 24, scene(32, 24), d.frozen, false)
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(body)
	}
}

func (d *digestDevice) valid(params string, r *http.Request, nonce string) bool {
	p := parseAuthParams(params)
	if p["username"] != d.user || p["nonce"] != nonce || p["uri"] != r.URL.RequestURI() || p["qop"] != "auth" {
		return false
	}
	h := func(s string) string { x := md5.Sum([]byte(s)); return hex.EncodeToString(x[:]) } //nolint:gosec // HTTP Digest
	ha1 := h(d.user + ":IP Camera(TEST):" + d.pass)
	ha2 := h(r.Method + ":" + r.URL.RequestURI())
	return p["response"] == h(ha1+":"+nonce+":"+p["nc"]+":"+p["cnonce"]+":auth:"+ha2)
}

func TestDigestTransportChallengesOnceThenReuses(t *testing.T) {
	d := &digestDevice{user: "admin", pass: "s3cret-pw"}
	d.nonce.Store("n-1")
	srv := httptest.NewServer(d.handler(t))
	t.Cleanup(srv.Close)

	client := NewHikvisionClient("admin", "s3cret-pw", 5*time.Second)
	url, err := HikvisionFrameURL(srv.URL, 2)
	require.NoError(t, err)
	for range 3 {
		f, err := FetchHikvisionFrame(context.Background(), client, url)
		require.NoError(t, err)
		require.Equal(t, 32, f.Cols)
	}
	require.Equal(t, int32(1), d.challenges.Load(), "one challenge, then the cached nonce pre-authorizes")
	require.Equal(t, int32(3), d.ok.Load())

	// Device rotates its nonce: exactly one re-challenge, then success.
	d.nonce.Store("n-2")
	_, err = FetchHikvisionFrame(context.Background(), client, url)
	require.NoError(t, err)
	require.Equal(t, int32(2), d.challenges.Load())

	// Wrong password: the 401 surfaces as an error (no retry storm).
	bad := NewHikvisionClient("admin", "wrong", 5*time.Second)
	_, err = FetchHikvisionFrame(context.Background(), bad, url)
	require.ErrorContains(t, err, "HTTP 401")
}

func TestHikvisionPollerPublishesAndSkipsFrozen(t *testing.T) {
	bus := event.NewEventBus(8)
	ch := make(chan event.Event, 8)
	require.NoError(t, bus.Subscribe(TopicTemperature, ch, 8))

	d := &digestDevice{user: "admin", pass: "pw-1"}
	d.nonce.Store("n-1")
	srv := httptest.NewServer(d.handler(t))
	t.Cleanup(srv.Close)
	url, err := HikvisionFrameURL(srv.URL, 2)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	StartReadoutPoller(ctx, NewFeed(bus), map[string]ReadoutSource{
		"cam-t": {Kind: KindHikvision, URL: url, Username: "admin", Password: "pw-1", Interval: time.Hour},
	}, nil)
	select {
	case evt := <-ch:
		r, ok := evt.Data.(Reading)
		require.True(t, ok)
		require.Equal(t, "cam-t", r.CameraID)
		require.InDelta(t, 61.4, *r.MaxC, 1e-9)
	case <-time.After(5 * time.Second):
		t.Fatal("no reading published")
	}

	// A frozen (shutter-calibration) frame is never published.
	frozen := &digestDevice{user: "admin", pass: "pw-1", frozen: true}
	frozen.nonce.Store("n-9")
	fsrv := httptest.NewServer(frozen.handler(t))
	t.Cleanup(fsrv.Close)
	furl, _ := HikvisionFrameURL(fsrv.URL, 2)
	bus2 := event.NewEventBus(8)
	ch2 := make(chan event.Event, 8)
	require.NoError(t, bus2.Subscribe(TopicTemperature, ch2, 8))
	StartReadoutPoller(ctx, NewFeed(bus2), map[string]ReadoutSource{
		"cam-f": {Kind: KindHikvision, URL: furl, Username: "admin", Password: "pw-1", Interval: time.Hour},
	}, nil)
	require.Eventually(t, func() bool { return frozen.ok.Load() >= 1 }, 5*time.Second, 20*time.Millisecond)
	select {
	case evt := <-ch2:
		t.Fatalf("frozen frame published: %+v", evt)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSourceForCamera(t *testing.T) {
	// Hikvision kind with no URL: the camera's own host, its credentials,
	// the default thermal channel 2.
	cam := &config.CameraConfig{
		ID: "cam-t", Protocol: "onvif", ImagingType: config.ImagingTypeThermal,
		ONVIFEndpoint: "http://192.0.2.190/onvif/device_service", Username: "admin", Password: "pw",
		ThermalReadout: &config.ThermalReadoutConfig{Kind: config.ThermalReadoutKindHikvision},
	}
	src, ok := SourceForCamera(cam)
	require.True(t, ok)
	require.Equal(t, KindHikvision, src.Kind)
	require.Equal(t, "http://192.0.2.190/ISAPI/Thermal/channels/2/thermometry/jpegPicWithAppendData?format=json", src.URL)
	require.Equal(t, "admin", src.Username)
	require.Equal(t, "pw", src.Password)

	// Explicit base + channel; an RTSP-only camera derives http://host.
	cam.ThermalReadout = &config.ThermalReadoutConfig{Kind: config.ThermalReadoutKindHikvision, URL: "http://192.0.2.191:8080/x", Channel: 3}
	cam.ThermalReadout.VisibleRect = &config.ThermalVisibleRect{X: 0.15, Y: 0.11, W: 0.65, H: 0.86}
	src, ok = SourceForCamera(cam)
	require.True(t, ok)
	require.Equal(t, "http://192.0.2.191:8080/ISAPI/Thermal/channels/3/thermometry/jpegPicWithAppendData?format=json", src.URL)
	require.Equal(t, &NormRect{X: 0.15, Y: 0.11, W: 0.65, H: 0.86}, src.VisibleRect)
	rtsp := &config.CameraConfig{
		ID: "cam-r", Protocol: "rtsp", ImagingType: config.ImagingTypeThermal, URL: "rtsp://192.0.2.192:554/Streaming/Channels/201",
		ThermalReadout: &config.ThermalReadoutConfig{Kind: config.ThermalReadoutKindHikvision},
	}
	src, ok = SourceForCamera(rtsp)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(src.URL, "http://192.0.2.192/ISAPI/"))

	// A GB28181 auto-enrolled channel: no ONVIF endpoint, no row credentials —
	// the readout block's own credentials authenticate the ISAPI call.
	gb := &config.CameraConfig{
		ID: "gb-hik", Protocol: "gb28181", ImagingType: config.ImagingTypeThermal,
		ThermalReadout: &config.ThermalReadoutConfig{
			Kind: config.ThermalReadoutKindHikvision, URL: "http://192.0.2.193",
			Username: "admin", Password: "devpw",
		},
	}
	src, ok = SourceForCamera(gb)
	require.True(t, ok)
	require.Equal(t, "http://192.0.2.193/ISAPI/Thermal/channels/2/thermometry/jpegPicWithAppendData?format=json", src.URL)
	require.Equal(t, "admin", src.Username)
	require.Equal(t, "devpw", src.Password)

	// Readout credentials take precedence over the camera row's.
	cam.ThermalReadout = &config.ThermalReadoutConfig{
		Kind: config.ThermalReadoutKindHikvision, Username: "ro", Password: "ropw",
	}
	src, ok = SourceForCamera(cam)
	require.True(t, ok)
	require.Equal(t, "ro", src.Username)
	require.Equal(t, "ropw", src.Password)

	// SPEC JSON source unchanged; visible channels and empty SPEC URLs refused.
	spec := &config.CameraConfig{
		ID: "gb-t", ImagingType: config.ImagingTypeThermal,
		ThermalReadout: &config.ThermalReadoutConfig{URL: "http://192.0.2.5:8088/api/thermal/current", AuthHeader: "Authorization", AuthValue: "Bearer x"},
	}
	src, ok = SourceForCamera(spec)
	require.True(t, ok)
	require.Empty(t, src.Kind)
	require.Equal(t, "Bearer x", src.AuthValue)
	_, ok = SourceForCamera(&config.CameraConfig{ID: "v", ThermalReadout: spec.ThermalReadout})
	require.False(t, ok, "visible channel")
	_, ok = SourceForCamera(&config.CameraConfig{ID: "e", ImagingType: config.ImagingTypeThermal, ThermalReadout: &config.ThermalReadoutConfig{}})
	require.False(t, ok, "SPEC kind without URL")
}
