// DayTimeline row-building: groups a day's recordings per camera into the
// render model (coverage bands, timelapse fragments/ribbons, merge strips,
// linked-audio segments). Pure data shaping — extracted from DayTimeline.svelte
// so the geometry contracts are unit-testable without a DOM.
import type { Recording, Camera } from '$lib/api';
import { parseServerDate } from '$lib/format';
import { epochMsToDaySec, type TimelineSegment } from '$lib/timeline-utils';

// Minimal shape DayTimeline actually reads from each recording. Accepting this
// subset (instead of the full Recording) lets the page pass the lightweight
// RecordingTimelineSegment[] from /api/recordings/timeline (issue #115) without
// a cast — both Recording and RecordingTimelineSegment satisfy it.
export type TimelineRecording = Pick<
  Recording,
  'id' | 'camera_id' | 'started_at' | 'ended_at' | 'duration' | 'format' | 'merge_status' | 'motion_score'
>;

export interface CameraRow {
  camera: Camera;
  segments: TimelineSegment[]; // seconds-from-midnight, for findSegmentAt (video only)
  bands: CoverageBand[]; // render data (video formats only)
  coverageSec: number; // total recorded seconds (video formats only)
  pendingCount: number; // un-merged segments (still fragments)
  tlFrags: TLFragment[]; // raw timelapse segments (point samples)
  tlRibbons: TLRibbon[]; // visually coalesced ranges of tlFrags
  mergeStrips: MergeStrip[]; // completed merges intersecting the day (≤2 layers)
  audioSource: Camera | null; // linked standalone audio source (#963)
  audioSegs: AudioSeg[]; // its audio segments mapped onto this row's day
}

// A standalone audio source's segment positioned on the 24h axis. Audio is
// genuine continuous coverage (not point samples), so these render as a
// solid strip; the click target opens the audio recording itself.
export interface AudioSeg {
  recordingId: string;
  startSec: number;
  endSec: number;
}

export interface CoverageBand {
  recordingId: string;
  startSec: number; // seconds from midnight
  endSec: number;
  format: string;
  merged: boolean;
  motionScore: number; // -1 = not analyzed (#435)
  motionConfidence: number; // -1 = pre-#634 row (#634)
}

export interface TLFragment {
  recordingId: string;
  startSec: number;
  endSec: number;
}

// A dotted ribbon is a VISUAL coalescing of adjacent timelapse fragments.
// The #733 bug period wrote ~150s/6-frame fragments on every JPEG-camera
// reconnect (206/day observed) — rendering those individually would produce
// an unreadable mess of hairlines. Hit resolution stays per-fragment.
export interface TLRibbon {
  startSec: number;
  endSec: number;
  fragCount: number;
}

// Adjacent timelapse windows closer than this render as one ribbon.
const TL_RIBBON_GAP_SEC = 120;

// Minimal shape of a completed periodic timelapse merge used for the top
// strip layer. Decoupled from the full TimelapseMerge so the page can pass a
// projected subset.
export interface TimelineMergeMark {
  id: number;
  camera_id: string;
  window_start: string; // ISO timestamp
  window_end: string; // ISO timestamp
  duration_label: string; // "8h" / "natural-day" / "7d" / ...
  frame_count: number;
  file_size: number;
}

export interface MergeStrip {
  merge: TimelineMergeMark;
  startSec: number; // intersection of the merge window with the day
  endSec: number;
  layer: number; // 0 = topmost; overlapping windows stack, max 2 layers
}

const MERGE_STRIP_LAYERS = 2;

export const DAY_SECONDS = 86400;

export function clampDay(sec: number): number {
  return Math.max(0, Math.min(DAY_SECONDS, sec));
}

