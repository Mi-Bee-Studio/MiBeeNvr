/**
 * Xiaomi API — cloud auth, device listing, sync
 */
import { apiRequest } from './client';

// --- Types ---

export interface XiaomiDevice {
  did: string;
  name: string;
  model: string;
  localip: string;
  isOnline: boolean;
}

export interface XiaomiDevicesResponse {
  devices: XiaomiDevice[];
  message?: string;
}

export interface XiaomiAuthResponse {
  user_id?: string;
  status?: string;
  captcha?: string;       // base64-encoded image
  verify_phone?: string;  // masked phone number
  verify_email?: string;  // masked email
  session_id?: string;    // for captcha/verify continuation
}

// --- Functions ---

export async function xiaomiAuth(
  username: string,
  password: string,
  region?: string,
  signal?: AbortSignal
): Promise<XiaomiAuthResponse> {
  return apiRequest('/xiaomi/auth', {
    method: 'POST',
    body: JSON.stringify({ username, password, region: region || 'cn' }),
    signal,
  });
}

export async function xiaomiDevices(signal?: AbortSignal): Promise<XiaomiDevicesResponse> {
  return apiRequest<XiaomiDevicesResponse>('/xiaomi/devices', { signal });
}

export async function xiaomiCaptcha(
  sessionId: string,
  captchaCode: string,
  signal?: AbortSignal
): Promise<XiaomiAuthResponse> {
  return apiRequest('/xiaomi/captcha', {
    method: 'POST',
    body: JSON.stringify({ session_id: sessionId, captcha_code: captchaCode }),
    signal,
  });
}

export async function xiaomiVerify(
  sessionId: string,
  ticket: string,
  signal?: AbortSignal
): Promise<XiaomiAuthResponse> {
  return apiRequest('/xiaomi/verify', {
    method: 'POST',
    body: JSON.stringify({ session_id: sessionId, ticket }),
    signal,
  });
}

export async function xiaomiSync(signal?: AbortSignal): Promise<{ synced: number; total: number }> {
  return apiRequest('/xiaomi/sync', { method: 'POST', signal });
}
