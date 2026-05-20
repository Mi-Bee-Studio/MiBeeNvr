/**
 * Base HTTP client — auth, generic fetch wrappers
 */

// Auth credentials storage (sessionStorage — cleared on browser close)
const AUTH_KEY = 'mibee_nvr_auth';

export interface AuthCredentials {
  username: string;
  password: string;
}

export interface LoginResponse {
  status: string;
}

export interface ApiError {
  error: string;
  code?: string;
}

// API error with machine-readable code for i18n mapping
export class ApiRequestError extends Error {
  constructor(message: string, public code?: string) {
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

// Store credentials in sessionStorage (cleared on browser close)
export function storeCredentials(username: string, password: string): void {
  const encoded = btoa(`${username}:${password}`);
  sessionStorage.setItem(AUTH_KEY, encoded);
}

// Get credentials from sessionStorage
export function getCredentials(): AuthCredentials | null {
  const encoded = sessionStorage.getItem(AUTH_KEY);
  if (!encoded) return null;

  try {
    const decoded = atob(encoded);
    const [username, password] = decoded.split(':');
    return { username, password };
  } catch (e) { console.warn('Failed to decode credentials:', e);
return null;
}
}

// Clear credentials
export function clearCredentials(): void {
  sessionStorage.removeItem(AUTH_KEY);
}

// Check if user is authenticated
export function isAuthenticated(): boolean {
  return getCredentials() !== null;
}

// Get Basic Auth header value
export function getAuthHeader(): string | null {
  const creds = getCredentials();
  if (!creds) return null;

  const encoded = btoa(`${creds.username}:${creds.password}`);
  return `Basic ${encoded}`;
}

// API base URL (relative path for embedded static files)
export const API_BASE = '/api';

// Generic API request function
export async function apiRequest<T>(
  endpoint: string,
  options: RequestInit = {}
): Promise<T> {
  const url = `${API_BASE}${endpoint}`;

  const headers: HeadersInit = {
    'Content-Type': 'application/json',
    ...options.headers,
  };

  const authHeader = getAuthHeader();
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }

  const response = await fetch(url, {
    ...options,
    headers,
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: 'Unknown error' }));
    const apiErr = errorData as ApiError;
    throw new ApiRequestError(apiErr.error || `HTTP ${response.status}`, apiErr.code);
  }

  return response.json();
}

// Generic API request for blob responses (e.g. file downloads)
export async function apiRequestBlob(
  endpoint: string,
  options: RequestInit = {}
): Promise<Blob> {
  const url = `${API_BASE}${endpoint}`;

  const headers: HeadersInit = {};
  const authHeader = getAuthHeader();
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }

  const response = await fetch(url, { ...options, headers });
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`);
  }
  return response.blob();
}

// Login endpoint
export async function login(
  username: string,
  password: string,
  signal?: AbortSignal
): Promise<LoginResponse> {
  const authHeader = `Basic ${btoa(`${username}:${password}`)}`;

  const response = await fetch('/api/auth/login', {
    method: 'POST',
    headers: {
      'Authorization': authHeader,
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

  const data = await response.json();

  // Store credentials on success
  storeCredentials(username, password);

  return data as LoginResponse;
}

// Logout
export function logout(): void {
  clearCredentials();
  window.location.hash = '#/login';
}

// Health check (no auth required)
export async function healthCheck(signal?: AbortSignal): Promise<HealthResponse> {
  const response = await fetch('/api/health', { signal });
  return response.json();
}

// System stats endpoint
export async function getSystemStats(signal?: AbortSignal): Promise<SystemStats> {
  return apiRequest<SystemStats>('/stats/system', { signal });
}
