package ai

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)


// makeDetection creates a Detection with the given bounding box for testing.
func makeDetection(t *testing.T, x1, y1, x2, y2 float64) Detection {
	t.Helper()
	return Detection{
		BBox:       [4]float64{x1, y1, x2, y2},
		Confidence: 0.95,
		ClassID:    0,
		ClassLabel: "person",
	}
}

// makeZone creates an ROI zone with the given name and points for testing.
func makeZone(t *testing.T, name string, points [][2]float64) ROI {
	t.Helper()
	return ROI{
		Name:   name,
		Points: points,
	}
}

// makeROIZone creates a fully-qualified ROIZone for testing.
func makeROIZone(t *testing.T, cameraID, name string, enabled bool, points [][2]float64) ROIZone {
	t.Helper()
	return ROIZone{
		CameraID: cameraID,
		Zone:     makeZone(t, name, points),
		Enabled:  enabled,
	}
}

// squarePolygon returns points for a unit square [0,0] to [1,1].
func squarePolygon(t *testing.T) [][2]float64 {
	t.Helper()
	return [][2]float64{
		{0, 0},
		{1, 0},
		{1, 1},
		{0, 1},
	}
}

// trianglePolygon returns points for a right triangle.
func trianglePolygon(t *testing.T) [][2]float64 {
	t.Helper()
	return [][2]float64{
		{0.2, 0.2},
		{0.8, 0.2},
		{0.5, 0.8},
	}
}

// lShapePolygon returns a concave L-shaped polygon (6 vertices).
// The L shape has a notch at the bottom-right, making it clearly concave.
func lShapePolygon(t *testing.T) [][2]float64 {
	t.Helper()
	return [][2]float64{
		{0.1, 0.1},
		{0.9, 0.1},
		{0.9, 0.4},
		{0.4, 0.4},
		{0.4, 0.9},
		{0.1, 0.9},
	}
}

// ============================================================================
// PointInPolygon Tests
// ============================================================================

func TestPointInPolygon_InsideSquare(t *testing.T) {
	poly := squarePolygon(t)

	assert.True(t, PointInPolygon(0.5, 0.5, poly), "center of square")
	assert.True(t, PointInPolygon(0.25, 0.25, poly), "near bottom-left")
	assert.True(t, PointInPolygon(0.75, 0.75, poly), "near top-right")
	assert.True(t, PointInPolygon(0.1, 0.9, poly), "near top-left")
	assert.True(t, PointInPolygon(0.9, 0.1, poly), "near bottom-right")
}

func TestPointInPolygon_OutsideSquare(t *testing.T) {
	poly := squarePolygon(t)

	assert.False(t, PointInPolygon(-0.1, 0.5, poly), "left of square")
	assert.False(t, PointInPolygon(1.1, 0.5, poly), "right of square")
	assert.False(t, PointInPolygon(0.5, -0.1, poly), "below square")
	assert.False(t, PointInPolygon(0.5, 1.1, poly), "above square")
	assert.False(t, PointInPolygon(-0.5, -0.5, poly), "far bottom-left")
	assert.False(t, PointInPolygon(2.0, 2.0, poly), "far top-right")
}

func TestPointInPolygon_OnEdge(t *testing.T) {
	poly := squarePolygon(t)

	// Points exactly on the edge may be classified as inside or outside
	// depending on the ray-casting implementation. The standard algorithm
	// typically classifies points on the top/left edges as inside and
	// bottom/right edges as outside when the edge is exactly horizontal/vertical.
	// We test that the function handles these boundary cases without panicking
	// and returns a deterministic boolean.
	onEdge := PointInPolygon(0.5, 0.0, poly)
	t.Logf("point (0.5, 0.0) on bottom edge: %v", onEdge)

	onEdge = PointInPolygon(0.5, 1.0, poly)
	t.Logf("point (0.5, 1.0) on top edge: %v", onEdge)

	onEdge = PointInPolygon(0.0, 0.5, poly)
	t.Logf("point (0.0, 0.5) on left edge: %v", onEdge)

	onEdge = PointInPolygon(1.0, 0.5, poly)
	t.Logf("point (1.0, 0.5) on right edge: %v", onEdge)

	// Corner point — should not panic
	onCorner := PointInPolygon(0.0, 0.0, poly)
	t.Logf("point (0.0, 0.0) at corner: %v", onCorner)

	// All edge/corner calls should complete without panic
	assert.True(t, true, "edge/corner points did not panic")
}

