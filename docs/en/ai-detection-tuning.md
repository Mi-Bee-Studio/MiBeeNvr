# AI Detection Tuning Guide

> How to balance detection sensitivity against false positives and performance. Applies to the browser-side YOLOv11 object detection (ONNX Runtime Web; inference runs in a Web Worker, off the main thread).

## Tunable parameters

All parameters are configurable in **Settings → AI Detection**. Saving persists to the backend YAML (the single source of truth); the frontend reads from `/api/ai/status` on startup and uses localStorage only as an offline cache.

| Parameter | Where | Range | Default | Effect |
|-----------|-------|-------|---------|--------|
| **Enable AI** | Settings → AI Detection (master toggle) | on/off | off | Global enable/disable |
| **Confidence threshold** `confidence_threshold` | Settings → AI Detection | 0.1–0.99 (step 0.01) | 0.5 | Detections below this confidence are discarded. **Higher = fewer, more accurate boxes** |
| **Frame skip** `frame_skip_rate` | Settings → AI Detection | 1–10 | 10 | Run inference every N frames. **Higher = less CPU but slower updates**. Use ≥ 8 on edge devices |
| **Detection classes** `enabled_classes` | Settings → AI Detection | presets + checkboxes | all 80 COCO classes | Detect only the selected classes (e.g. people/vehicles). **Empty = all** |
| **Smoothing factor** `ema_alpha` | Settings → AI Detection → Advanced | 0.1–0.9 | 0.3 | Box position smoothing. Lower = smoother (box lags), higher = snappier (box jitters) |
| **Box dwell time** `max_age` | Settings → AI Detection → Advanced | 3–30 | 15 | How many detection cycles a disappeared box lingers. Slider shows the equivalent seconds |
| **Model** `model_url` | Settings → AI Detection (dropdown) | `/models/*.onnx` | yolo11n.onnx | Precision/speed trade-off. yolo11n is fastest; yolo11s is more precise but slower |
| **ROI zones** | Settings → AI Detection → Per-camera | polygon | full frame | Detect only inside the drawn region. **The most effective noise reducer** |

Additionally, **per-camera overrides**: each camera can have its own confidence + frame-skip values (expand the per-camera section), which take priority over the global defaults.

![AI Detection settings panel](images/settings-ai.webp)

### Adaptive throttle (automatic, no config needed)

Inference runs in a Web Worker. When the running average inference time exceeds 80ms (typical for RPi-class WASM SIMD), the system automatically raises frameSkip (monotonic — never lowers on its own, capped at 10) to keep the page responsive. The log (dev builds only) shows `throttling frameSkip X→Y`.

---

## Why does it "box everything"?

Three possible causes, in diagnosis order:

### 1. Logit explosion on garbled frames (fixed, PR #193)

If **no parameter change has any effect** (confidence 0.98, class filter, model swap all useless) and every box reads `person:100%`, this was an H.265 decode-error frame feeding the model garbage, causing exploding logits (thousands) whose sigmoid is 1.0 — defeating any threshold < 1.0. **This is fixed in code**: logits > 15 are dropped as decode artifacts. If you still see wall-to-wall 100% false positives, confirm you're running a build after #193.

See [known-issues-ai-logit-false-positives.md](../known-issues-ai-logit-false-positives.md).

### 2. Confidence threshold too low (most common)

The default 0.5 shows any detection with confidence ≥ 0.5. YOLOv11-nano is a lightweight model prone to 0.5–0.6 low-quality detections on textures/shadows/glare. **Raising to 0.65–0.7 filters most of these**.

### 3. EMA smoothing makes false boxes linger

Even after a false detection vanishes next frame, `max_age` keeps the box visible for several seconds (depending on detection rate):

| Frame skip | Detection rate (30fps source) | Max false-box dwell (max_age=15) |
|------------|-------------------------------|----------------------------------|
| 3 | 10 Hz | ~1.5 s |
| 5 | 6 Hz | ~2.5 s |
| 10 | 3 Hz | ~5 s |

If false-box lingering is severe, lower `max_age` to 3–5 in the Advanced section.

