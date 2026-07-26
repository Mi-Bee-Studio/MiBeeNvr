package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// defaultModelURL is the upstream YOLOv11-nano ONNX model release. It is fetched
// by `mibee-nvr download-model` and (in Docker builds) by the release Dockerfile.
const defaultModelURL = "https://github.com/ultralytics/assets/releases/download/v8.3.0/yolo11n.onnx"

// modelFilename is the canonical on-disk filename served at /models/{filename}.
const modelFilename = "yolo11n.onnx"

// downloadBackoff is the fixed per-attempt backoff schedule (see internal/transcoding/downloader.go
// for the same pattern). 5 attempts → ~31s of total backoff worst case.
var downloadBackoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
}

// minModelSize is the absolute lower bound for a valid yolo11n.onnx. The real
// file is ~5.4MB; anything below 5MB is definitely truncated/corrupt. This is a
// last-resort sanity floor — the primary integrity check is Content-Length match.
const minModelSize int64 = 5 * 1024 * 1024 // 5MB

// downloadHTTPClient is the HTTP client used for model downloads. It has a
// generous timeout (5 min) to survive slow CDN edges without giving up, but is
// NOT infinite (the bare http.Get previously had no timeout at all → a stalled
// connection would hang the CLI forever). Package-level so tests can swap it.
var downloadHTTPClient = &http.Client{Timeout: 5 * time.Minute}

func cmdDownloadModel() {
	var cfgPath string
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--config" {
			i++
			if i < len(os.Args) {
				cfgPath = os.Args[i]
			}
		}
	}

	if cfgPath == "" {
		// Also check -config flag
		for i, arg := range os.Args {
			if arg == "-config" && i+1 < len(os.Args) {
				cfgPath = os.Args[i+1]
			}
		}
	}

	if cfgPath == "" {
		cfgPath = "mibee-nvr.yaml"
	}

	// Try to load config to get storage root
	var modelDir string
	cfg, err := config.Load(cfgPath)
	if err == nil && cfg != nil {
		modelDir = filepath.Join(cfg.Storage.RootDir, "models")
	} else {
		// Auto-detect data directory
		dataDir := os.Getenv("NVR_DATA_DIR")
		if dataDir == "" {
			if info, err := os.Stat("/data"); err == nil && info.IsDir() {
				dataDir = "/data"
			} else {
				dataDir = "/var/lib/mibee-nvr"
			}
		}
		modelDir = filepath.Join(dataDir, "models")
		fmt.Printf("Config not found, using default data directory: %s\n", dataDir)
	}

	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating models directory %s: %v\n", modelDir, err)
		os.Exit(1)
	}

	// downloadModelFile returns an error instead of calling os.Exit itself so its
	// `defer out.Close()` / `defer resp.Body.Close()` actually run on every path
	// (os.Exit skips defers — gocritic exitAfterDefer). The single os.Exit lives
	// here, in the command entry point, where there is nothing left deferred.
	if err := downloadModelFile(modelDir, modelFilename, defaultModelURL); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// downloadModelFile downloads `filename` from `modelURL` into `modelDir` with
