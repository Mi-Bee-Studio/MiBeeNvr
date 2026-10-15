package doorbell

import (
	"fmt"
	"strings"
)

// audioOffer is the audio m-line extracted from a doorbell's INVITE offer.
type audioOffer struct {
	Address string // c= connection address
	Port    int
	PCMA    bool // payload type 8 offered
	PCMU    bool // payload type 0 offered
}

// payloadType picks the G.711 variant to answer with (A-law preferred —
// the talk bridge and the web UI's encoder both speak A-law natively).
func (o audioOffer) payloadType() (pt uint8, name string, ok bool) {
	switch {
	case o.PCMA:
		return 8, "PCMA", true
	case o.PCMU:
		return 0, "PCMU", true
	default:
		return 0, "", false
	}
}

// parseAudioOffer extracts the first audio m-line. Deliberately line-based:
// SDP from intercom firmware is the wild west, and all we need is the
// connection address, port and the offered payload types.
func parseAudioOffer(sdp string) (audioOffer, bool) {
	var offer audioOffer
	connAddr := ""
	inAudio := false
	portSet := false
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "m="):
			inAudio = strings.HasPrefix(line, "m=audio ")
			if inAudio {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					var port int
					if _, err := fmt.Sscanf(fields[1], "%d", &port); err == nil {
						offer.Port = port
						portSet = true
					}
					for _, pt := range fields[3:] {
						switch strings.TrimSpace(pt) {
						case "8":
							offer.PCMA = true
						case "0":
							offer.PCMU = true
						}
					}
				}
			}
		case strings.HasPrefix(line, "c=IN IP4 "):
			addr := strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4 "))
			if inAudio || connAddr == "" {
				connAddr = addr
			}
		}
	}
	offer.Address = strings.Trim(connAddr, ".")
	if !portSet || offer.Port <= 0 || offer.Address == "" || (!offer.PCMA && !offer.PCMU) {
		return audioOffer{}, false
	}
	return offer, true
}

// buildAudioAnswer renders the SDP answer: our RTP endpoint, the chosen
// G.711 payload type, sendrecv two-way audio.
func buildAudioAnswer(addr string, port int, pt uint8, codecName string) string {
	return "v=0\r\n" +
		"o=- 0 0 IN IP4 " + addr + "\r\n" +
		"s=MiBee NVR Doorbell\r\n" +
		"c=IN IP4 " + addr + "\r\n" +
		"t=0 0\r\n" +
		fmt.Sprintf("m=audio %d RTP/AVP %d\r\n", port, pt) +
		fmt.Sprintf("a=rtpmap:%d %s/8000\r\n", pt, codecName) +
		"a=sendrecv\r\n"
}
