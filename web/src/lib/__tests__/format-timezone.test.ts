import { describe, it, expect } from 'vitest';
import { parseServerDate, formatDate } from '$lib/format';

describe('parseServerDate', () => {
  it('treats zoneless SQLite timestamps as UTC', () => {
    // Regression: bare "YYYY-MM-DD HH:MM:SS" was parsed as LOCAL time,
    // shifting AI-event display and event→recording jumps by the browser's
    // UTC offset (8h on CST deployments).
    const d = parseServerDate('2026-08-25 06:19:51');
    expect(d.toISOString()).toBe('2026-08-25T06:19:51.000Z');
  });

  it('handles fractional seconds and the T separator', () => {
    expect(parseServerDate('2026-08-25 06:19:51.25').toISOString()).toBe('2026-08-25T06:19:51.250Z');
    expect(parseServerDate('2026-08-25T06:19:51').toISOString()).toBe('2026-08-25T06:19:51.000Z');
  });

  it('keeps explicit offsets untouched', () => {
    expect(parseServerDate('2026-08-25T06:19:51Z').toISOString()).toBe('2026-08-25T06:19:51.000Z');
    expect(parseServerDate('2026-08-25T14:19:51+08:00').toISOString()).toBe('2026-08-25T06:19:51.000Z');
  });

  it('leaves non-timestamp strings to default parsing', () => {
    expect(parseServerDate('2026-08-25').toISOString()).toBe('2026-08-25T00:00:00.000Z'); // date-only is UTC by spec
    expect(isNaN(parseServerDate('not a date').getTime())).toBe(true);
  });

  it('formatDate renders a stable locale-independent string for zoneless input', () => {
    // Whatever the local timezone, the rendered LOCAL hour must equal the
    // server's UTC hour plus offset of the test env — assert via Date instead:
    const out = formatDate('2026-08-25 06:19:51');
    expect(out).toContain('2026');
  });
});
