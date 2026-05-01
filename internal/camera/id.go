package camera

import (
	"github.com/google/uuid"
)

// GenerateCameraID generates a unique camera ID with "cam-" prefix followed by UUID.
// Returns a string in format "cam-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx".
func GenerateCameraID() string {
	return "cam-" + uuid.New().String()
}