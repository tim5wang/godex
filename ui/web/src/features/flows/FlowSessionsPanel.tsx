import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Empty,
  Input,
  InputNumber,
  Popconfirm,
  Select,
  Space,
  Tag,
  Typography,
} from "antd";
import {
  appendFlowSessionEvent,
  createFlowSession,
  endFlowSession,
  getFlowSession,
  listFlowSessionEvents,
  listFlowSessions,
  publishFlowSessionSignal,
  pauseFlowSession,
  resumeFlowSession,
  type FlowSessionEvent,
  type FlowSessionEventInput,
  type FlowSessionView,
  type FlowVersionView,
} from "../../lib/api";
import { showError } from "../../lib/notifications";
import { VoiceBar } from "../../components/VoiceBar";
import { isVoiceAgentDefinition } from "./voiceAgent";

const { Text, Paragraph } = Typography;

interface Props {
  flowId: string;
  token: string | null;
  versions: FlowVersionView[];
  t: (key: string, values?: Record<string, string | number>) => string;
  guided?: boolean;
  voiceEnabled?: boolean;
  onPublishVersion?: (version: string) => void;
  onOpenDesigner?: () => void;
}

function prettyTime(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function eventStatusColor(status?: string) {
  if (status === "completed" || status === "active") return "green";
  if (status === "error" || status === "canceled") return "red";
  if (status === "paused") return "orange";
  return "blue";
}

export function FlowSessionsPanel({
  flowId,
  token,
  versions,
  t,
  guided = false,
  voiceEnabled = true,
  onPublishVersion,
  onOpenDesigner,
}: Props) {
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  const sessionVersions = useMemo(
    () => versions.filter((row) =>
      row.definition?.execution_mode === "session" &&
      (!guided || isVoiceAgentDefinition(row.definition)),
    ),
    [versions, guided],
  );
  const publishedSessionVersions = useMemo(
    () => sessionVersions.filter((row) => row.status === "published"),
    [sessionVersions],
  );
  const [selectedSessionID, setSelectedSessionID] = useState<string>();
  const [createVersion, setCreateVersion] = useState<string>();
  const [inputsText, setInputsText] = useState("{}");
  const [eventType, setEventType] = useState<string>();
  const [eventSource, setEventSource] = useState("web-ui");
  const [sourceSequence, setSourceSequence] = useState<string>("");
  const [correlationID, setCorrelationID] = useState("");
  const [payloadText, setPayloadText] = useState("{}");
  const [voiceOutputField, setVoiceOutputField] = useState("");

  useEffect(() => {
    const available = guided ? publishedSessionVersions : sessionVersions;
    if (!createVersion || !available.some((row) => row.version === createVersion)) {
      setCreateVersion(
        available.find((row) => row.status === "published")?.version ??
          (!guided ? sessionVersions.at(-1)?.version : undefined),
      );
    }
  }, [sessionVersions, publishedSessionVersions, createVersion, guided]);

  const sessionsQuery = useQuery({
    queryKey: ["flow-sessions", flowId],
    queryFn: () => listFlowSessions(token, flowId),
    refetchInterval: (query) =>
      (query.state.data ?? []).some((session) => ["active", "paused"].includes(session.status))
        ? 3000
        : false,
  });
  const sessions = sessionsQuery.data ?? [];
  useEffect(() => {
    if (selectedSessionID && sessions.some((session) => session.session_id === selectedSessionID)) return;
    setSelectedSessionID(sessions[0]?.session_id);
  }, [sessions, selectedSessionID]);

  const listedSession = sessions.find((session) => session.session_id === selectedSessionID);
  const detailQuery = useQuery({
    queryKey: ["flow-session", flowId, selectedSessionID],
    queryFn: () => getFlowSession(token, flowId, selectedSessionID!),
    enabled: Boolean(selectedSessionID),
    refetchInterval: (query) =>
      ["active", "paused"].includes(query.state.data?.status ?? "") ? 1500 : false,
  });
  const session = detailQuery.data ?? listedSession;
  const sessionDefinition = session
    ? versions.find((row) => row.version === session.version)?.definition
    : undefined;
  const voiceOutputFields = useMemo(() => {
    const fields = new Set<string>();
    for (const node of sessionDefinition?.nodes ?? []) {
      for (const output of node.outputs ?? []) {
        if (!output.type || output.type === "string" || output.type === "text") {
          fields.add(output.name);
        }
      }
    }
    return [...fields].sort();
  }, [sessionDefinition]);
  const triggers = sessionDefinition?.session_workflow?.triggers ?? [];
  const selectedTrigger =
    triggers.find((trigger) => trigger.event_type === eventType) ?? triggers[0];
  const eventsQuery = useQuery({
    queryKey: ["flow-session-events", flowId, selectedSessionID, session?.last_sequence],
    queryFn: () =>
      listFlowSessionEvents(
        token,
        flowId,
        selectedSessionID!,
        Math.max(0, (session?.last_sequence ?? 0) - 100),
        100,
      ),
    enabled: Boolean(selectedSessionID),
    refetchInterval: ["active", "paused"].includes(session?.status ?? "") ? 2000 : false,
  });
  const events: FlowSessionEvent[] = eventsQuery.data ?? [];

  useEffect(() => {
    const first = triggers[0]?.event_type;
    if (first && !triggers.some((trigger) => trigger.event_type === eventType)) {
      setEventType(first);
    }
  }, [triggers, eventType]);

  useEffect(() => {
    if (guided && voiceOutputFields.includes("speech")) {
      setVoiceOutputField("speech");
      return;
    }
    if (voiceOutputField && voiceOutputFields.includes(voiceOutputField)) return;
    setVoiceOutputField(voiceOutputFields.length === 1 ? voiceOutputFields[0] : "");
  }, [voiceOutputFields, voiceOutputField, guided]);

  const invalidateSessions = () => {
    void queryClient.invalidateQueries({ queryKey: ["flow-sessions", flowId] });
    if (selectedSessionID) {
      void queryClient.invalidateQueries({ queryKey: ["flow-session", flowId, selectedSessionID] });
      void queryClient.invalidateQueries({ queryKey: ["flow-session-events", flowId, selectedSessionID] });
    }
  };
  const createMutation = useMutation({
    mutationFn: async () => {
      if (!createVersion) throw new Error(t("flows.sessionChooseVersion"));
      let inputs: Record<string, unknown>;
      try {
        const parsed: unknown = JSON.parse(inputsText || "{}");
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
          throw new Error("inputs must be a JSON object");
        }
        inputs = parsed as Record<string, unknown>;
      } catch (error) {
        throw new Error(`${t("flows.jsonInvalid")}: ${error instanceof Error ? error.message : String(error)}`);
      }
      return createFlowSession(token, flowId, { version: createVersion, inputs });
    },
    onSuccess: (created) => {
      setSelectedSessionID(created.session_id);
      message.success(t("flows.sessionCreated"));
      invalidateSessions();
    },
    onError: (error) => showError(message, error, t("flows.sessionOperationFailed")),
  });
  const transitionMutation = useMutation({
    mutationFn: async (action: "pause" | "resume" | "completed" | "canceled") => {
      if (!selectedSessionID) throw new Error(t("flows.sessionSelect"));
      if (action === "pause") return pauseFlowSession(token, flowId, selectedSessionID);
      if (action === "resume") return resumeFlowSession(token, flowId, selectedSessionID);
      return endFlowSession(token, flowId, selectedSessionID, action);
    },
    onSuccess: () => {
      message.success(t("flows.sessionUpdated"));
      invalidateSessions();
    },
    onError: (error) => showError(message, error, t("flows.sessionOperationFailed")),
  });
  const eventMutation = useMutation({
    mutationFn: async () => {
      if (!session || !selectedTrigger) throw new Error(t("flows.sessionNoTriggers"));
      let payload: Record<string, unknown>;
      try {
        const parsed: unknown = JSON.parse(payloadText || "{}");
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
          throw new Error("payload must be a JSON object");
        }
        payload = parsed as Record<string, unknown>;
      } catch (error) {
        throw new Error(`${t("flows.jsonInvalid")}: ${error instanceof Error ? error.message : String(error)}`);
      }
      const sequence = sourceSequence.trim() ? Number(sourceSequence) : undefined;
      if (sequence !== undefined && (!Number.isSafeInteger(sequence) || sequence < 1)) {
        throw new Error(t("flows.sessionSourceSequenceInvalid"));
      }
      const input: FlowSessionEventInput = {
        source: eventSource.trim() || "web-ui",
        ...(sequence !== undefined ? { source_sequence: sequence } : {}),
        type: selectedTrigger.event_type,
        ...(correlationID.trim() ? { correlation_id: correlationID.trim() } : {}),
        payload,
      };
      if (selectedTrigger.delivery === "latest_wins") {
        return publishFlowSessionSignal(token, flowId, session.session_id, input);
      }
      return appendFlowSessionEvent(token, flowId, session.session_id, input);
    },
    onSuccess: () => {
      message.success(
        selectedTrigger?.delivery === "latest_wins"
          ? t("flows.sessionSignalSent")
          : t("flows.sessionEventSent"),
      );
      invalidateSessions();
    },
    onError: (error) => showError(message, error, t("flows.sessionOperationFailed")),
  });

  if (guided && publishedSessionVersions.length === 0) {
    const draftVersions = sessionVersions.filter((row) => row.status !== "published");
    return (
      <Card size="small">
        <Space direction="vertical" style={{ width: "100%" }} size={8}>
          <Alert
            type="info"
            showIcon
            message={draftVersions.length > 0 ? t("flows.voiceAgentNeedsPublish") : t("flows.voiceAgentNoPublishedVersion")}
          />
          {draftVersions.map((version) => (
            <Popconfirm
              key={version.version}
              title={t("flows.publishConfirm")}
              onConfirm={() => onPublishVersion?.(version.version)}
            >
              <Button disabled={!onPublishVersion}>
                {t("flows.voiceAgentPublishVersion", { version: version.version })}
              </Button>
            </Popconfirm>
          ))}
          {onOpenDesigner && (
            <Button onClick={onOpenDesigner}>{t("flows.voiceAgentDesignWithAgent")}</Button>
          )}
        </Space>
      </Card>
    );
  }

  if (sessionVersions.length === 0) {
    return (
      <Empty
        description={
          <Space direction="vertical">
            <Text>{t("flows.sessionNoVersion")}</Text>
            <Text type="secondary">{t("flows.sessionNoVersionHint")}</Text>
          </Space>
        }
      />
    );
  }

  const active = session?.status === "active";
  const paused = session?.status === "paused";
  const createInputs = (
    <Space direction="vertical" style={{ width: "100%" }} size={6}>
      <Select
        style={{ width: "100%" }}
        value={createVersion}
        placeholder={t("flows.sessionChooseVersion")}
        options={(guided ? publishedSessionVersions : sessionVersions).map((row) => ({
          value: row.version,
          label: `${row.version} (${row.status})`,
        }))}
        onChange={setCreateVersion}
      />
      {!guided && (
        <Input.TextArea
          rows={3}
          value={inputsText}
          onChange={(event) => setInputsText(event.target.value)}
          placeholder='{"conversation_id":"..."}'
          style={{ fontFamily: "monospace", fontSize: 12 }}
        />
      )}
      <Button
        type="primary"
        loading={createMutation.isPending}
        onClick={() => createMutation.mutate()}
      >
        {t("flows.sessionCreate")}
      </Button>
    </Space>
  );

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
        {!guided && (
          <Alert
            type="info"
            showIcon
            message={t("flows.sessionRuntimeIntro")}
            description={t("flows.sessionRuntimeMediaHint")}
          />
        )}
      <Card size="small" title={t("flows.sessionCreate")}>
        {createInputs}
      </Card>

      {sessionsQuery.isLoading ? (
        <Card size="small" title={t("flows.sessions")} loading />
      ) : sessions.length === 0 ? (
        <Empty description={t("flows.sessionEmpty")} />
      ) : (
        <Space direction="vertical" style={{ width: "100%" }} size={10}>
          <Card
            size="small"
            title={t("flows.sessions")}
            style={{ width: "100%" }}
          >
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "repeat(auto-fill, minmax(150px, 1fr))",
                gap: 8,
              }}
            >
              {sessions.map((row) => (
                <Button
                  key={row.session_id}
                  type={row.session_id === selectedSessionID ? "primary" : "default"}
                  onClick={() => setSelectedSessionID(row.session_id)}
                  style={{ height: "auto", minWidth: 0, padding: "6px 8px", textAlign: "left" }}
                >
                  <Space direction="vertical" size={1} style={{ alignItems: "flex-start", minWidth: 0 }}>
                    <Text code>{row.session_id.slice(0, 16)}</Text>
                    <Text style={{ fontSize: 11 }}>v{row.version} · {row.status}</Text>
                  </Space>
                </Button>
              ))}
            </div>
          </Card>

          {session ? (
            <Card
              size="small"
              title={
                <Space>
                  <Text code>{session.session_id}</Text>
                  <Tag color={eventStatusColor(session.status)}>{session.status}</Tag>
                  <Text type="secondary">v{session.version}</Text>
                </Space>
              }
              style={{ width: "100%", minWidth: 0 }}
              loading={detailQuery.isLoading}
            >
              <Space wrap style={{ marginBottom: 10 }}>
                {active && (
                  <Button
                    size="small"
                    loading={transitionMutation.isPending}
                    onClick={() => transitionMutation.mutate("pause")}
                  >
                    {t("flows.sessionPause")}
                  </Button>
                )}
                {paused && (
                  <Button
                    size="small"
                    type="primary"
                    loading={transitionMutation.isPending}
                    onClick={() => transitionMutation.mutate("resume")}
                  >
                    {t("flows.sessionResume")}
                  </Button>
                )}
                {(active || paused) && (
                  <>
                    <Popconfirm
                      title={t("flows.sessionEndConfirm")}
                      onConfirm={() => transitionMutation.mutate("completed")}
                    >
                      <Button size="small" danger loading={transitionMutation.isPending}>
                        {t("flows.sessionComplete")}
                      </Button>
                    </Popconfirm>
                    <Popconfirm
                      title={t("flows.sessionCancelConfirm")}
                      onConfirm={() => transitionMutation.mutate("canceled")}
                    >
                      <Button size="small" danger loading={transitionMutation.isPending}>
                        {t("flows.sessionCancel")}
                      </Button>
                    </Popconfirm>
                  </>
                )}
                <Text type="secondary" style={{ fontSize: 11 }}>
                  {t("flows.sessionSequences", {
                    processed: session.processed_sequence,
                    last: session.last_sequence,
                    stateVersion: session.state_version,
                  })}
                </Text>
              </Space>

              {session.last_error && (
                <Alert type="error" showIcon message={t("flows.sessionLastError")} description={session.last_error} />
              )}

              <Card size="small" title={t("flows.sessionVoice")}>
                <Space direction="vertical" size={8} style={{ width: "100%" }}>
                  <Text type="secondary">{t("flows.sessionVoiceHint")}</Text>
                    {!guided && voiceOutputFields.length > 0 && (
                    <Select
                      allowClear
                      value={voiceOutputField || undefined}
                      placeholder={t("flows.sessionVoiceOutputField")}
                      options={voiceOutputFields.map((field) => ({ value: field, label: field }))}
                      onChange={(value) => setVoiceOutputField(value ?? "")}
                      disabled={!active}
                      style={{ width: "100%", maxWidth: 360 }}
                    />
                  )}
                  <VoiceBar
                    key={session.session_id}
                    token={token}
                      sessionId={null}
                      enabled={voiceEnabled}
                    disabled={!active || (voiceOutputFields.length > 1 && !voiceOutputField)}
                    flowSession={{
                      flowId,
                      flowSessionId: session.session_id,
                      afterSequence: session.last_sequence,
                      eventType: selectedTrigger?.event_type ?? "voice.asr_final",
                      ...(voiceOutputField ? { outputField: voiceOutputField } : {}),
                    }}
                  />
                </Space>
              </Card>

              {(session.in_flight_lanes?.length ?? 0) > 0 && (
                <Card size="small" title={t("flows.sessionInFlight")} style={{ marginBottom: 8 }}>
                  <Space wrap>
                    {session.in_flight_lanes?.map((lane, index) => (
                      <Tag key={`${lane.lane_id ?? "default"}-${index}`} color="processing">
                        {lane.lane_id || "default"} · {lane.status} · {lane.event_type} · {lane.duration_ms ?? 0} ms
                      </Tag>
                    ))}
                  </Space>
                </Card>
              )}

              {session.last_execution && (
                <Card size="small" title={t("flows.sessionLastExecution")} style={{ marginBottom: 8 }}>
                  <Space wrap>
                    <Tag>{session.last_execution.delivery}</Tag>
                    {session.last_execution.lane_id && <Tag>{session.last_execution.lane_id}</Tag>}
                    <Tag color={eventStatusColor(session.last_execution.status)}>
                      {session.last_execution.status}
                    </Tag>
                    <Text>{session.last_execution.event_type}</Text>
                    <Text type="secondary">{session.last_execution.duration_ms} ms</Text>
                    <Text type="secondary">{t("flows.sessionOutputsCount", { count: session.last_execution.output_count })}</Text>
                  </Space>
                  <div style={{ marginTop: 6 }}>
                    {session.last_execution.nodes?.map((node) => (
                      <Tag key={node.node_id} color={eventStatusColor(node.status)}>
                        {node.node_id} · {node.status} · {node.duration_ms} ms
                      </Tag>
                    ))}
                  </div>
                </Card>
              )}

                {!guided && <Card size="small" title={t("flows.sessionState")} style={{ marginBottom: 8 }}>
                  <pre style={{ margin: 0, maxHeight: 160, overflow: "auto", fontSize: 11 }}>
                    {JSON.stringify(session.state ?? {}, null, 2)}
                  </pre>
                </Card>}

                {!guided && <Card size="small" title={t("flows.sessionTestEvent")} style={{ marginBottom: 8 }}>
                {triggers.length === 0 ? (
                  <Text type="secondary">{t("flows.sessionNoTriggers")}</Text>
                ) : (
                  <Space direction="vertical" style={{ width: "100%" }} size={6}>
                    <Space wrap>
                      <Select
                        style={{ width: 230 }}
                        value={selectedTrigger?.event_type}
                        options={triggers.map((trigger) => ({
                          value: trigger.event_type,
                          label: `${trigger.event_type} · ${trigger.delivery ?? "durable"}`,
                        }))}
                        onChange={setEventType}
                      />
                      <Input
                        style={{ width: 135 }}
                        value={eventSource}
                        onChange={(event) => setEventSource(event.target.value)}
                        placeholder={t("flows.sessionEventSource")}
                      />
                      <InputNumber
                        min={1}
                        precision={0}
                        style={{ width: 150 }}
                        value={sourceSequence ? Number(sourceSequence) : undefined}
                        onChange={(value) => setSourceSequence(value == null ? "" : String(value))}
                        placeholder={t("flows.sessionSourceSequence")}
                      />
                      <Input
                        style={{ width: 180 }}
                        value={correlationID}
                        onChange={(event) => setCorrelationID(event.target.value)}
                        placeholder={t("flows.sessionCorrelationID")}
                      />
                    </Space>
                    <Input.TextArea
                      rows={3}
                      value={payloadText}
                      onChange={(event) => setPayloadText(event.target.value)}
                      style={{ fontFamily: "monospace", fontSize: 12 }}
                      placeholder='{"text":"hello"}'
                    />
                    <Button
                      type="primary"
                      size="small"
                      loading={eventMutation.isPending}
                      disabled={!active || !selectedTrigger}
                      onClick={() => eventMutation.mutate()}
                    >
                      {selectedTrigger?.delivery === "latest_wins"
                        ? t("flows.sessionSendSignal")
                        : t("flows.sessionSendEvent")}
                    </Button>
                  </Space>
                )}
                </Card>}

                {!guided && <Card size="small" title={t("flows.sessionRecentEvents")}>
                {events.length === 0 ? (
                  <Text type="secondary">{t("flows.sessionNoEvents")}</Text>
                ) : (
                  <div style={{ maxHeight: 250, overflow: "auto" }}>
                    {[...events].reverse().map((event) => (
                      <div
                        key={`${event.sequence}-${event.type}`}
                        style={{
                          borderBottom: "1px solid #f0f0f0",
                          padding: "6px 0",
                          fontSize: 11,
                        }}
                      >
                        <Space wrap size={4}>
                          <Tag>{event.sequence}</Tag>
                          <Text strong>{event.type}</Text>
                          <Text type="secondary">{event.source}</Text>
                          <Text type="secondary">{prettyTime(event.received_at)}</Text>
                        </Space>
                        <Paragraph
                          type="secondary"
                          style={{ fontSize: 11, margin: "2px 0 0", whiteSpace: "pre-wrap" }}
                        >
                          {JSON.stringify(event.payload)}
                        </Paragraph>
                      </div>
                    ))}
                  </div>
                )}
                </Card>}
            </Card>
          ) : (
            <Empty description={t("flows.sessionSelect")} />
          )}
        </Space>
      )}
    </div>
  );
}
