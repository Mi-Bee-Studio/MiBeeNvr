package vod

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Entry is one playable recording inside a playlist, with its fragment plan.
type Entry struct {
	Rec   model.Recording
	Info  *merge.SegmentInfo
	Frags []Fragment
}

// IncludeAudio reports whether a recording's audio track is carried into the
// fragments. MSE-friendly codecs only: AAC and Opus append fine to
// SourceBuffer; G.711 (ulaw/alaw sample entries) is rejected by Chrome's
// MediaSource, so it is dropped rather than breaking the whole stream.
func IncludeAudio(info *merge.SegmentInfo) bool {
	return info.HasAudio && info.AudioCodec != "g711"
}

// Manager owns the segment cache and builds playlists. One instance lives in
// the API handler for the process lifetime.
type Manager struct {
	cache        *segmentCache
	targetSec    float64
	maxRecordings int
}

func NewManager() *Manager {
	return &Manager{
		// ~12 hourly recordings ≈ 50MB of cached sample tables worst case —
		// bounded for the RPi 3B memory budget.
		cache:         newSegmentCache(12),
		targetSec:     TargetFragmentDur,
		maxRecordings: 200,
	}
}

// BuildPlaylist parses every recording of the range (in parallel, bounded)
// and renders the VOD M3U8. Recordings that fail to parse are skipped with a
// warning — a corrupt hour must not take down the whole day.
func (m *Manager) BuildPlaylist(cameraID string, recordings []model.Recording) (string, int, error) {
	if len(recordings) == 0 {
		return "", 0, fmt.Errorf("no recordings in range")
	}
	if len(recordings) > m.maxRecordings {
		recordings = recordings[:m.maxRecordings]
	}
	sort.Slice(recordings, func(i, j int) bool {
		return recordings[i].StartedAt.Before(recordings[j].StartedAt)
	})

	entries := make([]*Entry, 0, len(recordings))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4) // bound parse parallelism (RPi: keep disk+CPU sane)
	for i := range recordings {
		rec := recordings[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			statRecording(&rec)
			info, err := m.cache.Get(rec)
			if err != nil {
				logger.Warn("skipping unplayable recording in playlist",
					"recording_id", rec.ID, "error", err)
				return
			}
			mu.Lock()
			entries = append(entries, &Entry{Rec: rec, Info: info, Frags: PlanFragments(info, m.targetSec)})
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(entries) == 0 {
		return "", 0, fmt.Errorf("no parseable recordings in range")
	}
	// goroutines may complete out of order — re-sort by start time.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Rec.StartedAt.Before(entries[j].Rec.StartedAt)
	})

	return m.renderPlaylist(cameraID, entries), len(entries), nil
}

// FragmentURL is the canonical fragment URL scheme (also parsed back by the
// API handler). Half-open sample range [first,end).
func FragmentURL(cameraID, recordingID string, first, end int) string {
	return fmt.Sprintf("/api/cameras/%s/playback/%s/f%d-%d.m4s", cameraID, recordingID, first, end)
}

// InitURL is the per-recording init segment URL.
func InitURL(cameraID, recordingID string) string {
	return fmt.Sprintf("/api/cameras/%s/playback/%s/init.mp4", cameraID, recordingID)
}

func (m *Manager) renderPlaylist(cameraID string, entries []*Entry) string {
	target := 1
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")

	for i, e := range entries {
		if i > 0 {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		b.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=%q\n", InitURL(cameraID, e.Rec.ID)))
		for _, f := range e.Frags {
			dur := f.DurationSec(e.Info.Timescale)
			target = max(target, int(math.Ceil(dur)))
			fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", dur, FragmentURL(cameraID, e.Rec.ID, f.First, f.End))
		}
	}

	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", target)
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// GetInfo exposes the cached SegmentInfo for a recording (used by the init +
// fragment handlers).
func (m *Manager) GetInfo(rec model.Recording) (*merge.SegmentInfo, error) {
	statRecording(&rec)
	return m.cache.Get(rec)
}

// TargetSec returns the fragment target duration in seconds.
func (m *Manager) TargetSec() float64 { return m.targetSec }
