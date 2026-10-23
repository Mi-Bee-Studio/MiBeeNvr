// Package audiosrc is an in-repo audio-only RTSP source for integration
// tests: four codecs over real SDP negotiation — /alaw, /ulaw (synthesized
// tremolo G.711), /aac and /opus (real pre-encoded fixtures under testdata).
//
// Born from the #29/#142 incident family: unit fixtures never carry the
// byte-shape distribution of REAL device payloads, and the classifier bug
// lived exactly in that gap. Recorder/ingest integration tests point at this
// server instead of hand-mocking the frame path, so the whole
// SDP → demux → recorder → hub chain runs against wire-real bytes.
//
// The server listens on 127.0.0.1:<random>, feeds all four streams
// continuously while started, and is safe for concurrent camera recorders.
package audiosrc

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/pion/rtp"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
)

// Codec identifies one of the served streams.
type Codec string

const (
	ALaw Codec = "alaw"
	ULaw Codec = "ulaw"
	AAC  Codec = "aac"
	Opus Codec = "opus"
)

var startTime = time.Now()

type feeder struct {
	st      *gortsplib.ServerStream
	media   *description.Media
	ssrc    uint32
	mu      sync.Mutex
	seq     uint16
	ts      uint32
	tsStep  uint32
	payload func(t time.Time) []*rtp.Packet
}

func (f *feeder) run(stop <-chan struct{}) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case t := <-tick.C:
			for _, p := range f.payload(t) {
				f.mu.Lock()
				f.seq++
				p.Header.Version = 2
				p.Header.SequenceNumber = f.seq
				p.Header.Timestamp = f.ts
				p.Header.SSRC = f.ssrc
				f.ts += f.tsStep
				f.mu.Unlock()
				_ = f.st.WritePacketRTP(f.media, p)
			}
		}
	}
}

// Server serves the four audio-only RTSP streams.
type Server struct {
	srv     *gortsplib.Server
	streams map[string]*feeder
	stop    chan struct{}
	addr    string
}

// Start launches the source server on 127.0.0.1:<random>.
func Start() (*Server, error) {
	s := &Server{streams: map[string]*feeder{}, stop: make(chan struct{})}

	for _, spec := range []struct {
		path  string
		mulaw bool
		freq  float64
		pt    uint8
	}{{"/alaw", false, 440, 8}, {"/ulaw", true, 660, 0}} {
		g := &format.G711{PayloadTyp: spec.pt, MULaw: spec.mulaw, SampleRate: 8000, ChannelCount: 1}
		media := &description.Media{Type: description.MediaTypeAudio, Formats: []format.Format{g}}
		st := newStream(media)
		mulaw, freq, pt := spec.mulaw, spec.freq, spec.pt
		s.streams[spec.path] = &feeder{
			st: st, media: media, ssrc: ssrcOf(), tsStep: 160,
			payload: func(t time.Time) []*rtp.Packet {
				return []*rtp.Packet{{Header: rtp.Header{PayloadType: pt}, Payload: toneFrame(t, freq, mulaw)}}
			},
		}
	}

	aus, err := loadADTS()
	if err != nil {
		return nil, fmt.Errorf("audiosrc: aac fixture: %w", err)
	}
	mfa := &format.MPEG4Audio{
		PayloadTyp: 96,
		Config: &mpeg4audio.AudioSpecificConfig{
			Type:         mpeg4audio.ObjectTypeAACLC,
			SampleRate:   44100,
			ChannelCount: 2,
		},
		SizeLength:       13,
		IndexLength:      3,
		IndexDeltaLength: 3,
	}
	mediaAAC := &description.Media{Type: description.MediaTypeAudio, Formats: []format.Format{mfa}}
	stAAC := newStream(mediaAAC)
	encAAC, err := mfa.CreateEncoder()
	if err != nil {
		return nil, err
	}
	var aacIdx uint32
	aacFeeder := &feeder{st: stAAC, media: mediaAAC, ssrc: ssrcOf(), tsStep: 1024}
	aacFeeder.payload = func(time.Time) []*rtp.Packet {
		au := [][]byte{aus[aacIdx%uint32(len(aus))]}
		aacIdx++
		pkts, err := encAAC.Encode(au)
		if err != nil {
			return nil
		}
		return pkts
	}
	s.streams["/aac"] = aacFeeder

	opkts, err := loadOggOpus()
	if err != nil {
		return nil, fmt.Errorf("audiosrc: opus fixture: %w", err)
	}
	op := &format.Opus{PayloadTyp: 97, ChannelCount: 1}
	mediaOpus := &description.Media{Type: description.MediaTypeAudio, Formats: []format.Format{op}}
	var opIdx uint32
	opFeeder := &feeder{st: newStream(mediaOpus), media: mediaOpus, ssrc: ssrcOf(), tsStep: 960}
	opFeeder.payload = func(time.Time) []*rtp.Packet {
		p := opkts[opIdx%uint32(len(opkts))]
		opIdx++
		return []*rtp.Packet{{Header: rtp.Header{PayloadType: 97}, Payload: p}}
	}
	s.streams["/opus"] = opFeeder

	s.srv = &gortsplib.Server{
		RTSPAddress: "127.0.0.1:0",
		Handler:     &handler{streams: s.streams},
	}
	if err := s.srv.Start(); err != nil {
		return nil, err
	}
	for _, f := range s.streams {
		f.st.Server = s.srv
		if err := f.st.Initialize(); err != nil {
			s.Close()
			return nil, err
		}
	}
	s.addr = s.srv.NetListener().Addr().String()
	for _, f := range s.streams {
		go f.run(s.stop)
	}
	return s, nil
}

