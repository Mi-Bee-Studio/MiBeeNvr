// Package avi implements a pure-Go AVI RIFF muxer and demuxer.
//
// AVI (Audio Video Interleaved) is a RIFF-based container format.
// This package supports MJPEG video (00dc chunks) + G.711 mu-law audio (01wb chunks)
// with proper hdrl (avih + strl/strh+strf for both streams), movi LIST with
// interleaved chunks, and idx1 index.
package avi

import (
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
	// Pre-computed list sizes (no backpatching needed).
	// strh(8+56) + strf(8+40) + fccstrl(4) = 116.
	videoStrlDataSize = 4 + 8 + aviStreamHeaderSize + 8 + bitmapInfoHeaderSize

	// strh(8+56) + strf(8+18) + fccstrl(4) = 94.
	audioStrlDataSize = 4 + 8 + aviStreamHeaderSize + 8 + waveformatexSize

	// fcchdrl(4) + avih(8+56) + videoStrl(8+116) + audioStrl(8+94) = 294.
	hdrlDataSize = 4 + 8 + aviMainHeaderSize + 8 + videoStrlDataSize + 8 + audioStrlDataSize
)

// indexEntry holds information for one idx1 index entry.
type indexEntry struct {
	ckID   uint32
	flags  uint32
	offset uint32
	length uint32
}

// Muxer writes AVI RIFF files with MJPEG video and G.711 audio streams.
//
// The muxer buffers all data in memory. On Close(), it backpatches all
// size fields, writes the idx1 index, and flushes to the underlying writer.
type Muxer struct {
	w io.Writer

	width      int
	height     int
	sampleRate int
	muLaw      bool

	buf        bytes.Buffer
	entries    []indexEntry
	moviStart  int // byte offset in buf where movi data begins
	closed     bool

	// Positions in buf that need backpatching.
	posRIFFSize       int
	posTotalFrames    int
	posMaxBytesPerSec int
	posVideoLength    int
	posAudioLength    int
	posAudioBufSize   int
	posMoviListSize   int

	videoFrames  int
	audioBytes   int
	maxFrameSize int
}

// NewMuxer creates a new AVI muxer.
//
// Parameters:
//   - w: destination writer (data is flushed on Close())
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
	}
	m.writeHeader()
	return m
}

// WriteVideo writes a single video frame as a 00dc chunk.
func (m *Muxer) WriteVideo(frame []byte, ptsMicroseconds int64) error {
	if m.closed {
		return errors.New("avi: muxer is closed")
	}

	chunkDataLen := len(frame)

	// Record index entry.
	m.entries = append(m.entries, indexEntry{
		ckID:   fcc00dc,
		flags:  aviifKeyFrame,
		offset: uint32(m.buf.Len() - m.moviStart),
		length: uint32(chunkDataLen),
	})

	// Write 00dc chunk.
	writeU32(&m.buf, fcc00dc)
	writeU32(&m.buf, uint32(chunkDataLen))
	m.buf.Write(frame)
	if chunkDataLen%2 == 1 {
		m.buf.WriteByte(0) // pad to even boundary
	}

	m.videoFrames++
	if chunkDataLen > m.maxFrameSize {
		m.maxFrameSize = chunkDataLen
	}

	_ = ptsMicroseconds // unused; PTS is recomputed by Demuxer from position

	return nil
}

// WriteAudio writes G.711 audio data as a 01wb chunk.
func (m *Muxer) WriteAudio(data []byte, ptsMicroseconds int64) error {
	if m.closed {
		return errors.New("avi: muxer is closed")
	}

	chunkDataLen := len(data)

	// Record index entry.
	m.entries = append(m.entries, indexEntry{
		ckID:   fcc01wb,
		flags:  0,
		offset: uint32(m.buf.Len() - m.moviStart),
		length: uint32(chunkDataLen),
	})

	// Write 01wb chunk.
	writeU32(&m.buf, fcc01wb)
	writeU32(&m.buf, uint32(chunkDataLen))
	m.buf.Write(data)
	if chunkDataLen%2 == 1 {
		m.buf.WriteByte(0) // pad to even boundary
	}

	m.audioBytes += chunkDataLen

	_ = ptsMicroseconds // unused; PTS is recomputed by Demuxer from position

	return nil
}

