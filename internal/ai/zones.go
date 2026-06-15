package ai

import (
	"fmt"
	"sync"
)

// ZoneManager manages ROI zones per camera with thread-safe CRUD operations.
// Zones define regions of interest in normalized coordinates [0, 1].
// They are used to filter AI detections to only include objects within
// specified regions of the frame.
type ZoneManager struct {
	mu    sync.RWMutex
	zones map[string][]ROI // cameraID → list of zones
}

// NewZoneManager creates a new ZoneManager with an empty zone map.
func NewZoneManager() *ZoneManager {
	return &ZoneManager{
		zones: make(map[string][]ROI),
	}
}

// AddZone adds a zone to a camera. Returns an error if validation fails
// (empty name, fewer than 3 points, points outside [0,1], empty cameraID,
// or duplicate zone name for the same camera).
func (zm *ZoneManager) AddZone(zone ROIZone) error {
	if err := validateZone(zone); err != nil {
		return err
	}

	zm.mu.Lock()
	defer zm.mu.Unlock()

	// Check for duplicate zone name per camera.
	for _, existing := range zm.zones[zone.CameraID] {
		if existing.Name == zone.Zone.Name {
			return fmt.Errorf("ai: zone %q already exists for camera %q", zone.Zone.Name, zone.CameraID)
		}
	}

	zm.zones[zone.CameraID] = append(zm.zones[zone.CameraID], zone.Zone)
	return nil
}

// RemoveZone removes a zone by camera ID and zone name.
// Returns an error if the zone is not found.
func (zm *ZoneManager) RemoveZone(cameraID, zoneName string) error {
	zm.mu.Lock()
	defer zm.mu.Unlock()

	zones, ok := zm.zones[cameraID]
	if !ok {
		return fmt.Errorf("ai: no zones found for camera %q", cameraID)
	}

	for i, z := range zones {
		if z.Name == zoneName {
			zm.zones[cameraID] = append(zones[:i], zones[i+1:]...)
			// Clean up empty slice entries to avoid map accumulation.
			if len(zm.zones[cameraID]) == 0 {
				delete(zm.zones, cameraID)
			}
			return nil
		}
	}

	return fmt.Errorf("ai: zone %q not found for camera %q", zoneName, cameraID)
}

// UpdateZone updates an existing zone's polygon points and enabled state.
// The zone is matched by CameraID and Zone.Name. Returns an error if the
// zone does not exist or validation fails.
func (zm *ZoneManager) UpdateZone(zone ROIZone) error {
	if err := validateZone(zone); err != nil {
		return err
	}

	zm.mu.Lock()
	defer zm.mu.Unlock()

	zones, ok := zm.zones[zone.CameraID]
	if !ok {
		return fmt.Errorf("ai: no zones found for camera %q", zone.CameraID)
	}

	for i, z := range zones {
		if z.Name == zone.Zone.Name {
			zm.zones[zone.CameraID][i] = zone.Zone
			return nil
		}
	}

	return fmt.Errorf("ai: zone %q not found for camera %q", zone.Zone.Name, zone.CameraID)
}

// GetZones returns a copy of all zones for a given camera.
// Returns nil if no zones exist for the camera.
func (zm *ZoneManager) GetZones(cameraID string) []ROI {
	zm.mu.RLock()
	defer zm.mu.RUnlock()

	zones, ok := zm.zones[cameraID]
	if !ok || len(zones) == 0 {
		return nil
	}

	result := make([]ROI, len(zones))
	copy(result, zones)
	return result
}

// GetAllZones returns a copy of all zones across all cameras.
func (zm *ZoneManager) GetAllZones() map[string][]ROI {
	zm.mu.RLock()
	defer zm.mu.RUnlock()

	result := make(map[string][]ROI, len(zm.zones))
	for cameraID, zones := range zm.zones {
		if len(zones) > 0 {
			copied := make([]ROI, len(zones))
			copy(copied, zones)
			result[cameraID] = copied
		}
	}
	return result
}

