// Package recframes owns frame-level access to frame-based recordings
// (#1009 batch 2): the TTL/mtime frame-directory listing cache, MJPEG and
// timelapse directory frame indexes, the AVI video-frame index walk, frame
// capture-timestamp extraction (filename pattern / JPEG EXIF / ModTime), and
// the timelapse-merge codec probe cache. The HTTP layer keeps only
// request/response shaping on top of this service.
package recframes

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// listCacheTTL bounds how long a cached directory listing is served without
// re-stat.
const listCacheTTL = 500 * time.Millisecond

type listEntry struct {
	names     []string
	dirMtime  int64
	scannedAt time.Time
}

// DirFrame is one indexed frame of an MJPEG/timelapse frame directory.
type DirFrame struct {
	Filename  string
	Size      int64
	Timestamp time.Time
}

// AVIFrame is one indexed video frame of an AVI recording (a complete JPEG
// payload at a known byte range, #321).
type AVIFrame struct {
	Index  int   // 0-based video frame index
	Offset int64 // absolute file offset of the JPEG payload
	Size   int64
	PTSUs  int64 // microseconds from stream start
}

// Service carries the frame caches. Safe for concurrent use.
type Service struct {
	listMu    sync.Mutex
	listCache map[string]*listEntry

	codecCache sync.Map // merge MP4 path → codec (outputs are write-once)
}

// New creates an empty service.
func New() *Service { return &Service{listCache: make(map[string]*listEntry)} }

// isImageFile reports jpg/jpeg/png (the listing-cache filter — parity with the
// historical ?frame=N resolution which accepted PNG snapshots too).
func isImageFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png")
}

// SortedImageFiles returns the sorted list of image filenames in dir, using a
// short-TTL cache to avoid os.ReadDir + sort on every request. The cache is
// invalidated when the directory's mtime changes (new frames written) or after
// listCacheTTL. This matters for MJPEG/timelapse frame dirs which can hold
// thousands of JPEGs; without it each ?frame=N / list-frames request
// re-scanned and re-sorted the whole directory.
func (s *Service) SortedImageFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	mtime := info.ModTime().Unix()

	s.listMu.Lock()
	cached, ok := s.listCache[dir]
	if ok && cached.dirMtime == mtime && time.Since(cached.scannedAt) < listCacheTTL {
		names := cached.names
		s.listMu.Unlock()
		return names, nil
	}
	s.listMu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isImageFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })

	s.listMu.Lock()
	s.listCache[dir] = &listEntry{names: names, dirMtime: mtime, scannedAt: time.Now()}
	s.listMu.Unlock()
	return names, nil
}

// MJPEGFrames lists the JPEG frames of an MJPEG recording directory
// (jpg/jpeg only, sorted by filename, sequential indices implied by order).
func (s *Service) MJPEGFrames(dir string) ([]DirFrame, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	frames := []DirFrame{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		frames = append(frames, DirFrame{Filename: name, Size: fi.Size()})
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].Filename < frames[j].Filename })
	return frames, nil
}

// isTimelapseFrame reports the supported timelapse frame extensions.
func isTimelapseFrame(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".h264") || strings.HasSuffix(lower, ".h265")
}

// TimelapseFrames lists the frames of a timelapse (or MJPEG) recording
// directory with capture timestamps extracted per frame (filename pattern,
// then JPEG EXIF, then ModTime).
func (s *Service) TimelapseFrames(dir string) ([]DirFrame, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	frames := []DirFrame{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isTimelapseFrame(name) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		frames = append(frames, DirFrame{
			Filename:  name,
			Size:      fi.Size(),
			Timestamp: frameTimestamp(name, filepath.Join(dir, name), fi.ModTime()),
		})
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].Filename < frames[j].Filename })
	return frames, nil
}

// AVIFrames walks the AVI movi chunk headers and returns the video frames'
// byte ranges (complete JPEGs). The payloads are not read.
func (s *Service) AVIFrames(path string) ([]AVIFrame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dmx, err := avi.NewDemuxer(f)
	if err != nil {
		return nil, err
	}
	entries, err := dmx.VideoFrameIndex()
	if err != nil {
		return nil, err
	}
	frames := make([]AVIFrame, 0, len(entries))
	for _, e := range entries {
		frames = append(frames, AVIFrame{Index: e.Index, Offset: e.Offset, Size: int64(e.Size), PTSUs: e.PTSUs})
	}
	return frames, nil
}

// ProbeCodec returns "h264" / "h265" / "mjpeg" for the timelapse merge MP4
// at path, or "" if the codec could not be determined. Merge outputs are
// write-once, so results are cached permanently per path — this avoids
// re-parsing the MP4 box header on every playback request.
func (s *Service) ProbeCodec(path string) string {
	if v, ok := s.codecCache.Load(path); ok {
		return v.(string)
	}
	codec := probeCodec(path)
	s.codecCache.Store(path, codec)
	return codec
}

func probeCodec(path string) string {
	info, err := mediaprobe.ProbeMP4(path)
	if err != nil {
		return ""
	}
	switch info.Codec {
	case model.TimelapseMergeCodecH264, model.TimelapseMergeCodecH265:
		return info.Codec
	default:
		// mediaprobe returns the raw codec string for mjpa; normalize to "mjpeg".
		return model.TimelapseMergeCodecMJPEG
	}
}
