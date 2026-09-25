import { render, cleanup, waitFor } from '@testing-library/svelte';
import { describe, it, expect, afterEach, vi } from 'vitest';
import MjpegSequencePlayer from '$lib/components/MjpegSequencePlayer.svelte';

// JSDOM has no ImageBitmap; stub with a minimal bitmap shape (the component
// only reads width/height and calls close()).
const fakeBitmap = { width: 320, height: 240, close: () => {} };
vi.stubGlobal('createImageBitmap', () => Promise.resolve(fakeBitmap));

function jpegBlob(): Blob {
  return new Blob([new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 0, 0])], { type: 'image/jpeg' });
}

function makeBatch(offset: number, n: number, total: number) {
  return {
    frames: Array.from({ length: n }, () => jpegBlob()),
    total,
    offset,
    fps: null,
  };
}

describe('MjpegSequencePlayer', () => {
  afterEach(() => cleanup());

  it('paints the first frame once the initial batch resolves — onMount must not mark it stale', async () => {
    // Regression: onMount used to replace the AbortController that the
    // resetKey effect had already created, so the first in-flight batch was
    // dropped as a stale sequence: blob cache stayed empty and the canvas
    // showed an endless spinner until the user manually stepped frames.
    let release: (() => void) | null = null;
    const gate = new Promise<void>((r) => { release = r; });
    const fetchBatch = vi.fn(() => gate.then(() => makeBatch(0, 2, 2)));

    const { container } = render(MjpegSequencePlayer, {
      props: { frameCount: 2, fps: 10, fetchBatch, showControls: false, resetKey: 'seq-1' },
    });

    // Let the resetKey effect dispatch the first batch and onMount settle
    // while the response is still pending (reproduces the race).
    await new Promise((r) => setTimeout(r, 30));
    expect(fetchBatch).toHaveBeenCalled();
    release!();

    await waitFor(() => {
      const canvas = container.querySelector('canvas');
      expect(canvas?.width).toBe(320);
      expect(canvas?.height).toBe(240);
    });
  });

  it('refetches and paints after a resetKey change (soft segment switch)', async () => {
    const fetchBatch = vi.fn()
      .mockImplementationOnce(() => Promise.resolve(makeBatch(0, 2, 2)))
      .mockImplementation(() => Promise.resolve(makeBatch(0, 3, 3)));
    const { container, rerender } = render(MjpegSequencePlayer, {
      props: { frameCount: 2, fps: 10, fetchBatch, showControls: false, resetKey: 'seq-A' },
    });
    await waitFor(() => {
      expect(container.querySelector('canvas')?.width).toBe(320);
    });
    await rerender({ props: { frameCount: 3, fps: 10, fetchBatch, showControls: false, resetKey: 'seq-B' } });
    await waitFor(() => {
      expect(fetchBatch.mock.calls.length).toBeGreaterThanOrEqual(2);
    });
  });
});
