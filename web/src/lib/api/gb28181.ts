// GB28181 device-side recording API (#337): RecordInfo query + playback
// fetch lifecycle + MANSRTSP control.

import { apiRequest } from './client';

export interface GB28181DeviceRecord {
  name: string;
  file_path?: string;
  start_time: string; // RFC3339 UTC
  end_time: string; // RFC3339 UTC
}

export interface GB28181RecordListResponse {
  channel_id: string;
  count: number;
  records: GB28181DeviceRecord[];
}

export interface GB28181PlaybackStatus {
  active: boolean;
  channel_id: string;
  device_id?: string;
  camera_id?: string;
  start?: string;
  end?: string;
  frames?: number;
  started_at?: string;
  paused?: boolean;
  scale?: number;
}

/** Query the device-side recording index for a time range. */
export async function queryGB28181Records(
  channelId: string,
  start: string,
  end: string,
  signal?: AbortSignal,
): Promise<GB28181RecordListResponse> {
  const params = new URLSearchParams({ start, end });
  return apiRequest<GB28181RecordListResponse>(`/gb28181/channels/${encodeURIComponent(channelId)}/records?${params}`, {
    signal,
  });
}

/** Start a device-recording fetch (playback INVITE → local recording). */
export async function startGB28181Playback(channelId: string, start: string, end: string): Promise<void> {
  await apiRequest(`/gb28181/channels/${encodeURIComponent(channelId)}/playback`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ start, end }),
  });
}

/** Report the running fetch for a channel (active=false when idle). */
export async function gb28181PlaybackStatus(channelId: string): Promise<GB28181PlaybackStatus> {
  return apiRequest<GB28181PlaybackStatus>(`/gb28181/channels/${encodeURIComponent(channelId)}/playback`);
}

/** Stop a running fetch (finalizes the partially-fetched recording). */
export async function stopGB28181Playback(channelId: string): Promise<void> {
  await apiRequest(`/gb28181/channels/${encodeURIComponent(channelId)}/playback`, {
    method: 'DELETE',
  });
}

/** Send a MANSRTSP control to a running fetch: pause | resume | seek. */
export async function controlGB28181Playback(
  channelId: string,
  action: 'pause' | 'resume' | 'seek',
  opts?: { scale?: number; position?: number },
): Promise<void> {
  await apiRequest(`/gb28181/channels/${encodeURIComponent(channelId)}/playback/control`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action, scale: opts?.scale ?? 0, position: opts?.position ?? 0 }),
  });
}

/** Recent alarm notifications of a device (SUBSCRIBE Alarm ring, latest first). */
export interface GB28181Alarm {
  camera_id?: string;
  device_id: string;
  channel_id?: string;
  alarm_priority?: string;
  alarm_method?: string;
  alarm_type?: string;
  alarm_time?: string;
  alarm_description?: string;
  received_at: string;
}

/** One mobile-position report (SUBSCRIBE MobilePosition). */
export interface GB28181Position {
  device_id: string;
  time: string;
  longitude: string;
  latitude: string;
  speed?: string;
  direction?: string;
  altitude?: string;
  updated_at: string;
}

export async function getGB28181Alarms(deviceId: string): Promise<GB28181Alarm[]> {
  return apiRequest<GB28181Alarm[]>(`/gb28181/devices/${encodeURIComponent(deviceId)}/alarms`);
}

export async function getGB28181Positions(deviceId: string): Promise<GB28181Position[]> {
  return apiRequest<GB28181Position[]>(`/gb28181/devices/${encodeURIComponent(deviceId)}/positions`);
}

export async function getGB28181TalkStatus(
  cameraId: string,
): Promise<{ active: boolean; packets?: number; bytes_sent?: number }> {
  return apiRequest(`/cameras/${encodeURIComponent(cameraId)}/gb28181/talk/status`);
}

export interface GB28181CascadeStatus {
  enabled: boolean;
  online: boolean;
  forwards: number;
  registered_for_seconds?: number;
}

export async function getGB28181CascadeStatus(): Promise<GB28181CascadeStatus> {
  return apiRequest<GB28181CascadeStatus>('/gb28181/cascade/status');
}
