package camera

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

// TestEnsureStableIDWritesDB verifies that ensureStableID writes stable_id
// to both YAML config and DB. The YAML write is the source of truth; the DB
// write is best-effort and must not return or panic on failure.
func TestEnsureStableIDWritesDB(t *testing.T) {
	mgr, _, db, configPath := newTestManager(t)

	ctx := context.Background()
	cameraID := "cam-onvif-test"
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID:       cameraID,
		Name:     "ONVIF Test Camera",
		Protocol: "onvif",
		URL:      "http://192.168.1.100/onvif/device_service",
		// StableID intentionally empty — ensureStableID should populate it.
	})
	mgr.reseedSnapshotConfigs()

	// Pre-seed deviceInfoCache so GetCachedDeviceInfo returns a serial.
	const testSerial = "ONVIF-SERIAL-001"
	mgr.deviceInfoMu.Lock()
	mgr.deviceInfoCache[cameraID] = &onvif.DeviceInfo{SerialNumber: testSerial}
	mgr.deviceInfoMu.Unlock()

	// Insert camera into DB so UpdateCameraStableID has a row to update.
	require.NoError(t, db.UpsertCamera(ctx, cameraID, "ONVIF Test Camera", "onvif", "",
		"http://192.168.1.100/onvif/device_service", "", "", "", "", "", ""))

	// Act
	mgr.ensureStableID(cameraID)

	// Assert: camera updated in YAML config.
	camCfg := mgr.GetCameraConfig(cameraID)
	require.NotNil(t, camCfg)
	assert.Equal(t, testSerial, camCfg.StableID,
		"ensureStableID should populate YAML config stable_id")

	// Assert: camera updated in DB.
	dbStableID, err := db.GetCameraStableID(ctx, cameraID)
	require.NoError(t, err)
	assert.Equal(t, testSerial, dbStableID,
		"ensureStableID should write stable_id to database")

	// Assert: YAML config was persisted to disk.
	loadedCfg, err := config.Load(configPath)
	require.NoError(t, err)
	var found bool
	for _, cam := range loadedCfg.Cameras {
		if cam.ID == cameraID {
			assert.Equal(t, testSerial, cam.StableID, "stable_id should be persisted to YAML on disk")
			found = true
			break
		}
	}
	assert.True(t, found, "camera should exist in persisted YAML config")
}

// TestEnsureStableIDWritesDB_FailureIsWarnOnly verifies that when DB write
// fails, ensureStableID still writes YAML and does not panic or return error.
func TestEnsureStableIDWritesDB_FailureIsWarnOnly(t *testing.T) {
	mgr, _, db, _ := newTestManager(t)

	cameraID := "cam-onvif-fail"
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID:       cameraID,
		Name:     "ONVIF Fail Camera",
		Protocol: "onvif",
		URL:      "http://192.168.1.100/onvif/device_service",
	})
	mgr.reseedSnapshotConfigs()

	const testSerial = "ONVIF-SERIAL-FAIL"
	mgr.deviceInfoMu.Lock()
	mgr.deviceInfoCache[cameraID] = &onvif.DeviceInfo{SerialNumber: testSerial}
	mgr.deviceInfoMu.Unlock()

	// Insert camera into DB so the UPDATE finds a row.
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, cameraID, "ONVIF Fail Camera", "onvif", "",
		"http://192.168.1.100/onvif/device_service", "", "", "", "", "", ""))

	// Simulate DB failure by closing the DB before ensureStableID.
	db.Close()

	// Act: must not panic despite DB being closed.
	require.NotPanics(t, func() {
		mgr.ensureStableID(cameraID)
	}, "ensureStableID must not panic when DB write fails")

	// Assert: YAML config was still written (source of truth).
	camCfg := mgr.GetCameraConfig(cameraID)
	require.NotNil(t, camCfg)
	assert.Equal(t, testSerial, camCfg.StableID,
		"stable_id should still be written to YAML config even when DB write fails")
}