---

## Recommended values by scenario

### Scenario A: Edge devices (RPi 3B/4, Banana Pi M5) — smoothness first

```yaml
confidence_threshold: 0.65
frame_skip_rate: 10
model: yolo11n.onnx
```

- **Why**: Edge CPUs are tight; frameSkip=10 keeps inference at 3Hz so the page stays responsive. 0.65 + class filter keeps false positives manageable.
- **Trade-off**: Slow detection updates (3/s); fast-moving objects' boxes lag.

### Scenario B: Desktop/laptop browser (x86, WebGPU) — sensitivity first

```yaml
confidence_threshold: 0.5
frame_skip_rate: 3
model: yolo11s.onnx (optional, more precise)
```

- **Why**: Desktop CPU/GPU is plentiful; frameSkip=3 gives 10Hz detection with snappy boxes. 0.5 catches more targets.
- **Trade-off**: More false positives. If the scene is complex, raise to 0.6 or use class filtering.

### Scenario C: Security monitoring (missing a detection is costly) — high recall

```yaml
confidence_threshold: 0.45–0.5
frame_skip_rate: 5–8
detection classes: person + common security targets
ROI: only entry/aisle areas
```

- **Why**: In security, a missed detection is worse than a false alarm, so use a low threshold for recall. But **you MUST use class filtering + ROI** to limit the detection area, or a low threshold floods the frame.

### Scenario D: Clean demo (false positives are costly) — high precision

```yaml
confidence_threshold: 0.75–0.8
frame_skip_rate: 5
detection classes: person only
```

- **Why**: For demos/screenshots, accuracy beats coverage. 0.75+ with a class filter leaves only high-confidence targets.

---

## Tuning steps (recommended order)

1. **Pick classes** (zero-cost noise reduction): Settings → AI Detection, use a preset ("People only" / "Security common") to immediately drop irrelevant-class boxes.

2. **Draw ROI zones** (highest-value noise reduction): Settings → AI Detection → Per-camera, draw detection regions that exclude sky, ground, reflective walls, swaying foliage.

3. **Raise confidence threshold**: From 0.5 to 0.65, observe for a few minutes. Still noisy? Raise to 0.7.

4. **Adjust frame skip**:
   - Edge device laggy → raise to 8–10.
   - Desktop smooth but boxes lag → lower to 3–5.

5. **Save**: Click "Save" at the bottom of the page; config is written to the backend + localStorage.

---

## About real humans showing non-green (non-person) boxes

In some scenes (top-down, distant, small targets), YOLO may classify a real human as a non-person class (yellow box instead of green), but the **position is usually accurate**. If your need is "accurately mark where people are", a yellow box is still valid.

- To show only a certain color: adjust the class filter.
- To change box colors: edit `getClassColor` in `web/src/components/AiOverlay.svelte`.

---

## Verify the active config

```bash
# Backend config (single source of truth)
curl -u admin:PASSWORD http://localhost:9090/api/ai/status

# List available models
curl -u admin:PASSWORD http://localhost:9090/api/ai/models
```

---

## Technical notes

- **Inference thread**: ONNX inference runs in a shared Web Worker (`inference-worker.ts`), off the main render thread. All cameras share one ORT session.
- **Visibility gating**: AI inference auto-pauses when the tab is backgrounded (saves CPU) without dropping the video stream.
- **Logit cap**: Exploding logits (>15) from corrupted decode frames are dropped, preventing wall-to-wall 100% false positives.
- **Adaptive throttle**: Raises frameSkip automatically when inference is slow (monotonic — never lowers on its own).
- **Config sync**: Backend YAML is the single source of truth; the frontend reads from the API on startup and caches to localStorage.

## Related

- [Known issue: logit explosion causing wall-to-wall person:100% false positives](../known-issues-ai-logit-false-positives.md)
- [Known issue: ONNX model loading gzip-trailer](../known-issues-ai-onnx-gzip-trailer.md)
- Architecture: AI inference is entirely browser-side (`web/src/lib/ai-detection/`); the backend only stores config and model files
