// SPDX-License-Identifier: MIT
//
// TUTK P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package tutk

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestICAM(t *testing.T) {
	t.Helper()
	cmd := uint32(0xD807FF00)
	result := ICAM(cmd, 0x01, 0x01)

	// Should start with "ICAM" magic
	require.Equal(t, "ICAM", string(result[:4]))

	// Command should be stored as LE uint32 at offset 4
	storedCmd := binary.LittleEndian.Uint32(result[4:])
	require.Equal(t, cmd, storedCmd)

	// Args count should be at offset 15
	require.Equal(t, byte(2), result[15])

	// Args should be at offset 23
	require.Equal(t, byte(0x01), result[23])
	require.Equal(t, byte(0x01), result[24])

	// Total length should be 23 + len(args)
	require.Len(t, result, 23+2)
}

func TestICAMNoArgs(t *testing.T) {
	t.Helper()
	result := ICAM(0x00000000)

	require.Equal(t, "ICAM", string(result[:4]))
	require.Equal(t, byte(0), result[15])
	require.Len(t, result, 23)
}

func TestICAMMultipleArgs(t *testing.T) {
	t.Helper()
	result := ICAM(0x12345678, 0xAA, 0xBB, 0xCC)

	require.Equal(t, "ICAM", string(result[:4]))
	cmd := binary.LittleEndian.Uint32(result[4:])
	require.Equal(t, uint32(0x12345678), cmd)
	require.Equal(t, byte(3), result[15])
	require.Equal(t, byte(0xAA), result[23])
	require.Equal(t, byte(0xBB), result[24])
	require.Equal(t, byte(0xCC), result[25])
	require.Len(t, result, 23+3)
}

func TestHL(t *testing.T) {
	t.Helper()
	cmdID := uint16(0x0601)
	payload := []byte("test payload data")
	result := HL(cmdID, payload)

	// Should start with "HL" magic
	require.Equal(t, "HL", string(result[:2]))

	// Version should be 5 at offset 2
	require.Equal(t, byte(5), result[2])

	// CmdID should be stored as LE uint16 at offset 4
	storedCmd := binary.LittleEndian.Uint16(result[4:])
	require.Equal(t, cmdID, storedCmd)

	// Payload length should be stored as LE uint16 at offset 6
	storedLen := binary.LittleEndian.Uint16(result[6:])
	require.Equal(t, uint16(len(payload)), storedLen)

	// Payload should be at offset 16
	require.Equal(t, payload, result[16:])

	// Total length should be 16 + len(payload)
	require.Len(t, result, 16+len(payload))
}

func TestHLEmptyPayload(t *testing.T) {
	t.Helper()
	result := HL(0x0000, []byte{})

	require.Equal(t, "HL", string(result[:2]))
	require.Equal(t, byte(5), result[2])
	storedLen := binary.LittleEndian.Uint16(result[6:])
	require.Equal(t, uint16(0), storedLen)
	require.Len(t, result, 16)
}

func TestParseHL(t *testing.T) {
	t.Helper()
	payload := []byte("hello world")
	hl := HL(0x1234, payload)

	cmdID, parsedPayload, ok := ParseHL(hl)
	require.True(t, ok)
	require.Equal(t, uint16(0x1234), cmdID)
	require.Equal(t, payload, parsedPayload)
}

func TestParseHLInvalid(t *testing.T) {
	t.Helper()
	// Too short
	_, _, ok := ParseHL([]byte("short"))
	require.False(t, ok)

	// Wrong magic
	_, _, ok = ParseHL([]byte("XX\x05\x00\x34\x12\x0b\x00" + string(make([]byte, 8)) + "hello world"))
	require.False(t, ok)

	// Empty
	_, _, ok = ParseHL([]byte{})
	require.False(t, ok)
}

func TestParseHLPartialPayload(t *testing.T) {
	t.Helper()
	// HL with payload length 100 but only contains 5 bytes of actual payload
	partial := make([]byte, 16+5)
	copy(partial, "HL")
	partial[2] = 5
	binary.LittleEndian.PutUint16(partial[4:], 0x0601)
	binary.LittleEndian.PutUint16(partial[6:], 100) // claims 100 bytes
	copy(partial[16:], "short")

	cmdID, payload, ok := ParseHL(partial)
	require.True(t, ok)
	require.Equal(t, uint16(0x0601), cmdID)
	require.Equal(t, []byte("short"), payload)
}

func TestFindHL(t *testing.T) {
	t.Helper()
	payload := []byte("video frame data")
	hl := HL(0x0601, payload)

	// FindHL on exact HL packet (no surrounding data)
	found := FindHL(hl, 0)
	require.NotNil(t, found)
	require.Equal(t, hl, found)

	// Create a buffer sized to exactly contain HL
	buf := make([]byte, len(hl))
	copy(buf, hl)
	found = FindHL(buf, 0)
	require.NotNil(t, found)
	require.Equal(t, hl, found)
}

func TestFindHLAtOffset(t *testing.T) {
	t.Helper()
	payload := []byte("test")
	hl := HL(0x0601, payload)

	buf := make([]byte, 100)
	copy(buf[20:], hl)
	copy(buf[60:], hl)

	// Finding with offset before first HL
	found := FindHL(buf, 0)
	require.NotNil(t, found)
	// Should point to buf[20:]
	require.Equal(t, hl, found[:len(hl)])

	// Finding with offset past first HL should find second
	found = FindHL(buf, 50)
	require.NotNil(t, found)
	// Should be a valid HL packet
	cmdID, parsedPayload, ok := ParseHL(found)
	require.True(t, ok)
	require.Equal(t, uint16(0x0601), cmdID)
	require.Equal(t, payload, parsedPayload)
}

func TestFindHLNotFound(t *testing.T) {
	t.Helper()
	buf := []byte("no HL marker here")
	found := FindHL(buf, 0)
	require.Nil(t, found)

	found = FindHL(buf, len(buf))
	require.Nil(t, found)
}

func TestGenSessionID(t *testing.T) {
	t.Helper()
	id := GenSessionID()
	require.Len(t, id, 8)

	// Verify it's a LE-encoded nanosecond timestamp
	ts := binary.LittleEndian.Uint64(id)
	require.Greater(t, ts, uint64(0))
	// Should be roughly current time in nanoseconds
	now := uint64(time.Now().UnixNano())
	// Allow 10 second difference
	diff := now - ts
	require.Less(t, diff, uint64(10*time.Second))
}

func TestGenSessionIDUnique(t *testing.T) {
	t.Helper()
	id1 := GenSessionID()
	id2 := GenSessionID()
	require.NotEqual(t, id1, id2, "two consecutive session IDs must differ")
}

func TestFindHLEmptyBuffer(t *testing.T) {
	t.Helper()
	found := FindHL([]byte{}, 0)
	require.Nil(t, found)

	found = FindHL(nil, 0)
	require.Nil(t, found)
}

func TestFindHLOffsetOutOfRange(t *testing.T) {
	t.Helper()
	buf := []byte("HL\x05\x00\x01\x06\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00hello")
	found := FindHL(buf, 100)
	require.Nil(t, found)
}
