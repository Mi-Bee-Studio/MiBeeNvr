package onvif

import "strings"

// SelectMainProfile picks the best media profile for recording from the list
// returned by GetProfiles.
//
// ONVIF cameras commonly expose multiple profiles (e.g. a high-resolution
// "main" stream and a lower-resolution "sub" stream). The previous code took
// profiles[0] blindly, which silently recorded from the sub-stream on cameras
// whose first profile happened to be the low-res one — recording "worked" but
// at the wrong resolution.
//
// The reliable ranking signal is the VideoEncoderConfiguration resolution
// (captured in DeviceProfile.Width/Height), NOT the profile order or token
// naming. The Amcrest IP4M-1051 famously returns the same stream URL for both
// profiles via ONVIF, and naming is unreliable across OEM firmware, so the
// token name is only a last-resort tiebreaker.
//
// Ranking:
//  1. Highest pixel count (Width × Height) wins — the main/recording stream.
//  2. On a tie, prefer a token/name containing main/primary cues.
//  3. Otherwise keep list order (stable).
//
// Returns "" when the list is empty.
func SelectMainProfile(profiles []DeviceProfile) string {
	if len(profiles) == 0 {
		return ""
	}
	best := 0
	bestPixels := profilePixels(profiles[0])
	for i := 1; i < len(profiles); i++ {
		pixels := profilePixels(profiles[i])
		if pixels > bestPixels {
			best = i
			bestPixels = pixels
		} else if pixels == bestPixels {
			// Tiebreak: prefer a token/name that looks like a main/primary stream.
			if looksLikeMain(profiles[i].Token, profiles[i].Name) && !looksLikeMain(profiles[best].Token, profiles[best].Name) {
				best = i
				bestPixels = pixels
			}
		}
	}
	return profiles[best].Token
}

// profilePixels returns Width × Height (0 when either is unset).
func profilePixels(p DeviceProfile) int {
	if p.Width <= 0 || p.Height <= 0 {
		return 0
	}
	return p.Width * p.Height
}

// looksLikeMain reports whether the token/name suggests this is the primary
// (main) stream rather than a sub/secondary stream. Case-insensitive; checks
// common English and Chinese cues.
func looksLikeMain(token, name string) bool {
	s := strings.ToLower(token + " " + name)
	for _, cue := range []string{"main", "primary", "主流", "主码流", "channel1", "channels/1"} {
		if strings.Contains(s, cue) {
			return true
		}
	}
	// Sub-stream cues actively disqualify.
	for _, cue := range []string{"sub", "secondary", "辅流", "辅码流", "extra"} {
		if strings.Contains(s, cue) {
			return false
		}
	}
	return false
}
