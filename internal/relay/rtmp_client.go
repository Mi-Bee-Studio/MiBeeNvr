package relay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/amf0"
	"github.com/bluenviron/gortmplib/pkg/bytecounter"
	"github.com/bluenviron/gortmplib/pkg/message"
)

// rtmpPublishConn adapts a raw net.Conn + gortmplib message ReadWriter to the
// gortmplib.Conn interface, so gortmplib.Writer can write H.264/AAC frames
// through it. Unlike gortmplib.Client (which hardcodes the connect command),
// this gives the relay engine full control over the RTMP handshake → connect →
// publish sequence, so we can match what FFmpeg sends for strict receivers
// (e.g. Douyu Live Companion).
type rtmpPublishConn struct {
	nconn       net.Conn
	mrw         *message.ReadWriter
	bc          *bytecounter.ReadWriter
	wmu         sync.Mutex // protects concurrent Write (video frames + PingResponse)
	streamID    uint32     // message stream ID (0x1000000 = FMLE convention)
	chunkSize   uint32     // write chunk size (default 128)
	videoWidth  int        // for onMetaData (0 = unknown)
	videoHeight int        // for onMetaData (0 = unknown)
	videoFPS    float64    // for onMetaData (0 = unknown → omit)
}

func (c *rtmpPublishConn) Read() (message.Message, error) { return c.mrw.Read() }

// Write forces Type 0 chunk headers (full 12-byte header) on outbound messages
// that strict RTMP receivers (Douyu Live Companion) choke on when they reuse a
// chunk stream ID already used by a prior message. gortmplib's standard writer
// emits a Type 1/2/3 header for the 2nd+ message on a CSID; the companion
// cannot parse those and resets the connection right after Publish.Start.
//
// Messages that MUST get Type 0 are those sent during gortmplib.Writer.Initialize()
// that land on a CSID already occupied by a command:
//   - onMetaData / @setDataFrame (DataAMF0 on CSID 4 — CSID 4 was first used by
//     the publish command, so without this intercept the metadata would be a
//     Type 1 header and trigger an RST).
//   - AudioSpecificConfig (Audio AACTypeConfig on CSID 4 — same reason).
//
// Video frames (AU) are routed through writeRawMessage for uniformity.
// AVC sequence header (Video VideoTypeConfig) is the first message on CSID 6, so
// the standard writer already emits Type 0 for it — we leave it alone because
// re-encoding its mp4 box payload would be needlessly complex.
// Command messages (connect, publish, etc.) go through the standard writer;
// dump analysis confirmed the companion accepts their Type 1 headers during
// the handshake phase.
func (c *rtmpPublishConn) Write(msg message.Message) error {
	switch m := msg.(type) {
	case *message.DataAMF0:
		// onMetaData / @setDataFrame — replace the sparse gortmplib metadata
		// (only videocodecid/videodatarate) with the full FFmpeg-style field set
		// that strict receivers (Douyu Live Companion) validate. Without
		// width/height/framerate the companion rejects the stream right after
		// Publish.Start (the FFmpeg relay path documents this same requirement).
		// Then force a Type 0 chunk header.
		fullMeta := c.buildFullMetadata(m.Payload)
		payload, err := fullMeta.Marshal()
		if err != nil {
			return fmt.Errorf("marshal onMetaData AMF0: %w", err)
		}
		return c.writeRawMessage(m.ChunkStreamID, 0x12, 0, payload) // msg type 18 = DataAMF0

	case *message.Video:
		if m.Type == message.VideoTypeAU && m.AU != nil {
			body := make([]byte, 5+len(m.AU))
			if m.IsKeyFrame {
				body[0] = 0x10 | m.Codec
			} else {
				body[0] = 0x20 | m.Codec
			}
			body[1] = 0x01 // AVCPacketType = NALU
			ptsDelta := uint32(m.PTSDelta / time.Millisecond)
			body[2] = byte(ptsDelta >> 16)
			body[3] = byte(ptsDelta >> 8)
			body[4] = byte(ptsDelta)
			copy(body[5:], m.AU)
			return c.writeRawMessage(m.ChunkStreamID, 0x09, uint32(m.DTS/time.Millisecond), body)
		}

	case *message.Audio:
		if m.Codec == message.CodecMPEG4Audio && m.AACType == message.AudioAACTypeConfig && m.AACConfig != nil {
			// AudioSpecificConfig would be a Type 1 on CSID 4 (after onMetaData).
			// Re-encode the raw config bytes from the struct and force Type 0.
			raw, mErr := m.AACConfig.Marshal()
			if mErr != nil {
				return fmt.Errorf("marshal AudioSpecificConfig: %w", mErr)
			}
			body := make([]byte, 2+len(raw))
			body[0] = (m.Codec << 4) | (uint8(m.Rate) << 2) | (uint8(m.Depth) << 1)
			if m.IsStereo {
				body[0] |= 1
			}
			body[1] = 0x00 // AAC sequence header
			copy(body[2:], raw)
			return c.writeRawMessage(m.ChunkStreamID, 0x08, 0, body)
		}
		if m.Codec == message.CodecMPEG4Audio && m.AACType == message.AudioAACTypeAU && m.AU != nil {
			body := make([]byte, 2+len(m.AU))
			body[0] = (m.Codec << 4) | (uint8(m.Rate) << 2) | (uint8(m.Depth) << 1)
			if m.IsStereo {
				body[0] |= 1
			}
			body[1] = 0x01 // AAC AU
			copy(body[2:], m.AU)
			return c.writeRawMessage(m.ChunkStreamID, 0x08, uint32(m.DTS/time.Millisecond), body)
		}
		if (m.Codec == message.CodecPCMA || m.Codec == message.CodecPCMU) && m.AU != nil {
			body := make([]byte, 1+len(m.AU))
			body[0] = (m.Codec << 4) | (uint8(m.Rate) << 2) | (uint8(m.Depth) << 1)
			if m.IsStereo {
				body[0] |= 1
			}
			copy(body[1:], m.AU)
			return c.writeRawMessage(m.ChunkStreamID, 0x08, uint32(m.DTS/time.Millisecond), body)
		}
	}

	// AVC sequence header (VideoTypeConfig), command messages, and control
	// messages use the standard writer. These are either first-on-CSID (Type 0)
	// or accepted by the companion as Type 1 during handshake.
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.setWriteDeadline()
	return c.mrw.Write(msg)
}

