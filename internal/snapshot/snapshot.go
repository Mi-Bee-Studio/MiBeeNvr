// Package snapshot captures still frames from any recorder type (#657/#656).
//
// Capture order (mirrors the issues' capability ladder):
//  1. JPEG-family recorders implementing LatestFrame() — direct, no network
//  2. H.264/H.265 cameras: one-shot StreamHub subscribe (the IDR cache replays
//     the cached param-sets+IDR access unit immediately — no GOP wait) decoded
//     to JPEG through the OPTIONAL FFmpeg subprocess (DecodeFunc)
//  3. cameras with a configured snapshot URL (ONVIF GetSnapshotUri): direct
//     HTTP fetch with the camera's credentials
//
// FFmpeg stays optional: when absent, path 2 fails and the caller degrades
// (API keeps 404, MQTT logs unsupported).

package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// DecodeFunc decodes one Annex-B access unit (parameter sets + IDR) into a
// JPEG. Production wires transcoding.DecodeAUToJPEG; tests inject a stub.
type DecodeFunc func(au [][]byte) ([]byte, error)

// ErrNoFrame is returned when no capture path can produce a frame for the
// camera (wrong recorder type, no hub frames yet, FFmpeg absent and no
// snapshot URL). Callers degrade gracefully on it.
var ErrNoFrame = errors.New("snapshot: no frame available for this camera")

const (
	// snapshotURLTimeout bounds the snapshot-URL fetch; snapshotBodyCap bounds
	// its body (a JPEG is a few MB at most).
	snapshotURLTimeout = 5 * time.Second
	snapshotBodyCap    = 8 << 20
)

// hubWaitTimeout bounds the one-shot StreamHub subscription: with the IDR
// cache the callback is immediate; without frames flowing (camera offline)
// this is pure wait. Var (not const) so tests can shorten it.
var hubWaitTimeout = 3 * time.Second

var subSeq atomic.Int64

// FrameFromRecorder captures one JPEG frame from the recorder:
// LatestFrame() fast path for JPEG-family recorders, otherwise a one-shot
// StreamHub subscription decoded through the injected DecodeFunc.
// Unwraps ONVIF delegate layers first.
func FrameFromRecorder(rec model.Recorder, decode DecodeFunc) ([]byte, error) {
	for {
		type delegater interface{ Delegate() model.Recorder }
		if u, ok := rec.(delegater); ok {
			if d := u.Delegate(); d != nil {
				rec = d
				continue
			}
		}
		break
	}

	type latestFramer interface{ LatestFrame() []byte }
	if lr, ok := rec.(latestFramer); ok {
		if frame := lr.LatestFrame(); frame != nil {
			return frame, nil
		}
	}

	type hubber interface{ GetHub() *streamhub.StreamHub }
	if h, ok := rec.(hubber); ok && decode != nil && h.GetHub() != nil {
		return frameFromHub(h.GetHub(), decode)
	}
	return nil, ErrNoFrame
}

// frameFromHub subscribes once, waits for the cached-IDR replay (or the next
// keyframe), and decodes it. The subscription is ALWAYS removed before
// returning — a leaked subscriber would pin frames forever.
func frameFromHub(hub *streamhub.StreamHub, decode DecodeFunc) ([]byte, error) {
	id := fmt.Sprintf("snapshot-%d", subSeq.Add(1))
	auCh := make(chan [][]byte, 1)
	if err := hub.Subscribe(id, func(_ int64, au [][]byte) {
		select {
		case auCh <- au:
		default: // first AU wins; later frames are dropped
		}
	}); err != nil {
		return nil, fmt.Errorf("snapshot: hub subscribe: %w", err)
	}
	defer hub.Unsubscribe(id)

	select {
	case au := <-auCh:
		jpeg, err := decode(au)
		if err != nil {
			return nil, fmt.Errorf("snapshot: decode: %w", err)
		}
		return jpeg, nil
	case <-time.After(hubWaitTimeout):
		return nil, ErrNoFrame
	}
}

// CameraSource resolves the live recorder (satisfied by *camera.CameraManager).
type CameraSource interface {
	GetRecorder(cameraID string) model.Recorder
}

// ConfigSource resolves per-camera snapshot URL + credentials (satisfied by
// *camera.CameraManager).
type ConfigSource interface {
	GetCameraConfig(cameraID string) *config.CameraConfig
}

// HTTPClient is the snapshot-URL fetch seam (tests inject a stub).
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Capturer tries every capture capability in order for one camera.
type Capturer struct {
	Decode   DecodeFunc
	Client   HTTPClient
	Recorder CameraSource
	Config   ConfigSource
}

// fetchSnapshotURL is the injectable HTTP fetch (production: real transport).
var fetchSnapshotURL = fetchViaClient

// Capture produces one JPEG for the camera: recorder frame → hub+decode →
// configured snapshot URL.
func (c *Capturer) Capture(cameraID string) ([]byte, error) {
	if rec := c.Recorder.GetRecorder(cameraID); rec != nil {
		jpeg, err := FrameFromRecorder(rec, c.Decode)
		if err == nil {
			return jpeg, nil
		}
		if !errors.Is(err, ErrNoFrame) {
			// A real decode failure is worth surfacing, but the snapshot-URL
			// fallback may still save us.
			if jpeg, urlErr := c.trySnapshotURL(cameraID); urlErr == nil {
				return jpeg, nil
			}
			return nil, err
		}
	}
	return c.trySnapshotURL(cameraID)
}

func (c *Capturer) trySnapshotURL(cameraID string) ([]byte, error) {
	if c.Config == nil || c.Client == nil {
		return nil, ErrNoFrame
	}
	cam := c.Config.GetCameraConfig(cameraID)
	if cam == nil || strings.TrimSpace(cam.SnapshotURL) == "" {
		return nil, ErrNoFrame
	}
	return fetchSnapshotURL(c.Client, cam.SnapshotURL, cam.Username, cam.Password)
}

func fetchViaClient(client HTTPClient, url, username, password string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), snapshotURLTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Cameras commonly require Basic auth on the snapshot endpoint even when
	// the ONVIF service itself is unauthenticated (mirrors handleSnapshot).
	if username != "" {
		req.SetBasicAuth(username, password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snapshot: snapshot URL returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, snapshotBodyCap+1))
	if err != nil {
		return nil, err
	}
	if len(body) > snapshotBodyCap {
		return nil, errors.New("snapshot: snapshot URL body exceeds cap")
	}
	return body, nil
}
