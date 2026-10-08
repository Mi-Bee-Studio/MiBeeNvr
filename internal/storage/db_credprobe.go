package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// camera_cred_probe accessors (#credprobe).
//
// The once-only guarantee lives here: ClaimCredentialProbe atomically inserts
// the camera's row, and only the claimer (INSERT actually added a row) may run
// the probe. Restart-safe, race-safe across concurrent enrollments.

// Credential probe statuses (status column).
const (
	CredProbeStatusAttempted = "attempted" // claimed, run in progress or interrupted
	CredProbeStatusMatched   = "matched"   // a candidate authenticated
	CredProbeStatusFailed    = "failed"    // no candidate matched / inconclusive
	CredProbeStatusRotated   = "rotated"   // matched AND password rotated to the operator default
)

// ClaimCredentialProbe atomically claims the once-only probe slot for a
// camera. Returns true when the caller is the first and only claimer; false
// means this camera was already probed (any outcome) and must be skipped.
func (d *DB) ClaimCredentialProbe(ctx context.Context, cameraID string) (bool, error) {
	res, err := d.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO camera_cred_probe (camera_id, status) VALUES (?, ?)`,
		cameraID, CredProbeStatusAttempted)
	if err != nil {
		return false, fmt.Errorf("claim credential probe: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim credential probe (rows): %w", err)
	}
	return n > 0, nil
}

// SetCredentialProbeResult records the probe outcome on an already-claimed row.
func (d *DB) SetCredentialProbeResult(ctx context.Context, cameraID, status, matchedUsername string, rotated bool) error {
	rot := 0
	if rotated {
		rot = 1
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE camera_cred_probe SET status=?, matched_username=?, rotated=? WHERE camera_id=?`,
		status, matchedUsername, rot, cameraID)
	if err != nil {
		return fmt.Errorf("set credential probe result: %w", err)
	}
	return nil
}

// GetCredentialProbeStatus returns the probe ledger row for a camera, if any.
// exists=false means the camera has never been probed (and is not currently
// subject to the once-only rule — e.g. it was added with credentials).
func (d *DB) GetCredentialProbeStatus(ctx context.Context, cameraID string) (exists bool, status, matchedUsername string, rotated bool, err error) {
	err = d.db.QueryRowContext(ctx,
		`SELECT status, matched_username, rotated FROM camera_cred_probe WHERE camera_id=?`,
		cameraID).Scan(&status, &matchedUsername, &rotated)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, "", "", false, nil
		}
		return false, "", "", false, fmt.Errorf("get credential probe status: %w", err)
	}
	return true, status, matchedUsername, rotated, nil
}
