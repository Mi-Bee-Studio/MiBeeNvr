import { render, cleanup, fireEvent } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import CameraForm from '$lib/components/CameraForm.svelte';
import { buildProtocolsMap, DEFAULT_PROTOCOLS } from '$lib/api';
import type { ProtocolInfo } from '$lib/api';

// --- Mock i18n ---
vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

// --- Mock lucide-svelte icons ---
vi.mock('lucide-svelte', () => {
  const icons = [
    'Eye',
    'EyeOff',
    'PlugZap',
    'Plus',
    'Trash2',
    'ArrowUpRight',
    'Copy',
    'Layers',
    'Brain',
    'Wifi',
    'Users',
    'Power',
    'RefreshCw',
    'AlertTriangle',
    'Save',
    'ChevronDown',
    'Move',
    'Image',
    'Bell',
    'Camera',
    'Radio',
    'Activity',
    'Circle',
  ];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = () => document.createElement('span');
  }
  return mock;
});

// --- Mock PushTargetStatus component ---
vi.mock('$lib/components/PushTargetStatus.svelte', () => ({
  default: () => ({ $$render: () => '' }),
}));

const mockApiRequest = vi.hoisted(() => vi.fn());

// --- Mock API client ---
vi.mock('$lib/api/client', async () => {
  const actual = await vi.importActual('$lib/api/client');
  return {
    ...actual,
    apiRequest: mockApiRequest,
  };
});

// --- Mock API functions called by CameraForm ---
vi.mock('$lib/api', async () => {
  const actual = await vi.importActual('$lib/api');
  return {
    ...actual,
    getPushStatus: vi.fn().mockResolvedValue({ targets: [] }),
    getMergeConfig: vi.fn().mockResolvedValue(null),
    getDeviceCapabilities: vi.fn().mockRejectedValue(new Error('no device')),
    getUntranscodedRecordingCount: vi.fn().mockResolvedValue({ count: 0 }),
  };
});

const protocolsMap = buildProtocolsMap(DEFAULT_PROTOCOLS as ProtocolInfo[]);

const mockPresets = [
  { name: 'youtube', description: 'YouTube live streaming' },
  { name: 'bilibili', description: 'Bilibili live streaming' },
  { name: 'generic', description: 'Generic RTMP/RTSP server' },
];

type CameraFormProps = {
  editingCamera: any;
  protocols: ProtocolInfo[];
  protocolsMap: Map<string, ProtocolInfo>;
  onsave: () => void;
  oncancel: () => void;
  groupSuggestions?: string[];
};

function defaultProps(overrides: Partial<CameraFormProps> = {}): CameraFormProps {
  return {
    editingCamera: null,
    protocols: DEFAULT_PROTOCOLS as ProtocolInfo[],
    protocolsMap,
    onsave: vi.fn(),
    oncancel: vi.fn(),
    ...overrides,
  };
}

describe('CameraForm - push target platform selector', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  it('fetches relay presets on mount', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') {
        return Promise.resolve(mockPresets);
      }
      return Promise.resolve(null);
    });

    render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });
  });

  it('populates platform dropdown with preset options', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') {
        return Promise.resolve(mockPresets);
      }
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const addTargetBtn = buttons.find((b) => b.textContent?.includes('pushOutAdd'));
    if (!addTargetBtn) throw new Error('Add Target button not found');
    await fireEvent.click(addTargetBtn);

    const selects = container.querySelectorAll('select');
    const platformSelect = Array.from(selects).find((s) => {
      const opts = Array.from(s.options);
      return opts.some((o) => o.value === 'youtube');
    });
    expect(platformSelect).toBeTruthy();

    const opts = Array.from(platformSelect!.options);
    expect(opts.some((o) => o.textContent?.includes('youtube'))).toBe(true);
    expect(opts.some((o) => o.textContent?.includes('bilibili'))).toBe(true);
  });

  it('shows Loading... while presets are being fetched', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') {
        return new Promise(() => {});
      }
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    // Wait a tick for $effect to start, then wait for button visibility
    await vi.waitFor(() => {
      const buttons = Array.from(container.querySelectorAll('button'));
      const addTargetBtn = buttons.find((b) => b.textContent?.includes('pushOutAdd'));
      expect(addTargetBtn).toBeTruthy();
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const addTargetBtn = buttons.find((b) => b.textContent?.includes('pushOutAdd'));
    if (addTargetBtn) await fireEvent.click(addTargetBtn);

    const selects = container.querySelectorAll('select');
    const platformSelect = Array.from(selects).find((s) => {
      const opts = Array.from(s.options);
      return opts.some((o) => o.textContent === 'Loading...');
    });
    expect(platformSelect).toBeTruthy();
  });

  it('falls back to Generic option on fetch error', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') {
        return Promise.reject(new Error('Network error'));
      }
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const addTargetBtn = buttons.find((b) => b.textContent?.includes('pushOutAdd'));
    if (!addTargetBtn) throw new Error('Add Target button not found');
    await fireEvent.click(addTargetBtn);

    await vi.waitFor(() => {
      const selects = container.querySelectorAll('select');
      const platformSelect = Array.from(selects).find((s) => {
        const opts = Array.from(s.options);
        return opts.some((o) => o.textContent === 'cameras.pushPlatformGeneric');
      });
      expect(platformSelect).toBeTruthy();
    });
  });
});

