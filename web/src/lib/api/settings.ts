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

export interface SettingsConfig {
  cleanup: CleanupConfig;
  webdav: WebDAVConfig;
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