// bounded retry, HTTP Range resume, strict size verification, and atomic write.
//
// Why all three defenses (retry + resume + size check):
//   - GitHub release assets are served via Azure blob CDN with a 1-hour signed
//     URL. On unreliable networks (notably China → GitHub) the connection is
//     silently truncated mid-transfer. A bare http.Get leaves a partial file on
//     disk; subsequent runs re-serve it to the browser, and ONNX Runtime Web
//     fails to parse the truncated protobuf ("ERROR_CODE 7", issue #109).
//   - Retry handles transient failures. Range resume avoids re-downloading the
//     already-correct prefix on each retry (faster, less bandwidth). The strict
//     Content-Length check is the hard gate that rejects a truncated file even
//     when the server reports a clean 200/206.
//
// The function is testable: pass a httptest.Server URL as modelURL.
func downloadModelFile(modelDir, filename, modelURL string) error {
	modelPath := filepath.Join(modelDir, filename)
	partPath := modelPath + ".part"

	fmt.Printf("Downloading YOLOv11n ONNX model...\n")
	fmt.Printf("  URL: %s\n", modelURL)
	fmt.Printf("  Destination: %s\n", modelPath)

	ctx := context.Background()
	var lastErr error

	for attempt := range downloadBackoff {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(downloadBackoff[attempt-1]):
			}
		}

		lastErr = downloadOnceWithResume(ctx, modelURL, partPath, attempt+1)
		if lastErr == nil {
			// Final integrity check + atomic rename.
			if err := verifyAndAtomicallyInstall(partPath, modelPath); err != nil {
				lastErr = err
				slog.Warn("model download integrity check failed", "attempt", attempt+1, "error", err)
				continue
			}
			fmt.Printf("\nModel downloaded successfully!\n")
			if info, err := os.Stat(modelPath); err == nil {
				fmt.Printf("  File: %s\n", modelPath)
				fmt.Printf("  Size: %d bytes (%.1f MB)\n", info.Size(), float64(info.Size())/(1024*1024))
			}
			return nil
		}

		slog.Warn("model download attempt failed", "attempt", attempt+1, "error", lastErr)
	}

	// All retries exhausted — clean up the partial file so a later run doesn't
	// mistake it for a complete download (the exact bug we're fixing).
	os.Remove(partPath)
	return fmt.Errorf("download failed after %d attempts: %w", len(downloadBackoff), lastErr)
}

// downloadOnceWithResume performs a single download attempt.
//
// If a `.part` file already exists (from a previous failed attempt in this or a
// prior run), it sends an HTTP `Range: bytes=N-` request to resume from byte N.
// The server may respond with:
//   - 206 Partial Content: resume honored — we append to the existing .part.
//   - 200 OK: resume not supported (or the .part was already complete) — we
//     truncate the .part and start fresh.
//
// expectedSize (from Content-Length / Content-Range) is recorded in the .part's
// trailing metadata via verifyAndAtomicallyInstall for the strict size check.
func downloadOnceWithResume(ctx context.Context, modelURL, partPath string, attempt int) error {
	existingSize := int64(0)
	if info, err := os.Stat(partPath); err == nil {
		existingSize = info.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}

	resp, err := downloadHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	var expectedTotal int64
	var writeMode int
	var startOffset int64

	switch resp.StatusCode {
	case http.StatusPartialContent:
		// Resume honored. Parse Content-Range: bytes start-end/total.
		expectedTotal = parseContentRangeTotal(resp.Header.Get("Content-Range"))
		writeMode = os.O_WRONLY | os.O_APPEND
		startOffset = existingSize
		fmt.Printf("\r  Resuming from byte %d (attempt %d)...", existingSize, attempt)
	case http.StatusOK:
		// Full download (server doesn't support Range, or no existing .part).
		// Truncate any stale partial.
		expectedTotal = resp.ContentLength
		writeMode = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		startOffset = 0
		fmt.Printf("\r  Downloading (attempt %d)...", attempt)
	default:
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	out, err := os.OpenFile(partPath, writeMode, 0o644)
	if err != nil {
		return fmt.Errorf("open part file: %w", err)
	}
	// NOTE: do NOT close `out` before the function returns — it is written to
	// below. (A premature close previously caused "file already closed" on the
	// first Write and left a 0-byte file.) defer runs on every return path.
	defer out.Close()

	// If this attempt determined the total via Content-Length/Content-Range,
	// remember it for the integrity check. We stash it in a sidecar file so
	// verifyAndAtomicallyInstall can compare against the final on-disk size
	// even across attempts. (Content-Range's /total is authoritative; a 200
	// with unknown Content-Length = -1 → skip strict check, fall back to
	// minModelSize.)
	if expectedTotal > 0 {
		if err := writeExpectedSizeSidecar(partPath, expectedTotal); err != nil {
			return fmt.Errorf("write expected-size sidecar: %w", err)
		}
	}

	downloaded, copyErr := streamCopy(out, resp.Body, startOffset, expectedTotal)
	if copyErr != nil {
		return copyErr
	}

	// If the server didn't tell us the total, we can't strictly verify; accept
	// any non-tiny download and let verifyAndAtomicallyInstall apply minModelSize.
	if expectedTotal <= 0 {
		return nil
	}
	if startOffset+downloaded < expectedTotal {
		return fmt.Errorf("download truncated: got %d bytes, expected %d", startOffset+downloaded, expectedTotal)
	}
	return nil
}

// streamCopy copies resp.Body to out with a progress line. Returns bytes copied
// (excluding the startOffset prefix on resume). A read or write error aborts.
func streamCopy(out io.Writer, body io.Reader, startOffset, expectedTotal int64) (int64, error) {
	buf := make([]byte, 32*1024)
	var downloaded int64
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				return downloaded, fmt.Errorf("write file: %w", writeErr)
			}
			downloaded += int64(n)
			totalSoFar := startOffset + downloaded
			if expectedTotal > 0 {
				pct := float64(totalSoFar) / float64(expectedTotal) * 100
				fmt.Printf("\r  Progress: %.1f%% (%d/%d bytes)", pct, totalSoFar, expectedTotal)
			} else {
				fmt.Printf("\r  Downloaded: %d bytes", totalSoFar)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return downloaded, nil
			}
			return downloaded, fmt.Errorf("read response: %w", readErr)
		}
	}
}

