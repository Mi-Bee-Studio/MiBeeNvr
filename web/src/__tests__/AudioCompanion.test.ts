import { render, cleanup, waitFor, fireEvent } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import AudioCompanion from '$lib/components/recordings/AudioCompanion.svelte';

// Mock lucide-svelte
vi.mock('lucide-svelte', () => ({
  AudioLines: function MockIcon() {
    return document.createElement('span');
  },
  Volume2: function MockIcon() {
    return document.createElement('span');
  },
  VolumeX: function MockIcon() {
    return document.createElement('span');
  },
}));

// t() echoes the key — assertions match on key fragments.
vi.mock('$lib/i18n', () => ({
  t: (k: string) => k,
}));

const mockListCameras = vi.fn();
const mockListRecordings = vi.fn();
vi.mock('$lib/api', () => ({
  listCameras: (...a: unknown[]) => mockListCameras(...a),
  listRecordings: (...a: unknown[]) => mockListRecordings(...a),
  appendAuthToken: (url: string) => url,
  API_BASE: '',
}));

// Fixed clock base so segment math is deterministic.
const T0 = '2026-10-01T08:00:00Z';
const T1 = '2026-10-01T08:01:00Z';
function seg(id: string, startISO: string, endISO: string) {
  return { id, camera_id: 'cam-mic', started_at: startISO, ended_at: endISO };
}

const videoRecording = {
  id: 'rec-video',
  camera_id: 'cam-hall',
  file_path: '/x/a.mp4',
  format: 'h264' as const,
  started_at: T0,
  ended_at: T1,
  duration: 60,
  file_size: 1,
  frame_count: 1,
  merge_status: 'pending' as const,
};

describe('AudioCompanion', () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
  });
  afterEach(() => cleanup());

  it('renders nothing when no audio device is linked to the recording camera', async () => {
    mockListCameras.mockResolvedValue([
      { id: 'cam-hall', encoding: 'h264', name: 'Hall' },
      { id: 'cam-mic', encoding: 'audio', name: 'Mic', audio_link_camera_id: 'cam-OTHER' },
    ]);
    const { container } = render(AudioCompanion, { props: { recording: videoRecording } });
    await waitFor(() => expect(mockListCameras).toHaveBeenCalled());
    // Give the async load a beat to settle, then expect no panel.
    await new Promise((r) => setTimeout(r, 20));
    expect(container.querySelector('.card')).toBeNull();
    expect(mockListRecordings).not.toHaveBeenCalled();
  });

  it('renders the timeline with the linked device segments', async () => {
    mockListCameras.mockResolvedValue([
      { id: 'cam-hall', encoding: 'h264', name: 'Hall' },
      { id: 'cam-mic', encoding: 'audio', name: 'Hall Mic', audio_link_camera_id: 'cam-hall' },
    ]);
    mockListRecordings.mockResolvedValue({
      recordings: [
        seg('rec-a1', '2026-10-01T07:59:30Z', '2026-10-01T08:00:30Z'),
        seg('rec-a2', '2026-10-01T08:00:30Z', '2026-10-01T08:01:30Z'),
      ],
      total: 2,
    });
    const { container, getByText } = render(AudioCompanion, {
      props: { recording: videoRecording },
    });
    await waitFor(() => expect(mockListRecordings).toHaveBeenCalled());
    await waitFor(() => expect(container.querySelectorAll('button[title*="–"]').length).toBe(2));
    expect(getByText('Hall Mic')).toBeTruthy();
    // Window request brackets the video recording with padding.
    const params = mockListRecordings.mock.calls[0][0] as Record<string, string>;
    expect(params.camera_id).toBe('cam-mic');
    expect(params.start).toBe('2026-10-01T07:58:00.000Z');
    expect(params.end).toBe('2026-10-01T08:03:00.000Z');
  });

  it('persists the manual offset per camera pair', async () => {
    mockListCameras.mockResolvedValue([
      { id: 'cam-mic', encoding: 'audio', name: 'Hall Mic', audio_link_camera_id: 'cam-hall' },
    ]);
    mockListRecordings.mockResolvedValue({
      recordings: [seg('rec-a1', '2026-10-01T07:59:30Z', '2026-10-01T08:00:30Z')],
      total: 1,
    });
    const { container } = render(AudioCompanion, { props: { recording: videoRecording } });
    await waitFor(() => expect(container.querySelector('input[type="range"]')).toBeTruthy());
    const slider = container.querySelector('input[type="range"]') as HTMLInputElement;
    await fireEvent.input(slider, { target: { value: '1.5' } });
    const stored = JSON.parse(
      localStorage.getItem('mibee_nvr_audio_sync_offsets') || '{}'
    ) as Record<string, number>;
    expect(stored['cam-hall|cam-mic']).toBe(1.5);
  });
});
