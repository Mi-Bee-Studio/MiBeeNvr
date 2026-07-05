package merge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// AVI FOURCC constants for merge operations.
const (
	fccRIFF = 0x46464952 // 'RIFF'
	fccAVI  = 0x20495641 // 'AVI '
	fccLIST = 0x5453494C // 'LIST'
	fcchdrl = 0x6C726468 // 'hdrl'
	fccavih = 0x68697661 // 'avih'
	fccstrl = 0x6C727473 // 'strl'
	fccstrh = 0x68727473 // 'strh'
	fccmovi = 0x69766F6D // 'movi'
	fccidx1 = 0x31786469 // 'idx1'
	fcc00dc = 0x63643030 // '00dc' (stream 0 compressed video)
	fcc01wb = 0x62773130 // '01wb' (stream 1 audio data)
	fccvids = 0x73646976 // 'vids' (video stream)
	fccauds = 0x73647561 // 'auds' (audio stream)

	aviKeyFrame = 0x00000010

	aviMainHeaderSize   = 56
	aviStreamHeaderSize = 56

	// Default microsec per frame for rate calculation (≈30fps).
	defaultMicroSecPerFrame = 33333
)

// aviIndexEntry holds one idx1 entry for tracking video/audio chunk positions.
type aviIndexEntry struct {
	ckID   uint32
	flags  uint32
	offset uint32 // relative to movi data start
	length uint32
}

// aviHeaderOffsets records byte positions in the header for backpatching.
type aviHeaderOffsets struct {
	riffSize    int64 // always 4
	totalFrames int64 // avih dwTotalFrames
	maxBPS      int64 // avih dwMaxBytesPerSec
	videoLength int64 // video strh dwLength
	videoBufSz  int64 // video strh dwSuggestedBufferSize
	audioLength int64 // audio strh dwLength (0 = no audio)
	audioBufSz  int64 // audio strh dwSuggestedBufferSize (0 = no audio)
	moviListSz  int64 // movi LIST size field
}