// setWriteDeadline bounds the next outbound write (#429): a dead peer
// (receiver restart, silently dropped NAT state) never RSTs, so once the
// kernel send buffer fills a deadline-less write blocks forever and the
// target wedges in status=streaming with kbps=0.
func (c *rtmpPublishConn) setWriteDeadline() {
	_ = c.nconn.SetWriteDeadline(time.Now().Add(relayWriteTimeout))
}

// buildFullMetadata takes gortmplib's sparse onMetaData payload
// ([@setDataFrame, "onMetaData", {videocodecid, videodatarate, ...}]) and
// returns a payload whose metadata object mirrors FFmpeg's full field set:
// duration, width, height, videodatarate, framerate, videocodecid, and (for
// audio tracks) audiodatarate, audiosamplerate, audiosamplesize, audiochannels,
// audiocodecid, stereo. Strict receivers such as the Douyu Live Companion
// validate width/height/framerate and reject the stream if they are absent.
func (c *rtmpPublishConn) buildFullMetadata(orig amf0.Data) amf0.Data {
	if len(orig) < 3 {
		return orig // unexpected shape; leave untouched
	}
	// orig[0] = "@setDataFrame", orig[1] = "onMetaData", orig[2] = metadata object.
	hasAudio := false
	if obj, ok := orig[2].(amf0.Object); ok {
		if v, found := obj.Get("audiocodecid"); found && v != nil {
			hasAudio = true
		}
	}

	meta := amf0.Object{
		{Key: "duration", Value: float64(0)},
	}
	if c.videoWidth > 0 {
		meta = append(meta, amf0.ObjectEntry{Key: "width", Value: float64(c.videoWidth)})
	}
	if c.videoHeight > 0 {
		meta = append(meta, amf0.ObjectEntry{Key: "height", Value: float64(c.videoHeight)})
	}
	meta = append(
		meta,
		amf0.ObjectEntry{Key: "videodatarate", Value: float64(0)},
	)
	if c.videoFPS > 0 {
		meta = append(meta, amf0.ObjectEntry{Key: "framerate", Value: c.videoFPS})
	}
	meta = append(
		meta,
		amf0.ObjectEntry{Key: "videocodecid", Value: float64(7)}, // H.264
	)
	if hasAudio {
		meta = append(
			meta,
			amf0.ObjectEntry{Key: "audiodatarate", Value: float64(0)},
			amf0.ObjectEntry{Key: "audiosamplerate", Value: float64(44100)},
			amf0.ObjectEntry{Key: "audiosamplesize", Value: float64(16)},
			amf0.ObjectEntry{Key: "audiochannels", Value: float64(2)},
			amf0.ObjectEntry{Key: "audiocodecid", Value: float64(10)}, // AAC
			amf0.ObjectEntry{Key: "stereo", Value: true},
		)
	}

	return amf0.Data{orig[0], orig[1], meta}
}

