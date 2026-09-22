import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { App as AntApp, Alert, Button, Input, Space, Typography } from "antd";
import { RocketOutlined, ThunderboltOutlined } from "@ant-design/icons";
import {
  createFlow,
  generateFlowSpec,
  type FlowDefinition,
  type FlowVersionView,
} from "../../lib/api";
import { FlowGramCanvas } from "./FlowGramCanvas";

const { Text, Paragraph } = Typography;

/**
 * NaturalLanguageTab — describe the business process in plain language; the
 * backend LLM drafts a Flow Spec definition; preview it on the canvas and
 * save as a new version. No hand-written JSON required (P2.5).
 */
export function NaturalLanguageTab(props: {
  flowId: string;
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  versions: FlowVersionView[];
  onApplied: () => void;
}) {
  const { flowId, token, t, versions, onApplied } = props;
  const { message } = AntApp.useApp();
  const [description, setDescription] = useState("");
  const [draft, setDraft] = useState<FlowDefinition | null>(null);
  const [draftVersion, setDraftVersion] = useState(() => nextNumericVersion(versions));

  const generateMutation = useMutation({
    mutationFn: async ({ desc, base }: { desc: string; base?: FlowDefinition }) => {
      const def = await generateFlowSpec(token, desc, base);
      // The generated flow_id is a suggestion; pin it to the current flow.
      def.flow_id = flowId;
      def.version = draftVersion || "1";
      def.status = "draft";
      return def;
    },
    onSuccess: (def) => {
      setDraft(def);
      message.success(t("flows.nlGenerated"));
    },
    onError: (err) => {
      message.error(err instanceof Error ? err.message : String(err));
    },
  });

  const saveMutation = useMutation({
    mutationFn: async ({ targetVersion }: { targetVersion: string }) => {
      if (!draft) throw new Error("no draft to save");
      const def: FlowDefinition = { ...draft, flow_id: flowId, version: targetVersion, status: "draft" };
      return createFlow(token, { flow_id: flowId, version: targetVersion, status: "draft", definition: def });
    },
    onSuccess: () => {
      message.success(t("flows.templateApplied"));
      setDraft(null);
      setDescription("");
      onApplied();
    },
    onError: (err) => {
      message.error(err instanceof Error ? err.message : String(err));
    },
  });

  return (
    <div>
      <Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t("flows.nlHint")}
      </Paragraph>
      <Input.TextArea
        rows={4}
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        placeholder={draft ? t("flows.nlAmendPlaceholder") : t("flows.nlPlaceholder")}
      />
      <Space style={{ marginTop: 12 }}>
        {draft ? (
          <Button
            type="primary"
            icon={<ThunderboltOutlined />}
            loading={generateMutation.isPending}
            disabled={!description.trim()}
            onClick={() => generateMutation.mutate({ desc: description.trim(), base: draft })}
          >
            {t("flows.nlAmend")}
          </Button>
        ) : (
          <Button
            type="primary"
            icon={<ThunderboltOutlined />}
            loading={generateMutation.isPending}
            disabled={!description.trim()}
            onClick={() => generateMutation.mutate({ desc: description.trim() })}
          >
            {t("flows.nlGenerate")}
          </Button>
        )}
        {draft && (
          <>
            <Text type="secondary">{t("flows.version")}</Text>
            <Input
              style={{ width: 100 }}
              value={draftVersion}
              onChange={(e) => setDraftVersion(e.target.value.trim())}
              placeholder="1"
            />
            <Button
              icon={<RocketOutlined />}
              type="primary"
              loading={saveMutation.isPending}
              onClick={() => saveMutation.mutate({ targetVersion: draftVersion || "1" })}
            >
              {t("flows.save")}
            </Button>
          </>
        )}
      </Space>

      {generateMutation.isError && (
        <Alert
          type="error"
          showIcon
          style={{ marginTop: 12 }}
          message={t("flows.nlGenerateFailed")}
          description={generateMutation.error instanceof Error ? generateMutation.error.message : String(generateMutation.error)}
        />
      )}

      {draft && (
        <div style={{ marginTop: 16 }}>
          <Text strong>{t("flows.nlPreview")}</Text>
          <div style={{ marginTop: 8 }}>
            <FlowGramCanvas def={draft} />
          </div>
        </div>
      )}
    </div>
  );
}

function nextNumericVersion(versions: FlowVersionView[]): string {
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
