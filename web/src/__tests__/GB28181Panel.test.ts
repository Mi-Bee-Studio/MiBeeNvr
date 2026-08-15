import { render, cleanup, fireEvent } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import GB28181Panel from '$lib/../routes/settings/GB28181Panel.svelte';
import { settingsForm } from '$lib/settings/settings-form.svelte';
import type { SettingsConfig } from '$lib/api';

// --- Mock i18n ---
vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

// --- Mock lucide-svelte icons ---
vi.mock('lucide-svelte', () => {
  const icons = ['Plus', 'X', 'ChevronDown'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = () => document.createElement('span');
  }
  return mock;
});

// --- Mock toast ---
vi.mock('$lib/toast', () => ({
  showToast: vi.fn(),
}));

// --- Mock API functions called by the panel ---
const mockGetSettings = vi.hoisted(() => vi.fn());
const mockUpdateSettings = vi.hoisted(() => vi.fn());

vi.mock('$lib/api', async () => {
  const actual = await vi.importActual('$lib/api');
  return {
    ...actual,
    getSettings: mockGetSettings,
    updateSettings: mockUpdateSettings,
  };
});

const sampleConfig: SettingsConfig = {
  cleanup: { retention_days: 7, disk_threshold_percent: 90 },
  webdav: { enabled: false, path_prefix: '/webdav', read_write: false },
  gb28181: {
    enabled: true,
    sip_listen: ':5060',
    server_id: '34020000002000000001',
    realm: '34020000002000000001',
    password_configured: true,
    port_range: '30000-30050',
    heartbeat_interval: '60s',
    catalog_interval: '30m',
    tcp_mode: true,
    tcp_framing: 'rfc4571',
    allowed_device_ids: ['34020000001310000001', '34020000001310000002'],
  },
};

async function renderPanel(config: SettingsConfig = sampleConfig) {
  mockGetSettings.mockResolvedValue(config);
  mockUpdateSettings.mockResolvedValue({ status: 'ok' });
  const result = render(GB28181Panel);
  // Wait for the async load to finish (loading skeleton → SettingsCard).
  await vi.waitFor(() => {
    expect(result.container.querySelector('.card button')).toBeTruthy();
  });
  // SettingsCard starts collapsed — expand it so the fields render.
  const header = result.container.querySelector('.card button') as HTMLButtonElement;
  await fireEvent.click(header);
  await vi.waitFor(() => {
    expect(result.container.querySelector('#gb28181-sip-listen')).toBeTruthy();
  });
  return result;
}

