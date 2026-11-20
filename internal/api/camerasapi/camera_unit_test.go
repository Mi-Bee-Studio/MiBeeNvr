package camerasapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	onvifserver "github.com/mickeyzzc/onvif-go/v2/server"
	"github.com/stretchr/testify/require"
)

// --- cameraRowForAPI tests ---

func TestCameraRowForAPI_ONVIFEndpoint(t *testing.T) {
	t.Helper()
	t.Parallel()
	row := &storage.CameraRow{Protocol: "onvif", ONVIFEndpoint: "http://192.168.1.100/onvif/device_service", URL: ""}
	cameraRowForAPI(row)
	require.Equal(t, "http://192.168.1.100/onvif/device_service", row.URL)
}

func TestCameraRowForAPI_NonONVIFUnchanged(t *testing.T) {
	t.Helper()
	t.Parallel()
	row := &storage.CameraRow{Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream", ONVIFEndpoint: ""}
	cameraRowForAPI(row)
	require.Equal(t, "rtsp://192.168.1.10/stream", row.URL)
}

func TestCameraRowForAPI_ONVIFWithURLAlreadySet(t *testing.T) {
	t.Helper()
	t.Parallel()
	row := &storage.CameraRow{Protocol: "onvif", URL: "http://already-set", ONVIFEndpoint: "http://192.168.1.100/onvif"}
	cameraRowForAPI(row)
	require.Equal(t, "http://already-set", row.URL)
}

func TestCameraRowForAPI_ONVIFNoEndpoint(t *testing.T) {
	t.Helper()
	t.Parallel()
	row := &storage.CameraRow{Protocol: "onvif", URL: "", ONVIFEndpoint: ""}
	cameraRowForAPI(row)
	require.Equal(t, "", row.URL)
}

// --- stripScheme tests ---

func TestStripScheme_RTSPWithPort(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "192.168.1.10:554", stripScheme("rtsp://192.168.1.10:554/stream"))
}

func TestStripScheme_RTSPNoPort(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "192.168.1.10:554", stripScheme("rtsp://192.168.1.10/stream"))
}

func TestStripScheme_HTTPWithPort(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "192.168.1.10:8080", stripScheme("http://192.168.1.10:8080/capture"))
}

func TestStripScheme_HTTPS(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "camera.example.com:443", stripScheme("https://camera.example.com:443/stream"))
}

func TestStripScheme_NoScheme(t *testing.T) {
	t.Helper()
	t.Parallel()
	result := stripScheme("192.168.1.10:554")
	require.Contains(t, result, "192.168.1.10:554")
}

func TestStripScheme_RTSPDefaultPort(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "10.0.0.1:554", stripScheme("rtsp://10.0.0.1/path"))
}

func TestStripScheme_RTSPWithCredentials(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "192.168.1.10:554", stripScheme("rtsp://admin:secret@192.168.1.10/stream"))
}

func TestStripScheme_RTSPWithCredentialsAndPort(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "192.168.1.10:8554", stripScheme("rtsp://user:pass@192.168.1.10:8554/stream"))
}

// TestResolveEncoding_ProbeWinsOverStored proves the live recorder probe is
// authoritative: a camera stored as h264 but physically streaming h265 (e.g.
// Xiaomi) must display h265. This is the core of the #165 fix.
func TestResolveEncoding_ProbeWinsOverStored(t *testing.T) {
	t.Helper()
	t.Parallel()
	got := resolveEncoding("h264", model.FormatH265)
	require.Equal(t, "h265", got, "probe (h265) must override stored h264")
}

// TestResolveEncoding_StoredFallbackOnEmptyProbe proves an empty probe (camera
// offline / codec not yet detected) keeps the stored value. This guards against
// re-introducing #112 (black screen when encoding="" reached the frontend).
func TestResolveEncoding_StoredFallbackOnEmptyProbe(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "h264", resolveEncoding("h264", ""),
		"empty probe must fall back to stored value")
	require.Equal(t, "", resolveEncoding("", ""),
		"empty probe + empty stored stays empty")
}

