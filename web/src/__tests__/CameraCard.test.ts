import { render, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import CameraCard from '$lib/components/CameraCard.svelte';
import { buildProtocolsMap, DEFAULT_PROTOCOLS } from '$lib/api';
import type { Camera } from '$lib/api';

// Mock API calls that CameraCard invokes
vi.mock('$lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('$lib/api')>();
  return {
    ...actual,
    enableCamera: vi.fn().mockResolvedValue({ id: 'test', enabled: true }),
    disableCamera: vi.fn().mockResolvedValue({ id: 'test', enabled: false }),
  };
});

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
    enabled: true,
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
    ontoggle: noop,
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

  it('shows disabled badge for disabled camera', () => {
    const camera = makeCamera({ enabled: false });
    const { getByText } = render(CameraCard, { props: defaultProps(camera) });
    expect(getByText('Disabled')).toBeTruthy();
  });
});
