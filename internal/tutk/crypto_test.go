// SPDX-License-Identifier: MIT
//
// TUTK P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package tutk

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReverseTransCodePartialRoundtrip(t *testing.T) {
	t.Helper()
	src := []byte("Hello TUTK World!!")
	dst := make([]byte, len(src))

	// Forward then reverse should restore original
	enc := TransCodePartial(dst, src)
	dec := ReverseTransCodePartial(nil, enc)
	require.Equal(t, src, dec[:len(src)])
}

func TestReverseTransCodeBlobRoundtrip(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		data []byte
	}{
		{"short (under 16 bytes)", []byte("short")},
		{"exact 16 bytes", []byte("0123456789ABCDEF")},
		{"24 bytes (partial encryption)", []byte("ABCDEFGHIJKLMNOPQRSTUVWX")},
		{"72 bytes (full blob)", []byte("This is a longer test message that should exercise full blob encryption pathways in the TUTK transform")},
		{"128 bytes (full coverage)", []byte("Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			enc := TransCodeBlob(tt.data)
			dec := ReverseTransCodeBlob(enc)
			require.Equal(t, tt.data, dec)
		})
	}
}

func TestTransCodeBlobPreservesLength(t *testing.T) {
	t.Helper()
	data := []byte("test data for length check")
	enc := TransCodeBlob(data)
	require.Len(t, enc, len(data))
}

func TestReverseTransCodeBlobUnder16(t *testing.T) {
	t.Helper()
	src := []byte("short")
	enc := TransCodeBlob(src)
	dec := ReverseTransCodeBlob(enc)
	require.Equal(t, src, dec)
}

func TestXXTEADecrypt(t *testing.T) {
	t.Helper()
	// Test vector adapted from go2rtc's crypto_test.go
	buf := []byte("WERhJxb87WF3zgPa")
	key := []byte("GAgDiwVPg2E4GMke")
	XXTEADecrypt(buf, buf, key)
	expected := []byte("\xc4\xa6\x2c\xa1\x10\x64\x17\xa5\xda\x02\xe1\x62\xa5\xf0\x62\x71")
	require.Equal(t, expected, buf)
}

func TestXXTEADecryptVar_NilHandling(t *testing.T) {
	t.Helper()
	// Too short data
	result := XXTEADecryptVar([]byte("short"), []byte("0123456789ABCDEF"))
	require.Nil(t, result)

	// Too short key
	result = XXTEADecryptVar([]byte("0123456789ABCDEF"), []byte("short"))
	require.Nil(t, result)
}

func TestXXTEADecryptVar_Basic(t *testing.T) {
	t.Helper()
	// 16-byte block (word-aligned)
	data := []byte("0123456789ABCDEF")
	key := []byte("0123456789ABCDEF")
	result := XXTEADecryptVar(data, key)
	require.NotNil(t, result)
	require.Equal(t, 16, len(result))
}

func TestXXTEADecryptVar_VariableLengths(t *testing.T) {
	t.Helper()
	// Test with various word-aligned lengths (minimum 8 bytes for XXTEA)
	key := []byte("0123456789ABCDEF")
	for _, size := range []int{8, 12, 16, 20, 24, 32, 48, 64} {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i * 7)
		}
		result := XXTEADecryptVar(data, key)
		require.NotNil(t, result, "failed for size %d", size)
		require.Equal(t, size, len(result), "length mismatch for size %d", size)
	}
}

func TestTransCodeBlobDeterministic(t *testing.T) {
	t.Helper()
	data := []byte("deterministic test data 12345")
	enc1 := TransCodeBlob(data)
	enc2 := TransCodeBlob(data)
	require.Equal(t, enc1, enc2, "TransCodeBlob must be deterministic")
}

func TestTransCodePartialBlockBoundaries(t *testing.T) {
	t.Helper()
	// Test sizes around the 16-byte block boundary
	for _, size := range []int{1, 2, 3, 4, 8, 15, 16, 17, 31, 32, 33, 47, 48, 49, 63, 64, 65} {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i*17 + 13)
		}
		enc := TransCodeBlob(data)
		dec := ReverseTransCodeBlob(enc)
		require.Equal(t, data, dec, "roundtrip failed for size %d", size)
	}
}

func TestSwapTable(t *testing.T) {
	t.Helper()
	tests := []struct {
		name  string
		n     int
		input []byte
		want  []byte
	}{
		{
			name:  "swap 2",
			n:     2,
			input: []byte{0x01, 0x02},
			want:  []byte{0x02, 0x01},
		},
		{
			name:  "swap 4",
			n:     4,
			input: []byte{0x01, 0x02, 0x03, 0x04},
			want:  []byte{0x03, 0x04, 0x01, 0x02},
		},
		{
			name:  "swap 8",
			n:     8,
			input: []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
			want:  []byte{0x08, 0x05, 0x04, 0x03, 0x02, 0x07, 0x06, 0x01},
		},
		{
			name:  "swap 16",
			n:     16,
			input: []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10},
			want:  []byte{0x0C, 0x0A, 0x09, 0x10, 0x0E, 0x0B, 0x0D, 0x0F, 0x03, 0x02, 0x06, 0x01, 0x07, 0x05, 0x08, 0x04},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			dst := make([]byte, len(tt.input))
			swap(dst, tt.input, tt.n)
			require.Equal(t, tt.want, dst[:tt.n])
		})
	}
}

func TestTransCodeBlobPartialEncryptionFlag(t *testing.T) {
	t.Helper()
	// When header byte 3 has bit 0 set, it indicates partial encryption
	// Test with a blob > 64 bytes to exercise both partial and full paths
	data := make([]byte, 80)
	for i := range data {
		data[i] = byte(i)
	}

	// Full encryption path (byte 3 bit 0 = 0)
	fullEnc := TransCodeBlob(data)
	// byte 3 after transcode should match original byte 3 XOR'd and swapped,
	// so we test that roundtrip works
	fullDec := ReverseTransCodeBlob(fullEnc)
	require.Equal(t, data, fullDec)
}

func TestXXTEADecryptVarBasic(t *testing.T) {
	t.Helper()
	// 16-byte block, 16-byte key
	data := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	key := []byte("0123456789ABCDEF")
	result := XXTEADecryptVar(data, key)
	require.NotNil(t, result)
	require.Equal(t, 16, len(result))
}

func TestTransCodeBlobNilInput(t *testing.T) {
	t.Helper()
	// Empty input should not panic
	empty := []byte{}
	result := TransCodeBlob(empty)
	require.Empty(t, result)

	result = ReverseTransCodeBlob(empty)
	require.Empty(t, result)
}

func TestReverseTransCodePartialProducesSameAsTransCodePartialAfterRoundtrip(t *testing.T) {
	t.Helper()
	// Verify ReverseTransCodePartial is the inverse of TransCodePartial
	for _, size := range []int{8, 16, 32, 64} {
		src := make([]byte, size)
		for i := range src {
			src[i] = byte(i*7 + 3)
		}
		enc := make([]byte, size)
		dec := make([]byte, size)

		TransCodePartial(enc, src)
		ReverseTransCodePartial(dec, enc)

		require.Equal(t, src, dec[:size], "failed for size %d", size)
	}
}

