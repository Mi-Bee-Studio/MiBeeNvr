package downloader

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// mustCreateFakeBinary creates a fake onnxruntime binary file.
func mustCreateFakeBinary(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "onnxruntime")
	content := []byte("#!/bin/sh\necho 'onnxruntime version 1.17.0'\nexit 0\n")
	if err := os.WriteFile(p, content, 0o755); err != nil {
		t.Fatalf("write fake onnxruntime: %v", err)
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

// readDownloadState reads and parses the onnx-download-state.json from tools dir.
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

// withExpectedHash sets the expected SHA-256 hash for the current platform
// to the SHA-256 of data. It restores the original map on test completion.
func withExpectedHash(t *testing.T, data []byte) {
	t.Helper()
	hash := sha256.Sum256(data)
	platform := runtime.GOOS + "/" + runtime.GOARCH
	orig := expectedSHA256
	expectedSHA256 = map[string]string{platform: hex.EncodeToString(hash[:])}
	t.Cleanup(func() { expectedSHA256 = orig })
}

// --- Tests ---

// TestGetStatus_NotInstalled verifies status when no ONNX Runtime exists.
func TestGetStatus_NotInstalled(t *testing.T) {
	d := newTestDownloader(t)

	status := d.GetStatus()
	if status.Status != "not_installed" {
		t.Fatalf("Status = %q, want %q", status.Status, "not_installed")
	}
	if status.Progress != 0 {
		t.Fatalf("Progress = %f, want 0", status.Progress)
	}
}

// TestGetStatus_Available verifies status when valid binary exists.
func TestGetStatus_Available(t *testing.T) {
	dir := t.TempDir()
	mustCreateToolsDir(t, dir)
	mustCreateFakeBinary(t, filepath.Join(dir, "tools"))

	d := NewDownloader(dir, nil)
	status := d.GetStatus()

	if status.Status != "available" {
		t.Fatalf("Status = %q, want %q", status.Status, "available")
	}
	if status.Version != onnxRuntimeVersion {
		t.Fatalf("Version = %q, want %q", status.Version, onnxRuntimeVersion)
	}
	if status.Progress != 1.0 {
		t.Fatalf("Progress = %f, want 1.0", status.Progress)
	}
}

// TestGetStatus_Downloading verifies status returns in-progress state.
func TestGetStatus_Downloading(t *testing.T) {
	d := newTestDownloader(t)

	d.mu.Lock()
	d.status = DownloadStatus{Status: "downloading", Progress: 0.42}
	d.mu.Unlock()

	status := d.GetStatus()
	if status.Status != "downloading" {
		t.Fatalf("Status = %q, want %q", status.Status, "downloading")
	}
	if status.Progress != 0.42 {
		t.Fatalf("Progress = %f, want 0.42", status.Progress)
	}
}

// TestDownload_Idempotent verifies that downloading when binary already exists returns nil.
func TestDownload_Idempotent(t *testing.T) {
	dir := t.TempDir()
	mustCreateToolsDir(t, dir)
	mustCreateFakeBinary(t, filepath.Join(dir, "tools"))

	d := NewDownloader(dir, nil)
	err := d.Download(context.Background())
	if err != nil {
		t.Fatalf("Download with existing binary: %v", err)
	}
}

// TestDownload_TarGzExtraction verifies download + extraction of a .tar.gz archive
// containing the onnxruntime-inference binary.
func TestDownload_TarGzExtraction(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho 'onnxruntime version 1.17.0-test'\n")

	archive := createTarGzArchive(t, map[string][]byte{
		"onnxruntime-linux-x64-1.17.0/onnxruntime-inference": binaryContent,
		"onnxruntime-linux-x64-1.17.0/include/onnxruntime_c_api.h": []byte("header"),
		"onnxruntime-linux-x64-1.17.0/lib/libonnxruntime.so":       []byte("lib"),
	})
	withExpectedHash(t, archive)

	srv := serveTarGz(t, archive)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	url := srv.URL + "/onnxruntime-linux-x64-1.17.0.tgz"
	err := d.downloadOnceWithURL(context.Background(), url)
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	// Verify binary exists and has correct content
	data, err := os.ReadFile(d.BinaryPath())
	if err != nil {
		t.Fatalf("read onnxruntime: %v", err)
	}
	if !bytes.Equal(data, binaryContent) {
		t.Fatalf("binary content mismatch: got %q, want %q", string(data), string(binaryContent))
	}

	// Verify it is executable
	info, err := os.Stat(d.BinaryPath())
	if err != nil {
		t.Fatalf("stat onnxruntime: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("onnxruntime should be executable")
	}

	// Verify archive was cleaned up
	toolsDir := filepath.Join(dir, "tools")
	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		t.Fatalf("readdir tools: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "onnx-download.tmp" {
			t.Fatal("archive should have been cleaned up")
		}
	}
}

// TestDownload_TarGzExtraction_ServerBinary verifies extraction of onnxruntime-server
// when onnxruntime-inference is not present.
func TestDownload_TarGzExtraction_ServerBinary(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho 'onnxruntime-server version 1.17.0'\n")

	archive := createTarGzArchive(t, map[string][]byte{
		"onnxruntime-linux-x64-1.17.0/onnxruntime-server": binaryContent,
	})
	withExpectedHash(t, archive)

	srv := serveTarGz(t, archive)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	url := srv.URL + "/onnxruntime-linux-x64-1.17.0.tgz"
	err := d.downloadOnceWithURL(context.Background(), url)
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	data, err := os.ReadFile(d.BinaryPath())
	if err != nil {
		t.Fatalf("read onnxruntime: %v", err)
	}
	if !bytes.Equal(data, binaryContent) {
		t.Fatalf("binary content mismatch")
	}
}

// TestDownload_TarGzCorruptArchive verifies graceful error on corrupt archive.
func TestDownload_TarGzCorruptArchive(t *testing.T) {
	corruptData := []byte("this is not a valid gzip stream at all!")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(corruptData)))
		w.Write(corruptData)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	url := srv.URL + "/onnxruntime-linux-x64-1.17.0.tgz"
	err := d.downloadOnceWithURL(context.Background(), url)
	if err == nil {
		t.Fatal("expected error from corrupt archive")
	}

	// Should mention extraction/gzip failure
	if !bytes.Contains([]byte(err.Error()), []byte("gzip")) &&
		!bytes.Contains([]byte(err.Error()), []byte("extract")) {
		t.Logf("error: %v", err)
	}

	// Verify no onnxruntime file left behind
	if _, err := os.Stat(d.BinaryPath()); !os.IsNotExist(err) {
		t.Fatal("onnxruntime should not exist after corrupt extraction")
	}
}