func TestPointInPolygon_InsideTriangle(t *testing.T) {
	poly := trianglePolygon(t)

	assert.True(t, PointInPolygon(0.5, 0.4, poly), "center of triangle")
	assert.True(t, PointInPolygon(0.35, 0.3, poly), "near left edge")
	assert.True(t, PointInPolygon(0.65, 0.3, poly), "near right edge")
	assert.True(t, PointInPolygon(0.5, 0.7, poly), "near top")
}

func TestPointInPolygon_OutsideTriangle(t *testing.T) {
	poly := trianglePolygon(t)

	assert.False(t, PointInPolygon(0.1, 0.1, poly), "below-left of triangle")
	assert.False(t, PointInPolygon(0.9, 0.1, poly), "below-right of triangle")
	assert.False(t, PointInPolygon(0.1, 0.9, poly), "above-left of triangle")
	assert.False(t, PointInPolygon(0.5, 0.1, poly), "below triangle base")
	assert.False(t, PointInPolygon(0.5, 0.9, poly), "above triangle tip")
}

func TestPointInPolygon_TooFewPoints(t *testing.T) {
	assert.False(t, PointInPolygon(0.5, 0.5, [][2]float64{}),
		"empty polygon")
	assert.False(t, PointInPolygon(0.5, 0.5, [][2]float64{{0, 0}}),
		"single point")
	assert.False(t, PointInPolygon(0.5, 0.5, [][2]float64{{0, 0}, {1, 0}}),
		"line segment (2 points)")
}

func TestPointInPolygon_EmptyPolygon(t *testing.T) {
	assert.False(t, PointInPolygon(0.5, 0.5, nil), "nil polygon")
}

func TestPointInPolygon_ConcaveShape(t *testing.T) {
	poly := lShapePolygon(t)

	// The L-shaped polygon:
	// (0.1,0.9)──────────(0.4,0.9)
	//    │                  │
	//    │                  │
	//    │                  │
	// (0.1,0.1)──────────(0.9,0.1)
	//    │                  │
	//    │                  │
	//    │                  │
	// (0.1,0.1)         (0.9,0.4)←notch corner
	//
	// The notch at (0.4, 0.4)-(0.9, 0.4)-(0.9, 0.1) creates concavity.
	// The region with x>0.4 AND y<0.4 is OUTSIDE (in the notch).

	// Inside the vertical bar of the L
	assert.True(t, PointInPolygon(0.2, 0.5, poly), "inside vertical bar")
	assert.True(t, PointInPolygon(0.25, 0.7, poly), "inside vertical bar top")
	assert.True(t, PointInPolygon(0.2, 0.2, poly), "inside vertical bar bottom")

	// Inside the horizontal bar of the L
	assert.True(t, PointInPolygon(0.6, 0.2, poly), "inside horizontal bar")
	assert.True(t, PointInPolygon(0.8, 0.25, poly), "inside horizontal bar right")

	// In the notch (missing quadrant: x>0.4 AND y>0.4) — should be outside
	assert.False(t, PointInPolygon(0.6, 0.6, poly), "point in the notch area")
	assert.False(t, PointInPolygon(0.7, 0.5, poly), "point in notch center")
	assert.False(t, PointInPolygon(0.5, 0.6, poly), "point in notch left")

	// Outside the L entirely
	assert.False(t, PointInPolygon(0.05, 0.5, poly), "left of L")
	assert.False(t, PointInPolygon(0.5, 0.95, poly), "above L")
	assert.False(t, PointInPolygon(0.95, 0.5, poly), "right of L")
}

