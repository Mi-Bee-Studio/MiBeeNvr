/**
 * AI Events API — MiBeeVision collaboration
 *
 * AI events are detection results written by MiBeeVision (external Rust AI
 * backend) via POST /api/ai/events. This client provides read-only access
 * for the NVR web UI to display AI detection history.
 */
import { apiRequest } from './client';

export interface AIEvent {
  id: number;
  camera_id: string;
  recording_id?: string;
  event_type: string;
  severity: 'info' | 'warning' | 'critical';
  zone_name?: string;
  class_name?: string;
  confidence: number;
  frame_idx?: number;
  frame_timestamp?: string;
  bbox?: string; // JSON array [x1,y1,x2,y2]
  snapshot_path?: string;
  metadata?: string;
  /** Writer instance (API key name) — multi-instance attribution; empty = legacy/anonymous. */
  source?: string;
  created_at: string;
}

export interface AIEventListResponse {
  events: AIEvent[];
  total: number;
  limit: number;
  offset: number;
}

export interface AIEventStats {
  event_type: string;
  count: number;
}

export interface AIEventStatsResponse {
  camera_id: string;
  period: string;
  stats: AIEventStats[];
}

export interface AIEventFilter {
  camera_id?: string;
  event_type?: string;
  source?: string;
  start?: string;
  end?: string;
  asc?: boolean;
  limit?: number;
  offset?: number;
}

export async function listAIEvents(filter: AIEventFilter = {}): Promise<AIEventListResponse> {
  const params = new URLSearchParams();
  if (filter.camera_id) params.set('camera_id', filter.camera_id);
  if (filter.event_type) params.set('event_type', filter.event_type);
  if (filter.source) params.set('source', filter.source);
  if (filter.start) params.set('start', filter.start);
  if (filter.end) params.set('end', filter.end);
  if (filter.asc) params.set('asc', 'true');
  if (filter.limit) params.set('limit', String(filter.limit));
  if (filter.offset) params.set('offset', String(filter.offset));
  const qs = params.toString();
  return apiRequest(`/ai/events${qs ? '?' + qs : ''}`);
}

export async function getAIEvent(id: number): Promise<AIEvent> {
  return apiRequest(`/ai/events/${id}`);
}

export async function getAIEventStats(cameraId: string, period: string = '24h'): Promise<AIEventStatsResponse> {
  return apiRequest(`/ai/stats?camera_id=${encodeURIComponent(cameraId)}&period=${period}`);
}
