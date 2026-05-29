package transcoding

import (
	"archive/tar"
	"compress/gzip"
	"context"
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

// getDownloadURL returns the FFmpeg static build URL for the platform.
// Package-level variable so tests can override it.
var getDownloadURL = defaultDownloadURL

func defaultDownloadURL(goos, goarch string) string {
	base := "https://johnvansickle.com/ffmpeg/builds"
	switch goos + "/" + goarch {
	case "linux/amd64":
	return base + "/ffmpeg-git-amd64-static.tar.gz"
case "linux/arm64":
	return base + "/ffmpeg-git-arm64-static.tar.gz"
case "linux/arm":
	return base + "/ffmpeg-git-armhf-static.tar.gz"
	default:
		return ""
	}
}

// Downloader manages FFmpeg static binary downloads.
// It is safe for concurrent use — a mutex prevents duplicate downloads.
type Downloader struct {
	mu         sync.Mutex
	dataDir    string
	status     DownloadStatus
	cancelFunc context.CancelFunc
	onProgress func(downloaded, total int64)
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

// NewDownloader creates a new FFmpeg downloader.
// dataDir is the storage root (e.g. StorageConfig.RootDir).
// onProgress is an optional callback invoked with bytes downloaded and total.
func NewDownloader(dataDir string, onProgress func(downloaded, total int64)) *Downloader {
	return &Downloader{
		dataDir:    dataDir,
		onProgress: onProgress,
	}
}

// FFmpegPath returns the expected FFmpeg binary path.
func (d *Downloader) FFmpegPath() string {
	return filepath.Join(d.dataDir, "tools", "ffmpeg")
}

// FFprobePath returns the expected ffprobe binary path.
func (d *Downloader) FFprobePath() string {
	return filepath.Join(d.dataDir, "tools", "ffprobe")
}

// StatePath returns the path for download state persistence.
func (d *Downloader) StatePath() string {
	return filepath.Join(d.dataDir, "tools", "download-state.json")
}

// GetFFmpegStatus checks FFmpeg availability and download status.
// It checks: 1) system PATH, 2) custom {dataDir}/tools/ffmpeg, 3) download state.
func (d *Downloader) GetFFmpegStatus() DownloadStatus {
	// Check system PATH first (e.g. apt-installed FFmpeg)
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		if version, err := d.getFFmpegVersion(p); err == nil {
			return DownloadStatus{
				Status:   "available",
				Version:  version,
				Progress: 1.0,
			}
		}
	}

	// Check if FFmpeg binary exists at custom path and works
	ffmpegPath := d.FFmpegPath()
	if info, err := os.Stat(ffmpegPath); err == nil && info.Size() > 0 {
		if version, err := d.getFFmpegVersion(ffmpegPath); err == nil {
			return DownloadStatus{
				Status:   "available",
				Version:  version,
				Progress: 1.0,
			}
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

// DownloadFFmpeg downloads FFmpeg static binary.
// Idempotent: if FFmpeg exists and is valid, returns nil immediately.
// Concurrent-safe: a second call while downloading returns an error.
func (d *Downloader) DownloadFFmpeg(ctx context.Context) error {
	d.mu.Lock()

	// Check if already available (filesystem-only check, no lock needed)
	ffmpegPath := d.FFmpegPath()
	if info, err := os.Stat(ffmpegPath); err == nil && info.Size() > 0 {
		if version, err := d.getFFmpegVersion(ffmpegPath); err == nil {
			d.mu.Unlock()
			_ = version
			return nil
		}
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
	version, err := d.getFFmpegVersion(d.FFmpegPath())
	if err != nil {
		d.status = DownloadStatus{Status: "failed", Error: fmt.Sprintf("verification failed: %v", err)}
		d.saveState()
		return fmt.Errorf("verification failed: %w", err)
	}

	d.status = DownloadStatus{Status: "available", Version: version, Progress: 1.0}
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

		slog.Warn("FFmpeg download attempt failed", "attempt", attempt+1, "error", lastErr)
	}

	return fmt.Errorf("download failed after %d attempts: %w", maxRetries, lastErr)
}

// downloadOnce downloads from the platform-detected URL.
func (d *Downloader) downloadOnce(ctx context.Context) error {
	url := getDownloadURL(runtime.GOOS, runtime.GOARCH)
	if url == "" {
		return fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return d.downloadOnceWithURL(ctx, url)
}

// downloadOnceWithURL downloads from a specific URL with atomic write.
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

	// Download to temp file (atomic write pattern: temp → rename)
	tmpPath := filepath.Join(toolsDir, "download.tmp")
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

	// Download with progress tracking
	var downloaded int64
	total := resp.ContentLength
	buf := make([]byte, 32*1024)

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := tmpFile.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("write failed: %w", writeErr)
			}
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

	// Extract binaries from the downloaded archive
	if err := d.extractArchive(tmpPath, toolsDir, url); err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}

	return nil
}

// extractArchive extracts ffmpeg and ffprobe from a .tar.gz archive.
// It handles both flat files and files nested inside a directory (e.g. ffmpeg-git-amd64-static/ffmpeg).
func (d *Downloader) extractArchive(archivePath, toolsDir, url string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	// Detect format: .tar.gz or plain binary
	var tarReader *tar.Reader

	if strings.HasSuffix(url, ".tar.gz") || strings.HasSuffix(url, ".tgz") {
		gzr, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("gzip decompression failed (corrupt archive?): %w", err)
		}
		defer gzr.Close()
		tarReader = tar.NewReader(gzr)
	} else if strings.HasSuffix(url, ".tar.xz") {
		// xz is not natively supported in Go stdlib.
		return fmt.Errorf("tar.xz archives not supported (use .tar.gz URL)")
	} else {
		// Not an archive — treat as raw binary (legacy behavior)
		if err := os.Rename(archivePath, d.FFmpegPath()); err != nil {
			return fmt.Errorf("rename to ffmpeg: %w", err)
		}
		// Don't remove archive since we renamed it
		return os.Chmod(d.FFmpegPath(), 0755)
	}

	// Extract ffmpeg and ffprobe from tar archive
	extracted := 0
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error (corrupt archive?): %w", err)
		}

		// Get the base filename (strip directory prefix)
		baseName := filepath.Base(hdr.Name)

		// Only extract ffmpeg and ffprobe (skip docs, man pages, etc.)
		if baseName != "ffmpeg" && baseName != "ffprobe" {
			continue
		}

		// Only extract regular files
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 {
			continue
		}

		targetPath := filepath.Join(toolsDir, baseName)

		// Atomic write: extract to .tmp first, then rename
		tmpTarget := targetPath + ".tmp2"
		outFile, err := os.OpenFile(tmpTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			return fmt.Errorf("create %s: %w", baseName, err)
		}

		if _, err := io.Copy(outFile, tarReader); err != nil {
			outFile.Close()
			os.Remove(tmpTarget)
			return fmt.Errorf("extract %s: %w", baseName, err)
		}
		outFile.Close()

		if err := os.Chmod(tmpTarget, 0755); err != nil {
			os.Remove(tmpTarget)
			return fmt.Errorf("chmod %s: %w", baseName, err)
		}

		if err := os.Rename(tmpTarget, targetPath); err != nil {
			os.Remove(tmpTarget)
			return fmt.Errorf("rename %s: %w", baseName, err)
		}

		extracted++
		slog.Info("Extracted binary from archive", "name", baseName, "path", targetPath)
	}

	if extracted == 0 {
		return fmt.Errorf("archive contained no ffmpeg or ffprobe binaries")
	}

	return nil
}

// getFFmpegVersion runs `ffmpeg -version` and returns the first line.
func (d *Downloader) getFFmpegVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "-version")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Parse first line: "ffmpeg version x.y.z ..."
	line := string(output)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	return line, nil
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
