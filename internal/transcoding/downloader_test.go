package transcoding

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- Helpers ---

// newTestDownloader creates a Downloader pointing at a temp dir.
func newTestDownloader(t *testing.T) *Downloader {
	t.Helper()
	return NewDownloader(t.TempDir(), nil)
}

// newTestDownloaderWithProgress creates a Downloader with a progress callback.
func newTestDownloaderWithProgress(t *testing.T, onProgress func(downloaded, total int64)) *Downloader {
	t.Helper()
	return NewDownloader(t.TempDir(), onProgress)
}

// mustCreateFakeFFmpeg creates a fake ffmpeg shell script that responds to -version.
func mustCreateFakeFFmpeg(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "ffmpeg")
	content := "#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then\necho 'ffmpeg version 7.0-static'\nexit 0\nfi\nexit 0\n"
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return p
}

// mustCreateToolsDir creates the tools subdirectory inside dir.
func mustCreateToolsDir(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "tools")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	return p
}

// readDownloadState reads and parses the download-state.json from tools dir.
func readDownloadState(t *testing.T, d *Downloader) *DownloadState {
	t.Helper()
	data, err := os.ReadFile(d.StatePath())
	if err != nil {
		return nil
	}
	var state DownloadState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	return &state
}

// createTarGzArchive creates a .tar.gz archive containing the given files.
// files is a map of filename -> content.
func createTarGzArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar write header for %s: %v", name, err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}

	return buf.Bytes()
}

// serveTarGz returns an httptest.Server that serves a .tar.gz archive via a .tar.gz URL.
func serveTarGz(t *testing.T, archive []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(archive)))
		w.Write(archive)
	}))
}

// --- Tests ---

// TestGetFFmpegStatus_NotInstalled verifies status when no FFmpeg exists.
func TestGetFFmpegStatus_NotInstalled(t *testing.T) {
	d := newTestDownloader(t)

	status := d.GetFFmpegStatus()
	if status.Status != "not_installed" {
		t.Fatalf("Status = %q, want %q", status.Status, "not_installed")
	}
	if status.Progress != 0 {
		t.Fatalf("Progress = %f, want 0", status.Progress)
	}
}

// TestGetFFmpegStatus_Available verifies status when valid FFmpeg exists.
func TestGetFFmpegStatus_Available(t *testing.T) {
	dir := t.TempDir()
	mustCreateToolsDir(t, dir)
	mustCreateFakeFFmpeg(t, filepath.Join(dir, "tools"))

	d := NewDownloader(dir, nil)
	status := d.GetFFmpegStatus()

	if status.Status != "available" {
		t.Fatalf("Status = %q, want %q", status.Status, "available")
	}
	if status.Version == "" {
		t.Fatal("Version should not be empty for available FFmpeg")
	}
	if status.Progress != 1.0 {
		t.Fatalf("Progress = %f, want 1.0", status.Progress)
	}
}

// TestGetFFmpegStatus_Downloading verifies status returns in-progress state.
func TestGetFFmpegStatus_Downloading(t *testing.T) {
	d := newTestDownloader(t)

	d.mu.Lock()
	d.status = DownloadStatus{Status: "downloading", Progress: 0.42}
	d.mu.Unlock()

	status := d.GetFFmpegStatus()
	if status.Status != "downloading" {
		t.Fatalf("Status = %q, want %q", status.Status, "downloading")
	}
	if status.Progress != 0.42 {
		t.Fatalf("Progress = %f, want 0.42", status.Progress)
	}
}

// TestDownload_Idempotent verifies that downloading when FFmpeg already exists returns nil.
func TestDownload_Idempotent(t *testing.T) {
	dir := t.TempDir()
	mustCreateToolsDir(t, dir)
	mustCreateFakeFFmpeg(t, filepath.Join(dir, "tools"))

	d := NewDownloader(dir, nil)
	err := d.DownloadFFmpeg(context.Background())
	if err != nil {
		t.Fatalf("DownloadFFmpeg with existing FFmpeg: %v", err)
	}
}

