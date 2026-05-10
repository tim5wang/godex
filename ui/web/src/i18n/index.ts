import { useCallback } from "react";
import { messages } from "./messages";
import { useLocaleStore, type Locale } from "../store/locale";

type MessageTree = Record<string, unknown>;

function resolveMessage(locale: Locale, key: string): string | null {
  const parts = key.split(".");
  let current: unknown = messages[locale] as MessageTree;
  for (const part of parts) {
    if (!current || typeof current !== "object" || !(part in current)) {
      return null;
    }
    current = (current as MessageTree)[part];
  }
  return typeof current === "string" ? current : null;
}

function interpolate(template: string, vars?: Record<string, string | number>) {
  if (!vars) {
    return template;
  }
  return template.replace(/\{(\w+)\}/g, (_, key: string) => String(vars[key] ?? ""));
}

export function useI18n() {
  const locale = useLocaleStore((state) => state.locale);
  const setLocale = useLocaleStore((state) => state.setLocale);
  const t = useCallback(
    (key: string, vars?: Record<string, string | number>) => {
      const fallback = resolveMessage("en", key) ?? key;
      const value = resolveMessage(locale, key) ?? fallback;
      return interpolate(value, vars);
    },
    [locale],
  );
  return { locale, setLocale, t };
}
