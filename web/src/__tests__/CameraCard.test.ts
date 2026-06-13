import { render, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import CameraCard from '$lib/components/CameraCard.svelte';
import { buildProtocolsMap, DEFAULT_PROTOCOLS } from '$lib/api';
import type { Camera } from '$lib/api';

// Mock lucide-svelte icons
vi.mock('lucide-svelte', () => {
  const icons = ['Pencil', 'Play', 'Square', 'RotateCw', 'Eye', 'MoreVertical', 'Archive'];
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
});
