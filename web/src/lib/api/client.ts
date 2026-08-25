/**
 * Base HTTP client — auth, generic fetch wrappers
 */
import { APP_BASE } from '$lib/base-path';

// Session token storage.
//
// The browser holds a stateless HMAC-signed session token (issued by
// /api/auth/login or /api/setup). It is kept in localStorage (NOT sessionStorage)
// so that opening a new tab or restarting the browser does NOT force a re-login.
// localStorage is shared across same-origin tabs, which is exactly the behavior
// users expect from an NVR dashboard.
//
// The token NEVER contains the password — only a username + expiry, signed by
// the server with a per-process pepper. See internal/middleware/token.go for the
// signing/verification scheme. Sliding renewal happens transparently: when the
// server sees a token nearing expiry it returns a fresh one in the
// X-Renewed-Token response header, and apiRequest swaps it in here.
const TOKEN_KEY = 'mibee_nvr_token';
// Renewed-token response header (must match internal/middleware/token.go).
const RENEWED_TOKEN_HEADER = 'X-Renewed-Token';

export interface AuthCredentials {
  username: string;
  password: string;
}

export interface LoginResponse {
  status: string;
  token?: string;
  expires_at?: string;
}

export interface ApiError {
  error: string;
  code?: string;
}

// API error with machine-readable code for i18n mapping
export class ApiRequestError extends Error {
  constructor(
    message: string,
    public code?: string,
  ) {
    super(message);
    this.name = 'ApiRequestError';
  }
}

export interface HealthCheck {
  status: 'ok' | 'warning' | 'error';
  message?: string;
}

export interface HealthResponse {
  status: 'ok' | 'degraded' | 'unhealthy';
  checks: Record<string, HealthCheck>;
  uptime: string;
  setup_required?: boolean;
  // True only when the backend considers this request "local": a loopback
  // connection (RemoteAddr 127.0.0.1/::1) with a loopback Host header and no
  // proxy/gateway headers, AND auth.local_bypass enabled. The SPA uses it to
  // skip the login page for local access.
  local_access?: boolean;
}

export interface SystemStats {
  cpu: {
    total: number;
    idle: number;
  };
  memory: {
    total: number;
    available: number;
    process_rss: number;
  };
  network: {
    bytes_sent: number;
    bytes_recv: number;
  };
  uptime: string;
  timestamp: number;
}

// --- Session token storage (localStorage) ---------------------------------

interface StoredSession {
  token: string;
  expiresAt: number; // epoch ms
}

function readSession(): StoredSession | null {
  try {
    const raw = localStorage.getItem(TOKEN_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as StoredSession;
    if (!parsed || typeof parsed.token !== 'string' || typeof parsed.expiresAt !== 'number') {
      return null;
    }
    return parsed;
  } catch (e) {
    console.warn('Failed to parse stored session:', e);
    return null;
  }
}

// Store the session token returned by /api/auth/login (or /api/setup). The
// token is kept in localStorage so the session survives new tabs and browser
// restarts. expiresAt is the absolute epoch-ms expiry (from the server's
// expires_at RFC3339 field, or Date.now()+fallback if absent).
export function storeToken(token: string, expiresAtIso?: string): void {
  let expiresAt: number;
  if (expiresAtIso) {
    const t = Date.parse(expiresAtIso);
    expiresAt = Number.isNaN(t) ? Date.now() + 2 * 60 * 60 * 1000 : t;
  } else {
    expiresAt = Date.now() + 2 * 60 * 60 * 1000;
  }
  const session: StoredSession = { token, expiresAt };
  localStorage.setItem(TOKEN_KEY, JSON.stringify(session));
}

// Get the raw session token, or null if absent/expired. Expired tokens are
// purged on read so a stale localStorage entry doesn't fool isAuthenticated().
export function getToken(): string | null {
  const s = readSession();
  if (!s) return null;
  if (Date.now() >= s.expiresAt) {
    // Locally expired — clear it so the next navigation goes to login cleanly.
    localStorage.removeItem(TOKEN_KEY);
    return null;
  }
  return s.token;
}

// Force a re-login from anywhere (raw fetches outside the api client, e.g.
// the WebRTC WHEP exchange): clears the stale token and routes to the login
// page. Exported so media players that bypass apiRequest() can share the
// exact 401 semantics — without this a token invalidated by a server restart
// left them retrying forever on dead credentials, rendering black tiles.
export function forceRelogin(): void {
  clearToken();
  window.location.hash = '#/login';
}

// Clear the stored session token (logout).
export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY);
}