// writeRawMessage writes an RTMP message with a Type 0 chunk header (always
// full 12-byte header), bypassing gortmplib's Type 1/2/3 optimization.
// This mirrors go2rtc's approach and is critical for strict RTMP receivers
// (e.g. Douyu Live Companion) that mis-parse Type 1/2/3 headers.
// The entire message (all chunks) is written in a single TCP write.
func (c *rtmpPublishConn) writeRawMessage(chunkID, msgType byte, timeMS uint32, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.setWriteDeadline()

	bodyLen := uint32(len(payload))
	cs := c.chunkSize
	if cs == 0 {
		cs = 4096
	}

	var buf []byte
	if bodyLen <= cs {
		// Single chunk: Type 0 header (12 bytes) + body
		buf = make([]byte, 12+bodyLen)
		buf[0] = chunkID // fmt=0, csid
		buf[1] = byte(timeMS >> 16)
		buf[2] = byte(timeMS >> 8)
		buf[3] = byte(timeMS)
		buf[4] = byte(bodyLen >> 16)
		buf[5] = byte(bodyLen >> 8)
		buf[6] = byte(bodyLen)
		buf[7] = msgType
		buf[8] = byte(c.streamID >> 24) // big-endian — matches gortmplib's
		buf[9] = byte(c.streamID >> 16) // chunk0.Marshal which strict FMS-
		buf[10] = byte(c.streamID >> 8) // compatible receivers (Douyu) expect.
		buf[11] = byte(c.streamID)      // (RTMP spec says little-endian, but
		copy(buf[12:], payload)         //  gortmplib/FFmpeg-in-practice use BE.)
	} else {
		// Multi-chunk: Type 0 first chunk + Type 3 continuations
		numChunks := (bodyLen + cs - 1) / cs
		buf = make([]byte, 12+bodyLen+(numChunks-1))
		buf[0] = chunkID
		buf[1] = byte(timeMS >> 16)
		buf[2] = byte(timeMS >> 8)
		buf[3] = byte(timeMS)
		buf[4] = byte(bodyLen >> 16)
		buf[5] = byte(bodyLen >> 8)
		buf[6] = byte(bodyLen)
		buf[7] = msgType
		buf[8] = byte(c.streamID >> 24)
		buf[9] = byte(c.streamID >> 16)
		buf[10] = byte(c.streamID >> 8)
		buf[11] = byte(c.streamID)
		copy(buf[12:12+cs], payload[:cs])
		off := uint32(12 + cs)
		for pos := cs; pos < bodyLen; pos += cs {
			end := pos + cs
			if end > bodyLen {
				end = bodyLen
			}
			buf[off] = 0xC0 | chunkID // fmt=3, same csid
			off++
			copy(buf[off:off+end-pos], payload[pos:end])
			off += end - pos
		}
	}

	_, err := c.bc.Write(buf)
	return err
}

func (c *rtmpPublishConn) BytesReceived() uint64 { return c.bc.Reader.Count() }
func (c *rtmpPublishConn) BytesSent() uint64     { return c.bc.Writer.Count() }

// RTMP digest-handshake constants (Adobe spec).
var (
	rtmpClientKeyC1 = []byte("Genuine Adobe Flash Player 001")
	rtmpServerKeyS1 = []byte("Genuine Adobe Flash Media Server 001")
	rtmpClientKeyC2 = append(append([]byte{}, []byte("Genuine Adobe Flash Player 001")...),
		0xf0, 0xee, 0xc2, 0x4a, 0x80, 0x68, 0xbe, 0xe8,
		0x2e, 0x00, 0xd0, 0xd1, 0x02, 0x9e, 0x7e, 0x57,
		0x6e, 0xec, 0x5d, 0x2d, 0x29, 0x80, 0x6f, 0xab,
		0x93, 0xb8, 0xe6, 0x36, 0xcf, 0xeb, 0x31, 0xae)
)

