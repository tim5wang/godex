import { useMemo } from "react";
import { Card } from "antd";
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import type { UsageTimeSeriesPoint } from "../../../lib/types";
import { useI18n } from "../../../i18n";

interface TokenTrendChartProps {
  data: UsageTimeSeriesPoint[];
  loading?: boolean;
}

export function TokenTrendChart({ data, loading }: TokenTrendChartProps) {
  const { t } = useI18n();
  const isEmpty = !loading && data.length === 0;

  const chartData = useMemo(
    () =>
      data.map((d) => ({
        bucket: d.bucket,
        input: d.input_tokens,
        output: d.output_tokens,
        cacheRead: d.cache_read_tokens,
        // Billable = input + output + cache_write (reduced by 75% of cache_read)
        billable: d.billable_tokens,
      })),
    [data],
  );

  return (
    <Card title={t("usage.tokenChart.title")} loading={loading} size="small">
      {isEmpty ? (
        <div style={{ textAlign: "center", padding: 40, color: "#999" }}>
          {t("usage.overviewTab.noData")}
        </div>
      ) : (
        <ResponsiveContainer width="100%" height={280}>
          <AreaChart data={chartData} margin={{ top: 4, right: 16, left: 0, bottom: 0 }}>
            <CartesianGrid strokeDasharray="3 3" opacity={0.4} />
            <XAxis dataKey="bucket" tick={{ fontSize: 11 }} tickLine={false} />
            <YAxis tick={{ fontSize: 11 }} width={56} />
            <Tooltip />
            <Legend />
            <Area
              type="monotone"
              dataKey="input"
              name={t("usage.tokenChart.inputTokens")}
              stackId="billed"
              fill="#1677ff"
              stroke="#1677ff"
              fillOpacity={0.3}
              isAnimationActive={false}
            />
            <Area
              type="monotone"
              dataKey="output"
              name={t("usage.tokenChart.outputTokens")}
              stackId="billed"
              fill="#52c41a"
              stroke="#52c41a"
              fillOpacity={0.3}
              isAnimationActive={false}
            />
            <Area
              type="monotone"
              dataKey="cacheRead"
              name={t("usage.tokenChart.cacheRead")}
              stackId="saved"
              fill="#722ed1"
              stroke="#722ed1"
              fillOpacity={0.15}
              strokeDasharray="4 3"
              isAnimationActive={false}
            />
          </AreaChart>
        </ResponsiveContainer>
      )}
    </Card>
  );
}
