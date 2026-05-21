/**
 * Settings API — cleanup, webdav, merge, feature flags
 */
import { apiRequest } from './client';

// --- Types ---

export interface CleanupConfig {
  retention_days: number;
  disk_threshold_percent: number;
  check_interval: string;
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
}

export interface SRTConfig {
  enabled: boolean;
  port: number;
}

export interface StreamingConfig {
  default_protocol: string; // webrtc | flv | hls | ll-hls
  webrtc: WebRTCConfig;
  flv: FLVStreamingConfig;
  hls: HLSStreamingConfig;
  rtmp?: RTMPConfig;
  srt?: SRTConfig;
}

export interface SettingsConfig {
  cleanup: CleanupConfig;
  webdav: WebDAVConfig;
  streaming?: StreamingConfig;
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
  signal?: AbortSignal
): Promise<{ status: string }> {
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

export async function updateMergeSettings(
  config: MergeConfig,
  signal?: AbortSignal
): Promise<{ status: string }> {
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

export async function updateFeatures(
  features: FeatureFlags,
  signal?: AbortSignal
): Promise<void> {
  await apiRequest('/features', {
    method: 'PUT',
    body: JSON.stringify(features),
    signal,
  });
}

// --- Streaming settings ---

export async function getStreamingSettings(signal?: AbortSignal): Promise<StreamingConfig> {
  return apiRequest<StreamingConfig>('/settings/streaming', { signal });
}

export async function updateStreamingSettings(
  config: StreamingConfig,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/streaming', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}
