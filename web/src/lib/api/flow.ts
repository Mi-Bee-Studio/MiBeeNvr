/**
 * Flow API — video pipeline flow-path observability (#469).
 *
 * GET /api/streams returns a point-in-time snapshot of every camera's frame
 * pipeline (producer → StreamHub → consumers). Cumulative counters
 * (frames_in / bytes_in) are diffed by the caller across polls to derive
 * fps/bitrate — the backend hot path never computes rates.
 */
import { apiRequest } from './client';

/** One hub consumer (ws-…, flv-…, webrtc-…, hls, health-…, relay-…). */
export interface FlowConsumer {
  id: string;
  sends: number;
  drops: number;
  idr_drops: number;
  drop_rate: number;
  bytes: number;
  buffer_depth: number;
  buffer_capacity: number;
  subscribed_at: string;
  last_send_at: string;
  dwell_avg_ms: number;
  dwell_max_ms: number;
}

/** One camera's flow snapshot (mirrors backend FlowCamera / model.HubStats). */
export interface FlowStream {
  camera_id: string;
  source: string;
  frames_in: number;
  bytes_in: number;
  last_frame_at: string;
  last_audio_frame_at: string;
  consumers: FlowConsumer[];
  audio_consumers: number;
  jitter_active: boolean;
  name: string;
  status: string;
  protocol?: string;
  encoding?: string;
  width?: number;
  height?: number;
  viewers: Record<string, number>;
}

export interface FlowStreamsResponse {
  streams: FlowStream[];
}

/** Fetch the flow-path snapshot for all cameras. */
export async function getFlowStreams(): Promise<FlowStreamsResponse> {
  return apiRequest<FlowStreamsResponse>('/api/streams');
}