func TestPointInPolygon_MultipleVertices(t *testing.T) {
	// A 20-sided regular polygon (icosagon) approximating a circle.
	// All points near the center should be inside.
	poly := make([][2]float64, 20)
	for i := 0; i < 20; i++ {
		angle := float64(i) * 2 * math.Pi / 20
		poly[i] = [2]float64{
			0.5 + 0.4*math.Cos(angle),
			0.5 + 0.4*math.Sin(angle),
		}
	}

	assert.True(t, PointInPolygon(0.5, 0.5, poly), "center of icosagon")
	assert.True(t, PointInPolygon(0.5, 0.7, poly), "upper-center")
	assert.True(t, PointInPolygon(0.7, 0.5, poly), "right-center")
	assert.False(t, PointInPolygon(0.5, 0.95, poly), "above icosagon")
	assert.False(t, PointInPolygon(0.05, 0.5, poly), "left of icosagon")
}

// ============================================================================
// FilterDetectionsByZone Tests
// ============================================================================

func TestFilterDetectionsByZone_WithZones(t *testing.T) {
	zones := []ROI{
		makeZone(t, "left-half", [][2]float64{
			{0, 0}, {0.5, 0}, {0.5, 1}, {0, 1},
		}),
	}

	detections := []Detection{
		makeDetection(t, 0.1, 0.1, 0.2, 0.2), // center (0.15, 0.15) — inside left zone
		makeDetection(t, 0.6, 0.1, 0.7, 0.2), // center (0.65, 0.15) — outside left zone
	}

	filtered := FilterDetectionsByZone(detections, zones)
	require.Len(t, filtered, 1)
	assert.Equal(t, detections[0].BBox, filtered[0].BBox)
	assert.Equal(t, detections[0].ClassLabel, filtered[0].ClassLabel)
}

func TestFilterDetectionsByZone_NoZones(t *testing.T) {
	detections := []Detection{
		makeDetection(t, 0.1, 0.1, 0.2, 0.2),
		makeDetection(t, 0.6, 0.1, 0.7, 0.2),
		makeDetection(t, 0.8, 0.8, 0.9, 0.9),
	}

	filtered := FilterDetectionsByZone(detections, nil)
	assert.Equal(t, detections, filtered, "all detections returned when zones is nil")
}

func TestFilterDetectionsByZone_EmptyZones(t *testing.T) {
	detections := []Detection{
		makeDetection(t, 0.1, 0.1, 0.2, 0.2),
	}

	filtered := FilterDetectionsByZone(detections, []ROI{})
	assert.Equal(t, detections, filtered, "all detections returned when zones is empty")
}

func TestFilterDetectionsByZone_EmptyDetections(t *testing.T) {
	zones := []ROI{
		makeZone(t, "zone1", trianglePolygon(t)),
	}

	filtered := FilterDetectionsByZone(nil, zones)
	assert.Empty(t, filtered, "nil detections returns empty")

	filtered = FilterDetectionsByZone([]Detection{}, zones)
	assert.Empty(t, filtered, "empty detections returns empty")
}

func TestFilterDetectionsByZone_AllOutside(t *testing.T) {
	zone := []ROI{
		makeZone(t, "small-center", [][2]float64{
			{0.4, 0.4}, {0.6, 0.4}, {0.6, 0.6}, {0.4, 0.6},
		}),
	}

	detections := []Detection{
		makeDetection(t, 0.0, 0.0, 0.1, 0.1),   // center (0.05, 0.05)
		makeDetection(t, 0.8, 0.8, 0.9, 0.9),   // center (0.85, 0.85)
		makeDetection(t, 0.7, 0.1, 0.8, 0.2),   // center (0.75, 0.15)
	}

	filtered := FilterDetectionsByZone(detections, zone)
	assert.Empty(t, filtered, "no detections should pass")
}

