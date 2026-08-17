import { state } from './i18n';

/**
 * Format a date string in a locale-aware manner.
 */
export function formatDate(dateStr: string): string {
  const date = new Date(dateStr);
  const lang = state.currentLang === 'zh' ? 'zh-CN' : 'en-US';
  return date.toLocaleString(lang, {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

/**
 * Format a duration in seconds to a human-readable string.
 * en: "1h 30m 15s" | zh: "1时 30分 15秒"
 */
export function formatDuration(seconds: number): string {
  const hrs = Math.floor(seconds / 3600);
  const mins = Math.floor((seconds % 3600) / 60);
  const secs = Math.floor(seconds % 60);
  const isZh = state.currentLang === 'zh';

  if (hrs > 0) {
    return isZh ? `${hrs}时 ${mins}分 ${secs}秒` : `${hrs}h ${mins}m ${secs}s`;
  }
  if (mins > 0) {
    return isZh ? `${mins}分 ${secs}秒` : `${mins}m ${secs}s`;
  }
  return isZh ? `${secs}秒` : `${secs}s`;
}

/**
 * Format a timestamp as a locale-aware relative time ("5 minutes ago" / "5分钟前").
 * Falls back to formatDate for anything older than a week.
 */
export function formatRelativeTime(dateStr: string, now: Date = new Date()): string {
  const date = new Date(dateStr);
  if (isNaN(date.getTime())) return dateStr;
  const lang = state.currentLang === 'zh' ? 'zh-CN' : 'en-US';
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' });
  const diffMs = date.getTime() - now.getTime();
  const absSec = Math.abs(diffMs) / 1000;
  if (absSec < 60) return rtf.format(Math.round(diffMs / 1000), 'second');
  if (absSec < 3600) return rtf.format(Math.round(diffMs / 60000), 'minute');
  if (absSec < 86400) return rtf.format(Math.round(diffMs / 3600000), 'hour');
  if (absSec < 7 * 86400) return rtf.format(Math.round(diffMs / 86400000), 'day');
  return formatDate(dateStr);
}

/**
 * Format bytes to a human-readable file size string.
 * e.g. "1.50 GB"
 */
export function formatFileSize(bytes: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let size = bytes;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex++;
  }
  return `${size.toFixed(2)} ${units[unitIndex]}`;
}

/**
 * Determine the best unit for a set of byte values (for chart axis).
 * Returns the divisor and unit label so all values use the same scale.
 */
export function getChartUnit(bytesArray: number[]): { divisor: number; unit: string } {
  const maxBytes = Math.max(...bytesArray, 0);
  const units = [
    { threshold: 1, divisor: 1, unit: 'B' },
    { threshold: 1024, divisor: 1024, unit: 'KB' },
    { threshold: 1024 * 1024, divisor: 1024 * 1024, unit: 'MB' },
    { threshold: 1024 * 1024 * 1024, divisor: 1024 * 1024 * 1024, unit: 'GB' },
  ];

  // Default to TB for very large values
  if (maxBytes >= 1024 * 1024 * 1024 * 1024) {
    return { divisor: 1024 * 1024 * 1024 * 1024, unit: 'TB' };
  }

  // Find the largest unit whose threshold is <= maxBytes
  for (let i = units.length - 1; i >= 0; i--) {
    if (maxBytes >= units[i].threshold) {
      return { divisor: units[i].divisor, unit: units[i].unit };
    }
  }
  return { divisor: 1, unit: 'B' };
}
