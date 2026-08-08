/**
 * Shared types for the AAC decoder backends (WebCodecs + WASM).
 *
 * `AudioData` re-exports the WebCodecs `AudioData` shape for the WebCodecs
 * path; the WASM backend produces a structurally-compatible object so the
 * AudioPlayer consumer treats both paths identically.
 */

export type AudioData = {
  /** Per-channel planar Float32 PCM samples. */
  readonly channelData: Float32Array[];
  /** Number of frames (samples per channel) in this output. */
  readonly numberOfFrames: number;
  /** Sample rate in Hz. */
  readonly sampleRate: number;
  /** Number of channels. */
  readonly numberOfChannels: number;
  /** Presentation timestamp in microseconds. */
  readonly timestamp: number;
  /** Release native resources (no-op for the WASM plain-object form). */
  close(): void;
};
