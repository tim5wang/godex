import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Card, Select, Space, Tag, Button } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import { getCacheStats } from "../../../lib/api";
import type { CacheStats } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import { ResponsiveTable } from "../../../components/ResponsiveTable";

interface CacheStatsTabProps {
  token: string | null;
  /** API key filter options (grouped) */
  keyOptions?: { label: string; options: { value: string; label: string }[] }[];
  /** Selected API key ID */
  selectedKeyId?: string;
  /** Callback when key filter changes */
  onKeyIdChange?: (id: string) => void;
}

export function CacheStatsTab({ token, keyOptions, selectedKeyId, onKeyIdChange }: CacheStatsTabProps) {
  const { t } = useI18n();
  const [range, setRange] = useState("day");

  const statsQuery = useQuery<CacheStats[]>({
    queryKey: ["usage", "cache-stats", range, selectedKeyId],
    queryFn: () => getCacheStats(token, { range, api_key_id: selectedKeyId || undefined }),
  });

  const chartData = useMemo(() => {
    const data = statsQuery.data ?? [];
    // Aggregate by model across periods to get overall hit rate
    const modelMap = new Map<string, {
      model: string;
      inputTokens: number;
      cacheRead: number;
      totalCalls: number;
    }>();
    for (const s of data) {
      const existing = modelMap.get(s.model);
      if (existing) {
        existing.inputTokens += s.input_tokens;
        existing.cacheRead += s.cache_read_tokens;
        existing.totalCalls += s.total_calls;
      } else {
        modelMap.set(s.model, {
          model: s.model,
          inputTokens: s.input_tokens,
          cacheRead: s.cache_read_tokens,
          totalCalls: s.total_calls,
        });
      }
    }
    return Array.from(modelMap.values())
      .map((m) => ({
        model: m.model,
        cacheRead: m.cacheRead,
        inputTokens: m.inputTokens,
        hitRate:
          m.inputTokens + m.cacheRead > 0
            ? Number(
                ((m.cacheRead / (m.inputTokens + m.cacheRead)) * 100).toFixed(1),
              )
            : 0,
        tokensSaved: m.cacheRead,
        totalCalls: m.totalCalls,
      }))
      .sort((a, b) => b.tokensSaved - a.tokensSaved);
  }, [statsQuery.data]);

  const columns = [
    { title: t("usage.sessionPanel.model"), dataIndex: "model", key: "model" },
    { title: t("usage.cachePanel.totalCalls"), dataIndex: "totalCalls", key: "totalCalls" },
    {
      title: t("usage.cachePanel.inputTokens"),
      dataIndex: "inputTokens",
      key: "inputTokens",
      render: (v: number) => v.toLocaleString(),
    },
    {
      title: t("usage.cachePanel.cacheRead"),
      dataIndex: "cacheRead",
      key: "cacheRead",
      render: (v: number) => v.toLocaleString(),
    },
    {
      title: t("usage.cachePanel.hitRate"),
      dataIndex: "hitRate",
      key: "hitRate",
      render: (v: number) => (
        <Tag color={v > 50 ? "green" : v > 20 ? "orange" : "red"}>{v}%</Tag>
      ),
    },
    {
      title: t("usage.cachePanel.tokensSaved"),
      dataIndex: "tokensSaved",
      key: "tokensSaved",
      render: (v: number) => v.toLocaleString(),
    },
  ];

  return (
    <Card
      title={t("usage.cachePanel.title")}
      extra={
        <Space wrap>
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
          <Select
            value={range}
            onChange={setRange}
            options={[
              { value: "day", label: t("usage.cachePanel.today") },
              { value: "week", label: t("usage.cachePanel.last7Days") },
              { value: "month", label: t("usage.cachePanel.last30Days") },
              { value: "all", label: t("usage.cachePanel.allTime") },
            ]}
            style={{ width: 140 }}
          />
          <Button
            icon={<ReloadOutlined />}
            onClick={() => statsQuery.refetch()}
          />
        </Space>
      }
      size="small"
    >
      {chartData.length > 0 && (
        <div style={{ marginBottom: 24 }}>
          <ResponsiveContainer width="100%" height={250}>
            <BarChart
              data={chartData}
              layout="vertical"
              margin={{ top: 4, right: 16, left: 100, bottom: 0 }}
            >
              <CartesianGrid strokeDasharray="3 3" opacity={0.4} />
              <XAxis type="number" tick={{ fontSize: 11 }} />
              <YAxis
                type="category"
                dataKey="model"
                tick={{ fontSize: 11 }}
                width={90}
              />
              <Tooltip />
              <Legend />
              <Bar
                dataKey="inputTokens"
                name={t("usage.cachePanel.inputTokens")}
                stackId="a"
                fill="#1677ff"
                isAnimationActive={false}
              />
              <Bar
                dataKey="cacheRead"
                name={t("usage.cachePanel.cacheRead")}
                stackId="a"
                fill="#52c41a"
                isAnimationActive={false}
              />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      <ResponsiveTable
        dataSource={chartData}
        columns={columns}
        rowKey="model"
        loading={statsQuery.isLoading}
        size="small"
        pagination={false}
        cardTitle={(r) => r.model}
      />
    </Card>
  );
}