describe('CameraForm - encoding field visibility (#166)', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  // Xiaomi recorders detect codec from the live stream and ignore any stored
  // encoding, so the field must be read-only "auto-detect" (like ONVIF).
  it('renders encoding as disabled auto-detect for xiaomi cameras', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const xiaomiCam = {
      id: 'xiaomi-test',
      name: 'Xiaomi Cam',
      protocol: 'xiaomi',
      encoding: 'h264', // stale value — must NOT be editable
      url: 'xiaomi://abc',
      enabled: true,
    };
    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: xiaomiCam as any }),
    });

    await vi.waitFor(() => {
      const encSelect = container.querySelector('#cam-encoding') as HTMLSelectElement | null;
      expect(encSelect, 'encoding <select> should exist').toBeTruthy();
      expect(encSelect!.disabled, 'encoding field must be disabled for xiaomi').toBe(true);
      // The only option is auto-detect
      const opts = Array.from(encSelect!.options);
      expect(opts.length).toBe(1);
      expect(opts[0].value).toBe('');
    });
  });

  // rtsp/http/srt/rtmp keep encoding editable — it drives recorder selection.
  it('renders encoding as editable for rtsp cameras', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const rtspCam = {
      id: 'rtsp-test',
      name: 'RTSP Cam',
      protocol: 'rtsp',
      encoding: 'h264',
      url: 'rtsp://example/stream',
      enabled: true,
    };
    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: rtspCam as any }),
    });

    await vi.waitFor(() => {
      const encSelect = container.querySelector('#cam-encoding') as HTMLSelectElement | null;
      expect(encSelect, 'encoding <select> should exist').toBeTruthy();
      expect(encSelect!.disabled, 'encoding field must be editable for rtsp').toBe(false);
      // Multiple codec options available (h264/h265/mjpeg)
      expect(encSelect!.options.length).toBeGreaterThan(1);
    });
  });
});

describe('CameraForm - transcode policy', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  it('shows H.264 hint when source encoding is h264', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') {
        return Promise.resolve(mockPresets);
      }
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const addTargetBtn = buttons.find((b) => b.textContent?.includes('pushOutAdd'));
    if (!addTargetBtn) throw new Error('Add Target button not found');
    await fireEvent.click(addTargetBtn);

    const spans = container.querySelectorAll('span');
    const hintSpan = Array.from(spans).find((s) => s.textContent?.includes('cameras.pushTranscodeNA'));
    expect(hintSpan).toBeTruthy();
  });

  it('shows transcode policy selector for H.265 source', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') {
        return Promise.resolve(mockPresets);
      }
      return Promise.resolve(null);
    });

    const h265Camera = {
      id: 'cam-h265',
      name: 'H265 Cam',
      protocol: 'rtsp',
      encoding: 'h265',
      url: 'rtsp://192.168.1.100/stream',
    };

    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: h265Camera }),
    });

    // Wait for effects to settle and relay presets to load
    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });

    // Wait for the add target button to appear
    await vi.waitFor(() => {
      const buttons = Array.from(container.querySelectorAll('button'));
      const addTargetBtn = buttons.find((b) => b.textContent?.includes('pushOutAdd'));
      expect(addTargetBtn).toBeTruthy();
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const addTargetBtn = buttons.find((b) => b.textContent?.includes('pushOutAdd'));
    if (addTargetBtn) await fireEvent.click(addTargetBtn);

    // Now look for the transcode policy select with Auto-detect hardware
    await vi.waitFor(() => {
      const selects = container.querySelectorAll('select');
      const transcodeSelect = Array.from(selects).find((s) => {
        const opts = Array.from(s.options);
        return opts.some((o) => o.value === 'auto' && o.textContent?.includes('cameras.pushTranscodeAuto'));
      });
      expect(transcodeSelect).toBeTruthy();
    });
  });
});

