// SPDX-License-Identifier: MIT
//
// Xiaomi cloud authentication adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package xiaomi

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var cloudLogger = slog.Default().With("component", "xiaomi-cloud")

// CloudSession holds authenticated Xiaomi cloud session data.
type CloudSession struct {
	UserID       string `json:"user_id"`
	PassToken    string `json:"pass_token"`
	ServiceToken string `json:"service_token"`
	Region       string `json:"region"`

	client    *http.Client
	ssecurity []byte
	cookies   string
}

// CloudDevice represents a Xiaomi IoT device from the cloud API.
type CloudDevice struct {
	DID      string `json:"did"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	IP       string `json:"ip"`
	IsOnline bool   `json:"isOnline"`
}

// LoginError is returned when the login flow requires user interaction
// (captcha or two-factor verification).
type LoginError struct {
	Captcha     []byte `json:"captcha,omitempty"`
	VerifyPhone string `json:"verify_phone,omitempty"`
	VerifyEmail string `json:"verify_email,omitempty"`
}

func (e *LoginError) Error() string {
	switch {
	case len(e.Captcha) > 0:
		return "xiaomi: captcha required"
	case e.VerifyPhone != "":
		return "xiaomi: verification required for " + e.VerifyPhone
	case e.VerifyEmail != "":
		return "xiaomi: verification required for " + e.VerifyEmail
	}
	return "xiaomi: login error"
}

// AuthRequest is the request body for the /api/xiaomi/auth endpoint.
type AuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Region   string `json:"region,omitempty"`
}

// SignIn authenticates with the Xiaomi cloud and returns a session.
// Region should be "cn", "sg", "de", etc. Defaults to "cn" if empty.
func SignIn(username, password, region string) (*CloudSession, error) {
	if username == "" || password == "" {
		return nil, errors.New("xiaomi: username and password are required")
	}
	if region == "" {
		region = "cn"
	}

	c := &Cloud{
		client: &http.Client{Timeout: 15 * time.Second},
		sid:    "xiaomiio",
		region: region,
	}

	if err := c.Login(username, password); err != nil {
		return nil, err
	}

	userID, passToken := c.UserToken()
	return &CloudSession{
		UserID:    userID,
		PassToken: passToken,
		Region:    region,
		client:    c.client,
		ssecurity: c.ssecurity,
		cookies:   c.cookies,
	}, nil
}

// SignInWithToken re-authenticates using a stored passToken.
func SignInWithToken(userID, passToken, region string) (*CloudSession, error) {
	if userID == "" || passToken == "" {
		return nil, errors.New("xiaomi: user_id and token are required")
	}
	if region == "" {
		region = "cn"
	}

	c := &Cloud{
		client: &http.Client{Timeout: 15 * time.Second},
		sid:    "xiaomiio",
		region: region,
	}

	if err := c.LoginWithToken(userID, passToken); err != nil {
		return nil, err
	}

	actualUserID, actualPassToken := c.UserToken()
	return &CloudSession{
		UserID:    actualUserID,
		PassToken: actualPassToken,
		Region:    region,
		client:    c.client,
		ssecurity: c.ssecurity,
		cookies:   c.cookies,
	}, nil
}

// GetDeviceList fetches the list of devices from the Xiaomi cloud.
func GetDeviceList(session *CloudSession) ([]CloudDevice, error) {
	if session == nil || session.cookies == "" {
		return nil, errors.New("xiaomi: session not authenticated")
	}

	c := &Cloud{
		client:    session.client,
		sid:       "xiaomiio",
		region:    session.Region,
		ssecurity: session.ssecurity,
		cookies:   session.cookies,
		userID:    session.UserID,
	}

	result, err := c.Request(
		"https://openapi.io.mi.com",
		"/openapi/device/list",
		`{"getVirtualModel":false,"getHuamiDevices":0}`,
		nil,
	)
	if err != nil {
		return nil, err
	}

	var raw struct {
		List struct {
			List []CloudDevice `json:"list"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &raw); err != nil {
		return nil, fmt.Errorf("xiaomi: failed to parse device list: %w", err)
	}

	return raw.List.List, nil
}

// --- Internal cloud client ---

type Cloud struct {
	client *http.Client
	sid    string
	region string

	cookies   string
	ssecurity []byte

	userID    string
	passToken string

	auth map[string]string
}