// GetEnabledZones returns a copy of only enabled zones (based on ROIZone matching)
// for a given camera. Note: this method requires the caller to provide enabled zones
// as ROIZone objects since the ZoneManager only stores ROI (without the Enabled flag).
// This is a convenience method that filters zones by the given enabled zone names.
//
// If enabledNames is empty, all zones are returned (no filtering).
func (zm *ZoneManager) GetEnabledZones(cameraID string, enabledNames []string) []ROI {
	zm.mu.RLock()
	defer zm.mu.RUnlock()

	zones, ok := zm.zones[cameraID]
	if !ok || len(zones) == 0 {
		return nil
	}

	if len(enabledNames) == 0 {
		// No explicit filter — return all zones.
		result := make([]ROI, len(zones))
		copy(result, zones)
		return result
	}

	enabledSet := make(map[string]struct{}, len(enabledNames))
	for _, name := range enabledNames {
		enabledSet[name] = struct{}{}
	}

	var result []ROI
	for _, z := range zones {
		if _, ok := enabledSet[z.Name]; ok {
			result = append(result, z)
		}
	}
	return result
}

// HasZones returns true if the camera has any zones defined.
func (zm *ZoneManager) HasZones(cameraID string) bool {
	zm.mu.RLock()
	defer zm.mu.RUnlock()

	zones, ok := zm.zones[cameraID]
	return ok && len(zones) > 0
}

// PointInPolygon determines if a point (x, y) is inside a polygon defined by
// the given vertices. Uses the ray-casting algorithm: cast a horizontal ray
// to the right and count edge crossings. An odd number of crossings means the
// point is inside.
//
// Points are expected to be in normalized coordinates [0, 1].
// Returns false for polygons with fewer than 3 vertices.
func PointInPolygon(x, y float64, polygon [][2]float64) bool {
	if len(polygon) < 3 {
		return false
	}

	inside := false
	n := len(polygon)
	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := polygon[i][0], polygon[i][1]
		xj, yj := polygon[j][0], polygon[j][1]

		if ((yi > y) != (yj > y)) && (x < (xj-xi)*(y-yi)/(yj-yi)+xi) {
			inside = !inside
		}
		j = i
	}

	return inside
}

// FilterDetectionsByZone filters detections to only include those whose
// bounding box center falls within at least one of the given zones.
// The bounding box center is computed as the midpoint of the normalized
// coordinates: ((x1+x2)/2, (y1+y2)/2).
//
// If zones is empty, all detections are returned unchanged (no filtering).
func FilterDetectionsByZone(detections []Detection, zones []ROI) []Detection {
	if len(zones) == 0 {
		return detections
	}

	filtered := make([]Detection, 0, len(detections))
	for _, det := range detections {
		cx := (det.BBox[0] + det.BBox[2]) / 2
		cy := (det.BBox[1] + det.BBox[3]) / 2

		for _, zone := range zones {
			if PointInPolygon(cx, cy, zone.Points) {
				filtered = append(filtered, det)
				break // matched at least one zone
			}
		}
	}

	return filtered
}

// validateZone checks that a ROIZone has valid fields.
func validateZone(zone ROIZone) error {
	if zone.CameraID == "" {
		return fmt.Errorf("ai: camera ID must not be empty")
	}
	if zone.Zone.Name == "" {
		return fmt.Errorf("ai: zone name must not be empty")
	}
	if len(zone.Zone.Points) < 3 {
		return fmt.Errorf("ai: zone %q must have at least 3 points, got %d", zone.Zone.Name, len(zone.Zone.Points))
	}

	for i, p := range zone.Zone.Points {
		if p[0] < 0 || p[0] > 1 || p[1] < 0 || p[1] > 1 {
			return fmt.Errorf("ai: zone %q point %d coordinates (%f, %f) outside [0, 1] range",
				zone.Zone.Name, i, p[0], p[1])
		}
	}

	return nil
}
