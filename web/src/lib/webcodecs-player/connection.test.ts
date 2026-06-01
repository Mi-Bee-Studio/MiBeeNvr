import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { ConnectionManager } from './connection';
import { MsgType, encodeCodecInfo } from './protocol';

// ─── Mock WebSocket ────────────────────────────────────────────────────────

interface MockWSInstance {
  url: string;
  binaryType: string;
  onopen: (() => void) | null;
  onmessage: ((ev: MessageEvent) => void) | null;
  onclose: ((ev: CloseEvent) => void) | null;
  onerror: (() => void) | null;
  close: ReturnType<typeof vi.fn>;
  readyState: number;
  send: ReturnType<typeof vi.fn>;
}

const WS_OPEN = 1;
const WS_CLOSED = 3;

let mockWSInstances: MockWSInstance[] = [];

function createMockWSClass() {
  mockWSInstances = [];
  return class MockWebSocket {
    url: string;
    binaryType = '';
    onopen: (() => void) | null = null;
    onmessage: ((ev: MessageEvent) => void) | null = null;
    onclose: ((ev: CloseEvent) => void) | null = null;
    onerror: (() => void) | null = null;
    close = vi.fn(() => { this.readyState = WS_CLOSED; });
    readyState = WS_OPEN;
    send = vi.fn();

    constructor(url: string) {
      this.url = url;
      mockWSInstances.push(this as unknown as MockWSInstance);
    }

    static OPEN = WS_OPEN;
    static CLOSED = WS_CLOSED;
  };
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

function createManager(opts?: Partial<ConstructorParameters<typeof ConnectionManager>[0]>) {
  return new ConnectionManager({
    url: 'ws://localhost/stream',
    onStateChange: vi.fn(),
    onCodecInfo: vi.fn(),
    onFrame: vi.fn(),
    onFreezeFrame: vi.fn(),
    ...opts,
  });
}

function createMockCoordinator() {
  return {
    requestReconnect: vi.fn().mockReturnValue(1000),
    completeReconnect: vi.fn(),
    cancelRequest: vi.fn(),
    reportBackendPressure: vi.fn(),
    activeReconnects: 0,
    maxConcurrent: 2,
    globalBackoffMs: 1000,
    globalCooldown: false,
    dispose: vi.fn(),
  };
}

function getLastWS(): MockWSInstance {
  return mockWSInstances[mockWSInstances.length - 1];
}

function simulateOpen(ws?: MockWSInstance) {
  const target = ws ?? getLastWS();
  target.readyState = WS_OPEN;
  target.onopen?.();
}

function simulateMessage(ws: MockWSInstance, data: ArrayBuffer) {
  ws.onmessage?.(new MessageEvent('message', { data }));
}

function simulateClose(ws: MockWSInstance, code: number) {
  ws.readyState = WS_CLOSED;
  ws.onclose?.(new CloseEvent('close', { code }));
}

function simulateError(ws: MockWSInstance) {
  ws.onerror?.();
}

function buildCodecInfoBuffer(): ArrayBuffer {
  return encodeCodecInfo({
    codec: 'h264',
    profile: 0x42,
    level: 0x1E,
    sps: new Uint8Array([0x67, 0x42, 0xC0, 0x1E]),
    pps: new Uint8Array([0x68, 0xCE, 0x38, 0x80]),
  });
}

function buildVideoFrameBuffer(): ArrayBuffer {
  const buf = new ArrayBuffer(2);
  new Uint8Array(buf)[0] = MsgType.VideoFrame;
  new Uint8Array(buf)[1] = 0xFF;
  return buf;
}

// ─── Setup / Teardown ──────────────────────────────────────────────────────

beforeEach(() => {
  vi.useFakeTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  mockWSInstances = [];
  vi.stubGlobal('WebSocket', createMockWSClass());
  vi.stubGlobal('document', {
    hidden: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  });
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  mockWSInstances = [];
});

// ─── connect ───────────────────────────────────────────────────────────────

describe('connect', () => {
  it('should create a WebSocket with the configured URL', () => {
    const cm = createManager();
    cm.connect();
    expect(mockWSInstances.length).toBe(1);
    expect(getLastWS().url).toBe('ws://localhost/stream');
  });

  it('should append auth token as query param', () => {
    const cm = createManager({ authToken: 'secret123' });
    cm.connect();
    expect(getLastWS().url).toBe('ws://localhost/stream?token=secret123');
  });

  it('should URL-encode special characters in auth token', () => {
    const cm = createManager({ authToken: 'a b+c=d' });
    cm.connect();
    expect(getLastWS().url).toBe('ws://localhost/stream?token=a%20b%2Bc%3Dd');
  });

  it('should set binaryType to arraybuffer', () => {
    const cm = createManager();
    cm.connect();
    expect(getLastWS().binaryType).toBe('arraybuffer');
  });

  it('should transition to loading state on connect', () => {
    const cm = createManager();
    cm.connect();
    const onState = cm['_opts'].onStateChange as ReturnType<typeof vi.fn>;
    expect(onState).toHaveBeenCalledWith('loading');
  });

  it('should transition to buffering on WebSocket open', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    const onState = cm['_opts'].onStateChange as ReturnType<typeof vi.fn>;
    expect(onState).toHaveBeenCalledWith('buffering');
  });

  it('should not connect when destroyed', () => {
    const cm = createManager();
    cm.destroy();
    cm.connect();
    expect(mockWSInstances.length).toBe(0);
  });

  it('should not connect when URL is empty', () => {
    const cm = createManager({ url: '' });
    cm.connect();
    expect(mockWSInstances.length).toBe(0);
  });
});

