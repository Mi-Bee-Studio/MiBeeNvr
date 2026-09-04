import { render, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import CameraCard from '$lib/components/CameraCard.svelte';
import { buildProtocolsMap, DEFAULT_PROTOCOLS } from '$lib/api';
import type { Camera } from '$lib/api';

const getCameraProtocolsMock = vi.hoisted(() => vi.fn());

vi.mock('$lib/api', async (importOriginal) => {
  const orig = await importOriginal<typeof import('$lib/api')>();
  return { ...orig, getCameraProtocols: getCameraProtocolsMock };
});

const copyTextMock = vi.hoisted(() => vi.fn());
vi.mock('$lib/clipboard', () => ({ copyText: copyTextMock }));

const toastMock = vi.hoisted(() => vi.fn());
vi.mock('$lib/toast', () => ({ showToast: toastMock }));

// Mock lucide-svelte icons
vi.mock('lucide-svelte', () => {
  const icons = ['Pencil', 'Play', 'Square', 'RotateCw', 'Eye', 'MoreVertical', 'Archive', 'Link', 'Loader2'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = () => document.createElement('span');
  }
  return mock;
});

const protocolsMap = buildProtocolsMap(DEFAULT_PROTOCOLS);

function makeCamera(overrides: Partial<Camera> = {}): Camera {
  return {
    id: 'test',
    name: 'Test Camera',
    protocol: 'rtsp',
    url: 'rtsp://192.168.1.100/stream',
    ...overrides,
  };
}

const noop = vi.fn();

function defaultProps(camera: Camera) {
  return {
    camera,
    protocolsMap,
    onedit: noop,
    ondelete: noop,
    onstart: noop,
    onstop: noop,
    onrestart: noop,
    onsaveName: noop,
  };
}

describe('CameraCard', () => {
  afterEach(() => cleanup());

  it('shows camera name', () => {
    const { getByText } = render(CameraCard, { props: defaultProps(makeCamera()) });
    expect(getByText('Test Camera')).toBeTruthy();
  });

  it('shows recording badge for active camera with recording status', () => {
    const camera = makeCamera({ status: 'recording' });
    const { getByText } = render(CameraCard, { props: defaultProps(camera) });
    expect(getByText('Recording')).toBeTruthy();
  });

  it('recording toggle has correct aria-labels', () => {
    const idle = makeCamera({ status: 'idle' });
    const { getByRole, unmount } = render(CameraCard, { props: defaultProps(idle) });
    expect(getByRole('switch', { name: 'Start' })).toBeTruthy();
    unmount();

    const rec = makeCamera({ status: 'recording' });
    const rendered = render(CameraCard, { props: defaultProps(rec) });
    expect(rendered.getByRole('switch', { name: 'Stop' })).toBeTruthy();
    expect(rendered.getByRole('button', { name: 'Restart' })).toBeTruthy();
  });

  it('action buttons contain text label spans', () => {
    const idle = makeCamera({ status: 'idle' });
    const { getByText, unmount } = render(CameraCard, { props: defaultProps(idle) });
    expect(getByText('Start')).toBeTruthy();
    unmount();

    const rec = makeCamera({ status: 'recording' });
    const rendered = render(CameraCard, { props: defaultProps(rec) });
    expect(rendered.getByText('Stop')).toBeTruthy();
  });

  it('live link has aria-label when HLS is available', () => {
    const camera = makeCamera();
    const { container } = render(CameraCard, { props: defaultProps(camera) });
    const liveLink = container.querySelector('a[href="#/live/test"]');
    expect(liveLink).toBeTruthy();
    expect(liveLink?.getAttribute('aria-label')).toBe('Live');
  });

  it('RTSP copy button fetches the server URL and copies it', async () => {
    getCameraProtocolsMock.mockReset();
    copyTextMock.mockReset();
    toastMock.mockReset();
    getCameraProtocolsMock.mockResolvedValue({
      protocols: [],
      encoding: 'h264',
      default: 'hls',
      rtsp: { available: true, url: 'rtsp://192.168.63.30:8554/test' },
    });
    copyTextMock.mockResolvedValue(true);
    const { container } = render(CameraCard, { props: defaultProps(makeCamera()) });
    const btn = container.querySelector('button[title*="RTSP"], button[aria-label*="RTSP"]');
    expect(btn).toBeTruthy();
    btn?.click();
    await vi.waitFor(() => expect(getCameraProtocolsMock).toHaveBeenCalledWith('test'));
    await vi.waitFor(() => expect(copyTextMock).toHaveBeenCalledWith('rtsp://192.168.63.30:8554/test'));
    expect(toastMock).toHaveBeenCalledWith(expect.stringContaining('RTSP'), 'success');
  });

  it('RTSP copy button toasts the reason when output is unavailable', async () => {
    getCameraProtocolsMock.mockReset();
    copyTextMock.mockReset();
    toastMock.mockReset();
    getCameraProtocolsMock.mockResolvedValue({
      protocols: [],
      encoding: 'mpeg4',
      default: '',
      rtsp: { available: false, reason: 'codec not servable over RTSP (H.264/H.265/MJPEG only)' },
    });
    const { container } = render(CameraCard, { props: defaultProps(makeCamera()) });
    const btn = container.querySelector('button[title*="RTSP"], button[aria-label*="RTSP"]');
    btn?.click();
    await vi.waitFor(() => expect(toastMock).toHaveBeenCalledWith(expect.stringContaining('RTSP'), 'error'));
    expect(copyTextMock).not.toHaveBeenCalled();
  });
});
