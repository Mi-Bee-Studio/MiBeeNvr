// Dual-spectrum thermal APIs (P4, #38 Tier 2): the minute-aggregated
// temperature history behind the playback curve. The live chip and the
// spot-probe grid ride the SSE feed / frame proxy and do not go through here.

import { apiRequest } from './client';

export interface ThermalPoint {
  minute: string; // RFC3339, minute start (UTC)
  max_c?: number | null;
  min_c?: number | null;
}

export interface ThermalHistory {
  camera_id: string;
  from: string;
  to: string;
  points: ThermalPoint[];
}

/** Fetch the decimated temperature history for [from, to] (ISO strings). */
export async function getThermalHistory(
  cameraId: string,
  from: string,
  to: string,
  opts?: { signal?: AbortSignal; points?: number },
): Promise<ThermalHistory> {
  const params = new URLSearchParams({ from, to });
  if (opts?.points) params.set('points', String(opts.points));
  const qs = params.toString();
  return apiRequest<ThermalHistory>(
    `/cameras/${encodeURIComponent(cameraId)}/thermal/history${qs ? `?${qs}` : ''}`,
    { signal: opts?.signal },
  );
}