// TestDownload_TarGzMissingBinaries verifies error when archive has no target binary.
func TestDownload_TarGzMissingBinaries(t *testing.T) {
	archive := createTarGzArchive(t, map[string][]byte{
		"onnxruntime-linux-x64-1.17.0/readme.txt":    []byte("no binaries here"),
		"onnxruntime-linux-x64-1.17.0/lib/libonnxruntime.so": []byte("lib"),
	})

	srv := serveTarGz(t, archive)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	url := srv.URL + "/onnxruntime-linux-x64-1.17.0.tgz"
	err := d.downloadOnceWithURL(context.Background(), url)
	if err == nil {
		t.Fatal("expected error when archive has no onnxruntime binary")
	}
}

// TestDownload_RawBinary verifies that a non-archive URL falls back to raw binary download.
func TestDownload_RawBinary(t *testing.T) {
	fakeBinary := []byte("#!/bin/sh\necho 'onnxruntime version 1.17.0-test'\n")
	withExpectedHash(t, fakeBinary)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fakeBinary)))
		w.Write(fakeBinary)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	// URL without .tar.gz or .tgz extension → raw binary
	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/onnxruntime")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	data, err := os.ReadFile(d.BinaryPath())
	if err != nil {
		t.Fatalf("read onnxruntime: %v", err)
	}
	if !bytes.Equal(data, fakeBinary) {
		t.Fatalf("content mismatch")
	}

	info, err := os.Stat(d.BinaryPath())
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

	err := d.downloadOnceWithURL(ctx, srv.URL+"/onnxruntime.tgz")
	if err == nil {
		t.Fatal("expected error from cancelled download")
	}

	// Verify no onnxruntime file exists
	if _, err := os.Stat(d.BinaryPath()); !os.IsNotExist(err) {
		t.Fatal("onnxruntime file should not exist after failed download")
	}

	// Verify no .tmp file remains
	toolsDir := filepath.Join(dir, "tools")
	tmpPath := filepath.Join(toolsDir, "onnx-download.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal("temp file should be cleaned up after failed download")
	}
}