// MergeAVISegments merges multiple AVI recording files into a single AVI file.
// It reads the hdrl from the first source, streams movi chunks from all sources
// using a 1MB buffer (via the Demuxer), rebuilds idx1, and backpatches all size
// fields. Returns the merged recording, source recording IDs (for deferred deletion),
// and error.
func MergeAVISegments(ctx context.Context, segments []*model.Recording, store *storage.Manager, cameraID string) (*model.Recording, []string, error) {
	if len(segments) == 0 {
		return nil, nil, fmt.Errorf("no segments to merge")
	}

	// Validate all segments have AVI format.
	for i, seg := range segments {
		if seg.Format != model.FormatAVI {
			return nil, nil, fmt.Errorf("segment %d: expected AVI format, got %s", i, seg.Format)
		}
	}

	// Sort by StartedAt so merged timestamps are correct.
	sorted := make([]*model.Recording, len(segments))
	copy(sorted, segments)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].StartedAt.Before(sorted[j].StartedAt)
	})

	// Create output segment via store (temp → atomic rename).
	tempPath, finalPath, err := store.CreateSegment(cameraID, "avi")
	if err != nil {
		return nil, nil, fmt.Errorf("create merged segment: %w", err)
	}

	// Open output file for writing.
	out, err := os.Create(tempPath)
	if err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("create output file: %w", err)
	}
	defer out.Close()

	// ── Step 1: Read the first source's RIFF + hdrl + movi LIST header. ──
	headerBuf, offs, err := readFirstAVIHeader(sorted[0].FilePath)
	if err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("read first source header: %w", err)
	}

	// ── Step 2: Write the header to the output. ──
	if _, err := out.Write(headerBuf); err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("write header: %w", err)
	}
	moviDataStart := int64(len(headerBuf)) // absolute output offset where chunk data goes

	// ── Step 3: Stream movi chunks from all sources, tracking idx1 entries. ──
	buf := make([]byte, mergeBufferSize)
	var entries []aviIndexEntry

	// Metadata accumulator.
	var (
		totalVideoFrames int
		totalAudioBytes  int64
		maxFrameSize     int
		hasAudio         bool
	)

	// Timing — computed from the source Recording metadata.
	var earliestStarted time.Time
	var latestEnded time.Time
	var totalDuration float64
	var totalFrameCount int

	for _, seg := range sorted {
		// Accumulate recording metadata.
		if seg.StartedAt.Before(earliestStarted) || earliestStarted.IsZero() {
			earliestStarted = seg.StartedAt
		}
		if seg.EndedAt.After(latestEnded) {
			latestEnded = seg.EndedAt
		}
		totalDuration += seg.Duration
		totalFrameCount += seg.FrameCount

		// Open source with Demuxer.
		f, err := os.Open(seg.FilePath)
		if err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("open segment %s: %w", seg.FilePath, err)
		}

		demuxer, err := avi.NewDemuxer(f)
		if err != nil {
			f.Close()
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("create demuxer for %s: %w", seg.FilePath, err)
		}

		// Stream chunks one at a time.
		for {
			chunk, err := demuxer.NextChunk()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				f.Close()
				os.Remove(tempPath)
				return nil, nil, fmt.Errorf("read chunk from %s: %w", seg.FilePath, err)
			}

			// Determine chunk fourcc and flags.
			var fourcc uint32
			flags := uint32(0)
			if chunk.Type == avi.ChunkVideo {
				fourcc = fcc00dc
				flags = aviKeyFrame
				totalVideoFrames++
				if len(chunk.Data) > maxFrameSize {
					maxFrameSize = len(chunk.Data)
				}
			} else {
				fourcc = fcc01wb
				hasAudio = true
				totalAudioBytes += int64(len(chunk.Data))
			}

			// Record offset relative to movi data start (before writing this chunk).
			currentOutPos, _ := out.Seek(0, io.SeekCurrent)
			relOffset := uint32(currentOutPos - moviDataStart)

			// ── Write chunk raw to output: fourcc + size + data + pad. ──
			chunkHdr := make([]byte, 8)
			binary.LittleEndian.PutUint32(chunkHdr[0:4], fourcc)
			binary.LittleEndian.PutUint32(chunkHdr[4:8], uint32(len(chunk.Data)))
			if _, err := out.Write(chunkHdr); err != nil {
				f.Close()
				os.Remove(tempPath)
				return nil, nil, fmt.Errorf("write chunk header: %w", err)
			}

			// Write chunk data via streaming copy with the 1MB buffer.
			// chunk.Data is already in memory from Demuxer; we copy it through the
			// buffer to maintain the streaming pattern (though for Demuxer chunks
			// the data is unavoidably in memory per-frame).
			if _, err := copyBytes(out, chunk.Data, buf); err != nil {
				f.Close()
				os.Remove(tempPath)
				return nil, nil, fmt.Errorf("write chunk data: %w", err)
			}

			// Pad to even boundary.
			if len(chunk.Data)%2 == 1 {
				if _, err := out.Write([]byte{0}); err != nil {
					f.Close()
					os.Remove(tempPath)
					return nil, nil, fmt.Errorf("write chunk padding: %w", err)
				}
			}

			// Record idx1 entry (offset is from chunk data start in output).
			entries = append(entries, aviIndexEntry{
				ckID:   fourcc,
				flags:  flags,
				offset: relOffset,
				length: uint32(len(chunk.Data)),
			})
		}
		f.Close()
	}

	// ── Step 4: Write idx1 index at end of file. ──
	idx1Pos, _ := out.Seek(0, io.SeekCurrent)
	idx1DataLen := len(entries) * 16

	idx1Hdr := make([]byte, 8)
	binary.LittleEndian.PutUint32(idx1Hdr[0:4], fccidx1)
	binary.LittleEndian.PutUint32(idx1Hdr[4:8], uint32(idx1DataLen))
	if _, err := out.Write(idx1Hdr); err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("write idx1 header: %w", err)
	}

	var entryBytes [16]byte
	for _, e := range entries {
		binary.LittleEndian.PutUint32(entryBytes[0:4], e.ckID)
		binary.LittleEndian.PutUint32(entryBytes[4:8], e.flags)
		binary.LittleEndian.PutUint32(entryBytes[8:12], e.offset)
		binary.LittleEndian.PutUint32(entryBytes[12:16], e.length)
		if _, err := out.Write(entryBytes[:]); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("write idx1 entry: %w", err)
		}
	}

	// ── Step 5: Backpatch all size fields in the header. ──
	totalFileSize, _ := out.Seek(0, io.SeekCurrent)
	uint32Buf := make([]byte, 4)

	// 5a. RIFF size at offset 4 = total file size - 8.
	binary.LittleEndian.PutUint32(uint32Buf, uint32(totalFileSize-8))
	if _, err := out.Seek(offs.riffSize, io.SeekStart); err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("seek to riff size: %w", err)
	}
	if _, err := out.Write(uint32Buf); err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("write riff size: %w", err)
	}

	// 5b. movi LIST size.
	moviSize := uint32(idx1Pos - offs.moviListSz - 4)
	binary.LittleEndian.PutUint32(uint32Buf, moviSize)
	if _, err := out.Seek(offs.moviListSz, io.SeekStart); err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("seek to movi list size: %w", err)
	}
	if _, err := out.Write(uint32Buf); err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("write movi list size: %w", err)
	}

	// 5c. avih dwTotalFrames.
	if offs.totalFrames > 0 {
		binary.LittleEndian.PutUint32(uint32Buf, uint32(totalVideoFrames))
		if _, err := out.Seek(offs.totalFrames, io.SeekStart); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("seek to totalFrames: %w", err)
		}
		if _, err := out.Write(uint32Buf); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("write totalFrames: %w", err)
		}
	}

	// 5d. avih dwMaxBytesPerSec.
	if offs.maxBPS > 0 && totalVideoFrames > 0 {
		totalDurUs := uint64(totalVideoFrames) * uint64(defaultMicroSecPerFrame)
		if totalDurUs > 0 {
			maxBPS := uint32(uint64(totalFileSize) * 1000000 / totalDurUs)
			binary.LittleEndian.PutUint32(uint32Buf, maxBPS)
			if _, err := out.Seek(offs.maxBPS, io.SeekStart); err != nil {
				os.Remove(tempPath)
				return nil, nil, fmt.Errorf("seek to maxBPS: %w", err)
			}
			if _, err := out.Write(uint32Buf); err != nil {
				os.Remove(tempPath)
				return nil, nil, fmt.Errorf("write maxBPS: %w", err)
			}
		}
	}

	// 5e. Video strh dwLength.
	if offs.videoLength > 0 {
		binary.LittleEndian.PutUint32(uint32Buf, uint32(totalVideoFrames))
		if _, err := out.Seek(offs.videoLength, io.SeekStart); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("seek to videoLength: %w", err)
		}
		if _, err := out.Write(uint32Buf); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("write videoLength: %w", err)
		}
	}

	// 5f. Video strh dwSuggestedBufferSize.
	if offs.videoBufSz > 0 {
		binary.LittleEndian.PutUint32(uint32Buf, uint32(maxFrameSize))
		if _, err := out.Seek(offs.videoBufSz, io.SeekStart); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("seek to videoBufSz: %w", err)
		}
		if _, err := out.Write(uint32Buf); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("write videoBufSz: %w", err)
		}
	}

	// 5g. Audio strh dwLength + dwSuggestedBufferSize.
	if hasAudio && offs.audioLength > 0 {
		binary.LittleEndian.PutUint32(uint32Buf, uint32(totalAudioBytes))
		if _, err := out.Seek(offs.audioLength, io.SeekStart); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("seek to audioLength: %w", err)
		}
		if _, err := out.Write(uint32Buf); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("write audioLength: %w", err)
		}
	}
	if hasAudio && offs.audioBufSz > 0 {
		binary.LittleEndian.PutUint32(uint32Buf, uint32(totalAudioBytes))
		if _, err := out.Seek(offs.audioBufSz, io.SeekStart); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("seek to audioBufSz: %w", err)
		}
		if _, err := out.Write(uint32Buf); err != nil {
			os.Remove(tempPath)
			return nil, nil, fmt.Errorf("write audioBufSz: %w", err)
		}
	}

	// ── Step 6: Sync output and close. ──
	if err := out.Sync(); err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("sync output: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("close output: %w", err)
	}

	// ── Step 7: Atomic rename via CloseSegment. ──
	if err := store.CloseSegment(tempPath, finalPath); err != nil {
		os.Remove(tempPath)
		return nil, nil, fmt.Errorf("close merged segment: %w", err)
	}

	// Gather final file size.
	fi, err := os.Stat(finalPath)
	if err != nil {
		os.Remove(finalPath)
		return nil, nil, fmt.Errorf("stat merged file: %w", err)
	}

	// Build source paths list for deferred deletion.
	sourcePaths := make([]string, len(sorted))
	for i, seg := range sorted {
		sourcePaths[i] = seg.FilePath
	}

	// Build merged recording.
	merged := &model.Recording{
		ID:         fmt.Sprintf("%d", time.Now().UnixNano()),
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.FormatAVI,
		StartedAt:  earliestStarted,
		EndedAt:    latestEnded,
		Duration:   totalDuration,
		FileSize:   fi.Size(),
		FrameCount: totalFrameCount,
	}

	return merged, sourcePaths, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// readFirstAVIHeader reads the first source AVI file, finds the hdrl and movi
