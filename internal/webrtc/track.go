package webrtc

import "strings"

// annexBStartCode is the H.264 Annex B start code (4-byte variant).
var annexBStartCode = []byte{0x00, 0x00, 0x00, 0x01}

// annexBEncode converts an access unit ([][]byte of NAL units without start codes)
// to an Annex B byte stream with start codes prepended to each NAL unit.
func annexBEncode(au [][]byte) []byte {
	if len(au) == 0 {
		return nil
	}
	totalSize := 0
	for _, nal := range au {
		totalSize += len(annexBStartCode) + len(nal)
	}

	buf := make([]byte, 0, totalSize)
	for _, nal := range au {
		buf = append(buf, annexBStartCode...)
		buf = append(buf, nal...)
	}
	return buf
}

// rejectAudioInSDP rejects all audio m-lines in the SDP answer by setting
// their port to 0. Applied only for video-only peers (#372): pion answers an
// offered audio m-line with the engine's codecs (port 9) even when no audio
// track was added — zeroing the port gives browsers an unambiguous rejection.
func rejectAudioInSDP(sdp string) string {
	lines := strings.Split(sdp, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "m=audio ") {
			// Replace port number with 0: "m=audio 9 ..." → "m=audio 0 ..."
			parts := strings.SplitN(line, " ", 3)
			if len(parts) >= 3 {
				lines[i] = "m=audio 0 " + parts[2]
			}
		}
	}
	return strings.Join(lines, "\n")
}
