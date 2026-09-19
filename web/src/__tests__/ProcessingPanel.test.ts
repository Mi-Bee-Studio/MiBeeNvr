import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import ProcessingPanel from '$lib/../routes/settings/ProcessingPanel.svelte';
import { settingsForm } from '$lib/settings/settings-form.svelte';

// --- Mock i18n ---
vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

// --- Mock lucide-svelte icons used by the panel's component tree ---
vi.mock('lucide-svelte', () => {
  const icons = ['AlertCircle', 'AlertTriangle', 'ChevronDown', 'ChevronUp', 'Cpu', 'Download', 'RotateCw', 'XCircle'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = function MockIcon() {
      return document.createElement('span');
    };
  }
  return mock;
});

// --- Mock toast ---
vi.mock('$lib/toast', () => ({
  showToast: vi.fn(),
}));

// --- Mock the API surface the panel + transcoding card touch ---
const mockGetMergeSettings = vi.hoisted(() => vi.fn());
const mockUpdateMergeSettings = vi.hoisted(() => vi.fn());

vi.mock('$lib/api', () => ({
  getMergeSettings: mockGetMergeSettings,
  updateMergeSettings: mockUpdateMergeSettings,
}));

vi.mock('$lib/api/transcoding', () => ({
  getTranscodingSettings: vi.fn().mockResolvedValue({ enabled: false, max_workers: 1, output_format: 'h264', crf: 23 }),
  getTranscodingCheck: vi.fn().mockResolvedValue({ ok: true, ffmpeg_available: false }),
  getTranscodingStatus: vi.fn().mockResolvedValue(null),
  getFFmpegStatus: vi.fn().mockResolvedValue(null),
  downloadFFmpeg: vi.fn(),
  retryDownload: vi.fn(),
}));

describe('ProcessingPanel fragment batching (#852)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    settingsForm.clear(); // one test's panel registrations must not leak
  });
  afterEach(() => {
    cleanup();
  });

  it('shows the effective hold window and saves an edited value', async () => {
    mockGetMergeSettings.mockResolvedValue({
      enabled: true,
      rolling_fragment_hold_s: 300,
    });
    const { container } = render(ProcessingPanel);

    // SettingsCard is an accordion — expand the merge card before querying.
    await waitFor(() => {
      const header = container.querySelector('button[aria-expanded]') as HTMLButtonElement | null;
      expect(header).toBeTruthy();
      return header!;
    }).then((header) => fireEvent.click(header));
    // The click handler flips expanded state asynchronously; give it a tick.
    await new Promise((r) => setTimeout(r, 20));

    const input = await waitFor(() => {
      const el = container.querySelector('#fragmentHoldS') as HTMLInputElement | null;
      expect(el).toBeTruthy();
      return el!;
    });
    expect(input.value).toBe('300');

    // Edit → dirty → registered save sends the new value.
    await fireEvent.input(input, { target: { value: '120' } });
    mockUpdateMergeSettings.mockResolvedValue({ status: 'updated' });
    // The panel registers its save under 'processing'; drive it via the
    // exposed form map (documented test-only surface).
    const panels = (settingsForm as unknown as { panels: Map<string, { save: () => Promise<void> }> }).panels;
    const handle = panels.get('processing');
    expect(handle).toBeTruthy();
    await handle!.save();
    expect(mockUpdateMergeSettings).toHaveBeenCalledWith(
      expect.objectContaining({ enabled: true, rolling_fragment_hold_s: 120 }),
    );
  });

  it('treats a missing value as the 300s default and clamps out-of-range input on save', async () => {
    mockGetMergeSettings.mockResolvedValue({ enabled: true });
    const { container } = render(ProcessingPanel);

    await waitFor(() => {
      const header = container.querySelector('button[aria-expanded]') as HTMLButtonElement | null;
      expect(header).toBeTruthy();
      return header!;
    }).then((header) => fireEvent.click(header));
    await new Promise((r) => setTimeout(r, 20));

    const input = await waitFor(() => {
      const el = container.querySelector('#fragmentHoldS') as HTMLInputElement | null;
      expect(el).toBeTruthy();
      return el!;
    });
    expect(input.value).toBe('300');

    await fireEvent.input(input, { target: { value: '9999' } });
    mockUpdateMergeSettings.mockResolvedValue({ status: 'updated' });
    const panels = (settingsForm as unknown as { panels: Map<string, { save: () => Promise<void> }> }).panels;
    const handle = panels.get('processing');
    expect(handle).toBeTruthy();
    await handle!.save();
    expect(mockUpdateMergeSettings).toHaveBeenCalledWith(
      expect.objectContaining({ rolling_fragment_hold_s: 3600 }),
    );
  });
});
