import { create } from "zustand";

export type Locale = "en" | "zh";

interface LocaleState {
  locale: Locale;
  setLocale: (locale: Locale) => void;
}

const localeKey = "godex:web:locale";
const storedLocale = typeof window === "undefined" ? "" : window.localStorage.getItem(localeKey) ?? "";

function normalizeLocale(value: string): Locale {
  return value === "zh" ? "zh" : "en";
}

function initialLocale(): Locale {
  if (storedLocale) {
    return normalizeLocale(storedLocale);
  }
  if (typeof window !== "undefined" && window.navigator.language.toLowerCase().startsWith("zh")) {
    return "zh";
  }
  return "en";
}

export const useLocaleStore = create<LocaleState>((set) => ({
  locale: initialLocale(),
  setLocale: (locale) => {
    window.localStorage.setItem(localeKey, locale);
    set({ locale });
  },
}));
