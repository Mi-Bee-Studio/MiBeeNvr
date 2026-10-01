import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import PtzControl from '$lib/components/PtzControl.svelte';

// --- Mock i18n (t returns the key) ---
vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

// --- Mock lucide-svelte icons ---
vi.mock('lucide-svelte', () => {
  const icons = ['ChevronUp', 'ChevronDown', 'ChevronLeft', 'ChevronRight', 'ZoomIn', 'ZoomOut'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = () => document.createElement('span');
  }
  return mock;
});

// --- Mock PTZ API functions ---
const mockPtzMove = vi.hoisted(() => vi.fn(async () => ({ status: 'ok' })));
const mockPtzStop = vi.hoisted(() => vi.fn(async () => ({ status: 'ok' })));
const mockGetPresets = vi.hoisted(() => vi.fn(async () => []));
const mockGoToPreset = vi.hoisted(() => vi.fn(async () => ({})));
const mockXiaomiMove = vi.hoisted(() => vi.fn(async () => ({ status: 'ok' })));
const mockXiaomiStop = vi.hoisted(() => vi.fn(async () => ({ status: 'ok' })));

vi.mock('$lib/api', async () => {
  const actual = await vi.importActual<typeof import('$lib/api')>('$lib/api');
  return {
    ...actual,
    ptzMove: mockPtzMove,
    ptzStop: mockPtzStop,
    getPTZPresets: mockGetPresets,
    goToPTZPreset: mockGoToPreset,
    xiaomiPtzMove: mockXiaomiMove,
    xiaomiPtzStop: mockXiaomiStop,
  };
});

beforeEach(() => {
  vi.clearAllMocks();
});
afterEach(() => cleanup());

describe('PtzControl', () => {
  it('renders the direction pad when enabled', () => {
    const { getByLabelText } = render(PtzControl, {
      cameraId: 'cam-1',
      enabled: true,
      protocol: 'onvif',
    });
    for (const key of ['ptz.up', 'ptz.down', 'ptz.left', 'ptz.right', 'ptz.zoomIn', 'ptz.zoomOut']) {
      expect(getByLabelText(key)).toBeTruthy();
    }
  });

  it('does not render when disabled', () => {
    const { queryByLabelText } = render(PtzControl, {
      cameraId: 'cam-1',
      enabled: false,
      protocol: 'onvif',
    });
    expect(queryByLabelText('ptz.up')).toBeNull();
  });

  it('pointerdown sends a continuous move; pointerup stops', async () => {
    const { getByLabelText } = render(PtzControl, {
      cameraId: 'cam-1',
      enabled: true,
      protocol: 'onvif',
    });
    await fireEvent.pointerDown(getByLabelText('ptz.up'));
    expect(mockPtzMove).toHaveBeenCalledWith(
      'cam-1',
      { mode: 'continuous', pan: 0, tilt: 0.5, zoom: 0 },
      expect.anything(),
    );
    await fireEvent.pointerUp(getByLabelText('ptz.up'));
    expect(mockPtzStop).toHaveBeenCalledWith('cam-1');
  });

  it('pointercancel also stops movement (touch gesture takeover guard)', async () => {
    const { getByLabelText } = render(PtzControl, {
      cameraId: 'cam-1',
      enabled: true,
      protocol: 'onvif',
    });
    const btn = getByLabelText('ptz.left');
    await fireEvent.pointerDown(btn);
    expect(mockPtzMove).toHaveBeenCalledTimes(1);
    await fireEvent.pointerCancel(btn);
    expect(mockPtzStop).toHaveBeenCalledWith('cam-1');
  });

  it('window blur stops an in-flight move (lost-focus guard)', async () => {
    const { getByLabelText } = render(PtzControl, {
      cameraId: 'cam-1',
      enabled: true,
      protocol: 'onvif',
    });
    await fireEvent.pointerDown(getByLabelText('ptz.right'));
    expect(mockPtzMove).toHaveBeenCalledTimes(1);
    fireEvent(window, new Event('blur'));
    await waitFor(() => {
      expect(mockPtzStop).toHaveBeenCalledWith('cam-1');
    });
  });

  it('xiaomi protocol routes through the unified vector API and hides zoom/presets', async () => {
    const { getByLabelText, queryByLabelText } = render(PtzControl, {
      cameraId: 'cam-x',
      enabled: true,
      protocol: 'xiaomi',
    });
    await fireEvent.pointerDown(getByLabelText('ptz.left'));
    expect(mockPtzMove).toHaveBeenCalledWith(
      'cam-x',
      { mode: 'continuous', pan: -0.5, tilt: 0, zoom: 0 },
      expect.anything(),
    );
    expect(mockXiaomiMove).not.toHaveBeenCalled();
    expect(queryByLabelText('ptz.zoomIn')).toBeNull();
    expect(queryByLabelText('ptz.zoomOut')).toBeNull();
  });

  it('speed selector changes the move magnitude', async () => {
    const { getByLabelText } = render(PtzControl, {
      cameraId: 'cam-1',
      enabled: true,
      protocol: 'onvif',
    });
    await fireEvent.click(getByLabelText('ptz.speedFast'));
    await fireEvent.pointerDown(getByLabelText('ptz.up'));
    expect(mockPtzMove).toHaveBeenCalledWith(
      'cam-1',
      { mode: 'continuous', pan: 0, tilt: 1.0, zoom: 0 },
      expect.anything(),
    );
    await fireEvent.click(getByLabelText('ptz.speedSlow'));
    await fireEvent.pointerDown(getByLabelText('ptz.right'));
    expect(mockPtzMove).toHaveBeenLastCalledWith(
      'cam-1',
      { mode: 'continuous', pan: 0.25, tilt: 0, zoom: 0 },
      expect.anything(),
    );
  });

  it('surfaces a failed stop instead of swallowing it', async () => {
    mockPtzStop.mockRejectedValueOnce(new Error('network down'));
    const { getByLabelText } = render(PtzControl, {
      cameraId: 'cam-1',
      enabled: true,
      protocol: 'onvif',
    });
    const btn = getByLabelText('ptz.up');
    await fireEvent.pointerDown(btn);
    await fireEvent.pointerUp(btn);
    await waitFor(() => {
      expect(document.querySelector('.ptz-error')).toBeTruthy();
    });
  });
});
