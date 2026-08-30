import { useMemo, useState } from "react";
import { Input, Tag, Typography } from "antd";
import { useI18n } from "../i18n";
import {
  cronBreakdown,
  cronDescribe,
  cronNextRuns,
  cronValidate,
  type CronField,
} from "../lib/cron";

interface CronExprInputProps {
  value?: string;
  onChange?: (value: string) => void;
  placeholder?: string;
  size?: "small" | "middle" | "large";
  // When false (default), a full description + field breakdown + next runs are
  // shown below the input. Set to true for a compact single-line assistant.
  compact?: boolean;
  timezone?: string;
}

const FIELD_ORDER: CronField[] = [
  "minute",
  "hour",
  "dayOfMonth",
  "month",
  "dayOfWeek",
];

export function CronExprInput({
  value = "",
  onChange = () => {},
  placeholder = "0 3 * * *",
  size = "middle",
  compact = false,
  timezone,
}: CronExprInputProps) {
  const { locale } = useI18n();
  const [focused, setFocused] = useState(false);
  const trimmed = value.trim();

  const validation = useMemo(() => cronValidate(value), [value]);
  const description = useMemo(
    () => (validation.valid ? cronDescribe(value, locale) : ""),
    [value, locale, validation.valid],
  );
  const nextRuns = useMemo(
    () => (validation.valid ? cronNextRuns(value, 2) : []),
    [value, validation.valid],
  );
  const breakdown = useMemo(
    () => (validation.valid ? cronBreakdown(value) : null),
    [value, validation.valid],
  );

  const showBody = !compact && (focused || trimmed) && trimmed !== "";
  const invalid = !validation.valid;

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4, flex: 1 }}>
      <Input
        allowClear
        style={{ width: "100%" }}
        value={value}
        placeholder={placeholder}
        size={size}
        spellCheck={false}
        aria-label="Cron expression"
        onChange={(e) => onChange(e.target.value)}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        data-invalid={invalid || undefined}
      />
      {showBody && (
        <div style={{ fontSize: 12 }}>
          {invalid ? (
            <Typography.Text type="danger">
              {invalidLabel(locale)}
            </Typography.Text>
          ) : (
            <>
              <Typography.Text type="secondary" style={{ display: "block" }}>
                {description}
              </Typography.Text>
              {description && timezone ? (
                <Typography.Text type="secondary" style={{ display: "block", fontSize: 11 }}>
                  {timezoneLabel(locale, timezone)}
                </Typography.Text>
              ) : null}
              {nextRuns.length > 0 ? (
                <Typography.Text type="secondary" style={{ display: "block", fontSize: 11 }}>
                  {nextRunLabel(locale, nextRuns[0])}
                  {descTime(locale, nextRuns[0])}
                </Typography.Text>
              ) : null}
              {breakdown ? (
                <div style={{ display: "flex", flexWrap: "wrap", gap: "4px 8px", marginTop: 2 }}>
                  {FIELD_ORDER.map((key) => (
                    <Tag key={key} style={{ fontSize: 11, lineHeight: "16px", marginInlineEnd: 0 }}>
                      {fieldLabel(locale, key)}={breakdown[key]}
                    </Tag>
                  ))}
                </div>
              ) : null}
            </>
          )}
        </div>
      )}
    </div>
  );
}

function descTime(locale: "en" | "zh", d: Date): string {
  const opts: Intl.DateTimeFormatOptions = {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  };
  return new Intl.DateTimeFormat(locale === "zh" ? "zh-CN" : "en-US", opts).format(d);
}

function nextRunLabel(locale: "en" | "zh", _d: Date): string {
  return locale === "zh" ? "下次运行: " : "Next run: ";
}

function timezoneLabel(locale: "en" | "zh", timezone: string): string {
  return locale === "zh" ? `时区: ${timezone}` : `Timezone: ${timezone}`;
}

function invalidLabel(locale: "en" | "zh"): string {
  return locale === "zh" ? "无效的 cron 表达式" : "Invalid cron expression";
}

function fieldLabel(locale: "en" | "zh", field: string): string {
  if (locale === "zh") {
    const map: Record<string, string> = {
      minute: "分",
      hour: "时",
      dayOfMonth: "日",
      month: "月",
      dayOfWeek: "周",
    };
    return map[field] ?? field;
  }
  return field;
}
