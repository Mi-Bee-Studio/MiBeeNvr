// P2P external access + commercial license (feat/p2p-device line).
// Settings GET/PUT hot-applies the link (no restart); status is the live
// diagnostics snapshot; license install hot-applies to the P2P gate.

import { apiRequest } from './client';

// --- P2P settings ---

// GET response — the password itself is never returned.
export interface P2pSettings {
  enabled: boolean;
  server_url: string;
  product_id: string;
  peer_id: string;
  service_name: string;
  username: string;
  password_configured: boolean;
  client_id: string;
  transport_timeout_s: number;
  max_clients: number;
  device_key_configured: boolean;
}

// PUT body — all fields optional (omitted = unchanged). Blank password keeps
// the stored one; clear_credentials wipes username+password.
export interface P2pSettingsUpdate {
  enabled?: boolean;
  server_url?: string;
  product_id?: string;
  peer_id?: string;
  service_name?: string;
  username?: string;
  password?: string;
  clear_credentials?: boolean;
  transport_timeout_s?: number;
  max_clients?: number;
}

export async function getP2pSettings(signal?: AbortSignal): Promise<P2pSettings> {
  return apiRequest<P2pSettings>('/settings/p2p', { signal });
}

export async function updateP2pSettings(
  config: P2pSettingsUpdate,
  signal?: AbortSignal,
): Promise<P2pStatus> {
  return apiRequest<P2pStatus>('/settings/p2p', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// --- P2P status (diagnostics) ---

export interface P2pRejection {
  code: number;
  reason: string;
  source: 'hello' | 'session';
  at: string;
}

// --- Account association (M1 local-confirm pairing, docs/05) ---
// App requests on LAN; the device owner confirms here; the App then pushes
// runtime config via PUT /settings/p2p.

export interface P2pAssociationEntry {
  platform_user_id: string;
  platform_username: string;
  display_name: string;
  request_id: string;
  at: string;
  confirmed_at?: string;
}

export interface P2pAssociationState {
  pending?: P2pAssociationEntry | null;
  confirmed?: P2pAssociationEntry | null;
}

export async function getP2pAssociation(signal?: AbortSignal): Promise<P2pAssociationState> {
  return apiRequest<P2pAssociationState>('/p2p/association', { signal });
}

export async function confirmP2pAssociation(requestId: string): Promise<P2pAssociationState> {
  return apiRequest<P2pAssociationState>('/p2p/association/confirm', {
    method: 'POST',
    body: JSON.stringify({ request_id: requestId }),
  });
}

// 拒绝申请与取消关联共用（清空 pending + confirmed）。
export async function cancelP2pAssociation(): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/p2p/association/cancel', {
    method: 'POST',
    body: '{}',
  });
}

export interface P2pGateStatus {
  allowed: boolean;
  state: string;
}

export interface P2pStatus {
  available?: boolean; // absent agent capability
  enabled: boolean;
  online: boolean;
  peer_id: string;
  product_id: string;
  service: string;
  server_url: string;
  active_clients: number;
  max_clients: number;
  cap_source: string; // license | config | license+config | default
  rejected_total: number;
  license?: P2pGateStatus;
  last_rejection?: P2pRejection;
}

export async function getP2pStatus(signal?: AbortSignal): Promise<P2pStatus> {
  return apiRequest<P2pStatus>('/p2p/status', { signal });
}

// --- Commercial license ---

export interface LicenseDocumentInfo {
  plan?: string;
  serial?: string;
  features?: string[];
  max_clients?: number;
  expires_at?: string;
}

export interface LicenseStatus {
  state: 'none' | 'invalid' | 'expired' | 'expiring' | 'valid';
  required: boolean;
  document?: LicenseDocumentInfo;
  reason?: string;
  time_left_sec?: number;
  allowed: boolean;
}

export interface LicenseInstall {
  document: string; // '' = clear
  public_key?: string; // '' = keep configured key
  required?: boolean;
}

export async function getLicense(signal?: AbortSignal): Promise<LicenseStatus> {
  return apiRequest<LicenseStatus>('/license', { signal });
}

export async function installLicense(
  body: LicenseInstall,
  signal?: AbortSignal,
): Promise<LicenseStatus> {
  return apiRequest<LicenseStatus>('/license', {
    method: 'PUT',
    body: JSON.stringify(body),
    signal,
  });
}

// --- Viewer interconnect quality (dashboard P2P card, 2026-10-07) ---
// The App self-measures (tunnel HTTP probe RTT + local-bridge byte counters)
// and reports periodically; the dashboard card renders only when the device
// is P2P-enabled AND account-associated (user-ruled visibility).

export interface P2pViewerSample {
  at: string;
  mode: 'tunnel' | 'relay';
  rtt_ms: number;
  kbps_up: number;
  kbps_down: number;
}

export interface P2pViewer {
  self_addr: string;
  mode: 'tunnel' | 'relay';
  rtt_ms: number;
  kbps_up: number;
  kbps_down: number;
  bytes_total: number;
  app_version: string;
  updated_at: string;
  stale: boolean;
  samples: P2pViewerSample[];
}

export interface P2pViewerStatus {
  enabled: boolean;
  associated: boolean;
  online: boolean;
  clients: number;
  /** 兼容字段 = viewers[0]（最新上报的观众）。 */
  viewer: P2pViewer | null;
  /** 多观众（多 App）各自的上报态，按最近上报新→旧。 */
  viewers: P2pViewer[];
  /** 连接记录（链路事件，旧→新）。 */
  events: P2pLinkEvent[];
  associ_name: string;
}

export interface P2pLinkEvent {
  at: string;
  type: string;
  session_id?: string;
  detail?: string;
}

export async function getP2pViewer(signal?: AbortSignal): Promise<P2pViewerStatus> {
  return apiRequest<P2pViewerStatus>('/p2p/viewer', { signal });
}
