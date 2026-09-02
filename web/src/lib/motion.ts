// Motion-score helpers shared by every heat surface (DayTimeline, TimelineBar,
// recording lists).

export interface MotionLike {
  motion_score?: number;
  motion_confidence?: number;
}

/**
 * The activity signal consumers should rank and display (#634).
 *
 * The raw compressed-domain score is purely RELATIVE (P-frame size vs the
 * segment's own baseline), so a bitrate-starved segment — e.g. a night-mode
 * camera crushing its output to ~300 bytes/frame — saturates the score on
 * rate-control jitter (field data 2026-08-31: empty stairwell scored 0.93).
 * The backend's absolute-size confidence discounts that: heat, badges and
 * ordering all use score × confidence.
 *
 * - null  → unanalyzed (motion_score < 0): render as "未分析", rank neutrally
 * - 0..1  → effective activity
 * - confidence < 0 (pre-#634 rows) → treated as full confidence (1)
 */
export function effectiveMotion(rec: MotionLike): number | null {
  const score = rec.motion_score ?? -1;
  if (score < 0) return null;
  const conf = rec.motion_confidence ?? -1;
  return score * (conf < 0 ? 1 : conf);
}