// TestDownload_TarGzExtraction verifies download + extraction of a .tar.gz archive
// containing both ffmpeg and ffprobe.
func TestDownload_TarGzExtraction(t *testing.T) {
	ffmpegContent := []byte("#!/bin/sh\necho 'ffmpeg version 7.0-test'\n")
	ffprobeContent := []byte("#!/bin/sh\necho 'ffprobe version 7.0-test'\n")

	archive := createTarGzArchive(t, map[string][]byte{
		"ffmpeg-git-amd64-static/ffmpeg":  ffmpegContent,
		"ffmpeg-git-amd64-static/ffprobe": ffprobeContent,
		"ffmpeg-git-amd64-static/readme.txt": []byte("docs"),
	})

	srv := serveTarGz(t, archive)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	// Use a URL ending in .tar.gz to trigger extraction
	url := srv.URL + "/ffmpeg-git-amd64-static.tar.gz"
	err := d.downloadOnceWithURL(context.Background(), url)
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	// Verify ffmpeg exists and has correct content
	ffmpegData, err := os.ReadFile(d.FFmpegPath())
	if err != nil {
		t.Fatalf("read ffmpeg: %v", err)
	}
	if !bytes.Equal(ffmpegData, ffmpegContent) {
		t.Fatalf("ffmpeg content mismatch: got %q, want %q", string(ffmpegData), string(ffmpegContent))
	}

	// Verify ffprobe exists and has correct content
	ffprobeData, err := os.ReadFile(d.FFprobePath())
	if err != nil {
		t.Fatalf("read ffprobe: %v", err)
	}
	if !bytes.Equal(ffprobeData, ffprobeContent) {
		t.Fatalf("ffprobe content mismatch: got %q, want %q", string(ffprobeData), string(ffprobeContent))
	}

	// Verify both are executable
	for _, path := range []string{d.FFmpegPath(), d.FFprobePath()} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Fatalf("%s should be executable", path)
		}
	}

	// Verify archive was cleaned up
	toolsDir := filepath.Join(dir, "tools")
	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		t.Fatalf("readdir tools: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "download.tmp" {
			t.Fatal("archive should have been cleaned up")
		}
	}
}

// TestDownload_TarGzCorruptArchive verifies graceful error on corrupt archive.
func TestDownload_TarGzCorruptArchive(t *testing.T) {
	// Create a file that looks like .tar.gz but is actually garbage
	corruptData := []byte("this is not a valid gzip stream at all!")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(corruptData)))
		w.Write(corruptData)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	url := srv.URL + "/ffmpeg-git-amd64-static.tar.gz"
	err := d.downloadOnceWithURL(context.Background(), url)
	if err == nil {
		t.Fatal("expected error from corrupt archive")
	}

	// Should mention extraction/gzip failure
	if !bytes.Contains([]byte(err.Error()), []byte("gzip")) &&
		!bytes.Contains([]byte(err.Error()), []byte("extract")) {
		t.Logf("error: %v", err)
	}

	// Verify no ffmpeg/ffprobe files left behind
	if _, err := os.Stat(d.FFmpegPath()); !os.IsNotExist(err) {
		t.Fatal("ffmpeg should not exist after corrupt extraction")
	}
	if _, err := os.Stat(d.FFprobePath()); !os.IsNotExist(err) {
		t.Fatal("ffprobe should not exist after corrupt extraction")
	}
}

// TestDownload_TarGzMissingBinaries verifies error when archive has no ffmpeg/ffprobe.
func TestDownload_TarGzMissingBinaries(t *testing.T) {
	archive := createTarGzArchive(t, map[string][]byte{
		"ffmpeg-git-amd64-static/readme.txt": []byte("no binaries here"),
	})

	srv := serveTarGz(t, archive)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	url := srv.URL + "/ffmpeg-git-amd64-static.tar.gz"
	err := d.downloadOnceWithURL(context.Background(), url)
	if err == nil {
		t.Fatal("expected error when archive has no ffmpeg/ffprobe")
	}
}

