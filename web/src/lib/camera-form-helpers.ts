/**
 * Pure helpers for the camera form.
 *
 * Extracted from CameraForm.svelte: input normalisation and display formatting
 * with no Svelte state — unit-testable and shared with future forms.
 */

/**
 * Parse the subnet-hints textarea into a clean CIDR list.
 *
 * One per line; whitespace and commas are both accepted as separators; blanks
 * are dropped. Returns `undefined` (not `[]`) when nothing usable is left, so
 * the caller can omit the field from the request instead of clearing it.
 * The backend validates each entry as a CIDR and rejects anything wider than
 * /24 (the rediscovery scanner caps its sweep there).
 */
export function parseSubnetHints(text: string): string[] | undefined {
  const parts = text
    .split(/[\n,]+/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
  return parts.length > 0 ? parts : undefined;
}

/** One-line label for a media profile: name (or token), resolution, codec. */
export function profileOptionLabel(p: { name?: string; token: string; width?: number; height?: number; encoding?: string }): string {
  const dims = p.width && p.height ? `${p.width}×${p.height}` : '';
  return [p.name || p.token, dims, p.encoding || ''].filter(Boolean).join(' · ');
}

/** Human-readable clock skew: seconds, minutes (1 decimal) or hours (1 decimal). */
export function fmtSkew(seconds: number): string {
  const abs = Math.abs(seconds);
  if (abs >= 3600) return (seconds / 3600).toFixed(1) + ' h';
  if (abs >= 60) return (seconds / 60).toFixed(1) + ' min';
  return seconds.toFixed(1) + ' s';
}
