package gb28181

import "net"

// setUDPReadBuffer enlarges the SO_RCVBUF of a GB28181 media socket. 2K IDRs
// arrive as multi-hundred-KB RTP bursts; the default buffer
// (net.core.rmem_default, typically 212KB) overflows mid-burst and the kernel
// silently drops packets — the receiver then gap-skips inside the keyframe and
// every downstream decoder error-conceals (green frames) until the next IDR.
// 4MB matches common net.core.rmem_max ceilings; the kernel silently caps the
// request when rmem_max is lower, so this is safe everywhere.
func setUDPReadBuffer(conn *net.UDPConn) {
	_ = conn.SetReadBuffer(4 << 20)
}
