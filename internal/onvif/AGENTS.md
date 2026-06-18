# MiBee NVR — ONVIF Package

## Overview

ONVIF device client. WS-Discovery, media profile enumeration, RTSP stream URI resolution, PTZ control. Used by `recorder/onvif.go` to create delegate recorders.

## Structure

```
client.go       # Client struct — Connect, GetProfiles, GetStreamUri, GetDeviceInformation, GetCapabilities (cached)
discovery.go    # WS-Discovery — multicast probe, parse responses, timeout handling
onvifgo.go      # onvif-go wrapper — raw SOAP calls for advanced operations
ptz.go          # PTZController — absolute/relative move, zoom, continuous move, presets
imaging.go      # Imaging — GetSnapshotUri, GetImagingSettings (minimal devices return 404)
events.go       # Event subscription — WS-Notification polling (minimal devices unsupported)
device_mgmt.go  # Device management — GetUsers, system reboot, network config
snapshot.go     # Snapshot helper — URI resolution wrapper
types.go        # Data types — DiscoveredDevice, DeviceProfile, DeviceInfo
interfaces.go   # PTZController + Client interfaces — enables mock testing
mocks.go        # Test mocks — MockClient, MockPTZController
*_test.go       # Per-component tests with mocks
```

## Where To Look

| Task | Location | Notes |
|------|----------|-------|
| Fix device connection | `client.go` `Connect()` | Creates onvif-go Client, validates endpoint |
| Fix stream URI | `client.go` `GetStreamUri()` | Returns RTSP URL, respects profile token + transport |
| Fix profile selection | `client.go` `GetProfiles()` | Enumerates media profiles with encoding/resolution |
| Fix discovery | `discovery.go` `Discover()` | WS-Discovery multicast, 3s timeout, dedup by UUID |
| Fix PTZ control | `ptz.go` `NewPTZController()` | Wraps onvif-go PTZ service, supports absolute/relative/continuous |
| Add ONVIF operation | `onvifgo.go` | Raw SOAP call wrapper using onvif-go library |
| Change device types | `types.go` | DiscoveredDevice, DeviceProfile, DeviceInfo structs |
| Fix imaging/snapshot | `imaging.go` / `snapshot.go` | GetSnapshotUri, GetImagingSettings — minimal devices (ESP32) return 404 |
| Fix event subscription | `events.go` | WS-Notification polling — unsupported on minimal ONVIF devices |
| Fix device management | `device_mgmt.go` | GetUsers, system reboot — WS-Security auth issues on some devices |
| Capabilities caching | `client.go` `GetCapabilities()` | Cached after first call; minimal devices get all-false caps (not error) |

## Conventions

- **Client lifecycle**: `NewClient()` → `Connect()` → operations. `sync.Mutex` protects all SOAP calls
- **Profile auto-selection**: ONVIF recorder (`recorder/onvif.go`) auto-selects first profile if none specified
- **Encoding detection**: Falls back to RTSP DESCRIBE if profile encoding is empty (some cameras don't report it)
- **PTZ interface**: `PTZController` interface in `interfaces.go` enables mock testing without real device
- **Discovery dedup**: Deduplicates by UUID, sorts by hardware name
- **Error wrapping**: All errors wrapped with context (`fmt.Errorf("onvif: ...")`)

- **Capabilities caching**: `GetCapabilities()` result is cached after first call; minimal ONVIF devices (ESP32 MiBeeCam) get all-false caps instead of an error. Gate advanced calls (PTZ, snapshot, events) on caps

## Anti-Patterns

- **DO NOT** call SOAP operations before `Connect()` — client will panic
- **DO NOT** assume profile encoding is accurate — some cameras return empty; use RTSP DESCRIBE fallback
- **DO NOT** forget mutex when adding new SOAP operations — onvif-go client is not goroutine-safe
- **DO NOT** hardcode profile token — cameras have different profile names (Profile1, Profile2_1_1_1, etc.)