func (c *Cloud) Login(username, password string) error {
	res, err := c.client.Get("https://account.xiaomi.com/pass/serviceLogin?_json=true&sid=" + c.sid)
	if err != nil {
		return fmt.Errorf("xiaomi: login step 1: %w", err)
	}

	var v1 struct {
		Qs       string `json:"qs"`
		Sign     string `json:"_sign"`
		Sid      string `json:"sid"`
		Callback string `json:"callback"`
	}
	if _, err = readLoginResponse(res.Body, &v1); err != nil {
		return err
	}

	hash := fmt.Sprintf("%X", md5.Sum([]byte(password)))

	form := url.Values{
		"_json":    {"true"},
		"hash":     {hash},
		"sid":      {v1.Sid},
		"callback": {v1.Callback},
		"_sign":    {v1.Sign},
		"qs":       {v1.Qs},
		"user":     {username},
	}
	cookies := "deviceId=" + randString(16)

	req := cloudRequest{
		Method:     "POST",
		URL:        "https://account.xiaomi.com/pass/serviceLoginAuth2",
		Body:       form,
		RawCookies: cookies,
	}.Encode()

	res, err = c.client.Do(req)
	if err != nil {
		return fmt.Errorf("xiaomi: login step 2: %w", err)
	}

	var v2 struct {
		Ssecurity       []byte `json:"ssecurity"`
		PassToken       string `json:"passToken"`
		Location        string `json:"location"`
		CaptchaURL      string `json:"captchaURL"`
		NotificationURL string `json:"notificationUrl"`
	}
	body, err := readLoginResponse(res.Body, &v2)
	if err != nil {
		return err
	}

	// save auth for two-step verification
	c.auth = map[string]string{
		"username": username,
		"password": password,
	}

	if v2.CaptchaURL != "" {
		return c.getCaptcha(v2.CaptchaURL)
	}

	if v2.NotificationURL != "" {
		return c.authStart(v2.NotificationURL)
	}

	if v2.Location == "" {
		return fmt.Errorf("xiaomi: login failed: %s", body)
	}

	c.auth = nil
	c.ssecurity = v2.Ssecurity
	c.passToken = v2.PassToken

	return c.finishAuth(v2.Location)
}

func (c *Cloud) LoginWithToken(userID, passToken string) error {
	req, err := http.NewRequest("GET", "https://account.xiaomi.com/pass/serviceLogin?_json=true&sid="+c.sid, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Cookie", fmt.Sprintf("userId=%s; passToken=%s", userID, passToken))

	res, err := c.client.Do(req)
	if err != nil {
		return err
	}

	var v1 struct {
		Ssecurity []byte `json:"ssecurity"`
		PassToken string `json:"passToken"`
		Location  string `json:"location"`
	}
	if _, err = readLoginResponse(res.Body, &v1); err != nil {
		return err
	}

	c.ssecurity = v1.Ssecurity
	c.passToken = v1.PassToken

	return c.finishAuth(v1.Location)
}

func (c *Cloud) UserToken() (string, string) {
	return c.userID, c.passToken
}

func (c *Cloud) Request(baseURL, apiURL, params string, headers map[string]string) ([]byte, error) {
	form := url.Values{"data": {params}}

	nonce := genNonce()
	signedNonce := genSignedNonce(c.ssecurity, nonce)

	// 1. gen hash for data param
	form.Set("rc4_hash__", genSignature64("POST", apiURL, form, signedNonce))

	// 2. encrypt data and hash params
	for _, v := range form {
		ciphertext, err := crypt(signedNonce, []byte(v[0]))
		if err != nil {
			return nil, err
		}
		v[0] = base64.StdEncoding.EncodeToString(ciphertext)
	}

	// 3. add signature for encrypted data and hash params
	form.Set("signature", genSignature64("POST", apiURL, form, signedNonce))

	// 4. add nonce
	form.Set("_nonce", base64.StdEncoding.EncodeToString(nonce))

	req, err := http.NewRequest("POST", baseURL+apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Cookie", c.cookies)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, errors.New(res.Status)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	ciphertext, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		return nil, err
	}

	plaintext, err := crypt(signedNonce, ciphertext)
	if err != nil {
		return nil, err
	}

	var res1 struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Result  json.RawMessage `json:"result"`
	}
	if err = json.Unmarshal(plaintext, &res1); err != nil {
		return nil, err
	}

	if res1.Code != 0 {
		return nil, errors.New("xiaomi: " + res1.Message)
	}

	return res1.Result, nil
}

func (c *Cloud) getCaptcha(captchaURL string) error {
	res, err := c.client.Get("https://account.xiaomi.com" + captchaURL)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	c.auth["ick"] = findCookie(res, "ick")

	return &LoginError{
		Captcha: body,
	}
}

func (c *Cloud) authStart(notificationURL string) error {
	rawURL := strings.Replace(notificationURL, "/fe/service/identity/authStart", "/identity/list", 1)
	res, err := c.client.Get(rawURL)
	if err != nil {
		return err
	}

	var v1 struct {
		Code int `json:"code"`
		Flag int `json:"flag"`
	}
	if _, err = readLoginResponse(res.Body, &v1); err != nil {
		return err
	}

	c.auth["flag"] = strconv.Itoa(v1.Flag)
	c.auth["identity_session"] = findCookie(res, "identity_session")

	return c.sendTicket()
}

func (c *Cloud) verifyName() string {
	switch c.auth["flag"] {
	case "4":
		return "Phone"
	case "8":
		return "Email"
	}
	return ""
}