// TestResolveEncoding_ProbeFillsEmptyStored proves the probe fills in an empty
// stored value (ONVIF auto-detect devices — the original backfill case).
func TestResolveEncoding_ProbeFillsEmptyStored(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, "jpeg", resolveEncoding("", model.EncJPEG),
		"probe must fill an empty stored encoding")
}

// TestProbeCodec_NilManager proves probeCodec is nil-safe so handlers can call
// it unconditionally even when camMgr is unset (e.g. setup-required state).
func TestProbeCodec_NilManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.Equal(t, model.Format(""), probeCodec(nil, "any-cam"),
		"probeCodec(nil, ...) must return empty, not panic")
}

// --- validProtocols map test ---

func TestValidProtocols(t *testing.T) {
	t.Parallel()
	for _, proto := range []string{"rtsp", "http", "onvif", "xiaomi", "srt", "rtmp", "timelapse"} {
		require.True(t, validProtocols[proto], "expected %q to be valid", proto)
	}
	require.False(t, validProtocols["ftp"])
	require.False(t, validProtocols[""])
}

// --- extractDIDFromURL tests ---

func TestExtractDIDFromURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "12345678", extractDIDFromURL("xiaomi://12345678"))
}

func TestExtractDIDFromURL_Empty(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", extractDIDFromURL(""))
}

func TestExtractDIDFromURL_Invalid(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", extractDIDFromURL("://bad"))
}

// --- strPtr helper ---

func TestStrPtr(t *testing.T) {
	t.Parallel()
	s := "hello"
	p := strPtr(s)
	require.NotNil(t, p)
	require.Equal(t, "hello", *p)
}

func TestProbeONVIFStreamDualSpectrumProfiles(t *testing.T) {
	t.Parallel()

	cfg := onvifserver.DefaultConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = 0
	cfg.Username = "admin"
	cfg.Password = "sim-pass"
	cfg.Profiles = []onvifserver.ProfileConfig{
		{
			Token: "Profile_visual", Name: "Visible Main",
			VideoSource: onvifserver.VideoSourceConfig{
				Token: "vs-0", Name: "Visible",
				Resolution: onvifserver.Resolution{Width: 2560, Height: 1440},
				Framerate:  25,
				Bounds:     onvifserver.Bounds{Width: 2560, Height: 1440},
			},
			VideoEncoder: onvifserver.VideoEncoderConfig{
				Encoding:   "H264",
				Resolution: onvifserver.Resolution{Width: 2560, Height: 1440},
				Framerate:  25, GovLength: 25, Bitrate: 4096,
			},
		},
		{
			Token: "Profile_thermal", Name: "Thermal",
			VideoSource: onvifserver.VideoSourceConfig{
				Token: "vs-1", Name: "Thermal",
				Resolution: onvifserver.Resolution{Width: 384, Height: 288},
				Framerate:  9,
				Bounds:     onvifserver.Bounds{Width: 384, Height: 288},
			},
			VideoEncoder: onvifserver.VideoEncoderConfig{
				Encoding:   "H264",
				Resolution: onvifserver.Resolution{Width: 384, Height: 288},
				Framerate:  9, GovLength: 9, Bitrate: 512,
			},
		},
	}

	// Port 0 would leak into the advertised service XAddrs (the simulator
	// publishes config.Port, not the ephemeral port the listener got) —
	// reserve a concrete free port instead.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	cfg.Port = port

	sim, err := onvifserver.New(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sim.Start(ctx) }()
	require.Eventually(t, func() bool { return sim.ListenAddr() != "" },
		5*time.Second, 50*time.Millisecond)

	endpoint := "http://" + sim.ListenAddr() + "/onvif/device_service"
	probe := probeONVIFStream(context.Background(), endpoint, "admin", "sim-pass", "")

	require.True(t, probe.Reachable, "device_service must answer: %s", probe.Reason)
	require.NotEmpty(t, probe.Profiles, "the profile list must ride the probe even without a stream leg")
	require.Len(t, probe.Profiles, 2)
	require.Equal(t, "Profile_visual", probe.AutoMainToken,
		"automatic selection picks the highest-resolution visible-light profile")
	tokens := []string{probe.Profiles[0].Token, probe.Profiles[1].Token}
	require.Contains(t, tokens, "Profile_thermal")
}

