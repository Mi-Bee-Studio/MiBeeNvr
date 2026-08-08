/**
 * WebCodecs AudioDecoder backend for Opus live preview.
 *
 * Wraps the browser-native `AudioDecoder` to decode raw Opus packets produced
 * by Xiaomi cameras (internal/xiaomi/recorder.go forwardAudio → MISS codec
 * 1032) and forwarded over the `?audio_only=1` WebSocket.
 *
 * WebCodecs Opus support is broad (Chrome, Firefox ≥130, Safari ≥17). On
 * browsers without it, the AudioPlayer falls back to the WASM libopus decoder
 * (./opus-wasm-decoder). WebCodecs requires a secure context (HTTPS or
 * localhost); on plain-HTTP LAN the WASM path is used.
 */

import type { AudioData } from './aac-types';
import type { AacDecoder } from './aac-webcodecs-decoder';
import { adaptNativeAudioData } from './aac-webcodecs-decoder';

/**
 * WebCodecs-backed Opus decoder. Opus packets carry their own config in-band
 * (no separate AudioSpecificConfig like AAC), so configure() only needs the
 * sample rate and channel count.
 */
export class WebCodecsOpusDecoder implements AacDecoder {
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

  configure(_aasc: Uint8Array, sampleRate: number, channels: number): void {
    if (typeof AudioDecoder === 'undefined') {
      throw new Error('WebCodecs AudioDecoder is not available (requires HTTPS or localhost)');
    }
    this.close();
    this._decoder = new AudioDecoder({
      output: (frame: globalThis.AudioData) => this._onOutput(adaptNativeAudioData(frame)),
      error: (err: DOMException) => this._onError(err),
    });
    this._decoder.configure({
      codec: 'opus',
      sampleRate,
      numberOfChannels: channels,
    });
    this._ready = true;
  }

  decode(frame: Uint8Array, pts: number): void {
    if (!this._decoder || !this._ready) return;
    if (this._decoder.decodeQueueSize > 32) {
      // Backpressure guard — drop oldest queued to bound latency.
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
