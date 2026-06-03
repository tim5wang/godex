import { useMemo } from "react";
import { Card } from "antd";
import {
  ComposedChart,
  Bar,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import type { UsageTimeSeriesPoint } from "../../../lib/types";
import { useI18n } from "../../../i18n";

interface ErrorRateChartProps {
  data: UsageTimeSeriesPoint[];
  loading?: boolean;
}

export function ErrorRateChart({ data, loading }: ErrorRateChartProps) {
  const { t } = useI18n();
  const isEmpty = !loading && data.length === 0;

  const chartData = useMemo(
    () =>
      data.map((d) => ({
        bucket: d.bucket,
        calls: d.call_count,
        errors: d.error_count,
        errorRate:
          d.call_count > 0
            ? Number(((d.error_count / d.call_count) * 100).toFixed(1))
            : 0,
      })),
    [data],
  );

  return (
    <Card title={t("usage.errorChart.title")} loading={loading} size="small">
      {isEmpty ? (
        <div style={{ textAlign: "center", padding: 40, color: "#999" }}>
          {t("usage.overviewTab.noData")}
        </div>
      ) : (
        <ResponsiveContainer width="100%" height={280}>
          <ComposedChart data={chartData} margin={{ top: 4, right: 16, left: 0, bottom: 0 }}>
            <CartesianGrid strokeDasharray="3 3" opacity={0.4} />
            <XAxis dataKey="bucket" tick={{ fontSize: 11 }} tickLine={false} />
            <YAxis yAxisId="left" tick={{ fontSize: 11 }} width={40} />
            <YAxis
              yAxisId="right"
              orientation="right"
              tick={{ fontSize: 11 }}
              width={40}
              tickFormatter={(v: number) => `${v}%`}
            />
            <Tooltip />
            <Legend />
            <Bar
              yAxisId="left"
              dataKey="calls"
              name={t("usage.errorChart.callCount")}
              fill="#1677ff"
              radius={[2, 2, 0, 0]}
              isAnimationActive={false}
            />
            <Line
              yAxisId="right"
              type="monotone"
              dataKey="errorRate"
              name={t("usage.errorChart.errorRate")}
              stroke="#ff4d4f"
              strokeWidth={2}
              dot={{ r: 3, fill: "#ff4d4f" }}
              isAnimationActive={false}
            />
          </ComposedChart>
        </ResponsiveContainer>
      )}
    </Card>
  );
}
