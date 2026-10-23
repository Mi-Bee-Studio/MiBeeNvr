import { render, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import Settings from '$lib/../routes/Settings.svelte';

vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

vi.mock('lucide-svelte', () => {
  const icons = ['Settings', 'HardDrive', 'Camera', 'Radio', 'RadioTower', 'BellRing', 'Tv', 'BrainCircuit', 'Film', 'Info', 'Save', 'RotateCcw', 'Globe'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = function MockIcon() {
      return document.createElement('span');
    };
  }
  return mock;
});

vi.mock('$lib/toast.svelte', () => ({ showToast: vi.fn() }));

// Stub every settings panel so the shell test only exercises the sidebar —
// the real panels each have (or get) their own tests.
async function stubPanel(path: string) {
  const mod = await import('./__fixtures__/BlankPanel.svelte');
  return { default: mod.default };
}
vi.mock('$lib/../routes/settings/GeneralPanel.svelte', () => stubPanel('GeneralPanel'));
vi.mock('$lib/../routes/settings/StoragePanel.svelte', () => stubPanel('StoragePanel'));
vi.mock('$lib/../routes/settings/CameraAccessPanel.svelte', () => stubPanel('CameraAccessPanel'));
vi.mock('$lib/../routes/settings/StreamingPanel.svelte', () => stubPanel('StreamingPanel'));
vi.mock('$lib/../routes/settings/GB28181Panel.svelte', () => stubPanel('GB28181Panel'));
vi.mock('$lib/../routes/settings/DoorbellPanel.svelte', () => stubPanel('DoorbellPanel'));
vi.mock('$lib/../routes/settings/DlnaPanel.svelte', () => stubPanel('DlnaPanel'));
vi.mock('$lib/../routes/settings/AIPanel.svelte', () => stubPanel('AIPanel'));
vi.mock('$lib/../routes/settings/ProcessingPanel.svelte', () => stubPanel('ProcessingPanel'));
vi.mock('$lib/../routes/settings/P2pPanel.svelte', () => stubPanel('P2pPanel'));
vi.mock('$lib/../routes/settings/UpdatePanel.svelte', () => stubPanel('UpdatePanel'));

describe('Settings sidebar categories', () => {
  afterEach(() => {
    cleanup();
  });

  // The Advanced (高级) section was removed with its only card — the resource
  // estimate table (user ruling 2026-09-24: no value, RPi-3B-era guesses).
  it('lists exactly the eleven categories incl. doorbell + DLNA (no 高级/advanced)', () => {
    const { container } = render(Settings);
    const labels = [...container.querySelectorAll('nav button')].map((b) => (b as HTMLElement).textContent?.trim());
    const expected = [
      'settings.sidebar.general',
      'settings.sidebar.storage',
      'settings.sidebar.cameras',
      'settings.sidebar.streaming',
      'settings.sidebar.gb28181',
      'settings.sidebar.doorbell',
      'settings.sidebar.dlna',
      'settings.sidebar.p2p',
      'settings.sidebar.ai',
      'settings.sidebar.processing',
      'settings.sidebar.about',
    ];
    // mobile strip + desktop sidebar each render the full list
    expect(labels).toEqual([...expected, ...expected]);
    expect(labels).not.toContain('settings.sidebar.advanced');
  });
});
