# Standalone Audio Sources

The NVR records audio as a first-class citizen, independent of any camera:
an audio source is enrolled like a camera (protocol RTSP or WHIP, encoding
**audio**) and records its own segments with its own retention — optionally
linked to a video camera for synchronized playback. For the audio that rides
inside a camera's own stream, see [Audio](audio.md) instead.

Recordable codecs: **G.711** (A-law/μ-law — the best-supported path: WAV
transcode, live listening, timeline sync), **AAC** and **Opus** (recorded and
played natively).

## Adding a source and probing it

Cameras → Add → protocol **RTSP** → encoding **audio**, paste the stream URL,
then press **Test connection**. The probe connects and DESCRIBEs the URL, so
it reports what the stream actually serves:

- `audio source ready: PCMA 8000Hz` — a recordable track was found;
- `codec G726 is not supported` — there is audio, but none of G.711/AAC/Opus;
  try another stream (a substream often differs) or transcode upstream;
- `no audio track` — the URL serves video only.

For video cameras the same probe now reports the stream's audio layout
(`audio: PCMA 8000Hz`) alongside the video encoding.

## Family 1: network pickups (IP microphones)

Dedicated IP microphones and camera-audio substreams all speak RTSP. Common
URL patterns (substream indices vary by device; `<user>`/`<pass>`/`<ip>` are
placeholders):

| Vendor | URL pattern |
|--------|-------------|
| Hikvision | `rtsp://<user>:<pass>@<ip>:554/Streaming/Channels/102` |
| Dahua | `rtsp://<user>:<pass>@<ip>:554/cam/realmonitor?channel=1&subtype=1` |
| Axis | `rtsp://<user>:<pass>@<ip>/axis-media/media.3gp?audio=1` |
| Generic | `rtsp://<user>:<pass>@<ip>/live` · `rtsp://<user>:<pass>@<ip>/stream` |

Paste the candidate URL into the form and probe it — the reported codec is
authoritative (device manuals routinely misstate it). Most pickups serve
G.711; devices that only serve other codecs will be called out explicitly.

## Family 2: an old phone as a room microphone

A retired phone is a serviceable Wi-Fi microphone via the browser's mic
capture pushed over [WHIP](whip-ingest.md) — no app needed.

1. Create the source: Cameras → Add → protocol **WHIP (WebRTC push)** →
   encoding **audio**, stream key e.g. `room-mic`. The form shows the
   endpoint URL, e.g. `http://<nvr-ip>:9090/whip/room-mic`.
2. Save this single-file page to the phone and open it in the browser
   (a local file counts as a secure context for mic capture in
   Chrome/Edge/Firefox; if your browser refuses, serve the page over HTTPS):

```html
<!doctype html>
<meta name="viewport" content="width=device-width">
<title>mic</title>
<button id="b">&#127908; Start</button> <span id="s"></span>
<script>
const ENDPOINT = 'http://<nvr-ip>:9090/whip/room-mic';
const pc = new RTCPeerConnection();
document.getElementById('b').onclick = async () => {
  const s = document.getElementById('s');
  try {
    const mic = await navigator.mediaDevices.getUserMedia({ audio: true });
    pc.addTransceiver(mic.getAudioTracks()[0], { direction: 'sendonly' });
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    await new Promise(r => pc.onicegatheringstatechange = () =>
      pc.iceGatheringState === 'complete' && r());
    const res = await fetch(ENDPOINT, {
      method: 'POST',
      headers: { 'Content-Type': 'application/sdp' },
      body: pc.localDescription.sdp,
    });
    if (!res.ok) throw new Error('WHIP ' + res.status);
    await pc.setRemoteDescription({ type: 'answer', sdp: await res.text() });
    s.textContent = 'streaming';
  } catch (e) { s.textContent = String(e); }
};
</script>
```

3. Press Start — the phone's Opus audio records like any other source
   (keep the browser in foreground; battery saver suspends mic capture).

Shipping this page inside the web UI is a pending product decision; for now
it lives here as a copy-paste recipe.

## Family 3: ESP32-S3 + I2S microphone nodes

Self-built nodes are the budget path for fixed placements. The well-trodden
wiring:

| Part | Notes |
|------|-------|
| ESP32-S3 dev board | any variant; Wi-Fi on 2.4 GHz |
| INMP441 I2S MEMS mic | WS→GPIO 4, SCK→GPIO 5, SD→GPIO 6, L/R→GND (mono left) |
| Power | USB 5 V; ~100 mA sustained |

Firmware: community builds around esp-adf / ESP-IDF I2S drivers that capture
PCM and push it out — pick a project that serves **G.711 8 kHz mono over
RTSP** (the most compatible target for this NVR: WAV transcode, live
listening and timeline sync all work) or Opus over WHIP. Keep the capture
buffer small (20–40 ms) so reconnects cost little audio.

## Linking a source to a camera

Edit the video camera and set its linked audio source (the form offers the
candidates). The payoff:

- the recording detail page grows a **companion panel** — the source's
  segments around the video, synced playback, and a ±5 s manual offset
  slider (persisted per camera pair) for devices without a shared clock;
- the recordings day timeline draws the source's segments as an emerald
  audio track on the linked camera's row; click a strip to open that audio
  recording.

Unlinked sources work too — they simply stand alone in the library.

## Next steps

- [Audio](audio.md) — in-camera audio recording, live listening, two-way talk
- [WHIP ingest](whip-ingest.md) — the push endpoint phones and OBS publish to
- [Recordings & playback](recording-playback.md) — the day timeline and library
