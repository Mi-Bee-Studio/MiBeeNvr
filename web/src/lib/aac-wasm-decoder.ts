/**
 * WASM AAC decoder backend (FAAD2 via @audio/decode-aac).
 *
 * This is the fallback for the plain-HTTP LAN case where the WebCodecs
 * AudioDecoder is unavailable. The NVR recorder emits raw AAC access units
 * (no ADTS header), but FAAD2's high-level streaming API scans for the ADTS
 * syncword — so each raw frame is wrapped in a synthesized 7-byte ADTS header
 * (see ./adts.ts) before being fed in.
 *
 * Implements the same {@link AacDecoder} interface as the WebCodecs backend so
 * the AudioPlayer treats both paths identically.
 */

import type { AudioData } from './aac-types';
import type { AacDecoder } from './aac-webcodecs-decoder';
import { buildAdtsHeader, parseAascForAdts, type AdtsParams } from './adts';
import { loadFaad, type FaadDecoder, type FaadDecodeResult } from './aac-wasm-loader';

/**
 * WASM (FAAD2) AAC decoder. Decoded PCM is handed to `onOutput` as planar
 * Float32 per channel at the configured sample rate.
 *
 * Unlike the WebCodecs backend, configure() is async (WASM module load) and
 * the `ready` flag flips only after the module initializes. decode() calls
 * before ready are dropped (the caller should await init()).
 */
export class WasmAacDecoder implements AacDecoder {
  private _faad: FaadDecoder | null = null;
  private _ready = false;
  private _params: AdtsParams | null = null;
  private _sampleRate = 0;
  private _channels = 0;
  private _onOutput: (frame: AudioData) => void;
  private _onError: (err: unknown) => void;

  constructor(onOutput: (frame: AudioData) => void, onError: (err: unknown) => void) {
    this._onOutput = onOutput;
    this._onError = onError;
  }

  get ready(): boolean {
    return this._ready;
  }

  /**
   * Configure the decoder with the AudioSpecificConfig. Resolves when the
   * FAAD2 WASM module has been loaded and a streaming decoder instantiated.
   * Safe to call again to reconfigure (closes the previous instance).
   */
  async configure(aasc: Uint8Array, sampleRate: number, channels: number): Promise<void> {
    this.close();
    const params = parseAascForAdts(aasc, sampleRate);
    if (!params) {
      throw new Error('AAC WASM: invalid or too-short AudioSpecificConfig');
    }
    this._params = params;
    this._sampleRate = sampleRate;
    this._channels = channels;
    try {
      const mod = await loadFaad();
      this._faad = await mod.decoder();
      this._ready = true;
    } catch (err) {
      this._faad = null;
      this._ready = false;
      throw err;
    }
  }

  decode(frame: Uint8Array, pts: number): void {
    if (!this._ready || !this._faad || !this._params) return;
    if (frame.length === 0) return;
    const header = buildAdtsHeader(this._params, 7 + frame.length);
    const adts = new Uint8Array(header.length + frame.length);
    adts.set(header, 0);
    adts.set(frame, header.length);
    let result: FaadDecodeResult;
    try {
      result = this._faad.decode(adts);
    } catch (err) {
      this._onError(err);
      return;
    }
    const audio = this._adapt(result, pts);
    if (audio) this._onOutput(audio);
  }

  reset(): void {
    // FAAD2's decoder() instance has no reset(); the cheapest equivalent is
    // free + re-create on next configure. Drop in-flight state only.
    this._ready = false;
  }

  close(): void {
    if (this._faad) {
      try {
        this._faad.free();
      } catch {
        /* already freed */
      }
      this._faad = null;
    }
    this._ready = false;
    this._params = null;
  }

  private _adapt(result: FaadDecodeResult, pts: number): AudioData | null {
    if (!result.channelData || result.channelData.length === 0) return null;
    const channelData = result.channelData;
    const numberOfFrames = channelData[0]?.length ?? 0;
    const sampleRate = result.sampleRate || this._sampleRate;
    const numberOfChannels = channelData.length;
    return { channelData, numberOfFrames, sampleRate, numberOfChannels, timestamp: pts, close() {} };
  }
}
