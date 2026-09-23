/**
 * Offload API — remote object-storage archive (issue #874):
 * listing remote (evicted) items + outbox status. Playback rides the
 * range-proxy URL below, in the protected media group — it carries the
 * session token as a ?token= query param (same mechanism as recording
 * downloads) so <video>/<a> fetches without an Authorization header still
 * authenticate.
 */
import { apiRequest, API_BASE, appendAuthToken } from './client';

export interface OffloadRemoteItem {
  /** outbox row id — the playback URL key */
  id: number;
  recording_id: string;
  camera_id: string;
  object_key: string;
  file_size: number;
  uploaded_size: number;
  status: 'pending' | 'uploading' | 'uploaded' | 'evicted' | 'skipped';
  format: string; // h264 / h265 / mjpeg …
  duration: number;
  started_at: string;
  ended_at: string;
  uploaded_at: string;
}

export interface OffloadStatus {
  counts: Record<string, number>;
  backlog: number; // pending + uploading
}

export interface ListOffloadRecordingsParams {
  camera_id?: string;
  /** RFC3339 — bounds started_at (inclusive) */
  start?: string;
  /** RFC3339 — bounds started_at (exclusive) */
  end?: string;
  limit?: number;
  offset?: number;
}

export async function listOffloadRecordings(
  params: ListOffloadRecordingsParams = {},
  signal?: AbortSignal,
): Promise<OffloadRemoteItem[]> {
  const q = new URLSearchParams();
  if (params.camera_id) q.set('camera_id', params.camera_id);
  if (params.start) q.set('start', params.start);
  if (params.end) q.set('end', params.end);
  if (params.limit) q.set('limit', String(params.limit));
  if (params.offset) q.set('offset', String(params.offset));
  const qs = q.toString();
  return apiRequest<OffloadRemoteItem[]>(`/offload/recordings${qs ? `?${qs}` : ''}`, { signal });
}

export async function getOffloadStatus(signal?: AbortSignal): Promise<OffloadStatus> {
  return apiRequest<OffloadStatus>('/offload/status', { signal });
}

/** Ranged playback URL for a remote item (use directly in <video>/<a download>). */
export function offloadObjectURL(item: Pick<OffloadRemoteItem, 'id'>): string {
  return appendAuthToken(`${API_BASE}/offload/objects/${item.id}`);
}
