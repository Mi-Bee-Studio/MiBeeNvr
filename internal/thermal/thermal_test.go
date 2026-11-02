package thermal

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/require"
)

// The feed's contract with the web overlay: readings ride the SSE bus under
// thermal.temperature, the payload marshals with the camera_id key the
// per-camera events endpoint filters on, and the wire shape matches what
// ThermalOverlay.svelte parses (max_c/min_c/spots).
func TestFeedPublishesReadingOnSSEBus(t *testing.T) {
	t.Parallel()
	bus := event.NewEventBus(8)
	ch := make(chan event.Event, 8)
	require.NoError(t, bus.SubscribeByPrefix("thermal.", ch, 8))

	maxC, minC := 88.4, 21.7
	feed := NewFeed(bus)
	feed.Publish(Reading{
		CameraID: "cam-hot",
		MaxC:     &maxC,
		MinC:     &minC,
		Spots:    []SpotReading{{Name: "炉体", X: 0.5, Y: 0.5, TempC: 88.4}},
	})

	select {
	case evt := <-ch:
		require.Equal(t, TopicTemperature, evt.Topic)
		require.Equal(t, "thermal.temperature", evt.Topic, "SSE event name = topic")

		// Wire shape: what the SPA frame parser sees after unwrapping Data.
		b, err := json.Marshal(evt.Data)
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal(b, &m))
		require.Equal(t, "cam-hot", m["camera_id"], "the per-camera SSE filter keys on camera_id")
		require.InDelta(t, 88.4, m["max_c"], 0.001)
		require.InDelta(t, 21.7, m["min_c"], 0.001)
		spots, ok := m["spots"].([]any)
		require.True(t, ok)
		require.Len(t, spots, 1)
		spot := spots[0].(map[string]any)
		require.Equal(t, "炉体", spot["name"])
		require.InDelta(t, 88.4, spot["temp_c"], 0.001)
		require.NotEmpty(t, m["timestamp"], "a zero timestamp must be stamped before publish")
	case <-time.After(2 * time.Second):
		t.Fatal("reading never reached the bus")
	}
}

func TestFeedNilBusNoOp(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() {
		NewFeed(nil).Publish(Reading{CameraID: "cam-hot"})
		var f *Feed
		f.Publish(Reading{CameraID: "cam-hot"})
	})
}
