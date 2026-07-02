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
	nconn     net.Conn
	mrw       *message.ReadWriter
	bc        *bytecounter.ReadWriter
	wmu       sync.Mutex // protects concurrent Write (video frames + PingResponse)
	streamID  uint32     // message stream ID (0x1000000 = FMLE convention)
	chunkSize uint32     // write chunk size (default 128)
}

func (c *rtmpPublishConn) Read() (message.Message, error) { return c.mrw.Read() }
func (c *rtmpPublishConn) Write(msg message.Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.mrw.Write(msg)
}

// writeRawMessage writes an RTMP message with a Type 0 chunk header (always
// full 12-byte header), bypassing gortmplib's Type 1/2/3 optimization.
// This mirrors go2rtc's approach and is critical for strict RTMP receivers
// (e.g. Douyu Live Companion) that mis-parse Type 1/2/3 headers.
// The entire message (all chunks) is written in a single TCP write.
func (c *rtmpPublishConn) writeRawMessage(chunkID, msgType byte, timeMS uint32, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

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
		buf[8] = byte(c.streamID)       // little-endian per RTMP spec
		buf[9] = byte(c.streamID >> 8)
		buf[10] = byte(c.streamID >> 16)
		buf[11] = byte(c.streamID >> 24)
		copy(buf[12:], payload)
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
		buf[8] = byte(c.streamID)
		buf[9] = byte(c.streamID >> 8)
		buf[10] = byte(c.streamID >> 16)
		buf[11] = byte(c.streamID >> 24)
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

// writeAVCSequenceHeader writes the H.264 AVC sequence header (SPS/PPS)
// using a Type 0 chunk header. Includes High Profile extension fields.
func (c *rtmpPublishConn) writeAVCSequenceHeader(sps, pps []byte) error {
	if len(sps) < 4 || len(pps) == 0 {
		return fmt.Errorf("invalid sps/pps")
	}
	profile, compat, level := sps[1], sps[2], sps[3]

	// Build AVCC configuration record
	avcc := []byte{
		0x01,             // configurationVersion
		profile,          // AVCProfileIndication
		compat,           // profile_compatibility
		level,            // AVCLevelIndication
		0xFF,             // reserved(6) + lengthSizeMinusOne(2) = 4-byte NALU length
		0xE1,             // reserved(3) + numSPS(5) = 1
		byte(len(sps) >> 8), byte(len(sps)), // SPS length
	}
	avcc = append(avcc, sps...)
	avcc = append(avcc, 0x01) // numPPS = 1
	avcc = append(avcc, byte(len(pps)>>8), byte(len(pps)))
	avcc = append(avcc, pps...)
	// High Profile extension (profile_idc != 66/77/88)
	if profile != 66 && profile != 77 && profile != 88 {
		avcc = append(avcc,
			0xFC|1, // reserved(6) + chroma_format_idc = 1 (4:2:0)
			0xF8,   // reserved(5) + bit_depth_luma_minus8 = 0
			0xF8,   // reserved(5) + bit_depth_chroma_minus8 = 0
			0x00,   // numOfSequenceParameterSetExt = 0
		)
	}

	// Build FLV video tag body: keyframe+H264 + config type + compTime(0) + AVCC
	flvBody := make([]byte, 5+len(avcc))
	flvBody[0] = 0x17 // keyframe(0x10) | H264(0x07)
	flvBody[1] = 0x00 // AVCPacketType = sequence header
	// flvBody[2..4] = compositionTime = 0
copy(flvBody[5:], avcc)

	engineLogger.Info("rtmp raw AVC header",
		"flvBody_len", len(flvBody), "sps_len", len(sps), "pps_len", len(pps),
		"hex", fmt.Sprintf("%x", flvBody[:min(40, len(flvBody))]))


return c.writeRawMessage(4, 0x09, 0, flvBody) // csid=6, type=video, ts=0
}

// writeMetadata sends @setDataFrame via raw Type 0 header.
// Encodes AMF0 manually to bypass gortmplib's Type 1/2/3 chunk optimization.
func (c *rtmpPublishConn) writeMetadata() error {
	data := amf0.Data{
		"@setDataFrame",
		"onMetaData",
		amf0.Object{
			{Key: "videocodecid", Value: float64(7)}, // H.264
			{Key: "videodatarate", Value: float64(0)},
		},
	}
	payload, err := data.Marshal()
	if err != nil {
		return err
	}
	return c.writeRawMessage(4, 0x12, 0, payload) // csid=4, type=DataAMF0(18), ts=0
}

// writeVideoFrame writes an H.264 video frame using a Type 0 chunk header.
// au is the access unit (slice of NALUs). NALUs are AVCC-encoded (4-byte length prefix).
func (c *rtmpPublishConn) writeVideoFrame(timeMS uint32, isKeyFrame bool, au [][]byte) error {
	// AVCC-encode the NALUs
	var avccData []byte
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		lenBytes := []byte{
			byte(len(nalu) >> 24),
			byte(len(nalu) >> 16),
			byte(len(nalu) >> 8),
			byte(len(nalu)),
		}
		avccData = append(avccData, lenBytes...)
		avccData = append(avccData, nalu...)
	}

	frameType := byte(0x20) // inter
	if isKeyFrame {
		frameType = 0x10 // keyframe
	}

	// FLV video tag body
	flvBody := make([]byte, 5+len(avccData))
	flvBody[0] = frameType | 0x07 // frameType | H264
	flvBody[1] = 0x01              // AVCPacketType = NALU
	// compositionTime = 0 (pts == dts for this source)
	copy(flvBody[5:], avccData)

	return c.writeRawMessage(4, 0x09, timeMS, flvBody)
}

