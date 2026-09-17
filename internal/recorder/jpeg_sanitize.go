package recorder

import "bytes"

// ExtractCompleteJPEGs salvages the complete JPEG images contained in a byte
// buffer that may carry leading/trailing garbage images — specifically the
// double-header buffers produced when an RTSP sender packs COMPLETE JPEGs
// into RFC 2435 RTP-JPEG payloads: the depacketizer prepends its own
// synthesized SOI/DQT/SOF/DHT/SOS header block (no entropy, no EOI), and
// occasionally two camera frames are glued into one buffer. Strict browser
// decoders (createImageBitmap, Chromium image decode) reject such buffers
// outright, which turned timelapse playback into an infinite spinner.
//
// A complete image runs from an SOI (FFD8) up to the next SOI or the end of
// the buffer, and must end with an EOI (FFD9) modulo trailing zero padding.
// Compliant JPEGs never contain a raw FFD8 inside entropy data (0xFF is
// always byte-stuffed), so SOI positions are reliable image boundaries. A
// buffer holding a single clean JPEG yields exactly that JPEG, unchanged.
func ExtractCompleteJPEGs(data []byte) [][]byte {
	soi := []byte{0xFF, 0xD8}
	eoi := []byte{0xFF, 0xD9}
	var images [][]byte
	pos := 0
	for pos < len(data) {
		rel := bytes.Index(data[pos:], soi)
		if rel < 0 {
			break
		}
		start := pos + rel
		next := bytes.Index(data[start+2:], soi)
		end := len(data)
		if next >= 0 {
			end = start + 2 + next
		}
		img := data[start:end]
		trimmed := trimTrailingZeros(img)
		if len(trimmed) >= 2 && bytes.HasSuffix(trimmed, eoi) {
			images = append(images, trimmed)
		}
		pos = end
	}
	return images
}

// trimTrailingZeros drops trailing 0x00 padding some container paths append
// after the EOI marker (e.g. AVI even-chunk alignment).
func trimTrailingZeros(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0x00 {
		b = b[:len(b)-1]
	}
	return b
}
