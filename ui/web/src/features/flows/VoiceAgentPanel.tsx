import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, App as AntApp, Button, Card, Input, Popconfirm, Space, Tag, Typography } from "antd";
import { useNavigate } from "react-router-dom";
import { createFlow, type FlowVersionView } from "../../lib/api";
import { FlowSessionsPanel } from "./FlowSessionsPanel";
import { flowTemplateById } from "./flowTemplates";
import { buildVoiceAgentDesignerPrompt, isVoiceAgentDefinition } from "./voiceAgent";

const { Text } = Typography;

interface VoiceStatus {
  enabled: boolean;
  engine_addr: string;
  reachable: boolean;
  ready: boolean;
  error?: string;
  default_asr?: string;
  default_vad?: string;
  default_tts?: string;
}

interface Props {
  flowId: string;
  token: string | null;
  versions: FlowVersionView[];
  t: (key: string, values?: Record<string, string | number>) => string;
  onRefresh: () => void;
  onPublish: (version: string) => void;
  onOpenDesigner: (prompt?: string) => void;
}

async function getVoiceStatus(token: string | null): Promise<VoiceStatus> {
  const response = await fetch("/v1/voice/status", {
    headers: { Accept: "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) },
  });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return (await response.json()) as VoiceStatus;
}

export function VoiceAgentPanel({
  flowId,
  token,
  versions,
  t,
  onRefresh,
  onPublish,
  onOpenDesigner,
}: Props) {
  const { message } = AntApp.useApp();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [goal, setGoal] = useState("");
  const openDesigner = () => onOpenDesigner(buildVoiceAgentDesignerPrompt(goal));
  const voiceVersions = useMemo(
    () => versions.filter((version) => isVoiceAgentDefinition(version.definition)),
    [versions],
  );
  const nextVersion = useMemo(() => {
    const highest = versions.reduce((max, version) => {
      const number = Number.parseInt(version.version, 10);
      return Number.isFinite(number) ? Math.max(max, number) : max;
    }, 0);
    return String(highest + 1);
  }, [versions]);
  const statusQuery = useQuery({
    queryKey: ["voice-status", token],
    queryFn: () => getVoiceStatus(token),
    enabled: Boolean(token),
    refetchInterval: (query) => (query.state.data?.ready ? 30_000 : 5_000),
  });
  const createTemplateMutation = useMutation({
    mutationFn: async () => {
      const template = flowTemplateById("voice-agent");
      if (!template) throw new Error("Voice Agent template is unavailable");
      const definition = template.build(flowId, nextVersion);
      return createFlow(token, {
        flow_id: flowId,
        version: nextVersion,
        status: "draft",
        definition,
      });
    },
    onSuccess: () => {
      message.success(t("flows.voiceAgentTemplateCreated", { version: nextVersion }));
      void queryClient.invalidateQueries({ queryKey: ["flow", flowId] });
      onRefresh();
    },
    onError: (error) => message.error(error instanceof Error ? error.message : String(error)),
  });

  const status = statusQuery.data;
  const enabledAndReady = Boolean(status?.enabled && status.ready);

  return (
    <Space direction="vertical" size={10} style={{ display: "flex" }}>
      <Card size="small" title={t("flows.voiceAgentTitle")}>
        <Space direction="vertical" size={8} style={{ display: "flex" }}>
          {statusQuery.isLoading ? (
            <Text type="secondary">{t("flows.voiceAgentChecking")}</Text>
          ) : statusQuery.isError ? (
            <Alert
              type="error"
              showIcon
              message={t("flows.voiceAgentStatusFailed")}
              description={statusQuery.error instanceof Error ? statusQuery.error.message : String(statusQuery.error)}
            />
          ) : !status?.enabled ? (
            <Alert
              type="warning"
              showIcon
              message={t("flows.voiceAgentDisabled")}
              description={
                <Button size="small" onClick={() => navigate("/settings")}>
                  {t("flows.voiceAgentOpenSettings")}
                </Button>
              }
            />
          ) : !status.reachable || !status.ready ? (
            <Alert
              type="error"
              showIcon
              message={status.error || t("flows.voiceAgentEngineUnavailable", { addr: status.engine_addr })}
              description={t("flows.voiceAgentStartEngine")}
            />
          ) : (
            <Alert
              type="success"
              showIcon
              message={t("flows.voiceAgentReady")}
              description={
                <Space wrap size={4}>
                  <Tag>{status.default_asr}</Tag>
                  <Tag>{status.default_vad}</Tag>
                  <Tag>{status.default_tts}</Tag>
                  <Text type="secondary">{status.engine_addr}</Text>
                </Space>
              }
            />
          )}
          <Text type="secondary">{t("flows.voiceAgentIntro")}</Text>
          <Input.TextArea
            rows={2}
            value={goal}
            onChange={(event) => setGoal(event.target.value)}
            placeholder={t("flows.voiceAgentGoalPlaceholder")}
            aria-label={t("flows.voiceAgentGoalLabel")}
          />
          {voiceVersions.length === 0 ? (
            <Space wrap>
              <Popconfirm
                title={t("flows.voiceAgentTemplateConfirm")}
                onConfirm={() => createTemplateMutation.mutate()}
              >
                <Button type="primary" loading={createTemplateMutation.isPending}>
                  {t("flows.voiceAgentUseTemplate")}
                </Button>
              </Popconfirm>
              <Button onClick={openDesigner}>{t("flows.voiceAgentDesignWithAgent")}</Button>
            </Space>
          ) : (
            <Button onClick={openDesigner}>{t("flows.voiceAgentCustomize")}</Button>
          )}
        </Space>
      </Card>
      {voiceVersions.length > 0 && (
        <FlowSessionsPanel
          flowId={flowId}
          token={token}
          versions={versions}
          t={t}
          guided
          voiceEnabled={enabledAndReady}
          onPublishVersion={onPublish}
          onOpenDesigner={onOpenDesigner}
        />
      )}
    </Space>
  );
}