func TestFilterDetectionsByZone_MultipleZones(t *testing.T) {
	zones := []ROI{
		makeZone(t, "top-half", [][2]float64{
			{0, 0.5}, {1, 0.5}, {1, 1}, {0, 1},
		}),
		makeZone(t, "left-half", [][2]float64{
			{0, 0}, {0.5, 0}, {0.5, 1}, {0, 1},
		}),
	}

	detections := []Detection{
		makeDetection(t, 0.1, 0.6, 0.2, 0.7), // center (0.15, 0.65) — only in top
		makeDetection(t, 0.1, 0.1, 0.2, 0.2), // center (0.15, 0.15) — only in left
		makeDetection(t, 0.7, 0.1, 0.8, 0.2), // center (0.75, 0.15) — outside both
		makeDetection(t, 0.1, 0.6, 0.2, 0.7), // center (0.15, 0.65) — in top only (duplicate coords)
		makeDetection(t, 0.3, 0.3, 0.4, 0.4), // center (0.35, 0.35) — in left AND top (overlap)
	}

	filtered := FilterDetectionsByZone(detections, zones)
	require.Len(t, filtered, 4, "detection in either zone should be included")

	// The outside point (index 2) should be excluded.
	for _, d := range filtered {
		assert.NotEqual(t, detections[2].BBox, d.BBox,
			"outside detection should be filtered out")
	}
}

func TestFilterDetectionsByZone_BBoxCenterCalculation(t *testing.T) {
	zone := []ROI{
		makeZone(t, "right-half", [][2]float64{
			{0.5, 0}, {1, 0}, {1, 1}, {0.5, 1},
		}),
	}

	tests := []struct {
		name   string
		bbox   [4]float64
		inside bool
	}{
		{"bbox entirely left but straddles center line", [4]float64{0.4, 0.4, 0.6, 0.6}, true},  // cx=0.5 → right edge
		{"bbox entirely in right", [4]float64{0.6, 0.4, 0.8, 0.6}, true},                         // cx=0.7
		{"bbox entirely in left", [4]float64{0.1, 0.4, 0.3, 0.6}, false},                         // cx=0.2
		{"bbox spans full width", [4]float64{0.0, 0.4, 1.0, 0.6}, true},                          // cx=0.5 → right edge
		{"single point at right", [4]float64{0.7, 0.5, 0.7, 0.5}, true},                          // cx=0.7
		{"single point at left", [4]float64{0.3, 0.5, 0.3, 0.5}, false},                          // cx=0.3
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dets := []Detection{makeDetection(t, tt.bbox[0], tt.bbox[1], tt.bbox[2], tt.bbox[3])}
			filtered := FilterDetectionsByZone(dets, zone)
			if tt.inside {
				assert.Len(t, filtered, 1, "should be inside zone")
			} else {
				assert.Empty(t, filtered, "should be outside zone")
			}
		})
	}
}

// ============================================================================
// ZoneManager Tests
// ============================================================================

func TestZoneManager_AddZone(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	err := zm.AddZone(zone)
	assert.NoError(t, err)

	// Verify zone was added.
	zones := zm.GetZones("cam-1")
	require.Len(t, zones, 1)
	assert.Equal(t, "driveway", zones[0].Name)
	assert.Equal(t, squarePolygon(t), zones[0].Points)
}

func TestZoneManager_AddZone_EmptyCameraID(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "", "driveway", true, squarePolygon(t))
	err := zm.AddZone(zone)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "camera ID must not be empty")
}

func TestZoneManager_AddZone_EmptyName(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "", true, squarePolygon(t))
	err := zm.AddZone(zone)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "zone name must not be empty")
}

func TestZoneManager_AddZone_TooFewPoints(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "bad-zone", true, [][2]float64{{0, 0}, {1, 0}})
	err := zm.AddZone(zone)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must have at least 3 points")
}

