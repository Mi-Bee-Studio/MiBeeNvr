package avi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ChunkType identifies whether a chunk is video or audio.
type ChunkType uint8

const (
	// ChunkVideo indicates a compressed video frame (00dc).
	ChunkVideo ChunkType = 0x01
	// ChunkAudio indicates an audio data chunk (01wb).
	ChunkAudio ChunkType = 0x02
)

// Chunk represents a single data chunk from the AVI movi list.
type Chunk struct {
	Type ChunkType
	PTS  int64 // timestamp in microseconds
	Data []byte
}

// Demuxer reads AVI RIFF files and yields video/audio chunks via NextChunk().
//
// It parses the RIFF header, hdrl list (avih + strl headers) to determine
// stream parameters, then reads chunks sequentially from the movi list.
// PTS timestamps are computed from the stream timing parameters and
// chunk position within the stream.
type Demuxer struct {
	r io.ReadSeeker

	// Stream parameters parsed from headers.
	dwMicroSecPerFrame uint32 // microseconds per video frame
	audioRate          uint32 // audio samples per second
	audioScale         uint32 // audio time scale

	// movi reading state.
	moviDataStart int64 // absolute offset in r where movi data starts
	atMovi        bool  // true once we've entered the movi list
	atEnd         bool  // true once we've exhausted movi

	// Per-stream sample counters for PTS calculation.
	videoSampleIdx int
	audioSampleIdx int

	// Whether the file has been parsed.
	parsed bool
}

// NewDemuxer creates a new AVI demuxer from a ReadSeeker.
// It parses the RIFF and hdrl headers immediately.
func NewDemuxer(r io.ReadSeeker) (*Demuxer, error) {
	d := &Demuxer{r: r}
	if err := d.parseHeaders(); err != nil {
		return nil, err
	}
	return d, nil
}

// NextChunk reads and returns the next data chunk from the AVI file.
// It returns io.EOF when no more chunks are available.
//
// The chunk's PTS field is computed from the stream timing:
//   - Video PTS = frameIndex * dwMicroSecPerFrame
//   - Audio PTS = sampleIndex * (1,000,000 * dwScale / dwRate)
func (d *Demuxer) NextChunk() (*Chunk, error) {
	if !d.atMovi {
		if err := d.enterMovi(); err != nil {
			return nil, err
		}
	}

	if d.atEnd {
		return nil, io.EOF
	}

	for {
		// Read chunk fourcc and size.
		var header [8]byte
		_, err := io.ReadFull(d.r, header[:])
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				d.atEnd = true
				return nil, io.EOF
			}
			return nil, fmt.Errorf("avi: read chunk header: %w", err)
		}

		ckID := binary.LittleEndian.Uint32(header[0:4])
		ckSize := binary.LittleEndian.Uint32(header[4:8])

		// Check for end of movi (LIST or idx1 at top level is not expected here
		// since we're reading inside movi; but idx1 follows movi in the RIFF).
		// If we encounter a non-data chunk, we've likely hit the end.
		switch ckID {
		case fccLIST:
			// Could be a 'rec ' list. Read list type and skip.
			var listType [4]byte
			if _, err := io.ReadFull(d.r, listType[:]); err != nil {
				return nil, fmt.Errorf("avi: read list type: %w", err)
			}
			// If it's 'rec ', skip the entire list recursively.
			// Subtract 4 from ckSize since we already read listType.
			remaining := int64(ckSize) - 4
			if _, err := d.r.Seek(remaining, io.SeekCurrent); err != nil {
				return nil, fmt.Errorf("avi: seek past rec list: %w", err)
			}
			continue

		case fccidx1:
			// We've reached idx1, which means movi is done.
			d.atEnd = true
			return nil, io.EOF

		default:
			// Check if this is a known data chunk.
			chunkType, streamID := classifyChunk(ckID)
			if chunkType == 0 {
				// Unknown chunk, skip it.
				skipSize := int64(ckSize)
				if skipSize%2 == 1 {
					skipSize++
				}
				if _, err := d.r.Seek(skipSize, io.SeekCurrent); err != nil {
					return nil, fmt.Errorf("avi: skip unknown chunk: %w", err)
				}
				continue
			}

			// Read the chunk data.
			data := make([]byte, ckSize)
			if _, err := io.ReadFull(d.r, data); err != nil {
				return nil, fmt.Errorf("avi: read chunk data: %w", err)
			}

			// Skip padding byte if present.
			if ckSize%2 == 1 {
				if _, err := d.r.Seek(1, io.SeekCurrent); err != nil {
					return nil, fmt.Errorf("avi: skip padding: %w", err)
				}
			}

			// Compute PTS.
			var pts int64
			if chunkType == ChunkVideo {
				pts = int64(d.videoSampleIdx) * int64(d.dwMicroSecPerFrame)
				d.videoSampleIdx++
			} else {
				// Audio PTS: each sample is dwScale/dwRate seconds.
				// With dwScale=1 and dwRate=sampleRate, each sample is 1/sampleRate seconds.
				// We compute this per-audio-chunk based on sample count.
				sampleCount := int64(len(data))
				if d.audioRate > 0 {
					pts = int64(d.audioSampleIdx) * 1000000 * int64(d.audioScale) / int64(d.audioRate)
				}
				d.audioSampleIdx += int(sampleCount)
			}

			_ = streamID // unused in current implementation

			return &Chunk{
				Type: chunkType,
				PTS:  pts,
				Data: data,
			}, nil
		}
	}
}

