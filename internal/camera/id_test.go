package camera

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestGenerateCameraID_Format(t *testing.T) {
	id := GenerateCameraID()
	
	// Check that ID starts with "cam-" prefix
	assert.True(t, len(id) > 4, "ID should be longer than 4 characters")
	assert.True(t, id[:4] == "cam-", "ID should start with 'cam-' prefix")
	
	// Check that the UUID part is valid (cam- + UUID format)
	uuidPart := id[4:]
	assert.Equal(t, 36, len(uuidPart), "UUID part should be 36 characters")
	
	// Verify the UUID format using uuid.Parse
	parsedUUID, err := uuid.Parse(uuidPart)
	assert.NoError(t, err, "UUID should be valid")
	assert.NotEqual(t, uuid.Nil, parsedUUID, "UUID should not be nil")
	
	// Check total length: "cam-" (4) + UUID (36) = 40
	assert.Equal(t, 40, len(id), "ID should be exactly 40 characters long")
}

func TestGenerateCameraID_Unique(t *testing.T) {
	ids := make(map[string]bool)
	
	// Generate 100 IDs and check for uniqueness
	for i := 0; i < 100; i++ {
		id := GenerateCameraID()
		ids[id] = true
	}
	
	assert.Equal(t, 100, len(ids), "All 100 generated IDs should be unique")
}