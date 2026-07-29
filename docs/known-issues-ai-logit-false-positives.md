# Known Issue: AI Detection "person:100% everywhere" — logit explosion bypassing the confidence threshold

> **Status**: ✅ **Fixed** (PR #193). This document is retained as a postmortem
> / regression reference.
> **Affected**: Browser-side AI detection (`parseYoloOutput`) on every camera,
> every model (yolo11n / yolo11s), every confidence threshold.
> **Impact (pre-fix)**: The live grid showed dozens-to-hundreds of phantom
> `person` boxes at "100%" confidence covering the entire frame — backgrounds,
> walls, foliage, sky. Every tuning attempt (class filter, confidence 0.5→0.98,
> model swap nano→s, EMA/maxAge) appeared to do nothing. Real humans were
> occasionally detected but misclassified as a non-`person` class (yellow box),
> while backgrounds were confidently labeled `person` (green box).
> **Symptom appeared identical to**: an under-trained model, a wrong model for
> the scene, a class-filter bug, an EMA smoothing problem — all of which were
> investigated, "fixed", and deployed before the real cause was found.

## Problem

After enabling AI detection on the surveillance grid, every camera tile was
covered in green `person` boxes at 100% confidence, regardless of scene
content. A debug overlay (`data-ai-debug` on the `AiOverlay` canvas) revealed
the actual worker output:

```
camera 0 (78 dets): person:100% | person:100% | person:100% | ...  (×78)
camera 2 (260 dets): person:100% | person:100% | person:100% | ... (×260)
```

Every single detection was `classId=0` (`person`) at `sigmoid→100%`. No other
class ever appeared in the background noise. Meanwhile a real human walking
through the scene was occasionally boxed in **yellow** (an "other" COCO class,
`classId > 25`), not green — i.e. the model's strongest *correct* detections
weren't even labeled `person`.

## Why this was extraordinarily hard to diagnose

The symptom ("AI boxes everything as person") points squarely at "the model is
bad for this scene". But every model/parameter lever was pulled to no effect:

| Lever tried | Values attempted | Result |
|-------------|------------------|--------|
| Class filter (`enabled_classes`) | all 80 → `["person"]` → `["airplane"]` | "Still many green boxes"; airplane filter proved the filter *works (boxes dropped), yet person-class noise persisted |
| Confidence threshold | 0.5 → 0.7 → 0.9 → 0.95 → **0.98** | "Still about the same" — even 0.98 didn't reduce the green boxes |
| Model | yolo11n → yolo11s (mAP 0.703 → 0.746) | "Still many" — a higher-precision model made no difference |
| EMA / maxAge | 15 → 8 → 3 → 1 | Reduced *dwell time* of stale boxes, but didn't reduce the *source* count |

The dead giveaway that this was NOT a model-quality issue: a **0.98 confidence
threshold had no visible effect**. In a functioning pipeline, 0.98 should strip
almost everything. The fact that it didn't meant the threshold comparison was
being defeated before it ran.

## Root cause: two stacked defects in `parseYoloOutput`

### Defect 1 — confidence threshold applied after sigmoid, with no discrimination

YOLOv11's default ONNX export emits **raw class logits** (unbounded reals, can
be large positives). The original code:

```js
// inference.ts (BROKEN)
const score = sigmoid(rawScore);   // normalize logit → probability
if (score > maxScore) { ... }
if (maxScore < confidenceThreshold) continue;
```

`sigmoid` maps any positive logit to `> 0.5`. So a background patch with a
weakly-positive `person` logit (say `0.3`) became `sigmoid(0.3) = 0.57` and
sailed past a `0.5` threshold. The threshold only started discriminating above
~`0.9` — and even then, defect 2 defeated it entirely.

### Defect 2 — no upper bound on logits (the real killer)

A garbled / partially-corrupted decoded frame (common with H.265 streams —
occasional decode errors feed the model noise instead of pixels) makes the
model emit **absurdly large logits**. Observed values on the failing camera:

