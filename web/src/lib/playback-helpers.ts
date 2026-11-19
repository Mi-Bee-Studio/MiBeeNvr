/**
 * Pure helpers for the recording playback panel.
 *
 * Extracted from PlaybackPanel.svelte: URL selection rules, seek awaiting and
 * small formatting utilities — no component state, directly unit-testable.
 */

/**
 * Which URL can feed a `<video>` element for this recording, or null when the
 * browser cannot play it directly.
 *
 * h264/h265 segments stream as-is. `timelapse` and `mjpeg` are JPEG-frame
 * recordings: only their merged MP4 output is video-playable (an unmerged one
 * stays with the JPEG cycler). The codec of that merged output is probed
 * asynchronously by the caller — this sync decision only rules out formats that
 * can never work.
 */
export function segmentVideoUrlSync(
  rec: { id: string; format?: string; merge_status?: string; merge_path?: string },
  urls: { segment: (id: string) => string; merged: (id: string) => string },
): string | null {
  const f = rec.format;
  if (f === 'h264' || f === 'h265') return urls.segment(rec.id);
  if (f === 'timelapse' || f === 'mjpeg') {
    if (rec.merge_status === 'merged' && rec.merge_path) return urls.merged(rec.id);
  }
  return null;
}

/**
 * Seek a media element and resolve once the seek lands (or after a safety
 * timeout), so callers can await continuity instead of racing on events.
 */
export function seekEl(el: HTMLVideoElement, offset: number, timeoutMs = 5000): Promise<void> {
  return new Promise((resolve) => {
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      el.removeEventListener('seeked', finish);
      resolve();
    };
    const timer = setTimeout(finish, timeoutMs);
    el.addEventListener('seeked', finish);
    el.currentTime = offset;
  });
}

/** Clamp a seek target into the recording's duration. */
export function clampOffset(offset: number, duration: number): number {
  if (!Number.isFinite(duration) || duration <= 0) return 0;
  return Math.min(Math.max(offset, 0), duration);
}