// parseHeaders reads and validates the RIFF header and hdrl list.
func (d *Demuxer) parseHeaders() error {
	// Read RIFF header.
	var riffHeader [12]byte
	if _, err := io.ReadFull(d.r, riffHeader[:]); err != nil {
		return fmt.Errorf("avi: read RIFF header: %w", err)
	}

	riffID := binary.LittleEndian.Uint32(riffHeader[0:4])
	if riffID != fccRIFF {
		return fmt.Errorf("avi: not a RIFF file (got 0x%08X)", riffID)
	}

	aviID := binary.LittleEndian.Uint32(riffHeader[8:12])
	if aviID != fccAVI {
		return fmt.Errorf("avi: not an AVI file (got 0x%08X)", aviID)
	}

	// Parse the hdrl list and find avih + strl headers.
	if err := d.parseHdrl(); err != nil {
		return err
	}

	d.parsed = true
	return nil
}

// parseHdrl reads the hdrl LIST and extracts stream parameters.
func (d *Demuxer) parseHdrl() error {
	for {
		var header [8]byte
		_, err := io.ReadFull(d.r, header[:])
		if err != nil {
			return fmt.Errorf("avi: read hdrl list header: %w", err)
		}

		ckID := binary.LittleEndian.Uint32(header[0:4])
		ckSize := binary.LittleEndian.Uint32(header[4:8])

		switch ckID {
		case fccLIST:
			// Read list type.
			var listType [4]byte
			if _, err := io.ReadFull(d.r, listType[:]); err != nil {
				return fmt.Errorf("avi: read list type: %w", err)
			}
			listTypeID := binary.LittleEndian.Uint32(listType[:])

			switch listTypeID {
			case fcchdrl:
				// Enter hdrl and parse its children.
				if err := d.parseHdrlChildren(ckSize - 4); err != nil {
					return err
				}
				return nil

			default:
				// Skip unknown list. Subtract 4 for listType already read.
				remaining := int64(ckSize) - 4
				if remaining < 0 {
					remaining = 0
				}
				if remaining%2 == 1 {
					remaining++
				}
				if _, err := d.r.Seek(remaining, io.SeekCurrent); err != nil {
					return fmt.Errorf("avi: skip list: %w", err)
				}
			}

		case fccidx1:
			// idx1 before hdrl? Skip it.
			skipSize := int64(ckSize)
			if skipSize%2 == 1 {
				skipSize++
			}
			if _, err := d.r.Seek(skipSize, io.SeekCurrent); err != nil {
				return fmt.Errorf("avi: skip idx1: %w", err)
			}

		default:
			// Unknown chunk, skip.
			skipSize := int64(ckSize)
			if skipSize%2 == 1 {
				skipSize++
			}
			if _, err := d.r.Seek(skipSize, io.SeekCurrent); err != nil {
				return fmt.Errorf("avi: skip chunk: %w", err)
			}

			// If we've been seeking through top-level items without finding hdrl,
			// something is wrong.
			return errors.New("avi: hdrl list not found")
		}
	}
}

// parseHdrlChildren reads the contents of the hdrl list: avih and strl lists.

