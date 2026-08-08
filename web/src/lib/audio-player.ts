/**
 * Web Audio API audio player for live-preview streams.
 *
 * Routes incoming frames by codec:
 *   - G.711 μ-law / A-law — pure-JS ITU-T lookup tables → 16-bit PCM, scheduled
 *     on an AudioBufferSourceNode at the stream's 8 kHz rate.
 *   - AAC — WebCodecs AudioDecoder (HTTPS/localhost) or FAAD2 WASM (plain HTTP)
 *     → Float32 planar PCM, scheduled on a BufferSource at the native rate.
 *
 * G.711 lookup tables, AudioContext sample-rate, and the gapless scheduling
 * scheme are the AGENTS.md "Audio recording & playback" CONVENTIONS; the
 * G.711 path is preserved byte-for-byte and must not be resampled.
 *
 * AudioContext is NOT created until init() — must be called from a user
 * gesture handler (browser autoplay policy).
 */

import { decodeMuLaw, decodeALaw } from './g711-decoder';
import type { AudioData } from './aac-types';
import {
  WebCodecsAacDecoder,
  detectWebCodecsAudioDecoder,
  detectWebCodecsOpus,
  type AacDecoder,
} from './aac-webcodecs-decoder';
import { WasmAacDecoder } from './aac-wasm-decoder';
import { WebCodecsOpusDecoder } from './opus-webcodecs-decoder';

/** Audio codec byte constants. */
export const AudioCodec = {
  MuLaw: 0x01,
  ALaw: 0x02,
  Opus: 0x03,
  AAC: 0x04,
} as const;

/** Reason the (AAC/Opus) decoder could not start, surfaced to the UI. */
export type AudioDecodeUnavailableReason =
  | 'unsupported_codec' // codec with no live-preview decode path
  | 'webcodecs_unavailable' // AAC over plain HTTP, no WebCodecs
  | 'wasm_load_failed' // WASM module failed to fetch/initialize
  | 'decoder_error'; // decoder reported a fatal error

export class AudioPlayer {
  private _ctx: AudioContext | null = null;
  private _gainNode: GainNode | null = null;
  private _codec: number;
  private _sampleRate: number;
  private _channels: number;
  private _config: Uint8Array | undefined; // AAC AASC
  private _muted = false;
  private _nextTime = 0;
  private _initialized = false;

  // AAC decoder backend (WebCodecs or WASM); null for G.711.
  private _aac: AacDecoder | null = null;
  private _unavailableReason: AudioDecodeUnavailableReason | null = null;

  constructor(codec: number, sampleRate: number, channels: number, config?: Uint8Array) {
    this._codec = codec;
    this._sampleRate = sampleRate || 8000;
    this._channels = channels || 1;
    this._config = config;
  }

  get initialized(): boolean {
    return this._initialized;
  }
  get muted(): boolean {
    return this._muted;
  }

  /**
   * If the codec has no available decode path, return a reason for the UI to
   * show a degradation hint. Null when the path is (or will be) usable.
   */
  get unavailableReason(): AudioDecodeUnavailableReason | null {
    return this._unavailableReason;
  }

  async init(): Promise<void> {
    if (this._initialized) return;

    // Only the four known audio codecs are routable; anything else degrades.
    if (
      this._codec !== AudioCodec.MuLaw &&
      this._codec !== AudioCodec.ALaw &&
      this._codec !== AudioCodec.AAC &&
      this._codec !== AudioCodec.Opus
    ) {
      this._unavailableReason = 'unsupported_codec';
      return;
    }

    // AAC and Opus need a decoder backend before playback can start. G.711 is
    // decoded synchronously by lookup tables (no backend needed).
    if (this._codec === AudioCodec.AAC) {
      const started = await this._initAacDecoder();
      if (!started) return; // reason already set
    } else if (this._codec === AudioCodec.Opus) {
      const started = await this._initOpusDecoder();
      if (!started) return; // reason already set
    }

    try {
      this._ctx = new AudioContext({ sampleRate: this._sampleRate });
      if (this._ctx.state === 'suspended') await this._ctx.resume();
      this._gainNode = this._ctx.createGain();
      this._gainNode.gain.value = this._muted ? 0 : 1;
      this._gainNode.connect(this._ctx.destination);
      this._nextTime = this._ctx.currentTime;
      this._initialized = true;
    } catch (err) {
      console.warn('[AudioPlayer] init failed:', err);
    }
  }

  /** Construct + configure the AAC decoder backend (WebCodecs preferred, WASM fallback). */
  private async _initAacDecoder(): Promise<boolean> {
    if (!this._config || this._config.length === 0) {
      // No AASC arrived — cannot configure either backend.
      this._unavailableReason = 'decoder_error';
      return false;
    }
    const onOutput = (frame: AudioData) => this._scheduleDecoded(frame);
    if (detectWebCodecsAudioDecoder()) {
      try {
        const dec = new WebCodecsAacDecoder(onOutput, (err) => {
          console.warn('[AudioPlayer] WebCodecs AAC error:', err);
          this._unavailableReason = 'decoder_error';
        });
        dec.configure(this._config, this._sampleRate, this._channels);
        this._aac = dec;
        return true;
      } catch (err) {
        // isConfigSupported may have rejected; fall through to WASM.
        console.warn('[AudioPlayer] WebCodecs AAC configure failed, trying WASM:', err);
      }
    } else {
      this._unavailableReason = 'webcodecs_unavailable';
    }
    // WASM fallback (works on plain HTTP).
    try {
      const dec = new WasmAacDecoder(onOutput, (err) => {
        console.warn('[AudioPlayer] WASM AAC error:', err);
        this._unavailableReason = 'decoder_error';
      });
      await dec.configure(this._config, this._sampleRate, this._channels);
      this._aac = dec;
      // WASM succeeded; clear the "webcodecs_unavailable" reason since we have a working path.
      this._unavailableReason = null;
      return true;
    } catch (err) {
      console.warn('[AudioPlayer] WASM AAC backend unavailable:', err);
      this._unavailableReason = this._unavailableReason ?? 'wasm_load_failed';
      return false;
    }
  }