// ─── disconnect ──────────────────────────────────────────────────────────────

describe('disconnect', () => {
  it('should close the WebSocket', () => {
    const cm = createManager();
    cm.connect();
    const ws = getLastWS();
    cm.disconnect();
    expect(ws.close).toHaveBeenCalled();
  });

  it('should cancel pending coordinator reconnect timer', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(1000);
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    // Coordinator grants slot with 1000ms delay — timer is set
    expect(coordinator.requestReconnect).toHaveBeenCalledWith('cam-1', expect.any(Function));
    cm.disconnect();
    // Timer should be cancelled — advancing time should not create new WS
    vi.advanceTimersByTime(60000);
    expect(mockWSInstances.length).toBe(1);
  });

  it('should call coordinator.cancelRequest on disconnect', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(-1); // queued
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    expect(coordinator.requestReconnect).toHaveBeenCalled();
    cm.disconnect();
    expect(coordinator.cancelRequest).toHaveBeenCalledWith('cam-1');
  });

  it('should stop zombie detection', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    cm.disconnect();
    vi.advanceTimersByTime(10000);
    expect((cm as { _zombieCheckTimer: ReturnType<typeof setInterval> | null })._zombieCheckTimer).toBeNull();
  });

  it('should be safe to call when not connected', () => {
    const cm = createManager();
    expect(() => cm.disconnect()).not.toThrow();
  });
});

// ─── reconnect ─────────────────────────────────────────────────────────────

describe('reconnect', () => {
  it('should call onFreezeFrame', () => {
    const freezeFn = vi.fn();
    const cm = createManager({ onFreezeFrame: freezeFn });
    cm.connect();
    simulateOpen();
    cm.reconnect();
    expect(freezeFn).toHaveBeenCalled();
  });

  it('should close existing WebSocket and create a new one', () => {
    const cm = createManager();
    cm.connect();
    const firstWS = getLastWS();
    simulateOpen();
    cm.reconnect();
    expect(firstWS.close).toHaveBeenCalled();
    expect(mockWSInstances.length).toBe(2);
    expect(getLastWS().url).toBe('ws://localhost/stream');
  });

  it('should transition through loading then buffering', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    cm.reconnect();
    const onState = cm['_opts'].onStateChange as ReturnType<typeof vi.fn>;
    const calls = onState.mock.calls.map((c: unknown[]) => c[0]);
    expect(calls).toContain('loading');
    simulateOpen();
    expect(onState).toHaveBeenCalledWith('buffering');
  });

  it('should use coordinator for reconnect', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(1000);
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    cm.reconnect();
    expect(coordinator.requestReconnect).toHaveBeenCalledWith('cam-1', expect.any(Function));
  });
});

// ─── destroy ──────────────────────────────────────────────────────────────

