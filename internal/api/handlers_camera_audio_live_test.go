package api

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

func audioLiveEnv(t *testing.T, rec model.Recorder, cameraID string) http.Handler {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.UpsertCamera(t.Context(), cameraID, "Mic", "rtsp", "audio", "rtsp://127.0.0.1:1/mic", "", "", "", "", "", ""))
	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cameras: []config.CameraConfig{},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	if rec != nil {
		camMgr.SetTestRecorder(cameraID, rec)
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil, nil)
	return h.Routes()
}

// TestAudioLiveWav_StreamsPCM drives the live endpoint against a real
// AudioRecorder + StreamHub: G.711 frames broadcast to the hub must arrive as
// a streaming WAV (44-byte header with 0xFFFFFFFF sizes + decoded PCM).
func TestAudioLiveWav_StreamsPCM(t *testing.T) {
	ar := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: "cam-mic"}, nil, nil)
	hub := streamhub.New()
	ar.SetHub(hub)
	ar.ArmPushFormat("g711", 8000, 1) // μ-law 8k mono

	srv := httptest.NewServer(audioLiveEnv(t, ar, "cam-mic"))
	defer srv.Close()

	// Feed the hub while a client is attached. The test body closes `stop`
	// (single closer — no defer here).
	stop := make(chan struct{})
	go func() {
		g711 := make([]byte, 160)
		for i := range g711 {
			g711[i] = 0xFF
		}
		deadline := time.After(3 * time.Second)
		for {
			select {
			case <-deadline:
				return
			case <-stop:
				return
			default:
			}
			hub.BroadcastAudio(time.Now().UnixNano(), model.AudioG711, g711)
			time.Sleep(20 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/cameras/cam-mic/audio/live.wav", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "audio/wav", resp.Header.Get("Content-Type"))

	head := make([]byte, 44)
	_, err = io.ReadFull(resp.Body, head)
	require.NoError(t, err)
	require.Equal(t, "RIFF", string(head[0:4]))
	require.Equal(t, uint32(0xFFFFFFFF), binary.LittleEndian.Uint32(head[4:8]), "streaming RIFF size")
	require.Equal(t, uint32(8000), binary.LittleEndian.Uint32(head[24:28]))

	body := make([]byte, 4096)
	n, _ := io.ReadFull(resp.Body, body)
	require.Greater(t, n, 320, "expected streaming PCM beyond one frame (got %d bytes)", n)
	// μ-law 0xFF decodes to 0 linear.
	require.EqualValues(t, 0, binary.LittleEndian.Uint16(body[0:2]))
	close(stop)

	// Session teardown must land in the audit ring.
	require.Eventually(t, func() bool {
		audioAudit.mu.Lock()
		defer audioAudit.mu.Unlock()
		for _, e := range audioAudit.entries {
			if e.Kind == "live" && e.CameraID == "cam-mic" && e.DurationS > 0 {
				return true
			}
		}
		return false
	}, 3*time.Second, 100*time.Millisecond, "live session should be audited")
}

// TestAudioLiveWav_DeclaresFiniteContentLength pins the #1052 fix shape:
// the WAV branches must declare a finite Content-Length (nominal window),
// because Chromium's WAV demuxer rejects open-ended bodies. The 0xFFFFFFFF
// size markers stay — with a known resource size the parser clamps them.
func TestAudioLiveWav_DeclaresFiniteContentLength(t *testing.T) {
	ar := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: "cam-mic"}, nil, nil)
	hub := streamhub.New()
	ar.SetHub(hub)
	ar.ArmPushFormat("g711", 8000, 1)

	srv := httptest.NewServer(audioLiveEnv(t, ar, "cam-mic"))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/cameras/cam-mic/audio/live.wav", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// 44-byte header + 8000 Hz * 1 ch * 2 bytes * 3600 s.
	require.Equal(t, "57600044", resp.Header.Get("Content-Length"))
}

// TestAudioLiveWav_EndsExactlyAtDeclaredLength shrinks the nominal window and
// asserts the body terminates cleanly at exactly Content-Length bytes — the
// mid-frame truncation path included.
func TestAudioLiveWav_EndsExactlyAtDeclaredLength(t *testing.T) {
	orig := audioLiveWavNominalWindow
	audioLiveWavNominalWindow = 250 * time.Millisecond // 8000*2*0.25 = 4000 bytes + 44 header
	t.Cleanup(func() { audioLiveWavNominalWindow = orig })

	ar := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: "cam-mic"}, nil, nil)
	hub := streamhub.New()
	ar.SetHub(hub)
	ar.ArmPushFormat("g711", 8000, 1)

	srv := httptest.NewServer(audioLiveEnv(t, ar, "cam-mic"))
	defer srv.Close()

	go func() { // keep frames flowing well past the window
		g711 := make([]byte, 160)
		deadline := time.After(3 * time.Second)
		for {
			select {
			case <-deadline:
				return
			default:
			}
			hub.BroadcastAudio(time.Now().UnixNano(), model.AudioG711, g711)
			time.Sleep(20 * time.Millisecond)
		}
	}()

	resp, err := http.Get(srv.URL + "/api/cameras/cam-mic/audio/live.wav")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, "4044", resp.Header.Get("Content-Length"))

	all, err := io.ReadAll(resp.Body) // ends when the handler closes at the cap
	require.NoError(t, err, "body must end cleanly, not with an unexpected EOF")
	require.Len(t, all, 4044, "exactly the declared Content-Length bytes")
	require.Equal(t, "RIFF", string(all[0:4]))
}

