import { render, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import PushTargetStatus from '$lib/components/PushTargetStatus.svelte';
import type { PushTargetStatus as PushTargetStatusType } from '$lib/api';

// Mock lucide-svelte icons
vi.mock('lucide-svelte', () => {
  const icons = ['Thermometer', 'AlertTriangle', 'Radio', 'Disc3'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = () => document.createElement('span');
  }
  return mock;
});

// Mock i18n
vi.mock('$lib/i18n', () => ({
  t: (key: string, params?: Record<string, string>) => {
    const translations: Record<string, string> = {
      'cameras.pushStatus.streaming': 'Streaming',
      'cameras.pushStatus.idle': 'Idle',
      'cameras.pushStatus.connecting': 'Connecting',
      'cameras.pushStatus.reconnecting': 'Reconnecting',
      'cameras.pushStatus.error': 'Error',
      'cameras.pushStatus.transcoding': 'Transcoding',
      'cameras.pushStatus.overheating': 'Overheating',
      'cameras.pushStatus.avDrift': `A/V drift ${params?.ms || ''}ms`,
      'cameras.pushAudioAAC': 'AAC',
      'cameras.pushAudioG711Mu': 'G.711 μ-law',
      'cameras.pushAudioG711A': 'G.711 a-law',
      'cameras.pushAudioG711': 'G.711',
      'cameras.pushAudioSilent': 'Silent AAC',
      'cameras.pushTranscodeHW': 'HW',
      'cameras.pushTranscodeSW': 'SW',
      'cameras.pushTranscodeThrottled': 'throttled',
    };
    return translations[key] || key;
  },
}));

function makeStatus(overrides: Partial<PushTargetStatusType> = {}): PushTargetStatusType {
  return {
    id: 'tgt-abc123',
    name: 'Test Target',
    protocol: 'rtmp',
    url: 'rtmp://example.com/live/key',
    status: 'streaming',
    kbps: 2500,
    enabled: true,
    uptime: '01:23:45',
    updated_at: new Date().toISOString(),
    ...overrides,
  };
}

describe('PushTargetStatus', () => {
  afterEach(() => cleanup());

  it('renders streaming badge with bitrate', () => {
    const status = makeStatus();
    const { container } = render(PushTargetStatus, { props: { status } });
    const badge = container.querySelector('.rounded-full');
    expect(badge).toBeTruthy();
    expect(badge?.textContent).toContain('2500');
    expect(badge?.textContent).toContain('kbps');
  });

  it('renders error status badge', () => {
    const status = makeStatus({ status: 'error', error: 'Connection refused' });
    const { container } = render(PushTargetStatus, { props: { status } });
    const badge = container.querySelector('.rounded-full');
    expect(badge).toBeTruthy();
    expect(badge?.textContent).toContain('Error');
  });

  it('renders connecting status badge', () => {
    const status = makeStatus({ status: 'connecting', kbps: 0 });
    const { container } = render(PushTargetStatus, { props: { status } });
    const badge = container.querySelector('.rounded-full');
    expect(badge).toBeTruthy();
    expect(badge?.textContent).toContain('Connecting');
  });

  it('renders idle status badge', () => {
    const status = makeStatus({ status: 'idle', kbps: 0 });
    const { container } = render(PushTargetStatus, { props: { status } });
    const badge = container.querySelector('.rounded-full');
    expect(badge).toBeTruthy();
    expect(badge?.textContent).toContain('Idle');
  });

  it('shows uptime when streaming', () => {
    const status = makeStatus({ uptime: '05:30:00' });
    const { container } = render(PushTargetStatus, { props: { status } });
    expect(container.textContent).toContain('05:30:00');
  });

  it('shows transcode status badge when transcode_status is set', () => {
    const status = makeStatus({ transcode_status: 'transcoding', transcode_resolution: '1920x1080' });
    const { container } = render(PushTargetStatus, { props: { status } });
    expect(container.textContent).toContain('Transcoding');
    expect(container.textContent).toContain('1920x1080');
    expect(container.textContent).toContain('HW');
  });

  it('shows throttled transcode status badge', () => {
    const status = makeStatus({ transcode_status: 'throttled', transcode_resolution: '1280x720' });
    const { container } = render(PushTargetStatus, { props: { status } });
    expect(container.textContent).toContain('Transcoding');
    expect(container.textContent).toContain('1280x720');
    expect(container.textContent).toContain('SW');
    expect(container.textContent).toContain('throttled');
  });

  it('shows audio codec indicator', () => {
    const status = makeStatus({ audio_codec: 'AAC' });
    const { container } = render(PushTargetStatus, { props: { status } });
    expect(container.textContent).toContain('AAC');
  });

  it('shows G.711 mu-law audio codec indicator', () => {
    const status = makeStatus({ audio_codec: 'G.711 μ-law' });
    const { container } = render(PushTargetStatus, { props: { status } });
    expect(container.textContent).toContain('G.711');
  });

  it('shows thermal warning when temperature > 70°C', () => {
    const status = makeStatus({ temperature_c: 85 });
    const { container } = render(PushTargetStatus, { props: { status } });
    expect(container.textContent).toContain('Overheating');
  });

  it('does NOT show thermal warning when temperature is normal', () => {
    const status = makeStatus({ temperature_c: 45 });
    const { container } = render(PushTargetStatus, { props: { status } });
    expect(container.textContent).not.toContain('Overheating');
  });

  it('shows A/V drift warning when drift > 500ms', () => {
    const status = makeStatus({ av_drift_ms: 1200 });
    const { container } = render(PushTargetStatus, { props: { status } });
    expect(container.textContent).toContain('1200');
    expect(container.textContent).toContain('drift');
  });

  it('does NOT show A/V drift warning when drift is within range', () => {
    const status = makeStatus({ av_drift_ms: 200 });
    const { container } = render(PushTargetStatus, { props: { status } });
    expect(container.textContent).not.toContain('drift');
  });

  it('handles missing enhanced fields gracefully', () => {
    const status = makeStatus({
      transcode_status: undefined,
      audio_codec: undefined,
      temperature_c: undefined,
      av_drift_ms: undefined,
    });
    const { container } = render(PushTargetStatus, { props: { status } });
    // Should still show basic streaming info
    expect(container.textContent).toContain('kbps');
    expect(container.textContent).not.toContain('Overheating');
    expect(container.textContent).not.toContain('drift');
  });

  it('shows reconnecting status badge', () => {
    const status = makeStatus({ status: 'reconnecting', kbps: 0 });
    const { container } = render(PushTargetStatus, { props: { status } });
    const badge = container.querySelector('.rounded-full');
    expect(badge).toBeTruthy();
    expect(badge?.textContent).toContain('Reconnecting');
  });

  it('shows silent audio codec indicator', () => {
    const status = makeStatus({ audio_codec: 'Silent AAC' });
    const { container } = render(PushTargetStatus, { props: { status } });
    expect(container.textContent).toContain('Silent AAC');
  });

  it('shows drift label in title attribute', () => {
    const status = makeStatus({ av_drift_ms: 1500 });
    const { container } = render(PushTargetStatus, { props: { status } });
    const driftEls = container.querySelectorAll('[style*="color: var(--color-warning)"]');
    expect(driftEls.length).toBeGreaterThanOrEqual(1);
  });
});
