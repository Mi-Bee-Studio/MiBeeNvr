import { render, cleanup, fireEvent } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import DoorbellPanel from '$lib/../routes/settings/DoorbellPanel.svelte';
import { settingsForm } from '$lib/settings/settings-form.svelte';
import { showToast } from '$lib/toast.svelte';
import type { SettingsConfig } from '$lib/api';

// --- Mock i18n ---
vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

// --- Mock lucide-svelte icons ---
vi.mock('lucide-svelte', () => {
  const icons = ['Plus', 'Trash2', 'ChevronDown'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = () => document.createElement('span');
  }
  return mock;
});

// --- Mock toast ---
vi.mock('$lib/toast.svelte', () => ({
  showToast: vi.fn(),
}));

// --- Mock API functions called by the panel ---
const mockGetSettings = vi.hoisted(() => vi.fn());
const mockUpdateSettings = vi.hoisted(() => vi.fn());
const mockGetDoorbellStatus = vi.hoisted(() => vi.fn());
const mockListCamerasSummary = vi.hoisted(() => vi.fn());

vi.mock('$lib/api', async () => {
  const actual = await vi.importActual('$lib/api');
  return {
    ...actual,
    getSettings: mockGetSettings,
    updateSettings: mockUpdateSettings,
    getDoorbellStatus: mockGetDoorbellStatus,
    listCamerasSummary: mockListCamerasSummary,
  };
});

const sampleConfig: SettingsConfig = {
  cleanup: { retention_days: 7, disk_threshold_percent: 90 },
  webdav: { enabled: false, path_prefix: '/webdav', read_write: false },
  doorbell: {
    enabled: true,
    listen: ':5070',
    realm: 'mibee-nvr',
    transport: 'udp',
    lan_only: true,
    ring_timeout: '60s',
    users: [
      { username: 'door1', name: 'Front door', camera_id: 'cam-1', secret_configured: true },
      { username: 'door2', name: 'Gate', camera_id: '', secret_configured: false },
    ],
    outbound: {
      server: 'pbx.example.com:5060',
      username: 'nvr',
      expires: '300s',
      secret_configured: true,
    },
  },
};

async function renderPanel(
  config: SettingsConfig = sampleConfig,
  status = { enabled: true, running: true, registered: [] as { user: string; source: string; expires_at: string }[] },
) {
  mockGetSettings.mockResolvedValue(config);
  mockUpdateSettings.mockResolvedValue({ status: 'updated', restart_required: true });
  mockGetDoorbellStatus.mockResolvedValue(status);
  mockListCamerasSummary.mockResolvedValue([
    { id: 'cam-1', name: 'Front cam', status: 'online', is_recording: true },
  ]);
  const result = render(DoorbellPanel);
  await vi.waitFor(() => {
    expect(result.container.querySelector('.card button')).toBeTruthy();
  });
  const header = result.container.querySelector('.card button') as HTMLButtonElement;
  await fireEvent.click(header);
  await vi.waitFor(() => {
    expect(result.container.querySelector('#doorbell-listen')).toBeTruthy();
  });
  return result;
}

function userField(container: HTMLElement, suffix: string): HTMLInputElement {
  return container.querySelector(`[id^="doorbell-user-"][id$="-${suffix}"]`) as HTMLInputElement;
}

