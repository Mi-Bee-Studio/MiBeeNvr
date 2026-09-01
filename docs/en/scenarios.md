# Scenario Playbooks

> Six real-world scenarios for newcomers — each one is a complete path from zero to working.

Not sure where to start? Find yourself on the map:

```mermaid
flowchart LR
    Q["I want to…"] --> A["a home CCTV setup"] --> S1["Scenario 1: up in 30 minutes"]
    Q --> B["alerts only when it matters"] --> S2["Scenario 2: AI watchman"]
    Q --> C["long changes as short clips"] --> S3["Scenario 3: timelapse"]
    Q --> D["to share a live stream"] --> S4["Scenario 4: relay to YouTube"]
    Q --> E["to check home while away"] --> S5["Scenario 5: remote access"]
    Q --> F["cams elsewhere / old Xiaomi"] --> S6["Scenario 6: remote & legacy cams"]
```

---

## Scenario 1: A Working Home CCTV in 30 Minutes

**You get**: live view of every camera + automatic recording + phone access.
**You need**: an always-on device (NAS / old PC / Raspberry Pi / mini PC) + an RTSP or ONVIF camera.

### Step 1: Install the NVR (5 min)

The one-liner on Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | sudo bash
```

Docker, or an app-store package on Synology / QNAP / fnOS, work equally well — see [Quick Start](quickstart.md). Once installed, open `http://device-ip:9090`.

### Step 2: Follow the setup wizard (2 min)

First launch shows a wizard: set the admin password → pick the storage path → done. The storage path is where recordings land — choose the disk with the most free space.

![Setup wizard](images/wizard.webp)

### Step 3: Discover and add cameras (5 min)

Open the **Cameras** page and click **Discover** — ONVIF cameras on the LAN appear automatically; select one, enter its credentials, done. Undiscovered devices just need an RTSP URL.

![ONVIF discovery](images/onvif-discovery.webp)

> Tip: Hikvision / Dahua RTSP URLs usually look like `rtsp://user:pass@camera-ip:554/Streaming/Channels/101` (main stream) or `...201` (sub-stream, lighter). More brands in the [camera compatibility guide](camera-guide.md).

### Step 4: Open the surveillance grid (1 min)

Head to the **Surveillance** page — every camera on one screen. Click any tile to enlarge; PTZ cameras get on-screen direction controls.

![Surveillance grid](images/surveillance.webp)

### Step 5: Confirm recording (2 min)

MP4 segments appearing on the **Recordings** page means recording works. Playback: drag the full-day timeline — across recordings and gaps alike.

![Playback](images/playback.webp)

**Tips**:

- Defaults just work — run first, fine-tune retention and segment length later via the [config reference](config.md)
- Open the same address in a phone browser; "Add to home screen" turns it into a PWA app
- Do **not** port-forward the NVR on your router (see Scenario 5 for the safe way)

---

## Scenario 2: AI Watchman — Alert on People, Not Curtains

**The pain**: classic motion detection fires on anything — swaying curtains, light changes. You go numb within days.
**The idea**: a browser-side AI model that only recognizes people, and only inside the zones you draw.

### Step 1: Enable AI detection

Open any camera's live view and click the AI icon. The first use downloads the YOLO11 model — inference runs in your browser, zero load on the NVR server.

![AI settings](images/settings-ai.webp)

### Step 2: Draw ROI zones

Draw the zones you actually care about — the front walk, the gate entrance. Targets outside are ignored.

### Step 3: Tune until it's neither deaf nor annoying

- Confidence: start at 0.5; raise to 0.65–0.8 if false positives creep in
- Class filter: keep only `person`
- Frame skip: 8–10 on low-power devices, 3–5 on desktops

The [AI tuning guide](ai-detection-tuning.md) has recommended values for four situation archetypes (edge device / desktop / security arming / demo).

### Step 4: Pipe alerts into your life

- Events appear live on the **AI Events** page with boxed snapshots
- MQTT integration forwards events to home-automation platforms (Home Assistant / Node-RED) to drive lights, speakers, notifications

![AI events](images/dashboard-ai.webp)

**Tip**: detection compute comes from **whichever browser is watching** (WebGPU accelerated, WASM fallback) — old NVR hardware doesn't need an upgrade, and multiple viewers share the load.

---

## Scenario 3: Garden & Construction — A Day in 30 Seconds

**For**: plant growth, renovation progress, beehives, cloud timelapses.

### Step 1: Pick a timelapse mode

