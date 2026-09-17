package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// buildCorruptMergeMP4 produces an mjpa merge whose samples are stub+image
// buffers (the RTSP double-header corruption) using the production GoMerger.
// Returns the output path and the clean source JPEGs for verification.
func buildCorruptMergeMP4(t *testing.T, dir string, n int) (string, [][]byte) {
	t.Helper()
	framesDir := filepath.Join(dir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var cleans [][]byte
	for i := range n {
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))
		for y := range 16 {
			for x := range 16 {
				img.Set(x, y, color.RGBA{R: uint8((x + i) * 8), G: uint8(y * 8), B: 64, A: 255})
			}
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
			t.Fatal(err)
		}
		clean := buf.Bytes()
		cleans = append(cleans, clean)
		// Synthesized-header stub + complete image — mirrors the ESP32/gortsplib
		// double-header buffers (no EOI in the stub).
		stub := append([]byte{0xFF, 0xD8, 0xFF, 0xDB, 0x00, 0x43, 0x00}, bytes.Repeat([]byte{0x11}, 64)...)
		stub = append(stub, 0xFF, 0xDA, 0x00, 0x08, 1, 0, 2, 3, 4)
		if err := os.WriteFile(filepath.Join(framesDir, "frame_"+string(rune('0'+i))+"0000.jpg"), append(stub, clean...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "periodic_test.mp4")
	res, err := timelapse.NewGoMerger().Merge(context.Background(), framesDir, out, 10)
	if err != nil {
		t.Fatalf("build corrupt merge: %v", err)
	}
	if res.FramesMerged != n {
		t.Fatalf("fixture merged %d frames, want %d", res.FramesMerged, n)
	}
	return out, cleans
}

func newRepairTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRepairOneTimelapseMerge_CleanFileUntouched(t *testing.T) {
	dir := t.TempDir()
	// A merge whose samples are already clean single JPEGs.
	framesDir := filepath.Join(dir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(framesDir, "frame_000000.jpg"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "clean.mp4")
	if _, err := timelapse.NewGoMerger().Merge(context.Background(), framesDir, out, 10); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(out)

	db := newRepairTestDB(t)
	id, err := db.InsertTimelapseMerge(context.Background(), &model.TimelapseMerge{
		CameraID: "cam-x", WindowStart: time.Now().UTC(), WindowEnd: time.Now().UTC().Add(time.Hour), DurationLabel: "1h",
		OutputPath: out, Codec: model.TimelapseMergeCodecMJPEG, FPS: 10,
		Status: model.TimelapseMergeStatusCompleted, FrameCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := db.GetTimelapseMerge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	done, err := repairOneTimelapseMerge(context.Background(), db, m, false)
	if err != nil {
		t.Fatalf("repair clean file: %v", err)
	}
	if done {
		t.Fatal("clean merge must be reported as skipped")
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(before, after) {
		t.Fatal("clean merge file must not be rewritten")
	}
}

func TestRepairOneTimelapseMerge_RewritesCorruptSamples(t *testing.T) {
	dir := t.TempDir()
	out, cleans := buildCorruptMergeMP4(t, dir, 3)

	db := newRepairTestDB(t)
	id, err := db.InsertTimelapseMerge(context.Background(), &model.TimelapseMerge{
		CameraID: "cam-x", WindowStart: time.Now().UTC(), WindowEnd: time.Now().UTC().Add(time.Hour), DurationLabel: "1h",
		OutputPath: out, Codec: model.TimelapseMergeCodecMJPEG, FPS: 10,
		Status: model.TimelapseMergeStatusCompleted, FrameCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := db.GetTimelapseMerge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	// Dry run: reports corruption, leaves the file untouched.
	corruptBefore, _ := os.ReadFile(out)
	dryDone, err := repairOneTimelapseMerge(context.Background(), db, m, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !dryDone {
		t.Fatal("corrupt merge must be detected")
	}
	if now, _ := os.ReadFile(out); !bytes.Equal(corruptBefore, now) {
		t.Fatal("dry run must not modify the file")
	}

	// Execute: rewrite happens.
	done, err := repairOneTimelapseMerge(context.Background(), db, m, false)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !done {
		t.Fatal("corrupt merge must be rewritten")
	}

	// Every sample is now a clean single JPEG (sanitize is a no-op on it).
	seg, err := merge.ParseSegment(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(seg.Samples) != len(cleans) {
		t.Fatalf("rewritten merge has %d samples, want %d", len(seg.Samples), len(cleans))
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i, s := range seg.Samples {
		buf := make([]byte, s.Size)
		if _, err := f.ReadAt(buf, s.Offset); err != nil {
			t.Fatalf("read sample %d: %v", i, err)
		}
		if !bytes.Equal(buf, cleans[i]) {
			t.Fatalf("sample %d not byte-identical to the salvaged source JPEG", i)
		}
		imgs := recorder.ExtractCompleteJPEGs(buf)
		if len(imgs) != 1 || !bytes.Equal(imgs[0], buf) {
			t.Fatalf("sample %d still not a clean single JPEG", i)
		}
	}

	// DB row refreshed.
	got, err := db.GetTimelapseMerge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.FrameCount != len(cleans) {
		t.Fatalf("DB frame count = %d, want %d", got.FrameCount, len(cleans))
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if got.FileSize != st.Size() {
		t.Fatalf("DB file size = %d, want %d", got.FileSize, st.Size())
	}
}

// buildTruncatedMergeMP4 produces a merge whose samples all end mid-entropy
// (no EOI) — the stream-EOF truncation variant. Salvage is near-zero, so the
// repair must refuse to rewrite rather than shrink the merge to a sliver.
func buildTruncatedMergeMP4(t *testing.T, dir string, n int) string {
	t.Helper()
	framesDir := filepath.Join(dir, "frames-trunc")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		img := image.NewRGBA(image.Rect(0, 0, 12, 12))
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
			t.Fatal(err)
		}
		stub := append([]byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08}, bytes.Repeat([]byte{0x22}, 16)...)
		var payload []byte
		if i == 0 {
			payload = buf.Bytes() // one salvageable sample
		} else {
			payload = buf.Bytes()[:len(buf.Bytes())-3] // drop EOI (+1 byte)
		}
		if err := os.WriteFile(filepath.Join(framesDir, "frame_00000"+string(rune('0'+i))+".jpg"), append(stub, payload...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "periodic_trunc.mp4")
	if _, err := timelapse.NewGoMerger().Merge(context.Background(), framesDir, out, 10); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRepairOneTimelapseMerge_SkipsRaggedSalvage(t *testing.T) {
	dir := t.TempDir()
	out := buildTruncatedMergeMP4(t, dir, 8)
	before, _ := os.ReadFile(out)

	db := newRepairTestDB(t)
	id, err := db.InsertTimelapseMerge(context.Background(), &model.TimelapseMerge{
		CameraID: "cam-x", WindowStart: time.Now().UTC(), WindowEnd: time.Now().UTC().Add(time.Hour),
		DurationLabel: "1h", OutputPath: out, Codec: model.TimelapseMergeCodecMJPEG, FPS: 10,
		Status: model.TimelapseMergeStatusCompleted, FrameCount: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := db.GetTimelapseMerge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	done, err := repairOneTimelapseMerge(context.Background(), db, m, false)
	if err != nil {
		t.Fatalf("ragged salvage: %v", err)
	}
	if done {
		t.Fatal("merge with near-zero salvage must be skipped, not rewritten")
	}
	if now, _ := os.ReadFile(out); !bytes.Equal(before, now) {
		t.Fatal("skipped merge must not be modified")
	}
}
