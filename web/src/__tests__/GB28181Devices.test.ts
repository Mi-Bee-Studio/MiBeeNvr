import { render, cleanup, fireEvent } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import GB28181Devices from '$lib/../routes/GB28181Devices.svelte';
import type { GB28181Device, GB28181Channel } from '$lib/api';

// Mock lucide-svelte icons
vi.mock('lucide-svelte', () => {
  const icons = ['AlertCircle', 'ChevronDown', 'RefreshCw', 'Settings', 'Video', 'VideoOff'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = () => document.createElement('span');
  }
  return mock;
});

// Mock i18n — return the key itself so assertions can check key presence
vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

// Mock formatDate
vi.mock('$lib/format', () => ({
  formatDate: () => '2026-01-01 00:00:00',
}));

// Mock toast
vi.mock('$lib/toast', () => ({
  showToast: vi.fn(),
}));

// Mock GB28181 status store
const statusMock = vi.hoisted(() => ({
  enabled: true,
  loaded: true,
  refreshGB28181Status: vi.fn().mockResolvedValue(undefined),
}));

vi.mock('$lib/gb28181-status', () => ({
  getGB28181Enabled: () => statusMock.enabled,
  getGB28181Loaded: () => statusMock.loaded,
  refreshGB28181Status: statusMock.refreshGB28181Status,
}));

// Mock API module
const apiMock = vi.hoisted(() => ({
  listGB28181Devices: vi.fn(),
  listGB28181Channels: vi.fn(),
  catalogRefreshGB28181: vi.fn(),
  inviteGB28181Channel: vi.fn(),
  byeGB28181Channel: vi.fn(),
}));

vi.mock('$lib/api', () => apiMock);

function makeDevice(overrides: Partial<GB28181Device> = {}): GB28181Device {
  return {
    ID: '34020000001320000001',
    Name: 'Front Gate Camera',
    Manufacturer: 'Hikvision',
    Model: 'DS-2CD2042WD-I',
    Status: 'online',
    LastKeepalive: '2026-01-01T00:00:00Z',
    RegisteredAt: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function makeChannel(overrides: Partial<GB28181Channel> = {}): GB28181Channel {
  return {
    ID: '34020000001320000002',
    DeviceID: '34020000001320000001',
    Name: 'Channel 1',
    Manufacturer: 'Hikvision',
    Parental: 0,
    Status: 'idle',
    CameraID: '',
    UpdatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

describe('GB28181Devices', () => {
  beforeEach(() => {
    statusMock.enabled = true;
    statusMock.loaded = true;
    statusMock.refreshGB28181Status.mockClear();
    apiMock.listGB28181Devices.mockReset();
    apiMock.listGB28181Channels.mockReset();
    apiMock.catalogRefreshGB28181.mockReset();
    apiMock.inviteGB28181Channel.mockReset();
    apiMock.byeGB28181Channel.mockReset();
  });

  afterEach(() => cleanup());

  it('renders device list when GB28181 is enabled', async () => {
    apiMock.listGB28181Devices.mockResolvedValue([makeDevice()]);
    const { container } = render(GB28181Devices);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('Front Gate Camera');
    });
    expect(container.textContent).toContain('Hikvision');
    expect(container.textContent).toContain('DS-2CD2042WD-I');
    expect(container.textContent).toContain('gb28181Devices.deviceStatus.online');
  });

  it('shows empty state when no devices are registered', async () => {
    apiMock.listGB28181Devices.mockResolvedValue([]);
    const { container } = render(GB28181Devices);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('gb28181Devices.empty');
    });
  });

  it('shows not-enabled state when GB28181 server is disabled', async () => {
    statusMock.enabled = false;
    const { container } = render(GB28181Devices);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('gb28181Devices.notEnabled');
    });
    expect(apiMock.listGB28181Devices).not.toHaveBeenCalled();
  });

  it('expands a device to load and show its channels', async () => {
    apiMock.listGB28181Devices.mockResolvedValue([makeDevice()]);
    apiMock.listGB28181Channels.mockResolvedValue([makeChannel()]);
    const { container } = render(GB28181Devices);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('Front Gate Camera');
    });

    const header = container.querySelector('.device-header');
    expect(header).toBeTruthy();
    await fireEvent.click(header as Element);

    await vi.waitFor(() => {
      expect(apiMock.listGB28181Channels).toHaveBeenCalledWith('34020000001320000001');
      expect(container.textContent).toContain('Channel 1');
    });
    expect(container.textContent).toContain('gb28181Devices.channelStatus.idle');
  });

  it('shows no-channels message when device reports none', async () => {
    apiMock.listGB28181Devices.mockResolvedValue([makeDevice()]);
    apiMock.listGB28181Channels.mockResolvedValue([]);
    const { container } = render(GB28181Devices);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('Front Gate Camera');
    });

    const header = container.querySelector('.device-header');
    await fireEvent.click(header as Element);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('gb28181Devices.noChannels');
    });
  });

  it('invites a channel via the invite button', async () => {
    apiMock.listGB28181Devices.mockResolvedValue([makeDevice()]);
    apiMock.listGB28181Channels.mockResolvedValue([makeChannel()]);
    apiMock.inviteGB28181Channel.mockResolvedValue({ status: 'ok' });
    const { container } = render(GB28181Devices);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('Front Gate Camera');
    });

    const header = container.querySelector('.device-header');
    await fireEvent.click(header as Element);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('Channel 1');
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const inviteBtn = buttons.find((b) => b.textContent?.includes('gb28181Devices.invite'));
    expect(inviteBtn).toBeTruthy();
    await fireEvent.click(inviteBtn as Element);

    await vi.waitFor(() => {
      expect(apiMock.inviteGB28181Channel).toHaveBeenCalledWith('34020000001320000002');
    });
  });

  it('disconnects a channel via the bye button', async () => {
    apiMock.listGB28181Devices.mockResolvedValue([makeDevice()]);
    apiMock.listGB28181Channels.mockResolvedValue([makeChannel({ Status: 'playing' })]);
    apiMock.byeGB28181Channel.mockResolvedValue({ status: 'ok' });
    const { container } = render(GB28181Devices);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('Front Gate Camera');
    });

    const header = container.querySelector('.device-header');
    await fireEvent.click(header as Element);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('Channel 1');
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const byeBtn = buttons.find((b) => b.textContent?.includes('gb28181Devices.bye'));
    expect(byeBtn).toBeTruthy();
    await fireEvent.click(byeBtn as Element);

    await vi.waitFor(() => {
      expect(apiMock.byeGB28181Channel).toHaveBeenCalledWith('34020000001320000002');
    });
  });

  it('requests a catalog refresh from the device header', async () => {
    apiMock.listGB28181Devices.mockResolvedValue([makeDevice()]);
    apiMock.catalogRefreshGB28181.mockResolvedValue({ status: 'ok' });
    const { container } = render(GB28181Devices);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('Front Gate Camera');
    });

    const buttons = Array.from(container.querySelectorAll('button'));
    const refreshBtn = buttons.find((b) => b.textContent?.includes('gb28181Devices.catalogRefresh'));
    expect(refreshBtn).toBeTruthy();
    await fireEvent.click(refreshBtn as Element);

    await vi.waitFor(() => {
      expect(apiMock.catalogRefreshGB28181).toHaveBeenCalledWith('34020000001320000001');
    });
  });

  it('shows error state when device list fails to load', async () => {
    apiMock.listGB28181Devices.mockRejectedValue(new Error('boom'));
    const { container } = render(GB28181Devices);

    await vi.waitFor(() => {
      expect(container.textContent).toContain('boom');
    });
  });
});