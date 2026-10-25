import { render, cleanup, fireEvent } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import NotificationsPanel from '$lib/../routes/settings/NotificationsPanel.svelte';
import { settingsForm } from '$lib/settings/settings-form.svelte';
import { showToast } from '$lib/toast.svelte';
import type { SettingsConfig, NotificationsStatus } from '$lib/api';

// --- Mock i18n ---
vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

// --- Mock lucide-svelte icons ---
vi.mock('lucide-svelte', () => {
  const icons = ['Plus', 'X', 'Send', 'ChevronDown'];
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
const mockGetNotificationsStatus = vi.hoisted(() => vi.fn());
const mockSendTestNotification = vi.hoisted(() => vi.fn());

vi.mock('$lib/api', async () => {
  const actual = await vi.importActual('$lib/api');
  return {
    ...actual,
    getSettings: mockGetSettings,
    updateSettings: mockUpdateSettings,
    getNotificationsStatus: mockGetNotificationsStatus,
    sendTestNotification: mockSendTestNotification,
  };
});

const sampleStatus: NotificationsStatus = {
  enabled: true,
  types: [
    'camera.connection',
    'camera.motion',
    'ai.detection',
    'doorbell.ring',
    'notify.test',
  ],
  sinks: [
    {
      name: 'hub',
      url: 'http://192.168.1.20:9080/api/ingest/webhook/***',
      sent_total: 3,
      failed_total: 0,
      retried_total: 0,
      dropped_queue: 0,
      dropped_rate: 0,
      dropped_filter: 0,
      queue_depth: 0,
    },
  ],
};

function sampleConfig(overrides: Partial<SettingsConfig['notifications']> = {}): SettingsConfig {
  return {
    cleanup: { retention_days: 7, disk_threshold_percent: 90 },
    webdav: { enabled: false, path_prefix: '/webdav', read_write: false },
    notifications: {
      enabled: true,
      base_url: 'http://192.168.1.10:9090',
      api_key_configured: true,
      sinks: [
        {
          name: 'hub',
          url: 'http://192.168.1.20:9080/api/ingest/webhook',
          token_configured: true,
          events: ['camera.connection'],
          severity_min: 'info',
          timeout_s: 5,
          burst_per_min: 30,
        },
      ],
      ...overrides,
    },
  };
}

async function renderPanel(config: SettingsConfig = sampleConfig()) {
  mockGetSettings.mockResolvedValue(config);
  mockGetNotificationsStatus.mockResolvedValue(sampleStatus);
  mockUpdateSettings.mockResolvedValue({ status: 'updated', restart_required: true });
  const result = render(NotificationsPanel);
  await vi.waitFor(() => {
    expect(result.container.querySelector('.card button')).toBeTruthy();
  });
  const header = result.container.querySelector('.card button') as HTMLButtonElement;
  await fireEvent.click(header);
  await vi.waitFor(() => {
    expect(result.container.querySelector('#notify-base-url')).toBeTruthy();
  });
  return result;
}

describe('NotificationsPanel', () => {
  beforeEach(() => {
    settingsForm.clear();
    mockGetSettings.mockReset();
    mockUpdateSettings.mockReset();
    mockGetNotificationsStatus.mockReset();
    mockSendTestNotification.mockReset();
    vi.mocked(showToast).mockClear();
  });

  afterEach(() => cleanup());

  it('renders fields bound to the sample config', async () => {
    const { container } = await renderPanel();

    const baseUrl = container.querySelector('#notify-base-url') as HTMLInputElement;
    expect(baseUrl.value).toBe('http://192.168.1.10:9090');

    const apiKey = container.querySelector('#notify-api-key') as HTMLInputElement;
    expect(apiKey.value).toBe('');
    expect(apiKey.placeholder).toBe('settings.notifications.secretConfigured');

    const sinkUrl = container.querySelector('#sink-url-0') as HTMLInputElement;
    expect(sinkUrl.value).toBe('http://192.168.1.20:9080/api/ingest/webhook');

    // Event-type chips render from the status vocabulary; the configured
    // selection is highlighted.
    expect(container.textContent).toContain('settings.notifications.eventType.camera.motion');
    const activeChip = Array.from(container.querySelectorAll('button')).find((b) =>
      b.classList.contains('border-accent'),
    );
    expect(activeChip?.textContent).toBe('settings.notifications.eventType.camera.connection');

    // Delivery counters from the status endpoint are shown.
    expect(container.textContent).toContain('3');
  });

  it('binds user edits back to state', async () => {
    const { container } = await renderPanel();

    const baseUrl = container.querySelector('#notify-base-url') as HTMLInputElement;
    await fireEvent.input(baseUrl, { target: { value: 'http://10.0.0.5:9090' } });
    expect(baseUrl.value).toBe('http://10.0.0.5:9090');
    expect(settingsForm.isDirty('notifications')).toBe(true);
  });

  it('save sends the notifications block and keeps blank secrets', async () => {
    await renderPanel();

    await settingsForm.panels.get('notifications')!.save();

    expect(mockUpdateSettings).toHaveBeenCalledTimes(1);
    const body = mockUpdateSettings.mock.calls[0][0];
    expect(body.notifications).toEqual({
      enabled: true,
      base_url: 'http://192.168.1.10:9090',
      api_key: '', // blank = keep existing server-side
      sinks: [
        {
          name: 'hub',
          url: 'http://192.168.1.20:9080/api/ingest/webhook',
          token: '', // blank = keep existing for same-named sink
          events: ['camera.connection'],
          severity_min: 'info',
          timeout_s: 5,
          burst_per_min: 30,
        },
      ],
    });

    const messages = vi.mocked(showToast).mock.calls.map((c) => c[0]);
    expect(messages).toContain('settings.notifications.restartRequired');
  });

  it('blocks save when enabled with no sinks', async () => {
    await renderPanel(sampleConfig({ sinks: [] }));

    await expect(
      settingsForm.panels.get('notifications')!.save(),
    ).rejects.toThrow('settings.notifications.validationSinks');
    expect(mockUpdateSettings).not.toHaveBeenCalled();
  });

  it('blocks save with an invalid sink URL', async () => {
    await renderPanel(
      sampleConfig({
        sinks: [
          {
            name: 'hub',
            url: 'not-a-url',
            token: 'tok',
            events: [],
            severity_min: 'info',
            timeout_s: 5,
            burst_per_min: 30,
          },
        ],
      }),
    );

    await expect(
      settingsForm.panels.get('notifications')!.save(),
    ).rejects.toThrow('settings.notifications.validationSink');
    expect(mockUpdateSettings).not.toHaveBeenCalled();
  });

  it('blocks save for a new sink without a token', async () => {
    await renderPanel(
      sampleConfig({
        sinks: [
          {
            name: 'hub',
            url: 'http://x:9080/api/ingest/webhook',
            token: '',
            token_configured: false,
            events: [],
            severity_min: 'info',
            timeout_s: 5,
            burst_per_min: 30,
          },
        ],
      }),
    );

    await expect(
      settingsForm.panels.get('notifications')!.save(),
    ).rejects.toThrow('settings.notifications.validationToken');
    expect(mockUpdateSettings).not.toHaveBeenCalled();
  });

  it('adds and removes sinks', async () => {
    const { container } = await renderPanel();

    const addBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('settings.notifications.addSink'),
    );
    expect(addBtn).toBeTruthy();
    await fireEvent.click(addBtn!);

    expect(container.querySelector('#sink-url-1')).toBeTruthy();
    expect(settingsForm.isDirty('notifications')).toBe(true);

    // Remove the first sink — the remaining one re-renders at index 0.
    const removeBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.title === 'settings.notifications.removeSink',
    );
    await fireEvent.click(removeBtn!);
    expect(container.querySelector('#sink-url-1')).toBeFalsy();
    expect(container.querySelector('#sink-url-0')).toBeTruthy();
  });

  it('toggles event types on a sink', async () => {
    const { container } = await renderPanel();

    const motionChip = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.trim() === 'settings.notifications.eventType.camera.motion',
    );
    expect(motionChip).toBeTruthy();
    await fireEvent.click(motionChip!);

    await settingsForm.panels.get('notifications')!.save();
    const body = mockUpdateSettings.mock.calls[0][0];
    expect(body.notifications.sinks[0].events).toContain('camera.motion');

    // Toggle back off.
    await fireEvent.click(motionChip!);
    await settingsForm.panels.get('notifications')!.save();
    const body2 = mockUpdateSettings.mock.calls[1][0];
    expect(body2.notifications.sinks[0].events).not.toContain('camera.motion');
  });

  it('test button enqueues a test alert and queues a toast', async () => {
    const { container } = await renderPanel();
    mockSendTestNotification.mockResolvedValue({ status: 'queued' });

    const testBtn = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('settings.notifications.testButton'),
    );
    expect(testBtn).toBeTruthy();
    await fireEvent.click(testBtn!);

    await vi.waitFor(() => {
      expect(mockSendTestNotification).toHaveBeenCalledWith({ severity: 'warning' });
    });
    const messages = vi.mocked(showToast).mock.calls.map((c) => c[0]);
    expect(messages).toContain('settings.notifications.testQueued');
  });

  it('disabled config renders no fields but still saves the section', async () => {
    // Inline render (not renderPanel): the enabled section never appears, so
    // the helper's #notify-base-url wait would never resolve.
    mockGetSettings.mockResolvedValue(sampleConfig({ enabled: false }));
    mockGetNotificationsStatus.mockResolvedValue(sampleStatus);
    mockUpdateSettings.mockResolvedValue({ status: 'updated' });
    const { container } = render(NotificationsPanel);
    await vi.waitFor(() => {
      expect(container.querySelector('.card button')).toBeTruthy();
    });
    const header = container.querySelector('.card button') as HTMLButtonElement;
    await fireEvent.click(header);
    await vi.waitFor(() => {
      // Toggle row is the enabled section's only content when disabled.
      expect(container.querySelector('input')).toBeFalsy();
    });

    await settingsForm.panels.get('notifications')!.save();
    expect(mockUpdateSettings).toHaveBeenCalledTimes(1);
    expect(mockUpdateSettings.mock.calls[0][0].notifications.enabled).toBe(false);
  });
});
