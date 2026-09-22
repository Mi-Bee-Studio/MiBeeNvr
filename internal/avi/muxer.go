// Package avi implements a pure-Go AVI RIFF muxer and demuxer.
//
// AVI (Audio Video Interleave) is a RIFF-based container format.
// This package supports MJPEG video (00dc chunks) + G.711 mu-law audio (01wb chunks)
// with proper hdrl (avih + strl/strh+strf for both streams), movi LIST with
// interleaved chunks, and idx1 index.
package avi

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// FOURCC constants for AVI RIFF structure.
const (
	fccRIFF = 0x46464952 // 'RIFF' little-endian
	fccAVI  = 0x20495641 // 'AVI ' little-endian
	fccLIST = 0x5453494C // 'LIST' little-endian

	fcchdrl = 0x6C726468 // 'hdrl' little-endian
	fccavih = 0x68697661 // 'avih' little-endian
	fccstrl = 0x6C727473 // 'strl' little-endian
	fccstrh = 0x68727473 // 'strh' little-endian
	fccstrf = 0x66727473 // 'strf' little-endian
	fccmovi = 0x69766F6D // 'movi' little-endian
	fccidx1 = 0x31786469 // 'idx1' little-endian
	fcc00dc = 0x63643030 // '00dc' little-endian (stream 0, compressed video)
	fcc01wb = 0x62773130 // '01wb' little-endian (stream 1, audio data)
	fccMJPG = 0x47504A4D // 'MJPG' little-endian (Motion JPEG)
	fccvids = 0x73646976 // 'vids' little-endian (video stream type)
	fccauds = 0x73647561 // 'auds' little-endian (audio stream type)
)

// AVI flags.
const (
	avifHasIndex      = 0x00000010
	avifIsInterleaved = 0x00000100
	avifTrustCKType   = 0x00000800
	aviifKeyFrame     = 0x00000010
)

// Default frame interval in microseconds (approximately 30 fps).
const defaultMicroSecPerFrame = 33333

// Size constants for AVI structures.
const (
	aviMainHeaderSize    = 56
	aviStreamHeaderSize  = 56
	bitmapInfoHeaderSize = 40
	waveformatexSize     = 18
	indexEntrySize       = 16

	// Pre-computed list sizes (no backpatching needed).
	// strh(8+56) + strf(8+40) + fccstrl(4) = 116.
	videoStrlDataSize = 4 + 8 + aviStreamHeaderSize + 8 + bitmapInfoHeaderSize

	// strh(8+56) + strf(8+18) + fccstrl(4) = 94.
	audioStrlDataSize = 4 + 8 + aviStreamHeaderSize + 8 + waveformatexSize

	// fcchdrl(4) + avih(8+56) + videoStrl(8+116) + audioStrl(8+94) = 294.
	hdrlDataSize = 4 + 8 + aviMainHeaderSize + 8 + videoStrlDataSize + 8 + audioStrlDataSize

	// fcchdrl(4) + avih(8+56) + videoStrl(8+116) = 192.
	videoOnlyHdrlDataSize = 4 + 8 + aviMainHeaderSize + 8 + videoStrlDataSize
)

// incrementalBufSize is the write-coalescing buffer for incremental mode —
// matches the 256KB coalescing buffer the storage layer uses for file-form
// segments (internal/storage/segment_writer.go), so AVI segments get the same
// syscall amortization as MP4 segments (#761: per-frame writes must not become
// per-frame write(2) calls).
const incrementalBufSize = 256 << 10

// indexEntry holds information for one idx1 index entry.
type indexEntry struct {
	ckID   uint32
	flags  uint32
	offset uint32
	length uint32
}

