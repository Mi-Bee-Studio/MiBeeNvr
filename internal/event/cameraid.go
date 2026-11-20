package event

import "encoding/json"

// cameraIDFromEventData attempts to extract a camera ID from event data.
// Uses fast type assertion for known event types (zero allocation). Falls
// back to one JSON serialize+parse pass for unknown types, whose serialized
// bytes are returned so the caller can compose the SSE frame without a
// second reflection round-trip (#875 M4).
func CameraIDFromEventData(data interface{}) (id string, marshaled []byte) {
	// Fast path: type-assert known event structs (zero allocation).
	switch d := data.(type) {
	case SegmentCompleted:
		return d.CameraID, nil
	case SegmentDeleted:
		return d.CameraID, nil
	case StorageHealthChanged:
		return d.CameraID, nil
	case AIDetectionEvent:
		return d.CameraID, nil
	case CameraSnapshotEvent:
		return d.CameraID, nil
	case GB28181AlarmEvent:
		return d.CameraID, nil
	case map[string]interface{}:
		for _, key := range []string{"camera_id", "CameraID", "camera", "Camera"} {
			if id, ok := d[key].(string); ok && id != "" {
				return id, nil
			}
		}
		return "", nil
	}
	// Fallback: one JSON round-trip for ad-hoc types.
	b, err := json.Marshal(data)
	if err != nil {
		return "", nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return "", nil
	}
	for _, key := range []string{"camera_id", "CameraID", "camera", "Camera"} {
		if id, ok := m[key].(string); ok && id != "" {
			return id, b
		}
	}
	return "", nil
}
