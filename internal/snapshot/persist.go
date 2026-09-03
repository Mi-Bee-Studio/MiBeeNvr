// Persistence + event publishing for captured snapshots (#656): atomic
// temp→rename writes under {root}/snapshots/{camera_id}/, then a
// camera.snapshot event so consumers (mqtt.status_events forwarding,
// automations) can react.

package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// Persistor writes snapshot JPEGs under {Root}/snapshots/{cameraID}/ using
// the storage convention (write temp in the same directory, then rename).
// Now is injectable for deterministic filenames in tests.
type Persistor struct {
	Root string
	Now  func() time.Time
}

func (p *Persistor) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Persist writes jpeg and returns the path relative to Root
// (slash-separated, e.g. "snapshots/cam-1/20260903-103005.123.jpg").
// Same-millisecond persists get a numeric suffix instead of overwriting.
func (p *Persistor) Persist(cameraID string, jpeg []byte) (string, error) {
	// Camera IDs are kebab-case by convention; Base is a guard so a stray
	// separator can never escape the camera's snapshots directory.
	safeID := filepath.Base(cameraID)

	dir := filepath.Join(p.Root, "snapshots", safeID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("snapshot: create dir: %w", err)
	}

	base := p.now().UTC().Format("20060102-150405.000")
	final := filepath.Join(dir, base+".jpg")
	for i := 2; ; i++ {
		if _, err := os.Stat(final); os.IsNotExist(err) {
			break
		}
		final = filepath.Join(dir, fmt.Sprintf("%s-%d.jpg", base, i))
	}

	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, jpeg, 0o644); err != nil {
		return "", fmt.Errorf("snapshot: write temp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("snapshot: rename: %w", err)
	}

	rel, err := filepath.Rel(p.Root, final)
	if err != nil {
		return "", fmt.Errorf("snapshot: rel path: %w", err)
	}
	return filepath.ToSlash(rel), nil
}

// FrameSource captures a JPEG for a camera — satisfied by *Capturer; kept as
// an interface so the Runner is testable with a stub.
type FrameSource interface {
	Capture(cameraID string) ([]byte, error)
}

// Publisher is the event-bus surface the Runner needs (satisfied by
// *event.EventBus).
type Publisher interface {
	Publish(ctx context.Context, topic string, data any)
}

// Runner couples capture → persist → event publish. Wired in pkg/app for the
// MQTT snapshot trigger; the capture runs on the caller's goroutine (the MQTT
// dispatcher already moves it off the paho handler).
type Runner struct {
	Source  FrameSource
	Storage *Persistor
	Bus     Publisher
	Now     func() time.Time
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// RunSnapshot captures one frame, persists it under the storage root, and
// publishes a camera.snapshot event. Trigger records what caused the capture
// (e.g. "mqtt") for downstream consumers.
func (r *Runner) RunSnapshot(ctx context.Context, cameraID string, trigger string) (string, error) {
	if r.Source == nil {
		return "", fmt.Errorf("snapshot: no capture source wired")
	}
	jpeg, err := r.Source.Capture(cameraID)
	if err != nil {
		return "", err
	}

	now := r.now()
	rel, err := r.Storage.Persist(cameraID, jpeg)
	if err != nil {
		return "", err
	}

	if r.Bus != nil {
		r.Bus.Publish(ctx, event.TopicCameraSnapshot, event.CameraSnapshotEvent{
			CameraID:  cameraID,
			FilePath:  rel,
			Timestamp: now,
			Trigger:   trigger,
		})
	}
	return rel, nil
}
