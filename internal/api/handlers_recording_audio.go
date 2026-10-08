package api

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var audioRecLogger = slogx.Component("api-audio-recording")

// handleAudioWav serves an audio-only recording transcoded to WAV:
// G.711 (μ-law/A-law) decodes through the pure-Go tables into 16-bit PCM —
// browsers do not play G.711-in-MP4 (#321 precedent), so this endpoint is
// the universal playback path for those segments. AAC/Opus recordings are
// natively playable in <audio> via the download endpoint and are refused
// here with a hint instead of being silently mistranscoded.
//
// Compliance: audio evidence is more sensitive than video — every fetch is
// audit-logged with the authenticated requester.
func (h *Handler) handleAudioWav(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.Format != model.FormatAudio {
		WriteError(w, http.StatusBadRequest, "not an audio recording")
		return
	}
	if rec.FilePath == "" {
		WriteError(w, http.StatusNotFound, "file not available")
		return
	}
	validPath, err := storage.ValidatePath(h.store.RootDir(), rec.FilePath)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, &model.PathTraversalError{Path: rec.FilePath})
		return
	}

	fourcc, rate, channels, payload, err := readAudioOnlyMP4(validPath)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Sprintf("audio extraction failed: %v", err))
		return
	}

	var pcm []byte
	switch fourcc {
	case "ulaw":
		pcm = g711ToPCM(payload, true, rate)
	case "alaw":
		pcm = g711ToPCM(payload, false, rate)
	case "mp4a", "dops", "Opus":
		// AAC ('mp4a') and Opus ('Opus' QuickTime style — what our muxer
		// writes — plus 'dops' MPEG style defensively) are natively playable
		// in browsers straight from the MP4 download endpoint.
		WriteError(w, http.StatusNotAcceptable,
			fmt.Sprintf("audio codec %q is natively playable in browsers — use the download endpoint for this recording", fourcc))
		return
	default:
		WriteError(w, http.StatusNotAcceptable, fmt.Sprintf("unsupported audio codec %q", fourcc))
		return
	}
	if channels == 0 {
		channels = 1
	}
	if rate == 0 {
		rate = 8000
	}

	audioRecLogger.Info("audio recording WAV fetch (audit)",
		"recording_id", id, "camera_id", rec.CameraID,
		"remote", r.RemoteAddr, "ua", r.UserAgent(),
		"codec", fourcc, "bytes_in", len(payload), "bytes_out", len(pcm)+44)
	recordAudioAudit(audioAuditEntry{
		At: time.Now(), Kind: "wav", Recording: id, CameraID: rec.CameraID,
		Remote: r.RemoteAddr, UA: r.UserAgent(),
		Bytes: int64(len(pcm) + 44),
	})

	w.Header().Set("Content-Type", "audio/wav")
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%s.wav", disposition, id))
	if err := writeWav(w, pcm, rate, channels); err != nil {
		audioRecLogger.Warn("audio WAV write failed", "recording_id", id, "error", err)
	}
}

// g711ToPCM decodes a raw G.711 byte stream into little-endian 16-bit PCM.
func g711ToPCM(g711 []byte, muLaw bool, _ int) []byte {
	pcm := make([]byte, len(g711)*2)
	for i, b := range g711 {
		var s int16
		if muLaw {
			s = recorder.DecodeMuLaw(b)
		} else {
			s = recorder.DecodeALaw(b)
		}
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(s))
	}
	return pcm
}

// writeWav streams a canonical 44-byte-header PCM WAV.
func writeWav(w io.Writer, pcm []byte, sampleRate, channels int) error {
	byteRate := sampleRate * channels * 2
	blockAlign := channels * 2
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(pcm)))
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16) // PCM chunk size
	binary.LittleEndian.PutUint16(hdr[20:], 1)  // PCM format
	binary.LittleEndian.PutUint16(hdr[22:], uint16(channels))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(byteRate))
	binary.LittleEndian.PutUint16(hdr[32:], uint16(blockAlign))
	binary.LittleEndian.PutUint16(hdr[34:], 16) // bits per sample
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(pcm)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(pcm)
	return err
}

// --- Minimal audio-only MP4 reader ---
//
// The muxer layout is fixed (ftyp | mdat | moov) with a single audio track:
// mdat holds the raw codec stream back to back, moov/mdia/minf/stbl/stsd's
// first sample entry names the codec. Hand-rolled instead of a full box
// parser because only these two facts are needed, and only for files the
// NVR itself wrote.

const audioMP4MaxPayload = 512 << 20 // sanity cap: 60s G.711 ≈ 0.5MB — anything near this cap is not our file

