package relay

import (
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
)

// --- Audio helpers ---

// parseASC unmarshals an AudioSpecificConfig from raw bytes.
// Returns nil if config is empty or unparsable.
func parseASC(config []byte) *mpeg4audio.AudioSpecificConfig {
	if len(config) == 0 {
		return nil
	}
	asc := &mpeg4audio.AudioSpecificConfig{}
	if err := asc.Unmarshal(config); err != nil {
		engineLogger.Warn("failed to unmarshal AudioSpecificConfig", "error", err)
		return nil
	}
	return asc
}

// parseG711Config parses the G.711 audio config bytes stored by the recorder.
// Format: [muLawFlag (1 byte), sampleRate (4 bytes big-endian)].
func parseG711Config(config []byte) (isMULaw bool, sampleRate int) {
	if len(config) < 5 {
		return false, 8000
	}
	isMULaw = config[0] != 0
	sampleRate = int(config[1])<<24 | int(config[2])<<16 | int(config[3])<<8 | int(config[4])
	if sampleRate <= 0 {
		sampleRate = 8000
	}
	return
}

// durationFromPTS converts a 90kHz PTS value to a time.Duration.
// Example: pts=45000 -> 500ms (45000/90000 = 0.5s).
func durationFromPTS(pts int64) time.Duration {
	if pts < 0 {
		return 0
	}
	return time.Duration(pts) * time.Second / 90000
}

// Sentinel errors.
var (
	errPermanent = errPermanentDef{}
)

type errPermanentDef struct{}

func (errPermanentDef) Error() string { return "permanent relay error (no retry)" }
