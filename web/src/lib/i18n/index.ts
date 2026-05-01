/**
 * Lightweight i18n module for MiBee NVR
 * No external dependencies - callback-based reactivity
 */

import zh from './zh.json';
import en from './en.json';

type Translations = Record<string, string>;

const locales: Record<string, Translations> = { zh, en };

let currentLang = 'en';
let listeners: (() => void)[] = [];

function detectLanguage(): string {
  const saved = localStorage.getItem('mibee_nvr_lang');
  if (saved && locales[saved]) return saved;

  const nav = navigator.language || '';
  if (/^zh\b/i.test(nav)) return 'zh';

  return 'en';
}

export function initI18n(): void {
  currentLang = detectLanguage();
}

export function getCurrentLang(): string {
  return currentLang;
}

export function setLang(lang: string): void {
  if (!locales[lang]) return;
  currentLang = lang;
  localStorage.setItem('mibee_nvr_lang', lang);
  listeners.forEach(l => l());
}

export function onLangChange(fn: () => void): () => void {
  listeners.push(fn);
  return () => {
    listeners = listeners.filter(l => l !== fn);
  };
}

export function t(key: string, params?: Record<string, string | number>): string {
  const dict = locales[currentLang] || locales['en'];
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
