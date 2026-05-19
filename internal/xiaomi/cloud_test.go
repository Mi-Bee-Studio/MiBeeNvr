// SPDX-License-Identifier: MIT

package xiaomi

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- SignIn validation tests ---

func TestSignInEmptyUsername(t *testing.T) {
	t.Helper()
	_, err := SignIn("", "password", "cn")
	require.Error(t, err)
	require.Contains(t, err.Error(), "username and password are required")
}

func TestSignInEmptyPassword(t *testing.T) {
	t.Helper()
	_, err := SignIn("user@example.com", "", "cn")
	require.Error(t, err)
	require.Contains(t, err.Error(), "username and password are required")
}

func TestSignInEmptyBoth(t *testing.T) {
	t.Helper()
	_, err := SignIn("", "", "cn")
	require.Error(t, err)
}

func TestSignInDefaultRegion(t *testing.T) {
	t.Helper()
	// We can't actually sign in without a real server, but we can verify
	// the validation passes (it'll fail on network, not validation).
	// The function should not error on validation for empty region.
	_, err := SignIn("user@example.com", "password", "")
	// Should NOT be "username and password are required"
	if err != nil && !errors.Is(err, nil) {
		require.NotContains(t, err.Error(), "username and password are required")
	}
}

// --- SignInWithCaptcha validation tests ---

func TestSignInWithCaptchaEmptyUsername(t *testing.T) {
	t.Helper()
	_, _, err := SignInWithCaptcha("", "pass", "cn")
	require.Error(t, err)
	require.Contains(t, err.Error(), "username and password are required")
}

func TestSignInWithCaptchaEmptyPassword(t *testing.T) {
	t.Helper()
	_, _, err := SignInWithCaptcha("user", "", "cn")
	require.Error(t, err)
	require.Contains(t, err.Error(), "username and password are required")
}

// --- SignInWithToken validation tests ---

func TestSignInWithTokenEmptyUserID(t *testing.T) {
	t.Helper()
	_, err := SignInWithToken("", "token", "cn")
	require.Error(t, err)
	require.Contains(t, err.Error(), "user_id and token are required")
}

func TestSignInWithTokenEmptyToken(t *testing.T) {
	t.Helper()
	_, err := SignInWithToken("12345", "", "cn")
	require.Error(t, err)
	require.Contains(t, err.Error(), "user_id and token are required")
}

func TestSignInWithTokenDefaultRegion(t *testing.T) {
	t.Helper()
	_, err := SignInWithToken("12345", "token", "")
	// Should fail on network, not validation
	if err != nil {
		require.NotContains(t, err.Error(), "user_id and token are required")
	}
}

// --- LoginWithCaptcha session tests ---

func TestLoginWithCaptchaInvalidSession(t *testing.T) {
	t.Helper()
	_, err := LoginWithCaptcha("nonexistent-session-id", "1234")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid or expired captcha session")
}

// --- LoginWithVerify session tests ---

func TestLoginWithVerifyInvalidSession(t *testing.T) {
	t.Helper()
	_, err := LoginWithVerify("nonexistent-session-id", "ticket123")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid or expired verification session")
}

// --- GetDeviceList validation tests ---

func TestGetDeviceListNilSession(t *testing.T) {
	t.Helper()
	_, err := GetDeviceList(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "session not authenticated")
}

