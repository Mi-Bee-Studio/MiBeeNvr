// Package downloader manages ONNX Runtime binary downloads from GitHub releases.
// It follows the same atomic-download pattern as the FFmpeg downloader:
// temp file → rename, mutex for thread safety, progress callback, state persistence.
package downloader

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// onnxRuntimeVersion is the pinned ONNX Runtime release version.
const onnxRuntimeVersion = "1.17.0"

// getDownloadURL returns the ONNX Runtime release URL for the given platform.
// Package-level variable so tests can override it.
var getDownloadURL = defaultDownloadURL

func defaultDownloadURL(goos, goarch string) string {
	base := fmt.Sprintf("https://github.com/microsoft/onnxruntime/releases/download/v%s", onnxRuntimeVersion)
	switch goos + "/" + goarch {
	case "linux/amd64":
		return base + "/onnxruntime-linux-x64-" + onnxRuntimeVersion + ".tgz"
	case "linux/arm64":
		return base + "/onnxruntime-linux-aarch64-" + onnxRuntimeVersion + ".tgz"
	default:
		return ""
	}
}

// expectedSHA256 contains the expected SHA-256 hash for each platform's archive.
// These can be populated from GitHub release assets' checksums.
var expectedSHA256 = map[string]string{
	"linux/amd64":  "efc344d54d1969446ff5d3e55b54e205c6579c06333ecf1d34a04215eefae7c6",
	"linux/arm64":  "ee5069252f549ef94759b6b60bdf10b2dc2cd71d064a7045dd66a052f956a68b",
}

// DownloadStatus represents the ONNX Runtime download state.
type DownloadStatus struct {
	Status          string  `json:"status"`           // "not_installed", "downloading", "available", "failed"
	Progress        float64 `json:"progress"`         // 0.0-1.0
	Version         string  `json:"version"`
	Error           string  `json:"error"`
	TotalBytes      int64   `json:"total_bytes"`
	DownloadedBytes int64   `json:"downloaded_bytes"`
}

// DownloadState persists download progress across restarts.
type DownloadState struct {
	Status          string  `json:"status"`
	Progress        float64 `json:"progress"`
	Version         string  `json:"version"`
	Error           string  `json:"error"`
	DownloadURL     string  `json:"download_url,omitempty"`
	LastUpdated     string  `json:"last_updated"`
	TotalBytes      int64   `json:"total_bytes,omitempty"`
	DownloadedBytes int64   `json:"downloaded_bytes,omitempty"`
}

// Downloader manages ONNX Runtime binary downloads.
// It is safe for concurrent use — a mutex prevents duplicate downloads.
type Downloader struct {
	mu         sync.Mutex
	dataDir    string
	status     DownloadStatus
	cancelFunc context.CancelFunc
	onProgress func(downloaded, total int64)
}

// NewDownloader creates a new ONNX Runtime downloader.
// dataDir is the storage root (e.g. StorageConfig.RootDir).
// onProgress is an optional callback invoked with bytes downloaded and total.
func NewDownloader(dataDir string, onProgress func(downloaded, total int64)) *Downloader {
	return &Downloader{
		dataDir:    dataDir,
		onProgress: onProgress,
	}
}

// BinaryPath returns the expected ONNX Runtime binary path.
func (d *Downloader) BinaryPath() string {
	return filepath.Join(d.dataDir, "tools", "onnxruntime")
}

// ModelPath returns the expected default model path.
func (d *Downloader) ModelPath() string {
	return filepath.Join(d.dataDir, "models", "yolov11n.onnx")
}

// StatePath returns the path for download state persistence.
func (d *Downloader) StatePath() string {
	return filepath.Join(d.dataDir, "tools", "onnx-download-state.json")
}

// GetStatus checks ONNX Runtime availability and download status.
// It checks: 1) system PATH for onnxruntime-inference, 2) custom {dataDir}/tools/onnxruntime, 3) download state.
func (d *Downloader) GetStatus() DownloadStatus {
	// Check system PATH first
	if p, err := exec.LookPath("onnxruntime-inference"); err == nil {
		if info, err := os.Stat(p); err == nil && info.Size() > 0 {
			return DownloadStatus{
				Status:   "available",
				Version:  onnxRuntimeVersion,
				Progress: 1.0,
			}
		}
	}

	// Check if binary exists at custom path and is valid
	binPath := d.BinaryPath()
	if info, err := os.Stat(binPath); err == nil && info.Size() > 0 {
		// Binary exists and has content — consider it available
		return DownloadStatus{
			Status:   "available",
			Version:  onnxRuntimeVersion,
			Progress: 1.0,
		}
	}

	// Check in-memory status (active download)
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.statusLocked()
}

