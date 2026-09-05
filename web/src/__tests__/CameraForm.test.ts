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
  const icons = ['Eye', 'EyeOff', 'PlugZap', 'Plus', 'Trash2', 'ArrowUpRight', 'Copy', 'Layers', 'Brain'];
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
      const create = mockApiRequest.mock.calls.find(
        (c) => typeof c[0] === 'string' && c[0] === '/cameras',
      );
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
      b.textContent?.includes('cameras.save')
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
      b.textContent?.includes('cameras.save')
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
