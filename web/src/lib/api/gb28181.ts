/**
 * GB28181 — registered device/channel management.
 *
 * The backend serializes its storage structs directly (no json tags), so all
 * fields arrive in Go PascalCase. Times are RFC3339 strings.
 */
import { apiRequest } from './client';

/** A device that registered with the NVR's SIP server. */
export interface GB28181Device {
  ID: string;
  Name: string;
  Manufacturer: string;
  Model: string;
  /** 'online' | 'offline' */
  Status: string;
  /** RFC3339 timestamp */
  LastKeepalive: string;
  /** RFC3339 timestamp */
  RegisteredAt: string;
}

/** A channel (camera) belonging to a GB28181 device. */
export interface GB28181Channel {
  ID: string;
  DeviceID: string;
  Name: string;
  Manufacturer: string;
  /** 0 = main channel, 1 = sub channel */
  Parental: number;
  /** 'idle' | 'inviting' | 'playing' */
  Status: string;
  /** MiBee camera ID when this channel is bound to a camera, else '' */
  CameraID: string;
  /** RFC3339 timestamp */
  UpdatedAt: string;
  /** GB/T 28181 PTZ capability: 0 = none, 1 = pan/tilt, 2 = pan/tilt + zoom */
  PTZType?: number;
}

export interface GB28181ActionResponse {
  status: string;
  device_id?: string;
  channel_id?: string;
}

/** List registered GB28181 devices (default 50, max 500). */
export async function listGB28181Devices(limit = 50): Promise<GB28181Device[]> {
  return apiRequest<GB28181Device[]>(`/gb28181/devices?limit=${limit}`);
}

/** List channels for a specific GB28181 device. */
export async function listGB28181Channels(deviceId: string): Promise<GB28181Channel[]> {
  return apiRequest<GB28181Channel[]>(`/gb28181/devices/${encodeURIComponent(deviceId)}/channels`);
}

/** Ask the device to re-send its catalog (async — returns 202). */
export async function catalogRefreshGB28181(deviceId: string): Promise<GB28181ActionResponse> {
  return apiRequest<GB28181ActionResponse>(`/gb28181/devices/${encodeURIComponent(deviceId)}/catalog-refresh`, {
    method: 'POST',
  });
}

/** Invite a channel to start sending media (async — returns 202). */
export async function inviteGB28181Channel(channelId: string): Promise<GB28181ActionResponse> {
  return apiRequest<GB28181ActionResponse>(`/gb28181/channels/${encodeURIComponent(channelId)}/invite`, {
    method: 'POST',
  });
}

/** Stop a channel's media session (BYE). */
export async function byeGB28181Channel(channelId: string): Promise<GB28181ActionResponse> {
  return apiRequest<GB28181ActionResponse>(`/gb28181/channels/${encodeURIComponent(channelId)}/bye`, {
    method: 'POST',
  });
}

/** Send a GB/T 28181 PTZ command to a channel. Body: {direction, zoom, preset}; speed is optional (0 = device default). */
export async function gb28181PtzMove(
	channelId: string,
	direction: string,
	opts: { zoom?: number; preset?: number; speed?: number } = {},
): Promise<GB28181ActionResponse> {
	return apiRequest<GB28181ActionResponse>(`/gb28181/channels/${encodeURIComponent(channelId)}/ptz`, {
		method: 'POST',
		body: JSON.stringify({ direction, zoom: opts.zoom ?? 0, preset: opts.preset ?? 0, speed: opts.speed ?? 0 }),
	});
}
