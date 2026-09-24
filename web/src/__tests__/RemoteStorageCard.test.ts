import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import RemoteStorageCard from '$lib/../routes/settings/RemoteStorageCard.svelte';
import zh from '$lib/i18n/zh.json';
import en from '$lib/i18n/en.json';
import { settingsForm } from '$lib/settings/settings-form.svelte';

// --- Mock i18n (identity — the placeholder keys must survive to the DOM) ---
vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

// --- Mock lucide-svelte icons used by the card ---
vi.mock('lucide-svelte', () => {
  const icons = ['Cloud', 'RefreshCw', 'ChevronDown'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = function MockIcon() {
      return document.createElement('span');
    };
  }
  return mock;
});

vi.mock('$lib/toast', () => ({
  showToast: vi.fn(),
}));

const mockGetSettings = vi.hoisted(() => vi.fn());
const mockUpdateSettings = vi.hoisted(() => vi.fn());
const mockGetOffloadStatus = vi.hoisted(() => vi.fn());

vi.mock('$lib/api', () => ({
  getSettings: mockGetSettings,
  updateSettings: mockUpdateSettings,
  getOffloadStatus: mockGetOffloadStatus,
}));

describe('RemoteStorageCard (#892 follow-up: placeholder must not evaluate)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    settingsForm.clear();
    mockGetSettings.mockResolvedValue({ storage: {} });
    mockGetOffloadStatus.mockResolvedValue(null);
  });
  afterEach(() => {
    cleanup();
  });

  // Regression: `placeholder="minioadmin 或 ${S3_ACCESS_KEY}"` in the template
  // made Svelte treat {S3_ACCESS_KEY} as an expression tag, so expanding the
  // card threw ReferenceError at render and the card body stayed permanently
  // empty (the render effect was destroyed). The placeholder must come from
  // i18n data so the literal ${S3_ACCESS_KEY} env-var hint is displayed.
  it('expands without throwing and keeps the ${S3_ACCESS_KEY} hint literal', async () => {
    const { container } = render(RemoteStorageCard);
    await waitFor(() => expect(container.querySelector('.card')).toBeTruthy());

    const btn = container.querySelector('button[aria-expanded]');
    expect(btn).toBeTruthy();
    await fireEvent.click(btn);

    const ak = container.querySelector('#remote-ak');
    expect(ak).toBeTruthy();
    expect(ak.getAttribute('placeholder')).toBe('settings.remote.accessKeyPlaceholder');

    const endpoint = container.querySelector('#remote-endpoint');
    expect(endpoint.getAttribute('placeholder')).toBe('settings.remote.endpointPlaceholder');
  });

  it('i18n data carries the literal env-var syntax (never a live expression)', () => {
    expect(zh['settings.remote.accessKeyPlaceholder']).toBe('minioadmin 或 ${S3_ACCESS_KEY}');
    expect(en['settings.remote.accessKeyPlaceholder']).toBe('minioadmin or ${S3_ACCESS_KEY}');
    expect(zh['settings.remote.endpointPlaceholder']).toBe('https://s3.example.com 或 http://minio:9000');
    expect(en['settings.remote.endpointPlaceholder']).toBe('https://s3.example.com or http://minio:9000');
  });
});
