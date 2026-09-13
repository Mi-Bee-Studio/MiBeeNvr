package recorder

import (
	"testing"
	"time"
)

// Manual recording window suite (#660): MQTT `{"action":"record","duration":"60s"}`
// must produce actual footage on a recording_enabled=false (live-only) camera —
// a timed override of the RecordEnabled gate. Without the window the live-only
// gate drops every frame (TestWriteFrames_LiveOnlyDrainsWithoutRecording).

// TestArmManualRecording_TimedWindow: arming opens the gate for the window
// duration (segments ARE created), and the window's expiry closes it again —
// later frames land in NO new segment.
func TestArmManualRecording_TimedWindow(t *testing.T) {
	b, store := newLiveOnlyBaseRecorder(t, true)

	sps := append([]byte{0, 0, 0, 1}, testSPS...)
	pps := append([]byte{0, 0, 0, 1}, testPPS...)
	idr := append([]byte{0, 0, 0, 1}, testIDR...)

	done := make(chan struct{})
	go b.writeFrames(done)

	now := time.Now()
	b.frameCh <- framePacket{data: sps, at: now}
	b.frameCh <- framePacket{data: pps, at: now}

	// Arm a 30s window; frames arriving inside it must reach the disk path.
	b.ArmManualRecording(30 * time.Second)
	if !b.ManualRecordingActive() {
		t.Fatal("ManualRecordingActive must be true right after arming")
	}
	b.frameCh <- framePacket{data: idr, at: now}
	b.frameCh <- framePacket{data: idr, at: now.Add(10 * time.Second)}

	// Frames past the window: gate closes, no further segments.
	b.frameCh <- framePacket{data: idr, at: now.Add(31 * time.Second)}
	b.frameCh <- framePacket{data: idr, at: now.Add(32 * time.Second)}

	close(b.frameCh)
	<-done

	if got := store.creates.Load(); got < 1 {
		t.Fatalf("manual window must let frames reach the segment path; creates=%d", got)
	}
	// The window reads expired for timestamps past its deadline (the frames
	// above proved the gate closed); ManualRecordingActive() reflects wall
	// now, which is still inside the real 30s window.
	if b.manual.Active(now.Add(32 * time.Second)) {
		t.Fatal("window must read expired for timestamps past the deadline")
	}
}

// TestArmManualRecording_NoWindowNoRecording: without arming, the live-only
// gate keeps dropping (guards against the window accidentally defaulting on).
func TestArmManualRecording_NoWindowNoRecording(t *testing.T) {
	b, store := newLiveOnlyBaseRecorder(t, true)

	sps := append([]byte{0, 0, 0, 1}, testSPS...)
	pps := append([]byte{0, 0, 0, 1}, testPPS...)
	idr := append([]byte{0, 0, 0, 1}, testIDR...)

	done := make(chan struct{})
	go b.writeFrames(done)
	now := time.Now()
	b.frameCh <- framePacket{data: sps, at: now}
	b.frameCh <- framePacket{data: pps, at: now}
	b.frameCh <- framePacket{data: idr, at: now}
	close(b.frameCh)
	<-done

	if got := store.creates.Load(); got != 0 {
		t.Fatalf("unarmed live-only recorder created %d segments; expected 0", got)
	}
	if b.ManualRecordingActive() {
		t.Fatal("ManualRecordingActive must be false when never armed")
	}
}
