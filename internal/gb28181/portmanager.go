// Port pool for GB/T 28181 RTP media ports.
//
// Each media session (INVITE) pulls one RTP port from the pool; the pool prevents
// two sessions from binding the same port. Thread safety comes from the recycle
// channel plus an atomic next-pointer: Get() prefers a recycled port, then bumps
// the counter with a CAS loop (Monibuca reference pattern).

package gb28181

import (
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
)

// ErrNoAvailablePorts is returned by Get when every port in the pool is in use.
var ErrNoAvailablePorts = errors.New("gb28181: no available RTP ports")

// PortManager allocates and recycles RTP ports from a fixed inclusive [start, end] range.
type PortManager struct {
	start   uint16
	end     uint16
	recycle chan uint16
	next    atomic.Uint32 // next sequential port to hand out (advanced via CAS)
}

// NewPortManager returns a PortManager serving ports in [start, end] (inclusive).
// It panics on an invalid range (start > end) — callers validate via config.
func NewPortManager(start, end uint16) *PortManager {
	if start > end {
		panic(fmt.Sprintf("gb28181: invalid port range %d-%d", start, end))
	}
	pm := &PortManager{
		start:   start,
		end:     end,
		recycle: make(chan uint16, int(end)-int(start)+1),
	}
	pm.next.Store(uint32(start))
	return pm
}

// Get returns the next free port, preferring a recycled one.
func (pm *PortManager) Get() (uint16, error) {
	select {
	case p := <-pm.recycle:
		return p, nil
	default:
	}
	for {
		cur := pm.next.Load()
		if cur > uint32(pm.end) {
			return 0, ErrNoAvailablePorts
		}
		if pm.next.CompareAndSwap(cur, cur+1) {
			return uint16(cur), nil
		}
	}
}

// Recycle returns a port to the pool for reuse. The buffer holds exactly the pool
// size, so a full channel means a double-recycle of the same port — drop and log.
func (pm *PortManager) Recycle(p uint16) {
	select {
	case pm.recycle <- p:
	default:
		slog.Warn("gb28181: port recycle channel full, dropping recycled port", "port", p)
	}
}
