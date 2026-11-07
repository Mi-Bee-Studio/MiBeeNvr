/**
 * Where the picture actually is: the content box of a player's media element
 * (object-fit: contain) in viewport coordinates. Overlays that map pointer
 * positions to image coordinates — the thermal spot probe, its over-threshold
 * markers, the dual-pane mirror — must measure against this box, not the
 * player container: a 4:3 thermal stream in a 16:9 (or height-capped) player
 * is letter/pillar-boxed, and a normalized position taken against the
 * container lands on the wrong pixel.
 */
export type Box = { left: number; top: number; width: number; height: number };

/** The visible picture element of a player host: the <video>, else the
 *  frame canvas (decoder players) — never the transparent AI overlay canvas
 *  (pointer-events-none). */
export function pickMedia(host: Element): HTMLVideoElement | HTMLCanvasElement | null {
  return host.querySelector('video, canvas:not([class*="pointer-events"])');
}

/** Contained content box of the host's media; the host's own box when there
 *  is no media yet or its intrinsic size is unknown. */
export function mediaContentBox(host: Element): Box {
  const hr = host.getBoundingClientRect();
  const media = pickMedia(host);
  if (!media) return { left: hr.left, top: hr.top, width: hr.width, height: hr.height };
  const mr = media.getBoundingClientRect();
  const iw = media instanceof HTMLVideoElement ? media.videoWidth : media.width;
  const ih = media instanceof HTMLVideoElement ? media.videoHeight : media.height;
  if (!iw || !ih || !mr.width || !mr.height) {
    return { left: mr.left, top: mr.top, width: mr.width, height: mr.height };
  }
  const scale = Math.min(mr.width / iw, mr.height / ih);
  const width = iw * scale;
  const height = ih * scale;
  return { left: mr.left + (mr.width - width) / 2, top: mr.top + (mr.height - height) / 2, width, height };
}

/** Normalized (0..1) picture position of a viewport point; null outside it. */
export function toNormalized(box: Box, clientX: number, clientY: number): { nx: number; ny: number } | null {
  if (!box.width || !box.height) return null;
  const nx = (clientX - box.left) / box.width;
  const ny = (clientY - box.top) / box.height;
  if (nx < 0 || nx > 1 || ny < 0 || ny > 1) return null;
  return { nx, ny };
}

/** Host-relative pixel position (for absolutely positioned markers) of a
 *  normalized picture position. */
export function toHostPx(host: Element, box: Box, nx: number, ny: number): { x: number; y: number } {
  const hr = host.getBoundingClientRect();
  return { x: box.left - hr.left + nx * box.width, y: box.top - hr.top + ny * box.height };
}
