package wsstream

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// TestRebindHub verifies that a registered stream follows a NEW hub after
// RebindHub: frames on the old hub stop being forwarded and frames on the new
// hub flow. This is the recorder-reconnect path — without the rebind, viewers
// sit on the dead hub with zero frames forever (observed on flaky MJPEG cams).
func TestRebindHub(t *testing.T) {
	m := NewManager()
	oldHub := newTestHub(t)
	newHub := newTestHub(t)

	if err := m.RegisterStream("cam1", model.FormatMJPEG, nil, nil, nil, oldHub); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := m.ActiveHub("cam1"); got != oldHub {
		t.Fatal("ActiveHub must return the registered hub")
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	conn := dialWS(t, "ws"+srv.URL[4:])
	defer conn.Close()
	waitForViewer(t, m, "cam1")

	// Frames on the OLD hub flow before the rebind (first message is codec
	// info — skip it).
	if _, err := readMessage(t, conn); err != nil {
		t.Fatalf("codec-info message: %v", err)
	}
	broadcastFrame(t, oldHub, 1000, [][]byte{{0xFF, 0xD8, 0x00}})
	if _, err := readMessage(t, conn); err != nil {
		t.Fatalf("frame from old hub before rebind: %v", err)
	}

	// Rebind to the NEW hub (recorder reconnected).
	m.RebindHub("cam1", newHub)
	if got := m.ActiveHub("cam1"); got != newHub {
		t.Fatal("ActiveHub must return the rebound hub")
	}

	// Frames on the old hub must no longer reach the viewer, frames on the new
	// hub must. (A read-timeout would poison the gorilla connection, so assert
	// by CONTENT: broadcast a stale marker on the old hub, then the real frame
	// on the new hub — the next message the viewer receives must be the new
	// hub's, not the stale one.)
	broadcastFrame(t, oldHub, 2000, [][]byte{{0xAA}})
	broadcastFrame(t, newHub, 3000, [][]byte{{0xFF, 0xD8, 0x01}})
	msg, err := readMessage(t, conn)
	if err != nil {
		t.Fatalf("frame after rebind: %v", err)
	}
	for _, b := range msg {
		if b == 0xAA {
			t.Fatalf("stale frame from old hub leaked after rebind: % X", msg)
		}
	}
	// The next message (if any) also must not carry the stale marker.
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		_, msg2, err := conn.ReadMessage()
		if err != nil {
			break // timeout/closed — done
		}
		for _, b := range msg2 {
			if b == 0xAA {
				t.Fatalf("stale frame from old hub leaked after rebind (queued): % X", msg2)
			}
		}
	}
}

// TestRebindHubUnknownStream is a no-op for unregistered cameras.
func TestRebindHubUnknownStream(t *testing.T) {
	m := NewManager()
	m.RebindHub("nope", newTestHub(t)) // must not panic or register
	if m.IsActive("nope") {
		t.Fatal("rebind must not create an entry")
	}
}
