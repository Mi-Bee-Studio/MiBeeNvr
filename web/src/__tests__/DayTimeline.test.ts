import { render, cleanup, fireEvent } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import DayTimeline from '$lib/components/DayTimeline.svelte';

vi.mock('lucide-svelte', () => ({
  Clock: function MockIcon() {
    return document.createElement('span');
  },
}));

// t() echoes the key — assertions match on key fragments.
vi.mock('$lib/i18n', () => ({
  t: (k: string) => k,
}));

const DAY = '2026-10-01';

// Local-time ISO strings pair with parseDayStart's local-midnight parsing, so
// the tests are timezone-independent.
function at(h: number, m: number): string {
  return `2026-10-01T${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:00`;
}

function seg(id: string, cameraId: string, start: string, end: string, format = 'h264') {
  const duration = (new Date(end).getTime() - new Date(start).getTime()) / 1000;
  return { id, camera_id: cameraId, started_at: start, ended_at: end, duration, format, merge_status: '' };
}

const videoCam = { id: 'cam-door', name: 'Door', encoding: 'h264' } as never;
const micCam = {
  id: 'cam-mic',
  name: 'Room mic',
  encoding: 'audio',
  audio_link_camera_id: 'cam-door',
} as never;
const yardCam = { id: 'cam-yard', name: 'Yard', encoding: 'h264' } as never;

function baseRecordings() {
  return [
    seg('rec-door-1', 'cam-door', at(8, 0), at(9, 0)),
    seg('rec-aud-1', 'cam-mic', at(8, 10), at(8, 20), 'audio'),
    seg('rec-yard-1', 'cam-yard', at(10, 0), at(10, 30)),
  ];
}

function baseProps() {
  return {
    cameras: [videoCam, micCam, yardCam],
    recordings: baseRecordings(),
    selectedDate: DAY,
    onseek: vi.fn(),
  };
}

describe('DayTimeline linked audio track (#963)', () => {
  afterEach(cleanup);

  it('renders the linked audio source segments as an audio strip on the video camera row', () => {
    const { container } = render(DayTimeline, { props: baseProps() });
    const strips = container.querySelectorAll('.audio-strip');
    expect(strips.length).toBe(1);
    // 08:10→08:20 on the 24h axis: left = 29400/86400, width = 600/86400
    expect(strips[0].getAttribute('style')).toContain('left: 34.02');
    expect(strips[0].getAttribute('style')).toContain('width: 0.69');
    // The strip opens the audio recording itself.
    expect(strips[0].getAttribute('title')).toContain('Room mic');
    expect(strips[0].getAttribute('title')).toContain('library.legendAudio');
  });

  it('styles the audio source own-row bands as audio (not video-blue)', () => {
    const { container } = render(DayTimeline, { props: baseProps() });
    const audioBand = container.querySelector('.band-audio');
    expect(audioBand).toBeTruthy();
    // 08:10→08:20 on the 24h axis: left = 29400/86400
    expect(audioBand!.getAttribute('style')).toContain('left: 34.02');
    // The seek path for audio bands is the shared band-click flow (geometry
    // needs a real layout, which jsdom lacks); strip-click covers onseek.
  });

  it('fires onseek(recordingId, 0) when an audio strip is clicked', async () => {
    const props = baseProps();
    const { container } = render(DayTimeline, { props });
    await fireEvent.click(container.querySelector('.audio-strip')!);
    expect(props.onseek).toHaveBeenCalledWith('rec-aud-1', 0);
  });

  it('renders no audio strips when the source is unlinked', () => {
    const props = baseProps();
    props.cameras = [videoCam, { ...micCam, audio_link_camera_id: undefined } as never, yardCam];
    const { container } = render(DayTimeline, { props });
    expect(container.querySelectorAll('.audio-strip').length).toBe(0);
    // The mic's own row still shows its bands.
    expect(container.querySelector('.band-audio')).toBeTruthy();
  });

  it('shows the audio track on a video camera with no video recordings that day', () => {
    const props = baseProps();
    props.recordings = [seg('rec-aud-1', 'cam-mic', at(8, 10), at(8, 20), 'audio')];
    const { container } = render(DayTimeline, { props });
    expect(container.querySelectorAll('.audio-strip').length).toBe(1);
  });
});
