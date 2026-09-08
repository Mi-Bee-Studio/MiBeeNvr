// SPDX-License-Identifier: MIT

// Probe-connection adoption (#723): the ONVIF recorder probes MJPEG candidate
// URLs before handing over to the HTTP-JPEG recorder; on ESP32-class devices
// with anti-hammer guards, re-dialing the just-probed URL within the guard
// window (<5s between connections) arms the guard. The probe's open response
// must therefore become the recorder's stream connection — one TCP dial total.

package recorder

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// adoptTestJPEG carries valid JPEG magic bytes (SOI + EOI) so the recorder's
// frame validation accepts it.
var adoptTestJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x04, 0xFF, 0xD9}

func TestHTTPJPEGRecorderAdoptsProbedConnection(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		w.WriteHeader(http.StatusOK)
		for range 400 {
			fmt.Fprintf(w, "--frame\r\nContent-Length: %d\r\n\r\n", len(adoptTestJPEG))
			_, _ = w.Write(adoptTestJPEG)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	// The ONVIF probe flow: header-only wait on a detached context.
	probeClient := &http.Client{Timeout: 0, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, probeCancel, err := probeMJPEGHeaders(context.Background(), probeClient, srv.URL)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, int32(1), requests.Load(), "the probe must be the only dial so far")

	recordDisabled := false // live-only: exercise the frame path, skip segment I/O
	rec := NewHTTPJPEGRecorder(HTTPJPEGConfig{
		CameraID:      "adopt-cam",
		URL:           srv.URL,
		RecordEnabled: &recordDisabled,
	}, &mockSegmentStore{})
	rec.AdoptStream(resp, probeCancel)

	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(func() {
		stop()
		_ = rec.Stop()
	})
	require.NoError(t, rec.Start(ctx))

	require.Eventually(t, func() bool { return rec.LatestFrame() != nil },
		5*time.Second, 50*time.Millisecond, "frames must flow over the adopted connection")

	// The whole point (#723): the recorder must continue the probed
	// connection instead of dialing the same URL again.
	require.Equal(t, int32(1), requests.Load(), "recorder must not re-dial the just-probed URL")
}

func TestHTTPJPEGRecorderWithoutAdoptionDialsItself(t *testing.T) {
	// Control: with no adopted response the recorder dials normally.
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		w.WriteHeader(http.StatusOK)
		for range 400 {
			fmt.Fprintf(w, "--frame\r\nContent-Length: %d\r\n\r\n", len(adoptTestJPEG))
			_, _ = w.Write(adoptTestJPEG)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	recordDisabled := false
	rec := NewHTTPJPEGRecorder(HTTPJPEGConfig{
		CameraID:      "dial-cam",
		URL:           srv.URL,
		RecordEnabled: &recordDisabled,
	}, &mockSegmentStore{})

	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(func() {
		stop()
		_ = rec.Stop()
	})
	require.NoError(t, rec.Start(ctx))

	require.Eventually(t, func() bool { return rec.LatestFrame() != nil }, 5*time.Second, 50*time.Millisecond)
	require.Equal(t, int32(1), requests.Load(), "unadopted recorder dials exactly once itself")
}

func TestProbeMJPEGHeadersTimeoutCancelsRequest(t *testing.T) {
	// Server that accepts the connection but never answers: the probe must
	// give up after its header-wait budget and cancel the request, leaving
	// no adopted response behind.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until the client goes away
	}))
	t.Cleanup(srv.Close)

	probeClient := &http.Client{Timeout: 0, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, cancel, err := probeMJPEGHeaders(context.Background(), probeClient, srv.URL)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Nil(t, cancel, "on failure the cancel func is consumed internally")
	require.Contains(t, err.Error(), "timed out")
}

func TestProbeMJPEGHeadersCallerCancelReturnsPromptly(t *testing.T) {
	// Caller aborts mid-probe: the probe must surface the caller's error
	// immediately, never hand out an adopted response, and never hang. Whether
	// the detached dial completes underneath is a race — either way nothing is
	// returned to the caller.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		w.WriteHeader(http.StatusOK)
		<-r.Context().Done() // hold the stream open until the client goes away
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-expired caller context

	probeClient := &http.Client{Timeout: 0, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, probeCancel, err := probeMJPEGHeaders(ctx, probeClient, srv.URL)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, resp, "cancelled probe must not hand out a response")
	require.Nil(t, probeCancel, "cancelled probe must not hand out a cancel func")
}