describe('CameraForm - push camera URL exemption', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  // rtmp/srt/whip push cameras are identified by stream key / stream-id and
  // the form shows no URL field for them. validate() used to require a URL
  // for rtmp/srt anyway — the hidden-field error silently blocked save.
  it('submits a create for an rtmp push camera without a URL', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(container.querySelector('#cam-name')).toBeTruthy();
    });

    const nameInput = container.querySelector('#cam-name') as HTMLInputElement;
    await fireEvent.input(nameInput, { target: { value: 'Push Cam' } });

    const protoSelect = container.querySelector('#cam-protocol') as HTMLSelectElement;
    await fireEvent.change(protoSelect, { target: { value: 'rtmp' } });
    await vi.waitFor(() => {
      expect(container.querySelector('#cam-stream-key')).toBeTruthy();
      expect(container.querySelector('#cam-url')).toBeFalsy();
    });

    const keyInput = container.querySelector('#cam-stream-key') as HTMLInputElement;
    await fireEvent.input(keyInput, { target: { value: 'push-key-1' } });

    const saveBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('cameras.save'),
    );
    await fireEvent.click(saveBtn!);

    await vi.waitFor(() => {
      const create = mockApiRequest.mock.calls.find((c) => typeof c[0] === 'string' && c[0] === '/cameras');
      expect(create, 'POST /cameras should have been issued').toBeTruthy();
    });
  });
});