func (d *Downloader) statusLocked() DownloadStatus {
	if d.status.Status != "" {
		return d.status
	}

	// Check persisted state
	state := d.loadState()
	if state != nil && state.Status != "" {
		return DownloadStatus{
			Status:          state.Status,
			Progress:        state.Progress,
			Version:         state.Version,
			Error:           state.Error,
			TotalBytes:      state.TotalBytes,
			DownloadedBytes: state.DownloadedBytes,
		}
	}

	return DownloadStatus{Status: "not_installed"}
}

// IsDownloaded checks if the ONNX Runtime binary has been downloaded and verified.
func (d *Downloader) IsDownloaded() bool {
	binPath := d.BinaryPath()
	info, err := os.Stat(binPath)
	if err != nil || info.Size() == 0 {
		return false
	}
	return true
}

// Download downloads the ONNX Runtime archive, verifies SHA-256, and extracts it.
// Idempotent: if ONNX Runtime exists and is valid, returns nil immediately.
// Concurrent-safe: a second call while downloading returns an error.
func (d *Downloader) Download(ctx context.Context) error {
	d.mu.Lock()

	// Check if already available (filesystem-only check)
	binPath := d.BinaryPath()
	if info, err := os.Stat(binPath); err == nil && info.Size() > 0 {
		d.mu.Unlock()
		return nil
	}

	// Check if already downloading
	if d.status.Status == "downloading" {
		d.mu.Unlock()
		return fmt.Errorf("download already in progress")
	}

	// Set downloading state
	d.status = DownloadStatus{Status: "downloading", Progress: 0}
	d.saveState()

	// Create cancellable context
	dlCtx, cancel := context.WithCancel(ctx)
	d.cancelFunc = cancel
	d.mu.Unlock()

	// Perform download with retry
	err := d.downloadWithRetry(dlCtx)

	d.mu.Lock()
	defer d.mu.Unlock()

	if err != nil {
		d.status = DownloadStatus{Status: "failed", Error: err.Error()}
		d.saveState()
		return err
	}

	// Verify downloaded binary
	if info, err := os.Stat(binPath); err != nil || info.Size() == 0 {
		d.status = DownloadStatus{Status: "failed", Error: "verification failed: binary not found after extraction"}
		d.saveState()
		return fmt.Errorf("verification failed: binary not found after extraction")
	}

	d.status = DownloadStatus{Status: "available", Version: onnxRuntimeVersion, Progress: 1.0}
	d.saveState()
	d.cancelFunc = nil

	return nil
}

// Cancel cancels an active download. Safe to call when nothing is downloading.
func (d *Downloader) Cancel() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancelFunc != nil {
		d.cancelFunc()
		d.cancelFunc = nil
	}
}

// downloadWithRetry performs up to 3 retries with exponential backoff.
func (d *Downloader) downloadWithRetry(ctx context.Context) error {
	backoff := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}
	return d.downloadWithRetryCustom(ctx, getDownloadURL(runtime.GOOS, runtime.GOARCH), backoff)
}

// downloadWithRetryCustom performs retries with configurable backoff (for testing).
func (d *Downloader) downloadWithRetryCustom(ctx context.Context, url string, backoff []time.Duration) error {
	if url == "" {
		return fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	var lastErr error
	maxRetries := 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff[attempt-1]):
			}
		}

		lastErr = d.downloadOnceWithURL(ctx, url)
		if lastErr == nil {
			return nil
		}

		slog.Warn("ONNX Runtime download attempt failed", "attempt", attempt+1, "error", lastErr)
	}

	return fmt.Errorf("download failed after %d attempts: %w", maxRetries, lastErr)
}

// downloadOnceWithURL downloads from a specific URL with atomic write and SHA-256 verification.
// The temp file is cleaned up on any failure.
func (d *Downloader) downloadOnceWithURL(ctx context.Context, url string) error {
	if url == "" {
		return fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// Create tools directory
	toolsDir := filepath.Join(d.dataDir, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		return fmt.Errorf("failed to create tools directory: %w", err)
	}

	// Download to temp file (atomic write pattern: temp → verify → extract)
	tmpPath := filepath.Join(toolsDir, "onnx-download.tmp")
	defer os.Remove(tmpPath) // cleanup archive on any failure

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Create temp file for the archive
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer tmpFile.Close()

	// Download with SHA-256 verification and progress tracking
	var downloaded int64
	total := resp.ContentLength
	buf := make([]byte, 32*1024)
	hasher := sha256.New()

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := tmpFile.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("write failed: %w", writeErr)
			}
			hasher.Write(buf[:n])
			downloaded += int64(n)

			if d.onProgress != nil {
				d.onProgress(downloaded, total)
			}

			// Update in-memory progress + byte tracking
			d.mu.Lock()
			d.status.DownloadedBytes = downloaded
			d.status.TotalBytes = total
			if total > 0 {
				d.status.Progress = float64(downloaded) / float64(total)
			}
			d.mu.Unlock()
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read failed: %w", readErr)
		}
	}
	tmpFile.Close()

	// Verify SHA-256 hash
	checksum := hasher.Sum(nil)
	if err := d.verifySHA256(checksum); err != nil {
		return err
	}

	// Extract binaries from the downloaded archive
	if err := d.extractArchive(tmpPath, toolsDir, url); err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}

	return nil
}

