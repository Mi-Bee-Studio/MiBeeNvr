package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestSSEReturnsOnStreamShutdown pins the tray-quit latency fix: SSE handler
// loops must return when CloseStreams fires (wired to http.Server's
// RegisterOnShutdown). Before this, httpSrv.Shutdown waited the full ctx
// timeout on never-idle SSE connections — a tray quit with the web UI open
// took the whole 30s to visibly do anything.
//
// Deliberately NOT parallel: it fires the package-level stream signal
// (reset before and after so the rest of the suite sees a fresh channel).
func TestSSEReturnsOnStreamShutdown(t *testing.T) {
	resetStreamShutdownForTest()
	t.Cleanup(resetStreamShutdownForTest)
	h, _ := setupEventsHandler(t)

	done := make(chan struct{})
	req := httptest.NewRequest(http.MethodGet, "/api/events?filter=", nil)
	req.RemoteAddr = "127.0.0.1:52000"
	req.Host = "127.0.0.1:9090"
	rec := httptest.NewRecorder() // implements Flusher
	go func() {
		h.handleEvents(rec, req)
		close(done)
	}()

	// Let the handler reach its streaming loop, then fire the shutdown
	// signal exactly like http.Server.RegisterOnShutdown would.
	time.Sleep(150 * time.Millisecond)
	CloseStreams()

	select {
	case <-done:
		// handler returned promptly — Shutdown will see the conn close.
	case <-time.After(2 * time.Second):
		t.Fatal("SSE handler did not return after CloseStreams")
	}
}
