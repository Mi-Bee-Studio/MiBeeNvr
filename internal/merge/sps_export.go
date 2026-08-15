package merge

// SPSResolution parses pixel dimensions from raw SPS bytes. Exported for the
// VOD fragmenter (init-segment stsd/tkhd need width/height); codec is "h264"
// or "h265". Thin adapter over the unexported parsers per the repo's adapter
// convention — existing signatures stay untouched.
func SPSResolution(codec string, sps []byte) (width, height int, err error) {
	if codec == "h265" {
		return parseHEVCSPSResolution(sps)
	}
	return parseSPSResolution(sps)
}