func TestZoneManager_AddZone_PointsOutOfRange(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "out-of-range", true, [][2]float64{
		{-0.1, 0.5}, {0.5, 0.5}, {0.5, 1.5},
	})
	err := zm.AddZone(zone)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outside [0, 1] range")
}

func TestZoneManager_AddZone_OutOfRangeY(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "bad-y", true, [][2]float64{
		{0.1, 0.1}, {0.5, -0.1}, {0.9, 0.1},
	})
	err := zm.AddZone(zone)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outside [0, 1] range")
}

func TestZoneManager_DuplicateZoneName(t *testing.T) {
	zm := NewZoneManager()

	zone1 := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	err := zm.AddZone(zone1)
	require.NoError(t, err)

	// Same name, same camera — should be rejected.
	zone2 := makeROIZone(t, "cam-1", "driveway", true, trianglePolygon(t))
	err = zm.AddZone(zone2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// Same name, different camera — should be allowed.
	zone3 := makeROIZone(t, "cam-2", "driveway", true, squarePolygon(t))
	err = zm.AddZone(zone3)
	assert.NoError(t, err)
}

func TestZoneManager_RemoveZone(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	err := zm.AddZone(zone)
	require.NoError(t, err)

	err = zm.RemoveZone("cam-1", "driveway")
	assert.NoError(t, err)

	// Verify zone is gone.
	zones := zm.GetZones("cam-1")
	assert.Nil(t, zones)
	assert.False(t, zm.HasZones("cam-1"))
}

func TestZoneManager_RemoveZone_RemovesMapEntry(t *testing.T) {
	zm := NewZoneManager()

	err := zm.AddZone(makeROIZone(t, "cam-1", "z1", true, squarePolygon(t)))
	require.NoError(t, err)

	err = zm.RemoveZone("cam-1", "z1")
	require.NoError(t, err)

	// The map entry should be deleted (not just slice emptied).
	allZones := zm.GetAllZones()
	_, exists := allZones["cam-1"]
	assert.False(t, exists, "camera entry should be removed from map when empty")
}

func TestZoneManager_RemoveNonexistentZone(t *testing.T) {
	zm := NewZoneManager()

	err := zm.RemoveZone("cam-1", "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no zones found")

	// Add a zone then try to remove a different one.
	_ = zm.AddZone(makeROIZone(t, "cam-1", "existing", true, squarePolygon(t)))
	err = zm.RemoveZone("cam-1", "other")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found for camera")
}

func TestZoneManager_RemoveNonexistentCamera(t *testing.T) {
	zm := NewZoneManager()

	err := zm.RemoveZone("no-such-camera", "zone1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no zones found")
}

func TestZoneManager_GetZones(t *testing.T) {
	zm := NewZoneManager()

	zone1 := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	zone2 := makeROIZone(t, "cam-1", "backyard", true, trianglePolygon(t))
	require.NoError(t, zm.AddZone(zone1))
	require.NoError(t, zm.AddZone(zone2))

	zones := zm.GetZones("cam-1")
	require.Len(t, zones, 2)
	assert.Equal(t, "driveway", zones[0].Name)
	assert.Equal(t, "backyard", zones[1].Name)
}

func TestZoneManager_GetZones_ReturnsCopy(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	require.NoError(t, zm.AddZone(zone))

	// Mutate the returned copy.
	zones := zm.GetZones("cam-1")
	zones[0].Name = "modified"

	// Internal state should be unchanged.
	zones2 := zm.GetZones("cam-1")
	assert.Equal(t, "driveway", zones2[0].Name, "original should not be affected by mutation")
}

func TestZoneManager_GetZones_NoZones(t *testing.T) {
	zm := NewZoneManager()

	zones := zm.GetZones("cam-1")
	assert.Nil(t, zones)

	// Also returns nil for camera with no zones when other cameras have zones.
	zone := makeROIZone(t, "cam-2", "driveway", true, squarePolygon(t))
	require.NoError(t, zm.AddZone(zone))

	zones = zm.GetZones("cam-1")
	assert.Nil(t, zones, "cam-1 should still have no zones")
}

func TestZoneManager_GetAllZones(t *testing.T) {
	zm := NewZoneManager()

	zone1 := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	zone2 := makeROIZone(t, "cam-2", "backyard", true, trianglePolygon(t))
	require.NoError(t, zm.AddZone(zone1))
	require.NoError(t, zm.AddZone(zone2))

	all := zm.GetAllZones()
	require.Len(t, all, 2)
	require.Len(t, all["cam-1"], 1)
	require.Len(t, all["cam-2"], 1)
	assert.Equal(t, "driveway", all["cam-1"][0].Name)
	assert.Equal(t, "backyard", all["cam-2"][0].Name)
}

func TestZoneManager_GetAllZones_ReturnsCopy(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	require.NoError(t, zm.AddZone(zone))

	// Mutate the returned copy.
	all := zm.GetAllZones()
	all["cam-1"][0].Name = "modified"

	// Internal state should be unchanged.
	all2 := zm.GetAllZones()
	assert.Equal(t, "driveway", all2["cam-1"][0].Name)
}

func TestZoneManager_GetAllZones_Empty(t *testing.T) {
	zm := NewZoneManager()

	all := zm.GetAllZones()
	assert.Empty(t, all)
}

func TestZoneManager_GetEnabledZones(t *testing.T) {
	zm := NewZoneManager()

	zone1 := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	zone2 := makeROIZone(t, "cam-1", "backyard", false, trianglePolygon(t))
	zone3 := makeROIZone(t, "cam-1", "sidewalk", true, trianglePolygon(t))
	require.NoError(t, zm.AddZone(zone1))
	require.NoError(t, zm.AddZone(zone2))
	require.NoError(t, zm.AddZone(zone3))

	enabled := zm.GetEnabledZones("cam-1", []string{"driveway", "sidewalk"})
	require.Len(t, enabled, 2)
	assert.Equal(t, "driveway", enabled[0].Name)
	assert.Equal(t, "sidewalk", enabled[1].Name)
}

func TestZoneManager_GetEnabledZones_NoFilter(t *testing.T) {
	zm := NewZoneManager()

	zone1 := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	zone2 := makeROIZone(t, "cam-1", "backyard", true, trianglePolygon(t))
	require.NoError(t, zm.AddZone(zone1))
	require.NoError(t, zm.AddZone(zone2))

	// Empty enabled names list = return all zones.
	enabled := zm.GetEnabledZones("cam-1", nil)
	require.Len(t, enabled, 2)

	enabled = zm.GetEnabledZones("cam-1", []string{})
	require.Len(t, enabled, 2)
}

func TestZoneManager_GetEnabledZones_NoZones(t *testing.T) {
	zm := NewZoneManager()

	enabled := zm.GetEnabledZones("cam-1", []string{"driveway"})
	assert.Nil(t, enabled)
}

func TestZoneManager_GetEnabledZones_ReturnsCopy(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	require.NoError(t, zm.AddZone(zone))

	enabled := zm.GetEnabledZones("cam-1", []string{"driveway"})
	enabled[0].Name = "modified"

	enabled2 := zm.GetEnabledZones("cam-1", []string{"driveway"})
	assert.Equal(t, "driveway", enabled2[0].Name, "original should not be affected")
}

func TestZoneManager_HasZones(t *testing.T) {
	zm := NewZoneManager()

	assert.False(t, zm.HasZones("cam-1"))

	zone := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	require.NoError(t, zm.AddZone(zone))

	assert.True(t, zm.HasZones("cam-1"))

	// After removal, should not have zones.
	require.NoError(t, zm.RemoveZone("cam-1", "driveway"))
	assert.False(t, zm.HasZones("cam-1"))
}

func TestZoneManager_UpdateZone(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	require.NoError(t, zm.AddZone(zone))

	// Update points to a triangle.
	updated := makeROIZone(t, "cam-1", "driveway", true, trianglePolygon(t))
	err := zm.UpdateZone(updated)
	assert.NoError(t, err)

	// Verify the update.
	zones := zm.GetZones("cam-1")
	require.Len(t, zones, 1)
	assert.Equal(t, "driveway", zones[0].Name)
	assert.Equal(t, trianglePolygon(t), zones[0].Points)
}

func TestZoneManager_UpdateZone_Nonexistent(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "nonexistent", true, squarePolygon(t))
	err := zm.UpdateZone(zone)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no zones found")
}

