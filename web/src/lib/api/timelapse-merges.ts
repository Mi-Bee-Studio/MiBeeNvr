/**
 * Timelapse Merges API — list / get / download periodic-merge outputs.
 *
 * A "timelapse merge" is the long-window (8h / 12h / 24h / natural-day / 7d /
 * 30d) video produced by folding many short timelapse segments into one MP4.
 * The backend writes one row per output to the `timelapse_merges` table
 * (migration v28) so the frontend can discover, play, and delete them.
 */
import { apiRequest, getAuthHeader, API_BASE, ApiRequestError } from './client';

export interface TimelapseMerge {
  id: number;
  camera_id: string;
  window_start: string; // UTC RFC3339-ish
  window_end: string;
  duration_label: string; // "1h" / "8h" / "24h" / "natural-day" / "7d" / "30d"
  output_path: string;
  file_size: number;
  frame_count: number;
  codec: '' | 'h264' | 'h265' | 'mjpeg';
  fps: number;
  status: 'pending' | 'merging' | 'completed' | 'failed';
  error?: string;
  source_segment_ids: string; // JSON array string
  created_at: string;
  completed_at?: string;
}

export interface TimelapseMergeListResponse {
  merges: TimelapseMerge[];
  total: number;
}

export interface ListTimelapseMergesParams {
  camera_id?: string;
  start?: string; // RFC3339, inclusive lower bound on window_start
  end?: string; // RFC3339, inclusive upper bound on window_start
  duration?: string; // exact duration_label match
  status?: string; // exact status match
  limit?: number;
  offset?: number;
  signal?: AbortSignal;
}

/**
 * List periodic-merge outputs with optional filters. Returns newest-first
 * (window_start DESC).
 */
export async function listTimelapseMerges(
  params: ListTimelapseMergesParams = {},
): Promise<TimelapseMergeListResponse> {
  const queryParams = new URLSearchParams();
  if (params.camera_id) queryParams.set('camera_id', params.camera_id);
  if (params.start) queryParams.set('start', params.start);
  if (params.end) queryParams.set('end', params.end);
  if (params.duration) queryParams.set('duration', params.duration);
  if (params.status) queryParams.set('status', params.status);
  if (params.limit !== undefined) queryParams.set('limit', String(params.limit));
  if (params.offset !== undefined) queryParams.set('offset', String(params.offset));

  const query = queryParams.toString();
  const endpoint = query ? `/timelapse/merges?${query}` : '/timelapse/merges';
  return apiRequest<TimelapseMergeListResponse>(endpoint, { signal: params.signal });
}

/**
 * Get a single timelapse merge by id.
 */
export async function getTimelapseMerge(id: number | string, signal?: AbortSignal): Promise<TimelapseMerge> {
  return apiRequest<TimelapseMerge>(`/timelapse/merges/${id}`, { signal });
}

/**
 * Build the direct-playback URL for a timelapse merge output. The URL is
 * suitable for use as a `<video src>` — the browser sends credentials via the
 * service worker / cookie, OR the asset is behind the public-merge route.
 * Returns null if the merge is not yet completed.
 */
export function getTimelapseMergeDownloadUrl(id: number | string): string {
  return `${API_BASE}/timelapse/merges/${id}/download`;
}

/**
 * Probe a timelapse merge's codec via HEAD (returns the X-Timelapse-Codec
 * header). Returns null on 404 / missing header / network failure. Same
 * pattern as probeMergedRecordingCodec but for the /timelapse/merges/{id}/
 * endpoint. Useful when a player route wants to decide between <video>
 * (h264/h265) and a fallback.
 *
 * NOTE: the codec is already on the TimelapseMerge row returned by
 * getTimelapseMerge / listTimelapseMerges — use the row directly when you
 * have it; only call this for a fresh probe.
 */
export async function probeTimelapseMergeCodec(id: number | string): Promise<string | null> {
  const url = `${API_BASE}/timelapse/merges/${id}/download`;
  const headers: Record<string, string> = {};
  const authHeader = getAuthHeader();
  if (authHeader) headers.Authorization = authHeader;
  try {
    const response = await fetch(url, {
      method: 'HEAD',
      headers,
      signal: AbortSignal.timeout(10000),
    });
    if (!response.ok) return null;
    return response.headers.get('X-Timelapse-Codec');
  } catch {
    return null;
  }
}

/**
 * Delete a timelapse merge (DB row + output file on disk). Does NOT touch the
 * source segments that were folded into the merge.
 */
export async function deleteTimelapseMerge(id: number | string): Promise<void> {
  const url = `${API_BASE}/timelapse/merges/${id}`;
  const headers: Record<string, string> = {};
  const authHeader = getAuthHeader();
  if (authHeader) headers.Authorization = authHeader;
  const res = await fetch(url, { method: 'DELETE', headers });
  if (!res.ok) {
    const errorData = await res.json().catch(() => ({ error: 'Failed to delete merge' }));
    throw new ApiRequestError(errorData.error || `HTTP ${res.status}`, errorData.code);
  }
}

// Re-exported for callers that want the ApiRequestError type.
export { ApiRequestError };
