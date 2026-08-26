/**
 * Shared end-to-end live-latency tracker (#469/#481).
 *
 * Every player protocol reports "browser now − frame ingest wallclock".
 * WS gets the exact ingest stamp in-band (VideoFrame trailer); FLV gets it
 * via the tag StreamID piggyback (#481); HLS/WebRTC feed approximations
 * (segment-based / hub-ingest age). The tracker smooths samples with an EMA
 * and reports to /api/telemetry every 10s tagged with the protocol.
 */
import { sendTelemetry } from '$lib/telemetry';

export class LiveLatencyTracker {
  value = $state<number | null>(null);
  private lastReport = 0;

  constructor(
    private cameraId: string,
    private protocol: string,
  ) {}

  /** Feed one sample: the ingest wallclock (unix ms) of a recently
   *  displayed frame, or null/undefined to skip (unknown sentinel). */
  track(ingestAtMs?: number | null): void {
    if (!ingestAtMs || ingestAtMs <= 0) return;
    const now = Date.now();
    const sample = now - ingestAtMs;
    if (sample < 0 || sample > 60_000) return; // clock-skew / stale-replay guard
    this.value = this.value == null ? sample : this.value * 0.9 + sample * 0.1;
    if (now - this.lastReport >= 10_000) {
      this.lastReport = now;
      sendTelemetry('live_latency', this.cameraId, Math.round(this.value), { protocol: this.protocol });
    }
  }

  /** Feed an already-computed latency sample (ms) directly — for
   *  approximations like hls.js `latency` where there is no ingest stamp. */
  trackLatencyMs(latencyMs: number): void {
    if (latencyMs < 0 || latencyMs > 60_000) return;
    this.track(Date.now() - latencyMs);
  }
}

/** Badge color classes shared by all players (<1s green / <3s yellow / rest red). */
export function latencyBadgeClass(ms: number | null): string {
  if (ms == null) return 'text-white/50';
  if (ms > 3000) return 'text-red-400/80';
  if (ms > 1000) return 'text-yellow-400/80';
  return 'text-green-400/70';
}
