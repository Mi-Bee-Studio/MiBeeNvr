/**
 * Lightweight i18n module for MiBee NVR
 * Svelte 5 reactive — state.currentLang is $state, t() reads it directly
 */

import zh from './zh.json';
import en from './en.json';
import ru from './ru.json';

type Translations = Record<string, string>;

const locales: Record<string, Translations> = { zh, en, ru };

// $state object — components import and read state.currentLang for reactive tracking
// t() also reads state.currentLang, so any template calling t() re-evaluates on lang change
export const state = $state({ currentLang: 'en' });

function detectLanguage(): string {
  const saved = localStorage.getItem('mibee_nvr_lang');
  if (saved && locales[saved]) return saved;

  const nav = navigator.language || '';
  if (/^zh\b/i.test(nav)) return 'zh';
  if (/^ru\b/i.test(nav)) return 'ru';

  return 'en';
}

export function initI18n(): void {
  state.currentLang = detectLanguage();
  document.documentElement.lang = state.currentLang;
}

export function setLang(lang: string): void {
  if (!locales[lang]) return;
  state.currentLang = lang;
  localStorage.setItem('mibee_nvr_lang', lang);
  document.documentElement.lang = lang;
}

// BCP-47 locale for Intl date formatting — one source of truth for every
// toLocaleDateString/toLocaleString call site (zh/en-only mappings strand ru
// and any future locale on en-US).
export function currentLocale(): string {
  switch (state.currentLang) {
    case 'zh':
      return 'zh-CN';
    case 'ru':
      return 'ru-RU';
    default:
      return 'en-US';
  }
}

export function t(key: string, params?: Record<string, string | number>): string {
  // Read state.currentLang ($state) and USE the value — compiler cannot optimize away
  const lang = state.currentLang;
  const dict = locales[lang] || locales['en'];
  let value = dict[key];

  if (value === undefined) {
    // Fallback to English
    value = locales['en'][key];
  }

  if (value === undefined) {
    return key;
  }

  if (params) {
    for (const [k, v] of Object.entries(params)) {
      value = value.replace(`{${k}}`, String(v));
    }
  }

  return value;
}
