import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Card, Descriptions, Select, Space, Table, Tag, Typography } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useI18n } from "../../../i18n";
import { APIError } from "../../../lib/apiClient";
import { formatBytes, formatCount } from "../../../lib/byteSize";
import { getAdminRelayUsage, getRelayUsage, type RelayUsageSummary } from "../../../lib/apiMetering";
import { reconcileRelayUsage } from "./reconciliation";

/** RelayUsagePanel shows THIS CALLER's forwarded traffic (design §7, basic tier).
 *
 *  The tenant is derived server-side from the principal, so there is no tenant
 *  selector here: one would imply a caller could read another tenant's traffic
 *  metadata. An operator holds no tenant of its own, so the server answers 400 and
 *  the panel says so rather than rendering a misleading zero. */
export function RelayUsagePanel({ token }: { token: string | null }) {
  const { t } = useI18n();
  const [hours, setHours] = useState(24);

  const query = useQuery({
    queryKey: ["relay-usage", token, hours],
    queryFn: () => getRelayUsage(token, hours),
    retry: false,
  });

  const noTenant = query.error instanceof APIError && query.error.status === 400;
  const unavailable = query.error instanceof APIError && query.error.status === 503;

  return (
    <Card
      title={t("usage.relayTitle")}
      loading={query.isLoading}
      extra={
        <Space>
          <WindowSelect value={hours} onChange={setHours} label={t("usage.relayWindow")} />
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void query.refetch()} />
        </Space>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t("usage.relaySubtitle")}
      </Typography.Paragraph>
      {noTenant ? (
        <Alert type="info" showIcon message={t("usage.relayNoTenant")} />
      ) : unavailable ? (
        <Alert type="warning" showIcon message={t("usage.relayUnavailable")} />
      ) : query.isError ? (
        <Alert type="error" showIcon message={query.error instanceof Error ? query.error.message : String(query.error)} />
      ) : query.data ? (
        <SummaryDescriptions summary={query.data.summary} />
      ) : null}
    </Card>
  );
}

/** AdminRelayUsageCard is the reconciliation surface (design §7, P3 acceptance
 *  "metering can be reconciled").
 *
 *  It renders the per-tenant rows AND the grand total, because the two must agree:
 *  a total that does not equal the sum of its rows means traffic was recorded that
 *  no tenant owns. Showing only the rows would hide exactly the gap this exists to
 *  surface. */
export function AdminRelayUsageCard({ token }: { token: string | null }) {
  const { t } = useI18n();
  const [hours, setHours] = useState(24);

  const query = useQuery({
    queryKey: ["admin-relay-usage", token, hours],
    queryFn: () => getAdminRelayUsage(token, hours),
    retry: false,
  });

  const forbidden = query.error instanceof APIError && query.error.status === 403;
  const unavailable = query.error instanceof APIError && query.error.status === 503;

  if (forbidden) {
    return <Alert type="warning" showIcon message={t("settings.adminForbidden")} />;
  }
  if (unavailable) {
    return <Alert type="info" showIcon message={t("usage.relayUnavailable")} />;
  }

  const rows = query.data?.tenants ?? [];
  // The rule lives in reconciliation.ts so it is unit-tested rather than only
  // readable: a total that does not equal its rows is the signal this surface
  // exists for, so it must not depend on an inline expression nobody exercises.
  const { summed, reconciles } = reconcileRelayUsage(rows, query.data?.total);
  const total = query.data?.total;

  return (
    <Card
      title={t("settings.adminRelayTitle")}
      loading={query.isLoading}
      extra={
        <Space>
          <WindowSelect value={hours} onChange={setHours} label={t("usage.relayWindow")} />
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void query.refetch()} />
        </Space>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t("settings.adminRelaySubtitle")}
      </Typography.Paragraph>
      {query.isError ? (
        <Alert type="error" showIcon message={query.error instanceof Error ? query.error.message : String(query.error)} />
      ) : (
        <Space direction="vertical" size={12} style={{ width: "100%" }}>
          {/* The reconciliation verdict is stated explicitly rather than left to the
              reader: comparing totals by eye is how a gap goes unnoticed. */}
          <Alert
            type={reconciles ? "success" : "warning"}
            showIcon
            message={
              reconciles
                ? t("settings.adminRelayReconciled", { count: formatCount(total?.requests) })
                : t("settings.adminRelayMismatch", {
                    total: formatCount(total?.requests),
                    summed: formatCount(summed),
                  })
            }
          />
          <Table<RelayUsageSummary>
            rowKey={(row) => row.tenant_id || "__unscoped__"}
            size="small"
            dataSource={rows}
            pagination={false}
            scroll={{ x: 720 }}
            locale={{ emptyText: t("settings.adminRelayEmpty") }}
            columns={[
              {
                title: t("settings.adminRelayTenantCol"),
                dataIndex: "tenant_id",
                render: (value: string) =>
                  value ? (
                    <Typography.Text>{value}</Typography.Text>
                  ) : (
                    // An empty tenant id is the platform operator's own direct
                    // traffic. Labelled rather than hidden: it is the row that
                    // explains a mismatch, so dropping it would remove the answer.
                    <Tag>{t("settings.adminRelayUnscoped")}</Tag>
                  ),
              },
              { title: t("settings.adminRelayRequestsCol"), dataIndex: "requests", render: (v: number) => formatCount(v) },
              { title: t("settings.adminRelayRequestBytesCol"), dataIndex: "request_bytes", render: (v: number) => formatBytes(v) },
              { title: t("settings.adminRelayResponseBytesCol"), dataIndex: "response_bytes", render: (v: number) => formatBytes(v) },
              {
                title: t("settings.adminRelayLastAtCol"),
                dataIndex: "last_at",
                render: (v?: string) => (v ? new Date(v).toLocaleString() : "-"),
              },
            ]}
          />
        </Space>
      )}
    </Card>
  );
}

function SummaryDescriptions({ summary }: { summary: RelayUsageSummary }) {
  const { t } = useI18n();
  return (
    <Descriptions
      column={{ xs: 1, sm: 2, md: 4 }}
      size="small"
      items={[
        { key: "requests", label: t("settings.adminRelayRequestsCol"), children: formatCount(summary.requests) },
        { key: "request", label: t("settings.adminRelayRequestBytesCol"), children: formatBytes(summary.request_bytes) },
        { key: "response", label: t("settings.adminRelayResponseBytesCol"), children: formatBytes(summary.response_bytes) },
        {
          key: "last",
          label: t("settings.adminRelayLastAtCol"),
          children: summary.last_at ? new Date(summary.last_at).toLocaleString() : "-",
        },
      ]}
    />
  );
}

/** WindowSelect offers the reconciliation periods the server accepts.
 *
 *  Every value is a positive hour count: the server answers 400 for anything else,
 *  so offering a free-form field would invite an error the UI could have prevented. */
function WindowSelect({ value, onChange, label }: { value: number; onChange: (next: number) => void; label: string }) {
  const { t } = useI18n();
  return (
    <Select
      size="small"
      value={value}
      onChange={onChange}
      aria-label={label}
      style={{ minWidth: 140 }}
      options={[
        { value: 1, label: t("usage.relayWindow1h") },
        { value: 24, label: t("usage.relayWindow24h") },
        { value: 168, label: t("usage.relayWindow7d") },
      ]}
    />
  );
}
