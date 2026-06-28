/**
 * Web Audio API audio player for G.711 μ-law/A-law streams.
 *
 * Decodes G.711 frames to 16-bit PCM using lookup tables from g711-decoder,
 * schedules them for gapless playback via AudioContext.createBufferSource().
 *
 * AudioContext is NOT created until init() is called — must be called from
 * a user gesture handler to satisfy browser autoplay policy.
 *
 * Codec byte values:
 *   0x01 = G.711 μ-law (PCMU)
 *   0x02 = G.711 A-law (PCMA)
 *   0x03 = Opus (not supported — logs and skips)
 *   0x04 = AAC (not supported — logs and skips)
 */

import { decodeMuLaw, decodeALaw } from './g711-decoder';

/** Maximum number of scheduled audio buffers to keep in the queue. */
const MAX_SCHEDULED_BUFFERS = 10;

/** Size of each decoded audio buffer in PCM frames (samples per channel). */
const BUFFER_FRAME_SIZE = 4096;

/** Audio codec byte constants. */
export const AudioCodec = {
  MuLaw: 0x01,
  ALaw: 0x02,
  Opus: 0x03,
  AAC: 0x04,
} as const;

export type AudioCodec = (typeof AudioCodec)[keyof typeof AudioCodec];

export class AudioPlayer {
  private _ctx: AudioContext | null = null;
  private _gainNode: GainNode | null = null;
  private _codec: number;
  private _sampleRate: number;
  private _channels: number;
  private _muted = false;
  private _nextSchedTime: number = 0;
  private _pendingBuffers: AudioBufferSourceNode[] = [];
  private _initialized = false;
  private _playbackStarted = false;
  private _ringBuffer: Int16Array[] = [];
  private _scheduleTimer: ReturnType<typeof setTimeout> | null = null;
  private _droppedFrames = 0;
  private _decodedFrames = 0;

  constructor(codec: number, sampleRate: number, channels: number) {
    this._codec = codec;
    this._sampleRate = sampleRate;
    this._channels = channels;
  }

  /**
   * Whether the player has been initialized (AudioContext created).
   */
  get initialized(): boolean {
    return this._initialized;
  }

  /**
   * Whether audio is currently muted.
   */
  get muted(): boolean {
    return this._muted;
  }

  /**
   * Number of frames dropped due to buffer overload.
   */
  get droppedFrames(): number {
    return this._droppedFrames;
  }

  /**
   * Number of frames decoded.
   */
  get decodedFrames(): number {
    return this._decodedFrames;
  }

  /**
   * Initialize the AudioContext.
   * MUST be called from a user gesture handler (click, touch, keypress).
   * If an AudioContext already exists, this is a no-op.
   */
  async init(): Promise<void> {
    if (this._initialized) return;

    try {
      // Create AudioContext at the nearest supported sample rate
      // G.711 is 8000 Hz, but not all browsers support 8000 Hz AudioContext
      const ctxSampleRate = this._sampleRate;
      this._ctx = new AudioContext({ sampleRate: ctxSampleRate });

      // Resume if suspended (autoplay policy)
      if (this._ctx.state === 'suspended') {
        await this._ctx.resume();
      }

      this._gainNode = this._ctx.createGain();
      this._gainNode.gain.value = this._muted ? 0 : 1;
      this._gainNode.connect(this._ctx.destination);

      this._nextSchedTime = this._ctx.currentTime;
      this._initialized = true;
      this._playbackStarted = true;
    } catch (err) {
      console.warn('[AudioPlayer] Failed to initialize AudioContext:', err);
      // Fallback: try with default sample rate
      try {
        this._ctx = new AudioContext();
        if (this._ctx.state === 'suspended') {
          await this._ctx.resume();
        }
        this._gainNode = this._ctx.createGain();
        this._gainNode.gain.value = this._muted ? 0 : 1;
        this._gainNode.connect(this._ctx.destination);
        this._nextSchedTime = this._ctx.currentTime;
        this._initialized = true;
        this._playbackStarted = true;
      } catch (err2) {
        console.warn('[AudioPlayer] Fallback AudioContext also failed:', err2);
      }
    }
  }

