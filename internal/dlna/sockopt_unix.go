//go:build !windows

package dlna

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// ssdpListenConfig allows several SSDP participants to share UDP 1900 on the
// same host (REUSEADDR/REUSEPORT are the UDA 1.1 recommendation for the
// multicast socket).
var ssdpListenConfig = net.ListenConfig{
	Control: func(network, address string, c syscall.RawConn) error {
		var opErr error
		err := c.Control(func(fd uintptr) {
			opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			if opErr != nil {
				return
			}
			opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		})
		if err != nil {
			return err
		}
		return opErr
	},
}