describe('CameraForm - preset override panel', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  it('renders as collapsed details element', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') {
        return Promise.resolve(mockPresets);
      }
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const addTargetBtn = buttons.find((b) => b.textContent?.includes('pushOutAdd'));
    if (!addTargetBtn) throw new Error('Add Target button not found');
    await fireEvent.click(addTargetBtn);

    const details = Array.from(container.querySelectorAll('details'));
    const overrideDetails = details.find((d) => {
      const summary = d.querySelector('summary');
      return summary?.textContent?.includes('cameras.pushPresetOverrides');
    });
    expect(overrideDetails).toBeTruthy();
    expect(overrideDetails?.hasAttribute('open')).toBe(false);
  });

  it('shows all override inputs when expanded', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') {
        return Promise.resolve(mockPresets);
      }
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const addTargetBtn = buttons.find((b) => b.textContent?.includes('pushOutAdd'));
    if (!addTargetBtn) throw new Error('Add Target button not found');
    await fireEvent.click(addTargetBtn);

    const details = Array.from(container.querySelectorAll('details'));
    const overrideDetails = details.find((d) => {
      const summary = d.querySelector('summary');
      return summary?.textContent?.includes('cameras.pushPresetOverrides');
    });
    if (!overrideDetails) throw new Error('Override panel not found');
    const summary = overrideDetails.querySelector('summary');
    if (summary) await fireEvent.click(summary);

    const labels = container.querySelectorAll('label');
    const labelTexts = Array.from(labels).map((l) => l.textContent);
    expect(labelTexts.some((t) => t?.includes('cameras.pushPresetResolution'))).toBeTruthy();
    expect(labelTexts.some((t) => t?.includes('cameras.pushPresetFramerate'))).toBeTruthy();
    expect(labelTexts.some((t) => t?.includes('cameras.pushPresetBitrate'))).toBeTruthy();
    expect(labelTexts.some((t) => t?.includes('cameras.pushPresetGOP'))).toBeTruthy();
    expect(labelTexts.some((t) => t?.includes('cameras.pushPresetProfile'))).toBeTruthy();
    expect(labelTexts.some((t) => t?.includes('cameras.pushPresetBFrames'))).toBeTruthy();
  });

  it('shows "Reset to preset defaults" button', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') {
        return Promise.resolve(mockPresets);
      }
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const addTargetBtn = buttons.find((b) => b.textContent?.includes('pushOutAdd'));
    if (!addTargetBtn) throw new Error('Add Target button not found');
    await fireEvent.click(addTargetBtn);

    const allBtns = container.querySelectorAll('button');
    const resetBtn = Array.from(allBtns).find((b) => b.textContent?.includes('cameras.pushPresetReset'));
    expect(resetBtn).toBeTruthy();
  });

  it('shows "custom" badge when override is set', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') {
        return Promise.resolve(mockPresets);
      }
      return Promise.resolve(null);
    });

    const cameraWithOverride = {
      id: 'cam-ovr',
      name: 'Override Cam',
      protocol: 'rtsp',
      encoding: 'h264',
      url: 'rtsp://192.168.1.100/stream',
      push_targets: [
        {
          id: 'tgt-001',
          name: 'Custom Target',
          protocol: 'rtmp' as const,
          url: 'rtmp://example.com/live/key',
          enabled: true,
          platform: 'youtube',
          transcode_policy: 'auto' as const,
          video_preset_override: { resolution: '1280x720' },
        },
      ],
    };

    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: cameraWithOverride }),
    });

    const details = Array.from(container.querySelectorAll('details'));
    const overrideDetails = details.find((d) => {
      const summary = d.querySelector('summary');
      return summary?.textContent?.includes('cameras.pushPresetOverrides');
    });
    expect(overrideDetails).toBeTruthy();
    const summary = overrideDetails?.querySelector('summary');
    expect(summary?.textContent?.includes('cameras.pushPresetCustom')).toBeTruthy();
  });
});
describe('CameraForm - gb28181 protocol (todo 18)', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  const gb28181Cam = {
    id: 'gb-cam-1',
    name: 'GB28181 Cam',
    protocol: 'gb28181',
    encoding: '',
    url: '',
    gb28181: { device_id: '34020000001310000001', channel_id: '34020000001320000001' },
  };

  it('shows DeviceID/ChannelID fields, hides URL, disables encoding when editing a gb28181 camera', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: gb28181Cam as any }),
    });

    await vi.waitFor(() => {
      const deviceInput = container.querySelector('#cam-gb28181-device-id') as HTMLInputElement | null;
      expect(deviceInput, 'DeviceID input should exist').toBeTruthy();
      expect(deviceInput!.value).toBe('34020000001310000001');
      const channelInput = container.querySelector('#cam-gb28181-channel-id') as HTMLInputElement | null;
      expect(channelInput, 'ChannelID input should exist').toBeTruthy();
      expect(channelInput!.value).toBe('34020000001320000001');
      // URL field hidden for gb28181 (identified by SIP IDs, not a URL)
      expect(container.querySelector('#cam-url'), 'URL field must be hidden for gb28181').toBeNull();
      // Encoding is auto-detect (disabled), like onvif/xiaomi
      const encSelect = container.querySelector('#cam-encoding') as HTMLSelectElement | null;
      expect(encSelect, 'encoding <select> should exist').toBeTruthy();
      expect(encSelect!.disabled, 'encoding field must be disabled for gb28181').toBe(true);
      const opts = Array.from(encSelect!.options);
      expect(opts.length).toBe(1);
      expect(opts[0].value).toBe('');
    });
  });

  it('shows gb28181 fields after selecting the protocol in add mode', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });

    const protocolSelect = container.querySelector('#cam-protocol') as HTMLSelectElement | null;
    expect(protocolSelect, 'protocol <select> should exist').toBeTruthy();
    // GB28181 must be a selectable option
    const gbOption = Array.from(protocolSelect!.options).find((o) => o.value === 'gb28181');
    expect(gbOption, 'gb28181 option should exist in protocol dropdown').toBeTruthy();

    await fireEvent.change(protocolSelect!, { target: { value: 'gb28181' } });

    await vi.waitFor(() => {
      expect(container.querySelector('#cam-gb28181-device-id')).toBeTruthy();
      expect(container.querySelector('#cam-gb28181-channel-id')).toBeTruthy();
      expect(container.querySelector('#cam-url')).toBeNull();
      const encSelect = container.querySelector('#cam-encoding') as HTMLSelectElement | null;
      expect(encSelect!.disabled).toBe(true);
    });
  });

  it('requires DeviceID and ChannelID when gb28181 is selected', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });

    const protocolSelect = container.querySelector('#cam-protocol') as HTMLSelectElement | null;
    await fireEvent.change(protocolSelect!, { target: { value: 'gb28181' } });

    const saveBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('cameras.save'),
    );
    expect(saveBtn).toBeTruthy();
    await fireEvent.click(saveBtn!);

    await vi.waitFor(() => {
      expect(container.querySelector('#cam-gb28181-device-id')?.classList.contains('border-red-500')).toBe(true);
      expect(container.querySelector('#cam-gb28181-channel-id')?.classList.contains('border-red-500')).toBe(true);
    });
    // Save must NOT have been attempted
    expect(mockApiRequest).not.toHaveBeenCalledWith('/cameras', expect.anything());
  });

  it('includes gb28181 in the create payload when protocol is gb28181', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve({ id: 'gb-cam-1', name: 'GB28181 Cam' });
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/relay-presets', expect.any(Object));
    });

    const protocolSelect = container.querySelector('#cam-protocol') as HTMLSelectElement | null;
    await fireEvent.change(protocolSelect!, { target: { value: 'gb28181' } });

    const nameInput = container.querySelector('#cam-name') as HTMLInputElement | null;
    await fireEvent.input(nameInput!, { target: { value: 'GB28181 Cam' } });
    const deviceInput = container.querySelector('#cam-gb28181-device-id') as HTMLInputElement | null;
    await fireEvent.input(deviceInput!, { target: { value: '34020000001310000001' } });
    const channelInput = container.querySelector('#cam-gb28181-channel-id') as HTMLInputElement | null;
    await fireEvent.input(channelInput!, { target: { value: '34020000001320000001' } });

    const saveBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('cameras.save'),
    );
    await fireEvent.click(saveBtn!);

    await vi.waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith(
        '/cameras',
        expect.objectContaining({
          method: 'POST',
          body: expect.stringContaining('gb28181'),
        }),
      );
    });
    const call = mockApiRequest.mock.calls.find((c) => c[0] === '/cameras');
    const body = JSON.parse(call![1].body);
    expect(body.gb28181).toEqual({ device_id: '34020000001310000001', channel_id: '34020000001320000001' });
    expect(body.url).toBe('');
  });
});