// Close finalizes the AVI file.
//
// Backpatches all size fields, writes idx1 index, and flushes to the
// underlying io.Writer. After Close(), the muxer must not be used.
func (m *Muxer) Close() error {
	if m.closed {
		return errors.New("avi: muxer already closed")
	}
	m.closed = true

	if m.w == nil {
		return errors.New("avi: nil writer")
	}

	// Backpatch avih.dwTotalFrames.
	binary.LittleEndian.PutUint32(m.buf.Bytes()[m.posTotalFrames:], uint32(m.videoFrames))

	// Backpatch avih.dwMaxBytesPerSec.
	maxBPS := uint32(0)
	if m.videoFrames > 0 {
		totalDurUs := uint64(m.videoFrames) * uint64(defaultMicroSecPerFrame)
		if totalDurUs > 0 {
			totalBytes := uint64(m.buf.Len())
			maxBPS = uint32(totalBytes * 1000000 / totalDurUs)
		}
	}
	binary.LittleEndian.PutUint32(m.buf.Bytes()[m.posMaxBytesPerSec:], maxBPS)

	// Backpatch video strh dwLength and dwSuggestedBufferSize.
	binary.LittleEndian.PutUint32(m.buf.Bytes()[m.posVideoLength:], uint32(m.videoFrames))
	binary.LittleEndian.PutUint32(m.buf.Bytes()[m.posVideoLength+4:], uint32(m.maxFrameSize))

	// Backpatch audio strh dwLength and dwSuggestedBufferSize.
	binary.LittleEndian.PutUint32(m.buf.Bytes()[m.posAudioLength:], uint32(m.audioBytes))
	binary.LittleEndian.PutUint32(m.buf.Bytes()[m.posAudioBufSize:], uint32(m.audioBytes))

	// Compute and backpatch movi list size.
	// moviSize = 'movi'(4) + data_chunks.
	moviTotal := m.buf.Len() - m.moviStart + 4
	binary.LittleEndian.PutUint32(m.buf.Bytes()[m.posMoviListSize:], uint32(moviTotal))

	// Write idx1 index at end of movi.
	idx1DataLen := len(m.entries) * indexEntrySize
	writeU32(&m.buf, fccidx1)
	writeU32(&m.buf, uint32(idx1DataLen))
	for _, e := range m.entries {
		writeU32(&m.buf, e.ckID)
		writeU32(&m.buf, e.flags)
		writeU32(&m.buf, e.offset)
		writeU32(&m.buf, e.length)
	}

	// Backpatch RIFF size = file size - 8.
	binary.LittleEndian.PutUint32(m.buf.Bytes()[m.posRIFFSize:], uint32(m.buf.Len()-8))

	// Flush to underlying writer.
	if _, err := io.Copy(m.w, &m.buf); err != nil {
		return fmt.Errorf("avi: flush: %w", err)
	}

	return nil
}