func TestAudioLiveWav_NotAudioDevice(t *testing.T) {
	h := audioLiveEnv(t, nil, "cam-none") // no recorder registered
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/cameras/cam-none/audio/live.wav")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAudioAuditEndpoint(t *testing.T) {
	h := audioLiveEnv(t, nil, "cam-x")
	recordAudioAudit(audioAuditEntry{Kind: "wav", CameraID: "cam-x", Recording: "r1"})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/recordings/audio-audit")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), `"recording":"r1"`)
}

// driveLiveFormat opens /audio/live against a hub-fed recorder, reads up to
// `readN` bytes of body and returns status, content type and the X-Audio-Codec
// header along with the first readN body bytes.
func driveLiveFormat(t *testing.T, ar *recorder.AudioRecorder, cameraID string, codec model.AudioCodec, frame []byte, frameEvery time.Duration, readN int) (int, string, string, []byte) {
	t.Helper()
	hub := ar.GetHub()
	srv := httptest.NewServer(audioLiveEnv(t, ar, cameraID))
	t.Cleanup(srv.Close)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		deadline := time.After(5 * time.Second)
		for {
			select {
			case <-deadline:
				return
			case <-stop:
				return
			default:
			}
			hub.BroadcastAudio(time.Now().UnixNano(), codec, frame)
			time.Sleep(frameEvery)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/cameras/"+cameraID+"/audio/live", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	// Accumulate until the buffer is full or the context deadline — the
	// server flushes on a 500ms cadence, so the first read may carry only
	// the initial header flush.
	body := make([]byte, readN)
	n := 0
	for n < readN {
		m, rerr := resp.Body.Read(body[n:])
		n += m
		if rerr != nil || ctx.Err() != nil {
			break
		}
	}
	require.Greater(t, n, 0, "expected streaming body (status %d)", resp.StatusCode)
	return resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("X-Audio-Codec"), body[:n]
}

// TestAudioLive_AAC_ADTS: an AAC-armed recorder (rate/channels only, no ASC —
// the push-ingest shape) must stream ADTS: first frame is a 7-byte header
// with the syncword, AAC-LC profile, the armed rate index and channels.
func TestAudioLive_AAC_ADTS(t *testing.T) {
	ar := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: "cam-aac"}, nil, nil)
	ar.SetHub(streamhub.New())
	ar.ArmPushFormat("aac", 44100, 2)

	status, ctype, codecHdr, body := driveLiveFormat(t, ar, "cam-aac", model.AudioAAC, make([]byte, 128), 10*time.Millisecond, 64)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "audio/aac", ctype)
	require.Equal(t, "aac", codecHdr)
	require.EqualValues(t, 0xFFF1, int(body[0])<<8|int(body[1]), "ADTS syncword + MPEG-4 + no CRC")
	require.Equal(t, byte(1), (body[2]>>6)&0x03, "AAC-LC profile")
	require.Equal(t, byte(4), (body[2]>>2)&0x0F, "44100 Hz frequency index")
	require.Equal(t, byte(2), body[2]&0x01<<2|body[3]>>6, "stereo channels")
	frameLen := int(body[3]&0x03)<<11 | int(body[4])<<3 | int(body[5])>>5
	require.Equal(t, 7+128, frameLen, "frame_length covers header + AU")
}