describe('CameraForm - group field', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  const groupedCam = {
    id: 'cam-group-1',
    name: 'Studio Cam',
    protocol: 'rtsp',
    encoding: 'h264',
    url: 'rtsp://192.168.1.10/stream',
    group: '室内',
  };

  it('prefills the group input when editing a grouped camera and suggests known groups', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, {
      props: defaultProps({
        editingCamera: groupedCam as any,
        groupSuggestions: ['室内', '室外'],
      }),
    });

    await vi.waitFor(() => {
      const input = container.querySelector('#cam-group') as HTMLInputElement | null;
      expect(input, 'group input should exist').toBeTruthy();
      expect(input!.value).toBe('室内');
    });
    const datalist = container.querySelector('datalist');
    expect(datalist, 'datalist should exist').toBeTruthy();
    const opts = Array.from(datalist!.querySelectorAll('option'));
    expect(opts.map((o) => o.value)).toEqual(['室内', '室外']);
  });

  it('sends group in the update payload (empty string ungroups)', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: groupedCam as any }),
    });

    await vi.waitFor(() => {
      expect((container.querySelector('#cam-group') as HTMLInputElement).value).toBe('室内');
    });
    const input = container.querySelector('#cam-group') as HTMLInputElement;
    await fireEvent.input(input, { target: { value: '' } });

    const saveBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('cameras.save'),
    );
    await fireEvent.click(saveBtn!);

    await vi.waitFor(() => {
      const put = mockApiRequest.mock.calls.find((c) => typeof c[0] === 'string' && c[0] === '/cameras/cam-group-1');
      expect(put, 'PUT /cameras/{id} should have been issued').toBeTruthy();
      const body = JSON.parse((put![1] as RequestInit).body as string);
      expect(body.group).toBe('');
    });
  });

  it('sends group in the create payload and omits it when empty', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });

    await vi.waitFor(() => {
      expect(container.querySelector('#cam-group')).toBeTruthy();
    });
    const nameInput = container.querySelector('#cam-name') as HTMLInputElement;
    await fireEvent.input(nameInput, { target: { value: 'New Cam' } });
    const urlInput = container.querySelector('#cam-url') as HTMLInputElement;
    await fireEvent.input(urlInput, { target: { value: 'rtsp://192.168.1.99/stream' } });
    const groupInput = container.querySelector('#cam-group') as HTMLInputElement;
    await fireEvent.input(groupInput, { target: { value: ' 室外 ' } });

    const saveBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('cameras.save'),
    );
    await fireEvent.click(saveBtn!);

    await vi.waitFor(() => {
      const create = mockApiRequest.mock.calls.find((c) => typeof c[0] === 'string' && c[0] === '/cameras');
      expect(create, 'POST /cameras should have been issued').toBeTruthy();
      const body = JSON.parse((create![1] as RequestInit).body as string);
      expect(body.group).toBe('室外');
    });
  });
});

describe('CameraForm - recording schedule (weekly ranges)', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  const scheduledCam = {
    id: 'cam-sched-1',
    name: 'Study',
    protocol: 'rtsp',
    encoding: 'h264',
    url: 'rtsp://192.168.1.10/stream',
    recording_schedule: {
      time_ranges: [
        { start: '15:00', end: '23:00', days_of_week: [1, 2, 3, 4, 5] },
        { start: '07:00', end: '23:00' },
      ],
      // Schedule-level days apply to the second range (no days of its own).
      days_of_week: [0, 6],
    },
  };

  function mockPresetsOnly() {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });
  }

  async function savedBody(container: HTMLElement, path: string) {
    const saveBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('cameras.save'),
    );
    await fireEvent.click(saveBtn!);
    let body: any;
    await vi.waitFor(() => {
      const call = mockApiRequest.mock.calls.find((c) => typeof c[0] === 'string' && c[0] === path);
      expect(call, `${path} should have been requested`).toBeTruthy();
      body = JSON.parse((call![1] as RequestInit).body as string);
    });
    return body;
  }

  it('renders one row per range and round-trips per-range days', async () => {
    mockPresetsOnly();
    const { container } = render(CameraForm, { props: defaultProps({ editingCamera: scheduledCam as any }) });

    await vi.waitFor(() => {
      expect(container.querySelectorAll('[data-testid="schedule-range"]').length).toBe(2);
    });
    const rows = container.querySelectorAll('[data-testid="schedule-range"]');
    const checked = (row: Element) =>
      Array.from(row.querySelectorAll('input[type="checkbox"]')).filter((c) => (c as HTMLInputElement).checked).length;
    expect(checked(rows[0])).toBe(5);
    expect(checked(rows[1])).toBe(2);

    const body = await savedBody(container, '/cameras/cam-sched-1');
    expect(body.recording_schedule).toEqual({
      time_ranges: [
        { start: '15:00', end: '23:00', days_of_week: [1, 2, 3, 4, 5] },
        { start: '07:00', end: '23:00', days_of_week: [0, 6] },
      ],
    });
  });

  it('adds a range and marks an overnight one', async () => {
    mockPresetsOnly();
    const { container } = render(CameraForm, { props: defaultProps({ editingCamera: scheduledCam as any }) });

    await vi.waitFor(() => {
      expect(container.querySelectorAll('[data-testid="schedule-range"]').length).toBe(2);
    });
    const addBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('cameras.recordingScheduleAddRange'),
    );
    await fireEvent.click(addBtn!);
    const rows = container.querySelectorAll('[data-testid="schedule-range"]');
    expect(rows.length).toBe(3);

    const [start, end] = Array.from(rows[2].querySelectorAll('input[type="time"]')) as HTMLInputElement[];
    await fireEvent.input(start, { target: { value: '22:00' } });
    await fireEvent.input(end, { target: { value: '06:00' } });
    expect(rows[2].textContent).toContain('cameras.recordingScheduleOvernight');

    const body = await savedBody(container, '/cameras/cam-sched-1');
    expect(body.recording_schedule.time_ranges[2]).toEqual({ start: '22:00', end: '06:00' });
  });

  it('sends an empty schedule when unchecked so the backend clears it', async () => {
    mockPresetsOnly();
    const { container } = render(CameraForm, { props: defaultProps({ editingCamera: scheduledCam as any }) });

    await vi.waitFor(() => {
      expect((container.querySelector('#cam-rec-schedule') as HTMLInputElement).checked).toBe(true);
    });
    await fireEvent.click(container.querySelector('#cam-rec-schedule')!);

    const body = await savedBody(container, '/cameras/cam-sched-1');
    expect(body.recording_schedule).toEqual({ time_ranges: [] });
  });
});

