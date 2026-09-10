import { describe, it, expect } from 'vitest';
import { formatMergeWindowLabel, formatFileSize } from './format';

describe('formatMergeWindowLabel', () => {
  // window_start values come back from the API as UTC RFC3339 ("...Z").
  // Expected labels are computed from the same Date so the test is
  // timezone-independent (CI runs UTC, dev machines run CST).
  const ws = '2026-09-08T16:00:00Z';
  const d = new Date(ws);
  const pad = (n: number) => String(n).padStart(2, '0');
  const datePart = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;

  it('renders date-only for natural-day windows (midnight-aligned, time part is noise)', () => {
    const label = formatMergeWindowLabel(ws, 'natural-day');
    expect(label).toBe(datePart);
    expect(label).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it('renders date-only for the other day-aligned windows (24h / 7d / 30d)', () => {
    for (const dl of ['24h', '7d', '30d']) {
      expect(formatMergeWindowLabel(ws, dl)).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    }
  });

  it('includes HH:mm for sub-day windows (1h / 8h / 12h) that repeat within a day', () => {
    for (const dl of ['1h', '8h', '12h']) {
      const label = formatMergeWindowLabel(ws, dl);
      expect(label).toBe(`${datePart} ${pad(d.getHours())}:${pad(d.getMinutes())}`);
      expect(label).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/);
    }
  });

  it('falls back to the raw string when the timestamp is unparseable', () => {
    expect(formatMergeWindowLabel('not-a-date', 'natural-day')).toBe('not-a-date');
  });
});

describe('formatFileSize (existing helper, used by the merge history list)', () => {
  it('formats MB values', () => {
    expect(formatFileSize(1994946)).toBe('1.90 MB');
  });
});
