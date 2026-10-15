// Package doorbell implements the optional SIP doorbell / intercom ingress
// (#971): standard SIP UA door stations register to the NVR's registrar
// (mode A) or reach it through a PBX the NVR registers to (mode B); their
// INVITE becomes a ring event, an answerable two-way G.711 talk bridge and
// an optional snapshot of the linked camera. Deliberately decoupled from
// internal/gb28181 — separate numbering, separate config, separate package.
package doorbell

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// Config is the resolved runtime config for the doorbell server.
type Config struct {
	Listen      string // ":5070"
	Realm       string
	Transport   string // udp | tcp | tls
	TLSCert     string
	TLSKey      string
	LANOnly     bool
	RingTimeout time.Duration
	Users       []User
	Outbound    *Outbound
}

// User is one door station account.
type User struct {
	Username string
	Secret   string
	Name     string
	CameraID string
}

// Outbound is mode B: register the NVR as a UA to an external PBX.
type Outbound struct {
	Server   string // host:port
	Username string
	Secret   string
	Expires  time.Duration
}

// ConfigFrom adapts the YAML-facing config section (defaults already
// applied by config.ApplyDefaults). Invalid durations fall back to safe
// values — validation reported them at load time.
func ConfigFrom(c config.DoorbellConfig) Config {
	cfg := Config{
		Listen:    c.Listen,
		Realm:     c.Realm,
		Transport: c.Transport,
		TLSCert:   c.TLSCert,
		TLSKey:    c.TLSKey,
		LANOnly:   c.LANOnlyEnabled(),
		Users:     make([]User, 0, len(c.Users)),
	}
	cfg.RingTimeout, _ = time.ParseDuration(c.RingTimeout)
	if cfg.RingTimeout <= 0 {
		cfg.RingTimeout = 60 * time.Second
	}
	for _, u := range c.Users {
		cfg.Users = append(cfg.Users, User{
			Username: u.Username,
			Secret:   u.Secret,
			Name:     u.Name,
			CameraID: u.CameraID,
		})
	}
	if c.Outbound != nil {
		o := &Outbound{
			Server:   c.Outbound.Server,
			Username: c.Outbound.Username,
			Secret:   c.Outbound.Secret,
		}
		o.Expires, _ = time.ParseDuration(c.Outbound.Expires)
		if o.Expires <= 0 {
			o.Expires = 300 * time.Second
		}
		cfg.Outbound = o
	}
	return cfg
}

// user looks up a configured door station by SIP username.
func (c Config) user(username string) (User, bool) {
	for _, u := range c.Users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}
