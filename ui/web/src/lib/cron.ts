import cronstrue from "cronstrue";
// Register the Chinese locale as a side-effect (keeps the bundle to ~2 locales
// instead of pulling the whole cronstrue i18n build).
import "cronstrue/locales/zh_CN";
import { CronExpressionParser } from "cron-parser";

export type CronLocale = "en" | "zh";

const LOCALE_MAP: Record<CronLocale, string> = {
  en: "en",
  zh: "zh_CN",
};

export type CronField =
  | "minute"
  | "hour"
  | "dayOfMonth"
  | "month"
  | "dayOfWeek";

export interface CronFieldInfo {
  label: string;
  raw: string;
}

export interface CronBreakdown {
  minute: string;
  hour: string;
  dayOfMonth: string;
  month: string;
  dayOfWeek: string;
}

export interface CronValidation {
  valid: boolean;
  error?: string;
}

// The backend validates with robfig/cron ParseStandard, which is the classic
// 5-field format (minute hour day-of-month month day-of-week) plus '@macro'
// forms. cron-parser defaults to a 6-field (second-first) dialect, so we
// coerce the field count here to match the server.
function fieldCount(expr: string): number {
  return expr.trim().split(/\s+/).filter(Boolean).length;
}

export function cronValidate(expr: string): CronValidation {
  const raw = expr.trim();
  if (!raw) {
    return { valid: false, error: "empty" };
  }
  if (!raw.startsWith("@") && fieldCount(raw) !== 5) {
    return { valid: false, error: "field-count" };
  }
  try {
    CronExpressionParser.parse(raw);
    return { valid: true };
  } catch (err) {
    return { valid: false, error: err instanceof Error ? err.message : String(err) };
  }
}

export function cronDescribe(expr: string, locale: CronLocale): string {
  const raw = expr.trim();
  if (!raw) {
    return "";
  }
  try {
    return cronstrue.toString(raw, {
      locale: LOCALE_MAP[locale] ?? "en",
      use24HourTimeFormat: true,
    });
  } catch {
    return "";
  }
}

export function cronNextRuns(expr: string, count = 1, now = new Date()): Date[] {
  const raw = expr.trim();
  if (!raw) {
    return [];
  }
  try {
    const iterator = CronExpressionParser.parse(raw, { currentDate: now });
    const runs: Date[] = [];
    for (let i = 0; i < count; i++) {
      if (iterator.hasNext()) {
        runs.push(iterator.next().toDate());
      }
    }
    return runs;
  } catch {
    return [];
  }
}

export function cronBreakdown(expr: string): CronBreakdown | null {
  const raw = expr.trim();
  if (!raw || raw.startsWith("@")) {
    return null;
  }
  if (fieldCount(raw) !== 5) {
    return null;
  }
  try {
    const it = CronExpressionParser.parse(raw);
    const f = it.fields as unknown as Record<CronField, { options?: { rawValue?: string } }>;
    const get = (k: CronField, fallback: string) => f[k]?.options?.rawValue ?? fallback;
    return {
      minute: get("minute", "*"),
      hour: get("hour", "*"),
      dayOfMonth: get("dayOfMonth", "*"),
      month: get("month", "*"),
      dayOfWeek: get("dayOfWeek", "*"),
    };
  } catch {
    return null;
  }
}
