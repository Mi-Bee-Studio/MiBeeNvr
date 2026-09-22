package model

// ROI defines a region of interest as a closed polygon. Points are specified in
// normalized coordinates (each coordinate in [0, 1]) relative to the frame
// dimensions. The polygon is implicitly closed (last point connects to first).
type ROI struct {
	Name   string       `json:"name"`   // human-readable name for the region (e.g. "driveway", "front-door")
	Points [][2]float64 `json:"points"` // polygon vertices in normalized coordinates
}

// ROIZone binds an ROI to a specific camera and controls whether AI detection
// should be filtered to only include objects within this zone.
type ROIZone struct {
	CameraID string `json:"camera_id"` // kebab-case camera identifier
	Zone     ROI    `json:"zone"`      // the region of interest definition
	Enabled  bool   `json:"enabled"`   // whether this zone filter is active
}