// writeHandcraftedHighProfileSeqHeader writes the AVC sequence header
// directly to the underlying socket, bypassing gortmplib's Writer and
// go-mp4's marshal. Matches FFmpeg/libavformat's AVCC byte layout for
// High profile exactly (reserved bits = 0x3F / 0x1F).
func (c *rtmpPublishConn) writeHandcraftedHighProfileSeqHeader(sps, pps []byte, streamID uint32) error {
	profile, compat, level := sps[1], sps[2], sps[3]
	// FLV body (5) + AVCC ver/profile/compat/level (4) +
	// reserved+numSPS (2) + SPS-len (2) + SPS + numPPS (1) + PPS-len (2) + PPS +
	// High-prof ext (4).
	bodyLen := 5 + 4 + 2 + 2 + len(sps) + 1 + 2 + len(pps) + 4
	// RTMP chunk header: fmt=0 csid=6, ts=0, msg_type=9 (video), sid.
	chunk := make([]byte, 12+bodyLen)
	chunk[0] = 0x06 // basic byte: fmt=0, csid=6 (VideoChunkStreamID)
	// chunk[1..3] = timestamp = 0
	chunk[4] = byte((bodyLen >> 16) & 0xFF)
	chunk[5] = byte((bodyLen >> 8) & 0xFF)
	chunk[6] = byte(bodyLen & 0xFF)
	chunk[7] = 0x09 // msg_type = video
	chunk[8] = byte((streamID >> 24) & 0xFF)
	chunk[9] = byte((streamID >> 16) & 0xFF)
	chunk[10] = byte((streamID >> 8) & 0xFF)
	chunk[11] = byte(streamID & 0xFF)

	body := chunk[12:]
	body[0] = 0x17 // keyframe + AVC
	body[1] = 0x00 // AVC seq header
	// body[2..4] = compositionTime = 0
	body[5] = 0x01 // AVCC configurationVersion
	body[6] = profile
	body[7] = compat
	body[8] = level
	body[9] = 0xFF // 6 reserved bits (0x3F) + lengthSizeMinusOne=3 (4-byte NALU len)
	body[10] = 0xE1 // 3 reserved bits (7) + numSPS=1
	body[11] = byte((len(sps) >> 8) & 0xFF)
	body[12] = byte(len(sps) & 0xFF)
	off := 13 + copy(body[13:], sps)
	body[off] = 0x01 // numPPS=1
	body[off+1] = byte((len(pps) >> 8) & 0xFF)
	body[off+2] = byte(len(pps) & 0xFF)
	off += 3 + copy(body[off+3:], pps)
	body[off] = 0xFD // 6 reserved bits (0x3F) + chroma_format_idc=1 (4:2:0)
	body[off+1] = 0xF8 // 5 reserved bits (0x1F) + bit_depth_luma_minus8=0
	body[off+2] = 0xF8 // 5 reserved bits (0x1F) + bit_depth_chroma_minus8=0
	body[off+3] = 0x00 // numOfSequenceParameterSetExt=0

	if _, err := c.bc.Write(chunk); err != nil {
		return fmt.Errorf("write handcrafted AVC seq header: %w", err)
	}
	engineLogger.Info("wrote handcrafted High-Profile AVC seq header",
		"profile_idc", profile, "sps_len", len(sps), "pps_len", len(pps))
	return nil
}
func (c *rtmpPublishConn) BytesReceived() uint64           { return c.bc.Reader.Count() }
func (c *rtmpPublishConn) BytesSent() uint64               { return c.bc.Writer.Count() }