describe('DoorbellPanel', () => {
  beforeEach(() => {
    settingsForm.clear();
    mockGetSettings.mockReset();
    mockUpdateSettings.mockReset();
    mockGetDoorbellStatus.mockReset();
    mockListCamerasSummary.mockReset();
    vi.mocked(showToast).mockClear();
  });

  afterEach(() => cleanup());

  it('renders fields bound to the sample config', async () => {
    const { container } = await renderPanel();

    const listen = container.querySelector('#doorbell-listen') as HTMLInputElement;
    expect(listen.value).toBe(':5070');

    const realm = container.querySelector('#doorbell-realm') as HTMLInputElement;
    expect(realm.value).toBe('mibee-nvr');

    const ringTimeout = container.querySelector('#doorbell-ring-timeout') as HTMLInputElement;
    expect(ringTimeout.value).toBe('60s');

    // Door station rows render with values and masked secrets.
    const usernames = container.querySelectorAll('[id^="doorbell-user-"][id$="-username"]');
    expect(usernames.length).toBe(2);
    expect(userField(container, 'username').value).toBe('door1');
    expect(userField(container, 'secret').value).toBe('');
    expect(userField(container, 'secret').placeholder).toBe('********');

    // Outbound (mode B) fields render.
    const server = container.querySelector('#doorbell-outbound-server') as HTMLInputElement;
    expect(server.value).toBe('pbx.example.com:5060');

    // The linked-camera dropdown carries the camera list.
    const cameraSelect = container.querySelector('[id$="-camera"]') as HTMLSelectElement;
    expect([...cameraSelect.options].some((o) => o.value === 'cam-1')).toBe(true);

    // Runtime status strip: running.
    expect(container.textContent).toContain('settings.doorbell.statusRunning');
  });

  it('shows the pending-restart state when enabled but not running', async () => {
    const { container } = await renderPanel(sampleConfig, {
      enabled: true,
      running: false,
      registered: [],
    });
    expect(container.textContent).toContain('settings.doorbell.statusRestartPending');
  });

  it('binds user edits back to state', async () => {
    const { container } = await renderPanel();

    const listen = container.querySelector('#doorbell-listen') as HTMLInputElement;
    await fireEvent.input(listen, { target: { value: ':5080' } });
    expect(listen.value).toBe(':5080');
    expect(settingsForm.isDirty('doorbell')).toBe(true);
  });

  it('save sends the doorbell block via updateSettings and surfaces restart_required', async () => {
    const { container } = await renderPanel();
    // Baseline: enabled config + running service.
    expect(container.textContent).toContain('settings.doorbell.statusRunning');

    await settingsForm.panels.get('doorbell')!.save();

    expect(mockUpdateSettings).toHaveBeenCalledTimes(1);
    const body = mockUpdateSettings.mock.calls[0][0];
    expect(body.doorbell).toEqual({
      enabled: true,
      listen: ':5070',
      realm: 'mibee-nvr',
      transport: 'udp',
      tls_cert: '',
      tls_key: '',
      lan_only: true,
      ring_timeout: '60s',
      users: [
        { username: 'door1', name: 'Front door', camera_id: 'cam-1', secret: '' },
        { username: 'door2', name: 'Gate', camera_id: '', secret: '' },
      ],
      outbound: {
        enabled: true,
        server: 'pbx.example.com:5060',
        username: 'nvr',
        secret: '',
        expires: '300s',
      },
    });

    // restart_required from the API becomes an explicit toast, and the
    // status strip flips to pending even though the (mocked) service is
    // still running — the disable/retune directions need that.
    const messages = vi.mocked(showToast).mock.calls.map((c) => c[0]);
    expect(messages).toContain('settings.doorbell.restartRequired');
    expect(container.textContent).toContain('settings.doorbell.statusRestartPending');
    expect(container.textContent).not.toContain('settings.doorbell.statusRunning');
  });

  it('blocks save when enabled with no users and no outbound', async () => {
    const config: SettingsConfig = {
      ...sampleConfig,
      doorbell: {
        ...sampleConfig.doorbell!,
        users: [],
        outbound: null,
      },
    };
    const { container } = await renderPanel(config);
    expect(container.querySelector('#doorbell-listen')).toBeTruthy();

    await expect(settingsForm.panels.get('doorbell')!.save()).rejects.toThrow(
      'settings.doorbell.validationUsersRequired',
    );
    expect(mockUpdateSettings).not.toHaveBeenCalled();
  });

  it('blocks save on duplicate usernames', async () => {
    const { container } = await renderPanel();

    await fireEvent.input(userField(container, 'username'), { target: { value: 'door2' } });

    await expect(settingsForm.panels.get('doorbell')!.save()).rejects.toThrow(
      'settings.doorbell.validationUsernameDuplicate',
    );
    expect(mockUpdateSettings).not.toHaveBeenCalled();
  });

  it('door station editor adds and removes rows', async () => {
    const { container } = await renderPanel();

    const addBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('settings.doorbell.userAdd'),
    );
    expect(addBtn).toBeTruthy();
    await fireEvent.click(addBtn!);

    const usernames = container.querySelectorAll('[id^="doorbell-user-"][id$="-username"]');
    expect(usernames.length).toBe(3);
    expect(settingsForm.isDirty('doorbell')).toBe(true);

    const removeBtn = container.querySelector('button[aria-label="settings.doorbell.userRemove"]');
    expect(removeBtn).toBeTruthy();
    await fireEvent.click(removeBtn!);

    expect(container.querySelectorAll('[id^="doorbell-user-"][id$="-username"]').length).toBe(2);
  });
});
