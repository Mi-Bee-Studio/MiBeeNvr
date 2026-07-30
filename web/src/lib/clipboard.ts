/**
 * Clipboard copy with a non-secure-context fallback.
 *
 * `navigator.clipboard.writeText()` is a Secure Context API — it only works on
 * `https://` origins or `http://localhost`. The NVR's most common deployment is
 * plain `http://<server-ip>:9090` (Docker, LAN, bare metal without a TLS
 * terminator), where `navigator.clipboard` is `undefined` and any call throws.
 * Without this fallback, the "copy push-out URL" and "copy API key" buttons
 * silently fail on every non-localhost HTTP install (issue #197).
 *
 * Strategy:
 *   1. On a secure context, prefer the async Clipboard API (returns a promise,
 *      works without focusing a text node).
 *   2. Otherwise (or if the API rejects, e.g. permission denied), fall back to
 *      the legacy `document.execCommand('copy')` on a temporary hidden
 *      `<textarea>`. `execCommand` is deprecated but remains the only reliable
 *      synchronous copy path on plain HTTP origins and is still supported by
 *      every major browser.
 *
 * Returns whether the copy succeeded so callers can pick the right toast.
 */

export async function copyText(text: string): Promise<boolean> {
  // 1. Secure context → async Clipboard API.
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Permission denied or the call rejected — fall through to the legacy path
      // rather than giving up (some browsers deny clipboard for unfocused docs).
    }
  }

  // 2. Legacy fallback: hidden textarea + execCommand('copy').
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    // Move it off-screen and prevent scroll/focus jump.
    ta.style.position = 'fixed';
    ta.style.top = '0';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}