func TestGetDeviceListEmptyCookies(t *testing.T) {
	t.Helper()
	_, err := GetDeviceList(&CloudSession{
		UserID: "12345",
		Region: "cn",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "session not authenticated")
}

// --- LoginError tests ---

func TestLoginErrorCaptcha(t *testing.T) {
	t.Helper()
	e := &LoginError{Captcha: []byte("image-data")}
	require.Contains(t, e.Error(), "captcha required")
}

func TestLoginErrorVerifyPhone(t *testing.T) {
	t.Helper()
	e := &LoginError{VerifyPhone: "+1******7890"}
	require.Contains(t, e.Error(), "verification required")
	require.Contains(t, e.Error(), "+1******7890")
}

func TestLoginErrorVerifyEmail(t *testing.T) {
	t.Helper()
	e := &LoginError{VerifyEmail: "t***@example.com"}
	require.Contains(t, e.Error(), "verification required")
	require.Contains(t, e.Error(), "t***@example.com")
}

func TestLoginErrorGeneric(t *testing.T) {
	t.Helper()
	e := &LoginError{}
	require.Equal(t, "xiaomi: login error", e.Error())
}

// --- CaptchaSessionError tests ---

func TestCaptchaSessionErrorUnwrap(t *testing.T) {
	t.Helper()
	inner := &LoginError{VerifyPhone: "+1234"}
	e := &CaptchaSessionError{
		LoginError:        inner,
		CaptchaSessionID: "session-abc",
	}
	require.Equal(t, inner.Error(), e.Error())
	require.Equal(t, inner, e.Unwrap())

	var target *LoginError
	require.True(t, errors.As(e, &target))
}

// --- Pending cloud store/load/delete round-trip ---

func TestPendingCloudRoundTrip(t *testing.T) {
	t.Helper()
	c := &Cloud{region: "cn", sid: "xiaomiio"}

	id := storePendingCloud(c)
	require.NotEmpty(t, id)

	loaded := loadPendingCloud(id)
	require.NotNil(t, loaded)
	require.Equal(t, "cn", loaded.region)

	// Load again before delete (should still be there)
	loaded2 := loadPendingCloud(id)
	require.NotNil(t, loaded2)

	deletePendingCloud(id)
	loaded3 := loadPendingCloud(id)
	require.Nil(t, loaded3)
}

func TestLoadPendingCloudNonexistent(t *testing.T) {
	t.Helper()
	loaded := loadPendingCloud("nonexistent-id")
	require.Nil(t, loaded)
}

// --- Cloud.Request with nil ssecurity (will panic on nil key) ---

func TestCloudRequestNilSsecurity(t *testing.T) {
	t.Helper()
	c := &Cloud{
		client:    nil,
		sid:       "xiaomiio",
		region:    "cn",
		ssecurity: nil,
		cookies:   "",
	}
	// Request should fail gracefully (nil ssecurity → genSignedNonce receives nil)
	require.Panics(t, func() {
		_, _ = c.Request("https://api.io.mi.com/app", "/test", "{}", nil)
	})
}

// --- getAPIBaseURL tests ---

func TestGetAPIBaseURLCN(t *testing.T) {
	t.Helper()
	require.Equal(t, "https://api.io.mi.com/app", getAPIBaseURL("cn"))
}

func TestGetAPIBaseURLEmpty(t *testing.T) {
	t.Helper()
	require.Equal(t, "https://api.io.mi.com/app", getAPIBaseURL(""))
}

func TestGetAPIBaseURLDE(t *testing.T) {
	t.Helper()
	require.Equal(t, "https://de.api.io.mi.com/app", getAPIBaseURL("de"))
}

func TestGetAPIBaseURLSG(t *testing.T) {
	t.Helper()
	require.Equal(t, "https://sg.api.io.mi.com/app", getAPIBaseURL("sg"))
}

func TestGetAPIBaseURLUS(t *testing.T) {
	t.Helper()
	require.Equal(t, "https://us.api.io.mi.com/app", getAPIBaseURL("us"))
}

func TestGetAPIBaseURLRU(t *testing.T) {
	t.Helper()
	require.Equal(t, "https://ru.api.io.mi.com/app", getAPIBaseURL("ru"))
}

func TestGetAPIBaseURLI2(t *testing.T) {
	t.Helper()
	require.Equal(t, "https://i2.api.io.mi.com/app", getAPIBaseURL("i2"))
}

// --- randString tests ---

func TestRandStringLength(t *testing.T) {
	t.Helper()
	for _, length := range []int{0, 1, 8, 16, 32, 64} {
		s := randString(length)
		require.Len(t, s, length)
	}
}

func TestRandStringUniqueness(t *testing.T) {
	t.Helper()
	results := make(map[string]bool)
	for i := 0; i < 100; i++ {
		s := randString(16)
		require.False(t, results[s], "randString should produce unique values")
		results[s] = true
	}
}

// --- readLoginResponse tests ---

func TestReadLoginResponseValidPrefix(t *testing.T) {
	t.Helper()
	// io.NopCloser for a simple reader
	body := []byte("&&&START&&&{\"code\":0}")
	data, err := readLoginResponse(&nopReadCloser{data: body}, nil)
	require.NoError(t, err)
	require.Equal(t, `{"code":0}`, string(data))
}

func TestReadLoginResponseMissingPrefix(t *testing.T) {
	t.Helper()
	body := []byte(`{"code":0}`)
	_, err := readLoginResponse(&nopReadCloser{data: body}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected response")
}

func TestReadLoginResponseWithJSONDecode(t *testing.T) {
	t.Helper()
	var v struct {
		Code int `json:"code"`
	}
	body := []byte("&&&START&&&{\"code\":42}")
	_, err := readLoginResponse(&nopReadCloser{data: body}, &v)
	require.NoError(t, err)
	require.Equal(t, 42, v.Code)
}

// --- genNonce tests ---

func TestGenNonceLength(t *testing.T) {
	t.Helper()
	nonce := genNonce()
	require.Len(t, nonce, 12)
}

func TestGenNonceUniqueness(t *testing.T) {
	t.Helper()
	nonces := make(map[string]bool)
	for i := 0; i < 50; i++ {
		n := genNonce()
		s := hex.EncodeToString(n)
		require.False(t, nonces[s], "genNonce should produce unique values")
		nonces[s] = true
	}
}

// --- genSignedNonce tests ---

func TestGenSignedNonceDeterministic(t *testing.T) {
	t.Helper()
	ssecurity := []byte("test-key-12345678901234567890")
	nonce := []byte("nonce-12345678")

	result1 := genSignedNonce(ssecurity, nonce)
	result2 := genSignedNonce(ssecurity, nonce)
	require.Equal(t, result1, result2)
	require.Len(t, result1, 32) // SHA256 output
}

// --- crypt tests ---

func TestCryptRoundTrip(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	plaintext := []byte("Hello Xiaomi Camera!")

	encrypted, err := crypt(key, plaintext)
	require.NoError(t, err)
	require.NotEqual(t, plaintext, encrypted)

	decrypted, err := crypt(key, encrypted)
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted)
}

func TestCryptEmptyPlaintext(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	encrypted, err := crypt(key, []byte{})
	require.NoError(t, err)
	require.Empty(t, encrypted)
}

// --- genSignature64 tests ---

func TestGenSignature64ReturnsBase64(t *testing.T) {
	t.Helper()
	values := url.Values{"data": {`{"test":true}`}}
	signedNonce := make([]byte, 32)
	for i := range signedNonce {
		signedNonce[i] = byte(i)
	}
	sig := genSignature64("POST", "/v2/test", values, signedNonce)
	require.NotEmpty(t, sig)
}

// --- findCookie tests ---

func TestFindCookiePresent(t *testing.T) {
	t.Helper()
	res := &http.Response{
		Header: http.Header{
			"Set-Cookie": {"sessionId=abc123; Path=/", "userId=42; Path=/"},
		},
	}
	require.Equal(t, "abc123", findCookie(res, "sessionId"))
	require.Equal(t, "42", findCookie(res, "userId"))
}

func TestFindCookieAbsent(t *testing.T) {
	t.Helper()
	res := &http.Response{
		Header: http.Header{
			"Set-Cookie": {"sessionId=abc123"},
		},
	}
	require.Equal(t, "", findCookie(res, "nonexistent"))
}

func TestFindCookieNoCookies(t *testing.T) {
	t.Helper()
	res := &http.Response{Header: http.Header{}}
	require.Equal(t, "", findCookie(res, "anything"))
}

// --- cloudRequest.Encode tests ---

func TestCloudRequestEncodeGET(t *testing.T) {
	t.Helper()
	req := cloudRequest{
		Method: "GET",
		URL:    "https://example.com/api",
	}.Encode()
	require.NotNil(t, req)
	require.Equal(t, "GET", req.Method)
	require.Equal(t, "https://example.com/api", req.URL.String())
}

func TestCloudRequestEncodePOSTWithBody(t *testing.T) {
	t.Helper()
	form := url.Values{"key": {"value"}}
	req := cloudRequest{
		Method: "POST",
		URL:    "https://example.com/api",
		Body:   form,
	}.Encode()
	require.NotNil(t, req)
	require.Equal(t, "POST", req.Method)
	require.Equal(t, "application/x-www-form-urlencoded", req.Header.Get("Content-Type"))
}

func TestCloudRequestEncodeWithCookies(t *testing.T) {
	t.Helper()
	req := cloudRequest{
		URL:        "https://example.com/api",
		RawCookies: "session=abc",
	}.Encode()
	require.NotNil(t, req)
	require.Equal(t, "session=abc", req.Header.Get("Cookie"))
}

func TestCloudRequestEncodeWithRawParams(t *testing.T) {
	t.Helper()
	req := cloudRequest{
		URL:       "https://example.com/api",
		RawParams: "foo=bar",
	}.Encode()
	require.NotNil(t, req)
	require.Contains(t, req.URL.String(), "foo=bar")
}

func TestCloudRequestEncodeInvalidURL(t *testing.T) {
	t.Helper()
	req := cloudRequest{
		URL: "\x00invalid",
	}.Encode()
	// http.NewRequest returns nil on error for invalid URL
	require.Nil(t, req)
}

// --- Cloud.UserToken tests ---

func TestCloudUserTokenEmpty(t *testing.T) {
	t.Helper()
	c := &Cloud{}
	userID, passToken := c.UserToken()
	require.Equal(t, "", userID)
	require.Equal(t, "", passToken)
}

func TestCloudUserTokenSet(t *testing.T) {
	t.Helper()
	c := &Cloud{userID: "12345", passToken: "abc"}
	userID, passToken := c.UserToken()
	require.Equal(t, "12345", userID)
	require.Equal(t, "abc", passToken)
}

// --- Cloud.verifyName tests ---

func TestCloudVerifyNamePhone(t *testing.T) {
	t.Helper()
	c := &Cloud{auth: map[string]string{"flag": "4"}}
	require.Equal(t, "Phone", c.verifyName())
}

func TestCloudVerifyNameEmail(t *testing.T) {
	t.Helper()
	c := &Cloud{auth: map[string]string{"flag": "8"}}
	require.Equal(t, "Email", c.verifyName())
}

func TestCloudVerifyNameUnknown(t *testing.T) {
	t.Helper()
	c := &Cloud{auth: map[string]string{"flag": "99"}}
	require.Equal(t, "", c.verifyName())
}

// --- CloudSession fields ---

func TestCloudSessionFields(t *testing.T) {
	t.Helper()
	s := &CloudSession{
		UserID:       "12345",
		PassToken:    "token-abc",
		ServiceToken: "svc-token",
		Region:       "us",
	}
	require.Equal(t, "12345", s.UserID)
	require.Equal(t, "token-abc", s.PassToken)
	require.Equal(t, "svc-token", s.ServiceToken)
	require.Equal(t, "us", s.Region)
}

// --- CloudDevice fields ---

func TestCloudDeviceFields(t *testing.T) {
	t.Helper()
	d := CloudDevice{
		DID:      "655448418",
		Name:     "Living Room Camera",
		Model:    ModelC200,
		IP:       "192.168.1.100",
		MAC:      "AA:BB:CC:DD:EE:FF",
		IsOnline: true,
	}
	require.Equal(t, "655448418", d.DID)
	require.Equal(t, "Living Room Camera", d.Name)
	require.True(t, d.IsOnline)
}

// --- AuthRequest fields ---

func TestAuthRequestJSON(t *testing.T) {
	t.Helper()
	req := AuthRequest{
		Username: "user@example.com",
		Password: "secret123",
		Region:   "sg",
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	require.Contains(t, string(data), "user@example.com")
	require.Contains(t, string(data), "secret123")
	require.Contains(t, string(data), "sg")
}

func TestAuthRequestEmptyRegion(t *testing.T) {
	t.Helper()
	req := AuthRequest{
		Username: "user@example.com",
		Password: "secret123",
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	// Empty region should be omitted (omitempty)
	require.NotContains(t, string(data), "region")
}

// --- Helper types ---

type nopReadCloser struct {
	data   []byte
	offset int
}

func (r *nopReadCloser) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func (r nopReadCloser) Close() error { return nil }
