import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Card, Row, Col } from "antd";
import { getUsageTimeSeries, listUsageKeys } from "../../../lib/api";
import type { UsageTimeSeriesPoint } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import { FilterBar } from "../FilterBar";
import { SummaryCards } from "./SummaryCards";
import type { SummaryData } from "./SummaryCards";
import { TokenTrendChart } from "./TokenTrendChart";
import { CreditTrendChart } from "./CreditTrendChart";
import { ErrorRateChart } from "./ErrorRateChart";

interface OverviewTabProps {
  token: string | null;
}

export function OverviewTab({ token }: OverviewTabProps) {
  const { t } = useI18n();
  const [granularity, setGranularity] = useState<"hour" | "day">("day");
  const [startTime, setStartTime] = useState<string | undefined>();
  const [endTime, setEndTime] = useState<string | undefined>();
  const [selectedKeyId, setSelectedKeyId] = useState<string>("");
  const [selectedModel, setSelectedModel] = useState<string>("");

  // Load keys for filter dropdown
  const keysQuery = useQuery({
    queryKey: ["usage", "keys"],
    queryFn: () => listUsageKeys(token),
  });

  const keyOptions = useMemo(() => {
    const keys = keysQuery.data ?? [];
    const proxyKeys = keys
      .filter((k) => !k.id.startsWith("system:"))
      .map((k) => ({ value: k.id, label: k.name }));
    const systemKeys = keys
      .filter((k) => k.id.startsWith("system:"))
      .map((k) => ({ value: k.id, label: k.name }));
    return [
      { label: "Proxy Keys", options: proxyKeys },
      { label: "System Entries", options: systemKeys },
    ].filter((group) => group.options.length > 0);
  }, [keysQuery.data]);

  // Build query params
  const queryParams = useMemo(
    () => ({
      granularity,
      start_time: startTime,
      end_time: endTime,
      api_key_id: selectedKeyId || undefined,
      model: selectedModel || undefined,
    }),
    [granularity, startTime, endTime, selectedKeyId, selectedModel],
  );

  const timeSeriesQuery = useQuery<UsageTimeSeriesPoint[]>({
    queryKey: ["usage", "time-series", queryParams],
    queryFn: () => getUsageTimeSeries(token, queryParams),
  });

  // Compute summary data from time series
  const summaryData: SummaryData | undefined = useMemo(() => {
    const points = timeSeriesQuery.data;
    if (!points || points.length === 0) return undefined;

    let totalCalls = 0;
    let totalErrors = 0;
    let totalInput = 0;
    let totalOutput = 0;
    let totalCredits = 0;
    let totalCacheRead = 0;

    for (const p of points) {
      totalCalls += p.call_count;
      totalErrors += p.error_count;
      totalInput += p.input_tokens;
      totalOutput += p.output_tokens;
      totalCredits += p.credits;
      totalCacheRead += p.cache_read_tokens;
    }

    // Hit rate uses the supplier convention: cache_read / (uncached
    // input + cache_read). input_tokens is already the uncached portion
    // (see protocol.Usage normalization), and output tokens do not
    // participate in prompt caching at all, so they stay out of the
    // denominator.
    const totalTokensForHitRate = totalInput + totalCacheRead;

    return {
      totalCalls,
      totalTokens: totalInput + totalOutput,
      totalCredits,
      errorRate: totalCalls > 0 ? (totalErrors / totalCalls) * 100 : 0,
      inputTokens: totalInput,
      outputTokens: totalOutput,
      cacheHits: totalCacheRead,
      tokensSaved: totalCacheRead,
      cacheHitRate:
        totalTokensForHitRate > 0
          ? (totalCacheRead / totalTokensForHitRate) * 100
          : 0,
    };
  }, [timeSeriesQuery.data]);

  return (
    <div>
      <Card size="small" style={{ marginBottom: 16 }}>
        <FilterBar
          startTime={startTime}
          endTime={endTime}
          onRangeChange={(start, end) => {
            setStartTime(start);
            setEndTime(end);
          }}
          granularity={granularity}
          onGranularityChange={setGranularity}
          keyOptions={keyOptions}
          selectedKeyId={selectedKeyId}
          onKeyIdChange={(id) => setSelectedKeyId(id ?? "")}
          selectedModel={selectedModel}
          onModelChange={(m) => setSelectedModel(m ?? "")}
          onRefresh={() => timeSeriesQuery.refetch()}
          loading={timeSeriesQuery.isFetching}
        />
      </Card>

      <SummaryCards data={summaryData} loading={timeSeriesQuery.isLoading} />

      <Row gutter={[16, 16]}>
        <Col xs={24} lg={12}>
          <TokenTrendChart
            data={timeSeriesQuery.data ?? []}
            loading={timeSeriesQuery.isLoading}
          />
        </Col>
        <Col xs={24} lg={12}>
          <CreditTrendChart
            data={timeSeriesQuery.data ?? []}
            loading={timeSeriesQuery.isLoading}
          />
        </Col>
        <Col xs={24}>
          <ErrorRateChart
            data={timeSeriesQuery.data ?? []}
            loading={timeSeriesQuery.isLoading}
          />
        </Col>
      </Row>
    </div>
  );
}
