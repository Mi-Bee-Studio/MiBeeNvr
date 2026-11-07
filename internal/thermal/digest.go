package thermal

import (
	"crypto/md5" //nolint:gosec // HTTP Digest (RFC 2617/7616 MD5) mandates MD5
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// DigestTransport is a minimal HTTP Digest client (RFC 7616 MD5 /
// MD5-sess, qop=auth or legacy no-qop) for vendor device APIs such as
// Hikvision ISAPI, whose web services refuse Basic by default. It answers
// the first 401 challenge, then pre-authorizes later requests with the
// cached nonce (nc incremented) so a 2 s poll costs one round trip, and
// re-challenges once when the device rotates its nonce. Requests must be
// replayable (GET / no body) — the only shape the readout adapters send.
type DigestTransport struct {
	Username string
	Password string
	// Base carries the requests (nil = http.DefaultTransport).
	Base http.RoundTripper

	mu   sync.Mutex
	chal *digestChallenge
	nc   uint32
}

type digestChallenge struct {
	realm, nonce, opaque, algorithm, qop string
}

// RoundTrip implements http.RoundTripper.
func (t *DigestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.Body != http.NoBody {
		return nil, fmt.Errorf("digest transport: request bodies are not replayable")
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	if ch := t.cachedChallenge(); ch != nil {
		resp, err := base.RoundTrip(t.authorized(req, ch))
		if err != nil || resp.StatusCode != http.StatusUnauthorized {
			return resp, err
		}
		// Nonce rotated (stale) or credentials rejected: take the fresh
		// challenge from this 401 and retry exactly once.
		fresh := parseDigestChallenge(resp.Header.Values("WWW-Authenticate"))
		if fresh == nil {
			return resp, nil
		}
		drainClose(resp)
		t.storeChallenge(fresh)
		return base.RoundTrip(t.authorized(req, fresh))
	}

	resp, err := base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	ch := parseDigestChallenge(resp.Header.Values("WWW-Authenticate"))
	if ch == nil {
		return resp, nil // not a digest challenge — surface the 401 as-is
	}
	drainClose(resp)
	t.storeChallenge(ch)
	return base.RoundTrip(t.authorized(req, ch))
}

func (t *DigestTransport) cachedChallenge() *digestChallenge {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.chal
}

func (t *DigestTransport) storeChallenge(ch *digestChallenge) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.chal = ch
	t.nc = 0
}

// authorized clones req with an Authorization header answering ch.
func (t *DigestTransport) authorized(req *http.Request, ch *digestChallenge) *http.Request {
	t.mu.Lock()
	t.nc++
	nc := fmt.Sprintf("%08x", t.nc)
	t.mu.Unlock()

	cnonce := randomHex(8)
	uri := req.URL.RequestURI()
	ha1 := md5Hex(t.Username + ":" + ch.realm + ":" + t.Password)
	if strings.EqualFold(ch.algorithm, "MD5-sess") {
		ha1 = md5Hex(ha1 + ":" + ch.nonce + ":" + cnonce)
	}
	ha2 := md5Hex(req.Method + ":" + uri)

	var b strings.Builder
	fmt.Fprintf(&b, `Digest username=%q, realm=%q, nonce=%q, uri=%q`, t.Username, ch.realm, ch.nonce, uri)
	if ch.qop != "" {
		resp := md5Hex(ha1 + ":" + ch.nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
		fmt.Fprintf(&b, `, qop=auth, nc=%s, cnonce=%q, response=%q`, nc, cnonce, resp)
	} else {
		fmt.Fprintf(&b, `, response=%q`, md5Hex(ha1+":"+ch.nonce+":"+ha2))
	}
	if ch.algorithm != "" {
		fmt.Fprintf(&b, `, algorithm=%s`, ch.algorithm)
	}
	if ch.opaque != "" {
		fmt.Fprintf(&b, `, opaque=%q`, ch.opaque)
	}

	r2 := req.Clone(req.Context())
	r2.Header.Set("Authorization", b.String())
	return r2
}

// parseDigestChallenge picks the first Digest challenge offering qop=auth
// (or no qop at all) out of the WWW-Authenticate header values.
func parseDigestChallenge(values []string) *digestChallenge {
	for _, v := range values {
		scheme, rest, ok := strings.Cut(strings.TrimSpace(v), " ")
		if !ok || !strings.EqualFold(scheme, "Digest") {
			continue
		}
		params := parseAuthParams(rest)
		ch := &digestChallenge{
			realm:     params["realm"],
			nonce:     params["nonce"],
			opaque:    params["opaque"],
			algorithm: params["algorithm"],
		}
		if ch.nonce == "" {
			continue
		}
		if q := params["qop"]; q != "" {
			for _, opt := range strings.Split(q, ",") {
				if strings.EqualFold(strings.TrimSpace(opt), "auth") {
					ch.qop = "auth"
				}
			}
			if ch.qop == "" {
				continue // only auth-int offered: not supported
			}
		}
		return ch
	}
	return nil
}

// parseAuthParams splits `k="v, with commas", k2=v2` into a lower-cased map.
func parseAuthParams(s string) map[string]string {
	out := map[string]string{}
	for len(s) > 0 {
		s = strings.TrimLeft(s, " ,\t")
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = strings.TrimLeft(s[eq+1:], " \t")
		var val string
		if strings.HasPrefix(s, `"`) {
			end := 1
			for end < len(s) && s[end] != '"' {
				if s[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(s) {
				val, s = s[1:], ""
			} else {
				val, s = s[1:end], s[end+1:]
			}
			val = strings.ReplaceAll(val, `\"`, `"`)
		} else {
			comma := strings.IndexByte(s, ',')
			if comma < 0 {
				val, s = strings.TrimSpace(s), ""
			} else {
				val, s = strings.TrimSpace(s[:comma]), s[comma+1:]
			}
		}
		out[key] = val
	}
	return out
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // HTTP Digest mandates MD5
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}
