import { describe, it, expect } from 'vitest';
import { mediaContentBox, toNormalized, toHostPx } from '$lib/mediaBox';

// A 640×480 (4:3) thermal picture shown object-fit: contain in a 1200×576
// element is pillarboxed to 768×576 — a click must map against that box.
function hostWithVideo(): HTMLElement {
  const host = document.createElement('div');
  const v = document.createElement('video');
  host.appendChild(v);
  Object.defineProperty(v, 'videoWidth', { value: 640 });
  Object.defineProperty(v, 'videoHeight', { value: 480 });
  const rect = (l: number, t: number, w: number, h: number) =>
    ({ left: l, top: t, width: w, height: h, x: l, y: t, right: l + w, bottom: t + h, toJSON() {} }) as DOMRect;
  host.getBoundingClientRect = () => rect(0, 100, 1200, 576);
  v.getBoundingClientRect = () => rect(0, 100, 1200, 576);
  return host;
}

describe('mediaContentBox', () => {
  it('pillarboxes a 4:3 picture inside a wide element', () => {
    const box = mediaContentBox(hostWithVideo());
    expect(box.width).toBeCloseTo(768);
    expect(box.height).toBeCloseTo(576);
    expect(box.left).toBeCloseTo(216);
    expect(box.top).toBeCloseTo(100);
  });

  it('maps clicks against the picture, not the container', () => {
    const host = hostWithVideo();
    const box = mediaContentBox(host);
    expect(toNormalized(box, 600, 388)).toEqual({ nx: 0.5, ny: 0.5 });
    expect(toNormalized(box, 100, 388)).toBeNull(); // in the black bar
    const p = toHostPx(host, box, 0, 1);
    expect(p.x).toBeCloseTo(216);
    expect(p.y).toBeCloseTo(576);
  });

  it('falls back to the host box without media', () => {
    const host = document.createElement('div');
    host.getBoundingClientRect = () =>
      ({ left: 5, top: 6, width: 100, height: 50, x: 5, y: 6, right: 105, bottom: 56, toJSON() {} }) as DOMRect;
    expect(mediaContentBox(host)).toEqual({ left: 5, top: 6, width: 100, height: 50 });
  });
});