// TestStartupBackfillStableID verifies the startup backfill goroutine writes
// stable_id from YAML config to DB for cameras that have it in YAML but not
// in DB (e.g. pre-migration cameras).
func TestStartupBackfillStableID(t *testing.T) {
	mgr, _, db, configPath := newTestManager(t)
	t.Cleanup(func() { db.Close() })

	// Set up cameras: some with yaml stable_id, some without.
	// Camera 1: has yaml stable_id "STABLE-A" → should backfill to DB.
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID:       "cam-with-stable",
		Name:     "Has Stable ID",
		Protocol: "onvif",
		StableID: "STABLE-A",
	})
	// Camera 2: also has yaml stable_id "STABLE-B" → should backfill.
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID:       "cam-with-stable-2",
		Name:     "Has Stable ID 2",
		Protocol: "onvif",
		StableID: "STABLE-B",
	})
	// Camera 3: yaml stable_id empty, non-ONVIF → should be skipped.
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID:       "cam-non-onvif",
		Name:     "Non-ONVIF",
		Protocol: "rtsp",
		StableID: "",
	})
	// Camera 4: yaml stable_id "STABLE-D", DB already has it → no-op.
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID:       "cam-already-in-db",
		Name:     "Already in DB",
		Protocol: "onvif",
		StableID: "STABLE-D",
	})
	mgr.reseedSnapshotConfigs()
	// Persist the config to disk so Start's iteration has the same data.
	require.NoError(t, config.Save(configPath, mgr.cfg))

	// Pre-seed DB stable_id for cam-already-in-db (to test no-op).
	ctx := context.Background()
	require.NoError(t, db.UpdateCameraStableID(ctx, "cam-already-in-db", "STABLE-D"))

	// Pre-seed deviceInfoCache for cam-non-onvif (should be skipped regardless).
	mgr.deviceInfoMu.Lock()
	mgr.deviceInfoCache["cam-non-onvif"] = &onvif.DeviceInfo{SerialNumber: "SHOULD-NOT-APPEAR"}
	mgr.deviceInfoMu.Unlock()

	// Act: Start() triggers backfill goroutine.
	// Use a fresh manager to simulate real startup (we call Start directly).
	// The backfill runs in a goroutine, so we wait for it.
	ctx, cancel := context.WithCancel(context.Background())
	// Register Stop+cancel as a t.Cleanup that runs BEFORE the db.Close cleanup
	// registered by newTestManager (LIFO). Stop() waits for the backfill
	// goroutine so it cannot outlive db.Close.
	t.Cleanup(func() {
		cancel()
		require.NoError(t, mgr.Stop())
	})
	err := mgr.Start(ctx)
	require.NoError(t, err)

	// Wait for backfill goroutine to complete.
	require.Eventually(t, func() bool {
		// Camera 1: should be backfilled.
		s1, err := db.GetCameraStableID(context.Background(), "cam-with-stable")
		if err != nil || s1 != "STABLE-A" {
			return false
		}
		// Camera 2: should be backfilled.
		s2, err := db.GetCameraStableID(context.Background(), "cam-with-stable-2")
		if err != nil || s2 != "STABLE-B" {
			return false
		}
		// Camera 3: non-ONVIF, should remain empty (no stable_id forced).
		s3, err := db.GetCameraStableID(context.Background(), "cam-non-onvif")
		if err != nil || s3 != "" {
			return false
		}
		// Camera 4: already had DB stable_id, should still be "STABLE-D".
		s4, err := db.GetCameraStableID(context.Background(), "cam-already-in-db")
		if err != nil || s4 != "STABLE-D" {
			return false
		}
		return true
	}, 3*time.Second, 50*time.Millisecond,
		"backfill should populate DB stable_id from YAML for cameras 1/2, skip 3/4")
}

