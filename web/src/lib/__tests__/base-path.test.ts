import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { APP_BASE, activeBase, setWorkerBase, withBase } from '$lib/base-path';

// Web Workers have no `window`, so the __NVR_BASE__ bootstrap injected into
// index.html never reaches them (#942): the inference worker's root-absolute
// asset URLs (ORT bundle, wasm paths, model files) dropped the gateway prefix
// and 404'd behind the fnOS gateway. The main thread forwards the base with
// the init message and registers it via setWorkerBase(); withBase() must then
// resolve against it in worker context while staying on APP_BASE on the main
// thread.

const WIN = (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__;

afterEach(() => {
  // Unstub BEFORE touching window: the worker-context tests stub it away.
  vi.unstubAllGlobals();
  (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__ = WIN;
});

describe('withBase in main-thread context', () => {
  it('no injected bootstrap: URLs are unchanged', () => {
    (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__ = '';
    // APP_BASE is a module-load constant; activeBase()/withBase() re-read
    // window so the test does not need a module registry reset.
    expect(withBase('/models/yolo11n.onnx')).toBe('/models/yolo11n.onnx');
  });

  it('gateway deployment: URLs carry the injected prefix', () => {
    (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__ = '/app/mibee-nvr';
    expect(activeBase()).toBe('/app/mibee-nvr');
    expect(withBase('/ort/ort.all.bundle.min.mjs')).toBe('/app/mibee-nvr/ort/ort.all.bundle.min.mjs');
  });
});

describe('withBase in worker context', () => {
  beforeEach(() => {
    // Simulate a Web Worker: no window at all.
    vi.stubGlobal('window', undefined);
  });

  it('without registration the base is empty (no-prefix behavior)', () => {
    setWorkerBase(undefined);
    expect(activeBase()).toBe('');
    expect(withBase('/models/yolo11n.onnx')).toBe('/models/yolo11n.onnx');
  });

  it('registered base prefixes worker asset URLs', () => {
    setWorkerBase('/app/mibee-nvr');
    expect(activeBase()).toBe('/app/mibee-nvr');
    expect(withBase('/ort/ort.all.bundle.min.mjs')).toBe('/app/mibee-nvr/ort/ort.all.bundle.min.mjs');
    expect(withBase('/ort/')).toBe('/app/mibee-nvr/ort/');
    expect(withBase('/models/yolo11n.onnx')).toBe('/app/mibee-nvr/models/yolo11n.onnx');
  });

  it('rejects non-root-absolute values instead of trusting them', () => {
    setWorkerBase('http://evil.example/');
    expect(activeBase()).toBe('');
    setWorkerBase('app/mibee-nvr');
    expect(activeBase()).toBe('');
  });

  it('re-registration overrides the previous value', () => {
    setWorkerBase('/app/mibee-nvr');
    setWorkerBase('');
    expect(activeBase()).toBe('');
  });
});

describe('APP_BASE export', () => {
  it('stays the module-load constant for consumers that imported it directly', () => {
    (window as unknown as { __NVR_BASE__?: string }).__NVR_BASE__ = '/app/mibee-nvr';
    expect(APP_BASE).toBe(WIN ?? '');
  });
});