// RTMP digest-handshake constants (Adobe spec).
var (
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

// detectS1Digest checks whether S1 (1536 B) has an HMAC-SHA256 digest at
// either of the two standard positions. Returns the digest if found.
func detectS1Digest(s1 []byte) ([]byte, bool) {
	data := s1[8:] // 1528 bytes
	for _, pos := range []int{
		4 + (int(data[0])+int(data[1])+int(data[2])+int(data[3])) % 728,
		768 + (int(data[764])+int(data[765])+int(data[766])+int(data[767])) % 728,
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

func dialRTMPPublish(ctx context.Context, rawURL string) (*rtmpPublishConn, func(), error) {
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
	bc := bytecounter.NewReadWriter(nconn)
	// Custom plain RTMP handshake: C0+C1 written together (single TCP segment,
	// like FFmpeg), C1.Time = current time (gortmplib's fillPlain leaves it 0,
	// which some receivers reject). C2 = echo of S1 (standard plain handshake).
	c1 := make([]byte, 1536)
	binary.BigEndian.PutUint32(c1[0:4], uint32(time.Now().Unix()))
	// c1[4:8] = version = 0 (already zero)
	if _, err := rand.Read(c1[8:]); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("handshake rand: %w", err)
	}
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
	conn := &rtmpPublishConn{nconn: nconn, mrw: mrw, bc: bc, chunkSize: 128, streamID: 0x1000000}


	app, streamKey := splitRTMPPath(u)
	if app == "" || streamKey == "" {
		cleanup()
		return nil, nil, fmt.Errorf("invalid rtmp path %q (expected /app/streamKey)", u.Path)
	}
	tcURL := getRTMPTcURL(u, app)

	// 4. NOTE: do NOT send SetChunkSize before connect. The writer updates its
	// internal chunk size on SetChunkSize write, so the connect would be sent as
	// one >128B chunk. Some receivers (Douyu Live Companion) ignore the client's
	// SetChunkSize and parse at the default 128B, mis-reading the large chunk.
	// Keep the default 128B chunking for connect (matches FFmpeg).
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

	// Send SetChunkSize=128 (keep default). Some companions ignore client-side
	// SetChunkSize and always parse at 128 bytes. Using 128 ensures compatibility.
	if err := mrw.Write(&message.SetChunkSize{Value: 128}); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("send SetChunkSize: %w", err)
	}

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
	deadline := time.Now().Add(15 * time.Second)
	for {
		if remaining := time.Until(deadline); remaining > 0 {
			// Best-effort read deadline — the underlying conn may not support it.
		}
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
		engineLogger.Info("rtmp handshake msg from server",
			"type", fmt.Sprintf("%T", msg), "msg", fmt.Sprintf("%+v", msg))
		if cmd, ok := msg.(*message.CommandAMF0); ok {
			if cmd.CommandID == commandID || (cmd.CommandID == 0 && cmd.Name == "onStatus") {
				return cmd, nil
			}
		}
	}
}

// Compile-time assertion that rtmpPublishConn satisfies gortmplib.Conn.
var _ gortmplib.Conn = (*rtmpPublishConn)(nil)