// Muxer writes AVI RIFF files with MJPEG video and G.711 audio streams in one
// of two modes, selected by the destination writer:
//
//   - Incremental (w is an io.WriterAt, e.g. *os.File): headers and chunks
//     stream through a 256KB coalescing buffer straight to the destination;
//     only the 16-byte-per-chunk idx1 entries stay in RAM. Close() flushes,
//     appends the idx1, and backpatches the header size fields with positional
//     WriteAt calls. RAM use is O(frames), not O(file) — segment duration is
//     no longer bounded by memory (#761).
//
//   - Buffered (w is a plain io.Writer): the whole file accumulates in RAM and
//     is flushed once on Close() (legacy behavior for in-memory callers).
//
// Both modes produce byte-identical output (guarded by
// TestMuxerIncrementalMatchesBuffered*).
type Muxer struct {
	w  io.Writer
	wa io.WriterAt // non-nil → incremental mode
	bw *bufio.Writer

	width      int
	height     int
	sampleRate int
	muLaw      bool
	hasAudio   bool

	buf       bytes.Buffer // buffered mode only
	off       int64        // absolute stream offset (both modes)
	entries   []indexEntry
	movistart int64
	closed    bool
	err       error

	// Absolute stream offsets that need backpatching in Close().
	posRIFFSize       int64
	posTotalFrames    int64
	posMaxBytesPerSec int64
	posVideoLength    int64
	posAudioLength    int64
	posAudioBufSize   int64
	posMoviListSize   int64

	videoFrames  int
	audioBytes   int
	maxFrameSize int
}

// NewMuxer creates a new AVI muxer.
//
// Parameters:
//   - w: destination writer. If it implements io.WriterAt (e.g. *os.File),
//     the muxer streams incrementally; otherwise it buffers in RAM and
//     flushes on Close().
//   - width, height: video frame dimensions
//   - sampleRate: audio sample rate (e.g., 8000 for G.711)
//   - muLaw: true for mu-law, false for A-law
func NewMuxer(w io.Writer, width, height, sampleRate int, muLaw bool) *Muxer {
	m := &Muxer{
		w:          w,
		width:      width,
		height:     height,
		sampleRate: sampleRate,
		muLaw:      muLaw,
		hasAudio:   true,
	}
	m.initMode()
	m.writeHeader()
	return m
}

// NewVideoOnlyMuxer creates a new AVI muxer with video only (no audio stream).
//
// Parameters:
//   - w: destination writer (see NewMuxer for the incremental/buffered modes)
//   - width, height: video frame dimensions
func NewVideoOnlyMuxer(w io.Writer, width, height int) *Muxer {
	m := &Muxer{
		w:      w,
		width:  width,
		height: height,
	}
	m.initMode()
	m.writeHeader()
	return m
}

// initMode selects incremental vs buffered mode from the writer's capabilities.
func (m *Muxer) initMode() {
	if wa, ok := m.w.(io.WriterAt); ok {
		m.wa = wa
		m.bw = bufio.NewWriterSize(m.w, incrementalBufSize)
	}
}

// TotalBytes reports the bytes written to the stream so far (the eventual AVI
// file size, minus the idx1 tail). Callers use it to rotate a segment before
// the RIFF uint32 size ceiling. Only meaningful while the muxer is open.
func (m *Muxer) TotalBytes() int64 { return m.off }

// WriteVideo writes a single video frame as a 00dc chunk.
func (m *Muxer) WriteVideo(frame []byte, ptsMicroseconds int64) error {
	if m.closed {
		return errors.New("avi: muxer is closed")
	}
	if m.err != nil {
		return m.err
	}

	chunkDataLen := len(frame)

	// Record index entry.
	m.entries = append(m.entries, indexEntry{
		ckID:   fcc00dc,
		flags:  aviifKeyFrame,
		offset: uint32(m.off - m.movistart),
		length: uint32(chunkDataLen),
	})

	m.put32(fcc00dc)
	m.put32(uint32(chunkDataLen))
	m.write(frame)
	if chunkDataLen%2 == 1 {
		m.write([]byte{0}) // pad to even boundary
	}

	m.videoFrames++
	if chunkDataLen > m.maxFrameSize {
		m.maxFrameSize = chunkDataLen
	}

	_ = ptsMicroseconds // unused; PTS is recomputed by Demuxer from position

	return m.err
}