describe('destroy', () => {
  it('should close the WebSocket', () => {
    const cm = createManager();
    cm.connect();
    const ws = getLastWS();
    cm.destroy();
    expect(ws.close).toHaveBeenCalled();
  });

  it('should cancel coordinator reconnect timer', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(1000);
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    expect(coordinator.requestReconnect).toHaveBeenCalled();
    cm.destroy();
    vi.advanceTimersByTime(60000);
    expect(mockWSInstances.length).toBe(1);
  });

  it('should call coordinator.cancelRequest on destroy', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(-1); // queued
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    cm.destroy();
    expect(coordinator.cancelRequest).toHaveBeenCalledWith('cam-1');
  });

  it('should stop zombie detection', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    cm.destroy();
    expect((cm as { _zombieCheckTimer: ReturnType<typeof setInterval> | null })._zombieCheckTimer).toBeNull();
  });

  it('should remove visibility handler', () => {
    const cm = createManager();
    cm.connect();
    cm.destroy();
    expect(document.removeEventListener).toHaveBeenCalledWith(
      'visibilitychange',
      expect.any(Function),
    );
  });

  it('should prevent any further operations', () => {
    const cm = createManager();
    cm.destroy();
    cm.connect();
    expect(mockWSInstances.length).toBe(0);
  });

  it('should be safe to call multiple times (double destroy)', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    cm.destroy();
    cm.destroy();
    expect(getLastWS().close).toHaveBeenCalledTimes(1);
  });

  it('should close WebSocket immediately on onopen after destroy', () => {
    const cm = createManager();
    cm.connect();
    cm.destroy();
    simulateOpen();
    expect(getLastWS().close).toHaveBeenCalled();
  });

  it('should not transition state on onopen after destroy', () => {
    const cm = createManager();
    cm.connect();
    cm.destroy();
    simulateOpen();
    const onState = cm['_opts'].onStateChange as ReturnType<typeof vi.fn>;
    expect(onState).not.toHaveBeenCalledWith('buffering');
  });
});

// ─── Callbacks ──────────────────────────────────────────────────────────────

describe('callbacks', () => {
  describe('onStateChange', () => {
    it('should be called with loading on connect', () => {
      const fn = vi.fn();
      const cm = createManager({ onStateChange: fn });
      cm.connect();
      expect(fn).toHaveBeenCalledWith('loading');
    });

    it('should be called with buffering on open', () => {
      const fn = vi.fn();
      const cm = createManager({ onStateChange: fn });
      cm.connect();
      simulateOpen();
      expect(fn).toHaveBeenCalledWith('buffering');
    });

    it('should be called with playing on first video frame', () => {
      const fn = vi.fn();
      const cm = createManager({ onStateChange: fn });
      cm.connect();
      simulateOpen();
      simulateMessage(getLastWS(), buildVideoFrameBuffer());
      expect(fn).toHaveBeenCalledWith('playing');
    });

    it('should not call playing again for subsequent frames', () => {
      const fn = vi.fn();
      const cm = createManager({ onStateChange: fn });
      cm.connect();
      simulateOpen();
      simulateMessage(getLastWS(), buildVideoFrameBuffer());
      simulateMessage(getLastWS(), buildVideoFrameBuffer());
      expect(fn).toHaveBeenCalledWith('playing');
      expect(fn).toHaveBeenCalledTimes(3);
    });

    it('should be called with disconnected on normal close (1000)', () => {
      const fn = vi.fn();
      const cm = createManager({ onStateChange: fn });
      cm.connect();
      simulateOpen();
      simulateClose(getLastWS(), 1000);
      expect(fn).toHaveBeenCalledWith('disconnected');
    });

    it('should be called with error on WebSocket error', () => {
      const fn = vi.fn();
      const cm = createManager({ onStateChange: fn });
      cm.connect();
      simulateOpen();
      simulateError(getLastWS());
      expect(fn).toHaveBeenCalledWith('error');
    });
  });

  describe('onCodecInfo', () => {
    it('should be called when CodecInfo message received', () => {
      const fn = vi.fn();
      const cm = createManager({ onCodecInfo: fn });
      cm.connect();
      simulateOpen();
      simulateMessage(getLastWS(), buildCodecInfoBuffer());
      expect(fn).toHaveBeenCalledTimes(1);
      const ci = fn.mock.calls[0][0];
      expect(ci.codec).toBe('h264');
      expect(ci.profile).toBe(0x42);
      expect(ci.level).toBe(0x1E);
    });
  });

  describe('onFrame', () => {
    it('should be called when VideoFrame message received', () => {
      const fn = vi.fn();
      const cm = createManager({ onFrame: fn });
      cm.connect();
      simulateOpen();
      const frameData = buildVideoFrameBuffer();
      simulateMessage(getLastWS(), frameData);
      expect(fn).toHaveBeenCalledWith(frameData);
    });

    it('should not call onFrame for empty ArrayBuffer', () => {
      const fn = vi.fn();
      const cm = createManager({ onFrame: fn });
      cm.connect();
      simulateOpen();
      simulateMessage(getLastWS(), new ArrayBuffer(0));
      expect(fn).not.toHaveBeenCalled();
    });

    it('should not call onFrame for non-ArrayBuffer data', () => {
      const fn = vi.fn();
      const cm = createManager({ onFrame: fn });
      cm.connect();
      simulateOpen();
      const ws = getLastWS();
      ws.onmessage?.(new MessageEvent('message', { data: 'text' }));
      expect(fn).not.toHaveBeenCalled();
    });

    it('should not call onFrame after destroy', () => {
      const fn = vi.fn();
      const cm = createManager({ onFrame: fn });
      cm.connect();
      simulateOpen();
      cm.destroy();
      simulateMessage(getLastWS(), buildVideoFrameBuffer());
      expect(fn).not.toHaveBeenCalled();
    });
  });

  describe('onFreezeFrame', () => {
    it('should be called on abnormal close', () => {
      const fn = vi.fn();
      const cm = createManager({ onFreezeFrame: fn });
      cm.connect();
      simulateOpen();
      simulateClose(getLastWS(), 1006);
      expect(fn).toHaveBeenCalled();
    });

    it('should not be called on normal close (1000)', () => {
      const fn = vi.fn();
      const cm = createManager({ onFreezeFrame: fn });
      cm.connect();
      simulateOpen();
      simulateClose(getLastWS(), 1000);
      expect(fn).not.toHaveBeenCalled();
    });

    it('should not be called on normal close (1001)', () => {
      const fn = vi.fn();
      const cm = createManager({ onFreezeFrame: fn });
      cm.connect();
      simulateOpen();
      simulateClose(getLastWS(), 1001);
      expect(fn).not.toHaveBeenCalled();
    });
  });
});