func (d *Demuxer) parseHdrlChildren(size uint32) error {
	remaining := int64(size)
	for remaining > 0 {
		var header [8]byte
		if _, err := io.ReadFull(d.r, header[:]); err != nil {
			return fmt.Errorf("avi: read hdrl child: %w", err)
		}
		remaining -= 8

		ckID := binary.LittleEndian.Uint32(header[0:4])
		ckSize := binary.LittleEndian.Uint32(header[4:8])
		switch ckID {
		case fccavih:
			// Read AVIMAINHEADER (56 bytes).
			if ckSize < aviMainHeaderSize {
				return fmt.Errorf("avi: avih too small: %d bytes", ckSize)
			}
			var avih [aviMainHeaderSize]byte
			if _, err := io.ReadFull(d.r, avih[:]); err != nil {
				return fmt.Errorf("avi: read avih: %w", err)
			}
			remaining -= aviMainHeaderSize

			d.dwMicroSecPerFrame = binary.LittleEndian.Uint32(avih[0:4])

			// Skip remaining avih data if any.
			if int64(ckSize) > aviMainHeaderSize {
				skip := int64(ckSize) - aviMainHeaderSize
				if _, err := d.r.Seek(skip, io.SeekCurrent); err != nil {
					return fmt.Errorf("avi: skip avih extra: %w", err)
				}
				remaining -= skip
			}
			// Pad byte.
			if ckSize%2 == 1 {
				if _, err := d.r.Seek(1, io.SeekCurrent); err != nil {
					return fmt.Errorf("avi: skip avih pad: %w", err)
				}
				remaining--
			}

		case fccLIST:
			// Read list type.
			var listType [4]byte
			if _, err := io.ReadFull(d.r, listType[:]); err != nil {
				return fmt.Errorf("avi: read hdrl list type: %w", err)
			}
			remaining -= 4

			listTypeID := binary.LittleEndian.Uint32(listType[:])
			if listTypeID == fccstrl {
				// Parse strl list.
				if err := d.parseStrl(ckSize - 4); err != nil {
					return err
				}
				remaining -= int64(ckSize) - 4
			} else {
				// Unknown list, skip.
				skip := int64(ckSize) - 4
				if skip%2 == 1 {
					skip++
				}
				if _, err := d.r.Seek(skip, io.SeekCurrent); err != nil {
					return fmt.Errorf("avi: skip unknown strl: %w", err)
				}
				remaining -= skip
			}

		default:
			// Unknown chunk, skip.
			skip := int64(ckSize)
			if skip%2 == 1 {
				skip++
			}
			if _, err := d.r.Seek(skip, io.SeekCurrent); err != nil {
				return fmt.Errorf("avi: skip hdrl chunk: %w", err)
			}
			remaining -= skip
		}
	}
	return nil
}

// parseStrl reads a single strl list (strh + strf + optional strd/strn).
func (d *Demuxer) parseStrl(size uint32) error {
	remaining := int64(size)
	for remaining > 0 {
		var header [8]byte
		if _, err := io.ReadFull(d.r, header[:]); err != nil {
			return fmt.Errorf("avi: read strl child: %w", err)
		}
		remaining -= 8

		ckID := binary.LittleEndian.Uint32(header[0:4])
		ckSize := binary.LittleEndian.Uint32(header[4:8])

		switch ckID {
		case fccstrh:
			// Read AVISTREAMHEADER (56 bytes).
			if ckSize < aviStreamHeaderSize {
				return fmt.Errorf("avi: strh too small: %d bytes", ckSize)
			}
			var strh [aviStreamHeaderSize]byte
			if _, err := io.ReadFull(d.r, strh[:]); err != nil {
				return fmt.Errorf("avi: read strh: %w", err)
			}
			remaining -= aviStreamHeaderSize

			fccType := binary.LittleEndian.Uint32(strh[0:4])
			dwScale := binary.LittleEndian.Uint32(strh[20:24])
			dwRate := binary.LittleEndian.Uint32(strh[24:28])

			if fccType == fccauds {
				d.audioScale = dwScale
				d.audioRate = dwRate
			}

			// Skip remaining.
			if int64(ckSize) > aviStreamHeaderSize {
				skip := int64(ckSize) - aviStreamHeaderSize
				if _, err := d.r.Seek(skip, io.SeekCurrent); err != nil {
					return fmt.Errorf("avi: skip strh extra: %w", err)
				}
				remaining -= skip
			}
			// Pad byte.
			if ckSize%2 == 1 {
				if _, err := d.r.Seek(1, io.SeekCurrent); err != nil {
					return fmt.Errorf("avi: skip strh pad: %w", err)
				}
				remaining--
			}

		case fccstrf:
			// Skip stream format chunk (we already have the info we need).
			skip := int64(ckSize)
			if skip%2 == 1 {
				skip++
			}
			if _, err := d.r.Seek(skip, io.SeekCurrent); err != nil {
				return fmt.Errorf("avi: skip strf: %w", err)
			}
			remaining -= skip

		default:
			// Unknown chunk in strl, skip.
			skip := int64(ckSize)
			if skip%2 == 1 {
				skip++
			}
			if _, err := d.r.Seek(skip, io.SeekCurrent); err != nil {
				return fmt.Errorf("avi: skip strl chunk: %w", err)
			}
			remaining -= skip
		}
	}
	return nil
}

