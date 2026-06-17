/**
 * Camera API — CRUD, ONVIF discovery, PTZ, protocols, per-camera merge config
 */
import { apiRequest, getAuthHeader, API_BASE } from './client';

// --- Types ---

export interface CameraTranscodingConfig {
  enabled: boolean;
  target_codec: string;
  preset: string;
  bitrate: string;
}

export interface Camera {
  id: string;
  name: string;
  protocol: string;
  encoding?: string;
  url: string;
  username?: string;
  has_password?: boolean;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  status?: string;
  error_type?: string | null;
  error_detail?: string | null;
  last_seen?: string;
  retention_days?: number;
  onvif_endpoint?: string;
  profile_token?: string;
  stream_encoding?: string;
  transcoding?: CameraTranscodingConfig;
  channel?: string;
  audio_enabled?: boolean;
  // Push/ingest fields (SRT/RTMP cameras)
  stream_key?: string;
  srt_passphrase?: string;
  srt_stream_id?: string;
  // Push-out relay (forward this camera's stream to remote targets)
  push_targets?: PushTargetConfig[];
  push_retention_days?: number | null;
}

/** One push-out relay destination (RTMP/RTSP) for a camera. */
export interface PushTargetConfig {
  id: string;
  name?: string;
  protocol: 'rtmp' | 'rtsp';
  url: string;
  enabled: boolean;
}

/** Live runtime status of one push-out target (from GET push-status). */
export interface PushTargetStatus {
  id: string;
  name: string;
  protocol: string;
  url: string;
  status: 'idle' | 'connecting' | 'streaming' | 'reconnecting' | 'error';
  kbps: number;
  enabled: boolean;
  uptime: string;
  error?: string;
  updated_at: string;
}

export interface PushStatusResponse {
  camera_id: string;
  targets: PushTargetStatus[];
}

export interface CreateCameraRequest {
  name: string;
  protocol: string;
  encoding?: string;
  url?: string;
  username?: string;
  password?: string;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  onvif_endpoint?: string;
  profile_token?: string;
  stream_encoding?: string;
  transcoding?: CameraTranscodingConfig;
  channel?: string;
  // Push/ingest fields (SRT/RTMP)
  stream_key?: string;
  srt_passphrase?: string;
  srt_stream_id?: string;
  // Push-out relay
  push_targets?: PushTargetConfig[];
  push_retention_days?: number | null;
}

export interface UpdateCameraRequest {
  name?: string;
  url?: string;
  protocol?: string;
  encoding?: string;
  username?: string;
  password?: string;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  retention_days?: number;
  onvif_endpoint?: string;
  profile_token?: string;
  stream_encoding?: string;
  transcoding?: CameraTranscodingConfig;
  channel?: string;
  // Push/ingest fields (SRT/RTMP)
  stream_key?: string;
  srt_passphrase?: string;
  srt_stream_id?: string;
  // Push-out relay (replace whole list when set)
  push_targets?: PushTargetConfig[];
  push_retention_days?: number | null;
}

export interface DiscoveredDevice {
  uuid: string;
  name: string;
  xaddrs: string[];
  scopes: string[];
  hardware: string;
  endpoint: string;
}

export interface DiscoveryError {
  category: 'NETWORK' | 'TIMEOUT' | 'NO_DEVICES' | 'PARSE_ERROR';
  message: string;
}

export interface DeviceInfo {
  manufacturer: string;
  model: string;
  firmware: string;
  serial_number: string;
  hardware_id: string;
}

export interface DiscoveryResult {
  devices: DiscoveredDevice[];
  error: DiscoveryError | null;
}

export interface PTZMoveRequest {
  mode: 'continuous' | 'absolute' | 'relative';
  pan: number;
  tilt: number;
  zoom: number;
}

export interface ProtocolCapabilities {
  hls: boolean;
  ptz: boolean;
  snapshot: boolean;
  discovery: boolean;
  auth: boolean;
}

export interface ProtocolInfo {
  id: string;
  label: string;
  encodings: string[];
  builtIn: boolean;
  capabilities: ProtocolCapabilities;
}

// Hardcoded fallback if API is unreachable
export const DEFAULT_PROTOCOLS: ProtocolInfo[] = [
  {
    id: 'rtsp',
    label: 'RTSP',
    encodings: ['h264', 'h265', 'mjpeg'],
    builtIn: true,
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: false, auth: true },
  },
  {
    id: 'http',
    label: 'HTTP',
    encodings: ['jpeg'],
    builtIn: true,
    capabilities: { hls: false, ptz: false, snapshot: true, discovery: false, auth: true },
  },
  {
    id: 'onvif',
    label: 'ONVIF',
    encodings: ['h264', 'h265', 'mjpeg'],
    builtIn: true,
    capabilities: { hls: true, ptz: true, snapshot: false, discovery: true, auth: true },
  },
  {
    id: 'xiaomi',
    label: 'Xiaomi',
    encodings: ['h264', 'h265'],
    builtIn: true,
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: true, auth: true },
  },
  {
    id: 'rtmp',
    label: 'RTMP',
    encodings: ['h264'],
    builtIn: true,
    capabilities: { hls: false, ptz: false, snapshot: false, discovery: false, auth: false },
  },
  {
    id: 'srt',
    label: 'SRT',
    encodings: ['h264', 'h265'],
    builtIn: true,
    capabilities: { hls: false, ptz: false, snapshot: false, discovery: false, auth: false },
  },
];

