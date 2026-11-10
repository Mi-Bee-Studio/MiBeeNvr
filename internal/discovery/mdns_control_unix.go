//go:build !windows

package discovery

import (
	"net"
	"syscall"
)

// mdnsJoinControl is a ListenConfig.Control that sets SO_REUSEADDR (bind
// alongside avahi/systemd-resolved that already hold 5353) and joins the
// mDNS group on EVERY up, multicast-capable, non-loopback interface — a
// multi-homed NAS (eth+bond+docker bridges) must answer on all of them.
// Deliberately NO SO_REUSEPORT: that would split datagrams with the
// resolvers, halving both sides' coverage.
func mdnsJoinControl(group string) func(network, address string, c syscall.RawConn) error {
	g := net.ParseIP(group).To4()
	return func(_, _ string, c syscall.RawConn) error {
		var firstErr error
		err := c.Control(func(fd uintptr) {
			if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil && firstErr == nil {
				firstErr = err
				return
			}
			if g == nil {
				return
			}
			ifaces, err := net.Interfaces()
			if err != nil && firstErr == nil {
				firstErr = err
				return
			}
			for _, ifi := range ifaces {
				if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 ||
					ifi.Flags&net.FlagMulticast == 0 {
					continue
				}
				addrs, err := ifi.Addrs()
				if err != nil {
					continue
				}
				for _, addr := range addrs {
					ipn, ok := addr.(*net.IPNet)
					if !ok {
						continue
					}
					ip := ipn.IP.To4()
					if ip == nil {
						continue
					}
					mreq := syscall.IPMreq{}
					copy(mreq.Multiaddr[:], g)
					copy(mreq.Interface[:], ip)
					if err := syscall.SetsockoptIPMreq(int(fd), syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, &mreq); err != nil && firstErr == nil {
						firstErr = err
					}
					break // one membership per interface
				}
			}
		})
		if err != nil {
			return err
		}
		return firstErr
	}
}
