package xiaomi

// Packet processing for the Xiaomi recorder: NALU dispatch, the H.264/H.265
// in-band parameter handling, HLS forwarding and the audio track path.
// Split out of recorder.go — the reconnect loop and segment lifecycle stay
// there.

import (
	"bytes"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

// processNALU handles a single NALU extracted from Annex B payload. at is the
// packet's arrival time (captured at ReadPacket), shared by every NALU of the
// same packet.
func (r *XiaomiRecorder) processNALU(nalu []byte, at time.Time) {
	if len(nalu) == 0 {
		return
	}

	switch r.codec {
	case model.FormatH264:
		r.processH264NALU(nalu, at)
	case model.FormatH265:
		r.processH265NALU(nalu, at)
	}
}

// recordDisabled reports whether this recorder is in live-only mode (no disk
// writes). Mirrors baseRecorder.RecordEnabled semantics: nil/true = record,
// false = live-only.
func (r *XiaomiRecorder) recordDisabled() bool {
	return r.cfg.RecordEnabled != nil && !*r.cfg.RecordEnabled
}

// processH264NALU handles an H.264 NAL unit. at is the packet arrival time
// (see the read loop's capture point).
func (r *XiaomiRecorder) processH264NALU(nalu []byte, at time.Time) {
	naluType := nalu[0] & 0x1F
	switch naluType {
	case 7: // SPS
		// r.muxer==nil guard: no open segment → just refresh the cache without
		// consuming rotation budget (e.g. param sync right after connect).
		if r.sps != nil && !bytes.Equal(r.sps, nalu) && r.muxer != nil {
			// #642: decode-equivalent SPS variants (VUI/timing-only differences)
			// never rotate; a real codec change rotates immediately.
			if r.paramGate.ShouldRotateSPS(r.sps, nalu, false, time.Now()) {
				xiaomiLogger.Info("SPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegment()
			} else {
				xiaomiLogger.Debug("SPS variant is decode-compatible, keeping segment open",
					"camera_id", r.cfg.CameraID)
			}
		}
		r.sps = append([]byte(nil), nalu...)
		r.publishCodecSnap()
		return
	case 8: // PPS
		if r.pps != nil && !bytes.Equal(r.pps, nalu) && r.muxer != nil {
			// No semantic PPS parser — rate-limit rapid variant alternation (#642).
			if r.paramGate.ShouldRotateUnparsed(time.Now()) {
				xiaomiLogger.Info("PPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegment()
			}
		}
		r.pps = append([]byte(nil), nalu...)
		r.publishCodecSnap()
		return
	}

	// Only write video frames (IDR=5, non-IDR=1).
	if naluType != 5 && naluType != 1 {
		return
	}
	if r.sps == nil || r.pps == nil {
		return
	}

	// Live-only mode: keep the stream alive for live preview (forwardHLS) but
	// skip all disk writes (segment creation + WriteSample + rotation).
	if r.recordDisabled() {
		r.forwardHLS(nalu, at)
		return
	}

	// Wait for IDR frame before starting a new segment.
	if r.muxer == nil && naluType != 5 {
		return
	}

	// Adaptive write-density gate (issue #435/#468) — see processH265NALU.
	if r.adaptive != nil {
		isIDR := naluType == 5
		_, skip, flush := r.adaptive.Observe(nalu, isIDR, at)
		// Ambient-audio cameras keep the disk audio track through sparse mode
		// (#496); the merge renders the ambient span into the atmosphere bed.
		r.audioSparse.Store(r.adaptive.Timelapse() && !r.cfg.Adaptive.AmbientAudio)
		if len(flush) > 0 {
			r.writeFlushedGOP(flush)
			// Pre-trigger audio back-fill (issue #478), mirroring the built-in
			// recorders: write the retained unwritten ring samples so the
			// segment carries pre_capture seconds of sound before the trigger.
			r.flushAudioRing()
		}
		if skip {
			r.forwardHLS(nalu, at)
			return
		}
	}

	// #953 对齐：时长轮转只发生在 IDR 上——本帧 IDR 落进新开段，段边
	// 界两侧零丢弃（此前在写后任意帧上关段，新段等下一个 IDR，每个边
	// 界丢最多一个 GOP 的画面）。
	if r.muxer != nil {
		elapsed := at.Sub(r.segStart)
		if naluType == 5 && elapsed >= r.cfg.SegmentDur {
			r.closeCurrentSegment()
			// fall through：下方开段块以本 IDR 开新段。
		} else if elapsed >= 2*r.cfg.SegmentDur {
			// 无 IDR 流的段长兜底：关段后本帧交回等-IDR 门（直播转发
			// 保留，仅磁盘写停到下个 IDR）。
			r.closeCurrentSegment()
			r.forwardHLS(nalu, at)
			return
		}
	}
	if r.muxer == nil {
		tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatH264))
		if err != nil {
			xiaomiLogger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		r.muxer = muxer.NewMP4Muxer(tempPath)
		// Preallocation from the previous segment's final size (#757 parity).
		if r.lastSegBytes > 0 {
			r.muxer.SetPreallocateBytes(r.lastSegBytes)
		}
		trackID, err := r.muxer.AddH264Track(r.sps, r.pps)
		if err != nil {
			xiaomiLogger.Error("failed to add H264 track", "camera_id", r.cfg.CameraID, "error", err)
			r.muxer = nil
			os.Remove(tempPath)
			return
		}
		r.trackID = trackID

		// Add audio track only when audio is enabled AND the camera keeps
		// audio in recordings (default off — #496 follow-up, issue #520).
		// Live audio and the audio trigger read the pre-disk path and do not
		// need the track.
		if r.cfg.AudioEnabled && r.cfg.AudioInRecordings && r.audioCodecID.Load() > 0 {
			codec, cfg, ok := buildAudioMuxerConfig(r.audioCodecID.Load())
			if ok {
				aID, err := r.muxer.AddAudioTrack(codec, cfg)
				if err != nil {
					xiaomiLogger.Debug("audio track not added to muxer", "camera_id", r.cfg.CameraID, "error", err)
				} else {
					r.audioTrackID = aID
				}
			}
		}
		r.curTempPath = tempPath
		r.curFinalPath = finalPath
		r.segStart = at
		r.lastFrameTime = at
		r.frameCount = 0
	}

	pts := at.Sub(r.segStart)
	duration := at.Sub(r.lastFrameTime)
	if duration < time.Millisecond {
		duration = time.Millisecond
	}
	r.lastFrameTime = at

	if err := r.muxer.WriteSample(r.trackID, nalu, pts, duration); err != nil {
		xiaomiLogger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	r.frameCount++
	if r.adaptive != nil {
		// Frame now on disk; a later flush into this segment must skip it (#473).
		r.adaptive.MarkLastWritten()
	}

	// Forward to HLS (non-blocking)
	r.forwardHLS(nalu, at)
}

// processH265NALU handles an H.265/HEVC NAL unit. at is the packet arrival
// time (see the read loop's capture point).
func (r *XiaomiRecorder) processH265NALU(nalu []byte, at time.Time) {
	// HEVC NALU type: 2-byte header, type is in bits 1-6 of first byte.
	naluType := (nalu[0] >> 1) & 0x3F
	switch naluType {
	case 32: // VPS
		if r.vps != nil && !bytes.Equal(r.vps, nalu) && r.muxer != nil {
			// No semantic VPS parser — rate-limit rapid variant alternation (#642).
			if r.paramGate.ShouldRotateUnparsed(time.Now()) {
				xiaomiLogger.Info("VPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegment()
			}
		}
		r.vps = append([]byte(nil), nalu...)
		r.publishCodecSnap()
		return
	case 33: // SPS
		if r.sps != nil && !bytes.Equal(r.sps, nalu) && r.muxer != nil {
			// #642: decode-equivalent SPS variants never rotate; a real codec
			// change rotates immediately.
			if r.paramGate.ShouldRotateSPS(r.sps, nalu, true, time.Now()) {
				xiaomiLogger.Info("SPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegment()
			} else {
				xiaomiLogger.Debug("SPS variant is decode-compatible, keeping segment open",
					"camera_id", r.cfg.CameraID)
			}
		}
		r.sps = append([]byte(nil), nalu...)
		r.publishCodecSnap()
		return
	case 34: // PPS
		if r.pps != nil && !bytes.Equal(r.pps, nalu) && r.muxer != nil {
			// No semantic PPS parser — rate-limit rapid variant alternation (#642).
			if r.paramGate.ShouldRotateUnparsed(time.Now()) {
				xiaomiLogger.Info("PPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegment()
			}
		}
		r.pps = append([]byte(nil), nalu...)
		r.publishCodecSnap()
		return
	}

	// Only write VCL NALUs (types 0-31). Non-VCL types are 32+.
	if naluType >= 32 {
		return
	}
	if r.vps == nil || r.sps == nil || r.pps == nil {
		return
	}

	// Live-only mode: keep the stream alive for live preview (forwardHLS) but
	// skip all disk writes (segment creation + WriteSample + rotation).
	if r.recordDisabled() {
		r.forwardHLS(nalu, at)
		return
	}

	// Wait for IDR frame (types 19=IDR_W_RADL, 20=IDR_N_LP).
	if r.muxer == nil && naluType != 19 && naluType != 20 {
		return
	}

	// Adaptive write-density gate (issue #435/#468): while the compressed-domain
	// activity signal is calm only sparse keyframes reach the disk; on a spike
	// the retained GOP ring is flushed first so the resume has no missing
	// references. Live fan-out (forwardHLS → StreamHub) keeps flowing for every
	// frame — the gate changes write density, never the connection.
	if r.adaptive != nil {
		isIDR := naluType == 19 || naluType == 20
		_, skip, flush := r.adaptive.Observe(nalu, isIDR, at)
		// Ambient-audio cameras keep the disk audio track through sparse mode
		// (#496); the merge renders the ambient span into the atmosphere bed.
		r.audioSparse.Store(r.adaptive.Timelapse() && !r.cfg.Adaptive.AmbientAudio)
		if len(flush) > 0 {
			r.writeFlushedGOP(flush)
			// Pre-trigger audio back-fill (issue #478), mirroring the built-in
			// recorders: write the retained unwritten ring samples so the
			// segment carries pre_capture seconds of sound before the trigger.
			r.flushAudioRing()
		}
		if skip {
			r.forwardHLS(nalu, at)
			return
		}
	}

	// #953 对齐（同 processH264NALU）：时长轮转只发生在 IDR 上；
	// 2×SegmentDur 兜底关段后交回等-IDR 门（直播转发保留）。
	if r.muxer != nil {
		elapsed := at.Sub(r.segStart)
		if (naluType == 19 || naluType == 20) && elapsed >= r.cfg.SegmentDur {
			r.closeCurrentSegment()
		} else if elapsed >= 2*r.cfg.SegmentDur {
			r.closeCurrentSegment()
			r.forwardHLS(nalu, at)
			return
		}
	}
	if r.muxer == nil {
		tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatH265))
		if err != nil {
			xiaomiLogger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		r.muxer = muxer.NewMP4Muxer(tempPath)
		// Preallocation from the previous segment's final size (#757 parity).
		if r.lastSegBytes > 0 {
			r.muxer.SetPreallocateBytes(r.lastSegBytes)
		}
		trackID, err := r.muxer.AddH265Track(r.vps, r.sps, r.pps)
		if err != nil {
			xiaomiLogger.Error("failed to add H265 track", "camera_id", r.cfg.CameraID, "error", err)
			r.muxer = nil
			os.Remove(tempPath)
			return
		}
		r.trackID = trackID

		// Add audio track if audio codec detected (same gating as H264 path:
		// enabled AND kept in recordings — issue #520).
		if r.cfg.AudioEnabled && r.cfg.AudioInRecordings && r.audioCodecID.Load() > 0 {
			codec, cfg, ok := buildAudioMuxerConfig(r.audioCodecID.Load())
			if ok {
				aID, err := r.muxer.AddAudioTrack(codec, cfg)
				if err != nil {
					xiaomiLogger.Debug("audio track not added to muxer", "camera_id", r.cfg.CameraID, "error", err)
				} else {
					r.audioTrackID = aID
				}
			}
		}
		r.curTempPath = tempPath
		r.curFinalPath = finalPath
		r.segStart = at
		r.lastFrameTime = at
		r.frameCount = 0
	}

	pts := at.Sub(r.segStart)
	duration := at.Sub(r.lastFrameTime)
	if duration < time.Millisecond {
		duration = time.Millisecond
	}
	r.lastFrameTime = at

	if err := r.muxer.WriteSample(r.trackID, nalu, pts, duration); err != nil {
		xiaomiLogger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	r.frameCount++
	if r.adaptive != nil {
		// Frame now on disk; a later flush into this segment must skip it (#473).
		r.adaptive.MarkLastWritten()
	}

	// Forward to HLS (non-blocking)
	r.forwardHLS(nalu, at)
}

// forwardHLS sends a NALU to all stream consumers via StreamHub (non-blocking).
// For IDR frames, the codec parameter sets (SPS/PPS or VPS/SPS/PPS) are prepended
// so the HLS DTS extractor receives a complete access unit.
func (r *XiaomiRecorder) forwardHLS(nalu []byte, at time.Time) {
	if r.Hub == nil || r.Hub.ConsumerCount() == 0 {
		return
	}
	// Convert wall-clock duration to 90kHz ticks (RTP timestamp units).
	// HLS manager uses ClockRate=90000, so PTS must be in 90kHz ticks,
	// not nanoseconds. This matches built-in H264/H265 recorders which
	// pass RTP timestamps directly. at is the packet arrival time (#1003):
	// consumption-time stamps would collapse after a read-loop stall.
	pts := at.Sub(r.streamStart).Nanoseconds() * 90000 / int64(time.Second)

	switch r.codec {
	case model.FormatH264:
		naluType := nalu[0] & 0x1F
		if naluType == 5 && r.sps != nil && r.pps != nil {
			r.Hub.Broadcast(pts, [][]byte{r.sps, r.pps, nalu}, true)
		} else {
			r.Hub.Broadcast(pts, [][]byte{nalu}, false)
		}
	case model.FormatH265:
		naluType := (nalu[0] >> 1) & 0x3F
		if (naluType == 19 || naluType == 20) && r.vps != nil && r.sps != nil && r.pps != nil {
			r.Hub.Broadcast(pts, [][]byte{r.vps, r.sps, r.pps, nalu}, true)
		} else {
			r.Hub.Broadcast(pts, [][]byte{nalu}, false)
		}
	default:
		r.Hub.Broadcast(pts, [][]byte{nalu}, false)
	}
}

// missCodecToAudio maps a MISS audio codec ID to a model.AudioCodec.
// Returns (codec, true) for known codecs, ("", false) for unknown/unsupported.
// PCMA, PCMU, and PCM all map to G.711 for StreamHub broadcast purposes.
func missCodecToAudio(codecID uint32) (model.AudioCodec, bool) {
	switch codecID {
	case missCodecPCMA, missCodecPCMU, missCodecPCM:
		return model.AudioG711, true
	case missCodecOPUS:
		return model.AudioOpus, true
	default:
		return "", false
	}
}

// buildAudioMuxerConfig returns the codec name and config bytes for the MP4 muxer
// based on the MISS audio codec ID.
func buildAudioMuxerConfig(codecID uint32) (codec string, config []byte, ok bool) {
	switch codecID {
	case missCodecPCMA:
		sr := 8000
		return "g711", []byte{0, byte(sr >> 24), byte(sr >> 16), byte(sr >> 8), byte(sr)}, true
	case missCodecPCMU:
		sr := 8000
		return "g711", []byte{1, byte(sr >> 24), byte(sr >> 16), byte(sr >> 8), byte(sr)}, true
	case missCodecOPUS:
		// Opus config: 1 byte channel count + 2 bytes PreSkip (BE) + 4 bytes InputSampleRate (BE)
		// Xiaomi cameras: mono, 16kHz input rate, no pre-skip info from MISS protocol
		opusRate := 16000
		return "opus", []byte{1, 0, 0, byte(opusRate >> 24), byte(opusRate >> 16), byte(opusRate >> 8), byte(opusRate)}, true
	default:
		return "", nil, false
	}
}

// forwardAudio broadcasts audio data via StreamHub (non-blocking)
// and writes to the MP4 muxer when an audio track is available.
// Skips silently when AudioEnabled is false.
// Unknown codec IDs are skipped with a warning log.
func (r *XiaomiRecorder) forwardAudio(codecID uint32, payload []byte) {
	if !r.cfg.AudioEnabled {
		return
	}
	audioCodec, ok := missCodecToAudio(codecID)
	if !ok {
		// Log unsupported codecs once, not per-frame (e.g. Opus codec_id=1032)
		if r.audioCodecID.Load() == 0 {
			xiaomiLogger.Info("audio codec not yet supported for recording, skipping", "camera_id", r.cfg.CameraID, "codec_id", codecID)
			r.audioCodecID.Store(codecID) // mark as seen to suppress repeated logs
		}
		return
	}

	// Remember the audio codec ID for muxer track creation.
	if r.audioCodecID.Load() == 0 {
		r.audioCodecID.Store(codecID)
		xiaomiLogger.Info("audio codec detected", "camera_id", r.cfg.CameraID, "codec_id", codecID, "codec", audioCodec)
	}

	// Broadcast to live consumers via StreamHub.
	if r.Hub != nil && r.Hub.AudioConsumerCount() > 0 {
		pts := time.Since(r.streamStart).Nanoseconds() * 90000 / int64(time.Second)
		r.Hub.BroadcastAudio(pts, audioCodec, payload)
	}

	// Audio-trigger input (issue #478): loudness window + pre-trigger ring for
	// the G.711 codecs. Opus has no pure-Go decoder — log once that the
	// trigger stays inactive for such cameras.
	if r.audioTrig != nil && r.adaptive != nil {
		if codecID == missCodecPCMU || codecID == missCodecPCMA {
			gate := r.adaptive
			trig := r.audioTrig
			trig.Ingest(codecID == missCodecPCMU, payload, 20*time.Millisecond, time.Now(),
				func(at time.Time) { gate.AudioLoud(at, 0) })
		} else if codecID == missCodecOPUS && !r.opusTrigWarned {
			r.opusTrigWarned = true
			xiaomiLogger.Warn("audio_trigger enabled but camera audio is Opus - trigger stays inactive (G.711 required)", "camera_id", r.cfg.CameraID)
		}
	}

	// Write audio to MP4 muxer if audio track is available.
	r.mu.Lock()
	m := r.muxer
	aid := r.audioTrackID
	start := r.segStart
	r.mu.Unlock()
	// Sparse (adaptive-timelapse) mode drops DISK audio, live audio continues
	// (mirrors the built-in H264/H265 recorders).
	if m != nil && aid > 0 && !r.audioSparse.Load() {
		pts := time.Since(start)
		// Audio frame duration: 20ms (G.711 and Opus both use 20ms frames).
		dur := 20 * time.Millisecond
		if err := m.WriteAudioSample(aid, payload, pts, dur); err != nil {
			xiaomiLogger.Error("failed to write audio sample", "camera_id", r.cfg.CameraID, "error", err)
		} else if r.audioTrig != nil {
			r.audioTrig.MarkWritten()
		}
	}
}
