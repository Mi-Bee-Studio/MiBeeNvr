// SPDX-License-Identifier: MIT
//
// Xiaomi camera crypto tests adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package xiaomi

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateKey(t *testing.T) {
	t.Helper()
	public, private, err := GenerateKey()
	require.NoError(t, err)
	require.NotNil(t, public)
	require.NotNil(t, private)
	require.Len(t, public, 32)
	require.Len(t, private, 32)
}

func TestCalcSharedKey(t *testing.T) {
	t.Helper()
	public, private, err := GenerateKey()
	require.NoError(t, err)

	publicHex := hex.EncodeToString(public)
	privateHex := hex.EncodeToString(private)

	shared, err := CalcSharedKey(publicHex, privateHex)
	require.NoError(t, err)
	require.NotNil(t, shared)
	require.Len(t, shared, 32)
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	t.Helper()
	public, private, err := GenerateKey()
	require.NoError(t, err)

	publicHex := hex.EncodeToString(public)
	privateHex := hex.EncodeToString(private)

	sharedKey, err := CalcSharedKey(publicHex, privateHex)
	require.NoError(t, err)

	plaintext := []byte("Hello Xiaomi Camera!")
	encoded, err := Encode(plaintext, sharedKey)
	require.NoError(t, err)
	require.NotEqual(t, plaintext, encoded[8:])

	decoded, err := Decode(encoded, sharedKey)
	require.NoError(t, err)
	require.Equal(t, plaintext, decoded)
}

func TestCalcSharedKeyInvalidHex(t *testing.T) {
	t.Helper()
	_, err := CalcSharedKey("not-valid-hex", "also-not-hex")
	require.Error(t, err)
}

func TestDecodeWithValidKey(t *testing.T) {
	t.Helper()
	public, private, err := GenerateKey()
	require.NoError(t, err)

	sharedKey, err := CalcSharedKey(hex.EncodeToString(public), hex.EncodeToString(private))
	require.NoError(t, err)

	// Test with empty plaintext
	encoded, err := Encode([]byte{}, sharedKey)
	require.NoError(t, err)
	require.Len(t, encoded, 8)

	decoded, err := Decode(encoded, sharedKey)
	require.NoError(t, err)
	require.Equal(t, []byte{}, decoded)
}
