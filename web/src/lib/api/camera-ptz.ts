/**
 * Camera API — PTZ control, presets and Xiaomi PTZ.
 * Re-exported from ./cameras so existing imports keep working.
 */
import { apiRequest, API_BASE, readJson } from './client';
import type { PTZMoveRequest, XiaomiPTZMove, XiaomiPTZPreset } from './camera-types';

// --- PTZ ---

export async function ptzMove(
  cameraId: string,
  request: PTZMoveRequest,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/move`, {
    method: 'POST',
    body: JSON.stringify(request),
    signal,
  });
}

export async function ptzStop(cameraId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/stop`, {
    method: 'POST',
    signal,
  });
}

// --- ONVIF Discovery ---

export async function discoverONVIFDevices(timeout: number = 5, signal?: AbortSignal): Promise<DiscoveryResult> {
  const result = await apiRequest<DiscoveryResult>('/onvif/discover', {
    method: 'POST',
    body: JSON.stringify({ timeout }),
    signal,
  });
  return {
    devices: result.devices || [],
    error: result.error || null,
  };
}

export async function getONVIFDeviceDetail(ip: string, signal?: AbortSignal): Promise<ONVIFDeviceDetail> {
  return apiRequest<ONVIFDeviceDetail>(`/onvif/discover/${ip}`, { signal });
}

export interface ONVIFProbeResult {
  device: DiscoveredDevice | null;
  // Present (as "no_onvif_response") when the endpoint was reachable but
  // answered nothing ONVIF-shaped — the frontend maps it to a distinct,
  // actionable message instead of the generic "probe failed" (#1125).
  reason?: string;
}

export async function probeONVIFDevice(
  host: string,
  port: number = 80,
  signal?: AbortSignal,
): Promise<ONVIFProbeResult> {
  return apiRequest<ONVIFProbeResult>('/onvif/probe', {
    method: 'POST',
    body: JSON.stringify({ host, port }),
    signal,
  });
}

// --- Protocols ---

export async function listProtocols(signal?: AbortSignal): Promise<ProtocolInfo[]> {
  const response = await apiRequest<{ protocols: ProtocolInfo[] }>('/protocols', { signal });
  return response.protocols;
}

// A single streaming protocol entry as returned by GET /api/cameras/{id}/protocols.
// `Protocol` is the backend handler name (webrtc/flv/ll-hls/hls/wasm/mjpeg);
// `Available` is false when the handler recognizes the codec but can't serve it
// (e.g. WebRTC for H.265) — in which case `Reason` explains why.
export interface CameraProtocolDetail {
  Protocol: string;
  Available: boolean;
  Reason: string;
}

// Sub-stream capability block in the protocols response (#512/#513): where a
// lower-resolution secondary feed exists. `codec` is the puller's observed
// codec once a sub pull has come up (may differ from the main stream's).
export interface CameraSubStreamDetail {
  available: boolean;
  source?: string;
  reason?: string;
  codec?: string;
}

// RTSP output-server pull endpoint in the protocols response (#522/#686):
// url is server-assembled (request host + optional credentials) and directly
// copy-pasteable into VLC/Synology.
export interface RtspEndpointDetail {
  available: boolean;
  reason?: string;
  url?: string;
}

// Response of GET /api/cameras/{id}/protocols — codec-aware per-camera protocol
// ranking. The backend probes the RUNNING recorder for the real codec (correcting
// ONVIF cameras that lie), then asks each registered stream handler CanHandle(codec).
// `default` is the latency-optimal available protocol (webrtc→flv→ll-hls→hls→mjpeg).
export interface CameraProtocolsResponse {
  protocols: CameraProtocolDetail[];
  encoding: string;
  default: string;
  sub_stream?: CameraSubStreamDetail;
  rtsp?: RtspEndpointDetail;
}

// Fetch the available streaming protocols for a specific camera. The grid uses
// this (instead of the global /protocols list) to pick the best playback mode
// per camera, accounting for codec and handler availability.
export async function getCameraProtocols(cameraId: string, signal?: AbortSignal): Promise<CameraProtocolsResponse> {
  return apiRequest<CameraProtocolsResponse>(`/cameras/${cameraId}/protocols`, { signal });
}