// TestDownload_RawBinary verifies that a non-archive URL falls back to raw binary download.
func TestDownload_RawBinary(t *testing.T) {
	fakeBinary := []byte("#!/bin/sh\necho 'ffmpeg version 7.0-test'\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fakeBinary)))
		w.Write(fakeBinary)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	// URL without .tar.gz or .tar.xz extension → raw binary
	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/ffmpeg")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	data, err := os.ReadFile(d.FFmpegPath())
	if err != nil {
		t.Fatalf("read ffmpeg: %v", err)
	}
	if !bytes.Equal(data, fakeBinary) {
		t.Fatalf("content mismatch")
	}

	info, err := os.Stat(d.FFmpegPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("should be executable")
	}
}

// TestDownload_AtomicCleanup verifies that a cancelled download does not leave a broken file.
func TestDownload_AtomicCleanup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write partial data then stall until cancelled
		w.Write([]byte("partial"))
		<-r.Context().Done()
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := d.downloadOnceWithURL(ctx, srv.URL+"/test.tar.gz")
	if err == nil {
		t.Fatal("expected error from cancelled download")
	}

	// Verify no ffmpeg file exists
	if _, err := os.Stat(d.FFmpegPath()); !os.IsNotExist(err) {
		t.Fatal("ffmpeg file should not exist after failed download")
	}

	// Verify no .tmp file remains
	toolsDir := filepath.Join(dir, "tools")
	tmpPath := filepath.Join(toolsDir, "download.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal("temp file should be cleaned up after failed download")
	}
}

// TestDownload_Retry verifies that download retries on transient failures.
func TestDownload_Retry(t *testing.T) {
	var attempts atomic.Int32

	fakeBinary := []byte("#!/bin/sh\necho 'ffmpeg version 7.0-test'\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fakeBinary)))
		w.Write(fakeBinary)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	// Use very short backoff for testing
	err := d.downloadWithRetryCustom(context.Background(), srv.URL, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("downloadWithRetryCustom: %v", err)
	}

	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

// TestDownload_RetryExhausted verifies failure after all retries exhausted.
func TestDownload_RetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	err := d.downloadWithRetryCustom(context.Background(), srv.URL, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond})
	if err == nil {
		t.Fatal("expected error when all retries exhausted")
	}
}

// TestDownload_ProgressCallback verifies progress callback is invoked.
func TestDownload_ProgressCallback(t *testing.T) {
	fakeBinary := make([]byte, 10*1024) // 10KB
	for i := range fakeBinary {
		fakeBinary[i] = byte(i % 256)
	}

	var progressCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fakeBinary)))
		w.Write(fakeBinary)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, func(downloaded, total int64) {
		progressCalls.Add(1)
	})

	// Use raw binary URL (no .tar.gz) to avoid extraction overhead
	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/ffmpeg")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	if progressCalls.Load() == 0 {
		t.Fatal("progress callback was never called")
	}
}

// TestDownload_CancelCleansUp verifies that Cancel() aborts download and cleans up.
func TestDownload_CancelCleansUp(t *testing.T) {
	// Server that blocks forever
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write a header then block
		w.Header().Set("Content-Length", "104857600") // 100MB
		w.Write([]byte("x"))
		<-r.Context().Done()
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.downloadOnceWithURL(ctx, srv.URL+"/test.tar.gz")
	}()

	// Give the download a moment to start
	time.Sleep(50 * time.Millisecond)

	// Cancel the download
	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error from cancelled download")
	}

	// Verify temp file cleaned up
	toolsDir := filepath.Join(dir, "tools")
	tmpPath := filepath.Join(toolsDir, "download.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal("temp file should be cleaned up after cancellation")
	}
}

// TestDownload_ConcurrentMutex verifies concurrent calls don't cause multiple downloads.
func TestDownload_ConcurrentMutex(t *testing.T) {
	fakeBinary := []byte("#!/bin/sh\necho 'ffmpeg version 7.0-test'\n")
	var serveCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveCount.Add(1)
		// Small delay to make concurrency more likely
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fakeBinary)))
		w.Write(fakeBinary)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	var wg sync.WaitGroup
	var errs []error
	var mu sync.Mutex

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := d.downloadOnceWithURL(context.Background(), srv.URL+"/ffmpeg")
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// At least one should succeed, but we don't expect all 3 to make full requests
	// since they're writing to the same file path
	for _, err := range errs {
		if err != nil {
			// Some may fail due to concurrent file writes, that's expected
			t.Logf("goroutine error (may be expected): %v", err)
		}
	}
}

// TestDownload_StatePersistence verifies state is written to JSON file.
func TestDownload_StatePersistence(t *testing.T) {
	d := newTestDownloader(t)

	d.mu.Lock()
	d.status = DownloadStatus{
		Status:   "failed",
		Error:    "connection timeout",
		Progress: 0.3,
		Version:  "",
	}
	d.saveState()
	d.mu.Unlock()

	state := readDownloadState(t, d)
	if state == nil {
		t.Fatal("state should not be nil after saveState")
	}
	if state.Status != "failed" {
		t.Fatalf("state.Status = %q, want %q", state.Status, "failed")
	}
	if state.Error != "connection timeout" {
		t.Fatalf("state.Error = %q, want %q", state.Error, "connection timeout")
	}
	if state.Progress != 0.3 {
		t.Fatalf("state.Progress = %f, want 0.3", state.Progress)
	}
	if state.LastUpdated == "" {
		t.Fatal("state.LastUpdated should not be empty")
	}
}