// ─── Reconnect behavior (no coordinator) ────────────────────────────────────

describe('reconnect behavior (no coordinator)', () => {
  it('should not reconnect on normal close (code 1000)', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1000);
    vi.advanceTimersByTime(60000);
    expect(mockWSInstances.length).toBe(1);
  });

  it('should not reconnect on normal close (code 1001)', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1001);
    vi.advanceTimersByTime(60000);
    expect(mockWSInstances.length).toBe(1);
  });

  it('should reconnect immediately on abnormal close (code 1006)', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    // Without coordinator, reconnect is immediate (no timer delay)
    expect(mockWSInstances.length).toBe(2);
  });

  it('should not process close events after destroy', () => {
    const fn = vi.fn();
    const cm = createManager({ onFreezeFrame: fn });
    cm.connect();
    simulateOpen();
    cm.destroy();
    simulateClose(getLastWS(), 1006);
    expect(fn).not.toHaveBeenCalled();
  });

  it('should not process error events after destroy', () => {
    const fn = vi.fn();
    const cm = createManager({ onStateChange: fn });
    cm.connect();
    simulateOpen();
    cm.destroy();
    simulateError(getLastWS());
    expect(fn).not.toHaveBeenCalledWith('error');
  });
});

// ─── Reconnect behavior (with coordinator) ──────────────────────────────────