// Normalize legacy combined protocol names (rtsp_h264, etc.) to base protocol ID
export function normalizeProtocol(protocol: string): string {
  if (protocol === 'rtsp_h264' || protocol === 'rtsp_h265' || protocol === 'rtsp_mjpeg') return 'rtsp';
  if (protocol === 'http_jpeg') return 'http';
  return protocol;
}

// Build a lookup map from protocol list
export function buildProtocolsMap(protocols: ProtocolInfo[]): Map<string, ProtocolInfo> {
  const map = new Map<string, ProtocolInfo>();
  for (const p of protocols) {
    map.set(p.id, p);
  }
  return map;
}

// Get capabilities for a protocol, handling legacy protocol names
export function getProtocolCapabilities(
  protocol: string,
  protocolsMap: Map<string, ProtocolInfo>,
): ProtocolCapabilities {
  const baseId = normalizeProtocol(protocol);
  const info = protocolsMap.get(baseId);
  if (info) return info.capabilities;
  // Fall back to the built-in catalog. The map is populated asynchronously
  // from GET /api/protocols and is EMPTY until that resolves (and on fetch
  // failure) — returning all-false raced the LiveView's orchestrator
  // registration into isHlsCapable=false → empty chain → "live not
  // supported" even though the camera streams fine (the Surveillance grid
  // registers only after the map loads, so only LiveView hit the race).
  const fallback = DEFAULT_PROTOCOLS.find((p) => p.id === baseId);
  if (fallback) return fallback.capabilities;
  return { hls: false, ptz: false, snapshot: false, discovery: false, auth: false };
}

// --- Xiaomi Vendor Check ---

export interface VendorCheckResult {
  vendor: string;
  compatible: boolean;
  message?: string;
}

export async function checkVendor(did: string): Promise<VendorCheckResult> {
  return apiRequest<VendorCheckResult>(`/xiaomi/check-vendor?did=${encodeURIComponent(did)}`);
}

// --- Imaging ---

export interface ImagingSettings {
  brightness?: number;
  contrast?: number;
  saturation?: number;
  sharpness?: number;
  exposure?: {
    mode: string;
    exposure_time?: number;
    gain?: number;
  };
  white_balance?: {
    mode: string;
    color_temperature?: number;
  };
}

export interface ImagingOptionRange {
  min: number;
  max: number;
}

export interface ImagingOptions {
  brightness?: ImagingOptionRange;
  contrast?: ImagingOptionRange;
  saturation?: ImagingOptionRange;
  sharpness?: ImagingOptionRange;
  exposure_time?: ImagingOptionRange;
  gain?: ImagingOptionRange;
  color_temperature?: ImagingOptionRange;
}

export async function getImagingSettings(cameraId: string, signal?: AbortSignal): Promise<ImagingSettings> {
  return apiRequest<ImagingSettings>(`/cameras/${cameraId}/imaging/settings`, { signal });
}

export async function setImagingSettings(
  cameraId: string,
  settings: Partial<ImagingSettings>,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/imaging/settings`, {
    method: 'PUT',
    body: JSON.stringify(settings),
    signal,
  });
}

export async function getImagingOptions(cameraId: string, signal?: AbortSignal): Promise<ImagingOptions> {
  return apiRequest<ImagingOptions>(`/cameras/${cameraId}/imaging/options`, { signal });
}

// --- PTZ Presets ---

export interface PTZPreset {
  token: string;
  name: string;
}

export async function getPTZPresets(cameraId: string, signal?: AbortSignal): Promise<PTZPreset[]> {
  // The handler wraps the list: {"presets": [...]} — unwrap to the array the
  // consumers iterate (a raw object made the preset dropdown render empty).
  const resp = await apiRequest<{ presets?: PTZPreset[] } | PTZPreset[]>(`/cameras/${cameraId}/ptz/presets`, {
    signal,
  });
  return Array.isArray(resp) ? resp : (resp.presets ?? []);
}

export async function createPTZPreset(cameraId: string, name: string, signal?: AbortSignal): Promise<PTZPreset> {
  return apiRequest<PTZPreset>(`/cameras/${cameraId}/ptz/presets`, {
    method: 'POST',
    body: JSON.stringify({ name }),
    signal,
  });
}

export async function goToPTZPreset(
  cameraId: string,
  token: string,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/presets/${encodeURIComponent(token)}/goto`, {
    method: 'POST',
    signal,
  });
}