// LIST structures, and returns the header bytes (RIFF + hdrl + movi LIST header
// up to the 'movi' fourcc) plus backpatching offsets.
func readFirstAVIHeader(path string) ([]byte, *aviHeaderOffsets, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}

	// Read at most 256 KB — headers never exceed this.
	readSize := fi.Size()
	if readSize > 256*1024 {
		readSize = 256 * 1024
	}

	buf := make([]byte, readSize)
	if _, err := io.ReadFull(f, buf); err != nil && err != io.ErrUnexpectedEOF {
		return nil, nil, err
	}

	offs := &aviHeaderOffsets{riffSize: 4}
	pos := int64(12) // skip RIFF header (12 bytes)

	for pos < int64(len(buf))-8 {
		ckID := binary.LittleEndian.Uint32(buf[pos : pos+4])
		ckSize := int64(binary.LittleEndian.Uint32(buf[pos+4 : pos+8]))

		if ckID == fccLIST {
			if pos+12 > int64(len(buf)) {
				break
			}
			listType := binary.LittleEndian.Uint32(buf[pos+8 : pos+12])

			switch listType {
			case fccmovi:
				offs.moviListSz = pos + 4 // offset of the LIST size field
				// Return everything up to and including the 'movi' fourcc.
				headerEnd := pos + 12 // 'LIST' + size + 'movi'
				return buf[:headerEnd], offs, nil

			case fcchdrl:
				walkHdrl(buf, pos+12, uint32(ckSize-4), offs)
			}

			pos += 8 + align2(ckSize)
		} else {
			pos += 8 + align2(ckSize)
		}

		if pos > int64(len(buf)) {
			break
		}
	}

	return nil, nil, errors.New("avi: movi LIST not found in first source")
}