func rtmpHMAC(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

// buildC1WithDigest constructs a 1536-byte C1 packet with an embedded
// HMAC-SHA256 digest (complex handshake). This is required by Adobe FMS and
// FMS-compatible platforms (Douyu, Huya, Bilibili) that validate the C1
// digest even in plain RTMP (version=3) mode.
//
// The digest uses scheme 0 (digest position derived from Data[0:4]) with key
// "Genuine Adobe Flash Player 001". The 32-byte digest is computed over C1
// with the digest region excluded, then written into position.
//
// gortmplib's fillPlain() skips this digest — that's why native Go RTMP fails
// against Douyu. See: https://codebuddy.work/agents/share/... (root cause analysis).
func buildC1WithDigest() []byte {
	c1 := make([]byte, 1536)
	binary.BigEndian.PutUint32(c1[0:4], uint32(time.Now().Unix()))
	// c1[4:8] = version = 0 (already zero)
	if _, err := rand.Read(c1[8:]); err != nil {
		// Fallback: leave zeros (rare — rand.Read practically never fails)
		engineLogger.Warn("rand.Read failed for C1, using zeros", "err", err)
	}

	data := c1[8:] // 1528 bytes

	// Scheme 0: digest position = digestChunkPos1(=4) + sum(Data[0:4]) % 728
	digestPos := 4 + (int(data[0])+int(data[1])+int(data[2])+int(data[3]))%728

	// Build message = C1 with the 32-byte digest region excluded
	msg := make([]byte, 0, 1536-32)
	msg = append(msg, c1[:8]...)
	// Time + Version
	msg = append(msg, data[:digestPos]...)
	// Data before digest
	msg = append(msg, data[digestPos+32:]...)
	// Data after digest

	digest := rtmpHMAC(rtmpClientKeyC1, msg)
	copy(data[digestPos:digestPos+32], digest)

	return c1
}

// detectS1Digest checks whether S1 (1536 B) has an HMAC-SHA256 digest at
// either of the two standard positions. Returns the digest if found.
func detectS1Digest(s1 []byte) ([]byte, bool) {
	data := s1[8:] // 1528 bytes
	for _, pos := range []int{
		4 + (int(data[0])+int(data[1])+int(data[2])+int(data[3]))%728,
		768 + (int(data[764])+int(data[765])+int(data[766])+int(data[767]))%728,
	} {
		if pos+32 > len(data) {
			continue
		}
		digest := data[pos : pos+32]
		msg := make([]byte, 0, 1536-32)
		msg = append(msg, s1[:8]...)
		msg = append(msg, data[:pos]...)
		msg = append(msg, data[pos+32:]...)
		if bytes.Equal(digest, rtmpHMAC(rtmpServerKeyS1, msg)) {
			return digest, true
		}
	}
	return nil, false
}

// buildC2 constructs the 1536-byte C2 packet. If S1 contains a digest, C2
// carries a computed response digest (proving the client validated S1).
// Otherwise C2 is a plain echo of S1 (standard plain handshake).
func buildC2(s1 []byte) []byte {
	if s1Digest, ok := detectS1Digest(s1); ok {
		c2 := make([]byte, 1536) // Time/Time2 = 0, Data = random
		rand.Read(c2[8:])
		// C2 digest at Data offset 1496 (wire offset 1504).
		msg := make([]byte, 8+1496) // Time(0) + Time2(0) + Data[:1496]
		copy(msg[8:], c2[8:8+1496])
		key := rtmpHMAC(rtmpClientKeyC2, s1Digest)
		copy(c2[8+1496:], rtmpHMAC(key, msg))
		return c2
	}
	c2 := make([]byte, 1536)
	copy(c2, s1)
	return c2
}

// dialRTMPPublish dials an RTMP target, completes the plain handshake, sends a
// connect command (matching FFmpeg's field set), runs the FMLE publish sequence
// (releaseStream → FCPublish → createStream → publish), and returns a Conn
// ready for gortmplib.Writer. The returned cleanup function closes the
// underlying TCP connection.
// defaultRTMPPort sets the default RTMP (1935) or RTMPS (443) port on u.Host
// if no port is present.
func defaultRTMPPort(u *url.URL) {
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		if u.Scheme == "rtmps" {
			u.Host = net.JoinHostPort(u.Host, "443")
		} else {
			u.Host = net.JoinHostPort(u.Host, "1935")
		}
	}
}

