package psmux

import (
	"bytes"
	"net"
	"testing"

	gb "github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/stretchr/testify/require"
)

// round-trip: muxer output fed through the platform-side PSDemuxer must
// reproduce the original NALUs and audio frames — the strongest interop
// guarantee available without a third-party platform in CI.
func TestRoundTripVideoH264(t *testing.T) {
	m := New()
	m.SetVideoCodec("h264")
	m.SetAudioCodec("g711u")

	idr := []byte{
		0, 0, 0, 1, 0x67, 0x42, 0x00, 0x1E, // SPS
		0, 0, 0, 1, 0x68, 0xCE, 0x38, 0x80, // PPS
		0, 0, 0, 1, 0x65, 0x88, 0x84, 0x00, 0x01, 0x23, 0x45, // IDR
	}
	pFrame := append([]byte{0, 0, 0, 1, 0x41, 0x9A}, bytes.Repeat([]byte{0x33}, 300)...)

	d := gb.NewPSDemuxer()

	ps := m.WriteAU(idr, 90000, true)
	nalus, err := d.FeedAU(ps, 9000, true)
	require.NoError(t, err)
	require.Len(t, nalus, 3, "SPS/PPS/IDR extracted: %d", len(nalus))
	require.Equal(t, idr[4:8], nalus[0])
	require.Equal(t, idr[12:16], nalus[1])
	require.Equal(t, idr[20:], nalus[2])

	audio := bytes.Repeat([]byte{0xFF, 0x7F}, 80) // μ-law-ish quiet frame
	ps = m.WriteAudio(audio, 90160)
	_, err = d.FeedAU(ps, 9160, true)
	require.NoError(t, err)
	frames := d.DrainAudio()
	require.Len(t, frames, 1)
	require.Equal(t, gb.AudioCodecG711U, frames[0].Codec)
	require.Equal(t, audio, frames[0].Data)

	ps = m.WriteAU(pFrame, 93000, false)
	nalus, err = d.FeedAU(ps, 9300, true)
	require.NoError(t, err)
	require.Len(t, nalus, 1)
	require.Equal(t, pFrame[4:], nalus[0])
}

func TestRoundTripVideoH265LargeAU(t *testing.T) {
	m := New()
	m.SetVideoCodec("h265")
	d := gb.NewPSDemuxer()

	// >60KB AU forces PES continuation packets.
	big := append([]byte{0, 0, 0, 1, 0x40, 0x01, 0x01, 0x01, 0x00}, // VPS
		append([]byte{0, 0, 0, 1, 0x42, 0x01, 0x01}, bytes.Repeat([]byte{0xAB}, 70000)...)...) // SPS-ish big NAL
	ps := m.WriteAU(big, 90000, true)
	nalus, err := d.FeedAU(ps, 9000, true)
	require.NoError(t, err)
	require.Len(t, nalus, 2)
	require.Equal(t, big[4:9], nalus[0])
	require.Equal(t, big[13:], nalus[1])
}

func TestRoundTripAudioOnlyAfterPSM(t *testing.T) {
	// PSM sent once (IDR) must keep declaring audio for subsequent AUs that
	// carry only audio — mirrors the gbsim discipline.
	m := New()
	m.SetVideoCodec("h264")
	m.SetAudioCodec("g711a")
	d := gb.NewPSDemuxer()

	_, err := d.FeedAU(m.WriteAU([]byte{0, 0, 0, 1, 0x67, 0x42}, 90000, true), 9000, true)
	require.NoError(t, err)

	payload := bytes.Repeat([]byte{0x55}, 160)
	_, err = d.FeedAU(m.WriteAudio(payload, 90160), 9160, true)
	require.NoError(t, err)
	frames := d.DrainAudio()
	require.Len(t, frames, 1)
	require.Equal(t, gb.AudioCodecG711A, frames[0].Codec)
	require.Equal(t, payload, frames[0].Data)
}

func TestPSMCRC(t *testing.T) {
	// CRC-32/MPEG-2 standard check value: "123456789" → 0x0376E6E7.
	require.EqualValues(t, 0x0376E6E7, crc32MPEG([]byte("123456789")))
}

func TestRTPPacketizerFragmentation(t *testing.T) {
	rx, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rx.Close() })
	dst := rx.LocalAddr().(*net.UDPAddr)

	tx, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Close() })

	p := NewRTPPacketizer(tx, dst, 0x02000009, 7)
	payload := bytes.Repeat([]byte{0x5A}, RTPMTU*2+300) // 3 fragments
	require.NoError(t, p.Send(payload, 123456))

	var got [][]byte
	var markers []bool
	var seqs []uint16
	buf := make([]byte, 2048)
	for range 3 {
		n, err := rx.Read(buf)
		require.NoError(t, err)
		pkt := buf[:n]
		require.EqualValues(t, 2, pkt[0]>>6)
		require.EqualValues(t, 96, pkt[1]&0x7F)
		markers = append(markers, pkt[1]&0x80 != 0)
		seqs = append(seqs, uint16(pkt[2])<<8|uint16(pkt[3]))
		require.EqualValues(t, 123456, uint32(pkt[4])<<24|uint32(pkt[5])<<16|uint32(pkt[6])<<8|uint32(pkt[7]))
		require.EqualValues(t, 0x02000009, uint32(pkt[8])<<24|uint32(pkt[9])<<16|uint32(pkt[10])<<8|uint32(pkt[11]))
		got = append(got, append([]byte(nil), pkt[12:]...))
	}
	require.Equal(t, []bool{false, false, true}, markers)
	require.Equal(t, []uint16{7, 8, 9}, seqs)
	var joined []byte
	for _, f := range got {
		joined = append(joined, f...)
	}
	require.Equal(t, payload, joined)
	require.EqualValues(t, 3, p.Sent())
}
