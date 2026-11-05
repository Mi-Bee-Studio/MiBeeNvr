package storage

import (
	"context"
	"time"
)

// Thermal history (P4, #38 Tier 2): one row per thermal camera per closed
// minute — scene extremes folded by the sampler, spots last-seen. ~1.4k
// rows/day/camera, purged after thermalHistoryRetention by the app service.

// ThermalPoint is one history sample as served by the API.
type ThermalPoint struct {
	Minute time.Time `json:"minute"`
	MaxC   *float64  `json:"max_c,omitempty"`
	MinC   *float64  `json:"min_c,omitempty"`
}

// InsertThermalMinute upserts one minute sample. A conflicting row (the same
// camera+minute re-flushed after a restart) merges by extremes so a partial
// row from before the restart keeps the more extreme values.
func (d *DB) InsertThermalMinute(ctx context.Context, cameraID string, minute time.Time, maxC, minC *float64, spotsJSON string) error {
	m := minute.UTC().Truncate(time.Minute).Format(time.RFC3339)
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO thermal_readings (camera_id, minute, max_c, min_c, spots)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(camera_id, minute) DO UPDATE SET
			max_c = CASE
				WHEN excluded.max_c IS NULL THEN thermal_readings.max_c
				WHEN thermal_readings.max_c IS NULL THEN excluded.max_c
				WHEN excluded.max_c > thermal_readings.max_c THEN excluded.max_c
				ELSE thermal_readings.max_c END,
			min_c = CASE
				WHEN excluded.min_c IS NULL THEN thermal_readings.min_c
				WHEN thermal_readings.min_c IS NULL THEN excluded.min_c
				WHEN excluded.min_c < thermal_readings.min_c THEN excluded.min_c
				ELSE thermal_readings.min_c END,
			spots = CASE WHEN excluded.spots = '' THEN thermal_readings.spots ELSE excluded.spots END`,
		cameraID, m, maxC, minC, spotsJSON)
	return err
}

// QueryThermalReadings returns the stored samples for [from, to] (inclusive),
// oldest first. Window decimation is the API layer's job.
func (d *DB) QueryThermalReadings(ctx context.Context, cameraID string, from, to time.Time) ([]ThermalPoint, error) {
	rows, err := d.readConn().QueryContext(ctx, `
		SELECT minute, max_c, min_c FROM thermal_readings
		WHERE camera_id = ? AND minute >= ? AND minute <= ?
		ORDER BY minute`,
		cameraID, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ThermalPoint
	for rows.Next() {
		var p ThermalPoint
		var m string
		if err := rows.Scan(&m, &p.MaxC, &p.MinC); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, m)
		if err != nil {
			continue // a malformed legacy row must not break the whole chart
		}
		p.Minute = t
		out = append(out, p)
	}
	return out, rows.Err()
}

// PurgeThermalReadingsBefore deletes history older than the cutoff and
// returns the number of removed rows.
func (d *DB) PurgeThermalReadingsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := d.db.ExecContext(ctx, `DELETE FROM thermal_readings WHERE minute < ?`,
		cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountThermalReadings reports the stored sample count for a camera
// (diagnostics + tests).
func (d *DB) CountThermalReadings(ctx context.Context, cameraID string) (int, error) {
	rows, err := d.readConn().QueryContext(ctx, `SELECT COUNT(*) FROM thermal_readings WHERE camera_id = ?`, cameraID)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}