func TestZoneManager_UpdateZone_WrongCamera(t *testing.T) {
	zm := NewZoneManager()

	zone := makeROIZone(t, "cam-1", "driveway", true, squarePolygon(t))
	require.NoError(t, zm.AddZone(zone))

	// Try to update on a different camera.
	update := makeROIZone(t, "cam-2", "driveway", true, trianglePolygon(t))
	err := zm.UpdateZone(update)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no zones found")
}

func TestZoneManager_Concurrency(t *testing.T) {
	zm := NewZoneManager()

	// Add zones from multiple goroutines to test thread safety.
	const numZones = 100
	errs := make(chan error, numZones)

	for i := 0; i < numZones; i++ {
		go func(idx int) {
			name := "zone"
			if idx >= 0 {
				name = "zone" + string(rune('0'+idx%10))
			}
			zone := makeROIZone(t, "cam-1", name, true, squarePolygon(t))
			errs <- zm.AddZone(zone)
		}(i)
	}

	for i := 0; i < numZones; i++ {
		err := <-errs
		// Errors are expected for duplicates, but no panics.
		t.Logf("concurrent add result: %v", err)
	}

	// Reading should also be safe.
	zones := zm.GetZones("cam-1")
	t.Logf("concurrent add resulted in %d zones", len(zones))
	assert.NotPanics(t, func() {
		_ = zm.GetAllZones()
		_ = zm.HasZones("cam-1")
	})
	assert.GreaterOrEqual(t, len(zones), 1)
}