export async function deletePTZPreset(
  cameraId: string,
  token: string,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/presets/${encodeURIComponent(token)}`, {
    method: 'DELETE',
    signal,
  });
}

// --- Snapshot URI ---

export interface SnapshotUriResponse {
  uri: string;
}

export async function getSnapshotUri(cameraId: string, signal?: AbortSignal): Promise<SnapshotUriResponse> {
  return apiRequest<SnapshotUriResponse>(`/cameras/${cameraId}/snapshot/uri`, { signal });
}

// --- Device Capabilities ---

export interface DeviceCapabilitiesInfo {
  ptz: boolean;
  imaging: boolean;
  events: boolean;
  snapshot: boolean;
  streaming: boolean;
  device_info?: {
    manufacturer?: string;
    model?: string;
    firmware?: string;
    serial_number?: string;
    hardware_id?: string;
  };
}

export async function getDeviceCapabilities(cameraId: string, signal?: AbortSignal): Promise<DeviceCapabilitiesInfo> {
  return apiRequest<DeviceCapabilitiesInfo>(`/cameras/${cameraId}/onvif/capabilities`, { signal });
}

// --- Device Management ---

export interface NetworkIPv4 {
  enabled: boolean;
  dhcp: boolean;
  address?: string;
  netmask?: string;
  gateway?: string;
}

export interface NetworkIPv6 {
  enabled: boolean;
  dhcp: boolean;
  address?: string;
  prefix?: number;
  gateway?: string;
}

export interface NetworkNTP {
  manual?: string[];
  dhcp: boolean;
}

export interface NetworkInterface {
  name: string;
  enabled: boolean;
  ipv4: NetworkIPv4;
  ipv6?: NetworkIPv6;
  dns?: string[];
  ntp?: NetworkNTP;
}

export interface ONVIFDeviceUser {
  username: string;
  password?: string;
  level: string; // "Administrator", "Operator", "User", "Anonymous"
}

export async function rebootDevice(cameraId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/reboot`, {
    method: 'POST',
    signal,
  });
}

export async function getNetworkInterfaces(
  cameraId: string,
  signal?: AbortSignal,
): Promise<{ interfaces: NetworkInterface[] }> {
  return apiRequest<{ interfaces: NetworkInterface[] }>(`/cameras/${cameraId}/onvif/network`, { signal });
}

export async function setNetworkInterfaces(
  cameraId: string,
  interfaces: NetworkInterface[],
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/network`, {
    method: 'PUT',
    body: JSON.stringify({ interfaces }),
    signal,
  });
}

export async function getDeviceUsers(cameraId: string, signal?: AbortSignal): Promise<{ users: ONVIFDeviceUser[] }> {
  return apiRequest<{ users: ONVIFDeviceUser[] }>(`/cameras/${cameraId}/onvif/users`, { signal });
}

export async function createDeviceUsers(
  cameraId: string,
  users: ONVIFDeviceUser[],
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/users`, {
    method: 'POST',
    body: JSON.stringify({ users }),
    signal,
  });
}

export async function deleteDeviceUsers(
  cameraId: string,
  usernames: string[],
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/users`, {
    method: 'DELETE',
    body: JSON.stringify({ usernames }),
    signal,
  });
}

// --- Per-camera timelapse config ---

export interface TimeRange {
  start: string;
  end: string;
}

export interface ScheduleConfig {
  time_ranges: TimeRange[];
  days_of_week: number[];
}

export interface TimelapseConfig {
  enabled: boolean;
  interval: string;
  frame_source: string;
  snapshot_url: string;
  schedule: ScheduleConfig | null;
  paused: boolean;
  delete_original: boolean;
  delete_recordings_after_merge?: boolean;
  merge_enabled?: boolean;
  merge_mode?: string;
  daily_merge?: boolean;
  merge_output_fps?: number;
  merge_duration?: string;
}

export async function getTimelapseConfig(cameraId: string, signal?: AbortSignal): Promise<TimelapseConfig> {
  return apiRequest<TimelapseConfig>(`/cameras/${cameraId}/timelapse`, { signal });
}

export async function updateTimelapseConfig(
  cameraId: string,
  config: TimelapseConfig,
  signal?: AbortSignal,
): Promise<any> {
  return apiRequest(`/cameras/${cameraId}/timelapse`, {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// --- Xiaomi PTZ ---

export interface XiaomiPtzMoveRequest {
  direction: 'left' | 'right' | 'up' | 'down';
  speed: number;
}

export async function xiaomiPtzMove(
  cameraId: string,
  direction: string,
  speed: number,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/xiaomi/ptz/move`, {
    method: 'POST',
    body: JSON.stringify({ direction, speed }),
    signal,
  });
}

