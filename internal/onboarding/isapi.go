package onboarding

// isapi.go — ISAPI write fallback for cameras whose ONVIF
// SetNetworkInterfaces is locked or rejects re-addressing (field-found on a
// Hikvision DS-2CD2225Y whose ONVIF op answers ter:InvalidArgVal while its
// singular ISAPI path PUT /ISAPI/System/Network/interfaces/1 accepts the
// same change; issue #1120 E2E, 2026-11-11). Carries its own minimal HTTP
// digest client (RFC 7616 MD5 + SHA-256) because the onvif-go digest
// transport is internal to that module.

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // MD5 required for ONVIF/ISAPI digest auth
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// isapiDigestClient performs HTTP digest auth over a base transport.
type isapiDigestClient struct {
	base     *http.Transport
	username string
	password string
	nc       int
}

func (d *isapiDigestClient) do(ctx context.Context, method, url, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)

	client := &http.Client{
		Transport: d,
		Timeout:   scanHostTimeout * 2,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 256<<10))
}

// RoundTrip implements http.RoundTripper with digest auth (RFC 7616):
// first attempt unauthenticated; on a 401 with a Digest challenge, answer
// it (SHA-256 when the challenge says so) and retry with a fresh body.
func (d *isapiDigestClient) RoundTrip(req *http.Request) (*http.Response, error) {
	var bodyBytes []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		bodyBytes = b
	}

	first := req.Clone(req.Context())
	first.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	resp, err := d.base.RoundTrip(first)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	challenge := resp.Header.Get("WWW-Authenticate")
	if !strings.Contains(challenge, "Digest") {
		return resp, nil
	}

	auth := d.authorizationHeader(req.Method, req.URL.RequestURI(), challenge)
	retry := req.Clone(req.Context())
	retry.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	retry.ContentLength = int64(len(bodyBytes))
	retry.Header.Set("Authorization", auth)

	if resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return d.base.RoundTrip(retry)
}

func (d *isapiDigestClient) authorizationHeader(method, uri, challenge string) string {
	realm := isapiParam(challenge, "realm")
	nonce := isapiParam(challenge, "nonce")
	qop := isapiParam(challenge, "qop")
	algorithm := isapiParam(challenge, "algorithm")
	opaque := isapiParam(challenge, "opaque")

	hash := func(s string) string {
		switch strings.ToUpper(algorithm) {
		case "SHA-256", "SHA256":
			h := sha256.Sum256([]byte(s))
			return hex.EncodeToString(h[:])
		default:
			h := md5.Sum([]byte(s))
			return hex.EncodeToString(h[:])
		}
	}

	d.nc++
	nc := fmt.Sprintf("%08x", d.nc)
	nonceBytes := make([]byte, 8)
	_, _ = rand.Read(nonceBytes)
	cnonce := hex.EncodeToString(nonceBytes)

	ha1 := hash(d.username + ":" + realm + ":" + d.password)
	ha2 := hash(method + ":" + uri)
	response := hash(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)

	out := fmt.Sprintf(`Digest username=%q, realm=%q, nonce=%q, uri=%q, response=%q, algorithm=%s`,
		d.username, realm, nonce, uri, response, algorithmOrMD5(algorithm))
	if qop != "" {
		out += fmt.Sprintf(", qop=%s, nc=%s, cnonce=%q", qop, nc, cnonce)
	}
	if opaque != "" {
		out += fmt.Sprintf(`, opaque=%q`, opaque)
	}
	return out
}

func algorithmOrMD5(algorithm string) string {
	if algorithm == "" {
		return "MD5"
	}
	return algorithm
}

func isapiParam(challenge, param string) string {
	if v := quotedParam(challenge, param); v != "" {
		return v
	}
	idx := strings.Index(challenge, param+"=")
	if idx == -1 {
		return ""
	}
	rest := challenge[idx+len(param)+1:]
	end := strings.IndexAny(rest, ", ")
	if end == -1 {
		end = len(rest)
	}
	return strings.Trim(strings.TrimSpace(rest[:end]), `"`)
}

func quotedParam(challenge, param string) string {
	idx := strings.Index(challenge, param+`="`)
	if idx == -1 {
		return ""
	}
	rest := challenge[idx+len(param)+2:]
	end := strings.Index(rest, `"`)
	if end == -1 {
		return ""
	}
	return rest[:end]
}

// isapiDeviceInfo mirrors the bootstrap document (the <id> of the network
// interface doc drives the singular PUT path).
type isapiNetInterfaceDoc struct {
	XMLName xml.Name `xml:"NetworkInterface"`
	ID      int      `xml:"id"`
	XMLNS   string   `xml:"xmlns,attr"`
	Version string   `xml:"version,attr"`
	Raw     []byte   `xml:",innerxml"`
}

