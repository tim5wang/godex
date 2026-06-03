import { useMemo } from "react";
import { Card } from "antd";
import {
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
  Line,
  ComposedChart,
} from "recharts";
import type { UsageTimeSeriesPoint } from "../../../lib/types";
import { useI18n } from "../../../i18n";

interface CreditTrendChartProps {
  data: UsageTimeSeriesPoint[];
  loading?: boolean;
}

export function CreditTrendChart({ data, loading }: CreditTrendChartProps) {
  const { t } = useI18n();
  const isEmpty = !loading && data.length === 0;

  const chartData = useMemo(() => {
    let cumulative = 0;
    return data.map((d) => {
      cumulative += d.credits;
      return {
        bucket: d.bucket,
        credits: Number(d.credits.toFixed(2)),
        cumulative: Number(cumulative.toFixed(2)),
        calls: d.call_count,
      };
    });
  }, [data]);

  return (
    <Card title={t("usage.creditChart.title")} loading={loading} size="small">
      {isEmpty ? (
        <div style={{ textAlign: "center", padding: 40, color: "#999" }}>
          {t("usage.overviewTab.noData")}
        </div>
      ) : (
        <ResponsiveContainer width="100%" height={280}>
          <ComposedChart data={chartData} margin={{ top: 4, right: 16, left: 0, bottom: 0 }}>
            <CartesianGrid strokeDasharray="3 3" opacity={0.4} />
            <XAxis dataKey="bucket" tick={{ fontSize: 11 }} tickLine={false} />
            <YAxis yAxisId="left" tick={{ fontSize: 11 }} width={56} />
            <YAxis yAxisId="right" orientation="right" tick={{ fontSize: 11 }} width={56} />
            <Tooltip />
            <Legend />
            <Bar
              yAxisId="left"
              dataKey="credits"
              name={t("usage.creditChart.credits")}
              fill="#fa8c16"
              radius={[2, 2, 0, 0]}
              isAnimationActive={false}
            />
            <Line
              yAxisId="right"
              type="monotone"
              dataKey="cumulative"
              name={t("usage.creditChart.cumulative")}
              stroke="#1677ff"
              strokeWidth={2}
              dot={false}
              isAnimationActive={false}
            />
          </ComposedChart>
        </ResponsiveContainer>
      )}
    </Card>
  );
}