// writeHeader writes the complete AVI RIFF header (RIFF + hdrl) to the buffer.
// All list/RIFF sizes are pre-computed constants, so no backpatching is needed
// during header writing (avoiding stale-slice bugs from buffer reallocation).
func (m *Muxer) writeHeader() {
	b := &m.buf

	// ---- RIFF header (placeholders: final size backpatched in Close()) ----
	writeU32(b, fccRIFF)
	m.posRIFFSize = b.Len()
	writeU32(b, 0) // placeholder RIFF size (backpatched in Close())
	writeU32(b, fccAVI)

	// ---- hdrl LIST ----
	writeU32(b, fccLIST)
	writeU32(b, uint32(hdrlDataSize)) // pre-computed: includes fcchdrl + avih + videoStrl + audioStrl
	writeU32(b, fcchdrl)

	// ---- avih chunk (56 bytes) ----
	writeU32(b, fccavih)
	writeU32(b, aviMainHeaderSize)
	writeU32(b, defaultMicroSecPerFrame)                        // dwMicroSecPerFrame
	m.posMaxBytesPerSec = b.Len()
	writeU32(b, 0)                                               // dwMaxBytesPerSec (backpatched in Close())
	writeU32(b, 0)                                               // dwPaddingGranularity
	writeU32(b, avifHasIndex|avifIsInterleaved|avifTrustCKType) // dwFlags
	m.posTotalFrames = b.Len()
	writeU32(b, 0)                    // dwTotalFrames (backpatched in Close())
	writeU32(b, 0)                    // dwInitialFrames
	writeU32(b, 2)                    // dwStreams
	writeU32(b, 0)                    // dwSuggestedBufferSize
	writeU32(b, uint32(m.width))     // dwWidth
	writeU32(b, uint32(m.height))    // dwHeight
	writeU32(b, 0)                    // dwReserved[0]
	writeU32(b, 0)                    // dwReserved[1]
	writeU32(b, 0)                    // dwReserved[2]
	writeU32(b, 0)                    // dwReserved[3]

	// ---- Video strl LIST (size is pre-computed constant) ----
	writeU32(b, fccLIST)
	writeU32(b, uint32(videoStrlDataSize))
	writeU32(b, fccstrl)

	// Video strh (56 bytes)
	writeU32(b, fccstrh)
	writeU32(b, aviStreamHeaderSize)
	writeU32(b, fccvids)       // fccType
	writeU32(b, fccMJPG)       // fccHandler
	writeU32(b, 0)             // dwFlags
	writeU16(b, 0)             // wPriority
	writeU16(b, 0)             // wLanguage
	writeU32(b, 0)             // dwInitialFrames
	writeU32(b, 1)             // dwScale
	writeU32(b, 1000000)       // dwRate (1M = 1 second in microseconds)
	writeU32(b, 0)             // dwStart
	m.posVideoLength = b.Len()
	writeU32(b, 0)             // dwLength (backpatched in Close())
	writeU32(b, 0)             // dwSuggestedBufferSize (backpatched in Close())
	writeU32(b, 0xFFFFFFFF)    // dwQuality (-1 = default)
	writeU32(b, 0)             // dwSampleSize
	writeU16(b, 0)                    // rcFrame left (SHORT)
	writeU16(b, 0)                    // rcFrame top (SHORT)
	writeU16(b, uint16(m.width))      // rcFrame right (SHORT)
	writeU16(b, uint16(m.height))     // rcFrame bottom (SHORT)

	// Video strf (BITMAPINFOHEADER, 40 bytes)
	writeU32(b, fccstrf)
	writeU32(b, bitmapInfoHeaderSize)
	writeU32(b, bitmapInfoHeaderSize)  // biSize
	writeU32(b, uint32(m.width))       // biWidth
	writeU32(b, uint32(m.height))      // biHeight
	writeU16(b, 1)                     // biPlanes
	writeU16(b, 24)                    // biBitCount
	writeU32(b, fccMJPG)               // biCompression
	writeU32(b, 0)                     // biSizeImage
	writeU32(b, 0)                     // biXPelsPerMeter
	writeU32(b, 0)                     // biYPelsPerMeter
	writeU32(b, 0)                     // biClrUsed
	writeU32(b, 0)                     // biClrImportant

	// ---- Audio strl LIST (size is pre-computed constant) ----
	writeU32(b, fccLIST)
	writeU32(b, uint32(audioStrlDataSize))
	writeU32(b, fccstrl)

	// Audio strh (56 bytes)
	writeU32(b, fccstrh)
	writeU32(b, aviStreamHeaderSize)
	writeU32(b, fccauds)              // fccType
	writeU32(b, 0)                    // fccHandler
	writeU32(b, 0)                    // dwFlags
	writeU16(b, 0)                    // wPriority
	writeU16(b, 0)                    // wLanguage
	writeU32(b, 0)                    // dwInitialFrames
	writeU32(b, 1)                    // dwScale
	writeU32(b, uint32(m.sampleRate)) // dwRate
	writeU32(b, 0)                    // dwStart
	m.posAudioLength = b.Len()
	writeU32(b, 0)                    // dwLength (backpatched in Close())
	m.posAudioBufSize = b.Len()
	writeU32(b, 0)                    // dwSuggestedBufferSize (backpatched in Close())
	writeU32(b, 0xFFFFFFFF)           // dwQuality (-1 = default)
	writeU32(b, 1)                    // dwSampleSize (1 byte per sample)
	writeU16(b, 0)                    // rcFrame left (SHORT)
	writeU16(b, 0)                    // rcFrame top (SHORT)
	writeU16(b, 0)                    // rcFrame right (SHORT)
	writeU16(b, 0)                    // rcFrame bottom (SHORT)

	// Audio strf (WAVEFORMATEX, 18 bytes)
	writeU32(b, fccstrf)
	writeU32(b, waveformatexSize)
	fmtTag := uint16(0x0006) // WAVE_FORMAT_MULAW
	if !m.muLaw {
		fmtTag = 0x0007 // WAVE_FORMAT_ALAW
	}
	writeU16(b, fmtTag)                  // wFormatTag
	writeU16(b, 1)                       // nChannels
	writeU32(b, uint32(m.sampleRate))    // nSamplesPerSec
	writeU32(b, uint32(m.sampleRate))    // nAvgBytesPerSec
	writeU16(b, 1)                       // nBlockAlign
	writeU16(b, 8)                       // wBitsPerSample
	writeU16(b, 0)                       // cbSize

	// ---- movi LIST header (size backpatched in Close()) ----
	writeU32(b, fccLIST)
	m.posMoviListSize = b.Len()
	writeU32(b, 0) // placeholder movi list size
	writeU32(b, fccmovi)
	m.moviStart = b.Len() // data chunks start here
}

// --- binary write helpers ---

func writeU32(b *bytes.Buffer, v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	b.Write(buf[:])
}

func writeU16(b *bytes.Buffer, v uint16) {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], v)
	b.Write(buf[:])
}
