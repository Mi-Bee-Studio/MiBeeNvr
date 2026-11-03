// Package thermal carries dual-spectrum temperature readings from device
// adapters to the SSE event feed. The transport and the web overlay are
// adapter-agnostic: whoever can produce Readings — the ONVIF Profile T
// metadata stream or a vendor radiometry bypass (both pending real-device
// bring-up) — publishes through a Feed, and the existing SSE plumbing
// delivers the frames to the UI under the thermal.temperature topic.
package thermal

import (
	"context"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// TopicTemperature is the SSE topic temperature readings ride. The web's
// thermal overlay subscribes per camera via /api/cameras/{id}/events (which
// filters by the payload's camera_id) and listens for this event name.
const TopicTemperature = "thermal.temperature"

// SpotReading is one named/positioned spot meter. X/Y are normalized [0,1]
// image coordinates when the device reports a position; 0,0 = unpositioned
// (the overlay renders the value, not a marker).
type SpotReading struct {
	Name  string  `json:"name,omitempty"`
	X     float64 `json:"x,omitempty"`
	Y     float64 `json:"y,omitempty"`
	TempC float64 `json:"temp_c"`
}

// Reading is one temperature observation for a thermal camera. MaxC/MinC
// are the scene extremes when the device reports them; Spots carries named
// meters when it does. All fields optional except CameraID — adapters
// report what the device gives.
type Reading struct {
	CameraID  string        `json:"camera_id"`
	Timestamp time.Time     `json:"timestamp"`
	MaxC      *float64      `json:"max_c,omitempty"` // scene maximum, °C
	MinC      *float64      `json:"min_c,omitempty"` // scene minimum, °C
	Spots     []SpotReading `json:"spots,omitempty"`
}

// Feed republishes readings onto the event bus. A nil-bus Feed (or a nil
// Feed) is a no-op, so adapters can hold one through early shutdown.
type Feed struct {
	bus *event.EventBus
}

// NewFeed wraps an event bus.
func NewFeed(bus *event.EventBus) *Feed { return &Feed{bus: bus} }

// Publish stamps a zero Timestamp with the current UTC time and republishes
// the reading under TopicTemperature.
func (f *Feed) Publish(r Reading) {
	if f == nil || f.bus == nil {
		return
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
	f.bus.Publish(context.Background(), TopicTemperature, r)
}
