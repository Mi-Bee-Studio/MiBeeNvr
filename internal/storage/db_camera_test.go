package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpdateCameraEncoding verifies the single-column runtime backfill for the
// encoding column (issue #112). UpsertCamera is a full-row overwrite, so a
// dedicated UpdateCameraEncoding is needed to persist a recorder-resolved codec
// without clobbering unrelated columns.
func TestUpdateCameraEncoding(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_enc.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	const camID = "cam-enc"
	// Insert a camera with empty encoding (the auto-detect / ESP32 MiBeeCam case).
	require.NoError(t, db.UpsertCamera(ctx, camID, "Enc Cam", "onvif", "", "", "", "", "", "", "", ""))

	// Initially empty.
	enc, err := db.GetCameraEncoding(ctx, camID)
	require.NoError(t, err)
	require.Equal(t, "", enc)

	// Runtime-resolved JPEG via the single-column UPDATE (mirrors ensureEncoding).
	require.NoError(t, db.UpdateCameraEncoding(ctx, camID, "jpeg"))
	enc, err = db.GetCameraEncoding(ctx, camID)
	require.NoError(t, err)
	require.Equal(t, "jpeg", enc)

	// stream_encoding (ONVIF uppercase form) round-trip.
	require.NoError(t, db.UpdateCameraStreamEncoding(ctx, camID, "H265"))
}

// TestUpdateCameraEncoding_IdempotentOnMissingRow confirms the UPDATE is a
// no-op (not an error) when the camera row doesn't exist — matching
// UpdateCameraStableID's contract. ensureEncoding is best-effort and must not
// fail hard on a race where the camera was removed between probe and persist.
func TestUpdateCameraEncoding_IdempotentOnMissingRow(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_enc2.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	// No row exists — UPDATE affects 0 rows, no error.
	require.NoError(t, db.UpdateCameraEncoding(ctx, "never-existed", "h264"))
	require.NoError(t, db.UpdateCameraStreamEncoding(ctx, "never-existed", "H264"))

	enc, err := db.GetCameraEncoding(ctx, "never-existed")
	require.NoError(t, err)
	require.Equal(t, "", enc, "GetCameraEncoding returns empty for a missing row, not an error")
}