// Silently swap in a renewed token delivered via the X-Renewed-Token response
// header, preserving the original expiry scheme. No-op when the header is absent
// or the value looks invalid.
function maybeApplyRenewedToken(response: Response): void {
  const renewed = response.headers.get(RENEWED_TOKEN_HEADER);
  if (!renewed) return;
  const s = readSession();
  // Preserve existing expiry (the server renewed near the same lifetime); if we
  // somehow have no session, fall back to +TTL from now.
  const expiresAt = s?.expiresAt ?? Date.now() + 2 * 60 * 60 * 1000;
  localStorage.setItem(TOKEN_KEY, JSON.stringify({ token: renewed, expiresAt } as StoredSession));
}

// Local-access bypass state. When true, the browser is running on the machine
// that hosts the NVR itself AND the operator enabled auth.local_bypass: the
// backend then skips credential checks for loopback requests with no
// proxy/gateway headers, and the SPA mirrors that by skipping the login page.
// (The exact gate lives in internal/middleware/auth.go — IsLocalIP only
// matches loopback, HasProxyHeaders blocks proxied requests, and the provider
// toggles on auth.local_bypass.)
let localBypass = false;

// Query /api/health (public) to learn whether the backend considers the current
// request local (see /api/health local_access). Called once during app
// bootstrap, before the router gates on isAuthenticated(). Bounded by a 3s
// timeout so a hung backend cannot block mount() forever.
export async function checkLocalBypass(): Promise<boolean> {
  try {
    const health = await healthCheck(AbortSignal.timeout(3000));
    if (health.local_access) {
      localBypass = true;
    }
  } catch {
    // Health check failed or timed out — leave localBypass false; the login
    // page will show.
  }
  return localBypass;
}

// Check if user is authenticated (has a non-expired session token, OR the
// browser is running on the NVR host machine and local access is bypassed).
export function isAuthenticated(): boolean {
  return getToken() !== null || localBypass;
}

// Get the Authorization header value for API calls: "Bearer <session-token>".
export function getAuthHeader(): string | null {
  const token = getToken();
  if (!token) return null;
  return `Bearer ${token}`;
}

// Get just the session token, for use as a ?token= query parameter where
// headers cannot be set (WebSocket upgrades, sendBeacon telemetry). The backend
// auth middleware accepts ?token=mbs_... on the same path as the Bearer header.
export function getTokenForUrl(): string | null {
  return getToken();
}

// API base URL: runtime base path (reverse-proxy / unified-gateway prefix,
// e.g. fnOS "/app/mibee-nvr") + "/api". Empty prefix keeps "/api" unchanged.
export const API_BASE = `${APP_BASE}/api`;

// Generic API request function
export async function apiRequest<T>(endpoint: string, options: RequestInit = {}): Promise<T> {
  const url = `${API_BASE}${endpoint}`;

  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  };
  if (options.headers) {
    // HeadersInit may be a Headers object, an array of [name, value] tuples, or
    // a plain record — normalize each form into the mutable record so the
    // Authorization header can be added below.
    if (options.headers instanceof Headers) {
      options.headers.forEach((v, k) => {
        headers[k] = v;
      });
    } else if (Array.isArray(options.headers)) {
      for (const [k, v] of options.headers) {
        headers[k] = v;
      }
    } else {
      Object.assign(headers, options.headers);
    }
  }

  const authHeader = getAuthHeader();
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }

  // Default 30s timeout so a hung backend (e.g. ONVIF SOAP call blocked by a
  // slow/minimal device) cannot leave every loading spinner spinning forever.
  // A caller-supplied signal (e.g. abort on unmount) always takes precedence.
  let response: Response;
  try {
    response = await fetch(url, {
      ...options,
      headers,
      signal: options.signal ?? AbortSignal.timeout(30000),
    });
  } catch (e) {
    if (e instanceof DOMException && (e.name === 'TimeoutError' || e.name === 'AbortError')) {
      throw new ApiRequestError('Request timed out', 'TIMEOUT');
    }
    throw e;
  }

  // Detect Service Worker offline response (SW returns 503 when network fails)
  if (response.status === 503) {
    window.dispatchEvent(new CustomEvent('nvr-api-offline'));
  }

  // Sliding renewal: a token nearing expiry causes the middleware to mint a
  // fresh one and return it here. Swap it into localStorage transparently.
  maybeApplyRenewedToken(response);

  if (!response.ok) {
    // 401 → session expired or invalid credentials → force re-login
    if (response.status === 401) {
      clearToken();
      window.location.hash = '#/login';
      throw new ApiRequestError('Session expired', 'AUTH_EXPIRED');
    }
    const errorData = await response.json().catch(() => ({ error: 'Unknown error' }));
    const apiErr = errorData as ApiError;
    throw new ApiRequestError(apiErr.error || `HTTP ${response.status}`, apiErr.code);
  }

  return response.json();
}

