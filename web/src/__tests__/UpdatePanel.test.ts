import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import UpdatePanel from '$lib/../routes/settings/UpdatePanel.svelte';

// --- Mock i18n / icons / toast (panel-wide, mirrors GB28181Panel.test.ts) ---
vi.mock('$lib/i18n', () => ({
  t: (key: string, params?: Record<string, string | number>) => {
    let v = key;
    if (params) for (const [k, p] of Object.entries(params)) v = v.replace(`{${k}}`, String(p));
    return v;
  },
}));

vi.mock('lucide-svelte', () => {
  const icons = [
    'RefreshCw',
    'CheckCircle2',
    'ArrowUpCircle',
    'ExternalLink',
    'Download',
    'History',
    'Loader2',
    'AlertTriangle',
  ];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) mock[name] = () => document.createElement('span');
  return mock;
});

vi.mock('$lib/toast', () => ({ showToast: vi.fn() }));

// --- Mock API surface ---
const mockGetUpdateStatus = vi.hoisted(() => vi.fn());
const mockRefresh = vi.hoisted(() => vi.fn());
const mockGetSettings = vi.hoisted(() => vi.fn());
const mockUpdateSettings = vi.hoisted(() => vi.fn());
const mockApply = vi.hoisted(() => vi.fn());
const mockGetApplyStatus = vi.hoisted(() => vi.fn());
const mockGetHistory = vi.hoisted(() => vi.fn());

vi.mock('$lib/api', async () => {
  const actual = await vi.importActual<typeof import('$lib/api')>('$lib/api');
  return {
    ...actual,
    getUpdateStatus: mockGetUpdateStatus,
    refreshUpdateStatus: mockRefresh,
    getSettings: mockGetSettings,
    updateSettings: mockUpdateSettings,
    applyUpdate: mockApply,
    getApplyStatus: mockGetApplyStatus,
    getUpdateHistory: mockGetHistory,
  };
});

function prime(status: Record<string, unknown>, opts?: { applyStatus?: Record<string, unknown>; autoApply?: boolean }) {
  mockGetUpdateStatus.mockResolvedValue({
    current: 'v1.0.0',
    latest: 'v9.9.9',
    update_available: true,
    deployment: 'binary',
    ...status,
  });
  mockGetSettings.mockResolvedValue({ update: { auto_apply: opts?.autoApply ?? false } });
  mockGetApplyStatus.mockResolvedValue(opts?.applyStatus ?? { state: 'idle' });
  mockApply.mockResolvedValue({ state: 'requested' });
  mockUpdateSettings.mockResolvedValue({ status: 'ok' });
  mockGetHistory.mockResolvedValue([]);
}

beforeEach(() => {
  vi.clearAllMocks();
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('UpdatePanel apply UI (#648)', () => {
  it('hides the apply button and auto-apply toggle on docker deployments', async () => {
    prime({ deployment: 'docker' });
    const { queryByText, findByText } = render(UpdatePanel);
    await findByText('v1.0.0');
    expect(queryByText(/about.applyNow/)).toBeNull();
    expect(queryByText('about.autoApply')).toBeNull();
  });

  it('shows the apply button on bare-metal, asks for confirmation, then triggers once', async () => {
    prime({});
    const { findByText, queryByText, getByText } = render(UpdatePanel);
    await findByText('v1.0.0');

    const btn = getByText(/about.applyNow/);
    await fireEvent.click(btn);

    // Confirm dialog with the target version, not an immediate trigger.
    await waitFor(() => expect(queryByText('about.applyConfirmTitle')).not.toBeNull());
    expect(mockApply).not.toHaveBeenCalled();

    await fireEvent.click(getByText('common.confirm'));
    await waitFor(() => expect(mockApply).toHaveBeenCalledTimes(1));
  });

  it('renders the success banner (with reload hint) for a completed apply', async () => {
    prime({}, { applyStatus: { state: 'success', from: 'v1.0.0', to: 'v9.9.9', time: '2026-09-03T00:00:00Z' } });
    const { findByText } = render(UpdatePanel);
    await findByText(/about.applyState.success/);
    expect(await findByText('about.applyRestarted')).toBeTruthy();
  });

  it('double-confirms before enabling auto-apply and saves the toggle', async () => {
    prime({});
    const { findByText, findByRole, getByText } = render(UpdatePanel);
    await findByText('v1.0.0');

    // The toggle renders as a switch bound to the auto-apply state.
    const sw = await findByRole('switch');
    await fireEvent.click(sw);

    // First click opens the confirmation dialog instead of saving directly.
    await waitFor(() => expect(getByText('about.autoApplyConfirmTitle')).toBeTruthy());
    expect(mockUpdateSettings).not.toHaveBeenCalled();

    await fireEvent.click(getByText('common.confirm'));
    await waitFor(() =>
      expect(mockUpdateSettings).toHaveBeenCalledWith(expect.objectContaining({ update: { auto_apply: true } })),
    );
  });
});
