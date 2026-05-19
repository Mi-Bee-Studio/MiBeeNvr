/**
 * API barrel — re-exports everything so existing `$lib/api` imports work unchanged.
 */

// Client — auth, base fetch wrappers
export {
  storeCredentials,
  getCredentials,
  clearCredentials,
  isAuthenticated,
  login,
  logout,
  healthCheck,
  getSystemStats,
  getAuthHeader,
  API_BASE,
  apiRequest,
  apiRequestBlob,
  ApiRequestError,
} from './client';

export type {
  AuthCredentials,
  LoginResponse,
  ApiError,
  HealthCheck,
  HealthResponse,
  SystemStats,
} from './client';

// Cameras — CRUD, ONVIF, PTZ, protocols
export {
  listCameras,
  createCamera,
  getCamera,
  updateCamera,
  deleteCamera,
  enableCamera,
  disableCamera,
  startCamera,
  stopCamera,
  getDashboardCameras,
  getSnapshotUrl,
  testConnection,
  getMergeConfig,
  updateMergeConfig,
  deleteCameraMergeConfig,
  ptzMove,
  ptzStop,
  getPTZStatus,
  discoverONVIFDevices,
  getONVIFDeviceDetail,
  probeONVIFDevice,
  listProtocols,
  normalizeProtocol,
  buildProtocolsMap,
  getProtocolCapabilities,
  DEFAULT_PROTOCOLS,
} from './cameras';

export type {
  Camera,
  CreateCameraRequest,
  UpdateCameraRequest,
  DiscoveredDevice,
  DeviceInfo,
  DeviceProfile,
  ONVIFDeviceDetail,
  PTZMoveRequest,
  PTZStatus,
  ProtocolCapabilities,
  ProtocolInfo,
  MergeConfig,
  TestConnectionRequest,
  TestConnectionResult,
} from './cameras';

// Recordings — list, download, frames, stats, archives
export {
  listRecordings,
  getRecording,
  deleteRecording,
  batchDeleteRecordings,
  getRecordingDownloadUrl,
  downloadRecording,
  listFrames,
  loadFrameBlob,
  loadRecordingVideoBlob,
  getStats,
  getStatsTrends,
  listArchives,
  listArchiveRecordings,
  deleteArchiveGroup,
  deleteArchiveRecording,
  setArchiveRetention,
} from './recordings';

export type {
  Recording,
  FrameInfo,
  FramesResponse,
  RecordingListResponse,
  StorageStats,
  DailyStats,
  ArchiveGroup,
  ArchiveListResponse,
} from './recordings';

// Settings — cleanup, webdav, merge, features
export {
  getSettings,
  updateSettings,
  getMergeSettings,
  updateMergeSettings,
  getMergeStatus,
  getMergePending,
  getFeatures,
  updateFeatures,
} from './settings';

export type {
  CleanupConfig,
  WebDAVConfig,
  SettingsConfig,
  MergeStatus,
  MergePending,
  FeatureFlags,
} from './settings';

// Xiaomi — cloud auth, devices, sync
export {
  xiaomiAuth,
  xiaomiDevices,
  xiaomiCaptcha,
  xiaomiVerify,
  xiaomiSync,
} from './xiaomi';

export type {
  XiaomiDevice,
  XiaomiDevicesResponse,
  XiaomiAuthResponse,
} from './xiaomi';
