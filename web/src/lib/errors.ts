/**
 * Friendly error rendering.
 *
 * Action handlers throughout the app used to do `showToast(e.message || 'Failed to ...')`,
 * which leaks raw backend text (SOAP faults, "HTTP 500", Go error strings, codec names)
 * directly to the user. The `ApiRequestError` type already carries a machine-readable
 * `code` (set by the API client from the backend's JSON `{error, code}` envelope), and
 * there's an `errors.*` i18n namespace intended to map those codes to translated phrases.
 *
 * This helper centralizes the lookup so every call site gets consistent behavior:
 *   1. If the error is an ApiRequestError with a code, try `errors.<code>` (translated).
 *   2. Otherwise fall back to the error's own message (better than nothing — it may be a
 *      network error like "Request timed out" which the client already localized).
 *   3. As a last resort, use the provided fallback (an i18n key resolved via t(), or a
 *      plain string).
 *
 * The fallback may be EITHER an i18n key (preferred — pass it through t()) or a literal
 * string. We detect keys by the presence of a dot and no spaces; anything else is treated
 * as a literal so existing call sites keep working during migration.
 */

import { t } from '$lib/i18n';
import { ApiRequestError } from '$lib/api/client';

/**
 * Resolve an error into a user-presentable message.
 *
 * @param e        The caught error (usually from apiRequest).
 * @param fallback Either an i18n key (e.g. 'cameras.failedStart') or a literal fallback
 *                 string. Resolved via t() when it looks like a key.
 */
export function friendlyError(e: unknown, fallback: string): string {
  if (e instanceof ApiRequestError) {
    // 1. Map the machine code to a translated phrase, if one exists.
    if (e.code) {
      const keyed = t(`errors.${e.code}`);
      // t() returns the key itself when no translation exists — detect that.
      if (keyed !== `errors.${e.code}`) return keyed;
    }
    // 2. Fall back to the backend's human message.
    if (e.message) return e.message;
  }
  if (e instanceof Error && e.message) return e.message;
  // 3. Last resort: the caller's fallback (i18n key or literal).
  return resolveFallback(fallback);
}

/** Resolve a fallback that may be an i18n key or a literal string. */
function resolveFallback(fallback: string): string {
  // A key looks like "namespace.phrase" with no spaces. Literals (e.g. "Failed to save")
  // contain spaces or punctuation that isn't a single dot between identifiers.
  if (looksLikeI18nKey(fallback)) return t(fallback);
  return fallback;
}

function looksLikeI18nKey(s: string): boolean {
  // Must contain a dot, have no whitespace, and be alphanumeric+dots+underscores only.
  return s.includes('.') && !/\s/.test(s) && /^[a-zA-Z0-9_.]+$/.test(s);
}
