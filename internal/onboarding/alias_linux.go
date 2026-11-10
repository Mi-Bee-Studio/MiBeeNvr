//go:build linux

package onboarding

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// linuxAliasManager implements AliasManager with vishvananda/netlink
// (pure Go rtnetlink client). Adding or removing a secondary address
// requires CAP_NET_ADMIN; permission errors surface verbatim so the
// operator can fix the systemd unit.
type linuxAliasManager struct {
	mu   sync.Mutex
	held map[string]int // ip.String() -> ifIndex the alias was bound on
}

func newPlatformAliasManager() AliasManager {
	return &linuxAliasManager{held: make(map[string]int)}
}

// defaultRouteLink finds the link the IPv4 default route leaves through —
// the alias must ride the same NIC the cameras answer on.
func defaultRouteLink() (netlink.Link, error) {
	routes, err := netlink.RouteList(nil, unix.AF_INET)
	if err != nil {
		return nil, fmt.Errorf("route dump: %w", err)
	}
	for _, r := range routes {
		if r.Dst == nil && r.LinkIndex != 0 {
			return netlink.LinkByIndex(r.LinkIndex)
		}
	}
	return nil, errors.New("no IPv4 default route found")
}

func isExistsErr(err error) bool {
	return errors.Is(err, unix.EEXIST) || errors.Is(err, unix.EADDRNOTAVAIL) ||
		strings.Contains(err.Error(), "exists")
}

func isGoneErr(err error) bool {
	return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EADDRNOTAVAIL)
}

func (l *linuxAliasManager) Up(ip net.IP, prefixLen int) error {
	ip = ip.To4()
	if ip == nil {
		return fmt.Errorf("alias address must be IPv4, got %v", ip)
	}

	l.mu.Lock()
	if _, ok := l.held[ip.String()]; ok {
		l.mu.Unlock()
		return nil // idempotent
	}
	l.mu.Unlock()

	link, err := defaultRouteLink()
	if err != nil {
		return err
	}
	addr := &netlink.Addr{
		IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(prefixLen, 32)},
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		if isExistsErr(err) {
			// Already bound (a previous run left it, or the operator did) —
			// treat as success and remember it so Down works.
			l.mu.Lock()
			l.held[ip.String()] = link.Attrs().Index
			l.mu.Unlock()
			return nil
		}
		return fmt.Errorf("add alias %s/%d on %s: %w (needs CAP_NET_ADMIN)", ip, prefixLen, link.Attrs().Name, err)
	}

	l.mu.Lock()
	l.held[ip.String()] = link.Attrs().Index
	l.mu.Unlock()
	return nil
}

func (l *linuxAliasManager) Down(ip net.IP) error {
	ip = ip.To4()
	if ip == nil {
		return nil
	}

	l.mu.Lock()
	idx, ok := l.held[ip.String()]
	l.mu.Unlock()
	if !ok {
		return nil // never bound — nothing to do
	}

	link, err := netlink.LinkByIndex(idx)
	if err == nil {
		addr := &netlink.Addr{
			IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)},
		}
		if derr := netlink.AddrDel(link, addr); derr != nil && !isGoneErr(derr) && !isExistsErr(derr) {
			l.mu.Lock()
			delete(l.held, ip.String())
			l.mu.Unlock()
			return fmt.Errorf("remove alias %s: %w", ip, derr)
		}
	}

	l.mu.Lock()
	delete(l.held, ip.String())
	l.mu.Unlock()
	return nil
}

func (l *linuxAliasManager) UpHeld(ip net.IP) bool {
	if ip == nil || ip.To4() == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.held[ip.To4().String()]
	return ok
}
