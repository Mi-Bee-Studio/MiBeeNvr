package config

import "time"

// Single source for defaults that several packages also reference inline
// (CLI fallbacks, installers, inline resolution for hand-built configs).
// Duplicated literals at those sites are how the double-write drift in #876
// happened.
const (
	// DefaultDataDir is the bare-metal default storage root. main.go compares
	// against it to auto-fix Docker defaults — keep every reference here.
	DefaultDataDir    = "/var/lib/mibee-nvr"
	DefaultListenAddr = ":9090"
	// DefaultRTSPTimeout bounds RTSP reads/writes in the recorders and the
	// relay (IDR intervals longer than this trip reconnects — make it
	// config-driven if such cameras show up).
	DefaultRTSPTimeout = 10 * time.Second

	// Rolling-merge fallbacks; ApplyDefaults materializes the same values.
	MergeDefaultDebounce       = 5 * time.Second
	MergeDefaultWindow         = time.Hour
	MergeDefaultTranscodeGrace = "90s"
	MergeDefaultBucketRetain   = 2
)
