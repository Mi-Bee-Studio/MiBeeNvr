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

// SelectSubProfile picks the best secondary ("sub") profile for sub-stream
// consumers (#512): the highest-pixel profile OTHER than the main one whose
// resolution is strictly LOWER than the main profile's. A same-resolution
// second profile is the same stream under another token (the Amcrest
// IP4M-1051 pattern) — not a sub stream. Returns "" when the camera has no
// distinct secondary profile (single-profile devices stay empty; consumers
// treat that as "no sub-stream capability").
//
// On multi-source devices (dual-spectrum thermal + visible, multi-lens) the
// candidates are limited to the main profile's video source: a thermal
// channel's sub stream must never resolve to the visible lens's sub profile
// just because that one ranks higher (a Hikvision DS-2TB2617 with the thermal
// main at 1280×720 would otherwise pick the visible 704×576 sub).
func SelectSubProfile(profiles []DeviceProfile, mainToken string) string {
	mainPixels := 0
	mainSource := ""
	for _, p := range profiles {
		if p.Token == mainToken {
			mainPixels = profilePixels(p)
			mainSource = p.VideoSourceToken
			break
		}
	}
	if mainPixels <= 0 {
		return ""
	}
	best := -1
	bestPixels := 0
	for i, p := range profiles {
		if p.Token == mainToken {
			continue
		}
		if mainSource != "" && p.VideoSourceToken != "" && p.VideoSourceToken != mainSource {
			continue
		}
		pixels := profilePixels(p)
		if pixels <= 0 || pixels >= mainPixels {
			continue
		}
		if pixels > bestPixels {
			best, bestPixels = i, pixels
		} else if pixels == bestPixels && best >= 0 &&
			looksLikeSub(p.Token, p.Name) && !looksLikeSub(profiles[best].Token, profiles[best].Name) {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return profiles[best].Token
}

// looksLikeSub reports whether the token/name suggests a secondary stream.
// Case-insensitive English and Chinese cues, mirroring looksLikeMain.
func looksLikeSub(token, name string) bool {
	s := strings.ToLower(token + " " + name)
	for _, cue := range []string{"sub", "secondary", "辅流", "辅码流", "子码流", "extra"} {
		if strings.Contains(s, cue) {
			return true
		}
	}
	return false
}