// verifySHA256 checks the downloaded archive against the expected hash.
// If no expected hash is configured for the platform, it logs a warning and passes.
func (d *Downloader) verifySHA256(sum []byte) error {
	actualHex := hex.EncodeToString(sum)

	platform := runtime.GOOS + "/" + runtime.GOARCH
	expected, ok := expectedSHA256[platform]
	if !ok || expected == "" {
		// No expected hash configured — log and pass
		slog.Warn("no expected SHA-256 configured, skipping verification",
			"platform", platform,
			"actual", actualHex,
		)
		return nil
	}

	if actualHex != expected {
		return fmt.Errorf("SHA-256 verification failed: expected %s, got %s", expected, actualHex)
	}

	slog.Info("SHA-256 verified", "hash", actualHex)
	return nil
}

// extractArchive extracts the ONNX Runtime binaries from a .tar.gz archive.
// The archive structure is: onnxruntime-linux-{arch}-{version}/...
// It looks for onnxruntime-inference or onnxruntime-server binaries.
func (d *Downloader) extractArchive(archivePath, toolsDir, url string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	var tarReader *tar.Reader

	if strings.HasSuffix(url, ".tar.gz") || strings.HasSuffix(url, ".tgz") {
		gzr, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("gzip decompression failed (corrupt archive?): %w", err)
		}
		defer gzr.Close()
		tarReader = tar.NewReader(gzr)
	} else if strings.HasSuffix(url, ".tar.xz") {
		return fmt.Errorf("tar.xz archives not supported (use .tar.gz URL)")
	} else {
		// Not an archive — treat as raw binary (legacy behavior)
		if err := os.Rename(archivePath, d.BinaryPath()); err != nil {
			return fmt.Errorf("rename to onnxruntime: %w", err)
		}
		return os.Chmod(d.BinaryPath(), 0755)
	}

	// Known binary names to extract from the archive.
	// Priority: onnxruntime-inference > onnxruntime-server > any executable in bin/
	targetBinaries := []string{"onnxruntime-inference", "onnxruntime-server"}
	extracted := false

	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error (corrupt archive?): %w", err)
		}

		baseName := filepath.Base(hdr.Name)

		// Only extract regular files
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 {
			continue
		}

		// Check if this is one of our target binaries
		for _, target := range targetBinaries {
			if baseName == target {
				if err := d.extractBinary(tarReader, toolsDir, target); err != nil {
					return err
				}
				extracted = true
				break
			}
		}

		if extracted {
			break
		}
	}

	if !extracted {
		return fmt.Errorf("archive contained no onnxruntime-inference or onnxruntime-server binary")
	}

	return nil
}

// extractBinary extracts a single binary from the tar stream with atomic write.
func (d *Downloader) extractBinary(tarReader *tar.Reader, toolsDir, name string) error {
	targetPath := d.BinaryPath()

	// Atomic write: extract to .tmp first, then rename
	tmpTarget := targetPath + ".tmp2"
	outFile, err := os.OpenFile(tmpTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}

	if _, err := io.Copy(outFile, tarReader); err != nil {
		outFile.Close()
		os.Remove(tmpTarget)
		return fmt.Errorf("extract %s: %w", name, err)
	}
	outFile.Close()

	if err := os.Chmod(tmpTarget, 0755); err != nil {
		os.Remove(tmpTarget)
		return fmt.Errorf("chmod %s: %w", name, err)
	}

	if err := os.Rename(tmpTarget, targetPath); err != nil {
		os.Remove(tmpTarget)
		return fmt.Errorf("rename %s: %w", name, err)
	}

	slog.Info("Extracted ONNX Runtime binary from archive", "name", name, "path", targetPath)
	return nil
}

// loadState reads the download state from disk. Returns nil on any error.
func (d *Downloader) loadState() *DownloadState {
	data, err := os.ReadFile(d.StatePath())
	if err != nil {
		return nil
	}
	var state DownloadState
	if json.Unmarshal(data, &state) != nil {
		return nil
	}
	return &state
}

// saveState persists the current download state to disk.
func (d *Downloader) saveState() {
	state := DownloadState{
		Status:          d.status.Status,
		Progress:        d.status.Progress,
		Version:         d.status.Version,
		Error:           d.status.Error,
		LastUpdated:     time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		TotalBytes:      d.status.TotalBytes,
		DownloadedBytes: d.status.DownloadedBytes,
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		slog.Warn("Failed to marshal download state", "error", err)
		return
	}

	toolsDir := filepath.Join(d.dataDir, "tools")
	os.MkdirAll(toolsDir, 0755)

	if err := os.WriteFile(d.StatePath(), data, 0644); err != nil {
		slog.Warn("Failed to save download state", "error", err)
	}
}