// Generic API request for blob responses (e.g. file downloads)
export async function apiRequestBlob(endpoint: string, options: RequestInit = {}): Promise<Blob> {
  const url = `${API_BASE}${endpoint}`;

  const headers: HeadersInit = {};
  const authHeader = getAuthHeader();
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }

  let response: Response;
  try {
    response = await fetch(url, { ...options, headers, signal: options.signal ?? AbortSignal.timeout(30000) });
  } catch (e) {
    if (e instanceof DOMException && (e.name === 'TimeoutError' || e.name === 'AbortError')) {
      throw new Error('Request timed out');
    }
    throw e;
  }
  // Sliding renewal applies to blob responses too (e.g. snapshot downloads).
  maybeApplyRenewedToken(response);
  if (!response.ok) {
    if (response.status === 401) {
      clearToken();
      window.location.hash = '#/login';
      throw new Error('Session expired');
    }
    throw new Error(`HTTP ${response.status}`);
  }
  return response.blob();
}

// HEAD request that returns a single response header value (or null when the
// header is absent or the request fails). Used for cheap codec probing before
// committing to a playback path — e.g. reading X-Timelapse-Codec to decide
// between <video> (H.264/H.265) and the JPEG frame cycler (MJPEG/mjpa).
//
// Non-OK responses (including 404) resolve to null rather than throwing, so
// callers can treat "unknown codec" and "no merged file" identically.
export async function apiHeadHeader(
  endpoint: string,
  headerName: string,
  options: RequestInit = {},
): Promise<string | null> {
  const url = `${API_BASE}${endpoint}`;
  const headers: HeadersInit = {};
  const authHeader = getAuthHeader();
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }
  try {
    const response = await fetch(url, {
      ...options,
      method: 'HEAD',
      headers,
      signal: options.signal ?? AbortSignal.timeout(10000),
    });
    if (!response.ok) return null;
    return response.headers.get(headerName);
  } catch {
    return null;
  }
}

// Login endpoint.
//
// The request itself still uses BasicAuth (the server validates user:pass via
// bcrypt), but on success the server returns a stateless signed session token
// which we persist — the browser then NEVER holds the password again. This is
// the core security improvement over the old base64(user:pass)-in-sessionStorage
// scheme.
export async function login(username: string, password: string, signal?: AbortSignal): Promise<LoginResponse> {
  const authHeader = `Basic ${btoa(`${username}:${password}`)}`;

  const response = await fetch(`${API_BASE}/auth/login`, {
    method: 'POST',
    headers: {
      Authorization: authHeader,
    },
    signal,
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: 'Invalid credentials' }));
    // Check for setup required (no password configured)
    if ((errorData as ApiError).code === 'SETUP_REQUIRED') {
      throw new Error('setup_required');
    }
    throw new Error((errorData as ApiError).error || 'Invalid credentials');
  }

  const data = (await response.json()) as LoginResponse;

  // Store the signed session token (NOT the password). expires_at drives local
  // expiry so isAuthenticated() can short-circuit without a round-trip.
  if (data.token) {
    storeToken(data.token, data.expires_at);
  }

  return data;
}

// Logout
export function logout(): void {
  clearToken();
  window.location.hash = '#/login';
}

// Unified-gateway session bootstrap (fnOS SSO, #394).
//
// On boot, when no session token is stored, the SPA asks the NVR to mint one
// via the gateway: inside the fnOS desktop the request arrives through the
// fnOS unified gateway with a verified NAS-admin identity and returns a normal
// signed session token — the user never sees the login page. Accessed
// directly on the NVR port there is no gateway identity, the endpoint 401s,
// and the app falls back to the regular login form. Must run BEFORE mount so
// the synchronous isAuthenticated() route gate sees the stored token.
export async function tryGatewaySession(): Promise<boolean> {
  if (getToken()) return true;
  try {
    const response = await fetch(`${API_BASE}/auth/gateway-session`, {
      signal: AbortSignal.timeout(5000),
    });
    if (!response.ok) return false;
    const data = (await response.json()) as LoginResponse;
    if (!data.token) return false;
    storeToken(data.token, data.expires_at);
    return true;
  } catch {
    return false;
  }
}

// Health check (no auth required)
export async function healthCheck(signal?: AbortSignal): Promise<HealthResponse> {
  const response = await fetch(`${API_BASE}/health`, { signal });
  return response.json();
}

// System stats endpoint
export async function getSystemStats(signal?: AbortSignal): Promise<SystemStats> {
  return apiRequest<SystemStats>('/stats/system', { signal });
}

// Setup response
export interface SetupResponse {
  status: string;
  token: string;
  expires_at?: string;
}

// First-time setup endpoint (no auth required)
export async function setupApi(
  username: string,
  password: string,
  language?: string,
  storagePath?: string,
): Promise<SetupResponse> {
  const body: Record<string, string> = { username, password };
  if (language) body.language = language;
  if (storagePath) body.storage_path = storagePath;

  const response = await fetch(`${API_BASE}/setup`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: 'Setup failed' }));
    throw new Error((errorData as ApiError).error || `HTTP ${response.status}`);
  }

  return response.json();
}
