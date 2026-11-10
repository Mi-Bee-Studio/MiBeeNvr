//go:build windows

package discovery

import (
	"net"
	"syscall"
)

// mdnsJoinControl is the Windows counterpart (see mdns_control_unix.go for
// the full rationale). Windows has no IP_ADD_MEMBERSHIP via the generic
// RawConn syscall surface here and product deployments are Linux-only, so
// this variant sets SO_REUSEADDR only — best-effort announcement on dev
// boxes; absence of the group join degrades to a plain bind, which is the
// honest behavior.
func mdnsJoinControl(group string) func(network, address string, c syscall.RawConn) error {
	_ = net.ParseIP(group)
	return func(_, _ string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			const soReuseAddr = 4 // SO_REUSEADDR on Windows
			_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, soReuseAddr, 1)
		})
		if err != nil {
			return err
		}
		return sockErr
	}
}
