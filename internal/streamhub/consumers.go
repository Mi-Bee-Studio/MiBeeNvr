package streamhub

// Consumer bookkeeping for the frame hub: the audio and video consumer entries,
// their bounded queues and the drain semantics that keep a slow subscriber from
// stalling the recording pipeline. Split out of streamhub.go.

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// audioFrameMsg is an internal audio frame representation passed through consumer channels.
type audioFrameMsg struct {
	pts   int64
	codec model.AudioCodec
	data  []byte
}

// audioConsumer holds a subscribed audio consumer with its own buffered channel,
// drain goroutine, and per-consumer drop counter.
type audioConsumer struct {
	cb     AudioCallback
	ch     chan audioFrameMsg
	done   chan struct{}
	drops  atomic.Int64
	sendMu sync.RWMutex
	closed bool
}

// drain reads audio frames from the consumer's channel and calls the callback.
func (c *audioConsumer) drain() {
	defer close(c.done)
	for msg := range c.ch {
		c.cb(msg.pts, msg.codec, msg.data)
	}
}

// queuedFrame pairs a frame with its enqueue time so the consumer's drain
// goroutine can measure queue dwell latency without touching the producer
// hot path.
type queuedFrame struct {
	msg        model.FrameMsg
	enqueuedAt int64 // unix nano
}

// consumerEntry holds a subscribed consumer with its own buffered channel,
// drain goroutine, and per-consumer counters.
// consumerRef is a snapshot entry for the per-frame distribution loops. The
// COW slices are rebuilt under h.mu on membership changes so the hot path
// pays one atomic load instead of a lock + map walk + alloc (#875 H6).
type consumerRef struct {
	id    string
	entry *consumerEntry
}

type audioRef struct {
	id    string
	entry *audioConsumer
}

type consumerEntry struct {
	cb           FrameCallback
	cbMsg        MsgCallback // set instead of cb for SubscribeMsg consumers
	ch           chan queuedFrame
	done         chan struct{} // closed when drain goroutine exits
	drops        atomic.Int64
	sends        atomic.Int64 // tracks successful sends for drop rate calculation
	idrDrops     atomic.Int64 // IDR frames lost in trySendIDR fallback
	bytes        atomic.Int64 // sum of NALU lengths delivered
	dwellSumNS   atomic.Int64 // cumulative enqueue→drain latency
	dwellCount   atomic.Int64
	dwellMaxNS   atomic.Int64
	subscribedAt time.Time
	lastSendAt   atomic.Int64 // unix nano of last successful enqueue
	sendMu       sync.RWMutex // protects ch from close-during-send race
	closed       bool
}

// drain reads frames from the consumer's channel and calls the callback.
// This decouples the Broadcast path from slow consumers. Dwell is measured
// here (consumer goroutine) rather than at enqueue so the producer path
// stays lock- and syscall-free.
func (e *consumerEntry) drain() {
	defer close(e.done)
	// Dwell stats are sampled 1-in-8 (#875 L5): three atomic ops per frame
	// bought no accuracy — the reported value is a running MEAN, and a
	// stationary-queue mean over a 1/8 sample is statistically identical.
	// dwellMaxNS stays unsampled so the max stays exact (one Load per frame,
	// CAS only on a new record).
	var frames int64
	for qf := range e.ch {
		frames++
		if ns := time.Now().UnixNano() - qf.enqueuedAt; ns > 0 {
			if frames%8 == 1 {
				e.dwellSumNS.Add(ns)
				e.dwellCount.Add(1)
			}
			for {
				cur := e.dwellMaxNS.Load()
				if ns <= cur || e.dwellMaxNS.CompareAndSwap(cur, ns) {
					break
				}
			}
		}
		if e.cbMsg != nil {
			e.cbMsg(qf.msg)
		} else {
			e.cb(qf.msg.PTS, qf.msg.AU)
		}
	}
}

// frameSize returns the total byte size of a frame's NAL units.
func frameSize(au [][]byte) int {
	n := 0
	for _, nalu := range au {
		n += len(nalu)
	}
	return n
}
