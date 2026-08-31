import { useMemo, useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Descriptions,
  Image,
  QRCode,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import {
  BellOutlined,
  CopyOutlined,
  LogoutOutlined,
  QrcodeOutlined,
  ReloadOutlined,
  SendOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { writeClipboardText } from "../../lib/clipboard";
import { ensurePushSubscription, pushSupported, sendTestPush } from "../../lib/push";
import type {
  ApplyReport,
  ChannelStatus,
  CIKSummary,
  DoctorCheck,
  PackageQualityReport,
  ProviderStatus,
  RuntimeServiceStatus,
  SecurityEvent,
  WeixinAuthStatus,
} from "../../lib/types";
import { configToYaml } from "./settingsConfigModel";

export function NotificationsCard({ token, t }: { token: string; t: (key: string) => string }) {
  const { message } = AntApp.useApp();
  const [pushState, setPushState] = useState<{ supported: boolean; permission: string; subscribed: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const supported = pushSupported();

  const enable = async () => {
    if (!supported) {
      void message.warning(t("settings.pushUnsupported"));
      return;
    }
    setBusy(true);
    try {
      const state = await ensurePushSubscription(token || null);
      setPushState(state);
      void message.success(state.subscribed ? t("settings.pushEnabled") : t("settings.pushDenied"));
    } catch (e: any) {
      void message.error(e?.message || t("settings.pushFailed"));
    } finally {
      setBusy(false);
    }
  };

  const test = async () => {
    setBusy(true);
    try {
      const notified = await sendTestPush(token || null);
      void message.success(notified > 0 ? t("settings.pushTestSent") : t("settings.pushNoSubscribers"));
    } catch (e: any) {
      void message.error(e?.message || t("settings.pushFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Space direction="vertical" size={8} style={{ width: "100%" }}>
      <Typography.Text type="secondary">{t("settings.pushSubtitle")}</Typography.Text>
      {pushState ? (
        <Tag color={pushState.subscribed ? "green" : "default"}>
          {pushState.subscribed ? t("settings.pushEnabled") : t("settings.pushDenied")}
        </Tag>
      ) : null}
      <Space wrap>
        <Button type="primary" icon={<BellOutlined />} loading={busy} disabled={!supported} onClick={() => void enable()}>
          {t("settings.pushEnable")}
        </Button>
        <Button icon={<SendOutlined />} loading={busy} disabled={!supported} onClick={() => void test()}>
          {t("settings.pushTest")}
        </Button>
      </Space>
    </Space>
  );
}

export function ProvidersPanel({
  providers,
  loading,
  testing,
  testingID,
  onTest,
}: {
  providers: ProviderStatus[];
  loading: boolean;
  testing: boolean;
  testingID?: string;
  onTest: (id: string) => void;
}) {
  const { t } = useI18n();
  return (
    <Card title={t("settings.providersTitle")}>
      <Table<ProviderStatus>
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={providers}
        scroll={{ x: 820 }}
        columns={[
          { title: t("settings.providerCol"), dataIndex: "id", render: (_value, row) => <Space><Typography.Text strong>{row.id}</Typography.Text>{row.name ? <Typography.Text type="secondary">{row.name}</Typography.Text> : null}</Space> },
          { title: t("settings.typeCol"), dataIndex: "type", render: (value) => <Tag>{value}</Tag> },
          { title: t("settings.credentialCol"), render: (_value, row) => <Space><Tag color={row.has_credential ? "green" : "default"}>{row.has_credential ? t("settings.credentialPresent") : t("settings.credentialMissing")}</Tag><Typography.Text type="secondary">{row.credential_kind || "-"}</Typography.Text></Space> },
          { title: t("settings.envCol"), dataIndex: "api_key_env", render: (value) => value || "-" },
          { title: t("settings.accountCol"), dataIndex: "account_id", render: (value) => value || "-" },
          { title: t("settings.lastErrorCol"), dataIndex: "last_test_error", render: (value) => value ? <Typography.Text type="danger">{value}</Typography.Text> : "-" },
          { title: t("settings.actionCol"), render: (_value, row) => <Button size="small" loading={testing && testingID === row.id} onClick={() => onTest(row.id)}>{t("settings.testAction")}</Button> },
        ]}
      />
    </Card>
  );
}

export function WeixinPanel({
  auth,
  runtime,
  pending,
  starting,
  loggingOut,
  onStart,
  onLogout,
}: {
  auth?: WeixinAuthStatus;
  runtime?: ChannelStatus;
  pending: boolean;
  starting: boolean;
  loggingOut: boolean;
  onStart: () => void;
  onLogout: () => void;
}) {
  const login = auth?.login;
  const { t } = useI18n();
  const qrInput = resolveWeixinQRInput(login?.qr_code_img_url, login?.qr_code_img_value, login?.qr_code);
  return (
    <Card
      title={t("settings.weixinTitle")}
      extra={
        <Space wrap>
          <Button icon={<QrcodeOutlined />} loading={starting} onClick={onStart}>{t("settings.startLogin")}</Button>
          <Button icon={<LogoutOutlined />} loading={loggingOut} onClick={onLogout}>{t("settings.logout")}</Button>
        </Space>
      }
    >
      <Space direction="vertical" size={16} style={{ width: "100%" }}>
        <Descriptions bordered size="small" column={{ xs: 1, md: 2 }} items={[
          { key: "account", label: t("settings.account"), children: auth?.account_id ?? "default" },
          { key: "enabled", label: t("settings.enabled"), children: auth?.enabled ? t("settings.yes") : t("settings.no") },
          { key: "configured", label: t("settings.configured"), children: auth?.configured ? t("settings.yes") : t("settings.no") },
          { key: "runtime", label: t("settings.runtimeState"), children: runtime ? `${runtime.running ? t("settings.channelRunning") : t("settings.channelIdle")} / ${runtime.state ?? "unknown"}` : pending ? "loading" : "unknown" },
          { key: "bot", label: t("settings.botID"), children: auth?.account?.ilink_bot_id ?? "-" },
          { key: "user", label: t("settings.userID"), children: auth?.account?.ilink_user_id ?? "-" },
        ]} />
        {login?.message || runtime?.detail ? <Alert type="info" showIcon message={login?.message || runtime?.detail} /> : null}
        {runtime?.last_error ? <Alert type="error" showIcon message={runtime.last_error} /> : null}
        {login?.active || qrInput ? (
          <Card size="small" title={t("settings.scanInWeixin")}>
            <div style={{ display: "grid", placeItems: "center", minHeight: 260 }}>
              {qrInput ? renderQRCode(qrInput, t) : <Typography.Text type="secondary">{t("settings.qrNotReady")}</Typography.Text>}
            </div>
          </Card>
        ) : null}
      </Space>
    </Card>
  );
}

export function DoctorPanel({ checks, loading }: { checks: DoctorCheck[]; loading: boolean }) {
  const { t } = useI18n();
  return (
    <Card title={t("settings.doctorTitle")}>
      <Table<DoctorCheck>
        rowKey={(record) => `${record.code}:${record.path ?? ""}:${record.message}`}
        size="small"
        loading={loading}
        dataSource={checks}
        scroll={{ x: 720 }}
        columns={[
          { title: t("settings.severityCol"), dataIndex: "severity", render: (value) => <Tag color={value === "error" ? "red" : value === "warning" ? "gold" : "blue"}>{value}</Tag> },
          { title: t("settings.codeCol"), dataIndex: "code" },
          { title: t("settings.pathCol"), dataIndex: "path", render: (value) => value || "-" },
          { title: t("settings.messageCol"), dataIndex: "message" },
          { title: t("settings.suggestionCol"), dataIndex: "suggestion", render: (value) => value || "-" },
        ]}
      />
    </Card>
  );
}

export function SecurityPanel({
  summary,
  audit,
  packageQuality,
  loading,
}: {
  summary?: CIKSummary;
  audit: SecurityEvent[];
  packageQuality?: PackageQualityReport;
  loading: boolean;
}) {
  const { t } = useI18n();
  const riskItems = summary ? [summary.capability, summary.identity, summary.knowledge] : [];
  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <div className="stat-grid">
        {riskItems.map((item) => (
          <Card key={item.axis} loading={loading}>
            <Space direction="vertical" size={4}>
              <Typography.Text type="secondary">{item.axis.toUpperCase()}</Typography.Text>
              <Space>
                <Tag color={item.level === "high" ? "red" : item.level === "medium" ? "gold" : "green"}>{item.level}</Tag>
                <Typography.Title level={3} style={{ margin: 0 }}>{item.score}</Typography.Title>
              </Space>
              <Typography.Text type="secondary">{(item.items ?? []).slice(0, 4).join(", ") || "-"}</Typography.Text>
            </Space>
          </Card>
        ))}
      </div>
      <Card title={t("settings.effectivePolicy")} loading={loading}>
        <Descriptions
          bordered
          size="small"
          column={{ xs: 1, md: 2 }}
          items={Object.entries(summary?.policy ?? {}).map(([key, value]) => ({
            key,
            label: key,
            children: Array.isArray(value) ? value.join(", ") || "-" : String(value),
          }))}
        />
      </Card>
      <Card title={t("settings.packageRisk")} loading={loading}>
        <Descriptions
          bordered
          size="small"
          column={{ xs: 1, md: 4 }}
          items={[
            { key: "packages", label: t("settings.packages"), children: packageQuality?.package_count ?? 0 },
            { key: "high", label: t("settings.highRisk"), children: <Tag color={(packageQuality?.high_risk_packages ?? 0) > 0 ? "red" : "green"}>{packageQuality?.high_risk_packages ?? 0}</Tag> },
            { key: "runs", label: t("settings.toolRuns"), children: packageQuality?.tool_health.total_runs ?? 0 },
            { key: "rate", label: t("settings.successRate"), children: `${Math.round(packageQuality?.tool_health.success_rate ?? 0)}%` },
          ]}
        />
      </Card>
      <Card title={t("settings.recentAudit")}>
        <Table<SecurityEvent>
          rowKey="id"
          size="small"
          loading={loading}
          dataSource={audit}
          pagination={{ pageSize: 8 }}
          scroll={{ x: 760 }}
          columns={[
            { title: t("settings.timeCol"), dataIndex: "at", render: formatTimestamp },
            { title: t("settings.axisCol"), dataIndex: "category", render: (value) => <Tag>{value}</Tag> },
            { title: t("settings.actionCol"), dataIndex: "action" },
            { title: t("settings.severityCol"), dataIndex: "severity", render: (value) => <Tag color={value === "warning" ? "gold" : value === "error" ? "red" : "blue"}>{value || "info"}</Tag> },
            { title: t("settings.summaryCol"), dataIndex: "summary", render: (value) => value || "-" },
          ]}
        />
      </Card>
    </Space>
  );
}

export function RuntimeServiceCard({
  status,
  loading,
  reloading,
  restarting,
  onReload,
  onRestart,
}: {
  status?: RuntimeServiceStatus;
  loading?: boolean;
  reloading?: boolean;
  restarting?: boolean;
  onReload: () => void;
  onRestart: () => void;
}) {
  const { t } = useI18n();
  const managed = status?.managed === true;
  return (
    <Card
      title={t("settings.runtimeServiceTitle")}
      extra={
        <Space>
          <Button icon={<ReloadOutlined />} aria-label={t("settings.reloadFromDisk")} loading={reloading} onClick={onReload}>
            {t("settings.reloadFromDisk")}
          </Button>
          <Button danger icon={<ReloadOutlined />} aria-label={t("settings.restartService")} loading={restarting} disabled={!managed} onClick={onRestart}>
            {t("settings.restartService")}
          </Button>
        </Space>
      }
    >
      <Descriptions
        bordered
        size="small"
        column={{ xs: 1, md: 2 }}
        items={[
          { key: "managed", label: t("settings.managedService"), children: loading ? "Loading..." : managed ? <Tag color="green">{t("settings.yes")}</Tag> : <Tag>{t("settings.no")}</Tag> },
          { key: "running", label: t("settings.running"), children: status?.running ? <Tag color="green">{t("settings.yes")}</Tag> : <Tag>{t("settings.unknown")}</Tag> },
          { key: "scope", label: t("settings.scope"), children: status?.scope ?? "-" },
          { key: "name", label: t("settings.name"), children: status?.name ?? "-" },
          { key: "service-file", label: t("settings.serviceFile"), children: status?.service_file ?? "-" },
          { key: "log-file", label: t("settings.logFile"), children: status?.log_file ?? "-" },
        ]}
      />
      {managed ? null : (
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          showIcon
          message={status?.detail || t("settings.restartViaService")}
        />
      )}
    </Card>
  );
}

export function ApplyReportView({ report, configInSync }: { report?: ApplyReport; configInSync?: boolean }) {
  const { t } = useI18n();
  if (!report && configInSync !== false) {
    return null;
  }
  return (
    <Space direction="vertical" size={8} style={{ width: "100%", marginTop: 12 }}>
      {configInSync === false ? <Alert type="warning" showIcon message={t("settings.configDrift")} /> : null}
      {report ? (
        <Alert
          type={report.runtime_status === "failed" || report.storage_status === "save_failed" ? "error" : "info"}
          showIcon
          message={report.message || t("settings.lastApplyReport")}
          description={[...(report.warnings ?? []), ...(report.errors ?? [])].join(" ")}
        />
      ) : null}
    </Space>
  );
}

function renderQRCode(value: string, t: ReturnType<typeof useI18n>["t"]) {
  if (value.startsWith("data:image/") || value.startsWith("http://") || value.startsWith("https://")) {
    return <Image src={value} alt={t("settings.weixinQrAlt")} style={{ maxHeight: 280, objectFit: "contain" }} />;
  }
  return <QRCode value={value} size={260} />;
}

function resolveWeixinQRInput(url?: string, value?: string, token?: string) {
  const rawValue = value?.trim();
  if (rawValue) {
    if (rawValue.startsWith("data:image/")) {
      return rawValue;
    }
    if (rawValue.startsWith("<svg") || rawValue.startsWith("<?xml")) {
      return `data:image/svg+xml;utf8,${encodeURIComponent(rawValue)}`;
    }
    return rawValue;
  }
  return url?.trim() || token?.trim() || "";
}

export function ConfigYamlCard(props: {
  storedValues: Record<string, unknown>;
  effectiveValues: Record<string, unknown>;
  loading: boolean;
}) {
  const { message } = AntApp.useApp();
  const { t } = useI18n();
  const { storedValues, effectiveValues, loading } = props;
  const [copied, setCopied] = useState(false);

  const yamlText = useMemo(
    () => configToYaml(storedValues, effectiveValues),
    [effectiveValues, storedValues],
  );

  const handleCopy = async () => {
    try {
      await writeClipboardText(yamlText);
      setCopied(true);
      void message.success(t("settings.yamlCopied"));
      setTimeout(() => setCopied(false), 2000);
    } catch {
      void message.error(t("settings.yamlCopyFailed"));
    }
  };

  return (
    <Card
      title={t("settings.yamlViewTitle")}
      loading={loading}
      extra={
        <Button
          size="small"
          icon={<CopyOutlined />}
          onClick={() => void handleCopy()}
        >
          {copied ? t("settings.copied") : t("settings.copyYaml")}
        </Button>
      }
    >
      <pre
        style={{
          margin: 0,
          padding: 12,
          background: "var(--godex-panel-muted)",
          color: "var(--godex-text)",
          borderRadius: 6,
          fontSize: 13,
          lineHeight: 1.5,
          overflow: "auto",
          maxHeight: "70vh",
          whiteSpace: "pre-wrap",
          wordBreak: "break-word",
        }}
      >
        {yamlText || t("settings.noConfigData")}
      </pre>
    </Card>
  );
}

export function renderChannelCapabilities(capabilities?: ChannelStatus["capabilities"]) {
  if (!capabilities) {
    return "-";
  }
  const labels: Array<[keyof NonNullable<ChannelStatus["capabilities"]>, string]> = [
    ["delivery", "delivery"],
    ["auth_login", "auth"],
    ["media", "media"],
    ["streaming", "stream"],
    ["typing", "typing"],
    ["status", "status"],
    ["allow_from", "allow_from"],
  ];
  const active = labels.filter(([key]) => Boolean(capabilities[key]));
  if (active.length === 0 && !capabilities.session_modes?.length) {
    return "-";
  }
  return (
    <Space size={[4, 4]} wrap>
      {active.map(([key, label]) => <Tag key={key}>{label}</Tag>)}
      {capabilities.session_modes?.map((mode) => <Tag key={`mode-${mode}`} color="blue">{mode}</Tag>)}
    </Space>
  );
}

export function renderDeliveryStatus(delivery?: ChannelStatus["last_delivery"]) {
  if (!delivery) {
    return "-";
  }
  const color = delivery.status === "delivered" ? "green" : delivery.status === "failed" ? "red" : "gold";
  return (
    <Space direction="vertical" size={0}>
      <Tag color={color}>{delivery.status}</Tag>
      <Typography.Text type="secondary">
        {delivery.attempts} attempt{delivery.attempts === 1 ? "" : "s"}
      </Typography.Text>
      {delivery.last_error ? <Typography.Text type="danger" ellipsis={{ tooltip: delivery.last_error }}>{delivery.last_error}</Typography.Text> : null}
    </Space>
  );
}

export function renderAccessDecision(access?: ChannelStatus["last_access"]) {
  if (!access) {
    return "-";
  }
  const color = access.action === "allow" ? "green" : access.action === "deny" ? "red" : "gold";
  const route = [access.platform_id, access.thread_id, access.sender_id].filter(Boolean).join(" / ");
  return (
    <Space direction="vertical" size={0}>
      <Tag color={color}>{access.action}</Tag>
      {route ? <Typography.Text type="secondary" ellipsis={{ tooltip: route }}>{route}</Typography.Text> : null}
      {access.reason ? <Typography.Text type="secondary" ellipsis={{ tooltip: access.reason }}>{access.reason}</Typography.Text> : null}
    </Space>
  );
}

export function formatTimestamp(value?: string) {
  if (!value) {
    return "-";
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

