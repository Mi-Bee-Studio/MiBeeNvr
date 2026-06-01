/**
 * Web Worker for WebCodecs video decoding.
 *
 * Runs the VideoDecoder off the main thread to avoid blocking UI.
 * Receives codec info and NALUs via postMessage, sends decoded frames back.
 *
 * Message protocol (main → worker):
 *   { type: 'codec-info', data: { codec, profile, level, sps, pps, vps? } }
 *   { type: 'video-frame', data: { pts, isKeyframe, nalus } }
 *   { type: 'reset' }
 *   { type: 'close' }
 *
 * Message protocol (worker → main):
 *   { type: 'frame', data: VideoFrame }        — transferable frame for rendering
 *   { type: 'error', error: string }            — error notification
 *
 * NOTE: This file uses a plain module pattern (no ES imports in worker context).
 * The build system (Vite) will bundle this as a web worker entry.
 */

// ─── Inline imports (bundled by Vite) ────────────────────────────────────

import { Decoder } from './decoder';

// ─── Decoder state ─────────────────────────────────────────────────────────

let decoder: any = null;
let pendingFrames: { frame: any }[] = [];

// ─── Message handler ──────────────────────────────────────────────────────

self.onmessage = (event: MessageEvent) => {
  const msg = event.data;

  switch (msg.type) {
    case 'codec-info':
      handleCodecInfo(msg.data);
      break;

    case 'video-frame':
      handleVideoFrame(msg.data);
      break;

    case 'reset':
      handleReset();
      break;

    case 'close':
      handleClose();
      break;
  }
};

// ─── Handlers ────────────────────────────────────────────────────────────

async function handleCodecInfo(data: {
  codec: string;
  profile: number;
  level: number;
  sps: Uint8Array;
  pps: Uint8Array;
  vps?: Uint8Array;
}): Promise<void> {
  // Close existing decoder if any
  if (decoder) {
    decoder.close();
    decoder = null;
  }

  // Create new decoder
  decoder = new Decoder();
  if (!decoder) {
    self.postMessage({ type: 'error', error: 'Failed to create decoder' });
    return;
  }

  // Set frame output callback — forward to main thread
  decoder.onFrame((frame: any) => {
    try {
      self.postMessage({ type: 'frame', data: frame }, [frame] as any);
    } catch {
      // postMessage failed — frame still owned by worker, must close to prevent GPU leak
      try { frame.close(); } catch { /* already closed */ }
      throw new Error('Failed to transfer frame to main thread');
    }
  });

  // Set error callback — forward to main thread
  decoder.onError((err: Error) => {
    self.postMessage({ type: 'error', error: err.message });
  });

  try {
    await decoder.configure({
      codec: data.codec as any,
      profile: data.profile,
      level: data.level,
      sps: data.sps,
      pps: data.pps,
      vps: data.vps,
    });
  } catch (err: any) {
    self.postMessage({ type: 'error', error: err?.message || 'Codec configuration failed' });
  }
}

function handleVideoFrame(data: {
  pts: number;
  isKeyframe: boolean;
  nalus: Uint8Array[];
}): void {
  if (!decoder) return;

  try {
    decoder.decode(data.nalus, data.pts, data.isKeyframe);
  } catch (err: any) {
    self.postMessage({ type: 'error', error: err?.message || 'Decode failed' });
  }
}

function handleReset(): void {
  if (decoder) {
    decoder.reset();
  }
}

function handleClose(): void {
  if (decoder) {
    decoder.close();
    decoder = null;
  }
}
