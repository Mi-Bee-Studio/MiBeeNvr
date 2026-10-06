import { cleanup } from '@testing-library/svelte';
import { describe, it, expect, afterEach } from 'vitest';
import { state, setLang, currentLocale } from '../lib/i18n/index.svelte';

afterEach(cleanup);

describe('currentLocale', () => {
  it('maps every UI language to its BCP-47 locale', () => {
    setLang('zh');
    expect(currentLocale()).toBe('zh-CN');
    setLang('en');
    expect(currentLocale()).toBe('en-US');
    setLang('ru');
    expect(currentLocale()).toBe('ru-RU');
  });

  it('falls back to en-US for unknown state', () => {
    state.currentLang = 'xx';
    expect(currentLocale()).toBe('en-US');
  });
});

describe('locale-aware formatting', () => {
  // parseServerDate treats zoneless strings as UTC; pin the input with Z so
  // expectations hold in any test-runner timezone.
  const iso = '2026-10-07T12:00:00Z';

  it('formatDate uses the ru locale when ru is active', async () => {
    setLang('ru');
    const { formatDate } = await import('../lib/format');
    const out = formatDate(iso);
    expect(out.toLowerCase()).toContain('окт');
    expect(out).toMatch(/2026/);
  });

  it('formatDate uses zh-CN for zh', async () => {
    setLang('zh');
    const { formatDate } = await import('../lib/format');
    expect(formatDate(iso)).toContain('10月');
  });

  it('formatRelativeTime renders Russian relative units under ru', async () => {
    setLang('ru');
    const { formatRelativeTime } = await import('../lib/format');
    const now = new Date('2026-10-07T12:10:00Z');
    const out = formatRelativeTime('2026-10-07T12:00:00Z', now);
    // ru-RU Intl.RelativeTimeFormat: "10 минут назад"
    expect(out.toLowerCase()).toContain('минут');
  });

  it('calendar month names localize via currentLocale', async () => {
    setLang('ru');
    const locale = currentLocale();
    const october = new Date(2000, 9, 1).toLocaleDateString(locale, { month: 'long' });
    expect(october.toLowerCase()).toBe('октябрь');
  });
});