// WriteAudio writes G.711 audio data as a 01wb chunk.
func (m *Muxer) WriteAudio(data []byte, ptsMicroseconds int64) error {
	if m.closed {
		return errors.New("avi: muxer is closed")
	}
	if m.err != nil {
		return m.err
	}

	chunkDataLen := len(data)

	// Record index entry.
	m.entries = append(m.entries, indexEntry{
		ckID:   fcc01wb,
		flags:  0,
		offset: uint32(m.off - m.movistart),
		length: uint32(chunkDataLen),
	})

	m.put32(fcc01wb)
	m.put32(uint32(chunkDataLen))
	m.write(data)
	if chunkDataLen%2 == 1 {
		m.write([]byte{0}) // pad to even boundary
	}

	m.audioBytes += chunkDataLen

	_ = ptsMicroseconds // unused; PTS is recomputed by Demuxer from position

	return m.err
}

// Close finalizes the AVI file.
//
// Backpatches all size fields, writes the idx1 index, and (buffered mode)
// flushes to the underlying io.Writer. After Close(), the muxer must not be
// used.
func (m *Muxer) Close() error {
	if m.closed {
		return errors.New("avi: muxer already closed")
	}
	m.closed = true

	if m.w == nil {
		return errors.New("avi: nil writer")
	}
	if m.err != nil {
		return m.err
	}

	// Shared size computations (identical values in both modes — the
	// byte-equality tests pin this).
	maxBPS := uint32(0)
	if m.videoFrames > 0 {
		totalDurUs := uint64(m.videoFrames) * uint64(defaultMicroSecPerFrame)
		if totalDurUs > 0 {
			maxBPS = uint32(uint64(m.off) * 1000000 / totalDurUs)
		}
	}
	// moviSize = 'movi'(4) + data_chunks.
	moviTTotal := m.off - m.movistart + 4
	idx1DataLen := int64(len(m.entries) * indexEntrySize)
	riffSize := m.off + 8 + idx1DataLen - 8

	if m.wa != nil {
		// Incremental: append idx1 through the coalescing writer, flush, then
		// patch the header fields positionally. Every byte before this point
		// went through bw in order, so logical offsets == file offsets.
		m.put32(fccidx1)
		m.put32(uint32(idx1DataLen))
		for _, e := range m.entries {
			m.put32(e.ckID)
			m.put32(e.flags)
			m.put32(e.offset)
			m.put32(e.length)
		}
		if m.err != nil {
			return m.err
		}
		if err := m.bw.Flush(); err != nil {
			return fmt.Errorf("avi: flush: %w", err)
		}
		patch := func(pos int64, v uint32) {
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], v)
			if _, err := m.wa.WriteAt(b[:], pos); err != nil && m.err == nil {
				m.err = err
			}
		}
		patch(m.posTotalFrames, uint32(m.videoFrames))
		patch(m.posMaxBytesPerSec, maxBPS)
		patch(m.posVideoLength, uint32(m.videoFrames))
		patch(m.posVideoLength+4, uint32(m.maxFrameSize))
		if m.hasAudio {
			patch(m.posAudioLength, uint32(m.audioBytes))
			patch(m.posAudioBufSize, uint32(m.audioBytes))
		}
		patch(m.posMoviListSize, uint32(moviTTotal))
		patch(m.posRIFFSize, uint32(riffSize))
		if m.err != nil {
			return fmt.Errorf("avi: backpatch: %w", m.err)
		}
		return nil
	}

	// Buffered: patch the in-memory copy, append idx1, flush once.
	back := m.buf.Bytes()

	// Backpatch avih.dwTotalFrames.
	binary.LittleEndian.PutUint32(back[m.posTotalFrames:], uint32(m.videoFrames))

	// Backpatch avih.dwMaxBytesPerSec.
	binary.LittleEndian.PutUint32(back[m.posMaxBytesPerSec:], maxBPS)

	// Backpatch video strh dwLength and dwSuggestedBufferSize.
	binary.LittleEndian.PutUint32(back[m.posVideoLength:], uint32(m.videoFrames))
	binary.LittleEndian.PutUint32(back[m.posVideoLength+4:], uint32(m.maxFrameSize))

	if m.hasAudio {
		// Backpatch audio strh dwLength and dwSuggestedBufferSize.
		binary.LittleEndian.PutUint32(back[m.posAudioLength:], uint32(m.audioBytes))
		binary.LittleEndian.PutUint32(back[m.posAudioBufSize:], uint32(m.audioBytes))
	}

	// Backpatch movi list size.
	binary.LittleEndian.PutUint32(back[m.posMoviListSize:], uint32(moviTTotal))

	// Write idx1 index at end of movi.
	m.put32(fccidx1)
	m.put32(uint32(idx1DataLen))
	for _, e := range m.entries {
		m.put32(e.ckID)
		m.put32(e.flags)
		m.put32(e.offset)
		m.put32(e.length)
	}

	// Backpatch RIFF size = file size - 8.
	binary.LittleEndian.PutUint32(back[m.posRIFFSize:], uint32(m.buf.Len()-8))

	// Flush to underlying writer.
	if _, err := io.Copy(m.w, &m.buf); err != nil {
		return fmt.Errorf("avi: flush: %w", err)
	}

	return nil
}

