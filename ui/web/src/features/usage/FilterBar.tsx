import { DatePicker, Select, Space, Button } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import dayjs from "dayjs";
import type { Dayjs } from "dayjs";
import { useI18n } from "../../i18n";

const { RangePicker } = DatePicker;

interface FilterBarProps {
  /** Date range start (ISO string) */
  startTime?: string;
  /** Date range end (ISO string) */
  endTime?: string;
  /** Callback when range changes */
  onRangeChange: (start?: string, end?: string) => void;
  /** Granularity: "hour" or "day" */
  granularity?: "hour" | "day";
  /** Callback when granularity changes */
  onGranularityChange?: (g: "hour" | "day") => void;
  /** API key filter options (grouped) */
  keyOptions?: { label: string; options: { value: string; label: string }[] }[];
  /** Selected API key ID */
  selectedKeyId?: string;
  /** Callback when key filter changes */
  onKeyIdChange?: (id: string) => void;
  /** Model filter options */
  modelOptions?: { value: string }[];
  /** Selected model */
  selectedModel?: string;
  /** Callback when model filter changes */
  onModelChange?: (model: string) => void;
  /** Refetch callback */
  onRefresh?: () => void;
  /** Whether data is loading */
  loading?: boolean;
}

export function FilterBar({
  startTime,
  endTime,
  onRangeChange,
  granularity,
  onGranularityChange,
  keyOptions,
  selectedKeyId,
  onKeyIdChange,
  modelOptions,
  selectedModel,
  onModelChange,
  onRefresh,
  loading,
}: FilterBarProps) {
  const { t } = useI18n();
  const rangeValue: [Dayjs | null, Dayjs | null] = [
    startTime ? dayjs(startTime) : null,
    endTime ? dayjs(endTime) : null,
  ];

  return (
    <Space wrap style={{ marginBottom: 16 }}>
      <RangePicker
        value={rangeValue}
        onChange={(dates) => {
          if (dates && dates[0] && dates[1]) {
            onRangeChange(dates[0].toISOString(), dates[1].toISOString());
          } else {
            onRangeChange(undefined, undefined);
          }
        }}
        allowClear
      />
      {onGranularityChange && (
        <Select
          value={granularity ?? "day"}
          onChange={onGranularityChange}
          options={[
            { value: "hour", label: t("usage.filter.hourly") },
            { value: "day", label: t("usage.filter.daily") },
          ]}
          style={{ width: 100 }}
        />
      )}
      {keyOptions && onKeyIdChange && (
        <Select
          value={selectedKeyId}
          onChange={onKeyIdChange}
          allowClear
          placeholder={t("usage.filter.allKeys")}
          style={{ width: 200 }}
          options={keyOptions}
        />
      )}
      {modelOptions && onModelChange && (
        <Select
          value={selectedModel}
          onChange={onModelChange}
          allowClear
          placeholder={t("usage.filter.allModels")}
          style={{ width: 160 }}
          options={modelOptions}
        />
      )}
      <Button icon={<ReloadOutlined />} onClick={onRefresh} loading={loading} />
    </Space>
  );
}