describe('reconnect behavior (with coordinator)', () => {
  it('should request coordinator slot on abnormal close', () => {
    const coordinator = createMockCoordinator();
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    expect(coordinator.requestReconnect).toHaveBeenCalledWith('cam-1', expect.any(Function));
  });

  it('should reconnect after coordinator grants slot with delay', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(2000);
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    // Should not reconnect immediately — waiting for coordinator delay
    expect(mockWSInstances.length).toBe(1);
    vi.advanceTimersByTime(1999);
    expect(mockWSInstances.length).toBe(1);
    vi.advanceTimersByTime(1);
    expect(mockWSInstances.length).toBe(2);
  });

  it('should reconnect when coordinator fires onGranted callback (queued)', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(-1); // queued
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    // Queued — no immediate reconnect
    expect(mockWSInstances.length).toBe(1);
    // Simulate coordinator granting slot
    const onGranted = coordinator.requestReconnect.mock.calls[0][1] as (delayMs: number) => void;
    onGranted(3000);
    vi.advanceTimersByTime(3000);
    expect(mockWSInstances.length).toBe(2);
  });

  it('should call completeReconnect when connection opens after coordinated reconnect', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(1000);
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    vi.advanceTimersByTime(1000);
    simulateOpen();
    expect(coordinator.completeReconnect).toHaveBeenCalledWith('cam-1');
  });

  it('should not call completeReconnect on first connect (no coordinated reconnect)', () => {
    const coordinator = createMockCoordinator();
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    expect(coordinator.completeReconnect).not.toHaveBeenCalled();
  });

  it('should cancel coordinator request when manually reconnecting', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(-1); // queued
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    expect(coordinator.cancelRequest).not.toHaveBeenCalled();
    // Manual reconnect should cancel previous request
    cm.reconnect();
    expect(coordinator.cancelRequest).toHaveBeenCalledWith('cam-1');
    expect(coordinator.requestReconnect).toHaveBeenCalledTimes(2);
  });

  it('should not reconnect when coordinator is at capacity (queued)', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(-1);
    const cm = createManager({ coordinator, cameraId: 'cam-1' });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    vi.advanceTimersByTime(60000);
    // Still only 1 WS — coordinator hasn't granted slot
    expect(mockWSInstances.length).toBe(1);
  });
});

// ─── Zombie detection ───────────────────────────────────────────────────────

describe('zombie detection', () => {
  it('should not trigger zombie when frames arrive regularly', () => {
    const cm = createManager({
      zombieCheckInterval: 2000,
      zombieMaxMisses: 3,
    });
    cm.connect();
    simulateOpen();

    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    vi.advanceTimersByTime(1500);
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    vi.advanceTimersByTime(1500);
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    vi.advanceTimersByTime(1500);

    expect(mockWSInstances.length).toBe(1);
  });

  it('should auto-reconnect immediately after zombie detection (no coordinator)', () => {
    const cm = createManager({
      zombieCheckInterval: 2000,
      zombieMaxMisses: 3,
    });
    cm.connect();
    simulateOpen();

    vi.advanceTimersByTime(2000);
    expect(mockWSInstances.length).toBe(1);

    vi.advanceTimersByTime(2000);
    expect(mockWSInstances.length).toBe(1);

    vi.advanceTimersByTime(2000);
    // Without coordinator, zombie triggers immediate reconnect
    expect(mockWSInstances.length).toBe(2);
  });

  it('should use coordinator for zombie-triggered reconnect', () => {
    const coordinator = createMockCoordinator();
    coordinator.requestReconnect.mockReturnValue(1500);
    const cm = createManager({
      coordinator,
      cameraId: 'cam-1',
      zombieCheckInterval: 2000,
      zombieMaxMisses: 3,
    });
    cm.connect();
    simulateOpen();

    vi.advanceTimersByTime(6000);
    // Zombie detected, coordinator should be asked
    expect(coordinator.requestReconnect).toHaveBeenCalledWith('cam-1', expect.any(Function));
    // Not yet reconnected — waiting for coordinator delay
    expect(mockWSInstances.length).toBe(1);
    vi.advanceTimersByTime(1500);
    expect(mockWSInstances.length).toBe(2);
  });

  it('should call onFreezeFrame before zombie reconnect', () => {
    const fn = vi.fn();
    const cm = createManager({
      onFreezeFrame: fn,
      zombieCheckInterval: 2000,
      zombieMaxMisses: 3,
    });
    cm.connect();
    simulateOpen();
    vi.advanceTimersByTime(6000);
    expect(fn).toHaveBeenCalled();
  });

  it('should reset zombie miss count when frame arrives', () => {
    const cm = createManager({
      zombieCheckInterval: 2000,
      zombieMaxMisses: 3,
    });
    cm.connect();
    simulateOpen();

    // Miss 1
    vi.advanceTimersByTime(2000);

    // Frame arrives — reset miss count
    simulateMessage(getLastWS(), buildVideoFrameBuffer());

    // Two more misses (total from reset: 2)
    vi.advanceTimersByTime(2000);
    vi.advanceTimersByTime(2000);

    expect(mockWSInstances.length).toBe(1);

    // Third miss from reset — triggers zombie
    vi.advanceTimersByTime(2000);
    expect(mockWSInstances.length).toBe(2);
  });

  it('should stop zombie detection when WebSocket closes', () => {
    const cm = createManager({
      zombieCheckInterval: 2000,
      zombieMaxMisses: 3,
    });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1000);
    expect((cm as { _zombieCheckTimer: ReturnType<typeof setInterval> | null })._zombieCheckTimer).toBeNull();
  });

  it('should not run zombie check when WebSocket is not open', () => {
    const cm = createManager({
      zombieCheckInterval: 2000,
      zombieMaxMisses: 3,
    });
    cm.connect();
    simulateOpen();
    getLastWS().readyState = 0;
    vi.advanceTimersByTime(6000);
    expect(mockWSInstances.length).toBe(1);
  });

  it('should not run zombie check after destroy', () => {
    const cm = createManager({
      zombieCheckInterval: 2000,
      zombieMaxMisses: 3,
    });
    cm.connect();
    simulateOpen();
    cm.destroy();
    vi.advanceTimersByTime(6000);
    expect(mockWSInstances.length).toBe(1);
  });
});

