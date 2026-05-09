/* ... (existing content up to line 460) ... */

// Dashboard-related types and functions
// Interfaces
export interface DiscoveredDevice {
  id: string;
  name: string;
  protocol: string;
  url: string;
  enabled: boolean;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
}
export interface ONVIFProfile {
  profileName: string;
  profileUri: string;
  capabilities: {
    video: {
      resolution: string[];
      frameRate: number[];
    };
    ptz: {
      supported: boolean;
      capabilities: {
        move: {
          speed: number[];
          direction: string[];
        };
      };
    };
  };
}
export interface ONVIFDeviceDetail {
  device: ONVIFProfile;
  capabilities: {
    video: {
      resolution: string[];
      frameRate: number[];
    };
    ptz: {
      supported: boolean;
      capabilities: {
        move: {
          speed: number[];
          direction: string[];
        };
      };
    };
  };
}
export interface PTZMoveRequest {
  speed: number;
  direction: string;
}
export interface PTZStatus {
  currentSpeed: number;
  currentDirection: string;
  status: 'moving' | 'stopped';
}

// Functions
export function getDashboardCameras(): Promise<Camera[]> {
  return apiRequest('/dashboard/cameras');
}
export async function discoverONVIFDevices(timeout: number = 5000): Promise<DiscoveredDevice[]> {
  return apiRequest<DiscoveredDevice[]>(`/onvif/discover?timeout=${timeout}`);
}
export async function getONVIFDeviceDetail(ip: string): Promise<ONVIFDeviceDetail> {
  return apiRequest<ONVIFDeviceDetail>(`/onvif/device/${ip}`);
}
export async function addONVIFCamera(data: ONVIFProfile): Promise<Camera> {
  return apiRequest('/cameras', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}
export async function ptzMove(cameraId: string, request: PTZMoveRequest): Promise<PTZStatus> {
  return apiRequest<PTZStatus>(`/cameras/${cameraId}/ptz/move', {
    method: 'POST',
    body: JSON.stringify(request),
  });
}
export async function ptzStop(cameraId: string): Promise<PTZStatus> {
  return apiRequest<PTZStatus>(`/cameras/${cameraId}/ptz/stop');
}
export async function getPTZStatus(cameraId: string): Promise<PTZStatus> {
  return apiRequest<PTZStatus>(`/cameras/${cameraId}/ptz/status');
}
export async function getCameraCapabilities(cameraId: string): Promise<ONVIFProfile> {
  return apiRequest<ONVIFProfile>(`/cameras/${cameraId}/capabilities');
}