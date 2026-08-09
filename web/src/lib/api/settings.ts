/**
 * Settings API — cleanup, webdav, merge, feature flags
 */
import { apiRequest } from './client';

// --- Types ---

export interface CleanupConfig {
  retention_days: number;
  disk_threshold_percent: number;
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
  stream_keys?: Record<string, string>; // stream_key → camera_id
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
  }>;
}

export interface SettingsConfig {
  cleanup: CleanupConfig;
  webdav: WebDAVConfig;
  streaming?: StreamingConfig;
  mibeevision?: MiBeeVisionConfig;
  timezone?: string; // "Local", "UTC", or IANA timezone name
  timezone_display?: string; // Human-readable timezone label (e.g. "Asia/Shanghai (UTC+8)")
  server?: { listen?: string }; // listen address ":9090" — changed via Settings UI
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

export async function updateSettings(settings: SettingsConfig, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings', {
    method: 'PUT',
    body: JSON.stringify(settings),
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
