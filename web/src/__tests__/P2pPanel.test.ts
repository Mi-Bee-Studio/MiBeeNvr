import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import P2pPanel from '$lib/../routes/settings/P2pPanel.svelte';
import { settingsForm } from '$lib/settings/settings-form.svelte';

// --- Mock i18n ---
vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

// --- Mock lucide-svelte icons ---
vi.mock('lucide-svelte', () => {
  const icons = ['ChevronDown'];
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

// --- Mock API ---
const mockGetP2pSettings = vi.hoisted(() => vi.fn());
const mockUpdateP2pSettings = vi.hoisted(() => vi.fn());
const mockGetP2pStatus = vi.hoisted(() => vi.fn());
const mockGetLicense = vi.hoisted(() => vi.fn());
const mockInstallLicense = vi.hoisted(() => vi.fn());

vi.mock('$lib/api', async () => {
  const actual = await vi.importActual('$lib/api');
  return {
    ...actual,
    getP2pSettings: mockGetP2pSettings,
    updateP2pSettings: mockUpdateP2pSettings,
    getP2pStatus: mockGetP2pStatus,
    getLicense: mockGetLicense,
    installLicense: mockInstallLicense,
  };
});

const sampleSettings = {
  enabled: true,
  server_url: 'wss://signal.example.com/signal',
  product_id: 'mibeenvr',
  peer_id: 'nvr-fixture-0001',
  service_name: 'nvr-api',
  username: 'nvr-svc',
  password_configured: true,
  client_id: 'p2p-signaling',
  transport_timeout_s: 20,
  max_clients: 0,
  device_key_configured: false,
};

const sampleStatus = {
  enabled: true,
  online: true,
  peer_id: 'nvr-fixture-0001',
  product_id: 'mibeenvr',
  service: 'nvr-api',
  server_url: 'wss://signal.example.com/signal',
  active_clients: 2,
  max_clients: 5,
  cap_source: 'license',
  rejected_total: 1,
  license: { allowed: true, state: 'valid' },
  last_rejection: {
    code: 1008,
    reason: 'license_required',
    source: 'hello',
    at: '2026-10-03T00:00:00Z',
  },
};

const sampleLicense = {
  state: 'valid' as const,
  required: true,
  allowed: true,
  time_left_sec: 36000,
  document: { plan: 'p2p-remote', max_clients: 5 },
};

async function renderPanel() {
  mockGetP2pSettings.mockResolvedValue(sampleSettings);
  mockGetP2pStatus.mockResolvedValue(sampleStatus);
  mockGetLicense.mockResolvedValue(sampleLicense);
  const result = render(P2pPanel);
  await waitFor(() => {
    expect(result.container.querySelector('#p2p-server-url')).toBeTruthy();
  });
  return result;
}

describe('P2pPanel', () => {
  beforeEach(() => {
    settingsForm.clear();
    vi.clearAllMocks();
  });

  afterEach(() => {
    cleanup();
  });

  it('renders settings + live status + license snapshot', async () => {
    const { container } = await renderPanel();

    const server = container.querySelector('#p2p-server-url') as HTMLInputElement;
    expect(server.value).toBe(sampleSettings.server_url);
    const user = container.querySelector('#p2p-username') as HTMLInputElement;
    expect(user.value).toBe('nvr-svc');
    const maxc = container.querySelector('#p2p-max-clients') as HTMLInputElement;
    expect(maxc.value).toBe('0');

    // Status card: 2/5 clients with license cap source, rejection surfaced.
    expect(container.textContent).toContain('2 / 5');
    expect(container.textContent).toContain('settings.p2p.capSource.license');
    expect(container.textContent).toContain('license_required');
    expect(container.textContent).toContain('settings.p2p.licenseStateValue.valid');
  });

  it('saves via the unified form pipeline and hot-applies', async () => {
    const { container } = await renderPanel();
    mockUpdateP2pSettings.mockResolvedValue({ ...sampleStatus, max_clients: 2 });

    const maxc = container.querySelector('#p2p-max-clients') as HTMLInputElement;
    await fireEvent.input(maxc, { target: { value: '2' } });

    expect(settingsForm.isDirty('p2p')).toBe(true);
    await settingsForm.saveAll();

    expect(mockUpdateP2pSettings).toHaveBeenCalledTimes(1);
    const body = mockUpdateP2pSettings.mock.calls[0][0];
    expect(body.max_clients).toBe(2);
    // 密码留空不发送（保持语义在服务端）。
    expect(body.password).toBeUndefined();
    expect(settingsForm.isDirty('p2p')).toBe(false);
  });

  it('sends the password only when filled', async () => {
    const { container } = await renderPanel();
    mockUpdateP2pSettings.mockResolvedValue(sampleStatus);

    const pass = container.querySelector('#p2p-password') as HTMLInputElement;
    await fireEvent.input(pass, { target: { value: 'new-secret' } });

    await settingsForm.saveAll();
    const body = mockUpdateP2pSettings.mock.calls[0][0];
    expect(body.password).toBe('new-secret');
  });

  it('installs a license document via the license card', async () => {
    const { container } = await renderPanel();
    mockInstallLicense.mockResolvedValue({ ...sampleLicense, state: 'expiring' });

    const doc = container.querySelector('#license-document') as HTMLTextAreaElement;
    await fireEvent.input(doc, { target: { value: 'doc.sig' } });
    const btn = [...container.querySelectorAll('button')].find(
      (b) => b.textContent?.includes('licenseInstall'),
    ) as HTMLButtonElement;
    await fireEvent.click(btn);

    await waitFor(() => {
      expect(mockInstallLicense).toHaveBeenCalledWith(
        expect.objectContaining({ document: 'doc.sig' }),
      );
    });
  });

  it('reset restores the last-saved snapshot', async () => {
    const { container } = await renderPanel();
    const server = container.querySelector('#p2p-server-url') as HTMLInputElement;
    await fireEvent.input(server, { target: { value: 'wss://other/signal' } });
    expect(settingsForm.isDirty('p2p')).toBe(true);

    settingsForm.resetAll();
    await waitFor(() => {
      expect((container.querySelector('#p2p-server-url') as HTMLInputElement).value).toBe(
        sampleSettings.server_url,
      );
    });
    expect(settingsForm.isDirty('p2p')).toBe(false);
  });
});