// newCameraDB is the shared setup for camera-row dedup tests: an initialized
// SQLite DB in a temp dir, closed on cleanup.
func newCameraDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_dedup.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestCameraIDByStableID covers the IP-roaming dedup lookup (issue #121). A
// camera with stable_id set must be findable by stable_id alone — this is how
// auto-discover recognizes a device that came back at a NEW IP.
func TestCameraIDByStableID(t *testing.T) {
	db := newCameraDB(t)
	ctx := context.Background()
	const camID = "cam-stable"
	const endpoint = "http://192.168.1.50:80/onvif/device_service"
	require.NoError(t, db.UpsertCamera(ctx, camID, "Stable Cam", "onvif", "", "", "", "", endpoint, "", "", "ABC-123"))

	// Match by stable_id.
	got, err := db.CameraIDByStableID(ctx, "ABC-123")
	require.NoError(t, err)
	require.Equal(t, camID, got)

	// Unknown stable_id → empty id, nil error (NOT sql.ErrNoRows).
	got, err = db.CameraIDByStableID(ctx, "DOES-NOT-EXIST")
	require.NoError(t, err)
	require.Equal(t, "", got)

	// Empty stable_id → empty id, nil error (short-circuit, no query).
	got, err = db.CameraIDByStableID(ctx, "")
	require.NoError(t, err)
	require.Equal(t, "", got)
}

// TestCameraIDByStableID_IncludesArchived confirms the lookup scans ALL rows
// (archived included), mirroring CameraExistsByStableID. Without this, an
// archived camera whose device is still on the network would be re-enrolled as
// a duplicate by auto-discover.
func TestCameraIDByStableID_IncludesArchived(t *testing.T) {
	db := newCameraDB(t)
	ctx := context.Background()
	const camID = "cam-archived"
	const endpoint = "http://192.168.1.51:80/onvif/device_service"
	require.NoError(t, db.UpsertCamera(ctx, camID, "Soon Archived", "onvif", "", "", "", "", endpoint, "", "", "ARCH-SN"))
	require.NoError(t, db.ArchiveCameraDB(ctx, camID))

	got, err := db.CameraIDByStableID(ctx, "ARCH-SN")
	require.NoError(t, err)
	require.Equal(t, camID, got, "archived camera must still be found by stable_id (dedup must see it)")
}

// TestCameraIDByOnvifEndpoint covers the two match kinds auto-discover needs to
// distinguish: "endpoint" (same device same address — skip) vs "serial" (same
// device new address — UPDATE endpoint). See issue #121.
func TestCameraIDByOnvifEndpoint(t *testing.T) {
	db := newCameraDB(t)
	ctx := context.Background()
	const camID = "cam-ep"
	const endpoint = "http://192.168.1.50:80/onvif/device_service"
	require.NoError(t, db.UpsertCamera(ctx, camID, "EP Cam", "onvif", "", "", "", "", endpoint, "", "", "ABC"))
	// serial_number is a separate column populated via UpdateCameraMetadata.
	require.NoError(t, db.UpdateCameraMetadata(ctx, camID, "", "", "", "", "SERIAL-XYZ", 0))

	// Same endpoint → "endpoint" match (same device, same address: skip enrollment).
	id, kind, err := db.CameraIDByOnvifEndpoint(ctx, endpoint, "")
	require.NoError(t, err)
	require.Equal(t, camID, id)
	require.Equal(t, "endpoint", kind)

	// Different endpoint, same serial → "serial" match (same device, NEW address: UPDATE).
	id, kind, err = db.CameraIDByOnvifEndpoint(ctx, "http://192.168.1.99:80/onvif/device_service", "SERIAL-XYZ")
	require.NoError(t, err)
	require.Equal(t, camID, id)
	require.Equal(t, "serial", kind)

	// Different endpoint, serial matches the stable_id column (not serial_number).
	id, kind, err = db.CameraIDByOnvifEndpoint(ctx, "http://192.168.1.99:80/onvif/device_service", "ABC")
	require.NoError(t, err)
	require.Equal(t, camID, id)
	require.Equal(t, "serial", kind, "serial arg must also match the stable_id column")

	// Unknown endpoint AND unknown serial → empty id, "" kind.
	id, kind, err = db.CameraIDByOnvifEndpoint(ctx, "http://unknown/onvif/device_service", "DIFFERENT")
	require.NoError(t, err)
	require.Equal(t, "", id)
	require.Equal(t, "", kind)

	// Both empty → short-circuit, empty result.
	id, kind, err = db.CameraIDByOnvifEndpoint(ctx, "", "")
	require.NoError(t, err)
	require.Equal(t, "", id)
	require.Equal(t, "", kind)

	// Endpoint match takes priority over serial match (checked first).
	const camID2 = "cam-ep2"
	const endpoint2 = "http://192.168.1.60:80/onvif/device_service"
	require.NoError(t, db.UpsertCamera(ctx, camID2, "EP Cam 2", "onvif", "", "", "", "", endpoint2, "", "", "SHARED-SN"))
	require.NoError(t, db.UpdateCameraMetadata(ctx, camID2, "", "", "", "", "SHARED-SN", 0))
	id, kind, err = db.CameraIDByOnvifEndpoint(ctx, endpoint2, "SHARED-SN")
	require.NoError(t, err)
	require.Equal(t, camID2, id)
	require.Equal(t, "endpoint", kind, "endpoint match must be reported when both endpoint and serial match")
}

// TestCameraIDByOnvifEndpoint_DefaultPortMismatch is the regression test for
// issue #175: a row stored with an explicit default port (:80) must still be
// matched by a query without it (and vice versa). Before the fix,
// CameraIDByOnvifEndpoint did a raw WHERE onvif_endpoint=? exact match, so
// autodiscover (which discovers endpoints WITHOUT :80 from WS-Discovery
// XAddrs) would miss a camera stored WITH :80 and create a duplicate.
func TestCameraIDByOnvifEndpoint_DefaultPortMismatch(t *testing.T) {
	db := newCameraDB(t)
	ctx := context.Background()
	const camID = "cam-port"

	// Store WITH explicit :80. UpsertCamera now normalizes on write, so the
	// stored value is actually "http://192.168.1.50/onvif/device_service"; we
	// also directly exercise the un-normalized stored case below.
	require.NoError(t, db.UpsertCamera(ctx, camID, "Port Cam", "onvif", "", "", "", "",
		"http://192.168.1.50:80/onvif/device_service", "", "", ""))

	// Query WITHOUT :80 → must match (this is the autodiscover discovery form).
	id, kind, err := db.CameraIDByOnvifEndpoint(ctx, "http://192.168.1.50/onvif/device_service", "")
	require.NoError(t, err)
	require.Equal(t, camID, id)
	require.Equal(t, "endpoint", kind)

	// Query WITH :80 → must still match (normalized comparison).
	id, kind, err = db.CameraIDByOnvifEndpoint(ctx, "http://192.168.1.50:80/onvif/device_service", "")
	require.NoError(t, err)
	require.Equal(t, camID, id)
	require.Equal(t, "endpoint", kind)

	// Simulate a LEGACY un-normalized row (written before UpsertCamera
	// normalized on write) by directly poking the column to contain :80.
	_, err = db.db.ExecContext(ctx, `UPDATE cameras SET onvif_endpoint=? WHERE id=?`,
		"http://192.168.1.50:80/onvif/device_service", camID)
	require.NoError(t, err)
	// Now a query WITHOUT :80 must STILL match via the normalize-and-compare
	// fallback scan — this is the exact scenario from #175.
	id, kind, err = db.CameraIDByOnvifEndpoint(ctx, "http://192.168.1.50/onvif/device_service", "")
	require.NoError(t, err)
	require.Equal(t, camID, id, "legacy row stored with :80 must match a query without :80 (#175)")
	require.Equal(t, "endpoint", kind)
}

// TestCameraExistsByOnvifEndpoint_DefaultPortMismatch mirrors the above for the
// sibling Exists query, which had the same exact-match bug.
func TestCameraExistsByOnvifEndpoint_DefaultPortMismatch(t *testing.T) {
	db := newCameraDB(t)
	ctx := context.Background()
	// Legacy row stored with explicit :80 via direct column poke.
	require.NoError(t, db.UpsertCamera(ctx, "cam-ex", "Exists Cam", "onvif", "", "", "", "",
		"http://192.168.1.70/onvif/device_service", "", "", ""))
	_, err := db.db.ExecContext(ctx, `UPDATE cameras SET onvif_endpoint=? WHERE id=?`,
		"http://192.168.1.70:80/onvif/device_service", "cam-ex")
	require.NoError(t, err)

	found, err := db.CameraExistsByOnvifEndpoint(ctx, "http://192.168.1.70/onvif/device_service", "")
	require.NoError(t, err)
	require.True(t, found, "query without :80 must match legacy row stored with :80 (#175)")
}
