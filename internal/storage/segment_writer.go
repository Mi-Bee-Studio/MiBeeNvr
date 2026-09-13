package storage

// segment_writer.go — per-segment writer state (#750).
//
// The frame write path used to pay, on EVERY frame: the global Manager mutex
// (all cameras serialized through one lock), a full os.Stat(tempPath) to
// classify dir-vs-file, and — on the append path — an open/write/close cycle.
// With N cameras × ~25fps that is hundreds of path resolutions and
// open/close pairs per second, which the block layer cannot coalesce.
//
// This file replaces that with a per-segment writer state, registered at
// CreateSegment (the segment's form is known there, so no classification
// stat is ever needed) or lazily classified once on first write:
//
//   - segment-lifetime FD retention on the append path (one open per segment)
//   - userspace write coalescing: frames accumulate in a bufio and flush at a
//     size threshold; CloseSegment flushes the remainder before fsync+rename,
//     so the temp→rename atomic-finalize contract is unchanged
//   - per-segment locking (cross-camera writes no longer serialize)
//   - cached camera attribution for health accounting (no per-frame map lookup)
//
// Crash semantics are unchanged: a crashed segment's .tmp (including any
// bytes still in the userspace buffer) is discarded by CleanupTempFiles —
// buffering inside an unfinalized temp was never durable to begin with.

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// segmentWriteBufSize is the userspace coalescing buffer for the append
	// path — same order as the MP4 muxer's (#521), tuned for the RPi-class
	// memory budget: a few hundred KB per in-flight file-form segment.
	segmentWriteBufSize = 256 * 1024
	// segmentFlushThreshold: once this many bytes are buffered, flush to the
	// kernel. Bounds both the crash loss window and per-segment memory.
	segmentFlushThreshold = 256 * 1024
	// segmentRevalidateWrites: with the FD retained, a vanished temp (unlinked
	// under us) is no longer surfaced by the write itself — unix happily
	// writes into an unlinked inode. Re-stat the path every N writes to keep
	// the #413 vanish detection alive at 1/Nth of the old cost.
	segmentRevalidateWrites = 1024
)

// Test seams (#750 TDD): syscall-count assertions swap these; production code
// must route classification stats and appends through them.
var (
	statFn     = os.Stat
	openFileFn = os.OpenFile
	syncFileFn = (*os.File).Sync // fsync seam — durability-tier assertions (#760)
)

// errSegmentReleased is returned when the writer state was already finalized
// (segment closed) but a late write raced in. It chains fs.ErrNotExist so
// callers treat it exactly like a vanished temp: benign frame drop.
var errSegmentReleased = fmt.Errorf("storage: segment already finalized: %w", fs.ErrNotExist)

// segmentWriter holds the per-segment write state: classification (dir vs
// file), camera attribution, and — for file-form segments — the retained FD
// and coalescing buffer. Registered at CreateSegment or classified lazily;
// released at segment finalize (or when the temp vanishes). Its mutex
// serializes writes within ONE segment, replacing the former global lock.
type segmentWriter struct {
	mu       sync.Mutex
	tempPath string
	cameraID string // cached attribution — kills the per-frame map lookup
	isDir    bool

	// Append-path state (file-form segments); file is opened lazily on the
	// first write and held until finalize. nil for dir-form segments.
	file     *os.File
	bw       *bufio.Writer
	pending  int64 // bytes buffered since the last flush
	writes   int64 // total write calls (drives vanish revalidation)
	released bool  // set when the segment finalized; late writes fail benign

	// Dir-form frame naming: frames written within the same millisecond get a
	// monotonically increasing suffix so they cannot overwrite each other
	// (name order still equals frame order — '.' < '_', suffix is numeric).
	lastBase string
	seq      int
}

// newSegmentWriter builds a classified writer state.
func newSegmentWriter(tempPath, cameraID string, isDir bool) *segmentWriter {
	return &segmentWriter{tempPath: tempPath, cameraID: cameraID, isDir: isDir}
}

