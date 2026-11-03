package thermal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/require"
)

// The readout poller's contract with the live chip: a device endpoint in
// the SPEC-draft shape lights the thermal.temperature SSE topic on the
// very first poll; tolerant key spellings are accepted; failing endpoints
// never wedge the loop (the next tick retries and the chip dims on its
// own).
func TestReadoutPollerPublishesToBus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "tok-1", r.Header.Get("X-Auth"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"max": 88.4, "min": 21.7, "spots": [{"name": "炉体", "x": 0.5, "y": 0.5, "temp_c": 88.4}]}`))
	}))
	t.Cleanup(srv.Close)

	bus := event.NewEventBus(8)
	ch := make(chan event.Event, 8)
	require.NoError(t, bus.Subscribe(TopicTemperature, ch, 8))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	StartReadoutPoller(ctx, NewFeed(bus), map[string]ReadoutSource{
		"cam-hot": {URL: srv.URL, AuthHeader: "X-Auth", AuthValue: "tok-1"},
	}, nil)

	select {
	case evt := <-ch:
		require.Equal(t, TopicTemperature, evt.Topic)
		reading, ok := evt.Data.(Reading)
		require.True(t, ok)
		require.Equal(t, "cam-hot", reading.CameraID)
		require.NotNil(t, reading.MaxC)
		require.InDelta(t, 88.4, *reading.MaxC, 0.001)
		require.InDelta(t, 21.7, *reading.MinC, 0.001)
		require.Len(t, reading.Spots, 1)
		require.InDelta(t, 88.4, reading.Spots[0].TempC, 0.001)
		require.False(t, reading.Timestamp.IsZero(), "the feed stamps the publish time")
	case <-time.After(5 * time.Second):
		t.Fatal("first poll never reached the bus")
	}
}

func TestReadoutPollerTolerantKeysAndFailures(t *testing.T) {
	t.Parallel()
	// Alternate good (temperature spelling), malformed, and 500 responses:
	// only the good ones publish; the loop survives everything.
	var call int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		switch call % 3 {
		case 1:
			_, _ = w.Write([]byte(`{"max_c": 40.1, "spots": [{"name": "s1", "temperature": 39.8}]}`))
		case 2:
			_, _ = w.Write([]byte(`{not json`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	bus := event.NewEventBus(8)
	ch := make(chan event.Event, 8)
	require.NoError(t, bus.Subscribe(TopicTemperature, ch, 8))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	StartReadoutPoller(ctx, NewFeed(bus), map[string]ReadoutSource{
		"cam-hot": {URL: srv.URL, Interval: 50 * time.Millisecond},
	}, nil)

	select {
	case evt := <-ch:
		reading := evt.Data.(Reading)
		require.InDelta(t, 40.1, *reading.MaxC, 0.001)
		require.Nil(t, reading.MinC)
		require.Len(t, reading.Spots, 1)
		require.InDelta(t, 39.8, reading.Spots[0].TempC, 0.001, "temperature spelling accepted")
	case <-time.After(5 * time.Second):
		t.Fatal("no reading despite a good response in the rotation")
	}

	// The malformed and 500 ticks must not publish — drain and assert quiet.
	drain := time.After(200 * time.Millisecond)
	for {
		select {
		case evt := <-ch:
			reading, ok := evt.Data.(Reading)
			require.True(t, ok, "only well-formed readings may publish")
			require.NotNil(t, reading.MaxC, "malformed/500 responses must be dropped silently")
		case <-drain:
			return
		}
	}
}
