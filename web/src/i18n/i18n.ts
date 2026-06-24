// i18n bootstrap. Priority for first-load language:
//   1. localStorage  — user has clicked the language switcher before
//   2. installerHint — backend injected window.__EDGENEST_DEFAULT_LANG into
//                      the served index.html, seeded by install.sh
//   3. navigator     — fall through to browser language
//   4. fallbackLng=en
//
// Adding a language: drop a locale file under ./locales mirroring zh.ts, then
// register it in the imports + resources + SUPPORTED_LANGS + LANG_NAMES below.
// locales/_typecheck.ts forces every file to mirror zh's structure at build time.

import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import en from "./locales/en";
import zh from "./locales/zh";
import zhTW from "./locales/zh-TW";
import fa from "./locales/fa";
import ru from "./locales/ru";
import vi from "./locales/vi";

export const LANG_STORAGE_KEY = "edgenest_lang";

export type Lang = "en" | "zh" | "zh-TW" | "fa" | "ru" | "vi";

export const SUPPORTED_LANGS: Lang[] = ["en", "zh", "zh-TW", "fa", "ru", "vi"];

// Right-to-left scripts. The switcher + bootstrap flip <html dir> for these.
export const RTL_LANGS: Lang[] = ["fa"];

// Native endonyms for the language switcher — intentionally NOT translated
// (a language is named in its own script everywhere).
export const LANG_NAMES: Record<Lang, string> = {
  en: "English",
  zh: "中文",
  "zh-TW": "繁體中文",
  fa: "فارسی",
  ru: "Русский",
  vi: "Tiếng Việt",
};

const detector = new LanguageDetector();
detector.addDetector({
  name: "installerHint",
  lookup() {
    const v = (window as unknown as { __EDGENEST_DEFAULT_LANG?: string })
      .__EDGENEST_DEFAULT_LANG;
    return v && (SUPPORTED_LANGS as string[]).includes(v) ? v : undefined;
  },
  cacheUserLanguage() {
    /* server-seeded hint; never write back. */
  },
});

i18n
  .use(detector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { translation: en },
      zh: { translation: zh },
      "zh-TW": { translation: zhTW },
      fa: { translation: fa },
      ru: { translation: ru },
      vi: { translation: vi },
    },
    fallbackLng: "en",
    supportedLngs: SUPPORTED_LANGS,
    // "zh-CN" → "zh", "en-US" → "en"; explicit "zh-TW" still wins over "zh".
    nonExplicitSupportedLngs: true,
    interpolation: { escapeValue: false },
    detection: {
      order: ["localStorage", "installerHint", "navigator"],
      lookupLocalStorage: LANG_STORAGE_KEY,
      caches: ["localStorage"],
    },
    returnNull: false,
  });

// Normalize i18next's resolved language (which may carry a region, e.g.
// "zh-CN" from the browser) down to one of our supported codes.
export function currentLang(): Lang {
  const l = i18n.language || "en";
  if ((SUPPORTED_LANGS as string[]).includes(l)) return l as Lang;
  if (/^zh/i.test(l)) return /(?:tw|hk|mo|hant)/i.test(l) ? "zh-TW" : "zh";
  const base = l.split("-")[0].toLowerCase();
  return (SUPPORTED_LANGS as string[]).includes(base) ? (base as Lang) : "en";
}

export function isRTL(lang: Lang = currentLang()): boolean {
  return RTL_LANGS.includes(lang);
}

// Flip <html dir>/<html lang> so RTL scripts (Persian) lay out correctly.
function applyDirection() {
  if (typeof document === "undefined") return;
  const lang = currentLang();
  document.documentElement.dir = isRTL(lang) ? "rtl" : "ltr";
  document.documentElement.lang = lang;
}

i18n.on("languageChanged", applyDirection);
applyDirection();

export function setLang(lang: Lang) {
  i18n.changeLanguage(lang);
  try {
    localStorage.setItem(LANG_STORAGE_KEY, lang);
  } catch {
    // localStorage unavailable (private mode) — i18next still updates in-memory.
  }
}

export default i18n;