// --- Camera CRUD ---

export async function listCameras(signal?: AbortSignal): Promise<Camera[]> {
  return apiRequest<Camera[]>('/cameras', { signal });
}

export async function createCamera(data: CreateCameraRequest, signal?: AbortSignal): Promise<Camera> {
  return apiRequest<Camera>('/cameras', {
    method: 'POST',
    body: JSON.stringify(data),
    signal,
  });
}

export async function getCamera(id: string, signal?: AbortSignal): Promise<Camera> {
  return apiRequest<Camera>(`/cameras/${id}`, { signal });
}

export async function updateCamera(id: string, data: UpdateCameraRequest, signal?: AbortSignal): Promise<Camera> {
  return apiRequest<Camera>(`/cameras/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
    signal,
  });
}

/** Fetch live push-out relay status for a camera (per-target state + bitrate). */
export async function getPushStatus(id: string, signal?: AbortSignal): Promise<PushStatusResponse> {
  return apiRequest<PushStatusResponse>(`/cameras/${id}/push-status`, { signal });
}

export async function deleteCamera(id: string, signal?: AbortSignal): Promise<void> {
  return apiRequest<void>(`/cameras/${id}`, {
    method: 'DELETE',
    signal,
  });
}

export interface CameraRecordingStats {
  recording_count: number;
  total_size: number;
}

export async function getCameraRecordingStats(id: string, signal?: AbortSignal): Promise<CameraRecordingStats> {
  return apiRequest<CameraRecordingStats>(`/cameras/${id}/stats`, { signal });
}

export async function startCamera(id: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${id}/start`, {
    method: 'POST',
    signal,
  });
}

export async function stopCamera(id: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${id}/stop`, {
    method: 'POST',
    signal,
  });
}

export function getDashboardCameras(signal?: AbortSignal): Promise<Camera[]> {
  return apiRequest('/cameras', { signal });
}

// --- Test Connection ---

export interface TestConnectionRequest {
  protocol: string;
  url: string;
  username?: string;
  password?: string;
  encoding?: string;
  onvif_endpoint?: string;
}

export interface TestConnectionResult {
  success: boolean;
  message: string;
  latency_ms: number;
}

export async function testConnection(data: TestConnectionRequest, signal?: AbortSignal): Promise<TestConnectionResult> {
  return apiRequest<TestConnectionResult>('/cameras/test-connection', {
    method: 'POST',
    body: JSON.stringify(data),
    signal,
  });
}

// Snapshot URL helper (returns JPEG from camera snapshot endpoint)
export function getSnapshotUrl(cameraId: string): string {
  return `${API_BASE}/cameras/${cameraId}/snapshot`;
}

// --- Per-camera merge config ---

export interface MergeConfig {
  enabled?: boolean;
  check_interval?: string;
  window_size?: string;
  batch_limit?: number;
  min_segment_age?: string;
  min_segments_to_merge?: number;
}

export async function getMergeConfig(cameraId: string, signal?: AbortSignal): Promise<MergeConfig | null> {
  try {
    return await apiRequest<MergeConfig>(`/cameras/${cameraId}/merge-config`, { signal });
  } catch {
    return null;
  }
}

export async function updateMergeConfig(
  cameraId: string,
  config: MergeConfig,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/merge-config`, {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

export async function deleteCameraMergeConfig(cameraId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/merge-config`, {
    method: 'DELETE',
    signal,
  });
}

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

export async function getONVIFDeviceDetail(
  ip: string,
  signal?: AbortSignal
): Promise<ONVIFDeviceDetail> {
  return apiRequest<ONVIFDeviceDetail>(`/onvif/discover/${ip}`, { signal });
}

export async function probeONVIFDevice(
  host: string,
  port: number = 80,
  signal?: AbortSignal,
): Promise<DiscoveredDevice | null> {
  const result = await apiRequest<{ device: DiscoveredDevice | null }>('/onvif/probe', {
    method: 'POST',
    body: JSON.stringify({ host, port }),
    signal,
  });
  return result.device;
}

// --- Protocols ---

export async function listProtocols(signal?: AbortSignal): Promise<ProtocolInfo[]> {
  const response = await apiRequest<{ protocols: ProtocolInfo[] }>('/protocols', { signal });
  return response.protocols;
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
  return apiRequest<PTZPreset[]>(`/cameras/${cameraId}/ptz/presets`, { signal });
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
