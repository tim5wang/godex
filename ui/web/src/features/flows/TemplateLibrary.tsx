import { useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { App as AntApp, Button, Card, Empty, Input, Popconfirm, Space, Typography } from "antd";
import { RocketOutlined } from "@ant-design/icons";
import { createFlow, type FlowVersionView } from "../../lib/api";
import { FLOW_TEMPLATES, flowTemplateById } from "./flowTemplates";

const { Text, Paragraph } = Typography;

// nextVersion returns the next numeric version ("1" when none exists, max+1
// when versions are numeric; falls back to "1" when unparseable).
function nextVersion(versions: FlowVersionView[]): string {
  let max = 0;
  let found = false;
  for (const v of versions) {
    const n = Number.parseInt(v.version, 10);
    if (Number.isFinite(n)) {
      max = Math.max(max, n);
      found = true;
    }
  }
  return found ? String(max + 1) : "1";
}

/**
 * TemplateLibrary — apply a preset template to an existing flow as a new
 * version (no hand-written JSON required). The user picks a template and an
 * optional version; the definition is generated and saved via the normal
 * create-flow path.
 */
export function TemplateLibrary(props: {
  flowId: string;
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  versions: FlowVersionView[];
  onApplied: () => void;
}) {
  const { flowId, token, t, versions, onApplied } = props;
  const { message } = AntApp.useApp();
  const [version, setVersion] = useState<string>(() => nextVersion(versions));

  const applyMutation = useMutation({
    mutationFn: async ({ templateId, targetVersion }: { templateId: string; targetVersion: string }) => {
      const tpl = flowTemplateById(templateId);
      if (!tpl) throw new Error(`unknown template ${templateId}`);
      const def = tpl.build(flowId, targetVersion);
      return createFlow(token, { flow_id: flowId, version: targetVersion, status: "draft", definition: def });
    },
    onSuccess: () => {
      message.success(t("flows.templateApplied"));
      onApplied();
    },
    onError: (err) => {
      message.error(err instanceof Error ? err.message : String(err));
    },
  });

  const versionsUsed = useMemo(() => versions, [versions]);

  return (
    <div>
      <Space style={{ marginBottom: 12 }} align="center">
        <Text type="secondary">{t("flows.version")}</Text>
        <Input
          style={{ width: 120 }}
          value={version}
          onChange={(e) => setVersion(e.target.value.trim())}
          placeholder="1"
        />
      </Space>
      <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
        {FLOW_TEMPLATES.length === 0 ? (
          <Empty description={t("flows.canvasEmpty")} />
        ) : (
          FLOW_TEMPLATES.map((tpl) => (
            <Card key={tpl.id} size="small">
              <Space style={{ width: "100%", justifyContent: "space-between" }} align="center">
                <div style={{ minWidth: 0 }}>
                  <Text strong>{tpl.name}</Text>
                  <Paragraph type="secondary" style={{ marginBottom: 0, fontSize: 12 }}>
                    {tpl.description}
                  </Paragraph>
                </div>
                <Popconfirm
                  title={t("flows.applyTemplateConfirm")}
                  onConfirm={() => applyMutation.mutate({ templateId: tpl.id, targetVersion: version || "1" })}
                >
                  <Button size="small" icon={<RocketOutlined />} loading={applyMutation.isPending}>
                    {t("flows.applyTemplate")}
                  </Button>
                </Popconfirm>
              </Space>
            </Card>
          ))
        )}
      </div>
      {versionsUsed.length > 0 && (
        <Paragraph type="secondary" style={{ marginTop: 12, fontSize: 12 }}>
          {t("flows.createHint")}
        </Paragraph>
      )}
    </div>
  );
}
