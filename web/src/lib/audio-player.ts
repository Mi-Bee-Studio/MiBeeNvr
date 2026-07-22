/**
 * Web Audio API audio player for G.711 μ-law/A-law streams.
 *
 * Decodes G.711 frames to 16-bit PCM using lookup tables,
 * schedules gapless playback via AudioContext.
 *
 * AudioContext is NOT created until init() — must be called from
 * a user gesture handler (browser autoplay policy).
 */

import { decodeMuLaw, decodeALaw } from './g711-decoder';

/** Audio codec byte constants. */
export const AudioCodec = {
  MuLaw: 0x01,
  ALaw: 0x02,
  Opus: 0x03,
  AAC: 0x04,
} as const;

export class AudioPlayer {
  private _ctx: AudioContext | null = null;
  private _gainNode: GainNode | null = null;
  private _codec: number;
  private _sampleRate: number;
  private _channels: number;
  private _muted = false;
  private _nextTime = 0;
  private _initialized = false;

  constructor(codec: number, sampleRate: number, channels: number) {
    this._codec = codec;
    this._sampleRate = sampleRate || 8000;
    this._channels = channels || 1;
  }

  get initialized(): boolean {
    return this._initialized;
  }
  get muted(): boolean {
    return this._muted;
  }

  async init(): Promise<void> {
    if (this._initialized) return;
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

  pushFrame(data: Uint8Array): void {
    if (!this._initialized || !this._ctx || !this._gainNode) return;
    if (data.length === 0) return;

    // Decode G.711 to PCM
    let pcm: Int16Array;
    if (this._codec === AudioCodec.MuLaw) {
      pcm = decodeMuLaw(data);
    } else if (this._codec === AudioCodec.ALaw) {
      pcm = decodeALaw(data);
    } else {
      return; // Unsupported codec (Opus/AAC)
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

  setMuted(muted: boolean): void {
    this._muted = muted;
    if (this._gainNode) this._gainNode.gain.value = muted ? 0 : 1;
  }

  destroy(): void {
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