  /** Construct + configure the Opus decoder backend (WebCodecs).
   *
   * Opus uses the WebCodecs AudioDecoder (broad browser support: Chrome,
   * Firefox ≥130, Safari ≥17). A libopus WASM fallback for plain-HTTP
   * deployments is deferred — the upstream `opus-decoder` package triggers a
   * Rolldown bundler panic (cross-chunk symbol "assignNames"), tracked
   * separately. On HTTP LAN without WebCodecs, Opus degrades with a clear
   * hint (AAC has the FAAD2 WASM fallback because that package bundles
   * cleanly). */
  private async _initOpusDecoder(): Promise<boolean> {
    const onOutput = (frame: AudioData) => this._scheduleDecoded(frame);
    const webcodecsOk = await detectWebCodecsOpus();
    if (webcodecsOk) {
      try {
        const dec = new WebCodecsOpusDecoder(onOutput, (err) => {
          console.warn('[AudioPlayer] WebCodecs Opus error:', err);
          this._unavailableReason = 'decoder_error';
        });
        dec.configure(this._config ?? new Uint8Array(0), this._sampleRate, this._channels);
        this._aac = dec;
        return true;
      } catch (err) {
        console.warn('[AudioPlayer] WebCodecs Opus configure failed:', err);
        this._unavailableReason = 'decoder_error';
        return false;
      }
    }
    // No WebCodecs and no WASM fallback wired (see method doc) — degrade.
    this._unavailableReason = detectWebCodecsAudioDecoder()
      ? 'decoder_error' // WebCodecs present but Opus unsupported by this browser
      : 'webcodecs_unavailable'; // HTTP LAN, no secure context
    return false;
  }

  pushFrame(data: Uint8Array): void {
    if (!this._initialized || !this._ctx || !this._gainNode) return;
    if (data.length === 0) return;

    // AAC / Opus are decoded asynchronously by their backends; feed raw and
    // let the output callback schedule the PCM. (AAC and Opus share the same
    // _aac slot — it's the common AacDecoder contract.)
    if (this._codec === AudioCodec.AAC || this._codec === AudioCodec.Opus) {
      this._aac?.decode(data, 0);
      return;
    }

    // G.711 synchronous decode (lookup tables) — unchanged.
    let pcm: Int16Array;
    if (this._codec === AudioCodec.MuLaw) {
      pcm = decodeMuLaw(data);
    } else if (this._codec === AudioCodec.ALaw) {
      pcm = decodeALaw(data);
    } else {
      return;
    }

    const frameCount = pcm.length;
    const audioBuffer = this._ctx.createBuffer(this._channels, frameCount, this._sampleRate);
    const ch0 = audioBuffer.getChannelData(0);
    for (let i = 0; i < frameCount; i++) ch0[i] = pcm[i] / 32768;

    const source = this._ctx.createBufferSource();
    source.buffer = audioBuffer;
    source.connect(this._gainNode);

    // Gapless scheduling: chain after previous buffer
    const now = this._ctx.currentTime;
    if (this._nextTime < now) this._nextTime = now + 0.01;
    source.start(this._nextTime);
    this._nextTime += frameCount / this._sampleRate;

    // Prevent scheduling too far ahead (drop old buffers)
    if (this._nextTime > now + 1.0) this._nextTime = now + 0.05;
  }

  /**
   * Schedule decoded (Float32 planar) PCM from an AAC backend onto the Web
   * Audio graph. Mirrors the G.711 gapless-scheduling scheme.
   */
  private _scheduleDecoded(frame: AudioData): void {
    if (!this._ctx || !this._gainNode) {
      frame.close();
      return;
    }
    const channels = Math.max(1, frame.numberOfChannels);
    const frameCount = frame.numberOfFrames;
    if (frameCount === 0) {
      frame.close();
      return;
    }
    const audioBuffer = this._ctx.createBuffer(channels, frameCount, frame.sampleRate || this._sampleRate);
    for (let c = 0; c < channels; c++) {
      const src = frame.channelData[c];
      if (src) audioBuffer.copyToChannel(src, c);
    }
    frame.close();

    const source = this._ctx.createBufferSource();
    source.buffer = audioBuffer;
    source.connect(this._gainNode);

    const now = this._ctx.currentTime;
    if (this._nextTime < now) this._nextTime = now + 0.01;
    source.start(this._nextTime);
    this._nextTime += frameCount / (frame.sampleRate || this._sampleRate);
    if (this._nextTime > now + 1.0) this._nextTime = now + 0.05;
  }

  setMuted(muted: boolean): void {
    this._muted = muted;
    if (this._gainNode) this._gainNode.gain.value = muted ? 0 : 1;
  }

  destroy(): void {
    if (this._aac) {
      try {
        this._aac.close();
      } catch {}
      this._aac = null;
    }
    if (this._gainNode) {
      try {
        this._gainNode.disconnect();
      } catch {}
      this._gainNode = null;
    }
    if (this._ctx) {
      try {
        this._ctx.close();
      } catch {}
      this._ctx = null;
    }
    this._initialized = false;
  }
}
