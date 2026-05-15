# Issues

(none yet)

## Manual QA Findings (live NVR at 192.168.63.31:9090)

### Bug: XiaomiDevice.ip vs API "localip" field mismatch
- **Live API** returns `{"localip": "192.168.62.106", ...}` for xiaomi devices
- **Frontend type** `XiaomiDevice` has `ip: string` — field name doesn't match
- **Affected code**: `Cameras.svelte:743` and `Cameras.svelte:846` read `device.ip` → shows `undefined`
- **Fix needed**: Change frontend type from `ip` → `localip` and update template references
- **Root cause**: `CloudDevice.IP` in Go has `json:"localip"` tag (correct per Xiaomi API), but frontend type was written with `ip`

### Confirmed: Protocol validation blocks xiaomi cameras
- Current deployed build returns: `{"error":"invalid protocol \"xiaomi\", must be one of: rtsp, http, onvif"}`
- Backend fix in codebase is correct and needed

### Confirmed: One xiaomi camera already active
- Camera "小米智能摄像机 云台版2K" (cam-1ad370df) with `protocol: "xiaomi"`, `url: "xiaomi://655448418"` is recording
- This was created before protocol validation or via a bypass

### Confirmed: /api/plugins returns 404 on current deployment
- Endpoint exists in code but not yet deployed
- Will return `{"plugins": [{"name": "xiaomi", "protocols": ["xiaomi"]}]}` after deploy

### Xiaomi devices in cloud (6 total, 4 are cameras):
- virtual.78687 — 小方智能摄像机组 (virtual group, no localip) — FILTERED by isXiaomiCameraModel
- 1052458277 — 小米智能摄像机2 云台版 (192.168.62.185, online)
- 655448418 — 小米智能摄像机 云台版2K (192.168.62.106, online) ← already configured
- 1029024829 — 小米智能摄像机 云台版Pro (192.168.62.218, offline)
- 1058760647 — 7-8楼转角 (192.168.62.229, online)
- 1025887482 — 小米智能摄像机2 云台版 (192.168.61.158, online)