func dialRTMPPublish(ctx context.Context, rawURL string, videoWidth, videoHeight int, videoFPS float64) (*rtmpPublishConn, func(), error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse rtmp url: %w", err)
	}
	if u.Scheme != "rtmp" && u.Scheme != "rtmps" {
		return nil, nil, fmt.Errorf("invalid rtmp scheme %q", u.Scheme)
	}
	defaultRTMPPort(u)

	// 1. TCP dial (with context).
	d := net.Dialer{}
	nconn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", u.Host, err)
	}
	cleanup := func() { _ = nconn.Close() }

	// TODO: TLS for RTMPS (plain RTMP only for now — companion/Douyu use rtmp://).

	// 2. Wrap with bytecounter + plain handshake.
	//    wrapDump is a no-op unless NVR_RTMP_DEBUG_DUMP names a file; it lets us
	//    capture every sent byte to diff against FFmpeg when debugging strict
	//    receivers (e.g. Douyu Live Companion RST after Publish.Start).
	bc := bytecounter.NewReadWriter(wrapDump(nconn))
	// Complex handshake: C1 contains an HMAC-SHA256 digest (key="Genuine Adobe
	// Flash Player 001", scheme 0). Adobe FMS and FMS-compatible platforms
	// (Douyu, Huya, Bilibili) validate this digest even in plain RTMP (v3) mode.
	// gortmplib's fillPlain() skips it — that's why native Go RTMP fails there.
	// C0+C1 are written together in a single TCP segment (matches FFmpeg).
	c1 := buildC1WithDigest()
	c0c1 := append([]byte{0x03}, c1...) // C0 (version 3) + C1
	if _, err := bc.Write(c0c1); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("send C0+C1: %w", err)
	}
	// Read S0 (1B) + S1 (1536B) + S2 (1536B).
	resp := make([]byte, 1+1536+1536)
	if _, err := io.ReadFull(bc, resp); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("read S0+S1+S2: %w", err)
	}
	if resp[0] != 3 {
		cleanup()
		return nil, nil, fmt.Errorf("unexpected server version %d", resp[0])
	}
	// C2 = digest-aware response (or plain echo if S1 has no digest).
	if _, err := bc.Write(buildC2(resp[1 : 1+1536])); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("send C2: %w", err)
	}

	// 3. Message-level read/writer on the same byte stream.
	mrw := message.NewReadWriter(bc, bc, false)
	conn := &rtmpPublishConn{
		nconn: nconn, mrw: mrw, bc: bc, chunkSize: 4096, streamID: 0x1000000,
		videoWidth: videoWidth, videoHeight: videoHeight, videoFPS: videoFPS,
	}

	app, streamKey := splitRTMPPath(u)
	if app == "" || streamKey == "" {
		cleanup()
		return nil, nil, fmt.Errorf("invalid rtmp path %q (expected /app/streamKey)", u.Path)
	}
	tcURL := getRTMPTcURL(u, app)

	// 4. Send SetChunkSize=4096 BEFORE connect (matches OBS/go2rtc). The server
	// must know our write chunk size before receiving any large messages.
	// go2rtc uses 4096 (comment: "OBS - 4096, Reolink - 4096"). Using 128
	// (RTMP default) causes ~400 chunks per keyframe vs ~13 at 4096, which can
	// overwhelm strict receivers.
	if err := mrw.Write(&message.SetChunkSize{Value: 4096}); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("send SetChunkSize: %w", err)
	}

	// 5. Connect command — field set EXACTLY matches FFmpeg (pcap-verified).
	// FFmpeg only sends 4 fields for publish mode: app, type, flashVer, tcUrl.
	// Extra fields (fpad/capabilities/audioCodecs/videoCodecs/videoFunction)
	// cause the Douyu Live Companion to RST after publish.
	connectArg := amf0.Object{
		{Key: "app", Value: app},
		{Key: "type", Value: "nonprivate"},
		{Key: "flashVer", Value: "FMLE/3.0 (compatible; Lavf61.7.103)"},
		{Key: "tcUrl", Value: tcURL},
	}
	if err := mrw.Write(&message.CommandAMF0{
		ChunkStreamID: 3,
		Name:          "connect",
		CommandID:     1,
		Arguments:     []any{connectArg},
	}); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("send connect: %w", err)
	}

	// Read connect response (_result or _error). Skip protocol control messages
	// (WindowAckSize, SetPeerBandwidth, UserControl) the server may send first.
	res, err := readRTMPCommandResult(mrw, 1)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("connect result: %w", err)
	}
	if res.Name == "_error" {
		cleanup()
		return nil, nil, fmt.Errorf("connect rejected by server: %v", res.Arguments)
	}

	// 6. FMLE-style publish: releaseStream + FCPublish (fire-and-forget — the
	// server's responses are consumed by the createStream read below).
	for i, name := range []string{"releaseStream", "FCPublish"} {
		if err := mrw.Write(&message.CommandAMF0{
			ChunkStreamID: 3,
			Name:          name,
			CommandID:     2 + i,
			Arguments:     []any{nil, streamKey},
		}); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("send %s: %w", name, err)
		}
	}

	// 7. createStream.
	if err := mrw.Write(&message.CommandAMF0{
		ChunkStreamID: 3,
		Name:          "createStream",
		CommandID:     4,
		Arguments:     []any{nil},
	}); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("send createStream: %w", err)
	}
	_, err = readRTMPCommandResult(mrw, 4)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("createStream result: %w", err)
	}
	// Note: we use hardcoded streamID=0x1000000 (FMLE convention) for all messages.
	// This matches what gortmplib uses for publish, ensuring consistency.

	// 8. publish.
	if err := mrw.Write(&message.CommandAMF0{
		ChunkStreamID:   4,
		MessageStreamID: 0x1000000,
		Name:            "publish",
		CommandID:       5,
		Arguments:       []any{nil, streamKey, app},
	}); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("send publish: %w", err)
	}
	if _, err := readRTMPStatus(mrw, 5); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("publish status: %w", err)
	}

	// Background reader goroutine: consume server messages (PingRequest,
	// WindowAckSize, onStatus, etc.) to prevent TCP receive buffer from filling.
	// Without this, the companion thinks we're unresponsive and RSTs the connection.
	// go2rtc does the same in its publish() function.
	go func() {
		for {
			if _, err := conn.Read(); err != nil {
				return
			}
		}
	}()

	return conn, cleanup, nil
}

