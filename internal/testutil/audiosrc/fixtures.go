package audiosrc

import (
	_ "embed"
	"strings"
)

// Fixtures are embedded so tests can use them from any working directory.
//
//go:embed testdata/aac.adts
var aacFixture []byte

//go:embed testdata/opus.ogg
var opusFixture []byte

// loadADTS extracts raw AAC access units from an ADTS file (fixture). Frame
// boundaries come from the ADTS frame_length field; the 7-byte headers are
// stripped.
func loadADTS() ([][]byte, error) {
	var raw = aacFixture
	var aus [][]byte
	for i := 0; i+7 <= len(raw); {
		if raw[i] != 0xFF || raw[i+1]&0xF6 != 0xF0 {
			i++
			continue
		}
		n := int(raw[i+3]&0x03)<<11 | int(raw[i+4])<<3 | int(raw[i+5])>>5
		if i+n > len(raw) || n <= 7 {
			i++
			continue
		}
		aus = append(aus, raw[i+7:i+n])
		i += n
	}
	return aus, nil
}

// loadOggOpus extracts raw Opus packets from an Ogg Opus file (fixture).
// Packets terminate on lacing values < 255; the OpusHead/OpusTags header
// packets are skipped.
func loadOggOpus() ([][]byte, error) {
	var raw = opusFixture
	var pkts [][]byte
	var cur []byte
	for i := 0; i+27 <= len(raw); {
		if string(raw[i:i+4]) != "OggS" {
			i++
			continue
		}
		nSegs := int(raw[i+26])
		if i+27+nSegs > len(raw) {
			break
		}
		p := i + 27 + nSegs
		for s := 0; s < nSegs; s++ {
			sz := int(raw[i+27+s])
			if p+sz > len(raw) {
				return pkts, nil
			}
			cur = append(cur, raw[p:p+sz]...)
			p += sz
			if sz < 255 {
				if !strings.HasPrefix(string(cur), "OpusHead") && !strings.HasPrefix(string(cur), "OpusTags") {
					pkts = append(pkts, cur)
				}
				cur = nil
			}
		}
		i = p
	}
	return pkts, nil
}