// URL returns the RTSP URL for one of the four codecs.
func (s *Server) URL(c Codec) string {
	return "rtsp://" + s.addr + "/" + string(c)
}

// Close stops the server and its feeders.
func (s *Server) Close() {
	close(s.stop)
	for _, f := range s.streams {
		f.st.Close()
	}
	s.srv.Close()
}

func newStream(media *description.Media) *gortsplib.ServerStream {
	return &gortsplib.ServerStream{Desc: &description.Session{Medias: []*description.Media{media}}}
}

func ssrcOf() uint32 { return uint32(time.Now().UnixNano()) }

type handler struct {
	streams map[string]*feeder
}

func (h *handler) OnConnOpen(*gortsplib.ServerHandlerOnConnOpenCtx) {}

func (h *handler) OnSessionOpen(*gortsplib.ServerHandlerOnSessionOpenCtx) {}

func (h *handler) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	f, ok := h.streams[ctx.Path]
	if !ok {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, fmt.Errorf("no such path")
	}
	return &base.Response{StatusCode: base.StatusOK}, f.st, nil
}

func (h *handler) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	f, ok := h.streams[ctx.Path]
	if !ok {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, fmt.Errorf("no such path")
	}
	return &base.Response{StatusCode: base.StatusOK}, f.st, nil
}

func (h *handler) OnPlay(*gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// --- G.711 synthesis (Sun g711.c reference algorithms) ---

var segUEnd = [8]int{0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF, 0x1FFF}
var segAEnd = [8]int{0x1F, 0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF}

func segSearch(v int, ends *[8]int) int {
	for i, e := range ends {
		if v <= e {
			return i
		}
	}
	return 8
}

func mulawEncode(pcm int) byte {
	pcm >>= 2
	mask := 0xFF
	if pcm < 0 {
		pcm = -pcm
		mask = 0x7F
	}
	if pcm > 8159 {
		pcm = 8159
	}
	pcm += 0x84 >> 2
	seg := segSearch(pcm, &segUEnd)
	if seg >= 8 {
		return byte(0x7F ^ mask)
	}
	return byte(seg<<4|(pcm>>(seg+1))&0xF) ^ byte(mask)
}

func alawEncode(pcm int) byte {
	pcm >>= 3
	mask := 0xD5
	if pcm < 0 {
		pcm = -pcm - 1
		mask = 0x55
	}
	seg := segSearch(pcm, &segAEnd)
	if seg >= 8 {
		return byte(0x7F ^ mask)
	}
	aval := byte(seg << 4)
	if seg < 2 {
		aval |= byte(pcm>>1) & 0xF
	} else {
		aval |= byte(pcm>>seg) & 0xF
	}
	return aval ^ byte(mask)
}

// toneFrame synthesizes 20ms of tremolo-modulated sine — the REAL amplitude
// fluctuation must survive the whole pipeline (any honest waveform render
// must visibly breathe).
func toneFrame(t time.Time, freq float64, mulaw bool) []byte {
	const rate = 8000
	n := 160
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		tt := t.Sub(startTime).Seconds() + float64(i)/rate
		env := 0.12 + 0.88*(0.5+0.5*math.Sin(2*math.Pi*0.45*tt))
		s := int16(env * 22000 * math.Sin(2*math.Pi*freq*tt))
		if mulaw {
			out = append(out, mulawEncode(int(s)))
		} else {
			out = append(out, alawEncode(int(s)))
		}
	}
	return out
}