func TestXiaomiPTZFromVector(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		pan, tilt, zoom float64
		wantDir         string
		wantSpeed       int
		wantErr         bool
	}{
		{"pan left", -0.5, 0, 0, "left", 5, false},
		{"pan right fast", 1.0, 0, 0, "right", 10, false},
		{"pan right slow", 0.25, 0, 0, "right", 3, false},
		{"tiny magnitude clamps to 1", -0.05, 0, 0, "left", 1, false},
		{"tilt up", 0, 0.6, 0, "up", 6, false},
		{"tilt down", 0, -0.4, 0, "down", 4, false},
		{"tilt dominates pan", 0.2, 0.9, 0, "up", 9, false},
		{"zoom rejected", 0, 0, 0.5, "", 0, true},
		{"zero vector stops", 0, 0, 0, "stop", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, speed, err := xiaomiPTZFromVector(tc.pan, tc.tilt, tc.zoom)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantDir, dir)
			require.Equal(t, tc.wantSpeed, speed)
		})
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	t.Parallel()
	const secret = "whsec_test"
	const window = 5 * time.Minute
	body := []byte(`{"reason":"doorbell"}`)

	valid := webhookSign(secret, webhookNow.Unix(), body)

	t.Run("valid", func(t *testing.T) {
		require.NoError(t, verifyWebhookSignature(secret, valid, body, webhookNow, window))
	})
	t.Run("tampered body", func(t *testing.T) {
		require.Error(t, verifyWebhookSignature(secret, valid, []byte(`{"reason":"evil"}`), webhookNow, window))
	})
	t.Run("wrong secret", func(t *testing.T) {
		require.Error(t, verifyWebhookSignature("whsec_other", valid, body, webhookNow, window))
	})
	t.Run("stale timestamp", func(t *testing.T) {
		stale := webhookSign(secret, webhookNow.Add(-window-time.Second).Unix(), body)
		require.ErrorIs(t, verifyWebhookSignature(secret, stale, body, webhookNow, window), errWebhookStaleTimestamp)
	})
	t.Run("future timestamp beyond window", func(t *testing.T) {
		future := webhookSign(secret, webhookNow.Add(window+time.Second).Unix(), body)
		require.ErrorIs(t, verifyWebhookSignature(secret, future, body, webhookNow, window), errWebhookStaleTimestamp)
	})
	t.Run("timestamp is covered by the mac", func(t *testing.T) {
		// A fresh ts with the v1 computed over a DIFFERENT ts must not verify —
		// the timestamp must be inside the signed payload, not a free field.
		mac := hmac.New(sha256.New, []byte(secret))
		fmt.Fprintf(mac, "%d.", webhookNow.Add(-time.Hour).Unix())
		mac.Write(body)
		forged := fmt.Sprintf("t=%d,v1=%s", webhookNow.Unix(), hex.EncodeToString(mac.Sum(nil)))
		require.Error(t, verifyWebhookSignature(secret, forged, body, webhookNow, window))
	})
	t.Run("malformed headers", func(t *testing.T) {
		cases := []string{
			"",
			"v1=abc",
			"t=abc,v1=abc",
			fmt.Sprintf("t=%d", webhookNow.Unix()),
			fmt.Sprintf("t=%d,v1=not-hex", webhookNow.Unix()),
		}
		for _, hdr := range cases {
			require.Error(t, verifyWebhookSignature(secret, hdr, body, webhookNow, window), "header %q must be rejected", hdr)
		}
	})
}

// webhookSign produces the X-MiBee-Signature header value for a body:
// t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>. Mirrors the scheme the
// webhook endpoint verifies (issue #709, Stripe-style signed payload).
func webhookSign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

// webhookNow pins the "server clock" for pure verification tests.
var webhookNow = time.Unix(1731000000, 0)
