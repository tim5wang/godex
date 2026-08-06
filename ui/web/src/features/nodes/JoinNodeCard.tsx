import { useState } from "react";
import { Alert, App, Button, Card, Input, Select, Space, Typography } from "antd";
import { CheckOutlined, CopyOutlined, PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { issueNodeCredential, registerControlNode } from "../../lib/api";
import { writeClipboardText } from "../../lib/clipboard";
import { useSettingsStore } from "../../store/settings";
import { buildJoinCommand } from "./joinCommand";

function suggestNodeID(): string {
  const rand = Math.random().toString(36).slice(2, 8);
  return `node-${rand}`;
}

export function JoinNodeCard() {
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  const { message } = App.useApp();

  const [nodeID, setNodeID] = useState("");
  const [name, setName] = useState("");
  const [trustLevel, setTrustLevel] = useState("trusted");
  const [generating, setGenerating] = useState(false);
  const [command, setCommand] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const centerURL = typeof window !== "undefined" ? window.location.origin : "";

  const generate = async () => {
    setError(null);
    setCommand(null);
    const id = (nodeID || suggestNodeID()).trim();
    if (!/^[a-zA-Z0-9_-]+$/.test(id)) {
      setError(t("nodes.joinInvalidID"));
      return;
    }
    setGenerating(true);
    try {
      await registerControlNode(token || null, { id, name: name.trim() || undefined, trust_level: trustLevel });
      const cred = await issueNodeCredential(token || null, id);
      setCommand(
        buildJoinCommand({
          centerURL,
          nodeID: id,
          credential: cred.credential,
          trustLevel,
          name: name.trim() || undefined,
        }),
      );
      setNodeID(id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setGenerating(false);
    }
  };

  const copyCommand = async () => {
    if (!command) {
      return;
    }
    try {
      await writeClipboardText(command);
      setCopied(true);
      void message.success(t("nodes.joinCopied"));
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      void message.error(t("nodes.joinCopyFailed"));
    }
  };

  return (
    <Card
      title={
        <Space size={8}>
          <PlusOutlined />
          <span>{t("nodes.joinTitle")}</span>
        </Space>
      }
      size="small"
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t("nodes.joinSubtitle")}
      </Typography.Paragraph>
      <Space wrap>
        <Input
          placeholder={t("nodes.joinIDPlaceholder")}
          value={nodeID}
          onChange={(e) => setNodeID(e.target.value)}
          style={{ width: 200 }}
          allowClear
        />
        <Input
          placeholder={t("nodes.joinNamePlaceholder")}
          value={name}
          onChange={(e) => setName(e.target.value)}
          style={{ width: 180 }}
          allowClear
        />
        <Select
          value={trustLevel}
          onChange={setTrustLevel}
          options={[
            { value: "trusted", label: t("nodes.joinTrustTrusted") },
            { value: "guarded-remote", label: t("nodes.joinTrustGuarded") },
          ]}
          style={{ width: 180 }}
        />
        <Button type="primary" icon={<PlusOutlined />} loading={generating} onClick={() => void generate()}>
          {t("nodes.joinGenerate")}
        </Button>
      </Space>

      {error ? (
        <Alert type="error" showIcon message={error} style={{ marginTop: 12 }} />
      ) : null}

      {command ? (
        <Space direction="vertical" style={{ marginTop: 12, width: "100%" }}>
          <Typography.Text strong>{t("nodes.joinCommandLabel")}</Typography.Text>
          <Typography.Paragraph
            style={{
              margin: 0,
              padding: "8px 12px",
              background: "var(--godex-code-bg, #f5f5f5)",
              borderRadius: 6,
              fontFamily: "var(--godex-mono, ui-monospace, SFMono-Regular, Menlo, monospace)",
              fontSize: 13,
              wordBreak: "break-all",
            }}
          >
            {command}
          </Typography.Paragraph>
          <Space>
            <Button size="small" icon={copied ? <CheckOutlined /> : <CopyOutlined />} onClick={() => void copyCommand()}>
              {copied ? t("nodes.joinCopied") : t("nodes.joinCopy")}
            </Button>
            <Button size="small" icon={<ReloadOutlined />} onClick={() => void generate()}>
              {t("nodes.joinRegenerate")}
            </Button>
          </Space>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            {t("nodes.joinSteps")}
          </Typography.Paragraph>
        </Space>
      ) : null}
    </Card>
  );
}