export async function xiaomiPtzStop(cameraId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/xiaomi/ptz/stop`, {
    method: 'POST',
    signal,
  });
}

// --- Xiaomi Device Info ---

export interface XiaomiDeviceInfo {
  firmware_version?: string;
  hardware_version?: string;
  model?: string;
  serial_number?: string;
  mac_address?: string;
  [key: string]: unknown;
}

export async function getXiaomiDeviceInfo(cameraId: string, signal?: AbortSignal): Promise<XiaomiDeviceInfo> {
  return apiRequest<XiaomiDeviceInfo>(`/cameras/${cameraId}/xiaomi/device-info`, { signal });
}

// --- Two-way Audio ---

export async function startTwoWayAudio(cameraId: string, signal?: AbortSignal): Promise<{ speaker_codec: number }> {
  return apiRequest<{ speaker_codec: number }>(`/cameras/${cameraId}/xiaomi/two-way-audio/start`, {
    method: 'POST',
    signal,
  });
}

export async function stopTwoWayAudio(cameraId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/xiaomi/two-way-audio/stop`, {
    method: 'POST',
    signal,
  });
}

/** Return the WebSocket URL for two-way audio upstream PCM. */
export function getAudioUpstreamWS(cameraId: string): string {
  const base = API_BASE.replace(/\/api$/, '');
  return `${base}/api/ws/camera/${cameraId}/audio-upstream`;
}

/** Pixel-domain fine gate (#636): classic CV over a ~1fps sampled decode of
 *  the camera's sub-stream; confirmed activity triggers full-rate. Requires
 *  an RTSP-reachable sub-stream + ffmpeg; enable/disable applies on restart. */
export interface CameraPixgateConfig {
  enabled: boolean;
  sample_fps?: number;
  min_area_pct?: number;
  persist?: number;
  hold?: string;
  masks?: { name?: string; points: [number, number][] }[];
}

// ── Camera time sync (#time-sync) ────────────────────────────────────────────

export interface CameraTimeStatus {
  camera_id: string;
  available: boolean;
  checked_at: string;
  skew_seconds: number;
  device_utc?: string;
  datetime_type?: string;
  timezone?: string;
  error?: string;
  auto_managed: boolean;
  sntp_enabled: boolean;
  credentials: boolean;
}

export interface CameraTimeSyncResult {
  changed: boolean;
  before_seconds: number;
  after_seconds: number;
}

/** Read the camera's clock vs the NVR's (no credentials needed). */
export async function getCameraTime(id: string, signal?: AbortSignal): Promise<CameraTimeStatus> {
  return apiRequest<CameraTimeStatus>(`/cameras/${encodeURIComponent(id)}/time`, {
    signal: signal ?? AbortSignal.timeout(20000),
  });
}

/** Write the NVR time to the camera when the skew is ≥1s (needs camera admin credentials). */
export async function syncCameraTime(
  id: string,
  timezone?: string,
  signal?: AbortSignal,
): Promise<CameraTimeSyncResult> {
  return apiRequest<CameraTimeSyncResult>(`/cameras/${encodeURIComponent(id)}/time/sync`, {
    method: 'POST',
    body: JSON.stringify(timezone ? { timezone } : {}),
    signal: signal ?? AbortSignal.timeout(30000),
  });
}

/** Point the camera at the NVR's SNTP server and switch it to NTP mode (path B). */
export async function pointCameraAtNVR(
  id: string,
  server?: string,
  signal?: AbortSignal,
): Promise<{ ok: boolean; server: string }> {
  return apiRequest<{ ok: boolean; server: string }>(`/cameras/${encodeURIComponent(id)}/time/ntp`, {
    method: 'POST',
    body: JSON.stringify(server ? { server } : {}),
    signal: signal ?? AbortSignal.timeout(30000),
  });
}
