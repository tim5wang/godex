import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { App as AntApp, Alert, Button, Input, Space, Typography } from "antd";
import { RocketOutlined, SendOutlined, ThunderboltOutlined } from "@ant-design/icons";
import {
  createFlow,
  generateFlowSpec,
  type FlowDefinition,
  type FlowVersionView,
} from "../../lib/api";
import { FlowGramCanvas } from "./FlowGramCanvas";

const { Text, Paragraph } = Typography;

interface ChatMessage {
  role: "user" | "assistant";
  content: string;
  /** Assistant messages carry the draft produced by that turn (for preview). */
  draft?: FlowDefinition;
  error?: boolean;
}

/**
 * NaturalLanguageTab — describe the business process in plain language; the
 * backend LLM drafts a Flow Spec definition; preview it on the canvas and
 * save as a new version. Multi-turn: every follow-up message AMENDS the
 * current draft (incremental editing, P3 余项 1) so the tab behaves like a
 * conversation with the flow instead of a one-shot form.
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
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
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
      setMessages((prev) => [
        ...prev,
        {
          role: "assistant",
          content: draftSummary(def),
          draft: def,
        },
      ]);
    },
    onError: (err) => {
      setMessages((prev) => [
        ...prev,
        {
          role: "assistant",
          content: err instanceof Error ? err.message : String(err),
          error: true,
        },
      ]);
    },
  });

  const send = () => {
    const desc = input.trim();
    if (!desc || generateMutation.isPending) return;
    setMessages((prev) => [...prev, { role: "user", content: desc }]);
    setInput("");
    generateMutation.mutate({ desc, base: draft ?? undefined });
  };

  const saveMutation = useMutation({
    mutationFn: async ({ targetVersion }: { targetVersion: string }) => {
      if (!draft) throw new Error("no draft to save");
      const def: FlowDefinition = { ...draft, flow_id: flowId, version: targetVersion, status: "draft" };
      return createFlow(token, { flow_id: flowId, version: targetVersion, status: "draft", definition: def });
    },
    onSuccess: () => {
      message.success(t("flows.templateApplied"));
      setMessages((prev) => [
        ...prev,
        { role: "assistant", content: t("flows.nlSavedVersion", { v: draftVersion }) },
      ]);
      setDraft(null);
      onApplied();
    },
    onError: (err) => {
      message.error(err instanceof Error ? err.message : String(err));
    },
  });

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      <Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
        {t("flows.nlHint")}
      </Paragraph>

      {/* Conversation */}
      <div
        style={{
          border: "1px solid #eee",
          borderRadius: 8,
          background: "#fff",
          maxHeight: 220,
          overflowY: "auto",
          padding: 8,
          display: "flex",
          flexDirection: "column",
          gap: 6,
        }}
      >
        {messages.length === 0 && (
          <Text type="secondary" style={{ fontSize: 12 }}>
            {t("flows.nlChatEmpty")}
          </Text>
        )}
        {messages.map((m, i) => (
          <div
            key={i}
            style={{
              alignSelf: m.role === "user" ? "flex-end" : "flex-start",
              maxWidth: "88%",
              background: m.role === "user" ? "#e6f4ff" : m.error ? "#fff2f0" : "#f6f6f6",
              borderRadius: 8,
              padding: "6px 10px",
              fontSize: 12,
              whiteSpace: "pre-wrap",
              wordBreak: "break-word",
            }}
          >
            {m.content}
          </div>
        ))}
        {generateMutation.isPending && (
          <Text type="secondary" style={{ fontSize: 11 }}>
            {t("flows.nlThinking")}
          </Text>
        )}
      </div>

      <Space.Compact style={{ width: "100%" }}>
        <Input.TextArea
          rows={2}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder={draft ? t("flows.nlAmendPlaceholder") : t("flows.nlPlaceholder")}
          onPressEnter={(e) => {
            if (!e.shiftKey) {
              e.preventDefault();
              send();
            }
          }}
        />
        <Button
          type="primary"
          icon={<SendOutlined />}
          loading={generateMutation.isPending}
          disabled={!input.trim()}
          onClick={send}
          style={{ height: "auto" }}
        >
          {t("flows.send")}
        </Button>
      </Space.Compact>

      {draft && (
        <Space wrap>
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
          <Button
            icon={<ThunderboltOutlined />}
            disabled={!draft}
            onClick={() =>
              generateMutation.mutate({ desc: t("flows.nlRegenerate"), base: draft })
            }
          >
            {t("flows.nlRegenerate")}
          </Button>
        </Space>
      )}

      {generateMutation.isError && !messages.some((m) => m.error) && (
        <Alert
          type="error"
          showIcon
          message={t("flows.nlGenerateFailed")}
          description={generateMutation.error instanceof Error ? generateMutation.error.message : String(generateMutation.error)}
        />
      )}

      {draft && (
        <div style={{ marginTop: 4 }}>
          <Text strong>{t("flows.nlPreview")}</Text>
          <div style={{ marginTop: 8 }}>
            <FlowGramCanvas def={draft} />
          </div>
        </div>
      )}
    </div>
  );
}

function draftSummary(def: FlowDefinition): string {
  const nodes = def.nodes?.length ?? 0;
  const edges = def.edges?.length ?? 0;
  const kinds = new Map<string, number>();
  for (const n of def.nodes ?? []) {
    kinds.set(n.kind ?? "node", (kinds.get(n.kind ?? "node") ?? 0) + 1);
  }
  const parts = [...kinds.entries()].map(([k, c]) => `${k}×${c}`).join("、");
  return `已生成草稿：${nodes} 节点 / ${edges} 连线${parts ? `（${parts}）` : ""}`;
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