describe('GB28181Panel', () => {
  beforeEach(() => {
    settingsForm.clear();
    mockGetSettings.mockReset();
    mockUpdateSettings.mockReset();
  });

  afterEach(() => cleanup());

  it('renders fields bound to the sample config', async () => {
    const { container } = await renderPanel();

    const sipListen = container.querySelector('#gb28181-sip-listen') as HTMLInputElement;
    expect(sipListen.value).toBe(':5060');

    const serverId = container.querySelector('#gb28181-server-id') as HTMLInputElement;
    expect(serverId.value).toBe('34020000002000000001');

    const realm = container.querySelector('#gb28181-realm') as HTMLInputElement;
    expect(realm.value).toBe('34020000002000000001');

    const password = container.querySelector('#gb28181-password') as HTMLInputElement;
    expect(password.value).toBe('');

    const portRange = container.querySelector('#gb28181-port-range') as HTMLInputElement;
    expect(portRange.value).toBe('30000-30050');

    const heartbeat = container.querySelector('#gb28181-heartbeat') as HTMLInputElement;
    expect(heartbeat.value).toBe('60s');

    const catalog = container.querySelector('#gb28181-catalog') as HTMLInputElement;
    expect(catalog.value).toBe('30m');

    const framing = container.querySelector('#gb28181-tcp-framing') as HTMLSelectElement;
    expect(framing.value).toBe('rfc4571');

    // Enabled toggle reflects the config; the legacy TCP mode toggle is now
    // the media transport select (#338).
    const switches = container.querySelectorAll('[role="switch"]');
    expect(switches.length).toBe(1);
    expect(switches[0].getAttribute('aria-checked')).toBe('true');
    const transport = [...container.querySelectorAll('select')].find((s) =>
      [...s.options].some((o) => (o as HTMLOptionElement).value === 'tcp-passive'),
    ) as HTMLSelectElement;
    expect(transport.value).toBe('tcp-passive');

    // Allowed device chips render.
    expect(container.textContent).toContain('34020000001310000001');
    expect(container.textContent).toContain('34020000001310000002');
  });

  it('binds user edits back to state', async () => {
    const { container } = await renderPanel();

    const sipListen = container.querySelector('#gb28181-sip-listen') as HTMLInputElement;
    await fireEvent.input(sipListen, { target: { value: ':6060' } });
    expect(sipListen.value).toBe(':6060');
    expect(settingsForm.isDirty('gb28181')).toBe(true);

    const serverId = container.querySelector('#gb28181-server-id') as HTMLInputElement;
    await fireEvent.input(serverId, { target: { value: '34020000002000000002' } });
    expect(serverId.value).toBe('34020000002000000002');
  });

  it('save sends the gb28181 block via updateSettings', async () => {
    await renderPanel();

    await settingsForm.panels.get('gb28181')!.save();

    expect(mockUpdateSettings).toHaveBeenCalledTimes(1);
    const body = mockUpdateSettings.mock.calls[0][0];
    expect(body.gb28181).toEqual({
      enabled: true,
      sip_listen: ':5060',
      server_id: '34020000002000000001',
      realm: '34020000002000000001',
      password: '',
      port_range: '30000-30050',
      heartbeat_interval: '60s',
      catalog_interval: '30m',
      media_transport: 'tcp-passive',
      tcp_framing: 'rfc4571',
      subscribe_catalog: true,
      subscribe_alarm: true,
      subscribe_mobile_position: false,
      subscribe_expires: '3600s',
      allowed_device_ids: ['34020000001310000001', '34020000001310000002'],
    });
  });

  it('blocks save when enabled with an empty server_id', async () => {
    const { container } = await renderPanel();

    const serverId = container.querySelector('#gb28181-server-id') as HTMLInputElement;
    await fireEvent.input(serverId, { target: { value: '' } });

    await expect(settingsForm.panels.get('gb28181')!.save()).rejects.toThrow('settings.gb28181.validationServerId');
    expect(mockUpdateSettings).not.toHaveBeenCalled();
  });

  it('blocks save when enabled with an empty sip_listen', async () => {
    const { container } = await renderPanel();

    const sipListen = container.querySelector('#gb28181-sip-listen') as HTMLInputElement;
    await fireEvent.input(sipListen, { target: { value: '' } });

    await expect(settingsForm.panels.get('gb28181')!.save()).rejects.toThrow('settings.gb28181.validationSipListen');
    expect(mockUpdateSettings).not.toHaveBeenCalled();
  });

  it('does not require server_id when disabled', async () => {
    const disabledConfig: SettingsConfig = {
      ...sampleConfig,
      gb28181: { ...sampleConfig.gb28181!, enabled: false, server_id: '' },
    };
    mockGetSettings.mockResolvedValue(disabledConfig);
    mockUpdateSettings.mockResolvedValue({ status: 'ok' });
    const { container } = render(GB28181Panel);
    // Disabled config renders no fields — just wait for the card, expand it.
    await vi.waitFor(() => {
      expect(container.querySelector('.card button')).toBeTruthy();
    });
    const header = container.querySelector('.card button') as HTMLButtonElement;
    await fireEvent.click(header);

    await settingsForm.panels.get('gb28181')!.save();

    expect(mockUpdateSettings).toHaveBeenCalledTimes(1);
    expect(mockUpdateSettings.mock.calls[0][0].gb28181.enabled).toBe(false);
  });

  it('allowed device editor adds and removes ids', async () => {
    const { container } = await renderPanel();

    // Add a new device via the tag input.
    const input = container.querySelector(
      'input[placeholder="settings.gb28181.allowedDevicesPlaceholder"]',
    ) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: '34020000001310000003' } });
    const addBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('settings.gb28181.allowedDevicesAdd'),
    );
    expect(addBtn).toBeTruthy();
    await fireEvent.click(addBtn!);

    expect(container.textContent).toContain('34020000001310000003');
    expect(settingsForm.isDirty('gb28181')).toBe(true);

    // Remove an existing device via its chip X button.
    const removeBtn = Array.from(container.querySelectorAll('button')).find(
      (b) => b.getAttribute('aria-label') === 'settings.gb28181.allowedDevicesRemove',
    );
    expect(removeBtn).toBeTruthy();
    await fireEvent.click(removeBtn!);

    expect(container.textContent).not.toContain('34020000001310000001');
  });
});