// enterMovi seeks to the start of the movi list data.
func (d *Demuxer) enterMovi() error {
	// Seek back to after the RIFF header (offset 12) and find movi.
	if _, err := d.r.Seek(12, io.SeekStart); err != nil {
		return fmt.Errorf("avi: seek to start: %w", err)
	}

	for {
		var header [8]byte
		_, err := io.ReadFull(d.r, header[:])
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				d.atMovi = true
				d.atEnd = true
				return io.EOF
			}
			return fmt.Errorf("avi: find movi: %w", err)
		}

		ckID := binary.LittleEndian.Uint32(header[0:4])
		ckSize := binary.LittleEndian.Uint32(header[4:8])

		switch ckID {
		case fccLIST:
			var listType [4]byte
			if _, err := io.ReadFull(d.r, listType[:]); err != nil {
				return fmt.Errorf("avi: read list type in enterMovi: %w", err)
			}
			listTypeID := binary.LittleEndian.Uint32(listType[:])

			if listTypeID == fccmovi {
				d.moviDataStart, err = d.r.Seek(0, io.SeekCurrent)
				if err != nil {
					return fmt.Errorf("avi: record movi position: %w", err)
				}
				d.atMovi = true
				return nil
			}

			// Skip other lists.
			remaining := int64(ckSize) - 4
			if remaining%2 == 1 {
				remaining++
			}
			if _, err := d.r.Seek(remaining, io.SeekCurrent); err != nil {
				return fmt.Errorf("avi: skip list in enterMovi: %w", err)
			}

		case fccidx1:
			// idx1 before movi? shouldn't happen but skip it.
			skip := int64(ckSize)
			if skip%2 == 1 {
				skip++
			}
			if _, err := d.r.Seek(skip, io.SeekCurrent); err != nil {
				return fmt.Errorf("avi: skip idx1 in enterMovi: %w", err)
			}

		case fccRIFF:
			// Nested RIFF? This shouldn't happen in a well-formed AVI.
			return errors.New("avi: unexpected nested RIFF")
		default:
			// Unknown top-level chunk, skip.
			skip := int64(ckSize)
			if skip%2 == 1 {
				skip++
			}
			if _, err := d.r.Seek(skip, io.SeekCurrent); err != nil {
				return fmt.Errorf("avi: skip chunk in enterMovi: %w", err)
			}
		}
	}
}

// classifyChunk returns the ChunkType and stream ID for a given FOURCC.
// Returns (0, 0) if the chunk is not a known data type.
func classifyChunk(ckID uint32) (ChunkType, int) {
	// The FOURCC encodes 'XYdc' or 'XYwb' where XY is the stream number.
	// Example: '00dc' = stream 0 compressed video, '01wb' = stream 1 audio.
	switch ckID {
	case fcc00dc:
		return ChunkVideo, 0
	case fcc01wb:
		return ChunkAudio, 1
	default:
		// Generic detection for arbitrary stream IDs.
		b := [4]byte{}
		binary.LittleEndian.PutUint32(b[:], ckID)
		// Stream number is encoded in the first two ASCII characters.
		// '00' = stream 0, '01' = stream 1, etc.
		suffix := uint16(b[2])<<8 | uint16(b[3])
		switch suffix {
		case 0x6463: // 'dc' (compressed video)
			return ChunkVideo, int(b[0]-'0')*10 + int(b[1]-'0')
		case 0x6277: // 'wb' (audio data)
			return ChunkAudio, int(b[0]-'0')*10 + int(b[1]-'0')
		case 0x6264: // 'db' (uncompressed video)
			return ChunkVideo, int(b[0]-'0')*10 + int(b[1]-'0')
		}
	}
	return 0, 0
}