```
person raw logits: 62932, 15351, 11356, 2620, 15324, ...
```

`sigmoid(62932)` is `1.0` to floating-point precision. `sigmoid(2620)` is `1.0`.
So **every** exploding-logit box reads as `100%` confidence, and **no threshold
short of >1.0 can reject them**. A single corrupted frame painted 260 phantom
`person:100%` boxes on one camera. This is why 0.98 "did nothing".

## Fix (PR #193, `inference.ts:parseYoloOutput`)

```js
// Find the best (highest-logit) class.
let maxRaw = -Infinity;
let maxClass = 0;
for (let j = 0; j < numClasses; j++) {
  const raw = data[offset + 4 + j];
  if (raw > maxRaw) { maxRaw = raw; maxClass = j; }
}

// Sanity cap: a well-formed YOLO detection has a class logit in roughly
// -10..+15. Values in the thousands (corrupted/garbled decoded frames) produce
// exploding logits whose sigmoid is indistinguishable from 1.0, swamping any
// threshold and painting dozens of phantom "person:100%" boxes. Reject them as
// decode artifacts.
const MAX_VALID_LOGIT = 15;
if (maxRaw > MAX_VALID_LOGIT) continue;

// Sigmoid to normalize, THEN threshold (a 0.95 threshold ≈ logit 2.94).
const maxScore = sigmoid(maxRaw);
if (maxScore < confidenceThreshold) continue;
```

Two changes:
1. **Logit upper bound (`MAX_VALID_LOGIT = 15`)** — drops exploding-logit boxes
   from corrupted frames. A genuine strong detection sits at logit ~5–12
   (`sigmoid` 0.99–0.99999), well under the cap; only decode-artifact boxes
   (logit in the thousands) are rejected.
2. **Keep sigmoid-then-threshold** (semantically clear: threshold is a
   probability), now that the cap stops the sigmoid-saturation exploit.

## Verification (M5, yolo11n, post-fix)

With the debug overlay reading the worker's actual output:

| Camera | Before fix | After fix |
|--------|-----------|-----------|
| cam 0 (7-8楼转角) | 78 × `person:100%` | `no detections` (empty scene) |
| cam 2 (视通) | 260 × `person:100%` | `no detections` (empty scene) |

An empty scene now correctly produces **zero boxes**. The confidence threshold
(0.5) finally discriminates: background noise (logit <0 → sigmoid <0.5) is
rejected; a real detection (logit 5–12 → sigmoid 0.99+) passes.

## How the diagnosis was finally cracked

The breakthrough was a **temporary debug overlay** that rendered the worker's
returned `Detection[]` (label + confidence) into the DOM (`data-ai-debug`),
making it readable via DOM snapshot instead of an opaque canvas. The moment the
actual numbers were visible — `person:100%` repeated 78–260 times, with the
"remove sigmoid" experiment revealing raw logits of **62932** — the exploding-
logit root cause was obvious. Every prior tuning attempt had been blind because
the canvas only showed *boxes*, not the underlying confidence values or logits.

**Lesson**: when a threshold "does nothing", suspect that the values being
thresholded are saturated/corrupted upstream — measure the raw numbers before
the comparison, don't keep raising the threshold.

## Related

- PR #193 — the fix (`MAX_VALID_LOGIT` cap + sigmoid clarification)
- `inference.test.ts` — regression test: exploding logit (`62932`) rejected,
  normal strong detection (`logit 6` → `sigmoid 0.998`) retained
- Sibling postmortem: `known-issues-ai-onnx-gzip-trailer.md` (#109) — another
  case where the visible symptom ("model won't load") misdirected diagnosis
  away from the real (middleware) cause
- The debug-overlay technique (`AiOverlay` → `data-ai-debug` DOM node) is a
  reusable pattern for inspecting worker-returned detection data when canvas
  pixels are unreadable; it was removed before merge but is documented here
  for future tuning sessions
