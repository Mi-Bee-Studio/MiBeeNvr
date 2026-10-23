import { render, cleanup, fireEvent } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import DlnaPanel from '$lib/../routes/settings/DlnaPanel.svelte';
import { settingsForm } from '$lib/settings/settings-form.svelte';
import { showToast } from '$lib/toast.svelte';
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
vi.mock('$lib/toast.svelte', () => ({
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
  dlna: {
    enabled: true,
    port: 8200,
    interfaces: ['eth0'],
    friendly_name: 'Living room NVR',
  },
};

async function renderPanel(config: SettingsConfig = sampleConfig) {
  mockGetSettings.mockResolvedValue(config);
  mockUpdateSettings.mockResolvedValue({ status: 'updated', restart_required: true });
  const result = render(DlnaPanel);
  await vi.waitFor(() => {
    expect(result.container.querySelector('.card button')).toBeTruthy();
  });
  const header = result.container.querySelector('.card button') as HTMLButtonElement;
  await fireEvent.click(header);
  await vi.waitFor(() => {
    expect(result.container.querySelector('#dlna-port')).toBeTruthy();
  });
  return result;
}

describe('DlnaPanel', () => {
  beforeEach(() => {
    settingsForm.clear();
    mockGetSettings.mockReset();
    mockUpdateSettings.mockReset();
    vi.mocked(showToast).mockClear();
  });

  afterEach(() => cleanup());

  it('renders fields bound to the sample config', async () => {
    const { container } = await renderPanel();

    const port = container.querySelector('#dlna-port') as HTMLInputElement;
    expect(port.value).toBe('8200');

    const friendly = container.querySelector('#dlna-friendly-name') as HTMLInputElement;
    expect(friendly.value).toBe('Living room NVR');

    // Interface chips render.
    expect(container.textContent).toContain('eth0');
  });

  it('binds user edits back to state', async () => {
    const { container } = await renderPanel();

    const friendly = container.querySelector('#dlna-friendly-name') as HTMLInputElement;
    await fireEvent.input(friendly, { target: { value: 'Bedroom' } });
    expect(friendly.value).toBe('Bedroom');
    expect(settingsForm.isDirty('dlna')).toBe(true);
  });

  it('save sends the dlna block via updateSettings and surfaces restart_required', async () => {
    await renderPanel();

    await settingsForm.panels.get('dlna')!.save();

    expect(mockUpdateSettings).toHaveBeenCalledTimes(1);
    const body = mockUpdateSettings.mock.calls[0][0];
    expect(body.dlna).toEqual({
      enabled: true,
      port: 8200,
      interfaces: ['eth0'],
      friendly_name: 'Living room NVR',
    });

    const messages = vi.mocked(showToast).mock.calls.map((c) => c[0]);
    expect(messages).toContain('settings.dlna.restartRequired');
  });

  it('blocks save when enabled with an invalid port', async () => {
    const { container } = await renderPanel();

    const port = container.querySelector('#dlna-port') as HTMLInputElement;
    await fireEvent.input(port, { target: { value: '99999' } });

    await expect(settingsForm.panels.get('dlna')!.save()).rejects.toThrow('settings.dlna.validationPort');
    expect(mockUpdateSettings).not.toHaveBeenCalled();
  });

  it('interface editor adds and removes entries', async () => {
    const { container } = await renderPanel();

    const input = container.querySelector('#dlna-interfaces') as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'enp3s0' } });
    const addBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('settings.dlna.interfacesAdd'),
    );
    expect(addBtn).toBeTruthy();
    await fireEvent.click(addBtn!);

    expect(container.textContent).toContain('enp3s0');
    expect(settingsForm.isDirty('dlna')).toBe(true);

    const removeBtn = container.querySelector('button[aria-label="settings.dlna.interfacesRemove"]');
    expect(removeBtn).toBeTruthy();
    await fireEvent.click(removeBtn!);

    expect(container.textContent).not.toContain('eth0');
  });

  it('disabled config saves without port validation', async () => {
    const disabledConfig: SettingsConfig = {
      ...sampleConfig,
      dlna: { ...sampleConfig.dlna!, enabled: false, port: 0 },
    };
    mockGetSettings.mockResolvedValue(disabledConfig);
    mockUpdateSettings.mockResolvedValue({ status: 'updated' });
    const { container } = render(DlnaPanel);
    // Disabled config renders no fields — just wait for the card, expand it.
    await vi.waitFor(() => {
      expect(container.querySelector('.card button')).toBeTruthy();
    });
    const header = container.querySelector('.card button') as HTMLButtonElement;
    await fireEvent.click(header);

    // Enabled section does not render; save still sends the section.
    expect(container.querySelector('#dlna-port')).toBeFalsy();

    await settingsForm.panels.get('dlna')!.save();

    expect(mockUpdateSettings).toHaveBeenCalledTimes(1);
    expect(mockUpdateSettings.mock.calls[0][0].dlna.enabled).toBe(false);
  });
});
