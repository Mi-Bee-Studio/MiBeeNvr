package doorbell

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/ghettovoice/gosip/sip"
)

// nonces issues and verifies digest challenges. A nonce is single-use and
// expires after 5 minutes; issuing a fresh challenge invalidates the user's
// previous pending nonce (a second REGISTER without auth re-challenges).
type nonces struct {
	mu   sync.Mutex
	byID map[string]nonceEntry
}

type nonceEntry struct {
	user      string
	expiresAt int64 // unix nanos
}

const nonceTTL = 5 * 60 * 1e9

func newNonces() *nonces {
	return &nonces{byID: map[string]nonceEntry{}}
}

func (n *nonces) issue(user string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sweepLocked()
	n.byID[id] = nonceEntry{user: user, expiresAt: nowNanos() + nonceTTL}
	return id
}

// consume verifies and burns a nonce for user.
func (n *nonces) consume(id, user string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	e, ok := n.byID[id]
	if !ok || e.user != user || e.expiresAt < nowNanos() {
		return false
	}
	delete(n.byID, id)
	return true
}

func (n *nonces) sweepLocked() {
	now := nowNanos()
	for id, e := range n.byID {
		if e.expiresAt < now {
			delete(n.byID, id)
		}
	}
}

// challengeHeader renders the 401's WWW-Authenticate value.
func challengeHeader(realm, nonce string) sip.Header {
	return &sip.GenericHeader{
		HeaderName: "WWW-Authenticate",
		Contents:   fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5`, realm, nonce),
	}
}

// verifyDigest checks a request's Authorization header against the user's
// secret. Mirrors gb28181's server-side check: parse the digest params,
// seed the expected password/method, compare the computed response.
func verifyDigest(req sip.Request, username, secret string) bool {
	for _, h := range req.GetHeaders("Authorization") {
		g, ok := h.(*sip.GenericHeader)
		if !ok {
			continue
		}
		auth := sip.AuthFromValue(g.Contents)
		if auth == nil || auth.Username() != username {
			continue
		}
		if auth.Nonce() == "" || auth.Response() == "" {
			continue
		}
		auth.SetPassword(secret)
		auth.SetMethod(string(req.Method()))
		auth.SetPassword(secret)
		auth.SetMethod(string(req.Method()))
		return auth.CalcResponse() == auth.Response()
	}
	return false
}