// ─── Visibility change ──────────────────────────────────────────────────────

describe('visibility change', () => {
  it('should reconnect when tab becomes visible after being hidden', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();

    const handler = (document.addEventListener as ReturnType<typeof vi.fn>).mock.calls.find(
      (c: unknown[]) => c[0] === 'visibilitychange',
    )?.[1] as (() => void) | undefined;

    (document as { hidden: boolean }).hidden = true;
    handler?.();
    expect(mockWSInstances.length).toBe(1);

    const firstWS = getLastWS();
    (document as { hidden: boolean }).hidden = false;
    handler?.();
    expect(firstWS.close).toHaveBeenCalled();
    expect(mockWSInstances.length).toBe(2);
  });

  it('should call onFreezeFrame when tab returns to visible', () => {
    const fn = vi.fn();
    const cm = createManager({ onFreezeFrame: fn });
    cm.connect();
    simulateOpen();

    const handler = (document.addEventListener as ReturnType<typeof vi.fn>).mock.calls.find(
      (c: unknown[]) => c[0] === 'visibilitychange',
    )?.[1] as (() => void) | undefined;

    (document as { hidden: boolean }).hidden = true;
    handler?.();
    (document as { hidden: boolean }).hidden = false;
    handler?.();

    expect(fn).toHaveBeenCalled();
  });

  it('should not reconnect if tab was never hidden', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();

    const handler = (document.addEventListener as ReturnType<typeof vi.fn>).mock.calls.find(
      (c: unknown[]) => c[0] === 'visibilitychange',
    )?.[1] as (() => void) | undefined;

    (document as { hidden: boolean }).hidden = false;
    handler?.();

    expect(mockWSInstances.length).toBe(1);
  });

  it('should not reconnect when tab becomes visible after destroy', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    cm.destroy();

    const handler = (document.addEventListener as ReturnType<typeof vi.fn>).mock.calls.find(
      (c: unknown[]) => c[0] === 'visibilitychange',
    )?.[1] as (() => void) | undefined;

    (document as { hidden: boolean }).hidden = true;
    handler?.();
    (document as { hidden: boolean }).hidden = false;
    handler?.();

    expect(mockWSInstances.length).toBe(1);
  });
});

// ─── Edge cases ─────────────────────────────────────────────────────────────

