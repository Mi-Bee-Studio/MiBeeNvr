// SPDX-License-Identifier: MIT

// OnStreamSessionStart hook (#921): the callback fires once per successful
// RTSP PLAY — the initial connect and every reconnect both land there — so a
// single hook covers "request IDR at stream start" and "request IDR after
// recovery" without the codecs knowing about ONVIF.

package recorder

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

func TestH264Recorder_OnStreamSessionStartFiresAfterPlay(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	fired := make(chan struct{}, 4)
	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:             "cam-hook-h264",
		RTSPURL:              srv.rtspURL,
		SegmentDur:           5 * time.Minute,
		OnStreamSessionStart: func() { fired <- struct{}{} },
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, rec.Start(ctx))
	defer rec.Stop()

	srv.waitPlay(t, 5*time.Second)
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("OnStreamSessionStart must fire after PLAY succeeds")
	}
	require.Equal(t, model.StatusRecording, rec.Status())
}

func TestH265Recorder_OnStreamSessionStartFiresAfterPlay(t *testing.T) {
	srv := newTestRTSPServerH265(t)
	defer srv.close()

	fired := make(chan struct{}, 4)
	mgr := newTestManager(t)
	rec := NewH265Recorder(H265Config{
		CameraID:             "cam-hook-h265",
		RTSPURL:              srv.rtspURL,
		SegmentDur:           5 * time.Minute,
		OnStreamSessionStart: func() { fired <- struct{}{} },
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, rec.Start(ctx))
	defer rec.Stop()

	srv.waitPlay(t, 5*time.Second)
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("OnStreamSessionStart must fire after PLAY succeeds")
	}
	require.Equal(t, model.StatusRecording, rec.Status())
}
