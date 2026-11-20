package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Pre-compiled regex patterns for validation (avoids SA6000: regexp.MatchString in loop).
// Used by camera push-target and transcoding validation below.
var (
	rePlatformName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	reResolution   = regexp.MustCompile(`^\d+x\d+$`)
	reBitrate      = regexp.MustCompile(`^(0|\d+(\.\d+)?[kMG])$`)
	// reStableID matches the character class allowed for a camera's stable_id
	// (the hardware-level identity used by IP self-healing / rediscovery):
	// alphanumerics plus ':', '_', '-'. Length 3–64. This deliberately rejects
	// values that cannot be a hardware serial: IPs (contain '.'), URLs (contain
	// '/'), all-zero / all-same-char strings (firmware glitch returns — see
	// upstream seeed-esp32s3-cam issue #2). See #216.
	reStableID = regexp.MustCompile(`^[A-Za-z0-9:_-]{3,64}$`)
)

// IsValidStableID reports whether s is a plausible hardware-identity value
// suitable for persisting as a camera's StableID. It accepts real-world serial
// formats seen on real hardware: 12-hex MAC, uppercase efuse MAC,
// colon-separated MAC, and
// vendor serials with `-`/`_` separators (`SN-AAA`, `XIAOMI-CAM-001`). It rejects
// the dirty values that have caused permanent rediscovery failure in production
// (see #216): IP addresses, URLs, all-zero, all-same-char, too short/long.
//
// Used to gate both write paths (ONVIF reverse lookup, API add/update, config
// validation) and the "already set, skip" guards so a once-persisted dirty value
// gets overwritten on the next successful ONVIF lookup instead of being frozen.
func IsValidStableID(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || !reStableID.MatchString(s) {
		return false
	}
	// Reject all-zero / all-same-character runs. Compare on the alphanumerics
	// only (skip ':'/'_'/'-' separators) so a colon-separated all-zero MAC like
	// "00:00:00:00:00:00" is also caught — its alphanumerics are all '0'.
	var first byte
	for i := range len(s) {
		c := s[i]
		if c == ':' || c == '_' || c == '-' {
			continue
		}
		if first == 0 {
			first = c
		} else if c != first {
			return true
		}
	}
	return false
}

// validatePortRange checks a "start-end" port range string (e.g. "30000-30050").
// Both endpoints must be valid TCP/UDP port numbers and start must be <= end.
func validatePortRange(r string) error {
	parts := strings.SplitN(r, "-", 2)
	if len(parts) != 2 {
		return fmt.Errorf("must be in format start-end (e.g. \"30000-30050\")")
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return fmt.Errorf("invalid start port %q: %w", parts[0], err)
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return fmt.Errorf("invalid end port %q: %w", parts[1], err)
	}
	if start < 1 || start > 65535 || end < 1 || end > 65535 {
		return fmt.Errorf("ports must be between 1 and 65535, got %d-%d", start, end)
	}
	if start > end {
		return fmt.Errorf("start port %d must be <= end port %d", start, end)
	}
	return nil
}

// MotionSourceCameraONVIF selects the camera-side ONVIF Pull-Point
// MotionAlarm subscription as this camera's motion signal (#711).
const MotionSourceCameraONVIF = "camera:onvif"

// ImagingTypeThermal marks a camera entry as the thermal channel of a
// dual-spectrum device. "" and "visible" mean visible light.
const ImagingTypeThermal = "thermal"