// write emits bytes to the active sink (buffered mode: RAM buffer; incremental
// mode: coalescing writer). The first write error sticks in m.err and later
// writes become no-ops; WriteVideo/WriteAudio/Close surface it.
func (m *Muxer) write(p []byte) {
	if m.err != nil {
		return
	}
	if m.bw != nil {
		if _, err := m.bw.Write(p); err != nil {
			m.err = err
			return
		}
	} else {
		m.buf.Write(p)
	}
	m.off += int64(len(p))
}

func (m *Muxer) put32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	m.write(b[:])
}

func (m *Muxer) put16(v uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	m.write(b[:])
}

// writeHeader writes the complete AVI RIFF header (RIFF + hdrl) to the sink.
// All list/RIFF sizes are pre-computed constants, so no backpatching is needed
// during header writing (avoiding stale-slice bugs from buffer reallocation).
func (m *Muxer) writeHeader() {
	// ---- RIFF header (placeholders: final size backpatched in Close()) ----
	m.put32(fccRIFF)
	m.posRIFFSize = m.off
	m.put32(0) // placeholder RIFF size (backpatched in Close())
	m.put32(fccAVI)

	// ---- hdrl LIST ----
	m.put32(fccLIST)
	hdSz := uint32(hdrlDataSize)
	if !m.hasAudio {
		hdSz = uint32(videoOnlyHdrlDataSize)
	}
	m.put32(hdSz) // pre-computed: includes fcchdrl + avih + videoStrl [+ audioStrl]
	m.put32(fcchdrl)

	// ---- avih chunk (56 bytes) ----
	m.put32(fccavih)
	m.put32(aviMainHeaderSize)
	m.put32(defaultMicroSecPerFrame) // dwMicroSecPerFrame
	m.posMaxBytesPerSec = m.off
	m.put32(0)                                                  // dwMaxBytesPerSec (backpatched in Close())
	m.put32(0)                                                  // dwPaddingGranularity
	m.put32(avifHasIndex | avifIsInterleaved | avifTrustCKType) // dwFlags
	m.posTotalFrames = m.off
	m.put32(0) // dwTotalFrames (backpatched in Close())
	m.put32(0) // dwInitialFrames
	strms := uint32(2)
	if !m.hasAudio {
		strms = 1
	}
	m.put32(strms)            // dwStreams
	m.put32(0)                // dwSuggestedBufferSize
	m.put32(uint32(m.width))  // dwWidth
	m.put32(uint32(m.height)) // dwHeight
	m.put32(0)                // dwReserved[0]
	m.put32(0)                // dwReserved[1]
	m.put32(0)                // dwReserved[2]
	m.put32(0)                // dwReserved[3]

	// ---- Video strl LIST (size is pre-computed constant) ----
	m.put32(fccLIST)
	m.put32(uint32(videoStrlDataSize))
	m.put32(fccstrl)

	// Video strh (56 bytes)
	m.put32(fccstrh)
	m.put32(aviStreamHeaderSize)
	m.put32(fccvids) // fccType
	m.put32(fccMJPG) // fccHandler
	m.put32(0)       // dwFlags
	m.put16(0)       // wPriority
	m.put16(0)       // wLanguage
	m.put32(0)       // dwInitialFrames
	m.put32(1)       // dwScale
	m.put32(1000000) // dwRate (1M = 1 second in microseconds)
	m.put32(0)       // dwStart
	m.posVideoLength = m.off
	m.put32(0)                // dwLength (backpatched in Close())
	m.put32(0)                // dwSuggestedBufferSize (backpatched in Close())
	m.put32(0xFFFFFFFF)       // dwQuality (-1 = default)
	m.put32(0)                // dwSampleSize
	m.put16(0)                // rcFrame left (SHORT)
	m.put16(0)                // rcFrame top (SHORT)
	m.put16(uint16(m.width))  // rcFrame right (SHORT)
	m.put16(uint16(m.height)) // rcFrame bottom (SHORT)

	// Video strf (BITMAPINFOHEADER, 40 bytes)
	m.put32(fccstrf)
	m.put32(bitmapInfoHeaderSize)
	m.put32(bitmapInfoHeaderSize) // biSize
	m.put32(uint32(m.width))      // biWidth
	m.put32(uint32(m.height))     // biHeight
	m.put16(1)                    // biPlanes
	m.put16(24)                   // biBitCount
	m.put32(fccMJPG)              // biCompression
	m.put32(0)                    // biSizeImage
	m.put32(0)                    // biXPelsPerMeter
	m.put32(0)                    // biYPelsPerMeter
	m.put32(0)                    // biClrUsed
	m.put32(0)                    // biClrImportant

	if m.hasAudio {
		// ---- Audio strl LIST (size is pre-computed constant) ----
		m.put32(fccLIST)
		m.put32(uint32(audioStrlDataSize))
		m.put32(fccstrl)

		// Audio strh (56 bytes)
		m.put32(fccstrh)
		m.put32(aviStreamHeaderSize)
		m.put32(fccauds)              // fccType
		m.put32(0)                    // fccHandler
		m.put32(0)                    // dwFlags
		m.put16(0)                    // wPriority
		m.put16(0)                    // wLanguage
		m.put32(0)                    // dwInitialFrames
		m.put32(1)                    // dwScale
		m.put32(uint32(m.sampleRate)) // dwRate
		m.put32(0)                    // dwStart
		m.posAudioLength = m.off
		m.put32(0) // dwLength (backpatched in Close())
		m.posAudioBufSize = m.off
		m.put32(0)          // dwSuggestedBufferSize (backpatched in Close())
		m.put32(0xFFFFFFFF) // dwQuality (-1 = default)
		m.put32(1)          // dwSampleSize (1 byte per sample)
		m.put16(0)          // rcFrame left (SHORT)
		m.put16(0)          // rcFrame top (SHORT)
		m.put16(0)          // rcFrame right (SHORT)
		m.put16(0)          // rcFrame bottom (SHORT)

		// Audio strf (WAVEFORMATEX, 18 bytes)
		m.put32(fccstrf)
		m.put32(waveformatexSize)
		fmtTag := uint16(0x0006) // WAVE_FORMAT_MULAW
		if !m.muLaw {
			fmtTag = 0x0007 // WAVE_FORMAT_ALAW
		}
		m.put16(fmtTag)               // wFormatTag
		m.put16(1)                    // nChannels
		m.put32(uint32(m.sampleRate)) // nSamplesPerSec
		m.put32(uint32(m.sampleRate)) // nAvgBytesPerSec
		m.put16(1)                    // nBlockAlign
		m.put16(8)                    // wBitsPerSample
		m.put16(0)                    // cbSize
	}

	// ---- movi LIST header (size backpatched in Close()) ----
	m.put32(fccLIST)
	m.posMoviListSize = m.off
	m.put32(0) // placeholder movi list size
	m.put32(fccmovi)
	m.movistart = m.off // data chunks start here
}
