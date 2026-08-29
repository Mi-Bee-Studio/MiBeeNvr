package gb28181

import (
	"testing"

	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

// TestSessionManager_Invite_ReplacedSessionSendsBye locks the cross-talk fix:
// replacing a session for a channel must send a SIP BYE for the old dialog
// BEFORE its (recycled) port is handed to another channel — otherwise the old
// sender keeps streaming into the new tenant forever.
func TestSessionManager_Invite_ReplacedSessionSendsBye(t *testing.T) {
	pm := platform.NewPortManager(55300, 55310)
	sm := NewSessionManager(pm, "34020000001320000001")

	byeCount := 0
	sm.SetByeSender(func(channelID string) error {
		byeCount++
		return nil
	})

	channel := &Channel{DeviceID: "dev", ID: "chan", Name: "c"}
	channel.Status.Store(ChannelIdle)
	sdpOffer := []byte("v=0\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	_, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 0, byeCount, "first Invite must not BYE anything")

	// Replace the session (auto-INVITE / re-REGISTER path).
	_, err = sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, byeCount, "replacing a session must BYE the old dialog")
	require.NotNil(t, sm.GetReceiver(channel.ID), "new session's receiver must be live")

	_ = sm.Bye(channel.ID)
	require.Equal(t, 2, byeCount, "explicit Bye also BYEs")
}

// TestReceiver_DropsForeignSSRC locks the SSRC isolation fix: after the first
// packet latches the dialog's SSRC, packets from any other SSRC are dropped —
// a stale sender on a recycled port must not interleave into the byte stream.
func TestReceiver_DropsForeignSSRC(t *testing.T) {
	r := NewReceiver("cam", nil, nil)
	var aus []int
	r.AUCallback = func(au [][]byte, pts int64, isIDR bool) { aus = append(aus, len(au)) }

	mk := func(ssrc uint32, seq uint16, marker bool) *rtp.Packet {
		p := &rtp.Packet{}
		p.Header.SSRC = ssrc
		p.Header.SequenceNumber = seq
		p.Header.Marker = marker
		p.Payload = []byte{0x00, 0x00, 0x01, 0xBA} // pack header stub — enough for the latch path
		return p
	}

	// First packet latches SSRC 0x1111 (payload not a complete AU — fine).
	require.NoError(t, r.feedJitterBuffer(mk(0x1111, 1, false)))
	require.True(t, r.ssrcLatched.Load(), "SSRC must latch on first packet")
	require.Equal(t, uint32(0x1111), r.expectedSSRC.Load())

	// Foreign SSRC 0x2222 is dropped, not buffered.
	require.NoError(t, r.feedJitterBuffer(mk(0x2222, 2, true)))
	require.Equal(t, int64(1), r.rtpPacketsDropped.Load(), "foreign packet must be dropped")
	require.Equal(t, int64(1), r.foreignDrops.Load())

	// Same-SSRC packet flows through to the jitter buffer.
	require.NoError(t, r.feedJitterBuffer(mk(0x1111, 3, false)))
	require.Equal(t, int64(1), r.rtpPacketsDropped.Load(), "same-SSRC packet must not be dropped")
}

// TestManscdpDecode_RecordInfoQueryRoot verifies the Query-root RecordInfo
// arm: the upper platform's recording query (CmdType RecordInfo under <Query>)
// must decode as RecordInfoQuery — previously it failed to unmarshal (XMLName
// expected Response) so the cascade rejected recording queries with 400.
func TestManscdpDecode_RecordInfoQueryRoot(t *testing.T) {
	query := []byte(`<?xml version="1.0" encoding="GB2312"?>
<Query>
<CmdType>RecordInfo</CmdType>
<SN>12345</SN>
<DeviceID>34020000001320000003</DeviceID>
<StartTime>2026-08-16T00:00:00</StartTime>
<EndTime>2026-08-16T17:00:00</EndTime>
<Type>all</Type>
</Query>`)
	cmd, payload, err := manscdp.Decode(query)
	require.NoError(t, err, "Query-root RecordInfo must decode")
	require.Equal(t, manscdp.CmdRecordInfo, cmd)
	q, ok := payload.(manscdp.RecordInfoQuery)
	require.True(t, ok, "payload must be RecordInfoQuery, got %T", payload)
	require.Equal(t, 12345, q.SN)
	require.Equal(t, "34020000001320000003", q.DeviceID)
	require.Equal(t, "2026-08-16T00:00:00", q.StartTime)
	require.Equal(t, "2026-08-16T17:00:00", q.EndTime)

	// Response-root RecordInfo still decodes as the device answer.
	resp := []byte(`<?xml version="1.0"?>
<Response>
<CmdType>RecordInfo</CmdType>
<SN>12345</SN>
<DeviceID>34020000001320000003</DeviceID>
<SumNum>1</SumNum>
<RecordList Num="1">
<Item>
<DeviceID>34020000001320000003</DeviceID>
<StartTime>2026-08-16T01:00:00</StartTime>
<EndTime>2026-08-16T01:10:00</EndTime>
</Item>
</RecordList>
</Response>`)
	cmd2, payload2, err := manscdp.Decode(resp)
	require.NoError(t, err)
	require.Equal(t, manscdp.CmdRecordInfo, cmd2)
	ri, ok := payload2.(manscdp.RecordInfo)
	require.True(t, ok, "Response-root payload must stay RecordInfo, got %T", payload2)
	require.Equal(t, 1, ri.SumNum)
	require.Len(t, ri.RecordList, 1)
}

// TestSplitAUsByFrame locks the multi-frame-AU fix: NALUs from two
// concatenated frames (lost RTP marker upstream) must split back into
// per-frame access units; single frames and parameter-set attachment must be
// preserved.
func TestSplitAUsByFrame(t *testing.T) {
	// H.264 slice NAL: header 0x41 (type 1, ref). first_mb_in_slice=0 →
	// first RBSP byte top bit set (0x88). Non-first slices use 0x08 (top bit 0).
	sps := []byte{0x67, 0x42, 0x80}
	pps := []byte{0x68, 0xCE, 0x3C}
	idr := []byte{0x65, 0x88, 0x01} // type 5, first_mb=0
	p1a := []byte{0x41, 0x88, 0x02} // type 1, first_mb=0 → new frame
	p1b := []byte{0x41, 0x08, 0x03} // type 1, first_mb>0 → continuation slice
	p2 := []byte{0x41, 0x88, 0x04}  // type 1, first_mb=0 → next frame
	sei := []byte{0x06, 0x01, 0x02}

	// Single frame: SPS+PPS+IDR stays together.
	got := splitAUsByFrame([][]byte{sps, pps, idr}, false)
	require.Len(t, got, 1)
	require.Equal(t, [][]byte{sps, pps, idr}, got[0])

	// Two frames merged into one NALU list → split at the second first_mb=0 VCL.
	got = splitAUsByFrame([][]byte{p1a, p1b, p2}, false)
	require.Len(t, got, 2, "two frames must split")
	require.Equal(t, [][]byte{p1a, p1b}, got[0])
	require.Equal(t, [][]byte{p2}, got[1])

	// Non-VCL NALs (SEI) attach to the FOLLOWING frame.
	got = splitAUsByFrame([][]byte{p1a, sei, p2}, false)
	require.Len(t, got, 2)
	require.Equal(t, [][]byte{p1a}, got[0])
	require.Equal(t, [][]byte{sei, p2}, got[1])

	// H.265: first bit after the 2-byte NAL header = first_slice_segment flag.
	h265slice1 := []byte{0x02, 0x01, 0x80, 0x00}  // type 1 (TRAIL_R), new pic
	h265slice1b := []byte{0x02, 0x01, 0x00, 0x00} // continuation segment
	h265slice2 := []byte{0x02, 0x01, 0x80, 0x01}  // new pic
	got = splitAUsByFrame([][]byte{h265slice1, h265slice1b, h265slice2}, true)
	require.Len(t, got, 2)
	require.Equal(t, [][]byte{h265slice1, h265slice1b}, got[0])
	require.Equal(t, [][]byte{h265slice2}, got[1])

	// Degenerate inputs.
	require.Nil(t, splitAUsByFrame(nil, false))
	require.Len(t, splitAUsByFrame([][]byte{sei}, false), 1)
}
