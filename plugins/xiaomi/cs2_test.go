// SPDX-License-Identifier: MIT
//
// Xiaomi CS2 P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package xiaomi

import (
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCS2MarshalCmd(t *testing.T) {
payload := []byte{0xAA, 0xBB, 0xCC}
	cmd := uint32(0x12345678)
	seq := uint16(0x00AB)
	channel := byte(0)

	result := cs2MarshalCmd(channel, seq, cmd, payload)

	// Total size: 4 (msg header) + 4 (drw header) + 4 (payload size) + 4 (cmd) + 3 (payload) = 19
	expectedLen := 4 + 4 + 4 + 4 + len(payload)
	require.Len(t, result, expectedLen)

	// 1. Message header: magic, msgDrw, size
	require.Equal(t, byte(cs2Magic), result[0])
	require.Equal(t, byte(cs2MsgDrw), result[1])
	require.Equal(t, uint16(4+4+4+len(payload)), binary.BigEndian.Uint16(result[2:]))

	// 2. DRW header
	require.Equal(t, byte(cs2MagicDrw), result[4])
	require.Equal(t, channel, result[5])
	require.Equal(t, seq, binary.BigEndian.Uint16(result[6:]))

	// 3. Payload size (4 + payload length)
	require.Equal(t, uint32(4+len(payload)), binary.BigEndian.Uint32(result[8:]))

	// 4. Command
	require.Equal(t, cmd, binary.BigEndian.Uint32(result[12:]))

	// 5. Payload
	require.Equal(t, payload, result[16:])
}

func TestCS2MarshalCmdEmptyPayload(t *testing.T) {
result := cs2MarshalCmd(0, 1, 0x99, nil)

	// Total: 4 + 4 + 4 + 4 + 0 = 16
	require.Len(t, result, 16)
	require.Equal(t, byte(cs2Magic), result[0])
	require.Equal(t, byte(cs2MsgDrw), result[1])
	require.Equal(t, uint16(12), binary.BigEndian.Uint16(result[2:]))
	require.Equal(t, uint32(0x99), binary.BigEndian.Uint32(result[12:]))
}

func TestCS2DataChannelPushPop(t *testing.T) {
ch := newCS2DataChannel(0, 10)

	// Push data with 4-byte big-endian size prefix
	data := []byte("hello")
	sizeBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(sizeBuf, uint32(len(data)))

	err := ch.Push(append(sizeBuf, data...))
	require.NoError(t, err)

	got, ok := ch.Pop()
	require.True(t, ok)
	require.Equal(t, data, got)
}

func TestCS2DataChannelPushMultipleInOnePacket(t *testing.T) {
ch := newCS2DataChannel(0, 10)

	// Two messages in one push: "abc" and "defgh"
	msg1 := []byte("abc")
	msg2 := []byte("defgh")

	buf := make([]byte, 0, 4+len(msg1)+4+len(msg2))
	size1 := make([]byte, 4)
	binary.BigEndian.PutUint32(size1, uint32(len(msg1)))
	buf = append(buf, size1...)
	buf = append(buf, msg1...)

	size2 := make([]byte, 4)
	binary.BigEndian.PutUint32(size2, uint32(len(msg2)))
	buf = append(buf, size2...)
	buf = append(buf, msg2...)

	err := ch.Push(buf)
	require.NoError(t, err)

	got1, ok := ch.Pop()
	require.True(t, ok)
	require.Equal(t, msg1, got1)

	got2, ok := ch.Pop()
	require.True(t, ok)
	require.Equal(t, msg2, got2)
}

func TestCS2DataChannelPushSeqInOrder(t *testing.T) {
ch := newCS2DataChannel(10, 100)

	data1 := makeDataWithSize("first")
	data2 := makeDataWithSize("second")

	// Push seq 0 (waitSeq starts at 0)
	pushed, err := ch.PushSeq(0, data1)
	require.NoError(t, err)
	require.Equal(t, 1, pushed)

	// Push seq 1
	pushed, err = ch.PushSeq(1, data2)
	require.NoError(t, err)
	require.Equal(t, 1, pushed)

	got1, ok := ch.Pop()
	require.True(t, ok)
	require.Equal(t, []byte("first"), got1)

	got2, ok := ch.Pop()
	require.True(t, ok)
	require.Equal(t, []byte("second"), got2)
}

func TestCS2DataChannelPushSeqOutOfOrder(t *testing.T) {
ch := newCS2DataChannel(10, 100)

	data0 := makeDataWithSize("zero")
	data1 := makeDataWithSize("one")

	// Push seq 1 first (out of order) — should be buffered
	pushed, err := ch.PushSeq(1, data1)
	require.NoError(t, err)
	require.Equal(t, 0, pushed) // saved to buffer, not processed

	// Push seq 0 — should process both 0 and 1
	pushed, err = ch.PushSeq(0, data0)
	require.NoError(t, err)
	require.Equal(t, 2, pushed) // processed both seq 0 and seq 1

	got0, ok := ch.Pop()
	require.True(t, ok)
	require.Equal(t, []byte("zero"), got0)

	got1, ok := ch.Pop()
	require.True(t, ok)
	require.Equal(t, []byte("one"), got1)
}

func TestCS2DataChannelPushSeqDuplicate(t *testing.T) {
ch := newCS2DataChannel(10, 100)

	data := makeDataWithSize("hello")

	// Push seq 0
	pushed, err := ch.PushSeq(0, data)
	require.NoError(t, err)
	require.Equal(t, 1, pushed)

	// Push seq 0 again (from the past)
	pushed, err = ch.PushSeq(0, data)
	require.NoError(t, err)
	require.Equal(t, 0, pushed) // already processed, ignored
}

func TestCS2DataChannelPushSeqBufferFull(t *testing.T) {
ch := newCS2DataChannel(2, 100) // small push buffer

	// Push future seq 1
	pushed, err := ch.PushSeq(1, []byte("a"))
	require.NoError(t, err)
	require.Equal(t, 0, pushed)

	// Push future seq 2
	pushed, err = ch.PushSeq(2, []byte("b"))
	require.NoError(t, err)
	require.Equal(t, 0, pushed)

	// Push future seq 3 — buffer full
	pushed, err = ch.PushSeq(3, []byte("c"))
	require.NoError(t, err)
	require.Equal(t, -1, pushed) // couldn't save
}

func TestCS2DataChannelPushSeqNoBuffer(t *testing.T) {
ch := newCS2DataChannel(0, 100) // pushSize=0, no reorder buffer

	// Future seq can't be saved
	pushed, err := ch.PushSeq(5, []byte("future"))
	require.NoError(t, err)
	require.Equal(t, -1, pushed)
}

func TestCS2DataChannelClose(t *testing.T) {
ch := newCS2DataChannel(0, 10)
	ch.Close()

	_, ok := ch.Pop()
	require.False(t, ok)
}

func TestCS2DataChannelPopBufferFull(t *testing.T) {
ch := newCS2DataChannel(0, 1) // pop buffer size 1

	// Push one message
	sizeBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(sizeBuf, 3)
	err := ch.Push(append(sizeBuf, "abc"...))
	require.NoError(t, err)

	// Push another — pop buffer full
	err = ch.Push(append(sizeBuf, "def"...))
	require.Equal(t, err.Error(), "cs2: pop buffer is full")
}

func TestCS2ConnStructFields(t *testing.T) {
// Verify struct can be initialized (no actual network connection needed)
	c := &CS2Conn{
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
	}

	require.False(t, c.isTCP)
	require.Equal(t, "cs2+udp", c.Protocol())
	require.Equal(t, "CS2", c.Version())
	require.Equal(t, io.EOF, c.Error()) // no error set, should return EOF

	// TCP variant
	c.isTCP = true
	require.Equal(t, "cs2+tcp", c.Protocol())
}

func TestCS2MarshalCmdChannelByte(t *testing.T) {
// Test with different channel values
	for _, ch := range []byte{0, 1, 2, 3} {
		result := cs2MarshalCmd(ch, 0, 0x01, nil)
		require.Equal(t, ch, result[5])
	}
}

func TestCS2MarshalCmdSeqIncrement(t *testing.T) {
result0 := cs2MarshalCmd(0, 0, 0x01, nil)
	result1 := cs2MarshalCmd(0, 1, 0x01, nil)

	require.Equal(t, uint16(0), binary.BigEndian.Uint16(result0[6:]))
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(result1[6:]))
}

// makeDataWithSize creates a CS2 data payload with a 4-byte big-endian size prefix.
func makeDataWithSize(s string) []byte {
	data := []byte(s)
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf, uint32(len(data)))
	copy(buf[4:], data)
	return buf
}
