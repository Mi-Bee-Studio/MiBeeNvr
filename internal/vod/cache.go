// Package vod turns recorded MP4 files into a time-addressable HLS VOD
// stream WITHOUT materializing anything on disk: an on-demand fMP4
// fragmenter that serves per-recording init segments + keyframe-aligned
// media fragments, plus the day-level M3U8 playlist that stitches every
// recording of a camera into one seekable timeline (#321 Phase 2).
//
// Design constraints (RPi-class host):
//   - Sample tables are parsed once per recording and cached (LRU, keyed by
//     recording ID, invalidated by path/size/mtime). Fragment requests are
//     then pure box-writing + bounded file copies.
//   - The media data (mdat) is NEVER fully loaded: fragments reference the
//     source file's sample byte ranges and stream them through a 1MB buffer.
//   - Video is the timeline master. Audio is included per fragment only when
//     the codec is MSE-friendly (AAC/Opus); G.711 is dropped because Chrome's
//     MediaSource does not accept ulaw/alaw sample entries.
package vod

import (
	"container/list"
	"fmt"
	"os"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"golang.org/x/sync/singleflight"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var logger = slogx.Component("vod")

// segmentCache caches parsed sample tables per recording. Parsing a 1-hour
// recording (~90k samples + per-sample keyframe probes) costs 100–300ms; the
// day playlist needs every recording parsed once and every fragment request
// re-uses the same tables, so the cache is what makes this cheap.
type segmentCache struct {
	mu      sync.Mutex
	max     int
	ll      *list.List // front = most recently used
	entries map[string]*list.Element

	sf singleflight.Group // collapses concurrent parses of the same recording
}

type cacheItem struct {
	rec  model.Recording // snapshot for stat validation
	info *merge.SegmentInfo
}

func newSegmentCache(maxEntries int) *segmentCache {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &segmentCache{
		max:     maxEntries,
		ll:      list.New(),
		entries: make(map[string]*list.Element),
	}
}

// Get returns the parsed SegmentInfo for a recording, parsing (and caching)
// on miss. Concurrent Gets for the same recording collapse into one parse.
// The returned pointer is shared — callers MUST NOT mutate it.
func (c *segmentCache) Get(rec model.Recording) (*merge.SegmentInfo, error) {
	if rec.FilePath == "" {
		return nil, fmt.Errorf("recording %s has no file path", rec.ID)
	}

	c.mu.Lock()
	if el, ok := c.entries[rec.ID]; ok {
		item := el.Value.(*cacheItem)
		if sameFileState(item.rec, rec) {
			c.ll.MoveToFront(el)
			c.mu.Unlock()
			return item.info, nil
		}
		// File changed underneath (rolling merge replaced it) — drop the entry.
		c.ll.Remove(el)
		delete(c.entries, rec.ID)
	}
	c.mu.Unlock()

	v, err, _ := c.sf.Do(rec.ID, func() (any, error) {
		return parseAndStore(c, rec)
	})
	if err != nil {
		return nil, err
	}
	return v.(*merge.SegmentInfo), nil
}

func parseAndStore(c *segmentCache, rec model.Recording) (*merge.SegmentInfo, error) {
	// Re-check under the key: another goroutine may have stored a fresh entry
	// between our lock release and the singleflight start.
	c.mu.Lock()
	if el, ok := c.entries[rec.ID]; ok {
		item := el.Value.(*cacheItem)
		if sameFileState(item.rec, rec) {
			c.ll.MoveToFront(el)
			c.mu.Unlock()
			return item.info, nil
		}
		c.ll.Remove(el)
		delete(c.entries, rec.ID)
	}
	c.mu.Unlock()

	info, err := merge.ParseSegmentNoProbe(rec.FilePath)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rec.FilePath, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[rec.ID]; ok { // lost a race with another store
		item := el.Value.(*cacheItem)
		if sameFileState(item.rec, rec) {
			c.ll.MoveToFront(el)
			return item.info, nil
		}
		c.ll.Remove(el)
		delete(c.entries, rec.ID)
	}
	el := c.ll.PushFront(&cacheItem{rec: rec, info: info})
	c.entries[rec.ID] = el
	for c.ll.Len() > c.max {
		oldest := c.ll.Back()
		if oldest == nil {
			break
		}
		c.ll.Remove(oldest)
		delete(c.entries, oldest.Value.(*cacheItem).rec.ID)
	}
	return info, nil
}

// sameFileState compares the identity fields used for cache validation.
func sameFileState(a, b model.Recording) bool {
	return a.FilePath == b.FilePath && a.FileSize == b.FileSize && a.StartedAt.Equal(b.StartedAt)
}

// statRecording refreshes file size from disk so cache validation also
// catches appends to the same path (recordings being actively written are
// normally excluded by the caller's time filter, this is defense in depth).
func statRecording(rec *model.Recording) {
	if fi, err := os.Stat(rec.FilePath); err == nil {
		rec.FileSize = fi.Size()
	}
}
