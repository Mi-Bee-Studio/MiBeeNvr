# Zero-Touch Camera Onboarding

Some cameras leave the factory pointed at a vendor-default subnet — Hikvision-family devices ship as `192.168.1.64`, for example. If the NVR lives on a different subnet, the camera is unreachable until somebody re-addresses it by hand. Zero-touch onboarding lets the NVR do that itself: it borrows a temporary address inside the camera's default subnet, finds the device, moves it onto the NVR's own subnet, and enrolls it — no laptop, no manual re-addressing.

This is the cross-subnet sibling of [ONVIF discovery](onvif-discovery.md): discovery finds cameras already reachable on the LAN; onboarding retrieves cameras that are not.

## When You Need This

- A new camera still on its factory defaults (`192.168.1.x` while the NVR sits on another subnet).
- An existing camera that drifted back to the vendor default after a reset.
- Batch bring-up: sweep once, adopt several devices in a row.

If the camera is already on the NVR's subnet, use [ONVIF discovery](onvif-discovery.md) instead — no aliasing required.

## How It Works

1. **Alias.** The NVR binds a temporary secondary IP (the alias) on its primary interface, inside the configured default subnet. Cameras on that subnet can now talk to it directly — they route via the gateway otherwise, and vendor firmware strictly does so.
2. **Scan.** A bounded sweep probes every host address (.1–.254) of the default subnet on port 80 — /24 max, 16 probes in parallel, 1.5 s per probe, a full /24 finishes in ~30 s worst case — and classifies what answers:
   - Hikvision bootstrap face first: unauthenticated `GET /ISAPI/System/deviceInfo`.
   - Generic ONVIF fallback: unauthenticated `GetDeviceInformation`.
3. **Adopt.** For a chosen candidate the NVR rewrites the camera's network settings — ONVIF `SetNetworkInterfaces` first, with a Hikvision ISAPI fallback (static mode) for firmware that rejects every ONVIF write shape. It then relocates the camera at its new address by matching the serial number read during the scan, and enrolls it with the serial as the stable camera identity.
4. **Cleanup.** The alias is released on every exit path — success, failure, or timeout.

The address change applies immediately on current Hikvision firmware (field-verified, no reboot; `reboot_needed` in the result tells you when a device does ask for one).

## Requirements

| Requirement | Why |
|---|---|
| Feature enabled (`camera_onboarding.enabled: true`) | Off by default — the flow rewrites device network settings |
| `CAP_NET_ADMIN` on Linux | Binding/removing the alias is a network-admin operation (see the comments in `deploy/mibee-nvr.service`) |
| Default subnet on the same broadcast domain | The sweep only reaches L2-adjacent hosts; a routed-but-isolated subnet is out of reach |
| Device reachable on HTTP port 80 | Both probe faces (ISAPI, ONVIF) ride the camera's HTTP server |

## Configuration

```yaml
camera_onboarding:
  enabled: true
  default_subnet: "192.168.1.0/24"  # where factory-fresh cameras sit
  alias_ip: ""                      # default: the subnet's last host address (.254 for a /24)
  target_dhcp: true                 # move adopted cameras to DHCP
  target_static_ip: ""              # static-mode target instead (on the NVR's subnet)
  scan_interval_s: 0                # 0 = manual scans only; >= 60 = periodic background sweep
```

| Key | Default | Notes |
|-----|---------|-------|
| `enabled` | `false` | Everything else is ignored while off |
| `default_subnet` | — (required) | CIDR; wider networks are clamped to their /24 base |
| `alias_ip` | subnet's last host address | Must be inside `default_subnet` |
| `target_dhcp` | `true` unless `target_static_ip` is set | Tri-state; per-request override available |
| `target_static_ip` | — | Valid IPv4 required in static mode |
| `scan_interval_s` | `0` | `0` = manual only; values below 60 are rejected at startup (they would hammer the LAN) |

## Walkthrough

All requests assume the API on `localhost:9090` with HTTP basic auth. Every onboarding endpoint answers `409 Conflict` while the feature is disabled.

### 1. Probe the feature

```bash
curl -u admin:password http://localhost:9090/api/onboarding/status
```

When enabled, the status reports the configured subnet, whether the alias is currently bound, and the previous scan's outcome.

### 2. Scan the default subnet