// isapiSetNet re-addresses a Hikvision camera through its ISAPI surface:
// GET the singular interface document, patch addressingType and the IPv4
// address, PUT it back. Falls back through the credential variants real
// deployments carry (the request pair, then "admin" with the same
// password — Hikvision's ONVIF user and device admin are independent
// tables that often share one password).
func isapiSetNet(ctx context.Context, ip, username, password, staticIP string, prefixLen int) error {
	doc, usedUser, err := isapiFetchInterfaceDoc(ctx, ip, username, password)
	if err != nil {
		return err
	}

	// Patch the RAW inner document (attribute-preserving string surgery —
	// xml.Marshal of ,innerxml would double-escape it).
	patched := patchISAPINetworkDoc(string(doc.Raw), staticIP, prefixLen)
	full := rewriteISAPIDoc(string(doc.Raw), doc.XMLNS, doc.Version, doc.ID, patched)
	body := []byte(full)

	clients := []isapiDigestClient{
		{base: &http.Transport{}, username: usedUser, password: password},
	}
	if usedUser != "admin" {
		clients = append(clients, isapiDigestClient{base: &http.Transport{}, username: "admin", password: password})
	}

	var lastErr error
	for i := range clients {
		c := clients[i]
		url := fmt.Sprintf("http://%s/ISAPI/System/Network/interfaces/%d", ip, doc.ID)
		respBody, err := c.do(ctx, http.MethodPut, url, "application/xml", body)
		if err != nil {
			lastErr = err
			continue
		}
		if strings.Contains(string(respBody), "<statusString>OK</statusString>") ||
			strings.Contains(string(respBody), "statusCode>1<") {
			return nil
		}
		lastErr = fmt.Errorf("ISAPI put rejected: %s", truncateISAPI(respBody))
	}
	return lastErr
}

// isapiFetchInterfaceDoc GETs the singular interface document, trying the
// request pair first and then "admin"+password (see isapiSetNet).
func isapiFetchInterfaceDoc(ctx context.Context, ip, username, password string) (*isapiNetInterfaceDoc, string, error) {
	attempts := []string{username}
	if username != "admin" {
		attempts = append(attempts, "admin")
	}
	var lastErr error
	for _, u := range attempts {
		c := isapiDigestClient{base: &http.Transport{}, username: u, password: password}
		body, err := c.do(ctx, http.MethodGet,
			fmt.Sprintf("http://%s/ISAPI/System/Network/interfaces/1", ip), "", nil)
		if err != nil {
			lastErr = err
			continue
		}
		if !strings.Contains(string(body), "<NetworkInterface") {
			lastErr = fmt.Errorf("ISAPI interface doc unreadable: %s", truncateISAPI(body))
			continue
		}
		var doc isapiNetInterfaceDoc
		if err := xml.Unmarshal(body, &doc); err != nil || doc.ID == 0 {
			lastErr = fmt.Errorf("ISAPI interface doc unparsable: %w", err)
			continue
		}
		return &doc, u, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("ISAPI unreachable")
	}
	return nil, "", lastErr
}

// patchISAPINetworkDoc flips addressingType to static and rewrites the
// IPv4 address (and netmask) inside the ISAPI document's inner XML.
func patchISAPINetworkDoc(inner, staticIP string, prefixLen int) string {
	out := replaceTagValue(inner, "addressingType", "static")

	// Rewrite the <ipAddress> block: <ipAddress>, <netmask>, both inside
	// <IPAddress>. Simple, bounded surgery on the two well-known tags.
	out = replaceTagValue(out, "ipAddress", staticIP)
	mask := prefixToMask(prefixLen)
	out = replaceTagValue(out, "netmask", mask)
	return out
}

// rewriteISAPIDoc reassembles the singular NetworkInterface document with
// the patched inner content, preserving the device's namespace attributes.
func rewriteISAPIDoc(inner, xmlns, version string, id int, patched string) string {
	return fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<NetworkInterface version=%q xmlns=%q><id>%d</id>%s</NetworkInterface>`,
		version, xmlns, id, patched)
}

func replaceTagValue(doc, tag, value string) string {
	openTag := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	start := strings.Index(doc, openTag)
	if start == -1 {
		return doc
	}
	endIdx := strings.Index(doc[start:], closeTag)
	if endIdx == -1 {
		return doc
	}
	return doc[:start] + openTag + value + doc[start+endIdx:]
}

func prefixToMask(prefix int) string {
	if prefix < 0 || prefix > 32 {
		prefix = 24
	}
	var b [4]byte
	for i := range 4 {
		if prefix >= 8 {
			b[i] = 255
			prefix -= 8
			continue
		}
		b[i] = byte(0xFF << (8 - prefix))
		prefix = 0
	}
	return strconv.Itoa(int(b[0])) + "." + strconv.Itoa(int(b[1])) + "." + strconv.Itoa(int(b[2])) + "." + strconv.Itoa(int(b[3]))
}

func truncateISAPI(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// isapiReach checks reachability through the ISAPI face (credentials as
// given, then admin+password).
func isapiReach(ctx context.Context, ip, username, password string) bool {
	attempts := []string{username}
	if username != "admin" {
		attempts = append(attempts, "admin")
	}
	for _, u := range attempts {
		c := isapiDigestClient{base: &http.Transport{}, username: u, password: password}
		ctx2, cancel := context.WithTimeout(ctx, scanHostTimeout*2)
		body, err := c.do(ctx2, http.MethodGet,
			fmt.Sprintf("http://%s/ISAPI/System/deviceInfo", ip), "", nil)
		cancel()
		if err == nil && strings.Contains(string(body), "<DeviceInfo") {
			return true
		}
	}
	return false
}