export function buildCameraRows(args: {
  cameras: Camera[];
  recordings: TimelineRecording[];
  merges?: TimelineMergeMark[];
  dayStartMs: number;
}): CameraRow[] {
  const { cameras, recordings, merges, dayStartMs } = args;
  // Group recordings by camera
  const byCam = new Map<string, TimelineRecording[]>();
  for (const r of recordings) {
    const list = byCam.get(r.camera_id) ?? [];
    list.push(r);
    byCam.set(r.camera_id, list);
  }
  // Group merges by camera
  const mergesByCam = new Map<string, TimelineMergeMark[]>();
  for (const m of merges ?? []) {
    const list = mergesByCam.get(m.camera_id) ?? [];
    list.push(m);
    mergesByCam.set(m.camera_id, list);
  }
  // Standalone audio sources (#963): a source linked to a video camera
  // (encoding "audio" + audio_link_camera_id) renders its segments as an
  // audio track on the LINKED camera's row, in addition to the source's own
  // row. The day's recordings were fetched for all cameras at once, so the
  // audio segments are already in `recordings` — no extra fetch needed.
  const audioCams = new Map<string, Camera>();
  for (const cam of cameras) {
    if (cam.encoding === 'audio') audioCams.set(cam.id, cam);
  }
  const audioByLinked = new Map<string, { source: Camera; segs: AudioSeg[] }>();
  for (const ac of audioCams.values()) {
    const linkedId = ac.audio_link_camera_id;
    if (!linkedId) continue; // unlinked source — only its own row shows it
    const segs: AudioSeg[] = [];
    for (const r of (byCam.get(ac.id) ?? [])
      .slice()
      .sort((a, b) => new Date(a.started_at).getTime() - new Date(b.started_at).getTime())) {
      const startMs = new Date(r.started_at).getTime();
      const endMs = r.ended_at ? new Date(r.ended_at).getTime() : startMs + (r.duration || 0) * 1000;
      const startSec = clampDay(epochMsToDaySec(startMs, dayStartMs));
      const endSec = clampDay(epochMsToDaySec(endMs, dayStartMs));
      if (endSec <= startSec) continue;
      segs.push({ recordingId: r.id, startSec, endSec });
    }
    if (segs.length > 0) audioByLinked.set(linkedId, { source: ac, segs });
  }
  const result: CameraRow[] = [];
  for (const cam of cameras) {
    const recs = (byCam.get(cam.id) ?? [])
      .slice()
      .sort((a, b) => new Date(a.started_at).getTime() - new Date(b.started_at).getTime());
    const bands: CoverageBand[] = [];
    const segs: TimelineSegment[] = [];
    const tlFrags: TLFragment[] = [];
    let coverageSec = 0;
    let pendingCount = 0;
    for (const r of recs) {
      const startMs = new Date(r.started_at).getTime();
      const endMs = r.ended_at ? new Date(r.ended_at).getTime() : startMs + (r.duration || 0) * 1000;
      const startSec = clampDay(epochMsToDaySec(startMs, dayStartMs));
      const endSec = clampDay(epochMsToDaySec(endMs, dayStartMs));
      if (endSec <= startSec) continue;
      if (r.format === 'timelapse') {
        // Timelapse rows never count as coverage — their span is a capture
        // window of sparse point samples, not continuous recording.
        tlFrags.push({ recordingId: r.id, startSec, endSec });
        continue;
      }
      bands.push({
        recordingId: r.id,
        startSec,
        endSec,
        format: r.format,
        merged: r.merge_status === 'merged' || r.merge_status === 'daily_merged',
        motionScore: r.motion_score ?? -1,
        motionConfidence: r.motion_confidence ?? -1,
      });
      segs.push({ id: r.id, startSec, endSec });
      coverageSec += endSec - startSec;
      if (r.merge_status === 'pending' || r.merge_status === '') pendingCount++;
    }
    const audio = cam.encoding === 'audio' ? undefined : audioByLinked.get(cam.id);
    result.push({
      camera: cam,
      segments: segs,
      bands,
      coverageSec,
      pendingCount,
      tlFrags,
      tlRibbons: coalesceRibbons(tlFrags),
      mergeStrips: buildMergeStrips(mergesByCam.get(cam.id) ?? [], dayStartMs),
      audioSource: audio?.source ?? null,
      audioSegs: audio?.segs ?? [],
    });
  }
  return result;
}

function coalesceRibbons(frags: TLFragment[]): TLRibbon[] {
  const sorted = frags.slice().sort((a, b) => a.startSec - b.startSec);
  const ribbons: TLRibbon[] = [];
  for (const f of sorted) {
    const last = ribbons[ribbons.length - 1];
    if (last && f.startSec - last.endSec <= TL_RIBBON_GAP_SEC) {
      last.endSec = Math.max(last.endSec, f.endSec);
      last.fragCount++;
    } else {
      ribbons.push({ startSec: f.startSec, endSec: f.endSec, fragCount: 1 });
    }
  }
  return ribbons;
}

// Merge strips: intersect each merge window with the day, then stack
// overlapping windows greedily into ≤MERGE_STRIP_LAYERS layers (widest
// window first so day-scale merges sit on the top layer). Windows beyond
// the layer cap are dropped — the exhaustive list lives in the history card.
function buildMergeStrips(list: TimelineMergeMark[], dayStartMs: number): MergeStrip[] {
  const spans = list
    .map((m) => {
      const ws = clampDay(epochMsToDaySec(parseServerDate(m.window_start).getTime(), dayStartMs));
      const we = clampDay(epochMsToDaySec(parseServerDate(m.window_end).getTime(), dayStartMs));
      return { merge: m, startSec: ws, endSec: we };
    })
    .filter((s) => s.endSec > s.startSec)
    .sort((a, b) => b.endSec - b.startSec - (a.endSec - a.startSec));
  const layers: Array<Array<{ startSec: number; endSec: number }>> = [];
  const strips: MergeStrip[] = [];
  for (const s of spans) {
    let placed = false;
    for (let li = 0; li < MERGE_STRIP_LAYERS && !placed; li++) {
      const layer = layers[li] ?? (layers[li] = []);
      if (layer.every((o) => s.startSec >= o.endSec || s.endSec <= o.startSec)) {
        layer.push(s);
        strips.push({ ...s, layer: li });
        placed = true;
      }
    }
  }
  return strips.sort((a, b) => a.layer - b.layer || a.startSec - b.startSec);
}
