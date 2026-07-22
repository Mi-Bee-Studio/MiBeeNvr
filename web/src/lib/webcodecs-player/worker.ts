/**
 * Web Worker for video decoding (WebCodecs + WASM H.265 fallback).
 *
 * Runs the decoder off the main thread to avoid blocking UI.
 * Receives codec info and NALUs via postMessage, sends decoded frames back.
 *
 * Message protocol (main → worker):
 *   { type: 'codec-info', data: { codec, profile, level, sps, pps, vps? } }
 *   { type: 'video-frame', data: { pts, isKeyframe, nalus } }
 *   { type: 'reset' }
 *   { type: 'close' }
 *
 * Message protocol (worker → main):
 *   { type: 'frame', data: VideoFrame }        — frame for rendering (transferable if GPU-backed)
 *   { type: 'error', error: string }            — error notification
 *
 * NOTE: WASM-decoded frames are CPU-backed (synthetic VideoFrame from RGBA).
 * These cannot be transferred — postMessage falls back to structured clone.
 * The worker does NOT close the frame after posting; main thread owns it.
 */

// ─── Inline imports (bundled by Vite) ────────────────────────────────────

import { Decoder } from './decoder';

// ─── Decoder state ─────────────────────────────────────────────────────────

let decoder: any = null;
let pendingFrames: { frame: any }[] = [];
let isMjpegMode = false;

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

  // MJPEG mode: no WebCodecs decoder needed. JPEG frames are decoded via
  // createImageBitmap in handleVideoFrame, bypassing the Decoder entirely.
  if (data.codec === 'mjpeg') {
    isMjpegMode = true;
    self.postMessage({ type: 'codec-ready' });
    return;
  }
  isMjpegMode = false;

  // Create new decoder
  decoder = new Decoder();
  if (!decoder) {
    self.postMessage({ type: 'error', error: 'Failed to create decoder' });
    return;
  }

  // Set frame output callback — forward to main thread
  // Tracks whether frames are from WASM (CPU-backed, not transferable)
  let isWasmMode = false;

  decoder.onFrame((frame: any) => {
    try {
      // GPU-backed VideoFrames (WebCodecs) can be transferred — zero-copy.
      // CPU-backed VideoFrames (WASM/RGBA) cannot be transferred — structured clone.
      if (isWasmMode) {
        // WASM frame: post without transfer list (structured clone copy)
        self.postMessage({ type: 'frame', data: frame });
        // Worker created the synthetic frame — close our reference after sending copy
        try {
          frame.close();
        } catch {
          /* already closed */
        }
      } else {
        // WebCodecs frame: transfer ownership (zero-copy)
        self.postMessage({ type: 'frame', data: frame }, [frame] as any);
      }
    } catch {
      // postMessage failed — frame still owned by worker, must close to prevent leak
      try {
        frame.close();
      } catch {
        /* already closed */
      }
      throw new Error('Failed to send frame to main thread');
    }
  });

  // Set error callback — forward to main thread
  decoder.onError((err: Error) => {
    self.postMessage({ type: 'error', error: err.message });
  });

  // Set backpressure callback — forward to main thread so ConnectionManager can skip frames
  decoder.onBackpressure((paused: boolean) => {
    self.postMessage({ type: 'backpressure', paused });
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
    isWasmMode = decoder.isWasm;
  } catch (err: any) {
    self.postMessage({ type: 'error', error: err?.message || 'Codec configuration failed' });
  }
}

function handleVideoFrame(data: { pts: number; isKeyframe: boolean; nalus: Uint8Array[] }): void {
  // MJPEG mode: decode JPEG via createImageBitmap (no WebCodecs decoder needed).
  // nalus[0] contains the complete JPEG image bytes.
  if (isMjpegMode) {
    if (data.nalus.length === 0 || data.nalus[0].length === 0) return;
    const jpegData = data.nalus[0];
    createImageBitmap(new Blob([jpegData], { type: 'image/jpeg' }))
      .then((bitmap) => {
        self.postMessage({ type: 'frame', data: { bitmap, pts: data.pts } }, [bitmap] as any);
      })
      .catch(() => {
        // Corrupt JPEG — silently skip (common during camera reconnection)
      });
    return;
  }

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
  isMjpegMode = false;
  if (decoder) {
    decoder.close();
    decoder = null;
  }
}
