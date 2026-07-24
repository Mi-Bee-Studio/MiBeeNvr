import { render, fireEvent, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import AviPlayback from '../AviPlayback.svelte';

// ── Mock modules ──
vi.mock('$lib/audio-player', () => {
  const mockPlayer = {
    init: vi.fn().mockResolvedValue(undefined),
    pushFrame: vi.fn(),
    setMuted: vi.fn(),
    set muted(v) {},
    get muted() {
      return false;
    },
    destroy: vi.fn(),
  };
  return {
    AudioPlayer: vi.fn().mockImplementation(function () {
      return mockPlayer;
    }),
    AudioCodec: { MuLaw: 0x01, ALaw: 0x02, Opus: 0x03, AAC: 0x04 },
  };
});

vi.mock('$lib/api', () => {
  return {
    getAuthHeader: vi.fn(() => 'Bearer mbs_testtoken.sig'),
    apiRequest: vi.fn(),
    API_BASE: '/api',
  };
});

describe('AviPlayback', () => {
  let mockSocket;

  class MockWebSocket {
    binaryType = 'arraybuffer';
    onopen = null;
    onmessage = null;
    onerror = null;
    onclose = null;
    readyState = 0;
    sentMessages = [];

    constructor(_url) {
      mockSocket = this;
      setTimeout(() => {
        this.readyState = 1;
        if (this.onopen) this.onopen(new Event('open'));
      }, 0);
    }

    close() {
      this.readyState = 3;
    }

    send(data) {
      this.sentMessages.push(data);
    }

    static get OPEN() {
      return 1;
    }

    sendBinaryFrame(type, pts, data) {
      const buf = new ArrayBuffer(13 + data.length);
      const view = new DataView(buf);
      view.setUint8(0, type);
      view.setBigUint64(1, BigInt(pts), true);
      view.setUint32(9, data.length, true);
      new Uint8Array(buf, 13, data.length).set(data);
      if (this.onmessage) {
        this.onmessage(new MessageEvent('message', { data: buf }));
      }
    }
  }

  beforeEach(() => {
    mockSocket = null;
    globalThis.WebSocket = MockWebSocket;
  });

  afterEach(() => {
    delete globalThis.WebSocket;
    vi.clearAllMocks();
    cleanup();
  });

  // ── Basic rendering ──

  it('renders play button with Play text', () => {
    const { container } = render(AviPlayback, {
      props: { recordingId: 'test-recording' },
    });
    const btn = container.querySelector('button');
    expect(btn).toBeTruthy();
    expect(btn.textContent).toContain('Play');
  });

  it('renders placeholder when no frame is loaded', () => {
    const { getByText } = render(AviPlayback, {
      props: { recordingId: 'test-recording' },
    });
    expect(getByText('No frame')).toBeTruthy();
  });

  // ── WebSocket connection ──

  it('connects to WS and sends play action on play click', async () => {
    const { container } = render(AviPlayback, {
      props: { recordingId: 'test-recording' },
    });

    await fireEvent.click(container.querySelector('button'));

    await vi.waitFor(() => {
      expect(mockSocket).toBeTruthy();
      expect(mockSocket.sentMessages.length).toBeGreaterThan(0);
      const playMsg = mockSocket.sentMessages.find((m) => typeof m === 'string' && m.includes('"play"'));
      expect(playMsg).toBeTruthy();
    });
  });

  // ── WebSocket video frame handling ──

  it('renders MJPEG frame in img element after receiving WS video message', async () => {
    const { container } = render(AviPlayback, {
      props: { recordingId: 'test-recording' },
    });

    // Click play to trigger WebSocket connection
    await fireEvent.click(container.querySelector('button'));

    await vi.waitFor(() => {
      expect(mockSocket).toBeTruthy();
    });

    // Send a minimal MJPEG frame (SOI + EOI markers)
    const jpegBytes = new Uint8Array([0xff, 0xd8, 0xff, 0xd9]);
    mockSocket.sendBinaryFrame(0x01, 0, jpegBytes);

    await vi.waitFor(() => {
      const img = container.querySelector('img');
      expect(img).toBeTruthy();
      expect(img.src).toBeTruthy();
      expect(img.src).not.toBe('');
      expect(img.src).toMatch(/^blob:/);
    });
  });

  // ── Audio frame handling (8kHz mandatory) ──

  it('creates AudioPlayer with 8000 Hz sample rate on audio WS message', async () => {
    const { AudioPlayer } = await import('$lib/audio-player');

    const { container } = render(AviPlayback, {
      props: { recordingId: 'test-recording' },
    });

    await fireEvent.click(container.querySelector('button'));

    await vi.waitFor(() => {
      expect(mockSocket).toBeTruthy();
    });

    // Send a G.711 μ-law audio frame (silence bytes)
    const audioData = new Uint8Array([0x80, 0x80, 0x80, 0x80]);
    mockSocket.sendBinaryFrame(0x02, 0, audioData);

    await vi.waitFor(() => {
      expect(AudioPlayer).toHaveBeenCalledWith(0x01, 8000, 1);
    });
  });

  it('shows audio badge after receiving audio frame', async () => {
    const { container } = render(AviPlayback, {
      props: { recordingId: 'test-recording' },
    });

    await fireEvent.click(container.querySelector('button'));

    await vi.waitFor(() => {
      expect(mockSocket).toBeTruthy();
    });

    const audioData = new Uint8Array([0x80, 0x80, 0x80, 0x80]);
    mockSocket.sendBinaryFrame(0x02, 0, audioData);

    await vi.waitFor(() => {
      const indicator = container.querySelector('[data-testid="audio-indicator"]');
      expect(indicator).toBeTruthy();
      expect(indicator.textContent).toContain('8000');
    });
  });

  // ── Play/Pause toggle ──

  it('toggles play state on button click', async () => {
    const { container } = render(AviPlayback, {
      props: { recordingId: 'test-recording' },
    });

    const btn = container.querySelector('button');

    // Start playback
    await fireEvent.click(btn);
    await vi.waitFor(() => {
      expect(mockSocket).toBeTruthy();
    });

    // Pause — button text should change to 'Play'
    await fireEvent.click(btn);
    await vi.waitFor(() => {
      expect(btn.textContent).toContain('Play');
    });
  });

  it('clears frame source on stop', async () => {
    const { container } = render(AviPlayback, {
      props: { recordingId: 'test-recording' },
    });

    const btn = container.querySelector('button');

    // Start and send a frame
    await fireEvent.click(btn);
    await vi.waitFor(() => expect(mockSocket).toBeTruthy());

    const jpegBytes = new Uint8Array([0xff, 0xd8, 0xff, 0xd9]);
    mockSocket.sendBinaryFrame(0x01, 0, jpegBytes);

    await vi.waitFor(() => {
      const img = container.querySelector('img');
      expect(img).toBeTruthy();
    });

    // Stop
    await fireEvent.click(btn);

    await vi.waitFor(() => {
      const img = container.querySelector('img');
      expect(img).toBeFalsy();
    });
  });

  // ── Error handling ──

  it('shows error when no recordingId provided', async () => {
    const { container } = render(AviPlayback);

    await fireEvent.click(container.querySelector('button'));

    await vi.waitFor(() => {
      const errEl = container.querySelector('[data-testid="error"]');
      expect(errEl).toBeTruthy();
      expect(errEl.textContent).toContain('No recording ID');
    });
  });
});