// TestStartupBackfillStableID_NonBlocking verifies that the backfill does NOT
// block Start() — Start() returns promptly even for many cameras.
func TestStartupBackfillStableID_NonBlocking(t *testing.T) {
	mgr, _, db, _ := newTestManager(t)
	t.Cleanup(func() { db.Close() })

	// Add 20 cameras with yaml stable_id to simulate load.
	for i := range 20 {
		camID := "cam-backfill-" + itoa(i)
		mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
			ID:       camID,
			Name:     "Backfill " + itoa(i),
			Protocol: "onvif",
			StableID: "STABLE-" + itoa(i),
		})
	}
	mgr.reseedSnapshotConfigs()

	ctx, cancel := context.WithCancel(context.Background())
	// Register Stop+cancel as a t.Cleanup that runs BEFORE the db.Close cleanup
	// registered by newTestManager (t.Cleanup is LIFO: last-registered runs
	// first). Stop() waits for the backfill goroutine to exit, which prevents
	// the "sql: database is closed" race where the goroutine issues db calls
	// after db.Close has already torn down the connection.
	t.Cleanup(func() {
		cancel()
		require.NoError(t, mgr.Stop())
	})

	// Act: Start() should return quickly (backfill runs in goroutine).
	start := time.Now()
	err := mgr.Start(ctx)
	require.NoError(t, err)
	elapsed := time.Since(start)

	// Start must return quickly despite 20 backfill items (goroutine).
	// The threshold is generous (1s) because CI runners are slow and Start
	// does real work beyond just spawning the goroutine (config snapshot,
	// ONVIF client init, etc.). The assertion's purpose is to catch
	// "backfill runs synchronously" regressions, not to benchmark Start —
	// backfill itself takes seconds, so anything well below that is fine.
	assert.Less(t, elapsed, 1*time.Second,
		"Start() must return quickly; backfill runs in a goroutine")

	// Eventually, all 20 cameras should be backfilled.
	require.Eventually(t, func() bool {
		for i := range 20 {
			s, err := db.GetCameraStableID(context.Background(), "cam-backfill-"+itoa(i))
			if err != nil || s != "STABLE-"+itoa(i) {
				return false
			}
		}
		return true
	}, 5*time.Second, 100*time.Millisecond,
		"all 20 cameras should eventually have their DB stable_id backfilled")
}

// itoa is a minimal int-to-string for test usage (no strconv import needed).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestEnsureEncodingWritesBoth verifies that ensureEncoding persists the
// recorder-resolved encoding to BOTH YAML config and DB (the dual-write that
// makes the value race-safe against UpsertCamera's full-row overwrite). This is
// the core fix for issue #112: without persistence, an ONVIF auto-detect camera
// (ESP32 MiBeeCam) carries encoding="" and a brief outage makes the frontend
// lose the codec → protocol storm.
func TestEnsureEncodingWritesBoth(t *testing.T) {
	mgr, _, db, configPath := newTestManager(t)
	ctx := context.Background()
	cameraID := "cam-enc-onvif"
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID:       cameraID,
		Name:     "Enc ONVIF Cam",
		Protocol: "onvif",
		// Encoding intentionally empty — ensureEncoding should populate it.
	})
	mgr.reseedSnapshotConfigs()

	// Insert a DB row (empty encoding) so UpdateCameraEncoding has a target.
	require.NoError(t, db.UpsertCamera(ctx, cameraID, "Enc ONVIF Cam", "onvif", "",
		"", "", "", "", "", "", ""))

	// Inject an ONVIF recorder that reports a resolved encoding + recording
	// status, the way a real recorder would after Start() probes RTSP DESCRIBE.
	rec := recorder.NewONVIFRecorder(
		recorder.ONVIFConfig{CameraID: cameraID},
		&onvif.MockDeviceClient{},
		nil, // store unused — recorder never runs a real Start here
	)
	rec.SetResolvedEncodingForTest("H265", model.StatusRecording)
	mgr.SetTestRecorder(cameraID, rec)

	// Act.
	mgr.ensureEncoding(cameraID)

	// Assert: YAML config updated (lowercase; MJPEG→jpeg normalization).
	camCfg := mgr.GetCameraConfig(cameraID)
	require.NotNil(t, camCfg)
	assert.Equal(t, "h265", camCfg.Encoding, "ensureEncoding should populate YAML config encoding (lowercase)")
	assert.Equal(t, "H265", camCfg.StreamEncoding, "ensureEncoding should populate YAML config stream_encoding (uppercase)")

	// Assert: DB updated.
	dbEnc, err := db.GetCameraEncoding(ctx, cameraID)
	require.NoError(t, err)
	assert.Equal(t, "h265", dbEnc, "ensureEncoding should write encoding to DB")

	// Assert: YAML persisted to disk.
	loadedCfg, err := config.Load(configPath)
	require.NoError(t, err)
	var found bool
	for _, cam := range loadedCfg.Cameras {
		if cam.ID == cameraID {
			assert.Equal(t, "h265", cam.Encoding, "encoding persisted to YAML on disk")
			found = true
			break
		}
	}
	assert.True(t, found, "camera present in persisted YAML")
}