describe('edge cases', () => {
  it('should ignore empty ArrayBuffer message', () => {
    const fn = vi.fn();
    const cm = createManager({ onFrame: fn });
    cm.connect();
    simulateOpen();
    simulateMessage(getLastWS(), new ArrayBuffer(0));
    expect(fn).not.toHaveBeenCalled();
  });

  it('should ignore message with unknown type byte', () => {
    const fn = vi.fn();
    const cm = createManager({ onFrame: fn, onCodecInfo: vi.fn() });
    cm.connect();
    simulateOpen();
    const buf = new ArrayBuffer(1);
    new Uint8Array(buf)[0] = 0xFF;
    simulateMessage(getLastWS(), buf);
    expect(fn).not.toHaveBeenCalled();
  });

  it('should not crash on malformed CodecInfo (too short)', () => {
    const codecFn = vi.fn();
    const cm = createManager({ onCodecInfo: codecFn });
    cm.connect();
    simulateOpen();
    const buf = new ArrayBuffer(2);
    new Uint8Array(buf)[0] = MsgType.CodecInfo;
    simulateMessage(getLastWS(), buf);
    expect(codecFn).not.toHaveBeenCalled();
  });

  it('should handle destroy during connect (before open fires)', () => {
    const cm = createManager();
    cm.connect();
    cm.destroy();
    simulateOpen();
    expect(getLastWS().close).toHaveBeenCalled();
  });

  it('should handle double destroy', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    cm.destroy();
    cm.destroy();
    expect(getLastWS().close).toHaveBeenCalledTimes(1);
  });

  it('should handle WebSocket constructor throwing', () => {
    const errorFn = vi.fn();
    vi.stubGlobal('WebSocket', class {
      constructor() { throw new Error('WebSocket not supported'); }
    });
    const cm = createManager({ onStateChange: errorFn });
    cm.connect();
    expect(errorFn).toHaveBeenCalledWith('error');
  });

  it('should not use coordinator when neither coordinator nor cameraId provided', () => {
    const coordinator = createMockCoordinator();
    // Pass coordinator but no cameraId — should fall back to immediate reconnect
    const cm = createManager({ coordinator });
    cm.connect();
    simulateOpen();
    simulateClose(getLastWS(), 1006);
    // Should reconnect immediately without calling coordinator
    expect(coordinator.requestReconnect).not.toHaveBeenCalled();
    expect(mockWSInstances.length).toBe(2);
  });
});

// ─── Backpressure ───────────────────────────────────────────────────────

describe('backpressure', () => {
  it('should pass frames to onFrame when not paused', () => {
    const frameFn = vi.fn();
    const cm = createManager({ onFrame: frameFn });
    cm.connect();
    simulateOpen();
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    expect(frameFn).toHaveBeenCalledTimes(1);
  });

  it('should skip video frames when paused', () => {
    const frameFn = vi.fn();
    const cm = createManager({ onFrame: frameFn });
    cm.connect();
    simulateOpen();

    cm.setPaused(true);
    expect(cm.paused).toBe(true);

    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    simulateMessage(getLastWS(), buildVideoFrameBuffer());

    expect(frameFn).not.toHaveBeenCalled();
    expect(cm.frameDropCount).toBe(3);
  });

  it('should resume passing frames when unpaused', () => {
    const frameFn = vi.fn();
    const cm = createManager({ onFrame: frameFn });
    cm.connect();
    simulateOpen();

    cm.setPaused(true);
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    expect(frameFn).not.toHaveBeenCalled();

    cm.setPaused(false);
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    expect(frameFn).toHaveBeenCalledTimes(1);
  });

  it('should call onFrameDrop callback when frames are dropped', () => {
    const dropFn = vi.fn();
    const cm = createManager({ onFrameDrop: dropFn });
    cm.connect();
    simulateOpen();

    cm.setPaused(true);
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    expect(dropFn).toHaveBeenCalledWith(1);

    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    expect(dropFn).toHaveBeenCalledWith(2);

    expect(dropFn).toHaveBeenCalledTimes(2);
  });

  it('should still process CodecInfo messages when paused', () => {
    const codecFn = vi.fn();
    const frameFn = vi.fn();
    const cm = createManager({ onCodecInfo: codecFn, onFrame: frameFn });
    cm.connect();
    simulateOpen();

    cm.setPaused(true);
    simulateMessage(getLastWS(), buildCodecInfoBuffer());
    simulateMessage(getLastWS(), buildVideoFrameBuffer());

    expect(codecFn).toHaveBeenCalledTimes(1);
    expect(frameFn).not.toHaveBeenCalled();
    expect(cm.frameDropCount).toBe(1);
  });

  it('should reset paused state on disconnect', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    cm.setPaused(true);
    expect(cm.paused).toBe(true);

    cm.disconnect();
    expect(cm.paused).toBe(false);
  });

  it('should reset paused state on destroy', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();
    cm.setPaused(true);
    expect(cm.paused).toBe(true);

    cm.destroy();
    expect(cm.paused).toBe(false);
  });

  it('should preserve frame drop count across pause/resume cycles', () => {
    const cm = createManager();
    cm.connect();
    simulateOpen();

    cm.setPaused(true);
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    expect(cm.frameDropCount).toBe(2);

    cm.setPaused(false);
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    expect(cm.frameDropCount).toBe(2); // No new drops

    cm.setPaused(true);
    simulateMessage(getLastWS(), buildVideoFrameBuffer());
    expect(cm.frameDropCount).toBe(3);
  });
});
