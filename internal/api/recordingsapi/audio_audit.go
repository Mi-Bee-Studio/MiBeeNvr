package recordingsapi

import (
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/audioaudit"
)

// handleAudioAudit returns the recent audio-access audit trail, newest first.
// The ring lives in internal/audioaudit (shared with the live-listening
// append path).
func (s *server) handleAudioAudit(w http.ResponseWriter, r *http.Request) {
	const limit = 200
	writeJSON(w, http.StatusOK, map[string]any{"entries": audioaudit.Recent(limit)})
}
