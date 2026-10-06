/**
 * Locale parity guard for the i18n dictionaries.
 *
 * Every dictionary in `src/lib/i18n` must stay in lockstep with `en.json`:
 * the same flattened key set (nested objects such as `pagination` and
 * `backToTop` included) and the same placeholders (`{count}`, `{{days}}`).
 *
 * The locale list is discovered with import.meta.glob, so adding a new
 * language (or a new key to only one dictionary) fails here loudly instead of
 * silently falling back to English at runtime — which is exactly how the
 * Russian dictionary was checked by hand before review.
 */
import { describe, it, expect } from 'vitest';

const modules = import.meta.glob('../lib/i18n/*.json', { eager: true }) as Record<
  string,
  { default: Record<string, unknown> }
>;

function flatten(obj: Record<string, unknown>, prefix = ''): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      Object.assign(out, flatten(value as Record<string, unknown>, path));
    } else {
      out[path] = String(value);
    }
  }
  return out;
}

function placeholders(value: string): string[] {
  return (value.match(/\{\{?\w+\}?\}/g) ?? []).sort();
}

const dictionaries: Record<string, Record<string, string>> = {};
for (const [path, mod] of Object.entries(modules)) {
  const lang = path.split('/').pop()!.replace(/\.json$/, '');
  dictionaries[lang] = flatten(mod.default);
}

const en = dictionaries.en;
const others = Object.keys(dictionaries)
  .filter((lang) => lang !== 'en')
  .sort();

describe('i18n dictionary parity', () => {
  it('en.json is present and looks like the full dictionary', () => {
    expect(en).toBeDefined();
    expect(Object.keys(en).length).toBeGreaterThan(1000);
  });

  it('ships at least one translated locale besides en', () => {
    expect(others.length).toBeGreaterThan(0);
  });

  for (const lang of others) {
    it(`${lang}.json has exactly the same keys as en.json`, () => {
      const missing = Object.keys(en).filter((key) => !(key in dictionaries[lang]));
      const extra = Object.keys(dictionaries[lang]).filter((key) => !(key in en));
      expect({ missing, extra }).toEqual({ missing: [], extra: [] });
    });

    it(`${lang}.json keeps every placeholder of en.json`, () => {
      const mismatched = Object.keys(en).filter((key) => {
        if (!(key in dictionaries[lang])) return false; // reported by the key test
        return JSON.stringify(placeholders(en[key])) !== JSON.stringify(placeholders(dictionaries[lang][key]));
      });
      expect(mismatched).toEqual([]);
    });
  }
});
