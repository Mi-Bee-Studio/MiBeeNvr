/**
 * Settings API — cleanup, webdav, merge, feature flags
 */
import { apiRequest } from './client';

// --- Types ---

export interface CleanupConfig {
  retention_days: number;
  disk_threshold_percent: number;
  // Motion-aware disk cleanup (#435): when the disk threshold is hit, evict
  // boring (low motion_score) segments first. Absent = on (server default).
  motion_aware_disk_cleanup?: boolean;
  // Optional on writes: omit (or send "") to keep the server's current value.
  // The cleanup settings UI no longer exposes this field (1h backend default is
  // optimal), so we never send it on save — sending "" used to 400 the whole
  // cleanup save because the server validates it with time.ParseDuration("").
  check_interval?: string;
}

export interface WebDAVConfig {
  enabled: boolean;
  path_prefix: string;
  read_write: boolean;
}

export interface WebRTCConfig {
  enabled: boolean;
  max_viewers: number;
  idle_timeout: string;
}

export interface FLVStreamingConfig {
  enabled: boolean;
  max_viewers: number;
  idle_timeout: string;
  gop_cache_size: number;
}

export interface HLSStreamingConfig {
  low_latency: boolean;
}

export interface RTMPConfig {
  enabled: boolean;
  port: number;
  stream_keys?: Record<string, string>; // camera_id → stream_key (legacy map; per-camera stream_key takes precedence)
}

export interface SRTStreamConfig {
  stream_id: string;
  camera_id: string;
  mode: string; // "listener" or "caller"
  address: string;
  passphrase: string;
}

export interface SRTConfig {
  enabled: boolean;
  port: number;
  streams?: SRTStreamConfig[];
}

export interface StreamingConfig {
  webrtc: WebRTCConfig;
  flv: FLVStreamingConfig;
  hls: HLSStreamingConfig;
  rtmp?: RTMPConfig;
  srt?: SRTConfig;
}

export interface MiBeeVisionConfig {
  api_keys: Array<{
    name: string;
    prefix: string;
    revoked: boolean;
    /** RFC3339 UTC timestamp of the last successful auth with this key (#335). */
    last_used?: string;
  }>;
}

/** One vision consumer instance (multi-instance routing). */
export interface VisionInstanceConfig {
  name: string;
  url: string;
  /** API key name used to attribute heartbeats/events/ai_status reports. */
  api_key_name?: string;
  /** undefined = enabled (omitted in YAML round-trips). */
  enabled?: boolean;
}

/** The `vision` settings section (push integration). */
export interface VisionSettingsConfig {
  enabled?: boolean;
  /** Legacy single-consumer URL; becomes the implicit instance "default". */
  url?: string;
  push_mode?: string;
  heartbeat_timeout_secs?: number;
  instances?: VisionInstanceConfig[];
}

export interface SettingsConfig {
  cleanup: CleanupConfig;
  webdav: WebDAVConfig;
  streaming?: StreamingConfig;
  mibeevision?: MiBeeVisionConfig;
  vision?: VisionSettingsConfig;
  gb28181?: GB28181Config;
  timezone?: string; // "Local", "UTC", or IANA timezone name
  timezone_display?: string; // Human-readable timezone label (e.g. "Asia/Shanghai (UTC+8)")
  server?: { listen?: string }; // listen address ":9090" — changed via Settings UI
  storage?: { root_dir?: string }; // recording root (#395) — applied on next start
}

export interface GB28181Config {
  enabled: boolean;
  sip_listen: string;
  server_id: string;
  realm: string;
  /** Whether a SIP password is configured — the value itself is never returned. */
  password_configured?: boolean;
  port_range: string;
  heartbeat_interval: string;
  catalog_interval: string;
  tcp_mode: boolean;
  tcp_framing: string; // "auto" | "rfc4571" | "0x24"
  media_transport?: string; // "udp" | "tcp-passive" | "tcp-active"
  subscribe_catalog?: boolean;
  subscribe_alarm?: boolean;
  subscribe_mobile_position?: boolean;
  subscribe_expires?: string;
  sip_transport?: string; // "udp" | "tcp"
  allowed_device_ids?: string[];
}

export interface MergeStatus {
  enabled: boolean;
  last_run_time: string;
  segments_merged: number;
  files_created: number;
  error_count: number;
}

export interface MergePending {
  enabled: boolean;
  pending: Record<string, number>;
}

export interface FeatureFlags {
  protocols: Record<string, boolean>;
}

// --- Settings ---

export async function getSettings(signal?: AbortSignal): Promise<SettingsConfig> {
  return apiRequest<SettingsConfig>('/settings', { signal });
}

export async function updateSettings(
  settings: SettingsConfig,
  signal?: AbortSignal,
): Promise<{ status: string; restart_required?: boolean }> {
  return apiRequest<{ status: string; restart_required?: boolean }>('/settings', {
    method: 'PUT',
    body: JSON.stringify(settings),
    signal,
  });
}

