import { Card, List, Space, Tag, Typography } from "antd";
import { useI18n } from "../../../i18n";
import type { RecallDiagnostic } from "../../../lib/types";

export function RecallDiagnosticsPanel({ diagnostics, mode }: { diagnostics: RecallDiagnostic[]; mode?: string }) {
  const { t } = useI18n();
  const label = (value: string) => {
    const key = `chat.recallDiagnostic_${value}`;
    const translated = t(key);
    return translated === key ? value.replace(/_/g, " ") : translated;
  };
  if (!diagnostics.length) return null;
  return (
    <Card size="small" title={t("chat.recallDiagnosticsTitle")}>
      <Typography.Paragraph type="secondary">{t("chat.recallDiagnosticsPreview")}</Typography.Paragraph>
      {mode ? <Tag>{label(mode)}</Tag> : null}
      {diagnostics.map((d, index) => <details key={index}>
        <summary style={{ cursor: "pointer" }}>{`${label(d.layer || "primary")} · ${label(d.intent)} · ${d.returned}/${d.candidates}`}</summary>
        <Space direction="vertical" style={{ width: "100%", paddingTop: 8 }}>
          <Typography.Text>{d.query || t("chat.contextInspectorNoQuery")}</Typography.Text>
          <Space wrap>
            <Tag>{label(d.cache || "not_run")}</Tag>
            <Tag>{d.elapsed_ms.toFixed(2)} ms</Tag>
            <Tag>≈ {d.estimated_result_tokens} tokens</Tag>
          </Space>
          <Typography.Text type="secondary">{t("chat.recallDiagnosticsTokenHint")}</Typography.Text>
          <Space wrap>
            {Object.entries({ ...d.eligibility, ...d.rejected }).filter(([, count]) => count > 0).map(([reason, count]) => (
              <Tag key={reason}>{label(reason)}: {count}</Tag>
            ))}
          </Space>
          <List size="small" dataSource={d.decisions ?? []} renderItem={(decision) => (
            <List.Item>
              <Space direction="vertical" size={2}>
                <Space wrap><Typography.Text strong>{decision.title || decision.id}</Typography.Text><Tag>{label(decision.reason)}</Tag>{decision.kind ? <Tag>{label(decision.kind)}</Tag> : null}</Space>
                {decision.type_match ? <Typography.Text type="secondary">{t("chat.recallDiagnosticsTypeMatch")}</Typography.Text> : null}
                {decision.matched_terms?.length ? <Typography.Text type="secondary">{t("chat.recallDiagnosticsMatched")}: {decision.matched_terms.join(", ")}</Typography.Text> : null}
                <Typography.Text type="secondary" copyable>{decision.id}</Typography.Text>
                {decision.source ? <Typography.Text type="secondary">{decision.source}{decision.version ? ` · v${decision.version}` : ""}</Typography.Text> : null}
                {decision.duplicate_of ? <Typography.Text type="secondary">{t("chat.recallDiagnosticsDuplicateOf")}: {decision.duplicate_of}</Typography.Text> : null}
              </Space>
            </List.Item>
          )} />
          {d.details_truncated ? <Typography.Text type="secondary">{t("chat.recallDiagnosticsTruncated")}</Typography.Text> : null}
        </Space>
      </details>)}
    </Card>
  );
}
