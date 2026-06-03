import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Button,
  Card,
  Drawer,
  Input,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  Descriptions,
  TableColumnsType,
} from "antd";
import {
  SearchOutlined,
  ReloadOutlined,
  EyeOutlined,
} from "@ant-design/icons";
import dayjs from "dayjs";
import { listUsageSessions, getUsageSessionDetail, listUsageKeys } from "../../../lib/api";
import type { SessionUsageSummary } from "../../../lib/types";
import { useI18n } from "../../../i18n";

const { Text } = Typography;

interface SessionTabProps {
  token: string | null;
}

export function SessionTab({ token }: SessionTabProps) {
  const { t } = useI18n();
  const [searchId, setSearchId] = useState("");
  const [selectedKeyId, setSelectedKeyId] = useState<string>("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [detailSessionId, setDetailSessionId] = useState<string | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);

  // Load keys for filter
  const keysQuery = useQuery({
    queryKey: ["usage", "keys"],
    queryFn: () => listUsageKeys(token),
  });

  const keyFilterOptions = useMemo(() => {
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

  // Load session list
  const sessionsQuery = useQuery<SessionUsageSummary[]>({
    queryKey: ["usage", "sessions", selectedKeyId, page, pageSize],
    queryFn: () =>
      listUsageSessions(token, {
        api_key_id: selectedKeyId || undefined,
        limit: pageSize,
        offset: (page - 1) * pageSize,
      }),
  });

  // Load session detail
  const detailQuery = useQuery<SessionUsageSummary>({
    queryKey: ["usage", "session-detail", detailSessionId],
    queryFn: () => getUsageSessionDetail(token, detailSessionId!),
    enabled: !!detailSessionId,
  });

  const filteredSessions = useMemo(() => {
    const sessions = sessionsQuery.data ?? [];
    if (!searchId.trim()) return sessions;
    return sessions.filter((s) =>
      s.session_id.toLowerCase().includes(searchId.trim().toLowerCase()),
    );
  }, [sessionsQuery.data, searchId]);

  const columns: TableColumnsType<SessionUsageSummary> = [
    {
      title: t("usage.sessionPanel.sessionId"),
      dataIndex: "session_id",
      key: "session_id",
      width: 200,
      render: (v: string) => (
        <Text code copyable style={{ fontSize: 12 }}>
          {v.length > 24 ? `${v.slice(0, 24)}...` : v}
        </Text>
      ),
    },
    {
      title: t("usage.sessionPanel.calls"),
      dataIndex: "call_count",
      key: "call_count",
      width: 80,
      sorter: (a, b) => a.call_count - b.call_count,
    },
    {
      title: t("usage.sessionPanel.inputTokens"),
      dataIndex: "input_tokens",
      key: "input_tokens",
      width: 100,
      sorter: (a, b) => a.input_tokens - b.input_tokens,
    },
    {
      title: t("usage.sessionPanel.outputTokens"),
      dataIndex: "output_tokens",
      key: "output_tokens",
      width: 100,
      sorter: (a, b) => a.output_tokens - b.output_tokens,
    },
    {
      title: t("usage.sessionPanel.credits"),
      dataIndex: "credits",
      key: "credits",
      width: 100,
      render: (v: number) => v.toFixed(2),
      sorter: (a, b) => a.credits - b.credits,
    },
    {
      title: t("usage.sessionPanel.firstCall"),
      dataIndex: "first_call",
      key: "first_call",
      width: 120,
      render: (v: string) => (v ? dayjs(v).format("MM-DD HH:mm") : "-"),
    },
    {
      title: t("usage.sessionPanel.lastCall"),
      dataIndex: "last_call",
      key: "last_call",
      width: 120,
      render: (v: string) => (v ? dayjs(v).format("MM-DD HH:mm") : "-"),
    },
    {
      title: t("usage.sessionPanel.models"),
      dataIndex: "model_usage",
      key: "model_usage",
      render: (models: SessionUsageSummary["model_usage"]) =>
        models?.slice(0, 3).map((m) => (
          <Tag key={m.model} style={{ marginBottom: 2 }}>
            {m.model}
          </Tag>
        )),
    },
    {
      title: "",
      key: "actions",
      width: 48,
      render: (_: unknown, record: SessionUsageSummary) => (
        <Button
          type="text"
          size="small"
          icon={<EyeOutlined />}
          onClick={() => {
            setDetailSessionId(record.session_id);
            setDetailOpen(true);
          }}
        />
      ),
    },
  ];

  return (
    <Card
      title={t("usage.sessionPanel.title")}
      extra={
        <Space>
          {selectedKeyId && (
            <Select
              value={selectedKeyId}
              onChange={setSelectedKeyId}
              allowClear
              placeholder={t("usage.sessionPanel.filterByKey")}
              style={{ width: 200 }}
              options={keyFilterOptions}
            />
          )}
          <Input
            placeholder={t("usage.sessionPanel.searchPlaceholder")}
            prefix={<SearchOutlined />}
            value={searchId}
            onChange={(e) => setSearchId(e.target.value)}
            style={{ width: 220 }}
            allowClear
          />
          <Button
            icon={<ReloadOutlined />}
            onClick={() => sessionsQuery.refetch()}
          />
        </Space>
      }
      size="small"
    >
      {/* Key filter for non-empty selection */}
      <Space style={{ marginBottom: 12 }}>
        <Select
          value={selectedKeyId}
          onChange={(val) => {
            setSelectedKeyId(val ?? "");
            setPage(1);
          }}
          allowClear
          placeholder={t("usage.filter.allKeys")}
          style={{ width: 200 }}
          options={keyFilterOptions}
        />
      </Space>

      <Table
        dataSource={filteredSessions}
        columns={columns}
        rowKey="session_id"
        loading={sessionsQuery.isLoading}
        size="small"
        pagination={{
          current: page,
          pageSize,
          onChange: (p, ps) => {
            setPage(p);
            setPageSize(ps);
          },
          showSizeChanger: true,
          pageSizeOptions: ["10", "20", "50"],
          total: filteredSessions.length,
        }}
      />

      <Drawer
        title={`${t("usage.sessionPanel.sessionId")}: ${detailSessionId?.slice(0, 32)}...`}
        open={detailOpen}
        onClose={() => {
          setDetailOpen(false);
          setDetailSessionId(null);
        }}
        width={480}
        loading={detailQuery.isLoading}
      >
        {detailQuery.data && (
          <>
            <Descriptions column={1} size="small" bordered>
              <Descriptions.Item label={t("usage.sessionPanel.sessionId")}>
                <Text copyable>{detailQuery.data.session_id}</Text>
              </Descriptions.Item>
              <Descriptions.Item label={t("usage.sessionPanel.calls")}>
                {detailQuery.data.call_count}
              </Descriptions.Item>
              <Descriptions.Item label={t("usage.sessionPanel.inputTokens")}>
                {detailQuery.data.input_tokens.toLocaleString()}
              </Descriptions.Item>
              <Descriptions.Item label={t("usage.sessionPanel.outputTokens")}>
                {detailQuery.data.output_tokens.toLocaleString()}
              </Descriptions.Item>
              <Descriptions.Item label="Cache Read">
                {detailQuery.data.cache_read_tokens.toLocaleString()}
              </Descriptions.Item>
              <Descriptions.Item label="Cache Write">
                {detailQuery.data.cache_write_tokens.toLocaleString()}
              </Descriptions.Item>
              <Descriptions.Item label="Billable Tokens">
                {detailQuery.data.billable_tokens.toLocaleString()}
              </Descriptions.Item>
              <Descriptions.Item label={t("usage.sessionPanel.credits")}>
                {detailQuery.data.credits.toFixed(4)}
              </Descriptions.Item>
              <Descriptions.Item label={t("usage.sessionPanel.firstCall")}>
                {dayjs(detailQuery.data.first_call).format("YYYY-MM-DD HH:mm:ss")}
              </Descriptions.Item>
              <Descriptions.Item label={t("usage.sessionPanel.lastCall")}>
                {dayjs(detailQuery.data.last_call).format("YYYY-MM-DD HH:mm:ss")}
              </Descriptions.Item>
            </Descriptions>

            <Typography.Title level={5} style={{ marginTop: 24 }}>
              {t("usage.sessionPanel.modelBreakdown")}
            </Typography.Title>
            <Table
              dataSource={detailQuery.data.model_usage}
              columns={[
                { title: t("usage.sessionPanel.model"), dataIndex: "model", key: "model" },
                {
                  title: t("usage.sessionPanel.calls"),
                  dataIndex: "call_count",
                  key: "call_count",
                },
                {
                  title: t("usage.sessionPanel.inputTokens"),
                  dataIndex: "input_tokens",
                  key: "input_tokens",
                },
                {
                  title: t("usage.sessionPanel.outputTokens"),
                  dataIndex: "output_tokens",
                  key: "output_tokens",
                },
              ]}
              rowKey="model"
              size="small"
              pagination={false}
            />
          </>
        )}
      </Drawer>
    </Card>
  );
}