// TestAudioLive_Opus_Ogg: an Opus-armed recorder must stream Ogg Opus: ID
// header page (BOS), OpusTags page, then data pages whose granule advances
// by 960 samples per packet and whose lacing handles >255-byte packets.
func TestAudioLive_Opus_Ogg(t *testing.T) {
	ar := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: "cam-opus"}, nil, nil)
	ar.SetHub(streamhub.New())
	ar.ArmPushFormat("opus", 48000, 1)

	status, ctype, codecHdr, body := driveLiveFormat(t, ar, "cam-opus", model.AudioOpus, make([]byte, 300), 15*time.Millisecond, 4096)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "audio/ogg", ctype)
	require.Equal(t, "opus", codecHdr)

	require.Equal(t, "OggS", string(body[0:4]))
	require.Equal(t, byte(0x02), body[5], "first page is BOS")
	require.Contains(t, string(body[:512]), "OpusHead", "ID header payload")
	require.Contains(t, string(body[:512]), "OpusTags", "comment header payload")

	// CRC of the first page verifies with the Ogg polynomial.
	end := 27 + 1 + 19 // one segment of 19 bytes (OpusHead payload)
	page := append([]byte(nil), body[:end]...)
	for i := 22; i < 26; i++ {
		page[i] = 0 // CRC is computed over the page with the field zeroed
	}
	require.Equal(t, oggCRC(page), binary.LittleEndian.Uint32(body[22:26]), "page CRC")

	// A 300-byte packet needs two lacing segments (255 + 45); find its page
	// by scanning for a page whose lacing sums to 300.
	found := false
	for off := 0; off+27 < len(body)-330; off++ {
		if string(body[off:off+4]) != "OggS" || off+27 > len(body) {
			continue
		}
		nSegs := int(body[off+26])
		total := 0
		for i := range nSegs {
			total += int(body[off+27+i])
		}
		if total == 300 && nSegs == 2 {
			found = true
			break
		}
	}
	require.True(t, found, "a 300-byte Opus packet should span two lacing segments")

	// Granule positions on consecutive data pages advance by 960 (20ms@48k).
	var dataGranules []int64
	for off := 0; off+27 < len(body)-300; off++ {
		if string(body[off:off+4]) != "OggS" {
			continue
		}
		if string(body[off+28:off+28+8]) == "OpusHead" || string(body[off+28:off+28+8]) == "OpusTags" {
			continue
		}
		dataGranules = append(dataGranules, int64(binary.LittleEndian.Uint64(body[off+6:off+14])))
		if len(dataGranules) >= 3 {
			break
		}
	}
	require.GreaterOrEqual(t, len(dataGranules), 2, "expected at least two data pages")
	require.Equal(t, int64(960), dataGranules[0], "first data page granule = one 20ms frame")
	require.Equal(t, int64(1920), dataGranules[1], "granule advances 960 per packet")
}

// TestAudioLive_G711_SameAsWav: the adaptive endpoint serves G.711 as the
// same streaming WAV as /live.wav.
func TestAudioLive_G711_SameAsWav(t *testing.T) {
	ar := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: "cam-g711"}, nil, nil)
	ar.SetHub(streamhub.New())
	ar.ArmPushFormat("g711", 8000, 1)

	status, ctype, codecHdr, body := driveLiveFormat(t, ar, "cam-g711", model.AudioG711, make([]byte, 160), 20*time.Millisecond, 128)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "audio/wav", ctype)
	require.Equal(t, "g711", codecHdr)
	require.Equal(t, "RIFF", string(body[0:4]))
	require.Equal(t, uint32(0xFFFFFFFF), binary.LittleEndian.Uint32(body[4:8]))
}

// TestAudioLive_OpusHeaderParams: OpusHead payload carries channels + input
// rate from the armed config (1ch / 48k default).
func TestAudioLive_OpusHeaderParams(t *testing.T) {
	ar := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: "cam-opus2"}, nil, nil)
	ar.SetHub(streamhub.New())
	ar.ArmPushFormat("opus", 16000, 2) // unusual rate+stereo must round-trip

	_, _, _, body := driveLiveFormat(t, ar, "cam-opus2", model.AudioOpus, make([]byte, 20), 20*time.Millisecond, 128)
	require.Equal(t, "OpusHead", string(body[28:36]))
	require.Equal(t, byte(2), body[37], "channels")
	require.Equal(t, uint32(16000), binary.LittleEndian.Uint32(body[40:44]), "input sample rate")
}

// TestADTS_ASCParse exercises the production ASC parser (the RTSP-pull path
// of newLiveADTSFormat): fields decode per ISO 14496-3 and out-of-range
// configurations are rejected so the armed-rate fallback takes over.
func TestADTS_ASCParse(t *testing.T) {
	// ASC for AAC-LC, 48000 Hz (idx 3), stereo: AOT=2 (00010), freq idx 3
	// (0011), ch 2 (0010); bits: 00010 0011 0010 ... = 0x11 0x90.
	objType, fi, cc, ok := parseAudioSpecificConfig([]byte{0x11, 0x90})
	require.True(t, ok)
	require.Equal(t, 2, objType)
	require.Equal(t, 3, fi)
	require.Equal(t, 2, cc)

	// AOT 0 and reserved frequency index 13 must be rejected (all other
	// fields kept valid so the rejection isolates the one under test).
	_, _, _, bad := parseAudioSpecificConfig([]byte{0x01, 0x90}) // AOT 0, freq idx 3, stereo
	require.False(t, bad)
	_, _, _, bad = parseAudioSpecificConfig([]byte{0x16, 0x90}) // AOT 2, freq idx 13 (reserved), stereo
	require.False(t, bad)
	_, _, _, bad = parseAudioSpecificConfig([]byte{0x11}) // truncated
	require.False(t, bad)
}

// TestAudioLive_NotConnected: no hub → 503.
func TestAudioLive_NotConnected(t *testing.T) {
	ar := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: "cam-off"}, nil, nil)
	srv := httptest.NewServer(audioLiveEnv(t, ar, "cam-off"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/cameras/cam-off/audio/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}