  /**
   * Push an audio frame for decoding and playback.
   * The frame data is raw G.711 μ-law or A-law samples (not PCM).
   * For unsupported codecs (Opus, AAC), the frame is silently skipped.
   *
   * @param data - Raw encoded audio data (e.g., G.711 μ-law samples)
   */
  pushFrame(data: Uint8Array): void {
    if (!this._initialized || !this._ctx || !this._gainNode) return;
    if (data.length === 0) return;

    // Unsupported codecs — skip
    if (this._codec === AudioCodec.Opus) {
      if (this._decodedFrames === 0) {
        console.log('[AudioPlayer] Opus audio not yet supported via WebSocket');
      }
      this._decodedFrames++;
      return;
    }
    if (this._codec === AudioCodec.AAC) {
      if (this._decodedFrames === 0) {
        console.log('[AudioPlayer] AAC audio not yet supported via WebSocket');
      }
      this._decodedFrames++;
      return;
    }

    // Decode G.711 to 16-bit PCM
    let pcm: Int16Array;
    if (this._codec === AudioCodec.MuLaw) {
      pcm = decodeMuLaw(data);
    } else if (this._codec === AudioCodec.ALaw) {
      pcm = decodeALaw(data);
    } else {
      console.warn(`[AudioPlayer] Unknown audio codec: 0x${this._codec.toString(16)}`);
      this._decodedFrames++;
      return;
    }

    this._decodedFrames++;

    // Add to ring buffer
    this._ringBuffer.push(pcm);

    // Trim ring buffer if too large
    if (this._ringBuffer.length > MAX_SCHEDULED_BUFFERS) {
      const excess = this._ringBuffer.length - MAX_SCHEDULED_BUFFERS;
      this._ringBuffer.splice(0, excess);
      this._droppedFrames += excess;
    }

    // Schedule if not already running
    if (!this._scheduleTimer) {
      this._scheduleNext();
    }
  }

  /**
   * Set muted state.
   */
  setMuted(muted: boolean): void {
    this._muted = muted;
    if (this._gainNode) {
      this._gainNode.gain.value = muted ? 0 : 1;
    }
  }

  /**
   * Full cleanup — close AudioContext and release resources.
   */
  destroy(): void {
    this._cancelSchedule();
    this._pendingBuffers = [];
    this._ringBuffer = [];
    this._playbackStarted = false;
    this._initialized = false;

    if (this._gainNode) {
      try { this._gainNode.disconnect(); } catch { /* already disconnected */ }
      this._gainNode = null;
    }

    if (this._ctx) {
      try {
        if (this._ctx.state !== 'closed') {
          this._ctx.close();
        }
      } catch {
        /* already closed */
      }
      this._ctx = null;
    }
  }

  // ─── Internal: Scheduling ──────────────────────────────────────────────────

  private _scheduleNext(): void {
    if (!this._initialized || !this._ctx || this._ringBuffer.length === 0) {
      this._scheduleTimer = null;
      return;
    }

    const ctx = this._ctx;

    // Schedule buffers in batches, but don't schedule too far ahead
    const scheduledCount = Math.min(
      this._ringBuffer.length,
      MAX_SCHEDULED_BUFFERS,
    );

    const now = ctx.currentTime;

    // If next scheduled time is behind, catch up
    if (this._nextSchedTime < now) {
      this._nextSchedTime = now;
    }

    for (let i = 0; i < scheduledCount; i++) {
      const pcm = this._ringBuffer.shift();
      if (!pcm) break;

      // Create audio buffer
      const frameCount = pcm.length;
      const audioBuffer = ctx.createBuffer(
        this._channels,
        frameCount,
        this._sampleRate,
      );

      // Copy PCM data to channel 0 (mono), or interleave for stereo
      const channelData = audioBuffer.getChannelData(0);
      for (let s = 0; s < frameCount; s++) {
        // Convert Int16 to float32 (-1.0 to 1.0)
        channelData[s] = pcm[s] / 32768;
      }

      // If stereo, duplicate channel 0 data to channel 1
      if (this._channels >= 2 && audioBuffer.numberOfChannels >= 2) {
        const ch1 = audioBuffer.getChannelData(1);
        for (let s = 0; s < frameCount; s++) {
          ch1[s] = channelData[s];
        }
      }

      // Create source
      const source = ctx.createBufferSource();
      source.buffer = audioBuffer;
      source.connect(this._gainNode!);

      // Schedule playback
      const duration = frameCount / this._sampleRate;
      source.start(this._nextSchedTime);
      this._nextSchedTime += duration;

      // Track pending buffers for cleanup
      this._pendingBuffers.push(source);

      // Remove from pending when done
      source.onended = () => {
        const idx = this._pendingBuffers.indexOf(source);
        if (idx >= 0) this._pendingBuffers.splice(idx, 1);
      };
    }

    // Schedule next batch
    this._scheduleTimer = setTimeout(() => {
      this._scheduleTimer = null;
      this._scheduleNext();
    }, 100);
  }

  private _cancelSchedule(): void {
    if (this._scheduleTimer !== null) {
      clearTimeout(this._scheduleTimer);
      this._scheduleTimer = null;
    }
  }
}
