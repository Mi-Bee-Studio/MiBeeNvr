package merge

// Writer helpers used while streaming merge output: byte/count/limited writers
// that let the layout pass measure box sizes without touching disk. Split out
// of mp4merge.go.

import (
	"fmt"
	"io"
)

// --- Helper types ---

// bytesWriter implements io.WriteSeeker backed by a byte buffer.
// Used to pre-calculate moov box size.
type bytesWriter struct {
	data []byte
	pos  int64
}

func (b *bytesWriter) Write(p []byte) (int, error) {
	if b.pos+int64(len(p)) > int64(len(b.data)) {
		grow := b.pos + int64(len(p)) - int64(len(b.data))
		b.data = append(b.data, make([]byte, grow)...)
	}
	copy(b.data[b.pos:], p)
	b.pos += int64(len(p))
	return len(p), nil
}

func (b *bytesWriter) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		b.pos = offset
	case 1:
		b.pos += offset
	case 2:
		b.pos = int64(len(b.data)) + offset
	}
	if b.pos < 0 {
		b.pos = 0
	}
	return b.pos, nil
}

// countWriter is a no-retention io.WriteSeeker for measuring box output
// (#1011 debt 3): it mirrors bytesWriter's position semantics — including
// SeekEnd anchored at the high-water mark, which for append-only writes is
// exactly the length bytesWriter would have grown to — while discarding the
// payload bytes. Used by the moov-size estimation pass, which used to buffer
// the entire placeholder moov (a retention of the same order as the moov
// itself).
type countWriter struct {
	pos  int64
	high int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	c.pos += int64(len(p))
	if c.pos > c.high {
		c.high = c.pos
	}
	return len(p), nil
}

func (c *countWriter) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		c.pos = offset
	case io.SeekCurrent:
		c.pos += offset
	case io.SeekEnd:
		c.pos = c.high + offset
	default:
		return c.pos, fmt.Errorf("countWriter: invalid whence %d", whence)
	}
	if c.pos < 0 {
		c.pos = 0
	}
	return c.pos, nil
}

// size reports the number of bytes a bytesWriter-backed run would have
// produced (the high-water mark of write ends).
func (c *countWriter) size() int64 { return c.high }

// limitedWriter wraps an io.WriteSeeker and limits the total bytes written.
// Used to write moov box in-place without overflowing into mdat.
// It tracks the actual file position so seeks are properly accounted for.
type limitedWriter struct {
	w         io.WriteSeeker
	remaining int64
	pos       int64 // tracks actual file position
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.w.Write(p)
	l.remaining -= int64(n)
	l.pos += int64(n)
	return n, err
}

// Seek delegates to the underlying writer.
// Adjusts remaining based on position changes to prevent overflow.
func (l *limitedWriter) Seek(offset int64, whence int) (int64, error) {
	newPos, err := l.w.Seek(offset, whence)
	if err != nil {
		return newPos, err
	}
	// Adjust remaining: forward seek reduces, backward seek increases.
	delta := newPos - l.pos
	l.remaining -= delta
	l.pos = newPos
	return newPos, nil
}
