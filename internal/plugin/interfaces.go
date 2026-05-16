package plugin

import (
	"context"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// SegmentStore abstracts storage operations needed by FrameReceiver and other
// plugin-level recorders. This is the canonical definition — recorders in
// internal/recorder/ and plugins/xiaomi/ define their own local copies with
// identical method sets to avoid cross-package coupling.
type SegmentStore interface {
	CreateSegment(cameraID string, format string) (tempPath string, finalPath string, err error)
	CloseSegment(tempPath, finalPath string) error
}

// RecordingDB abstracts database operations needed by FrameReceiver and other
// plugin-level recorders.
type RecordingDB interface {
	InsertRecording(ctx context.Context, r *model.Recording) error
	InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error
}
