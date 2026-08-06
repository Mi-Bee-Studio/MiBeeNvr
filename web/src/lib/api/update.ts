/**
 * Update / version check API — sensing layer only (never executes an upgrade).
 * Backend polls GitHub Releases and caches the result; the UI reads it here.
 */
import { apiRequest } from './client';

export interface UpdateStatus {
  /** Running version (injected via ldflags at build time; "dev" for local builds). */
  current: string;
  /** Newest tag from GitHub ("vX.Y.Z"); empty string if no successful check yet. */
  latest: string;
  /** True only when `latest` is strictly newer than `current` (semver). */
  update_available: boolean;
  /** ISO8601 publish time of the latest release. */
  published_at?: string;
  /** GitHub release page URL. */
  html_url?: string;
  /** Release notes body (GitHub markdown). Render with sanitization. */
  changelog?: string;
  /** Where the app runs: "docker" | "binary" | "" — drives upgrade-instruction text. */
  deployment: string;
  /** ISO8601 time of the last successful / 304 check. */
  checked_at?: string;
  /** Release stream: "stable" | "beta". */
  channel?: string;
}

/** Lightweight current version + deployment (no network call). */
export async function getVersion(): Promise<UpdateStatus> {
  // apiRequest already prepends API_BASE ("/api"), so the endpoint is relative.
  return apiRequest<UpdateStatus>('/version');
}

/** Cached version-check status. */
export async function getUpdateStatus(): Promise<UpdateStatus> {
  return apiRequest<UpdateStatus>('/update/check');
}

/** Force a refresh on the backend ("check now" button) before returning. */
export async function refreshUpdateStatus(): Promise<UpdateStatus> {
  return apiRequest<UpdateStatus>('/update/check', { method: 'POST' });
}