describe('CameraForm - RTSP audio probe display (#964)', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  function mockProbeResponse(result: Record<string, unknown>) {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      if (path === '/cameras/test-connection') return Promise.resolve(result);
      return Promise.resolve(null);
    });
  }

  const audioCam = {
    id: 'mic-test',
    name: 'Room mic',
    protocol: 'rtsp',
    encoding: 'audio',
    url: 'rtsp://example/stream',
    enabled: true,
  };

  async function clickTestButton(container: HTMLElement) {
    const btn = await vi.waitFor(() => {
      const b = container.querySelector('button[title="cameras.testConnection"]') as HTMLButtonElement;
      expect(b).toBeTruthy();
      return b;
    });
    await fireEvent.click(btn);
  }

  it('shows the probed audio codec after a successful test connection', async () => {
    mockProbeResponse({
      success: true,
      message: 'audio source ready: PCMA 8000Hz',
      latency_ms: 12,
      reachable: true,
      stream_ok: true,
      has_audio: true,
      audio_supported: true,
      audio: 'PCMA 8000Hz',
    });
    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: audioCam as any }),
    });
    await clickTestButton(container);
    await vi.waitFor(() => {
      // t() is mocked to echo the key (params are dropped), so the key
      // itself proves the probe line rendered; real interpolation is
      // covered by the i18n locale tests.
      expect(container.textContent).toContain('cameras.testAudioFound');
    });
  });

  it('renders no audio line when the stream carries no audio info', async () => {
    mockProbeResponse({
      success: true,
      message: 'connection successful',
      latency_ms: 8,
      reachable: true,
      stream_ok: true,
    });
    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: { ...audioCam, encoding: 'h264' } as any }),
    });
    await clickTestButton(container);
    await vi.waitFor(() => {
      expect(container.textContent).toContain('cameras.testSuccess');
    });
    expect(container.textContent).not.toContain('cameras.testAudioFound');
  });
});

describe('CameraForm - audio pickup variant (GB voice channel)', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
    vi.unstubAllGlobals();
  });

  const gbMic = {
    id: 'gb-34020000001360000003',
    name: 'Hall Pickup',
    protocol: 'gb28181',
    encoding: 'audio',
    url: '',
    group: '',
    gb28181: { device_id: '34020000001320000001', channel_id: '34020000001360000003' },
  };

  it('shows the audio-link select for a GB audio channel (not just rtsp mics)', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });
    // listCameras goes through raw fetch (ETag caching), not apiRequest.
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      headers: { get: () => null },
      text: async () =>
        JSON.stringify([
          { id: 'cam-1', name: 'Studio Cam', encoding: 'h264' },
          { id: 'gb-other-mic', name: 'Other Mic', encoding: 'audio' },
        ]),
    });
    vi.stubGlobal('fetch', fetchMock);

    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: gbMic as any }),
    });

    await vi.waitFor(() => {
      // The candidates fetch must actually run and populate the select.
      expect(fetchMock).toHaveBeenCalled();
      expect(container.querySelectorAll('#cam-audio-link option').length).toBe(2);
    });
    const opts = Array.from(container.querySelectorAll('#cam-audio-link option'));
    // Other audio devices are not link candidates; only the video camera is.
    expect(opts.map((o) => (o as HTMLOptionElement).value)).toEqual(['', 'cam-1']);
  });

  it('hides the video audio toggles for an audio-source device', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: gbMic as any }),
    });

    await vi.waitFor(() => {
      expect(container.querySelector('#cam-encoding')).toBeTruthy();
    });
    expect(container.querySelector('#cam-audio')).toBeNull();
    expect(container.querySelector('#cam-audio-in-recordings')).toBeNull();
  });

  it('keeps the cascade catalog toggle but hides sub-stream forwarding for audio', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: gbMic as any }),
    });

    await vi.waitFor(() => {
      expect(container.querySelector('#cam-cascade')).toBeTruthy();
    });
    expect(container.querySelector('#cam-cascade-sub')).toBeNull();
  });
});

