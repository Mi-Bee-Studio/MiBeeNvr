package vod

import (
	"encoding/json"
	"os"
)

// Fragment-plan sidecar: the plan for one recording (keyframe-aligned
// fragment boundaries with durations) persisted next to the MP4 after it is
// first computed. A playlist for a whole day then needs no sample-table
// parsing at all — stat + read sidecar + render, sub-second even on a USB
// HDD. Sample tables are parsed on demand per recording when a fragment is
// actually served (hls.js fetches fragments sequentially, which the small
// in-memory LRU handles naturally).
//
// Best-effort: any read/write failure falls back to live computation.

const sidecarSuffix = ".vodidx"
const sidecarVersion = 2

type sidecarData struct {
	Version   int           `json:"version"`
	ModTimeNs int64         `json:"mtime_ns"`
	Size      int64         `json:"size"`
	Ts        uint32        `json:"ts"` // video timescale
	Frags     []sidecarFrag `json:"frags"`
}

type sidecarFrag struct {
	First     int    `json:"f"` // video sample range [First,End)
	End       int    `json:"e"`
	AudioFirst int   `json:"af"`
	AudioEnd  int    `json:"ae"`
	StartUnits uint64 `json:"s"`
	DurUnits  uint64 `json:"d"`
}

func sidecarPath(mp4Path string) string {
	return mp4Path + sidecarSuffix
}

// readSidecar returns the persisted fragment plan (+ video timescale) when
// the sidecar matches the recording file's identity (size + mtime).
func readSidecar(mp4Path string) ([]Fragment, uint32, bool) {
	raw, err := os.ReadFile(sidecarPath(mp4Path))
	if err != nil {
		return nil, 0, false
	}
	var d sidecarData
	if err := json.Unmarshal(raw, &d); err != nil || d.Version != sidecarVersion || d.Ts == 0 {
		return nil, 0, false
	}
	fi, err := os.Stat(mp4Path)
	if err != nil || fi.Size() != d.Size || fi.ModTime().UnixNano() != d.ModTimeNs {
		return nil, 0, false // stale — file was re-merged/replaced
	}
	if len(d.Frags) == 0 {
		return nil, 0, false
	}
	frags := make([]Fragment, len(d.Frags))
	for i, f := range d.Frags {
		frags[i] = Fragment{
			First: f.First, End: f.End,
			AudioFirst: f.AudioFirst, AudioEnd: f.AudioEnd,
			StartUnits: f.StartUnits, DurationUnits: f.DurUnits,
		}
	}
	return frags, d.Ts, true
}

// writeSidecar persists a fragment plan. Failures are silent (best-effort).
func writeSidecar(mp4Path string, frags []Fragment, ts uint32) {
	fi, err := os.Stat(mp4Path)
	if err != nil {
		return
	}
	d := sidecarData{
		Version:   sidecarVersion,
		ModTimeNs: fi.ModTime().UnixNano(),
		Size:      fi.Size(),
		Ts:        ts,
		Frags:     make([]sidecarFrag, len(frags)),
	}
	for i, f := range frags {
		d.Frags[i] = sidecarFrag{
			First: f.First, End: f.End,
			AudioFirst: f.AudioFirst, AudioEnd: f.AudioEnd,
			StartUnits: f.StartUnits, DurUnits: f.DurationUnits,
		}
	}
	raw, err := json.Marshal(&d)
	if err != nil {
		return
	}
	// Atomic-ish: temp + rename, mirroring the storage layer's crash-safety
	// convention. Same directory as the recording (NVR owns the tree).
	tmp := sidecarPath(mp4Path) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, sidecarPath(mp4Path)); err != nil {
		_ = os.Remove(tmp)
	}
}