// Recording-root choices (#395): the current storage.root_dir plus extra
// locations the host platform granted (fnOS user-authorized directories).
export interface StorageCandidatesResponse {
  current: string;
  candidates: Array<{ path: string; label: string }>;
  restart_hint: string;
  /** True when NVR_STORAGE_CANDIDATES is set: the platform (e.g. fnOS
   *  authorized dirs) owns the list at boot — manually added paths are
   *  session-only until properly authorized on the platform. */
  env_managed?: boolean;
}

export async function getStorageCandidates(signal?: AbortSignal): Promise<StorageCandidatesResponse> {
  return apiRequest<StorageCandidatesResponse>('/storage/candidates', { signal });
}

/** Add a recording-root candidate at RUNTIME (no restart): the path must
 *  exist as a directory (the new disk's mount point) and be writable. */
export async function addStorageCandidate(
  path: string,
  signal?: AbortSignal,
): Promise<{ status: string; path: string; env_managed?: boolean }> {
  return apiRequest('/storage/candidates', {
    method: 'POST',
    body: JSON.stringify({ path }),
    signal,
  });
}

export async function removeStorageCandidate(path: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest(`/storage/candidates?path=${encodeURIComponent(path)}`, {
    method: 'DELETE',
    signal,
  });
}

// Storage migration (#395 rework): hot per-camera storage switching with a
// background idle-time migrator. The database stays on the data volume; only
// files move and stored paths are rewritten — no restart involved.

export interface MigrationJob {
  camera_id: string;
  to_root: string;
  delete_source: boolean;
  state: 'queued' | 'running' | 'paused' | 'done' | 'failed';
  detail?: string;
  error?: string;
  total_files?: number;
  done_files?: number;
  total_bytes?: number;
  done_bytes?: number;
}

export interface StorageMigrateStatusResponse {
  state: 'idle' | 'running';
  jobs: MigrationJob[];
}

/** Batch entry: switch the DEFAULT storage (hot) and enqueue a background
 *  migration per camera that has recordings elsewhere. */
export async function startStorageMigrate(
  target: string,
  deleteSource: boolean,
  signal?: AbortSignal,
): Promise<{ status: string; target: string; jobs_enqueued: number }> {
  return apiRequest('/storage/migrate', {
    method: 'POST',
    body: JSON.stringify({ target, delete_source: deleteSource }),
    signal,
  });
}

export async function getStorageMigrateStatus(signal?: AbortSignal): Promise<StorageMigrateStatusResponse> {
  return apiRequest<StorageMigrateStatusResponse>('/storage/migrate/status', { signal });
}

export interface CameraStorageRoot {
  camera_id: string;
  override_root: string;
  effective_root: string;
  default_root: string;
  migration?: MigrationJob;
}

export async function getCameraStorageRoot(cameraId: string, signal?: AbortSignal): Promise<CameraStorageRoot> {
  return apiRequest<CameraStorageRoot>(`/cameras/${cameraId}/storage-root`, { signal });
}

/** Switch ONE camera's storage (hot) and optionally enqueue the background
 *  migration of its history. root = "" returns it to the default storage. */
export async function setCameraStorageRoot(
  cameraId: string,
  root: string,
  migrate: boolean,
  deleteSource: boolean,
  signal?: AbortSignal,
): Promise<{ status: string; camera_id: string; storage_root: string; migration?: MigrationJob }> {
  return apiRequest(`/cameras/${cameraId}/storage-root`, {
    method: 'PUT',
    body: JSON.stringify({ root, migrate, delete_source: deleteSource }),
    signal,
  });
}

// --- Global merge settings ---

export async function getMergeSettings(signal?: AbortSignal): Promise<MergeConfig> {
  return apiRequest<MergeConfig>('/settings/merge', { signal });
}

