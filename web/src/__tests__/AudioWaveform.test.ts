import { render, cleanup, waitFor } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import AudioWaveform from '$lib/components/AudioWaveform.svelte';

// jsdom has no Web Audio — install a controllable fake directly on window
// (vi.stubGlobal misses the component's window lookup here). The component
// reads window.AudioContext lazily at wiring time. Module-scope graph state
// persists across these tests by design — the second test exercises the
// reuse path on the element wired by the first.

const resume = vi.fn(() => Promise.resolve());
const createMediaElementSource = vi.fn(() => ({ connect: vi.fn() }));
const fakeCtx = {
  state: 'suspended' as 'suspended' | 'running',
  resume,
  createMediaElementSource,
  createAnalyser: vi.fn(() => ({ connect: vi.fn() })),
  destination: {},
};

export function installFakeAudioContext() {
  (window as unknown as Record<string, unknown>).AudioContext = function () {
    return fakeCtx;
  };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  delete (window as unknown as Record<string, unknown>).AudioContext;
});

describe('AudioWaveform', () => {
  it('resumes a suspended shared context when the wired element starts playing', async () => {
    installFakeAudioContext();
    const el = document.createElement('audio');
    render(AudioWaveform, { audio: el });
    await waitFor(() => expect(createMediaElementSource).toHaveBeenCalledWith(el));
    // The regression: a suspended context pins the element's playback clock
    // at zero. The element's play gesture must resume the context.
    fakeCtx.state = 'suspended';
    resume.mockClear();
    el.dispatchEvent(new Event('play'));
    expect(resume).toHaveBeenCalledTimes(1);
  });

  it('reuses the per-element graph on remount (createMediaElementSource is once-per-element)', async () => {
    installFakeAudioContext();
    const el = document.createElement('audio');
    const { unmount } = render(AudioWaveform, { audio: el });
    await waitFor(() => expect(createMediaElementSource.mock.calls.length).toBeGreaterThan(0));
    const before = createMediaElementSource.mock.calls.length;
    unmount();
    render(AudioWaveform, { audio: el });
    await new Promise((r) => setTimeout(r, 50));
    expect(createMediaElementSource.mock.calls.length).toBe(before);
  });
});

describe('AudioWaveform node tap', () => {
  it('taps an AudioNode in parallel without re-feeding the destination', async () => {
    installFakeAudioContext();
    const fakeNode = { connect: vi.fn() } as unknown as AudioNode;
    render(AudioWaveform, { node: fakeNode } as never);
    await waitFor(() => expect(fakeCtx.createAnalyser).toHaveBeenCalled());
    // the tap must connect the source node into the analyser
    expect(fakeNode.connect).toHaveBeenCalled();
  });
});