// readAudioOnlyMP4 returns the audio fourcc ('ulaw'/'alaw'/'mp4a'/…), the
// sample rate and channel count from the first sample entry, and the raw
// mdat payload.
func readAudioOnlyMP4(path string) (fourcc string, rate, channels int, payload []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, 0, nil, err
	}
	defer f.Close()

	var mdat []byte
	for {
		var size uint32
		var btype [4]byte
		if err := binary.Read(f, binary.BigEndian, &size); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", 0, 0, nil, fmt.Errorf("read box header: %w", err)
		}
		if _, err := io.ReadFull(f, btype[:]); err != nil {
			return "", 0, 0, nil, fmt.Errorf("read box type: %w", err)
		}
		headerLen := int64(8)
		if size == 1 {
			var large uint64
			if err := binary.Read(f, binary.BigEndian, &large); err != nil {
				return "", 0, 0, nil, fmt.Errorf("read largesize: %w", err)
			}
			if large > uint64(audioMP4MaxPayload)+1024 {
				return "", 0, 0, nil, errors.New("oversized box")
			}
			size = uint32(large)
			headerLen = 16
		} else if size == 0 {
			// Box extends to EOF — only legal for the last box.
			fi, serr := f.Stat()
			if serr != nil {
				return "", 0, 0, nil, serr
			}
			pos, _ := f.Seek(0, io.SeekCurrent)
			size = uint32(fi.Size() - pos + 8)
		}
		bodyLen := int64(size) - headerLen
		if bodyLen < 0 {
			return "", 0, 0, nil, errors.New("negative box body")
		}
		switch string(btype[:]) {
		case "mdat":
			if bodyLen > audioMP4MaxPayload {
				return "", 0, 0, nil, errors.New("mdat exceeds sanity cap")
			}
			mdat = make([]byte, bodyLen)
			if _, err := io.ReadFull(f, mdat); err != nil {
				return "", 0, 0, nil, fmt.Errorf("read mdat: %w", err)
			}
		case "moov":
			moov := make([]byte, bodyLen)
			if _, err := io.ReadFull(f, moov); err != nil {
				return "", 0, 0, nil, fmt.Errorf("read moov: %w", err)
			}
			fourcc, rate, channels, err = probeStsd(moov)
			if err != nil {
				return "", 0, 0, nil, err
			}
		default:
			if _, err := f.Seek(bodyLen, io.SeekCurrent); err != nil {
				return "", 0, 0, nil, fmt.Errorf("skip box: %w", err)
			}
		}
	}
	if fourcc == "" {
		return "", 0, 0, nil, errors.New("no audio sample entry found")
	}
	if mdat == nil {
		return "", 0, 0, nil, errors.New("no mdat found")
	}
	return fourcc, rate, channels, mdat, nil
}

// probeStsd descends moov→trak→mdia→minf→stbl→stsd and reads the first
// sample entry's fourcc plus the AudioSampleEntry channel/rate fields.
func probeStsd(moov []byte) (fourcc string, rate, channels int, err error) {
	trak := findBox(moov, "trak")
	if trak == nil {
		return "", 0, 0, errors.New("trak not found")
	}
	mdia := findBox(trak, "mdia")
	if mdia == nil {
		return "", 0, 0, errors.New("mdia not found")
	}
	minf := findBox(mdia, "minf")
	if minf == nil {
		return "", 0, 0, errors.New("minf not found")
	}
	stbl := findBox(minf, "stbl")
	if stbl == nil {
		return "", 0, 0, errors.New("stbl not found")
	}
	stsd := findBox(stbl, "stsd")
	if stsd == nil {
		return "", 0, 0, errors.New("stsd not found")
	}
	// stsd payload: version/flags(4) + entry_count(4) + entries. First
	// entry: size(4) + fourcc(4), then AudioSampleEntry: reserved[6] +
	// data_reference_index[2] + entry_version[2] + reserved[6] +
	// channelcount[2] + samplesize[2] + predefined[2] + reserved[2] +
	// samplerate[4 as 16.16 fixed-point].
	if len(stsd) < 16 {
		return "", 0, 0, errors.New("stsd too short")
	}
	fourcc = string(stsd[12:16])
	if len(stsd) >= 44 {
		channels = int(binary.BigEndian.Uint16(stsd[32:34]))
		rate = int(binary.BigEndian.Uint32(stsd[40:44]) >> 16)
	}
	return fourcc, rate, channels, nil
}

// findBox returns the first child box's payload with the given type.
func findBox(parent []byte, want string) []byte {
	for off := 0; off+8 <= len(parent); {
		size := int(binary.BigEndian.Uint32(parent[off : off+4]))
		btype := string(parent[off+4 : off+8])
		if size < 8 || off+size > len(parent) {
			return nil
		}
		if btype == want {
			return parent[off+8 : off+size]
		}
		off += size
	}
	return nil
}
