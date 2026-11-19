package hls

// On-demand sub-stream pull for HLS: the RTSP reader that feeds a sub-stream
// muxer, plus the H.264/H.265 depacketisers. Split out of manager.go.

import (
	"context"
	"errors"
	"fmt"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/pion/rtp"
)

// StartSubStreamReader starts a separate RTSP connection to a sub-stream URL for HLS.
// It connects to subStreamURL, extracts codec parameters (SPS/PPS for H264, VPS/SPS/PPS for H265),
// and feeds frames to the HLS muxer for the given camera.
// If the sub-stream connection fails, it logs a warning and returns — the caller should fall back to main stream.
func (m *Manager) StartSubStreamReader(cameraID, subStreamURL string, isH265 bool, fallbackFn func()) error {
	// Hold the write lock across the dedup check AND the subStreamCancel
	// assignment: readSubStream's exit path nils the same field under m.mu, so
	// an unlocked check-then-set here races a just-exited reader (a second
	// reader could be spawned on top of a live one). Caught by -race in the
	// sub-stream pump tests.
	m.mu.Lock()
	entry, ok := m.streams[cameraID]
	if !ok {
		m.mu.Unlock()
		return ErrStreamNotActive
	}
	if entry.subStreamCancel != nil {
		m.mu.Unlock()
		return nil // already running
	}

	ctx, cancel := context.WithCancel(m.ctx)
	entry.subStreamCancel = cancel
	m.mu.Unlock()

	go m.readSubStream(ctx, cameraID, subStreamURL, isH265, entry, fallbackFn)

	hlsLogger.Info("HLS sub-stream reader started", "camera_id", cameraID, "sub_stream_url", subStreamURL)
	return nil
}

func (m *Manager) readSubStream(ctx context.Context, cameraID, rtspURL string, isH265 bool, entry *streamEntry, fallbackFn func()) {
	var err error
	defer func() {
		m.mu.Lock()
		if e, ok := m.streams[cameraID]; ok {
			e.subStreamCancel = nil
		}
		m.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			hlsLogger.Warn("HLS sub-stream reader exited, falling back to main stream", "camera_id", cameraID, "error", err)
			if fallbackFn != nil {
				fallbackFn()
			}
		}
	}()

	u, parseErr := base.ParseURL(rtspURL)
	if parseErr != nil {
		err = fmt.Errorf("invalid sub-stream RTSP URL: %w", parseErr)
		return
	}

	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:   u.Scheme,
		Host:     u.Host,
		Protocol: &tcp,
	}

	if dialErr := client.Start(); dialErr != nil {
		err = fmt.Errorf("sub-stream client start: %w", dialErr)
		return
	}
	defer client.Close()

	desc, _, descErr := client.Describe(u)
	if descErr != nil {
		err = fmt.Errorf("sub-stream DESCRIBE: %w", descErr)
		return
	}

	if isH265 {
		err = m.readSubStreamH265(ctx, client, desc, cameraID, entry)
	} else {
		err = m.readSubStreamH264(ctx, client, desc, cameraID, entry)
	}
}

func (m *Manager) readSubStreamH264(ctx context.Context, client *gortsplib.Client, desc *description.Session, cameraID string, entry *streamEntry) error {
	var forma *format.H264
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("H264 media not found in sub-stream")
	}

	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("sub-stream create RTP decoder: %w", err)
	}

	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("sub-stream SETUP: %w", err)
	}

	errCh := make(chan error, 1)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		aus, decErr := rtpDec.Decode(pkt)
		if decErr != nil {
			if !errors.Is(decErr, rtph264.ErrNonStartingPacketAndNoPrevious) && !errors.Is(decErr, rtph264.ErrMorePacketsNeeded) {
				hlsLogger.Warn("sub-stream RTP decode error", "camera_id", cameraID, "error", decErr)
			}
			return
		}
		_ = m.WriteH264(cameraID, int64(pkt.Timestamp), aus)
	})

	if _, playErr := client.Play(nil); playErr != nil {
		return fmt.Errorf("sub-stream PLAY: %w", playErr)
	}

	go func() { errCh <- client.Wait() }()

	select {
	case <-ctx.Done():
		client.Close()
		return nil
	case err = <-errCh:
		return err
	}
}

func (m *Manager) readSubStreamH265(ctx context.Context, client *gortsplib.Client, desc *description.Session, cameraID string, entry *streamEntry) error {
	var forma *format.H265
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("H265 media not found in sub-stream")
	}

	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("sub-stream create RTP decoder: %w", err)
	}

	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("sub-stream SETUP: %w", err)
	}

	errCh := make(chan error, 1)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		aus, decErr := rtpDec.Decode(pkt)
		if decErr != nil {
			if !errors.Is(decErr, rtph265.ErrNonStartingPacketAndNoPrevious) && !errors.Is(decErr, rtph265.ErrMorePacketsNeeded) {
				hlsLogger.Warn("sub-stream RTP decode error", "camera_id", cameraID, "error", decErr)
			}
			return
		}
		_ = m.WriteH265(cameraID, int64(pkt.Timestamp), aus)
	})

	if _, playErr := client.Play(nil); playErr != nil {
		return fmt.Errorf("sub-stream PLAY: %w", playErr)
	}

	go func() { errCh <- client.Wait() }()

	select {
	case <-ctx.Done():
		client.Close()
		return nil
	case err = <-errCh:
		return err
	}
}