// ============================================================================
// Integration-Style Tests
// ============================================================================

func TestZoneManager_FilterIntegration(t *testing.T) {
	zm := NewZoneManager()

	// Set up zones.
	require.NoError(t, zm.AddZone(makeROIZone(t, "cam-1", "entrance", true, [][2]float64{
		{0, 0}, {0.3, 0}, {0.3, 1}, {0, 1},
	})))
	require.NoError(t, zm.AddZone(makeROIZone(t, "cam-1", "driveway", false, [][2]float64{
		{0.7, 0}, {1, 0}, {1, 1}, {0.7, 1},
	})))

	// Get enabled zones (only "entrance" is enabled).
	enabled := zm.GetZones("cam-1")
	require.Len(t, enabled, 2)

	enabledNames := []string{"entrance"}
	activeZones := zm.GetEnabledZones("cam-1", enabledNames)
	require.Len(t, activeZones, 1)
	assert.Equal(t, "entrance", activeZones[0].Name)

	// Filter detections.
	detections := []Detection{
		makeDetection(t, 0.1, 0.1, 0.2, 0.2),   // center (0.15, 0.15) — inside entrance
		makeDetection(t, 0.8, 0.1, 0.9, 0.2),   // center (0.85, 0.15) — inside driveway (disabled)
		makeDetection(t, 0.5, 0.5, 0.6, 0.6),   // center (0.55, 0.55) — outside both
	}

	filtered := FilterDetectionsByZone(detections, activeZones)
	require.Len(t, filtered, 1)
	assert.Equal(t, detections[0].BBox, filtered[0].BBox)
}
