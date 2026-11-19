package webrtc

// Per-peer types for the WebRTC manager: the encoded-frame envelope, the peer
// entry holding a session's tracks and subscription, and the audio capability
// projection. Split out of manager.go.

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// peerEntry holds a single WHEP peer connection and its metadata.
// encodedFrame carries one annexB-encoded AU shared by every peer under a
// stream key (#875 M1): WriteH264 encodes once and the per-peer writeLoops
// only read the buffer (pion's packetizer copies into RTP payloads), so
// sharing across peers is safe — mirrors the FLV manager's single-tag pattern.
type encodedFrame struct {
	data  []byte
	pts   int64
	isIDR bool
}

type peerEntry struct {
	mu           sync.Mutex
	pc           *webrtc.PeerConnection
	track        *webrtc.TrackLocalStaticSample
	sender       *webrtc.RTPSender
	audioTrack   *webrtc.TrackLocalStaticSample // nil for video-only peers
	audioCh      chan model.AudioFrame          // nil for video-only peers
	audioClock   int                            // RTP clock rate for audio PTS→duration
	lastAudioPTS int64
	cancel       context.CancelFunc
	camID        string // owning camera (no quality suffix) — logging/metrics
	streamKey    string // camPeers/hubSubs key: camID or camID+streamhub.SubStreamKeySuffix (#513)
	sessionID    string
	// lastUsed is the last activity stamp in unix milliseconds (atomic — the
	// per-frame path must not take the entry mutex, #875 H3).
	lastUsed   atomic.Int64
	frameCh    chan encodedFrame
	drops      uint64             // atomic: total frames dropped due to buffer full
	framesSent prometheus.Counter // pre-resolved per-camera counter (#875 H5)
	congestion *congestionTracker // tracks drop rate for bitrate adaptation
	lastPTS    int64
}

// congestionTracker tracks frame send/drop rates in a sliding window
// to detect network congestion and trigger frame skipping.
//
// Concurrency: recordDropped (called from WriteH264 goroutine) and recordSent /
// shouldSkipFrame / dropRate (called from writeLoop goroutine) access these fields
// concurrently. All public methods hold mu for the duration of their critical section.
type congestionTracker struct {
	mu          sync.Mutex
	windowSize  int
	window      []bool // true = sent, false = dropped (circular buffer)
	windowPos   int
	windowCount int
	congested   bool
	skipCounter int // alternates skip pattern: 0=send, 1=skip
}
