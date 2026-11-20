// Pure frame-math + timestamp helpers for the playback panel's cycler modes
// (timelapse / mjpeg / AVI through the JPEG sequence player). Split out of
// PlaybackPanel.svelte so the offset mapping contracts are unit-testable
// without mounting the player.
import type { Recording, TimelapseFrame } from '$lib/api';

/** Host-local calendar day (en-CA ⇒ YYYY-MM-DD) used by TimelineBar. */
export function timelineDate(recording: Recording | null | undefined): string {
  if (!recording || !recording.started_at) return new Date().toLocaleDateString('en-CA');
  return new Date(recording.started_at).toLocaleDateString('en-CA');
}

/**
 * Map a wall-clock offset (seconds within the recording) onto a cycler frame
 * index. Prefers per-frame timestamps (timelapse timelines are compressed —
 * file seconds ≠ wall seconds); falls back to even spacing over the duration.
 */
export function frameIndexAtOffset(
  frames: TimelapseFrame[],
  recording: Recording | null | undefined,
  offsetSec: number,
): number {
  if (frames.length === 0 || !recording) return 0;
  const start = Date.parse(recording.started_at);
  if (Number.isFinite(start) && frames[0]?.timestamp) {
    const targetMs = start + Math.max(0, offsetSec) * 1000;
    for (let i = 0; i < frames.length; i++) {
      const ts = Date.parse(frames[i].timestamp);
      if (!Number.isNaN(ts) && ts >= targetMs) return i;
    }
    return frames.length - 1;
  }
  const dur = recording.duration || 0;
  if (dur > 0) {
    return Math.min(frames.length - 1, Math.floor((Math.max(0, offsetSec) / dur) * frames.length));
  }
  return 0;
}

/** Inverse of frameIndexAtOffset: wall-clock seconds shown for the current frame. */
export function cyclerOffsetSec(
  frames: TimelapseFrame[],
  recording: Recording | null | undefined,
  currentIndex: number,
): number {
  const f = frames[currentIndex];
  if (!recording || !f) return 0;
  if (f.timestamp) {
    const start = Date.parse(recording.started_at);
    const d = Date.parse(f.timestamp) - start;
    if (Number.isFinite(d) && Number.isFinite(start)) return Math.max(0, d / 1000);
  }
  const total = frames.length || 1;
  return (currentIndex / total) * (recording.duration || 0);
}

/** HH:MM:SS stamp for the current frame (per-frame timestamp, else estimated). */
export function formatFrameTimestamp(
  frames: TimelapseFrame[],
  recording: Recording | null | undefined,
  currentIndex: number,
): string {
  if (!recording || !frames[currentIndex]) return '';
  const start = new Date(recording.started_at).getTime();
  const frame = frames[currentIndex];
  if (frame.timestamp) {
    const ts = new Date(frame.timestamp).getTime();
    const diff = Math.max(0, ts - start);
    return formatHms(diff / 1000);
  }
  const totalFrames = frames.length;
  const durationMs = recording.duration * 1000;
  const estimatedSec = Math.floor((currentIndex / Math.max(1, totalFrames)) * (durationMs / 1000));
  return formatHms(estimatedSec);
}

function formatHms(totalSec: number): string {
  const s = Math.floor(totalSec);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}`;
}