// writeFrame appends one frame to the segment. Dir-form segments write the
// JPEG as its own timestamped file (per-frame files are #761's container work,
// out of scope here); file-form segments append through the coalescing buffer.
func (s *segmentWriter) writeFrame(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.released {
		return 0, errSegmentReleased
	}

	if s.isDir {
		base := time.Now().Format("20060102_150405.000")
		if base == s.lastBase {
			// Sub-millisecond burst: same-ms frames would collide into one
			// file (silent frame loss). Append a per-segment sequence suffix;
			// lexical order still matches frame order.
			s.seq++
			base = fmt.Sprintf("%s_%d", base, s.seq)
		} else {
			s.lastBase = base
			s.seq = 0
		}
		jpgPath := filepath.Join(s.tempPath, base+".jpg")
		if err := os.WriteFile(jpgPath, data, 0o644); err != nil {
			return 0, fmt.Errorf("storage: failed to write JPEG frame: %w", err)
		}
		return 0, nil
	}

	// Retained-FD caveat: a temp unlinked after the FD was opened would keep
	// accepting writes into the orphaned inode. Re-stat periodically so the
	// vanish still surfaces (benign, #413) at bounded staleness.
	s.writes++
	if s.file != nil && s.writes%segmentRevalidateWrites == 0 {
		if _, err := statFn(s.tempPath); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				s.releaseLocked()
				return 0, fmt.Errorf("storage: temp path not accessible: %w", err)
			}
			// Non-ENOENT stat trouble: report it — the write itself may still
			// succeed, so don't tear the segment down here.
			slog.Warn("storage: segment temp revalidation failed", "path", s.tempPath, "error", err)
		}
	}

	if s.file == nil {
		// No O_CREATE: the temp must exist (CreateSegment made it, or the lazy
		// classification just stat'ed it). Recreating a vanished temp here
		// would silently fork the segment into a fresh file and lose every
		// earlier frame — the ENOENT must surface so the recorder restarts
		// the segment (#413).
		f, err := openFileFn(s.tempPath, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, fmt.Errorf("storage: failed to open temp file for writing: %w", err)
		}
		s.file = f
		s.bw = bufio.NewWriterSize(f, segmentWriteBufSize)
	}

	n, err := s.bw.Write(data)
	if err != nil {
		return n, fmt.Errorf("storage: write failed: %w", err)
	}
	s.pending += int64(n)
	if s.pending >= segmentFlushThreshold {
		if err := s.bw.Flush(); err != nil {
			return n, fmt.Errorf("storage: flush failed: %w", err)
		}
		s.pending = 0
	}
	return n, nil
}

// finalize flushes any buffered bytes, fsyncs the retained FD, and closes it.
// Called from CloseSegment with s.mu held. A nil FD (segment written through
// an external handle — the MP4 muxer, merge outputs) is a no-op so the caller
// falls back to its reopen-and-sync path.
// finalize flushes the coalescing buffer (mandatory in every tier — buffered
// frames must reach the kernel before close/rename), then optionally fsyncs.
// skipSync=true is the #760 relaxed tier: rename atomicity still guarantees a
// crash leaves either the complete pre-crash bytes or no final file.
func (s *segmentWriter) finalize(skipSync bool) error {
	if s.bw != nil {
		if err := s.bw.Flush(); err != nil {
			return fmt.Errorf("storage: flush media: %w", err)
		}
		s.pending = 0
	}
	if s.file != nil {
		if !skipSync {
			if err := syncFileFn(s.file); err != nil {
				s.file.Close()
				s.file = nil
				s.bw = nil
				s.released = true
				return fmt.Errorf("storage: failed to sync temp file: %w", err)
			}
		}
		if err := s.file.Close(); err != nil {
			s.file = nil
			s.bw = nil
			s.released = true
			return fmt.Errorf("storage: failed to close temp file: %w", err)
		}
		s.file = nil
		s.bw = nil
	}
	s.released = true
	return nil
}

// releaseLocked drops the retained FD without flushing (temp vanished —
// nothing to preserve). Callers hold s.mu.
func (s *segmentWriter) releaseLocked() {
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
		s.bw = nil
	}
	s.pending = 0
	s.released = true
}

// ---------------------------------------------------------------------------
// Manager integration
// ---------------------------------------------------------------------------

// registerWriterState records the classified state for a segment. Segments
// created via CreateSegment call this with the form's classification; the
// path→camera map registration stays in registerTempPath.
func (m *Manager) registerWriterState(tempPath, cameraID string, isDir bool) {
	m.statesMu.Lock()
	if m.segmentStates == nil {
		m.segmentStates = make(map[string]*segmentWriter)
	}
	m.segmentStates[tempPath] = newSegmentWriter(tempPath, cameraID, isDir)
	m.statesMu.Unlock()
}

// stateFor returns the writer state for tempPath, lazily classifying and
// registering it for temps we did not create (external writers). The error
// is non-nil only when classification was attempted and the stat failed —
// callers distinguish a vanished temp (fs.ErrNotExist, benign) from real
// trouble; a registered segment never stats, so the hot path sees no error.
func (m *Manager) stateFor(tempPath string) (*segmentWriter, error) {
	m.statesMu.RLock()
	st := m.segmentStates[tempPath]
	m.statesMu.RUnlock()
	if st != nil {
		return st, nil
	}

	info, err := statFn(tempPath)
	if err != nil {
		return nil, err
	}
	cameraID := m.lookupCameraByPath(tempPath)
	st = newSegmentWriter(tempPath, cameraID, info.IsDir())

	m.statesMu.Lock()
	if m.segmentStates == nil {
		m.segmentStates = make(map[string]*segmentWriter)
	}
	if existing := m.segmentStates[tempPath]; existing != nil {
		st = existing // concurrent first-writers: keep the winner
	} else {
		m.segmentStates[tempPath] = st
	}
	m.statesMu.Unlock()
	return st, nil
}

// dropWriterState removes the state for tempPath (best-effort, idempotent).
func (m *Manager) dropWriterState(tempPath string) {
	m.statesMu.Lock()
	delete(m.segmentStates, tempPath)
	m.statesMu.Unlock()
}

// segmentStateCount reports the number of live writer states (test hook for
// FD/state lifecycle assertions).
func (m *Manager) segmentStateCount() int {
	m.statesMu.RLock()
	defer m.statesMu.RUnlock()
	return len(m.segmentStates)
}
