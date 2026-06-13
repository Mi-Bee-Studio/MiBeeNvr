package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

func cmdDownloadModel() {
	var cfgPath string
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--config":
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

	if err := os.MkdirAll(modelDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating models directory %s: %v\n", modelDir, err)
		os.Exit(1)
	}

	downloadModelFile(modelDir, "yolov11n.onnx")
	os.Exit(0)
}

func downloadModelFile(modelDir, filename string) {
	modelURL := "https://github.com/ultralytics/assets/releases/download/v8.3.0/yolo11n.onnx"
	modelPath := filepath.Join(modelDir, filename)

	fmt.Printf("Downloading YOLOv11n ONNX model...\n")
	fmt.Printf("  URL: %s\n", modelURL)
	fmt.Printf("  Destination: %s\n", modelPath)

	out, err := os.Create(modelPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating file: %v\n", err)
		os.Exit(1)
	}
	defer out.Close()

	resp, err := http.Get(modelURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading model: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Download failed: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	totalSize := resp.ContentLength

	// Download with progress
	var downloaded int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				fmt.Fprintf(os.Stderr, "Error writing file: %v\n", writeErr)
				os.Exit(1)
			}
			downloaded += int64(n)
			if totalSize > 0 {
				pct := float64(downloaded) / float64(totalSize) * 100
				fmt.Printf("\r  Progress: %.1f%% (%d/%d MB)", pct, downloaded/(1024*1024), totalSize/(1024*1024))
			} else {
				fmt.Printf("\r  Downloaded: %d MB", downloaded/(1024*1024))
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			fmt.Fprintf(os.Stderr, "\nError reading response: %v\n", readErr)
			os.Exit(1)
		}
	}
	fmt.Println()

	// Verify file size
	info, err := os.Stat(modelPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error verifying model file: %v\n", err)
		os.Exit(1)
	}

	const minSize int64 = 5 * 1024 * 1024 // 5MB
	if info.Size() < minSize {
		fmt.Fprintf(os.Stderr, "Error: model file too small (%d bytes, expected >= %d bytes)\n", info.Size(), minSize)
		os.Exit(1)
	}

	fmt.Printf("\nModel downloaded successfully!\n")
	fmt.Printf("  File: %s\n", modelPath)
	fmt.Printf("  Size: %d bytes (%.1f MB)\n", info.Size(), float64(info.Size())/(1024*1024))
}
