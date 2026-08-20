package config

import "os"

// DockerDataDir reports the data-volume path when the process runs inside a
// containerized deployment: NVR_DATA_DIR when set, else /data when it exists.
// Empty means bare-metal — callers must not silently remap storage paths then.
func DockerDataDir() string {
	if dir := os.Getenv("NVR_DATA_DIR"); dir != "" {
		return dir
	}
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		return "/data"
	}
	return ""
}
