package doorbell

import (
	"net"
	"sync"
	"time"
)

func nowNanos() int64 { return time.Now().UnixNano() }

// registration is one door station's current REGISTER binding.
type registration struct {
	user      string
	source    string // ip:port the REGISTER arrived from
	expiresAt time.Time
}

// registry tracks mode-A registrations: which door station is online and
// where its SIP signaling comes from. INVITEs must come from a registered
// user — and, when the registrar is local (mode A), from the same source
// IP that registered (spoofing From on an unregistered socket gets 403).
type registry struct {
	mu     sync.Mutex
	byUser map[string]*registration
}

func newRegistry() *registry {
	return &registry{byUser: map[string]*registration{}}
}

func (r *registry) put(user, source string, ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byUser[user] = &registration{
		user:      user,
		source:    source,
		expiresAt: time.Now().Add(ttl),
	}
}

func (r *registry) remove(user string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byUser, user)
}

// registered reports whether user has a live binding; sourceIP (when
// non-empty) must match the binding's source IP.
func (r *registry) registered(user, sourceIP string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.byUser[user]
	if !ok || time.Now().After(reg.expiresAt) {
		return false
	}
	if sourceIP == "" {
		return true
	}
	return hostOf(reg.source) == sourceIP
}

func (r *registry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, reg := range r.byUser {
		if time.Now().Before(reg.expiresAt) {
			n++
		}
	}
	return n
}

// hostOf splits "ip:port" into ip.
func hostOf(source string) string {
	if h, _, err := net.SplitHostPort(source); err == nil {
		return h
	}
	return source
}
