//go:build windows

package dlna

import (
	"net"
	"syscall"

	"golang.org/x/sys/windows"
)

// ssdpListenConfig allows several SSDP participants to share UDP 1900 on the
// same host. Windows has no SO_REUSEPORT; SO_REUSEADDR alone permits the
// multicast bind.
var ssdpListenConfig = net.ListenConfig{
	Control: func(network, address string, c syscall.RawConn) error {
		var opErr error
		err := c.Control(func(fd uintptr) {
			opErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
		})
		if err != nil {
			return err
		}
		return opErr
	},
}