describe('CameraForm - cascade catalog boundary (#1037)', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
    vi.unstubAllGlobals();
  });

  const gbCam = {
    id: 'gb-34020000001329990001',
    name: 'GB cam',
    protocol: 'gb28181',
    encoding: '',
    url: '',
    group: '',
    gb28181: { device_id: '34020000001329990001', channel_id: '34020000001329990002' },
  };
  const onvifCam = {
    id: 'cam-onvif',
    name: 'ONVIF cam',
    protocol: 'onvif',
    encoding: 'h265',
    url: 'http://192.168.1.10/onvif/device_service',
    group: '',
  };

  async function renderCascade(cam: any) {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });
    const { container } = render(CameraForm, {
      props: defaultProps({ editingCamera: cam }),
    });
    await vi.waitFor(() => {
      expect(container.querySelector('#cam-cascade')).toBeTruthy();
    });
    return container.querySelector('#cam-cascade') as HTMLInputElement;
  }

  it('defaults the toggle on for a GB camera and off for a non-GB camera', async () => {
    expect((await renderCascade(gbCam)).checked).toBe(true);
    cleanup();
    expect((await renderCascade(onvifCam)).checked).toBe(false);
  });

  it('keeps an explicit cascade_enabled choice regardless of protocol', async () => {
    expect((await renderCascade({ ...onvifCam, cascade_enabled: true })).checked).toBe(true);
    cleanup();
    expect((await renderCascade({ ...gbCam, cascade_enabled: false })).checked).toBe(false);
  });

  it('follows protocol switches with the default until the user touches the toggle', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });
    const { container } = render(CameraForm, { props: defaultProps() });
    await vi.waitFor(() => {
      expect(container.querySelector('#cam-cascade')).toBeTruthy();
    });
    const cb = container.querySelector('#cam-cascade') as HTMLInputElement;
    const sel = container.querySelector('#cam-protocol') as HTMLSelectElement;
    expect(cb.checked).toBe(false);

    sel.value = 'gb28181';
    await fireEvent.change(sel);
    expect(cb.checked).toBe(true);

    // An explicit toggle pins the choice; protocol switches stop overriding.
    await fireEvent.change(cb, { target: { checked: false } });
    sel.value = 'onvif';
    await fireEvent.change(sel);
    expect(cb.checked).toBe(false);
    sel.value = 'gb28181';
    await fireEvent.change(sel);
    expect(cb.checked).toBe(false);
  });
});

describe('CameraForm - thermal imaging channel', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  function mockBase() {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      return Promise.resolve(null);
    });
  }

  async function fillMinimalCreate(container: HTMLElement) {
    const nameInput = container.querySelector('#cam-name') as HTMLInputElement;
    await fireEvent.input(nameInput, { target: { value: 'Dual Thermal' } });
    const urlInput = container.querySelector('#cam-url') as HTMLInputElement;
    await fireEvent.input(urlInput, { target: { value: 'rtsp://192.0.2.10/thermal' } });
  }

  it('defaults to visible and offers the thermal option', async () => {
    mockBase();
    const { container } = render(CameraForm, { props: defaultProps() });
    await vi.waitFor(() => {
      expect(container.querySelector('#cam-imaging')).toBeTruthy();
    });
    const sel = container.querySelector('#cam-imaging') as HTMLSelectElement;
    expect(sel.value).toBe('visible');
    const opts = Array.from(sel.options).map((o) => o.value);
    expect(opts).toContain('thermal');
    // No thermal hint in visible mode.
    expect(container.textContent).not.toContain('cameras.thermalHint');
  });

  it('selecting thermal shows the policy hint and sends imaging_type on create', async () => {
    mockBase();
    const { container } = render(CameraForm, { props: defaultProps() });
    await vi.waitFor(() => {
      expect(container.querySelector('#cam-imaging')).toBeTruthy();
    });
    await fillMinimalCreate(container);

    const sel = container.querySelector('#cam-imaging') as HTMLSelectElement;
    await fireEvent.change(sel, { target: { value: 'thermal' } });
    expect(container.textContent).toContain('cameras.thermalHint');

    const saveBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('cameras.save'),
    );
    await fireEvent.click(saveBtn!);

    await vi.waitFor(() => {
      const create = mockApiRequest.mock.calls.find((c) => typeof c[0] === 'string' && c[0] === '/cameras');
      expect(create, 'POST /cameras should have been issued').toBeTruthy();
      const body = JSON.parse(create![1].body as string);
      expect(body.imaging_type).toBe('thermal');
    });
  });

  it('warns when thermal is combined with adaptive recording', async () => {
    mockBase();
    const { container } = render(CameraForm, { props: defaultProps() });
    await vi.waitFor(() => {
      expect(container.querySelector('#cam-imaging')).toBeTruthy();
    });
    await fillMinimalCreate(container);

    // thermal + continuous: no warning yet.
    await fireEvent.change(container.querySelector('#cam-imaging')!, { target: { value: 'thermal' } });
    expect(container.textContent).not.toContain('cameras.thermalAdaptiveWarn');

    // Switch to adaptive: the shutter warning appears.
    await fireEvent.change(container.querySelector('#cam-recording-mode')!, {
      target: { value: 'adaptive' },
    });
    expect(container.textContent).toContain('cameras.thermalAdaptiveWarn');
  });
});