```bash
curl -u admin:password -X POST http://localhost:9090/api/onboarding/scan
# {"job_id":"onb-..."}

curl -u admin:password http://localhost:9090/api/onboarding/jobs/onb-...
```

Poll the job until `state` is `done`; the classified devices are in `candidates`.

### 3. Read the candidates

```json
{
  "ip": "192.168.1.64",
  "state": "auth_required",
  "serial": "DS-2CD2225Y-YN...",
  "manufacturer": "Hikvision",
  "model": "DS-2CD2225Y-YN",
  "device_name": "IP Camera"
}
```

| State | Meaning | What you can do |
|-------|---------|-----------------|
| `unactivated` | Factory-fresh, never activated | Reported only — activate it first (camera web UI or vendor tool); the challenge-response activation variant is not implemented yet |
| `activated` | Activated, device info readable without credentials | Adopt with credentials |
| `auth_required` | Activated, credentials required even for device info | Adopt with credentials |

### 4. Adopt

```bash
curl -u admin:password -X POST http://localhost:9090/api/onboarding/adopt \
  -H "Content-Type: application/json" \
  -d '{"ip":"192.168.1.64","username":"admin","password":"YourPassword","mode":"static","static_ip":"192.168.10.84"}'
```

The job runs the whole chain — network change, relocation, enrollment — and reports:

```json
{
  "serial": "DS-2CD2225Y-YN...",
  "new_ip": "192.168.10.84",
  "camera_id": "cam-ab12cd34",
  "reboot_needed": false
}
```

`mode`, `static_ip` and `prefix_len` are per-request overrides; omit them to follow the configured target (DHCP by default).

- **Static mode** (recommended): the NVR polls the chosen address until the camera with the expected serial answers (~60 s budget).
- **DHCP mode**: the NVR sweeps its own subnet for the serial (~90 s budget, longer when the device reboots). If several devices report the same serial it refuses to guess and fails the job — re-run in static mode.

### 5. Verify

The enrolled camera appears under `GET /api/cameras` with a serial-derived stable ID; recording then behaves exactly like any manually added camera.

## Operational Notes

- **One operation at a time.** Scan and adopt share the alias; a second concurrent request answers `409` until the running job finishes.
- **Jobs are short-lived registry entries.** Finished jobs stay queryable; the registry keeps the most recent 64.
- **Vendor notes (Hikvision, field-verified):**
  - Some firmware rejects ONVIF `SetNetworkInterfaces` for every request shape while accepting the equivalent ISAPI write — the flow falls back automatically (static mode).
  - The ONVIF interface may enforce SHA-256 HTTP Digest with its own user table, separate from the device admin account. Credentials that work on the camera web UI may not work over ONVIF; create or check the ONVIF user in the camera's UI.
  - Address changes apply immediately; DHCP re-acquisition typically lands within a minute.
- **Factory-unactivated devices** must be activated before adoption (see candidate states above).

## Troubleshooting

| Symptom | Likely cause / fix |
|---------|--------------------|
| `409` on every endpoint | Feature disabled — set `camera_onboarding.enabled: true` and restart |
| Scan finds nothing | Camera not on `default_subnet`; different broadcast domain (VLAN, routed isolation); HTTP port 80 firewalled |
| Scan finds it, adopt fails auth | Wrong credentials; or the ONVIF user is not the device admin (create an ONVIF user in the camera UI) |
| Adopt fails at relocation | Both write faces rejected — the ISAPI fallback covers Hikvision static mode; other vendors need working ONVIF writes |
| DHCP adopt times out | No DHCP server leasing on the NVR's subnet; or the sweep found several serial matches (fail-safe) — use static mode |
| Camera "disappeared" after adopt | It moved successfully — look for it at the new address / under the cameras page |

## Security Notes

- The whole feature is **off by default** and every endpoint requires authentication.
- Scans are passive, unauthenticated reads of device info endpoints; nothing is written during a scan.
- The alias exists only while a scan/adopt is running and is always released afterwards.
- Adopt sends device credentials to the camera itself over its local HTTP connection — keep the default subnet on a trusted L2.

## See Also

- [API reference — Onboarding](api/onboarding.md)
- [Camera Guide](camera-guide.md) — zero-touch section with the same flow in brief
- [ONVIF Discovery](onvif-discovery.md) — same-subnet plug-and-play enrollment
