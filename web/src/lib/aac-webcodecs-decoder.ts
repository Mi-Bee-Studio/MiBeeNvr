/**
 * WebCodecs AudioDecoder backend for AAC live preview.
 *
 * Wraps the browser-native `AudioDecoder` (part of the WebCodecs API) to
 * decode raw AAC access units produced by the recorder's
 * `rtpmpeg4audio.Decoder` and forwarded over the `?audio_only=1` WebSocket.
 *
 * The recorder sends raw AAC frames (no ADTS header), and the
 * AudioSpecificConfig (AASC) arrives in the AudioCodecInfo.config field. The
 * AASC is required by `AudioDecoder.configure()` as the `description`.
 *
 * WebCodecs requires a secure context (HTTPS or localhost). On plain-HTTP LAN
 * deployments `AudioDecoder` is undefined; callers must fall back to the WASM
 * decoder (`./aac-wasm-decoder`).
 */

import type { AudioData } from './aac-types';

/**
 * Adapt a native WebCodecs `AudioData` into the shared plain-object form so
 * the AudioPlayer consumer is identical across WebCodecs / WASM backends.
 * Copies the PCM out of the GPU/heap buffer and closes the native handle.
 */
export function adaptNativeAudioData(native: globalThis.AudioData): AudioData {
  const numberOfChannels = native.numberOfChannels;
  const numberOfFrames = native.numberOfFrames;
  const channelData: Float32Array[] = [];
  for (let c = 0; c < numberOfChannels; c++) {
    const out = new Float32Array(numberOfFrames);
    native.copyTo(out, { planeIndex: c });
    channelData.push(out);
  }
  const sampleRate = native.sampleRate;
  const timestamp = native.timestamp;
  native.close();
  return { channelData, numberOfFrames, sampleRate, numberOfChannels, timestamp, close() {} };
}

/** Sampling frequency index table (ISO 14496-3 §1.6.3.3), for aascSampleRate(). */
const AAC_SAMPLE_RATE_INDEX = [
  96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
  16000, 12000, 11025, 8000, 7350,
];

/**
 * Map an AudioSpecificConfig to its WebCodecs codec string by reading the
 * `audioObjectType` from the first 5 bits.
 *
 *   2  → mp4a.40.2  (AAC-LC, the overwhelmingly common case)
 *   5  → mp4a.40.5  (HE-AAC v1 / SBR)
 *   29 → mp4a.40.29 (HE-AAC v2 / PS)
 *
 * Unknown types fall back to AAC-LC; if that is wrong the decoder will emit a
 * decode error and the caller surfaces a graceful degradation.
 */
export function aascToCodecString(aasc: Uint8Array): string {
  if (aasc.length < 1) return 'mp4a.40.2';
  const aot = (aasc[0] >> 3) & 0x1f;
  switch (aot) {
    case 5:
      return 'mp4a.40.5';
    case 29:
      return 'mp4a.40.29';
    default:
      return 'mp4a.40.2';
  }
}

/**
 * Resolve the sample rate implied by an AASC's samplingFrequencyIndex.
 * Returns null for HE-AAC (where SBR overrides the base rate) or when the
 * bits cannot be parsed; the caller passes the authoritative sample rate from
 * the AudioCodecInfo, so this is only a diagnostic fallback.
 */
export function aascSampleRate(aasc: Uint8Array): number | null {
  if (aasc.length < 2) return null;
  const aot = (aasc[0] >> 3) & 0x1f;
  if (aot === 5) return null; // HE-AAC base rate is not the output rate.
  const freqIndex = ((aasc[0] & 0x07) << 1) | (aasc[1] >> 7);
  if (freqIndex === 0x0f) return null; // explicit 24-bit rate (rare)
  if (freqIndex >= 0 && freqIndex < AAC_SAMPLE_RATE_INDEX.length) {
    return AAC_SAMPLE_RATE_INDEX[freqIndex];
  }
  return null;
}

/** Check whether the WebCodecs AudioDecoder API is available. */
export function detectWebCodecsAudioDecoder(): boolean {
  return typeof AudioDecoder !== 'undefined';
}

/**
 * Probe whether the browser's WebCodecs AudioDecoder can decode Opus. Unlike
 * the general `detectWebCodecsAudioDecoder` (which only checks API presence),
 * this also confirms Opus codec support via isConfigSupported. Resolves false
 * if WebCodecs is absent or Opus is unsupported (older Safari).
 */
export async function detectWebCodecsOpus(): Promise<boolean> {
  if (!detectWebCodecsAudioDecoder()) return false;
  try {
    const result = await AudioDecoder.isConfigSupported({
      codec: 'opus',
      sampleRate: 48000,
      numberOfChannels: 1,
    });
    return !!result.supported;
  } catch {
    return false;
  }
}

/** Minimal interface shared with the WASM AAC decoder backend. */
export interface AacDecoder {
  configure(aasc: Uint8Array, sampleRate: number, channels: number): void;
  decode(frame: Uint8Array, pts: number): void;
  reset(): void;
  close(): void;
  readonly ready: boolean;
}

/**
 * WebCodecs-backed AAC decoder.
 *
 * Decoded `AudioData` frames are handed to `onOutput` as Float32 PCM per
 * channel at the configured sample rate. The consumer schedules them on a
 * Web Audio AudioBufferSourceNode.
 */
export class WebCodecsAacDecoder implements AacDecoder {
  private _decoder: AudioDecoder | null = null;
  private _ready = false;
  private _onOutput: (frame: AudioData) => void;
  private _onError: (err: DOMException) => void;

  constructor(onOutput: (frame: AudioData) => void, onError: (err: DOMException) => void) {
    this._onOutput = onOutput;
    this._onError = onError;
  }

  get ready(): boolean {
    return this._ready;
  }

  configure(aasc: Uint8Array, sampleRate: number, channels: number): void {
    if (!detectWebCodecsAudioDecoder()) {
      throw new Error('WebCodecs AudioDecoder is not available (requires HTTPS or localhost)');
    }
    this.close();
    this._decoder = new AudioDecoder({
      output: (frame: globalThis.AudioData) => this._onOutput(adaptNativeAudioData(frame)),
      error: (err: DOMException) => this._onError(err),
    });
    this._decoder.configure({
      codec: aascToCodecString(aasc),
      sampleRate,
      numberOfChannels: channels,
      description: aasc,
    });
    this._ready = true;
  }

  decode(frame: Uint8Array, pts: number): void {
    if (!this._decoder || !this._ready) return;
    if (this._decoder.decodeQueueSize > 32) {
      // Backpressure: drop the oldest queued frames to keep latency bounded.
      // decode() is synchronous-enqueue; dropping here prevents runaway queue
      // growth if the consumer falls behind (e.g. tab backgrounded).
      return;
    }
    const chunk = new EncodedAudioChunk({
      type: 'key',
      timestamp: pts,
      data: frame,
    });
    this._decoder.decode(chunk);
  }

  reset(): void {
    if (this._decoder) {
      try {
        this._decoder.reset();
      } catch {
        /* already closed */
      }
      this._ready = false;
    }
  }

  close(): void {
    if (this._decoder) {
      try {
        this._decoder.close();
      } catch {
        /* already closed */
      }
      this._decoder = null;
      this._ready = false;
    }
  }
}