describe('CameraForm - ONVIF main profile picker', () => {
  afterEach(() => {
    cleanup();
    mockApiRequest.mockReset();
  });

  const dualSpectrumProfiles = {
    profiles: [
      { token: 'Profile_main', name: 'Main', encoding: 'H264', width: 2560, height: 1440 },
      { token: 'Profile_thermal', name: 'Thermal', encoding: 'H264', width: 384, height: 288 },
    ],
    capabilities: { ptz: false, streaming: true },
  };

  it('populates from the device after a test connection and sends profile_token on create', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      if (path === '/cameras/test-connection')
        return Promise.resolve({
          success: true,
          stream_ok: true,
          message: 'ok',
          latency_ms: 12,
          profiles: dualSpectrumProfiles.profiles,
          auto_main_token: 'Profile_main',
        });
      return Promise.resolve(null);
    });

    const { container } = render(CameraForm, { props: defaultProps() });
    await vi.waitFor(() => {
      expect(container.querySelector('#cam-url')).toBeTruthy();
    });

    // ONVIF protocol + endpoint, then run the test connection.
    await fireEvent.change(container.querySelector('#cam-protocol')!, { target: { value: 'onvif' } });
    const nameInput = container.querySelector('#cam-name') as HTMLInputElement;
    await fireEvent.input(nameInput, { target: { value: 'Dual Spectrum' } });
    const urlInput = container.querySelector('#cam-url') as HTMLInputElement;
    await fireEvent.input(urlInput, { target: { value: 'http://192.0.2.10/onvif/device_service' } });

    // Main profile select exists but has no device options before probing.
    const mainSel = container.querySelector('#cam-main-token') as HTMLSelectElement;
    expect(mainSel).toBeTruthy();
    expect(Array.from(mainSel.options).some((o) => o.value === 'Profile_thermal')).toBe(false);

    const testBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('cameras.testConnection'),
    );
    await fireEvent.click(testBtn!);

    await vi.waitFor(() => {
      const sel = container.querySelector('#cam-main-token') as HTMLSelectElement;
      expect(Array.from(sel.options).some((o) => o.value === 'Profile_thermal')).toBe(true);
    });

    // Pick the thermal channel and save: profile_token must ride the create.
    await fireEvent.change(container.querySelector('#cam-main-token')!, {
      target: { value: 'Profile_thermal' },
    });
    const saveBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('cameras.save'),
    );
    await fireEvent.click(saveBtn!);

    await vi.waitFor(() => {
      const create = mockApiRequest.mock.calls.find((c) => typeof c[0] === 'string' && c[0] === '/cameras');
      expect(create, 'POST /cameras should have been issued').toBeTruthy();
      const body = JSON.parse(create![1].body as string);
      expect(body.profile_token).toBe('Profile_thermal');
      expect(body.imaging_type).toBe('visible'); // independent knob
    });
  });

  it('edit mode loads profiles from the registered camera and keeps a manual token visible', async () => {
    mockApiRequest.mockImplementation((path: string) => {
      if (path === '/relay-presets') return Promise.resolve(mockPresets);
      if (path === '/cameras/cam-dual/onvif/profiles') return Promise.resolve(dualSpectrumProfiles);
      return Promise.resolve(null);
    });

    const editing = {
      id: 'cam-dual',
      name: 'Dual',
      protocol: 'onvif',
      url: 'http://192.0.2.10/onvif/device_service',
      profile_token: 'Profile_thermal',
      imaging_type: 'thermal',
    };
    const { container } = render(CameraForm, { props: defaultProps({ editingCamera: editing }) });

    await vi.waitFor(() => {
      const sel = container.querySelector('#cam-main-token') as HTMLSelectElement;
      expect(sel).toBeTruthy();
      expect(sel.value).toBe('Profile_thermal');
      expect(Array.from(sel.options).some((o) => o.value === 'Profile_main')).toBe(true);
    });
    // Thermal label preselected.
    expect((container.querySelector('#cam-imaging') as HTMLSelectElement).value).toBe('thermal');
  });
});
