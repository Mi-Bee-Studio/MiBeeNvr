package thermal

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// ReadoutSource describes one device-side temperature readout endpoint
// (dual-spectrum; the device's SPEC-style GET .../thermal/current shape).
// AuthHeader/AuthValue carry an optional static header — the pre-SPEC
// integration seam; the finalized SPEC session auth replaces it later
// without touching the poll loop.
type ReadoutSource struct {
	URL        string
	AuthHeader string
	AuthValue  string
	Interval   time.Duration // poll cadence; <=0 → 2s
}

// StartReadoutPoller polls every source and republishes readings through
// the feed until ctx is done. One goroutine per camera; a failing endpoint
// logs and retries on the next tick (the overlay dims to "stale" on its
// own — a flapping source never wedges the loop).
func StartReadoutPoller(ctx context.Context, feed *Feed, sources map[string]ReadoutSource, client *http.Client) {
	if feed == nil || len(sources) == 0 {
		return
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	for cameraID, src := range sources {
		go pollReadout(ctx, feed, client, cameraID, src)
	}
}

func pollReadout(ctx context.Context, feed *Feed, client *http.Client, cameraID string, src ReadoutSource) {
	interval := src.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	poll := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
		if err != nil {
			slog.Warn("thermal readout: bad request", "camera_id", cameraID, "error", err)
			return
		}
		if src.AuthHeader != "" {
			req.Header.Set(src.AuthHeader, src.AuthValue)
		}
		resp, err := client.Do(req)
		if err != nil {
			slog.Debug("thermal readout: endpoint unreachable", "camera_id", cameraID, "error", err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			slog.Debug("thermal readout: non-200", "camera_id", cameraID, "status", resp.StatusCode)
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			slog.Debug("thermal readout: read body", "camera_id", cameraID, "error", err)
			return
		}
		reading, ok := parseReadout(cameraID, body)
		if !ok {
			slog.Debug("thermal readout: unparsable payload", "camera_id", cameraID)
			return
		}
		feed.Publish(reading)
	}
	poll() // light the chip immediately on startup
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			poll()
		}
	}
}

// readoutPayload accepts the SPEC-draft shape plus tolerant key spellings
// (max/max_c, temp_c/temperature) — the device SPEC is not finalized;
// strictness tightens once it is. The feed stamps the publish time, so the
// endpoint's own timestamp fields are ignored for now.
type readoutPayload struct {
	Max   *float64      `json:"max"`
	Min   *float64      `json:"min"`
	MaxC  *float64      `json:"max_c"`
	MinC  *float64      `json:"min_c"`
	Spots []spotPayload `json:"spots"`
}

type spotPayload struct {
	Name  string   `json:"name"`
	X     float64  `json:"x"`
	Y     float64  `json:"y"`
	Temp  *float64 `json:"temp_c"`
	Temp2 *float64 `json:"temperature"`
}

func parseReadout(cameraID string, body []byte) (Reading, bool) {
	var p readoutPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return Reading{}, false
	}
	r := Reading{CameraID: cameraID}
	r.MaxC = firstSet(p.MaxC, p.Max)
	r.MinC = firstSet(p.MinC, p.Min)
	if r.MaxC == nil && r.MinC == nil && len(p.Spots) == 0 {
		return Reading{}, false
	}
	for _, s := range p.Spots {
		if v := firstSet(s.Temp, s.Temp2); v != nil {
			r.Spots = append(r.Spots, SpotReading{Name: s.Name, X: s.X, Y: s.Y, TempC: *v})
		}
	}
	return r, true
}

func firstSet(vs ...*float64) *float64 {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}