export async function updateMergeSettings(config: MergeConfig, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/merge', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// MergeConfig type — re-exported from cameras module for convenience
export type { MergeConfig } from './cameras';

// --- Merge status ---

export async function getMergeStatus(signal?: AbortSignal): Promise<MergeStatus> {
  return apiRequest<MergeStatus>('/merge/status', { signal });
}

export async function getMergePending(signal?: AbortSignal): Promise<MergePending> {
  return apiRequest<MergePending>('/merge/pending', { signal });
}

// --- Feature flags ---

export async function getFeatures(signal?: AbortSignal): Promise<FeatureFlags> {
  return apiRequest<FeatureFlags>('/features', { signal });
}

export async function updateFeatures(features: FeatureFlags, signal?: AbortSignal): Promise<void> {
  await apiRequest('/features', {
    method: 'PUT',
    body: JSON.stringify(features),
    signal,
  });
}

// --- Auto-discover settings ---

export interface AutoDiscoverSettings {
  enabled: boolean;
  scan_interval: number; // seconds (floor 30)
  listen_for_hello: boolean;
  network_interface: string;
  default_username: string;
  has_default_password: boolean; // never returns the password itself
  ignore_scopes: string[];
}

// Request body for updates: all optional (nil = unchanged). default_password is
// only sent when the user types a new value.
export interface AutoDiscoverUpdate {
  enabled?: boolean;
  scan_interval?: number;
  listen_for_hello?: boolean;
  network_interface?: string;
  default_username?: string;
  default_password?: string;
  ignore_scopes?: string[];
}

export async function getAutoDiscoverSettings(signal?: AbortSignal): Promise<AutoDiscoverSettings> {
  return apiRequest<AutoDiscoverSettings>('/settings/auto-discover', { signal });
}

export async function updateAutoDiscoverSettings(
  config: AutoDiscoverUpdate,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/auto-discover', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// --- RTSP output server settings (#686) ---

// GET response. The password itself is never returned — password_configured
// tells the UI whether one is set (mirrors gb28181/auto-discover masking).
export interface RtspOutputSettings {
  enabled: boolean;
  port: number;
  username: string;
  password_configured: boolean;
}

// PUT body: all fields optional (omitted = unchanged). password blank/omitted
// keeps the current one; clear_credentials wipes username+password (open
// access). Changes take effect after an NVR restart.
export interface RtspOutputUpdate {
  enabled?: boolean;
  port?: number;
  username?: string;
  password?: string;
  clear_credentials?: boolean;
}

export async function getRtspOutputSettings(signal?: AbortSignal): Promise<RtspOutputSettings> {
  return apiRequest<RtspOutputSettings>('/settings/rtsp-output', { signal });
}

export async function updateRtspOutputSettings(
  config: RtspOutputUpdate,
  signal?: AbortSignal,
): Promise<{ status: string; restart_required: boolean }> {
  return apiRequest<{ status: string; restart_required: boolean }>('/settings/rtsp-output', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// --- Streaming settings ---

export async function getStreamingSettings(signal?: AbortSignal): Promise<StreamingConfig> {
  return apiRequest<StreamingConfig>('/settings/streaming', { signal });
}

export async function updateStreamingSettings(
  config: StreamingConfig,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/streaming', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// --- MiBeeVision API Key Management ---

export async function generateAPIKey(name: string): Promise<{ name: string; key: string; prefix: string }> {
  return apiRequest<{ name: string; key: string; prefix: string }>('/settings/api-keys', {
    method: 'POST',
    body: JSON.stringify({ name }),
  });
}

export async function revokeAPIKey(name: string): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/settings/api-keys/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  });
}

// --- MiBeeVision consumer health (#328) ---

// Runtime metrics block carried by heartbeat v2 (#671). All optional — an
// older consumer reports none of these.
export interface VisionMetrics {
  queue_capacity?: number;
  decode_workers?: number;
  workers_busy?: number;
  received_total?: number;
  dropped_total?: number;
  dropped_queue_full?: number;
  dropped_ttl?: number;
  events_emitted?: number;
  seg_ms_p50?: number;
  seg_ms_p90?: number;
  decoded_queue_depth?: number;
  mem_available_mb?: number;
  load1?: number;
}

// Mirrors GET /api/vision/status. When the vision integration is disabled the
// backend returns only { enabled: false }; all other fields are optional.
export interface VisionStatus {
  enabled: boolean;
  healthy?: boolean;
  last_seen?: string;
  device?: string;
  queue_depth?: number;
  processed?: number;
  metrics?: VisionMetrics;
  /** Recordings marked ai_status='skipped' from consumer drop reports. */
  drops_marked_total?: number;
  /** Per-instance runtime state (multi-instance); always ≥1 entry ("default"). */
  instances?: VisionInstanceStatus[];
}

/** One instance's status block inside GET /api/vision/status. */
export interface VisionInstanceStatus {
  name: string;
  url: string;
  healthy: boolean;
  last_seen?: string;
  device?: string;
  queue_depth?: number;
  processed?: number;
  skip_cameras?: string[];
  metrics?: VisionMetrics;
  drops_marked_total?: number;
}

export async function getVisionStatus(signal?: AbortSignal): Promise<VisionStatus> {
  return apiRequest<VisionStatus>('/vision/status', { signal });
}

/** PUT /settings {vision} — partial update (nil fields unchanged). */
export async function updateVisionSettings(
  config: VisionSettingsConfig,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings', {
    method: 'PUT',
    body: JSON.stringify({ vision: config }),
    signal,
  });
}

// One heartbeat sample in the in-memory history ring (#671) — the data source
// for dashboard sparklines.
export interface VisionSample {
  ts: string;
  queue_depth: number;
  processed_count: number;
  dropped_total: number;
  decode_workers: number;
  workers_busy: number;
  events_emitted: number;
}

export interface VisionMetricsResponse {
  enabled: boolean;
  instance?: string;
  points: VisionSample[];
  marked_total: number;
}

/** GET /api/vision/metrics?hours=[&instance=] — heartbeat history ring (~24h @ 30s). */
export async function getVisionMetrics(hours = 24, instance?: string, signal?: AbortSignal): Promise<VisionMetricsResponse> {
  const q = new URLSearchParams({ hours: String(hours) });
  if (instance) q.set('instance', instance);
  return apiRequest<VisionMetricsResponse>(`/vision/metrics?${q.toString()}`, { signal });
}