func (c *Cloud) sendTicket() error {
	name := c.verifyName()
	cookies := "identity_session=" + c.auth["identity_session"]

	req := cloudRequest{
		URL:        "https://account.xiaomi.com/identity/auth/verify" + name,
		RawParams:  "_flag=" + c.auth["flag"] + "&_json=true",
		RawCookies: cookies,
	}.Encode()

	res, err := c.client.Do(req)
	if err != nil {
		return err
	}

	var v1 struct {
		Code        int    `json:"code"`
		MaskedPhone string `json:"maskedPhone"`
		MaskedEmail string `json:"maskedEmail"`
	}
	if _, err = readLoginResponse(res.Body, &v1); err != nil {
		return err
	}

	captCode := c.auth["captcha_code"]
	if captCode != "" {
		cookies += "; ick=" + c.auth["ick"]
	}

	form := url.Values{
		"_json": {"true"},
		"icode": {captCode},
		"retry": {"0"},
	}

	req2 := cloudRequest{
		Method:     "POST",
		URL:        "https://account.xiaomi.com/identity/auth/send" + name + "Ticket",
		Body:       form,
		RawCookies: cookies,
	}.Encode()

	res, err = c.client.Do(req2)
	if err != nil {
		return err
	}

	var v2 struct {
		Code       int    `json:"code"`
		CaptchaURL string `json:"captchaURL"`
	}
	body, err := readLoginResponse(res.Body, &v2)
	if err != nil {
		return err
	}

	if v2.CaptchaURL != "" {
		return c.getCaptcha(v2.CaptchaURL)
	}

	if v2.Code != 0 {
		return fmt.Errorf("xiaomi: %s", body)
	}

	return &LoginError{
		VerifyPhone: v1.MaskedPhone,
		VerifyEmail: v1.MaskedEmail,
	}
}

func (c *Cloud) finishAuth(location string) error {
	res, err := c.client.Get(location)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	var cUserID, serviceToken string

	for res != nil {
		for _, cookie := range res.Cookies() {
			switch cookie.Name {
			case "userId":
				c.userID = cookie.Value
			case "cUserId":
				cUserID = cookie.Value
			case "serviceToken":
				serviceToken = cookie.Value
			case "passToken":
				c.passToken = cookie.Value
			}
		}

		if s := res.Header.Get("Extension-Pragma"); s != "" {
			var v1 struct {
				Ssecurity []byte `json:"ssecurity"`
			}
			if err = json.Unmarshal([]byte(s), &v1); err != nil {
				return err
			}
			c.ssecurity = v1.Ssecurity
		}

		res = res.Request.Response
	}

	c.cookies = fmt.Sprintf("userId=%s; cUserId=%s; serviceToken=%s", c.userID, cUserID, serviceToken)

	return nil
}

// --- Internal helpers ---

type cloudRequest struct {
	Method     string
	URL        string
	RawParams  string
	Body       url.Values
	RawCookies string
}

func (r cloudRequest) Encode() *http.Request {
	if r.RawParams != "" {
		r.URL += "?" + r.RawParams
	}

	var body io.Reader
	if r.Body != nil {
		body = strings.NewReader(r.Body.Encode())
	}

	req, err := http.NewRequest(r.Method, r.URL, body)
	if err != nil {
		return nil
	}

	if r.Body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if r.RawCookies != "" {
		req.Header.Set("Cookie", r.RawCookies)
	}

	return req
}

func readLoginResponse(rc io.ReadCloser, v any) ([]byte, error) {
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}

	body, ok := bytes.CutPrefix(body, []byte("&&&START&&&"))
	if !ok {
		return nil, fmt.Errorf("xiaomi: unexpected response: %s", body)
	}

	return body, json.Unmarshal(body, &v)
}

func genNonce() []byte {
	ts := time.Now().Unix() / 60

	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce[:8])
	binary.BigEndian.PutUint32(nonce[8:], uint32(ts))
	return nonce
}

func genSignedNonce(ssecurity, nonce []byte) []byte {
	hasher := sha256.New()
	hasher.Write(ssecurity)
	hasher.Write(nonce)
	return hasher.Sum(nil)
}

func crypt(key, plaintext []byte) ([]byte, error) {
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		return nil, err
	}

	tmp := make([]byte, 1024)
	cipher.XORKeyStream(tmp, tmp)

	ciphertext := make([]byte, len(plaintext))
	cipher.XORKeyStream(ciphertext, plaintext)

	return ciphertext, nil
}

func genSignature64(method, path string, values url.Values, signedNonce []byte) string {
	s := method + "&" + path + "&data=" + values.Get("data")
	if values.Has("rc4_hash__") {
		s += "&rc4_hash__=" + values.Get("rc4_hash__")
	}
	s += "&" + base64.StdEncoding.EncodeToString(signedNonce)

	hasher := sha1.New()
	hasher.Write([]byte(s))
	signature := hasher.Sum(nil)

	return base64.StdEncoding.EncodeToString(signature)
}

func findCookie(res *http.Response, name string) string {
	for _, cookie := range res.Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// randString generates a random alphanumeric string of the given length.
func randString(length int) string {
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	result := make([]byte, length)
	randomBytes := make([]byte, length)
	_, _ = rand.Read(randomBytes)
	for i, b := range randomBytes {
		result[i] = chars[b%byte(len(chars))]
	}
	return string(result)
}
