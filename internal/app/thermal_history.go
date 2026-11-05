package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/thermal"
)

// thermalHistoryRetention bounds the thermal_readings table (P4 temperature
// history): ~1.4k rows/day/camera × 90d ≈ 130k rows per thermal camera —
// a few MB. Recordings retention intentionally does NOT apply: the history
// is the "temperature archive" differentiator and should outlive clips.
const thermalHistoryRetention = 90 * 24 * time.Hour

// thermalHistoryService persists the thermal.temperature stream as one
// minute-aggregated row per camera (P4, #38 Tier 2): the live SSE chip keeps
// its 2s cadence, the history gets the folded extremes. Subscribes to the
// same bus topic the SSE bridge reads — one poller, two consumers.
type thermalHistoryService struct {
	db  *storage.DB
	bus *event.EventBus
}

func newThermalHistoryService(db *storage.DB, bus *event.EventBus) *thermalHistoryService {
	return &thermalHistoryService{db: db, bus: bus}
}

func (s *thermalHistoryService) Name() string { return "thermal-history" }

func (s *thermalHistoryService) Start(ctx context.Context) error {
	// nil db/bus = nothing to record (tests / minimal builds) — a no-op
	// service still starts so registration stays uniform.
	if s.db == nil || s.bus == nil {
		return nil
	}

	ch := make(chan event.Event, 128)
	if err := s.bus.Subscribe(thermal.TopicTemperature, ch, 128); err != nil {
		return err
	}

	go func() {
		defer s.bus.Unsubscribe(thermal.TopicTemperature, ch)
		rec := thermal.NewMinuteRecorder()
		flush := func(samples []thermal.MinuteSample) {
			for _, sm := range samples {
				spots, err := json.Marshal(sm.Spots)
				if err != nil {
					spots = nil
				}
				if err := s.db.InsertThermalMinute(ctx, sm.CameraID, sm.Minute, sm.MaxC, sm.MinC, string(spots)); err != nil {
					slog.Warn("thermal history insert failed", "camera_id", sm.CameraID, "error", err)
				}
			}
		}

		purge := func() {
			if n, err := s.db.PurgeThermalReadingsBefore(ctx, time.Now().UTC().Add(-thermalHistoryRetention)); err != nil {
				slog.Warn("thermal history purge failed", "error", err)
			} else if n > 0 {
				slog.Info("thermal history purged", "rows", n)
			}
		}
		purge() // startup pass; the ticker below keeps it daily after that

		tick := time.NewTicker(20 * time.Second)
		purgeTick := time.NewTicker(24 * time.Hour)
		defer tick.Stop()
		defer purgeTick.Stop()

		for {
			select {
			case <-ctx.Done():
				// Drain the open buckets so a restart doesn't lose the last
				// partial minute; the upsert merge keeps values consistent if
				// the minute was already flushed.
				flush(rec.Due(time.Now().UTC().Add(time.Hour)))
				return
			case ev := <-ch:
				if rd, ok := ev.Data.(thermal.Reading); ok {
					if sm := rec.Observe(time.Now().UTC(), rd); sm != nil {
						flush([]thermal.MinuteSample{*sm})
					}
				}
			case <-tick.C:
				flush(rec.Due(time.Now().UTC()))
			case <-purgeTick.C:
				purge()
			}
		}
	}()
	return nil
}

func (s *thermalHistoryService) Stop() error { return nil } // ctx cancel stops the loop