// verifyAndAtomicallyInstall runs the final integrity gate and renames
// `.part` → final filename. It removes the .part on any failure.
func verifyAndAtomicallyInstall(partPath, modelPath string) error {
	defer os.Remove(partPath + ".size") // clean up sidecar regardless

	info, err := os.Stat(partPath)
	if err != nil {
		return fmt.Errorf("stat part file: %w", err)
	}

	// 1. Absolute floor — a truncated yolo11n is always < 5MB.
	if info.Size() < minModelSize {
		return fmt.Errorf("model file too small: %d bytes (minimum %d)", info.Size(), minModelSize)
	}

	// 2. Strict Content-Length match (the hard gate). If the server reported a
	//    total and we didn't reach it, the file is truncated — reject it.
	if expected, ok := readExpectedSizeSidecar(partPath); ok && info.Size() != expected {
		return fmt.Errorf("size mismatch: got %d bytes, expected %d (truncated download)", info.Size(), expected)
	}

	// Atomic install: temp → rename. Crash-safe; either the old file or the new
	// one is on disk, never a half-written one.
	if err := os.Rename(partPath, modelPath); err != nil {
		return fmt.Errorf("rename part→final: %w", err)
	}
	return nil
}

// expectedSizeSidecarName returns the sidecar path for a given .part file.
func expectedSizeSidecarName(partPath string) string { return partPath + ".size" }

func writeExpectedSizeSidecar(partPath string, expected int64) error {
	return os.WriteFile(expectedSizeSidecarName(partPath), []byte(fmt.Sprintf("%d", expected)), 0o644)
}

func readExpectedSizeSidecar(partPath string) (int64, bool) {
	data, err := os.ReadFile(expectedSizeSidecarName(partPath))
	if err != nil {
		return 0, false
	}
	var n int64
	if _, err := fmt.Sscanf(string(data), "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

// parseContentRangeTotal extracts the /total from a Content-Range header value
// of the form "bytes 0-1023/2048". Returns 0 if the header is missing or
// unparseable (caller treats 0 as "unknown total").
func parseContentRangeTotal(header string) int64 {
	// Find the last '/' — total is whatever follows it (may be "*" for unknown).
	for i := len(header) - 1; i >= 0; i-- {
		if header[i] == '/' {
			var total int64
			if _, err := fmt.Sscanf(header[i+1:], "%d", &total); err == nil {
				return total
			}
			return 0
		}
	}
	return 0
}