// walkHdrl walks the contents of the hdrl LIST to find avih and strl offsets.
func walkHdrl(buf []byte, start int64, size uint32, offs *aviHeaderOffsets) {
	end := start + int64(size)
	pos := start

	for pos+8 <= end && pos+8 <= int64(len(buf)) {
		ckID := binary.LittleEndian.Uint32(buf[pos : pos+4])
		ckSize := int64(binary.LittleEndian.Uint32(buf[pos+4 : pos+8]))

		switch ckID {
		case fccavih:
			// avih chunk data.
			// Layout from chunk start: 'avih'(4) + size(4) + data(56)
			// dwMaxBytesPerSec at chunk_start + 12
			// dwTotalFrames at chunk_start + 24
			if ckSize >= 20 {
				offs.maxBPS = pos + 12
			}
			if ckSize >= 24 {
				offs.totalFrames = pos + 24
			}

		case fccLIST:
			if pos+12 > end || pos+12 > int64(len(buf)) {
				return
			}
			listType := binary.LittleEndian.Uint32(buf[pos+8 : pos+12])
			if listType == fccstrl {
				walkStrl(buf, pos+12, uint32(ckSize-4), offs)
			}
		}

		pos += 8 + align2(ckSize)
		if pos > end {
			break
		}
	}
}

// walkStrl walks one strl LIST to find strh offsets.
func walkStrl(buf []byte, start int64, size uint32, offs *aviHeaderOffsets) {
	end := start + int64(size)
	pos := start

	for pos+8 <= end && pos+8 <= int64(len(buf)) {
		ckID := binary.LittleEndian.Uint32(buf[pos : pos+4])
		ckSize := int64(binary.LittleEndian.Uint32(buf[pos+4 : pos+8]))

		if ckID == fccstrh {
			// Determine stream type from fccType field at chunk data offset 8.
			// strh chunk: 'strh'(4) + size(4) + data
			//   data[0:4] = fccType (vids or auds)
			//   data[32:36] = dwLength (data offset 32 from data start)
			//   data[36:40] = dwSuggestedBufferSize
			// So from chunk start: dwLength at pos+8+32 = pos+40,
			// dwSuggestedBufferSize at pos+8+36 = pos+44.
			dataOffset := pos + 8
			if dataOffset+8 > int64(len(buf)) {
				return
			}
			fccType := binary.LittleEndian.Uint32(buf[dataOffset : dataOffset+4])

			// Check if we have enough data for dwLength (at data+32) and dwBufSz (at data+36).
			// The strh chunk data is at least aviStreamHeaderSize=64 bytes.
			if ckSize >= aviStreamHeaderSize {
				switch fccType {
				case fccvids:
					offs.videoLength = pos + 40 // dataOffset + 32
					offs.videoBufSz = pos + 44  // dataOffset + 36
				case fccauds:
					offs.audioLength = pos + 40 // dataOffset + 32
					offs.audioBufSz = pos + 44  // dataOffset + 36
				}
			}
		}

		pos += 8 + align2(ckSize)
		if pos > end {
			break
		}
	}
}

// copyBytes copies data to w using the provided buffer (streaming pattern).
func copyBytes(w io.Writer, data []byte, buf []byte) (int, error) {
	remaining := data
	total := 0
	for len(remaining) > 0 {
		n := len(remaining)
		if n > len(buf) {
			n = len(buf)
		}
		copy(buf, remaining[:n])
		written, err := w.Write(buf[:n])
		total += written
		if err != nil {
			return total, err
		}
		remaining = remaining[n:]
	}
	return total, nil
}

// align2 rounds n up to the nearest even number (AVI word alignment).
func align2(n int64) int64 {
	if n%2 == 0 {
		return n
	}
	return n + 1
}
