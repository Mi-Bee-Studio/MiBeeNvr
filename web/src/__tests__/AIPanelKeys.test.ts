import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import AIPanel from '$lib/../routes/settings/AIPanel.svelte';
import { settingsForm } from '$lib/settings/settings-form.svelte';

vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

vi.mock('lucide-svelte', () => {
  const icons = ['Plus', 'Trash2', 'X', 'Copy', 'Check', 'ChevronDown'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = function MockIcon() {
      return document.createElement('span');
    };
  }
  return mock;
});

vi.mock('$lib/toast', () => ({ showToast: vi.fn() }));
vi.mock('$lib/clipboard', () => ({ copyText: vi.fn().mockResolvedValue(true) }));
vi.mock('$lib/mibeevision-status.svelte', () => ({ refreshMiBeeVisionStatus: vi.fn() }));
vi.mock('$lib/format', () => ({ formatRelativeTime: () => 'just now' }));

// #718 allows reusing the name of a REVOKED key, so the key list can contain
// duplicate names (observed live on M5: two "e2e-probe-token [revoked]").
const mockGetSettings = vi.hoisted(() => vi.fn());

vi.mock('$lib/api', () => ({
  getAiStatus: vi.fn().mockResolvedValue({
    enabled: false,
    confidence_threshold: 0.5,
    frame_skip_rate: 3,
    ema_alpha: 0.3,
    max_age: 15,
    enabled_classes: [],
    model_url: '',
  }),
  getAiSettings: vi.fn().mockResolvedValue({ enabled: false, confidence_threshold: 0.5, frame_skip_rate: 3 }),
  saveAiSettings: vi.fn(),
  detectAiBackend: vi.fn().mockResolvedValue(''),
  listCameras: vi.fn().mockResolvedValue([]),
  getAiStatusExtra: vi.fn(),
  updateAiConfig: vi.fn(),
  listAiModels: vi.fn().mockResolvedValue([]),
  getPerCameraAiSettings: vi.fn().mockReturnValue({}),
  savePerCameraAiSettings: vi.fn(),
  getAIZones: vi.fn().mockResolvedValue([]),
  createAIZone: vi.fn(),
  deleteAIZone: vi.fn(),
  getSettings: mockGetSettings,
  generateAPIKey: vi.fn(),
  revokeAPIKey: vi.fn(),
  getVisionStatus: vi.fn().mockResolvedValue(null),
  updateVisionSettings: vi.fn(),
}));

describe('AIPanel MiBeeVision key list with duplicate (revoked) names', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    settingsForm.clear();
    mockGetSettings.mockResolvedValue({
      mibeevision: {
        api_keys: [
          { name: 'mibee-app-1', prefix: 'mbv_aaa1…', revoked: false },
          { name: 'e2e-probe-token', prefix: 'mbv_bbb1…', revoked: true },
          { name: 'e2e-probe-token', prefix: 'mbv_bbb2…', revoked: true },
          { name: 'e2e-dup-check', prefix: 'mbv_ccc1…', revoked: true },
          { name: 'e2e-dup-check', prefix: 'mbv_ccc2…', revoked: true },
        ],
      },
    });
  });
  afterEach(() => {
    cleanup();
  });

  // Regression (live on M5 2026-09-24): {#each keys (key.name)} threw
  // each_key_duplicate once a revoked name was reused, killing the whole
  // panel render — the MiBeeVision and per-camera cards expanded to nothing.
  it('renders the key list without each_key_duplicate and shows every entry', async () => {
    const { container } = render(AIPanel);

    // wait until the panel finished loading and the MiBeeVision card mounted
    let visionCard: HTMLElement | undefined;
    await waitFor(
      () => {
        visionCard = [...container.querySelectorAll('.card.border')].find((c) =>
          c.querySelector('h3')?.textContent?.trim() === 'settings.mibeevision.title'
        ) as HTMLElement | undefined;
        expect(visionCard).toBeTruthy();
      },
      { timeout: 4000 }
    );
    const btn = visionCard.querySelector('button[aria-expanded]');
    await fireEvent.click(btn);

    // every duplicate-named entry must be rendered (pre-fix: render throws)
    expect(visionCard.textContent?.split('e2e-probe-token').length - 1).toBe(2);
    expect(visionCard.textContent?.split('e2e-dup-check').length - 1).toBe(2);
  });
});
