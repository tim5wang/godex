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

/** translate resolves a key for an EXPLICIT locale, falling back to English.
 *
 *  It exists for code that runs outside React: a plain error helper has no hook to call,
 *  so without this it would either hard-code one language or import the message tree and
 *  duplicate the fallback rule. Errors are exactly the text that must not come out in the
 *  wrong language. */
export function translate(locale: Locale, key: string, vars?: Record<string, string | number>): string {
  const fallback = resolveMessage("en", key) ?? key;
  const value = resolveMessage(locale, key) ?? fallback;
  return interpolate(value, vars);
}

export function useI18n() {
  const locale = useLocaleStore((state) => state.locale);
  const setLocale = useLocaleStore((state) => state.setLocale);
  const t = useCallback(
    (key: string, vars?: Record<string, string | number>) => translate(locale, key, vars),
    [locale],
  );
  return { locale, setLocale, t };
}
