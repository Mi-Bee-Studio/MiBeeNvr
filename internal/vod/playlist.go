package vod

import (
	"time"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Entry is one playable recording inside a playlist, with its fragment plan.
type Entry struct {
	Rec   model.Recording
	Ts    uint32 // video timescale (EXTINF rendering)
	Frags []Fragment
}

// IncludeAudio reports whether a recording's audio track is carried into the
// fragments. MSE-friendly codecs only: AAC and Opus append fine to
// SourceBuffer; G.711 (ulaw/alaw sample entries) is rejected by Chrome's
// MediaSource, so it is dropped rather than breaking the whole stream.
func IncludeAudio(info *merge.SegmentInfo) bool {
	return info.HasAudio && info.AudioCodec != "g711"
}

// Manager owns the caches and builds playlists. One instance lives in the
// API handler for the process lifetime.
type Manager struct {
	cache         *segmentCache
	targetSec     float64
	maxRecordings int

	planMu sync.Mutex
	plans  map[string]*planCacheItem // recording ID → fragment plan
}

type planCacheItem struct {
	path    string
	size    int64
	mtimeNs int64
	ts      uint32
	frags   []Fragment
}

func NewManager() *Manager {
	return &Manager{
		// ~12 hourly recordings of parsed sample tables ≈ tens of MB — bounded
		// for the RPi 3B memory budget. Playlist generation does NOT depend on
		// this cache (it reads persisted fragment plans); only fragment/init
		// serving parses tables, and hls.js fetches those sequentially.
		cache:         newSegmentCache(12),
		targetSec:     TargetFragmentDur,
		maxRecordings: 200,
		plans:         make(map[string]*planCacheItem),
	}
}

// statFile returns (size, mtime) for plan-cache validation.
func statFile(path string) (int64, int64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	return fi.Size(), fi.ModTime().UnixNano(), true
}

// Fragments returns the fragment plan for a recording, resolving through
// (1) the in-memory plan cache, (2) the persisted sidecar plan, (3) live
// computation (sample-table parse + stss/probing oracle), persisting the
// result. The plan depends only on file identity — sample tables are parsed
// at most once per file state.
func (m *Manager) Fragments(rec model.Recording) ([]Fragment, uint32, error) {
	if rec.FilePath == "" {
		return nil, 0, fmt.Errorf("recording %s has no file path", rec.ID)
	}
	size, mtimeNs, ok := statFile(rec.FilePath)
	if !ok {
		return nil, 0, fmt.Errorf("stat %s", rec.FilePath)
	}

	m.planMu.Lock()
	if pc, ok := m.plans[rec.ID]; ok && pc.path == rec.FilePath && pc.size == size && pc.mtimeNs == mtimeNs {
		frags, ts := pc.frags, pc.ts
		m.planMu.Unlock()
		return frags, ts, nil
	}
	m.planMu.Unlock()

	// Persisted plan (validated internally against the same stat fields).
	if frags, ts, ok := readSidecar(rec.FilePath); ok {
		m.storePlan(rec.ID, rec.FilePath, size, mtimeNs, ts, frags)
		return frags, ts, nil
	}

	// Compute: parse sample tables once, pick the keyframe oracle.
	statRecording(&rec)
	info, err := m.cache.Get(rec)
	if err != nil {
		return nil, 0, err
	}
	var oracle keyframeOracle = stssOracle{samples: info.Samples}
	if !info.KeyframesFromStss {
		f, err := os.Open(info.FilePath)
		if err != nil {
			return nil, 0, fmt.Errorf("open for keyframe probing: %w", err)
		}
		oracle = newProbeOracle(f, info)
		defer f.Close()
	}
	frags := PlanFragments(info, m.targetSec, oracle)
	if len(frags) == 0 {
		return nil, 0, fmt.Errorf("no fragments planned for %s", rec.ID)
	}
	writeSidecar(info.FilePath, frags, info.Timescale)
	m.storePlan(rec.ID, rec.FilePath, size, mtimeNs, info.Timescale, frags)
	return frags, info.Timescale, nil
}

func (m *Manager) storePlan(id, path string, size, mtimeNs int64, ts uint32, frags []Fragment) {
	m.planMu.Lock()
	m.plans[id] = &planCacheItem{path: path, size: size, mtimeNs: mtimeNs, ts: ts, frags: frags}
	m.planMu.Unlock()
}

// BuildPlaylist plans every recording of the range (in parallel, bounded)
// and renders the VOD M3U8. Recordings that fail are skipped with a warning —
// a corrupt hour must not take down the whole day.
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
			frags, ts, err := m.Fragments(rec)
			if err != nil {
				logger.Warn("skipping unplayable recording in playlist",
					"recording_id", rec.ID, "error", err)
				return
			}
			mu.Lock()
			entries = append(entries, &Entry{Rec: rec, Ts: ts, Frags: frags})
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
		// Wall-clock anchor for the recording period: lets the client find the
		// NEAREST period by wall clock when rebuilding the session after
		// rolling merges replaced the currently-playing recording (404s).
		fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", e.Rec.StartedAt.UTC().Format(time.RFC3339Nano))
		b.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=%q\n", InitURL(cameraID, e.Rec.ID)))
		for _, f := range e.Frags {
			dur := f.DurationSec(e.Ts)
			target = max(target, int(math.Ceil(dur)))
			fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", dur, FragmentURL(cameraID, e.Rec.ID, f.First, f.End))
		}
	}

	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", target)
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// GetInfo exposes the cached (or freshly parsed) SegmentInfo for a recording
// — used by the init + fragment handlers. Audio-inclusion and sample offsets
// need the full table; this is the only path that parses it.
func (m *Manager) GetInfo(rec model.Recording) (*merge.SegmentInfo, error) {
	statRecording(&rec)
	return m.cache.Get(rec)
}

// TargetSec returns the fragment target duration in seconds.
func (m *Manager) TargetSec() float64 { return m.targetSec }