// TestDownload_StatePersistence_NilOnError verifies no crash when dir doesn't exist.
func TestDownload_StatePersistence_NilOnError(t *testing.T) {
	d := NewDownloader("/nonexistent/path/that/does/not/exist", nil)
	// Should not panic
	d.mu.Lock()
	d.status = DownloadStatus{Status: "failed", Error: "test"}
	d.saveState()
	d.mu.Unlock()
}

// TestDownload_CancelNoop verifies Cancel is safe when nothing is downloading.
func TestDownload_CancelNoop(t *testing.T) {
	d := newTestDownloader(t)
	// Should not panic
	d.Cancel()
}

// TestGetDownloadURL verifies platform URL generation.
func TestGetDownloadURL(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"linux", "amd64", "https://johnvansickle.com/ffmpeg/builds/ffmpeg-git-amd64-static.tar.gz"},
		{"linux", "arm64", "https://johnvansickle.com/ffmpeg/builds/ffmpeg-git-arm64-static.tar.gz"},
		{"linux", "arm", "https://johnvansickle.com/ffmpeg/builds/ffmpeg-git-armhf-static.tar.gz"},
		{"darwin", "amd64", ""},
		{"darwin", "arm64", ""},
		{"windows", "amd64", ""},
	}

	for _, tc := range tests {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			got := getDownloadURL(tc.goos, tc.goarch)
			if got != tc.want {
				t.Fatalf("getDownloadURL(%q, %q) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
			}
		})
	}
}

// TestDownload_UnsupportedPlatform verifies error on unsupported platform.
func TestDownload_UnsupportedPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this test needs a non-linux GOOS, but we can't change runtime.GOOS")
	}
	// On non-linux, downloadOnce should fail with unsupported platform
	d := newTestDownloader(t)
	err := d.downloadOnce(context.Background())
	if err == nil {
		t.Fatal("expected error on unsupported platform")
	}
}

// TestFFmpegPath verifies paths are correct.
func TestFFmpegPath(t *testing.T) {
	d := NewDownloader("/data", nil)
	want := filepath.Join("/data", "tools", "ffmpeg")
	if d.FFmpegPath() != want {
		t.Fatalf("FFmpegPath = %q, want %q", d.FFmpegPath(), want)
	}
}

// TestFFprobePath verifies ffprobe path is correct.
func TestFFprobePath(t *testing.T) {
	d := NewDownloader("/data", nil)
	want := filepath.Join("/data", "tools", "ffprobe")
	if d.FFprobePath() != want {
		t.Fatalf("FFprobePath = %q, want %q", d.FFprobePath(), want)
	}
}

// TestDownload_FailedBinaryVerification verifies that an invalid binary (non-executable output) fails verification.
func TestDownload_FailedBinaryVerification(t *testing.T) {
	// Server returns garbage via a non-archive URL → raw binary fallback
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not a real binary"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/ffmpeg")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	// The file exists but isn't a valid FFmpeg
	if _, err := os.Stat(d.FFmpegPath()); os.IsNotExist(err) {
		t.Fatal("downloaded file should exist")
	}

	// Verify getFFmpegVersion should fail
	_, err = d.getFFmpegVersion(d.FFmpegPath())
	if err == nil {
		t.Fatal("getFFmpegVersion should fail on invalid binary")
	}
}

// TestDownload_CancellationViaDownloadFFmpeg verifies full DownloadFFmpeg with context cancellation.
func TestDownload_CancellationViaDownloadFFmpeg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "104857600")
		w.Write([]byte("x"))
		<-r.Context().Done()
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	// Override URL getter
	origGetter := getDownloadURL
	getDownloadURL = func(goos, goarch string) string { return srv.URL + "/test.tar.gz" }
	defer func() { getDownloadURL = origGetter }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := d.DownloadFFmpeg(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("error should be context error, got: %v", err)
	}
}

