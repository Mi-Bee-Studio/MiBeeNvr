import { describe, it, expect } from 'vitest';
import { gbSiblings, spectrumLabelKey, gridCellAt, gridValues, gridFrame, cellsOverThreshold, thermalToVisible } from '$lib/spectrum';
import type { Camera } from '$lib/api/cameras';

const gb = (id: string, deviceId: string, imaging_type?: string): Camera =>
  ({ id, protocol: 'gb28181', gb28181: { device_id: deviceId, channel_id: id }, imaging_type }) as unknown as Camera;

describe('gbSiblings (dual-spectrum channels of one GB device)', () => {
  it('returns the other channels sharing the same gb28181.device_id', () => {
    const visible = gb('gb-1', '34020000001320000011');
    const thermal = gb('gb-2', '34020000001320000011', 'thermal');
    const other = gb('gb-3', '34020000009999999999');
    const got = gbSiblings(visible, [visible, thermal, other]);
    expect(got.map((c) => c.id)).toEqual(['gb-2']);
  });

  it('returns both siblings for a three-channel device and none for a lone channel', () => {
    const a = gb('gb-a', 'dev-1');
    const b = gb('gb-b', 'dev-1', 'thermal');
    const c = gb('gb-c', 'dev-1');
    expect(gbSiblings(a, [a, b, c]).map((x) => x.id).sort()).toEqual(['gb-b', 'gb-c']);
    expect(gbSiblings(gb('gb-x', 'dev-2'), [a, b, c])).toEqual([]);
  });

  it('links channels of separate SIP registrations via a shared spectrum_group', () => {
    // The real dual-spectrum rig topology: each channel registers as its own
    // GB device, so the device_ids differ and only the explicit group links them.
    const visible = { ...gb('gb-vis', '34020000001320000011'), spectrum_group: 'rig-7f' } as Camera;
    const thermal = { ...gb('gb-th', '34020000001320000003', 'thermal'), spectrum_group: 'rig-7f' } as Camera;
    const stranger = { ...gb('gb-9', '34020000001320000099', 'thermal'), spectrum_group: 'other-rig' } as Camera;
    const got = gbSiblings(thermal, [visible, thermal, stranger]);
    expect(got.map((c) => c.id)).toEqual(['gb-vis']);
    expect(gbSiblings(visible, [visible, thermal, stranger]).map((c) => c.id)).toEqual(['gb-th']);
  });

  it('does not group by an empty spectrum_group and needs no gb28181 binding when grouped', () => {
    const a = { ...gb('gb-a', 'dev-a'), spectrum_group: '' } as Camera;
    const b = { ...gb('gb-b', 'dev-b'), spectrum_group: '' } as Camera;
    expect(gbSiblings(a, [a, b])).toEqual([]);
    const noBinding = { id: 'gb-c', protocol: 'gb28181', spectrum_group: 'rig-7f' } as unknown as Camera;
    const linked = { id: 'gb-d', protocol: 'gb28181', spectrum_group: 'rig-7f' } as unknown as Camera;
    expect(gbSiblings(noBinding, [noBinding, linked]).map((c) => c.id)).toEqual(['gb-d']);
  });

  it('ignores non-GB cameras and a camera without a gb28181 binding', () => {
    const onvif = { id: 'cam-1', protocol: 'onvif' } as unknown as Camera;
    const visible = gb('gb-1', 'dev-1');
    expect(gbSiblings(visible, [visible, onvif])).toEqual([]);
    expect(gbSiblings({ id: 'gb-9', protocol: 'gb28181' } as unknown as Camera, [visible])).toEqual([]);
    expect(gbSiblings(visible, null)).toEqual([]);
  });
});

describe('spectrumLabelKey', () => {
  it('labels thermal siblings and visible siblings distinctly', () => {
    expect(spectrumLabelKey(gb('gb-2', 'd', 'thermal'))).toBe('live.spectrum.thermal');
    expect(spectrumLabelKey(gb('gb-1', 'd'))).toBe('live.spectrum.visible');
  });
});

describe('spot-temperature grid helpers', () => {
  const frame = { ok: true, data: { grid: { values: new Array(32 * 24).fill(20) } } };

  it('maps a normalized click to the right 32x24 cell', () => {
    expect(gridCellAt(0.5, 0.5)).toEqual({ x: 16, y: 12 });
    expect(gridCellAt(0, 0)).toEqual({ x: 0, y: 0 });
    expect(gridCellAt(0.999, 0.999)).toEqual({ x: 31, y: 23 });
  });

  it('unwraps the family envelope and extracts grid values', () => {
    expect(gridValues(frame)?.length).toBe(768);
    expect(gridValues({ grid: { values: [1, 2] } })).toBeNull(); // wrong size
    expect(gridValues({ ok: false, data: { grid: { values: new Array(768) } } })).toBeNull();
  });

  it('ranks over-threshold cells hottest-first, capped', () => {
    const v = new Array(768).fill(20);
    v[0] = 45; v[1] = 50; v[100] = 60; v[767] = 30;
    const hot = cellsOverThreshold(v, 30);
    expect(hot[0]).toMatchObject({ x: 4, y: 3, v: 60 }); // index 100
    expect(hot.map((c) => c.v)).toEqual([60, 50, 45, 30]);
  });
});

describe('dual-spectrum ONVIF pairs and vendor grid geometry', () => {
  it('pairs two ONVIF cameras of one device through spectrum_group', () => {
    // One Hikvision endpoint, visible + thermal media profiles = two cameras.
    const vis = { id: 'cam-v', protocol: 'onvif', spectrum_group: 'hik-1' } as unknown as Camera;
    const thr = { id: 'cam-t', protocol: 'onvif', spectrum_group: 'hik-1', imaging_type: 'thermal' } as unknown as Camera;
    const other = { id: 'cam-x', protocol: 'onvif', spectrum_group: 'other' } as unknown as Camera;
    expect(gbSiblings(vis, [vis, thr, other]).map((c) => c.id)).toEqual(['cam-t']);
    expect(gbSiblings(thr, [vis, thr, other]).map((c) => c.id)).toEqual(['cam-v']);
  });

  it('reads a 160x120 radiometric frame with its own geometry and registration', () => {
    const values = new Array(160 * 120).fill(25);
    values[60 * 160 + 80] = 41.5; // centre cell
    const f = gridFrame({
      grid: { cols: 160, rows: 120, values },
      visible_rect: { x: 0.149, y: 0.11, w: 0.646, h: 0.858 },
    });
    expect(f).not.toBeNull();
    expect(f!.cols).toBe(160);
    expect(f!.rows).toBe(120);
    const { x, y } = gridCellAt(0.5, 0.5, f!.cols, f!.rows);
    expect(f!.values[y * f!.cols + x]).toBe(41.5);
    expect(cellsOverThreshold(f!.values, 40, 8, f!.cols)[0]).toMatchObject({ x: 80, y: 60 });
    const v = thermalToVisible(0.5, 0.5, f!.visibleRect);
    expect(v.nx).toBeCloseTo(0.472, 3);
    expect(v.ny).toBeCloseTo(0.539, 3);
  });

  it('keeps the SPEC 32x24 default and rejects a geometry/value mismatch', () => {
    expect(gridFrame({ grid: { values: new Array(768).fill(1) } })?.cols).toBe(32);
    expect(gridFrame({ grid: { cols: 160, rows: 120, values: new Array(768) } })).toBeNull();
    expect(thermalToVisible(0.25, 0.75)).toEqual({ nx: 0.25, ny: 0.75 });
  });
});
