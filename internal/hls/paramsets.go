package hls

// Codec parameter-set handling for the HLS muxer: extracting VPS/SPS/PPS out
// of an access unit, caching them per stream, and injecting them ahead of a
// keyframe that lacks in-band parameters (SDP-only cameras). Split out of
// manager.go.

import "github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"

// extractParamSets scans a H264/H265 access unit for its parameter set NALs
// (SPS/PPS for H264, VPS/SPS/PPS for H265). Returns ok=false if any required
// parameter is missing. Most cameras emit these immediately before each IDR.
func extractParamSets(au [][]byte, isH265 bool) (vps, sps, pps []byte, ok bool) {
	for _, nal := range au {
		if len(nal) == 0 {
			continue
		}
		if isH265 {
			// HEVC NAL type is the low 6 bits of (first byte >> 1).
			nalType := (nal[0] >> 1) & 0x3F
			switch nalType {
			case 32:
				vps = nal // VPS_NUT
			case 33:
				sps = nal // SPS_NUT
			case 34:
				pps = nal // PPS_NUT
			}
		} else {
			// AVC NAL type is the low 5 bits of the first byte.
			nalType := nal[0] & 0x1F
			switch nalType {
			case 7:
				sps = nal // SPS
			case 8:
				pps = nal // PPS
			}
		}
	}
	if isH265 {
		return vps, sps, pps, vps != nil && sps != nil && pps != nil
	}
	return nil, sps, pps, sps != nil && pps != nil
}

// rebuildMuxer recreates the HLS muxer for a stream whose previous muxer was
// destroyed by handleWriteError after a transient write failure (e.g. DTS
// non-monotonic from RTP packet loss). Must be called from writeLoop only
// (which owns entry.mux writes). The current frame must be an IDR carrying
// fresh parameter sets. Returns true on success.
func (m *Manager) rebuildMuxer(cameraID string, entry *streamEntry, au [][]byte) bool {
	vps, sps, pps, ok := extractParamSets(au, entry.isH265)
	if !ok {
		// SDP-only cameras never carry parameter sets in-band (#772) —
		// writeLoop normally injects the cache before we get here; the
		// fallback covers any path that reaches rebuild without injection.
		vps, sps, pps = entry.vps, entry.sps, entry.pps
		ok = nalutil.HasCompleteParamSets([][]byte{vps, sps, pps}, entry.isH265)
	}
	if !ok {
		hlsLogger.Warn("HLS rebuild: IDR frame missing parameter sets, waiting for next IDR",
			"camera_id", cameraID, "consecutive_errors", entry.consecutiveErrors)
		return false
	}
	mux, track, err := m.createMuxAndTrack(entry.isH265, sps, pps, vps, entry.dirPath)
	if err != nil {
		hlsLogger.Warn("HLS rebuild: failed to create muxer",
			"camera_id", cameraID, "error", err, "consecutive_errors", entry.consecutiveErrors)
		return false
	}
	entry.mu.Lock()
	entry.mux = mux
	entry.track = track
	entry.mu.Unlock()
	// NOTE: consecutiveErrors/backoff are NOT reset here — muxer creation can
	// succeed on a read-only filesystem (the file write is deferred). They are
	// only cleared on a successful frame write in writeLoop, proving the path
	// is truly writable.
	if m.metrics != nil {
		m.metrics.HLSMuxerRestarts.WithLabelValues(cameraID).Inc()
	}
	hlsLogger.Info("HLS muxer rebuilt after transient failure", "camera_id", cameraID)
	return true
}

// refreshParamCache updates the entry's cached parameter sets from in-band
// NALs (SPS/PPS for H264, VPS/SPS/PPS for H265). In-band bytes are fresher
// than the startStream seed — a mid-stream codec change lands here first and
// the next injected IDR / muxer rebuild uses the new values. Called from
// writeLoop only (cache owner); the field writes hold entry.mu because
// Handle's gap-placeholder path reads the cache from HTTP goroutines.
func refreshParamCache(entry *streamEntry, au [][]byte) {
	if entry.isH265 {
		vps, sps, pps := nalutil.ExtractParamSetsH265(au)
		entry.mu.Lock()
		if vps != nil {
			entry.vps = vps
		}
		if sps != nil {
			entry.sps = sps
		}
		if pps != nil {
			entry.pps = pps
		}
		entry.mu.Unlock()
		return
	}
	sps, pps := nalutil.ExtractParamSetsH264(au)
	entry.mu.Lock()
	if sps != nil {
		entry.sps = sps
	}
	if pps != nil {
		entry.pps = pps
	}
	entry.mu.Unlock()
}

// withCachedParamSets returns au with any missing parameter-set NALs prepended
// from the entry cache so IDR access units are self-contained. Cameras that
// signal parameters only via SDP sprop-parameter-sets (out-of-band, RFC 6184)
// otherwise break the HLS muxer on every keyframe: gohlslib's DTS extractors
// need the SPS inside the AU ("unable to extract DTS"), and the muxer dies into
// the rebuild loop (#772). Same rationale as the RTSP egress writeParamSets
// (internal/rtsp/server.go). The returned slice is freshly allocated — the
// hub-shared backing array of the incoming AU is never mutated.
func withCachedParamSets(au [][]byte, entry *streamEntry) [][]byte {
	var haveVPS, haveSPS, havePPS bool
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		if entry.isH265 {
			switch (nalu[0] >> 1) & 0x3F {
			case 32:
				haveVPS = true
			case 33:
				haveSPS = true
			case 34:
				havePPS = true
			}
		} else {
			switch nalu[0] & 0x1F {
			case 7:
				haveSPS = true
			case 8:
				havePPS = true
			}
		}
	}
	pre := make([][]byte, 0, 3)
	if entry.isH265 && !haveVPS && entry.vps != nil {
		pre = append(pre, entry.vps)
	}
	if !haveSPS && entry.sps != nil {
		pre = append(pre, entry.sps)
	}
	if !havePPS && entry.pps != nil {
		pre = append(pre, entry.pps)
	}
	if len(pre) == 0 {
		return au
	}
	out := make([][]byte, 0, len(pre)+len(au))
	out = append(out, pre...)
	return append(out, au...)
}