// splitRTMPPath splits an RTMP URL path into (app, streamKey). The query string
// stays attached to streamKey (needed for auth params like Douyu's wsSecret).
// Mirrors gortmplib's internal splitPath but lives here so we don't fork it.
func splitRTMPPath(u *url.URL) (app, streamKey string) {
	// RequestURI() = path + "?" + rawQuery (keeps auth params on the last segment).
	uri := u.RequestURI()
	segs := strings.Split(uri, "/") // segs[0] == ""
	switch len(segs) {
	case 2:
		app = segs[1]
	case 3:
		app = segs[1]
		streamKey = segs[2]
	default:
		if len(segs) > 3 {
			app = strings.Join(segs[1:3], "/")
			streamKey = strings.Join(segs[3:], "/")
		}
	}
	return app, streamKey
}

// getRTMPTcURL builds the tcUrl for the connect command: scheme://host[:port]/app
// (no query string, no stream key).
func getRTMPTcURL(u *url.URL, app string) string {
	nu := *u
	nu.RawQuery = ""
	nu.Path = "/"
	// nu.String() = scheme://host[:port]/  → append app
	return strings.TrimSuffix(nu.String(), "/") + "/" + app
}

// readRTMPCommandResult reads messages until it finds a CommandAMF0 whose
// CommandID matches (or CommandID==0 with _result/_error name). Non-command
// messages (WindowAckSize, SetPeerBandwidth, UserControl, etc.) are silently
// consumed — this is how the server sends its post-connect control flow.
func readRTMPCommandResult(mrw *message.ReadWriter, commandID int) (*message.CommandAMF0, error) {
	for {
		msg, err := mrw.Read()
		if err != nil {
			return nil, err
		}
		if cmd, ok := msg.(*message.CommandAMF0); ok {
			if cmd.CommandID == commandID || (cmd.CommandID == 0 &&
				(cmd.Name == "_result" || cmd.Name == "_error")) {
				return cmd, nil
			}
		}
	}
}

func readRTMPStatus(mrw *message.ReadWriter, commandID int) (*message.CommandAMF0, error) {
	for {
		msg, err := mrw.Read()
		if err != nil {
			return nil, err
		}
		if cmd, ok := msg.(*message.CommandAMF0); ok {
			if cmd.CommandID == commandID || (cmd.CommandID == 0 && cmd.Name == "onStatus") {
				return cmd, nil
			}
		}
	}
}

// Compile-time assertion that rtmpPublishConn satisfies gortmplib.Conn.
var _ gortmplib.Conn = (*rtmpPublishConn)(nil)