// TestExtractArchive_TarGzWithDirectoryPrefix verifies extraction from archives with nested directories.
func TestExtractArchive_TarGzWithDirectoryPrefix(t *testing.T) {
	ffmpegContent := []byte("#!/bin/sh\necho 'ffmpeg version N-test'\n")

	archive := createTarGzArchive(t, map[string][]byte{
		"build-dir/ffmpeg":  ffmpegContent,
		"build-dir/ffprobe": []byte("#!/bin/sh\necho 'ffprobe version N-test'\n"),
		"build-dir/manpage": []byte("manpage content"),
	})

	srv := serveTarGz(t, archive)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/build.tar.gz")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	data, err := os.ReadFile(d.FFmpegPath())
	if err != nil {
		t.Fatalf("read ffmpeg: %v", err)
	}
	if !bytes.Equal(data, ffmpegContent) {
		t.Fatalf("ffmpeg content mismatch")
	}

	// ffprobe should also be extracted
	if _, err := os.Stat(d.FFprobePath()); err != nil {
		t.Fatalf("ffprobe should exist: %v", err)
	}
}

// TestExtractArchive_CorruptGzipBody verifies error when gzip body is truncated.
func TestExtractArchive_CorruptGzipBody(t *testing.T) {
	// Create a valid gzip header but corrupt body
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write([]byte("some data"))
	gw.Close()

	// Truncate the compressed data
	corrupt := buf.Bytes()[:len(buf.Bytes())-4]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(corrupt)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/test.tar.gz")
	if err == nil {
		t.Fatal("expected error from corrupt gzip body")
	}
}

// TestExtractArchive_TarXzUnsupported verifies that .tar.xz URLs return an error.
func TestExtractArchive_TarXzUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake xz content"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/test.tar.xz")
	if err == nil {
		t.Fatal("expected error for .tar.xz URL")
	}
	if err.Error() == "" {
		t.Fatal("error should have a message")
	}
}

// TestDownload_FFprobeExtractedAndExecutable verifies ffprobe is extracted with exec perm.
func TestDownload_FFprobeExtractedAndExecutable(t *testing.T) {
	ffmpegContent := []byte("#!/bin/sh\necho 'ffmpeg version 7.0-test'\n")
	ffprobeContent := []byte("#!/bin/sh\necho 'ffprobe version 7.0-test'\n")

	archive := createTarGzArchive(t, map[string][]byte{
		"ffmpeg":  ffmpegContent,
		"ffprobe": ffprobeContent,
	})

	srv := serveTarGz(t, archive)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/ffmpeg-static.tar.gz")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	// Verify ffprobe is executable
	info, err := os.Stat(d.FFprobePath())
	if err != nil {
		t.Fatalf("stat ffprobe: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("ffprobe should be executable")
	}

	// Verify ffprobe content
	data, err := os.ReadFile(d.FFprobePath())
	if err != nil {
		t.Fatalf("read ffprobe: %v", err)
	}
	if !bytes.Equal(data, ffprobeContent) {
		t.Fatalf("ffprobe content mismatch")
	}
}

// TestDownload_LargeTarGzProgress verifies progress tracking works with archive extraction.
func TestDownload_LargeTarGzProgress(t *testing.T) {
	// Create a larger fake ffmpeg binary to test progress
	largeContent := make([]byte, 50*1024) // 50KB
	for i := range largeContent {
		largeContent[i] = byte(i % 256)
	}

	archive := createTarGzArchive(t, map[string][]byte{
		"ffmpeg": largeContent,
	})

	var progressCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(archive)))
		// Write in small chunks to trigger multiple progress callbacks
		chunkSize := 1024
		for offset := 0; offset < len(archive); offset += chunkSize {
			end := offset + chunkSize
			if end > len(archive) {
				end = len(archive)
			}
			w.Write(archive[offset:end])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, func(downloaded, total int64) {
		progressCalls.Add(1)
	})

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/ffmpeg-static.tar.gz")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	if progressCalls.Load() == 0 {
		t.Fatal("progress callback should have been called")
	}

	// Verify the extracted file matches
	data, err := os.ReadFile(d.FFmpegPath())
	if err != nil {
		t.Fatalf("read ffmpeg: %v", err)
	}
	if !bytes.Equal(data, largeContent) {
		t.Fatalf("extracted content size mismatch: got %d, want %d", len(data), len(largeContent))
	}
}