| Mode | Fits | Notes |
|------|------|-------|
| Dedicated timelapse camera | A spare camera exists | One stream capturing frames at a fixed interval |
| Extract from recordings | Camera already records | No extra overhead — keyframes pulled from existing recordings |

### Step 2: Set the merge window

Under **Settings → Timelapse** pick a merge window: natural-day (default) / 1h / 8h / 24h / 7d / 30d. The NVR auto-merges frames into an MP4 clip; results show up on the **Timelapse** page.

![Timelapse](images/timelapse-view.webp)

### Step 3: Watch and share

Merged clips are archived per window, playable in the browser, downloadable. H.265 cameras are supported too (compliant hvcC built automatically — plays even on Windows Edge).

**Tips**: construction sites suit the 7d window — a week of progress in one clip; plants suit natural-day — one "today's growth" clip per day. Details in [Timelapse](timelapse.md).

---

## Scenario 4: Relay Your Live Stream to YouTube / Twitch

**For**: aquarium cams, backyard bird feeders, craft-studio streams.

### Step 1: Get your platform's RTMP endpoint

Enable live streaming on the platform and copy the RTMP URL + stream key.

### Step 2: Configure the relay target in the NVR

**Camera → Edit → Relay**, add a target with the full RTMP URL (endpoint + stream key). Save — relaying starts.

![Relay config](images/relay-edit.webp)

### Step 3: Go live

Once the platform's dashboard shows the incoming stream, start the broadcast. The relay is native Go (far lower CPU than FFmpeg pipelines); strict-compatibility receivers are supported, and exotic platforms can fall back to FFmpeg per-target.

**Tips**:

- Relay and recording don't interfere — recording while relaying on the same camera is by design
- Details in [Live Relay](relay-guide.md)

---

## Scenario 5: Check Home While Away — Safely

**Rule one: never expose the NVR directly to the internet.** Use an encrypted tunnel; pick one:

| Option | Fits | One-liner |
|--------|------|-----------|
| Tailscale / ZeroTier | Easiest start | Install on both ends, same account, instant virtual LAN |
| WireGuard / self-hosted VPN | You manage the router | Router-level tunnel, whole home benefits |
| HTTPS reverse proxy | Domain + public IP | Caddy/Nginx adds TLS + auth in front |

With the tunnel up, the phone reaches the NVR's internal address (e.g. `http://100.x.x.x:9090`) and live view automatically uses WebRTC — sub-second latency, smooth even on 4G/5G.

![Live view & PTZ](images/live-ptz.webp)

**Tips**:

- Playback rides HTTP chunks — more resilient than live on weak networks; "what just happened" checks are painless
- LAN discovery (mDNS) partially works inside the Tailscale virtual network; back home, you're on local direct connection again
- More depth in [Remote Access](remote-access.md)

---

## Scenario 6: Remote Cameras & Fresh Life for Old Xiaomi Cams

### Case A: the camera is on another network (parents' home / shop)

Have the far end **push** the stream in (the NVR never needs to reach the camera):

- **SRT push** (best over weak / cross-internet links): enable the SRT listener under **Settings → Streaming**, then push from the camera side with FFmpeg:

```bash
ffmpeg -i "rtsp://camera-address" -c copy -f mpegts \
  "srt://nvr-public-address:9000?streamid=#!:r=shop-camera,m=publish"
```

- **RTMP push**: `rtmp://nvr-public-address:1935/live/shop-camera`
- **WebRTC WHIP**: browser / phone / OBS publishes straight to `http://nvr-address:9090/whip/camera-id`

The NVR treats each pushed stream as a regular camera — recording, live view, AI, all as usual. See [SRT / RTMP push-in](srt-rtmp.md).

> Note: when opening inbound ports, enable stream-key auth and restrict sources with a firewall.

### Case B: old Xiaomi cameras at home

Xiaomi cameras are locked to their cloud — playback means a subscription. With MiBee NVR:

1. **Cameras → Add → Xiaomi**, sign in with your Xiaomi account
2. Scan and pick devices — they join automatically

![Xiaomi scan](images/xiaomi-scan.webp)

Recordings now land on your own disk — no subscription, no privacy concerns. CS2 models and 7 legacy TUTK models (Dafang, Xiaofang, Xiaobai, Aqara G2, …) are supported. See [Xiaomi camera setup](xiaomi-setup.md).

---

## Next Steps

- Full feature boundary: [Feature Overview](features.md)
- Comparing options: [How It Compares](comparison.md)
- Stuck? Start with [Troubleshooting](troubleshooting.md)
