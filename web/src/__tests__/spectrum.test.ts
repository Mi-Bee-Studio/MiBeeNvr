import { describe, it, expect } from 'vitest';
import { gbSiblings, spectrumLabelKey } from '$lib/spectrum';
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