// TestDownload_Retry verifies that download retries on transient failures.
func TestDownload_Retry(t *testing.T) {
	var attempts atomic.Int32

	binaryContent := []byte("#!/bin/sh\necho 'onnxruntime version 1.17.0-test'\n")
	archive := createTarGzArchive(t, map[string][]byte{
		"onnxruntime-linux-x64-1.17.0/onnxruntime-inference": binaryContent,
	})
	withExpectedHash(t, archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(archive)))
		w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	err := d.downloadWithRetryCustom(context.Background(), srv.URL, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("downloadWithRetryCustom: %v", err)
	}

	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}

	// Verify binary extracted
	if _, err := os.Stat(d.BinaryPath()); err != nil {
		t.Fatalf("binary should exist after successful download")
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
	withExpectedHash(t, fakeBinary)

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
	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/onnxruntime")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	if progressCalls.Load() == 0 {
		t.Fatal("progress callback was never called")
	}
}

// TestDownload_CancelCleansUp verifies that Cancel() aborts download and cleans up.
func TestDownload_CancelCleansUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		errCh <- d.downloadOnceWithURL(ctx, srv.URL+"/onnxruntime.tgz")
	}()

	// Give the download a moment to start
	time.Sleep(50 * time.Millisecond)

	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error from cancelled download")
	}

	// Verify temp file cleaned up
	toolsDir := filepath.Join(dir, "tools")
	tmpPath := filepath.Join(toolsDir, "onnx-download.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal("temp file should be cleaned up after cancellation")
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
		{"linux", "amd64", "https://github.com/microsoft/onnxruntime/releases/download/v1.17.0/onnxruntime-linux-x64-1.17.0.tgz"},
		{"linux", "arm64", "https://github.com/microsoft/onnxruntime/releases/download/v1.17.0/onnxruntime-linux-aarch64-1.17.0.tgz"},
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
	d := newTestDownloader(t)
	err := d.Download(context.Background())
	if err == nil {
		t.Fatal("expected error on unsupported platform")
	}
}

// TestBinaryPath verifies path is correct.
func TestBinaryPath(t *testing.T) {
	d := NewDownloader("/data", nil)
	want := filepath.Join("/data", "tools", "onnxruntime")
	if d.BinaryPath() != want {
		t.Fatalf("BinaryPath = %q, want %q", d.BinaryPath(), want)
	}
}

// TestModelPath verifies model path is correct.
func TestModelPath(t *testing.T) {
	d := NewDownloader("/data", nil)
	want := filepath.Join("/data", "models", "yolov11n.onnx")
	if d.ModelPath() != want {
		t.Fatalf("ModelPath = %q, want %q", d.ModelPath(), want)
	}
}

// TestStatePath verifies state path is correct.
func TestStatePath(t *testing.T) {
	d := NewDownloader("/data", nil)
	want := filepath.Join("/data", "tools", "onnx-download-state.json")
	if d.StatePath() != want {
		t.Fatalf("StatePath = %q, want %q", d.StatePath(), want)
	}
}

// TestIsDownloaded verifies IsDownloaded returns correct state.
func TestIsDownloaded(t *testing.T) {
	t.Run("not_downloaded", func(t *testing.T) {
		d := newTestDownloader(t)
		if d.IsDownloaded() {
			t.Fatal("IsDownloaded should return false when binary doesn't exist")
		}
	})

	t.Run("downloaded", func(t *testing.T) {
		dir := t.TempDir()
		mustCreateToolsDir(t, dir)
		mustCreateFakeBinary(t, filepath.Join(dir, "tools"))

		d := NewDownloader(dir, nil)
		if !d.IsDownloaded() {
			t.Fatal("IsDownloaded should return true when binary exists")
		}
	})
}

// TestDownload_CancellationViaDownload verifies full Download with context cancellation.
func TestDownload_CancellationViaDownload(t *testing.T) {
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
	getDownloadURL = func(goos, goarch string) string { return srv.URL + "/onnxruntime.tgz" }
	defer func() { getDownloadURL = origGetter }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := d.Download(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("error should be context error, got: %v", err)
	}
}

// TestExtractArchive_TarGzWithDirectoryPrefix verifies extraction from archives with nested directories.
func TestExtractArchive_TarGzWithDirectoryPrefix(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho 'onnxruntime version N-test'\n")

	archive := createTarGzArchive(t, map[string][]byte{
		"build-dir/onnxruntime-inference": binaryContent,
		"build-dir/include/api.h":          []byte("header"),
		"build-dir/lib/libonnxruntime.so":  []byte("lib"),
	})
	withExpectedHash(t, archive)

	srv := serveTarGz(t, archive)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/onnxruntime.tgz")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	data, err := os.ReadFile(d.BinaryPath())
	if err != nil {
		t.Fatalf("read onnxruntime: %v", err)
	}
	if !bytes.Equal(data, binaryContent) {
		t.Fatalf("binary content mismatch")
	}
}

