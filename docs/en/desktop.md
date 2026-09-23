# Desktop Edition (Windows / macOS)

> Applies to MiBeeNvr v0.13.0 · new platform, feature parity with Docker / bare-metal Linux

Starting with v0.13.0, MiBee NVR ships **desktop editions for Windows and macOS**: single-file installers, a system tray (Windows) / menubar helper (macOS), and a passwordless local-trust model that works out of the box. They turn an always-on Windows mini PC or a Mac mini into a home NVR — no Docker, no command line.

## Installation

### Windows

1. Download `MiBeeNVR-Setup-v0.13.0-windows-amd64.exe` from [GitHub Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases) (the installer *is* the server itself — a single file)
2. Double-click to run — it installs, starts, and opens the browser into the first-run wizard in one step
3. After installation the MiBee icon appears in the system tray: Open Web UI / Change Password / Listen Address / Quit
4. Uninstall: the MiBee NVR entry in Windows Settings → Apps (or the Control Panel); data is kept by default — tick the delete option or run `mibee-nvr uninstall --purge` to remove it as well

> The Windows build is a GUI-subsystem binary — no black cmd window. Closing the browser doesn't affect recording; quit the NVR from the tray menu. When launched from a terminal, console output re-attaches automatically (AttachConsole).

### macOS

1. Download `MiBeeNVR-v0.13.0-macOS-universal.dmg` (universal arm64 + amd64)
2. Open the DMG and drag **MiBeeNVR.app** to Applications
3. Double-click to run for the first time: it installs the service and opens the first-run wizard; the MiBee helper icon appears in the menubar
4. The menubar helper offers: Open Web UI / Change Password / Change Listen Address / Quit
5. Uninstall: run `mibee-nvr uninstall` in a terminal (data is kept; `--purge` removes it as well)

> macOS 13+. If Gatekeeper warns about an unidentified developer, right-click → Open to allow it once.

## Trust model (differences from the server editions)

The desktop editions treat themselves as **personal software on this machine** by default:

- **Listens on `127.0.0.1` only** — not exposed to the LAN. To reach the NVR from a phone or another computer, change the listen address in the tray/menubar (e.g. `0.0.0.0:9090`); the switch happens in-process without interrupting recording. Set a password immediately after opening it up
- **Passwordless local login** (`local_bypass`): loopback access is allowed through automatically — the local browser just works, no password prompt; **cross-site browser requests get no free pass** (`Sec-Fetch-Site` check)
- Changing the password no longer requires the old one (a loopback request is authorization enough) — a forgotten password can be reset right on the machine
- The menubar/tray **Quit** and **Shutdown** (`POST /api/system/shutdown`) and **Change Listen Address** (`PUT /api/system/listen`) actions accept loopback requests only — remote calls always get a 403

## Data & upgrades

- Data directory: `%LOCALAPPDATA%\MiBeeNVR` on Windows (holds `mibee-nvr.yaml`, recordings, the database, and `nvr.log`); `~/Library/Application Support/MiBeeNVR` on macOS. The program itself lives in `%LOCALAPPDATA%\Programs\MiBeeNVR` (Windows) / `~/Applications/MiBeeNVR` (macOS)
- Uninstalling keeps data by default; `mibee-nvr uninstall --purge` removes it as well
- **The desktop editions don't take part in in-app auto-update** (`mibee-nvr update` refuses to run on Windows/macOS) — upgrading means downloading the new installer and running it again; config and data are preserved
- The settings page's one-click upgrade is unavailable on desktop; the version check keeps working as usual

## FAQ

| Question | Answer |
|----------|--------|
| Does the NVR stop when I close the browser? | No — the server process keeps running; quitting goes through the tray/menubar only |
| Can other devices on the LAN reach it? | Yes — change the listen address to `0.0.0.0:9090` in the tray/menubar first, then set a password |
| Why does the page open without a login? | Passwordless loopback access (`local_bypass`) — expected desktop behavior; LAN access still requires a password |
| Does it start at boot? | Yes — Windows registers a per-user login autostart plus a Start-menu shortcut; macOS uses a LaunchAgent (current user only, no admin rights needed) |
| Any feature differences vs. the Docker edition? | Recording / live streaming / AI / integrations are identical; only the deployment form and trust model differ |

## Next Steps

- [Quick Start](quickstart.md) — add your first camera
- [Configuration Reference](configuration.md) — the full config-file reference
- [Performance Tuning](performance.md) — IO / memory tuning on desktop hardware
