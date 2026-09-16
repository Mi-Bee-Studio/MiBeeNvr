package transcoding

import (
	"log/slog"
	"sync"
)

// softwareEncoderWarned mutes the static "software encoding on ARM is slow"
// warnings after their first emission per key per process. The condition
// derives from startup-probed capabilities and never changes mid-run, but
// the warns fired per task and flooded production journals (~2.9k
// lines/day on the M5, whose Amlogic SoC has no mainline v4l2m2m encoder —
// verified 2026-09-16: ffmpeg h264_v4l2m2m fails with -22). Distinct
// encoder/arch variants each keep their one emission.
var softwareEncoderWarned sync.Map

// warnSoftwareEncoderOnce emits msg at Warn level the first time it is
// called with a given key; subsequent calls with the same key are silent.
func warnSoftwareEncoderOnce(key, msg string, args ...any) {
	if _, loaded := softwareEncoderWarned.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	slog.Warn(msg, args...)
}

// resetSoftwareEncoderWarnOnce restores first-emission behavior. Tests only.
func resetSoftwareEncoderWarnOnce() {
	softwareEncoderWarned = sync.Map{}
}