// TestExtractArchive_CorruptGzipBody verifies error when gzip body is truncated.
func TestExtractArchive_CorruptGzipBody(t *testing.T) {
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

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/onnxruntime.tgz")
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

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/onnxruntime.tar.xz")
	if err == nil {
		t.Fatal("expected error for .tar.xz URL")
	}
	if err.Error() == "" {
		t.Fatal("error should have a message")
	}
}

// TestDownload_SHA256Verification_Pass verifies SHA-256 passes when hashes match.
func TestDownload_SHA256Verification_Pass(t *testing.T) {
	fakeBinary := []byte("#!/bin/sh\necho 'onnxruntime'\n")
	hash := sha256.Sum256(fakeBinary)
	expectedHex := hex.EncodeToString(hash[:])

	// Set expected hash for current platform
	platform := runtime.GOOS + "/" + runtime.GOARCH
	origHashes := expectedSHA256
	expectedSHA256 = map[string]string{platform: expectedHex}
	defer func() { expectedSHA256 = origHashes }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fakeBinary)))
		w.Write(fakeBinary)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/onnxruntime")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}
}

// TestDownload_SHA256Verification_Fail verifies SHA-256 fails when hashes mismatch.
func TestDownload_SHA256Verification_Fail(t *testing.T) {
	fakeBinary := []byte("#!/bin/sh\necho 'onnxruntime'\n")
	// Use a wrong hash
	expectedSHA256Orig := expectedSHA256
	expectedSHA256 = map[string]string{
		runtime.GOOS + "/" + runtime.GOARCH: "0000000000000000000000000000000000000000000000000000000000000000",
	}
	defer func() { expectedSHA256 = expectedSHA256Orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fakeBinary)))
		w.Write(fakeBinary)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/onnxruntime")
	if err == nil {
		t.Fatal("expected SHA-256 verification failure")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("SHA-256")) {
		t.Fatalf("error should mention SHA-256, got: %v", err)
	}

	// Verify binary was NOT installed
	if _, err := os.Stat(d.BinaryPath()); !os.IsNotExist(err) {
		t.Fatal("binary should not exist after failed verification")
	}
}

// TestDownload_LargeTarGzProgress verifies progress tracking works with archive extraction.
func TestDownload_LargeTarGzProgress(t *testing.T) {
	largeContent := make([]byte, 50*1024) // 50KB
	for i := range largeContent {
		largeContent[i] = byte(i % 256)
	}

	archive := createTarGzArchive(t, map[string][]byte{
		"onnxruntime-linux-x64-1.17.0/onnxruntime-inference": largeContent,
	})
	withExpectedHash(t, archive)

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

	err := d.downloadOnceWithURL(context.Background(), srv.URL+"/onnxruntime.tgz")
	if err != nil {
		t.Fatalf("downloadOnceWithURL: %v", err)
	}

	if progressCalls.Load() == 0 {
		t.Fatal("progress callback should have been called")
	}

	// Verify the extracted file matches
	data, err := os.ReadFile(d.BinaryPath())
	if err != nil {
		t.Fatalf("read onnxruntime: %v", err)
	}
	if !bytes.Equal(data, largeContent) {
		t.Fatalf("extracted content size mismatch: got %d, want %d", len(data), len(largeContent))
	}
}

// TestDownload_ConcurrentMutex verifies concurrent Download calls don't cause multiple downloads.
// It counts actual HTTP requests to the test server: if the mutex works, the server receives
// exactly 1 request even when 3 goroutines call Download concurrently. Success count alone is
// unreliable because a fast download lets subsequent callers see the binary already exists.
func TestDownload_ConcurrentMutex(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho 'onnxruntime version 1.17.0-test'\n")
	archive := createTarGzArchive(t, map[string][]byte{
		"onnxruntime-linux-x64-1.17.0/onnxruntime-inference": binaryContent,
	})
	withExpectedHash(t, archive)

	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(archive)))
		w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir, nil)

	// Override URL getter to point to test server
	origGetter := getDownloadURL
	getDownloadURL = func(goos, goarch string) string { return srv.URL + "/onnxruntime.tgz" }
	defer func() { getDownloadURL = origGetter }()

	var wg sync.WaitGroup
	var successCount int32

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.Download(context.Background()); err == nil {
				atomic.AddInt32(&successCount, 1)
			}
		}()
	}
	wg.Wait()

	// At least one Download must succeed
	if atomic.LoadInt32(&successCount) == 0 {
		t.Fatal("at least one Download call should succeed")
	}
	// Mutex must prevent concurrent downloads: server should receive exactly 1 request
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("mutex should prevent concurrent downloads: expected 1 server request, got %d", got)
	}
}