// TestEnsureEncoding_NormalizesMJPEGToJPEG confirms the MJPEG-over-RTSP case
// (internal code "MJPEG") is persisted as "jpeg" so the frontend's MJPEG
// player short-circuit (which keys on encoding=="jpeg"|"mjpeg") selects the
// right player even when the device is later unreachable. This is the exact
// scenario from issue #112 (MiBeeCam-S3).
func TestEnsureEncoding_NormalizesMJPEGToJPEG(t *testing.T) {
	mgr, _, db, _ := newTestManager(t)
	ctx := context.Background()
	cameraID := "cam-mjpeg-rtsp"
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID: cameraID, Name: "MJPEG Cam", Protocol: "onvif",
	})
	mgr.reseedSnapshotConfigs()
	require.NoError(t, db.UpsertCamera(ctx, cameraID, "MJPEG Cam", "onvif", "", "", "", "", "", "", "", ""))

	rec := recorder.NewONVIFRecorder(
		recorder.ONVIFConfig{CameraID: cameraID},
		&onvif.MockDeviceClient{}, nil,
	)
	// detectEncoding returns "MJPEG" for MJPEG-over-RTSP (ESP32 RTSP-AVI firmware).
	rec.SetResolvedEncodingForTest("MJPEG", model.StatusRecording)
	mgr.SetTestRecorder(cameraID, rec)

	mgr.ensureEncoding(cameraID)

	camCfg := mgr.GetCameraConfig(cameraID)
	require.NotNil(t, camCfg)
	assert.Equal(t, "jpeg", camCfg.Encoding, "MJPEG should normalize to jpeg for player selection")
	// MJPEG-over-RTSP does NOT populate stream_encoding (only H264/H265 do).
	assert.Equal(t, "", camCfg.StreamEncoding)
}

// TestEnsureEncoding_IdempotentWhenAlreadySet confirms ensureEncoding is a
// no-op when the camera already has an encoding (mirrors ensureStableID's
// already-set guard). This prevents re-probe races and unnecessary writes.
func TestEnsureEncoding_IdempotentWhenAlreadySet(t *testing.T) {
	mgr, _, db, _ := newTestManager(t)
	ctx := context.Background()
	cameraID := "cam-already-set"
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID: cameraID, Name: "Already Set", Protocol: "onvif", Encoding: "h264",
	})
	mgr.reseedSnapshotConfigs()
	require.NoError(t, db.UpsertCamera(ctx, cameraID, "Already Set", "onvif", "h264", "", "", "", "", "", "", ""))

	rec := recorder.NewONVIFRecorder(
		recorder.ONVIFConfig{CameraID: cameraID},
		&onvif.MockDeviceClient{}, nil,
	)
	// Recorder would resolve h265, but since config already says h264 we keep it.
	rec.SetResolvedEncodingForTest("H265", model.StatusRecording)
	mgr.SetTestRecorder(cameraID, rec)

	mgr.ensureEncoding(cameraID)

	camCfg := mgr.GetCameraConfig(cameraID)
	require.NotNil(t, camCfg)
	assert.Equal(t, "h264", camCfg.Encoding, "existing encoding must not be overwritten")
}
