import { useMemo, useState, type RefObject } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { App as AntApp, Button, Empty, Popconfirm, Space, Table, Tabs, Tag, Typography } from "antd";
import { BugOutlined, CloseOutlined, DeleteOutlined, EyeOutlined, PlayCircleOutlined, SaveOutlined } from "@ant-design/icons";
import { showError } from "../../lib/notifications";
import { CodeViewer } from "../../components/CodeViewer";
import { createFlow, deleteFlowVersion, diagnoseFlowRun, flowRunEvents, listFlowRuns, listFlowVersions, type FlowDefinition, type FlowDiagnosis, type FlowRunEvent, type FlowRunView, type FlowSummaryView, type FlowVersionView } from "../../lib/api";
import { type FlowGramFlowEditorHandle } from "./FlowGramFlowEditor";
import { FlowChatPanel } from "./FlowChatPanel";
import { FlowSessionsPanel } from "./FlowSessionsPanel";
import { SessionWorkflowTab } from "./SessionWorkflowTab";
import { VoiceAgentPanel } from "./VoiceAgentPanel";
import { DefinitionJsonTab, ProductionGatewayTab, VariableScopePanel } from "./FlowDetailPanels";
import { formatTime, runStatusColor, versionStatusColor } from "./flowPageUtils";

const { Text, Paragraph } = Typography;

export function FlowDetailColumn(props: {
  flow: FlowSummaryView;
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  message: ReturnType<typeof AntApp.useApp>["message"];
  onClose: () => void;
  onRefresh: () => void;
  onPublish: (version: string) => void;
  onRun: (version: string) => void;
  onCancelRun: (runId: string) => void;
  onApplyJson: (def: FlowDefinition) => void;
  editorHandleRef: RefObject<FlowGramFlowEditorHandle | null>;
}) {
  const {
    flow,
    token,
    t,
    message,
    onClose,
    onRefresh,
    onPublish,
    onRun,
    onCancelRun,
    onApplyJson,
    editorHandleRef,
  } = props;
  const [activeTab, setActiveTab] = useState("versions");
  const [designerInitialPrompt, setDesignerInitialPrompt] = useState("");

  const versionsQuery = useQuery({
    queryKey: ["flow", flow.flow_id],
    queryFn: () => listFlowVersions(token, flow.flow_id),
  });
  const runsQuery = useQuery({
    queryKey: ["flow-runs", flow.flow_id],
    queryFn: () => listFlowRuns(token, flow.flow_id),
    // Auto-refresh while any run is still active so statuses update live.
    refetchInterval: (query) =>
      (query.state.data ?? []).some((r) =>
        ["pending", "running", "waiting_human"].includes(r.status),
      )
        ? 3000
        : false,
  });

  const versions = versionsQuery.data ?? [];
  const runs = runsQuery.data ?? [];

  // P3 Agent 闭环 §22.2: diagnosis of a failed run — LLM root cause +
  // suggestions + optional fixed definition (NOT saved).
  const [diagnosis, setDiagnosis] = useState<FlowDiagnosis | null>(null);
  const diagnoseMutation = useMutation({
    mutationFn: ({ runId }: { runId: string }) =>
      diagnoseFlowRun(token, runId, flow.flow_id),
    onSuccess: setDiagnosis,
    onError: (err) => showError(message, err, t("flows.diagnoseFailed")),
  });

  // Run detail: a selected run expands to its inputs / outputs / error /
  // event timeline (B1) so the 运行记录 tab shows what actually happened.
  const [runDetail, setRunDetail] = useState<FlowRunView | null>(null);
  const runDetailEventsQuery = useQuery({
    queryKey: ["flow-run-events", flow.flow_id, runDetail?.run_id],
    queryFn: () =>
      runDetail
        ? flowRunEvents(token, runDetail.run_id, flow.flow_id)
        : Promise.resolve([] as FlowRunEvent[]),
    enabled: Boolean(runDetail),
  });
  // Applying the fixed definition saves it as a NEW version (createFlow
  // path) — the actual 优化 → 新版本 leg of the loop.
  const nextVersion = useMemo(() => {
    let max = 0;
    for (const v of versions) {
      const n = Number.parseInt(v.version, 10);
      if (Number.isFinite(n)) max = Math.max(max, n);
    }
    return max > 0 ? String(max + 1) : "1";
  }, [versions]);
  const applyFixedMutation = useMutation({
    mutationFn: (def: FlowDefinition) =>
      createFlow(token, {
        flow_id: flow.flow_id,
        version: nextVersion,
        status: "draft",
        definition: def,
      }),
    onSuccess: () => {
      message.success(t("flows.fixApplied"));
      setDiagnosis(null);
      onRefresh();
    },
    onError: (err) => showError(message, err, t("flows.saveFailed")),
  });

  // Latest definition (ascending versions; the LAST match wins) — used by the
  // variables tab for scope-chain rendering.
  const latestDef = useMemo(() => {
    return [...versions].reverse().find((v) => v.definition)?.definition ?? undefined;
  }, [versions]);

  // 删除版本（C1）：允许删除古早/非运行中的版本；有活动 run 的版本后端会
  // 拒绝（409）。
  const deleteVersionMutation = useMutation({
    mutationFn: ({ version }: { version: string }) =>
      deleteFlowVersion(token, flow.flow_id, version),
    onSuccess: (_data, { version }) => {
      message.success(t("flows.versionDeleted", { v: version }));
      onRefresh();
    },
    onError: (err) => showError(message, err, t("flows.deleteFailed")),
  });

  return (
    <div className="flow-detail-column">
      <div className="flow-detail-head">
        <Text strong>{flow.flow_id}</Text>
        <Space size={2}>
          <Button size="small" type="text" icon={<CloseOutlined />} onClick={onClose} />
        </Space>
      </div>
      <Tabs
        activeKey={activeTab}
        onChange={setActiveTab}
        items={[
          {
            key: "versions",
            label: t("flows.versions"),
            children: (
              <Table<FlowVersionView>
                rowKey="version"
                size="small"
                dataSource={versions}
                pagination={false}
                columns={[
                  { title: t("flows.version"), dataIndex: "version" },
                  {
                    title: t("flows.status"),
                    dataIndex: "status",
                    render: (s: string) => <Tag color={versionStatusColor(s)}>{s}</Tag>,
                  },
                  {
                    title: t("flows.digest"),
                    dataIndex: "digest",
                    render: (d: string | undefined) => (
                      <Text style={{ fontFamily: "monospace", fontSize: 11 }} copyable>
                        {d ? d.slice(0, 12) : "—"}
                      </Text>
                    ),
                  },
                  {
                    title: t("flows.actions"),
                    key: "actions",
                    render: (_: unknown, row: FlowVersionView) => (
                      <Space>
                        {row.status !== "published" && (
                          <Popconfirm
                            title={t("flows.publishConfirm")}
                            onConfirm={() => onPublish(row.version)}
                          >
                            <Button size="small">{t("flows.publish")}</Button>
                          </Popconfirm>
                        )}
                        <Button size="small" icon={<PlayCircleOutlined />} onClick={() => onRun(row.version)}>
                          {t("flows.run")}
                        </Button>
                        <Popconfirm
                          title={t("flows.deleteVersionConfirm", { v: row.version })}
                          okText={t("flows.delete")}
                          okButtonProps={{ danger: true }}
                          onConfirm={() => deleteVersionMutation.mutate({ version: row.version })}
                        >
                          <Button
                            size="small"
                            danger
                            icon={<DeleteOutlined />}
                            loading={
                              deleteVersionMutation.isPending &&
                              deleteVersionMutation.variables?.version === row.version
                            }
                          >
                            {t("flows.delete")}
                          </Button>
                        </Popconfirm>
                      </Space>
                    ),
                  },
                ]}
              />
            ),
          },
          {
            key: "runs",
            label: t("flows.runs"),
            children: (
              <>
                <Table<FlowRunView>
                rowKey="run_id"
                size="small"
                dataSource={runs}
                pagination={false}
                columns={[
                  {
                    title: t("flows.runId"),
                    dataIndex: "run_id",
                    render: (id: string) => <Text style={{ fontFamily: "monospace", fontSize: 11 }}>{id}</Text>,
                  },
                  { title: t("flows.version"), dataIndex: "version" },
                  {
                    title: t("flows.status"),
                    dataIndex: "status",
                    render: (s: string) => <Tag color={runStatusColor(s)}>{s}</Tag>,
                  },
                  {
                    title: t("flows.startedAt"),
                    dataIndex: "started_at",
                    render: (v: string) => formatTime(v),
                  },
                  {
                    title: t("flows.actions"),
                    key: "actions",
                    render: (_: unknown, row: FlowRunView) => (
                      <Space wrap>
                        <Button
                          size="small"
                          icon={<EyeOutlined />}
                          onClick={() => setRunDetail(row)}
                        >
                          {t("flows.detail")}
                        </Button>
                        <Button
                          size="small"
                          icon={<BugOutlined />}
                          disabled={row.status !== "error"}
                          loading={diagnoseMutation.isPending && diagnoseMutation.variables?.runId === row.run_id}
                          onClick={() => diagnoseMutation.mutate({ runId: row.run_id })}
                        >
                          {t("flows.diagnose")}
                        </Button>
                        <Popconfirm
                          title={t("flows.cancelRunConfirm")}
                          onConfirm={() => onCancelRun(row.run_id)}
                        >
                          <Button
                            size="small"
                            danger
                            disabled={row.status === "canceled" || row.status === "completed" || row.status === "error"}
                          >
                            {t("flows.cancel")}
                          </Button>
                        </Popconfirm>
                      </Space>
                    ),
                  },
                ]}
              />
              {runDetail && (
                <div
                  style={{
                    marginTop: 12,
                    border: "1px solid #e5e5e5",
                    borderRadius: 8,
                    padding: 10,
                    background: "#fafafa",
                    display: "flex",
                    flexDirection: "column",
                    gap: 8,
                  }}
                >
                  <Space align="center" style={{ justifyContent: "space-between", width: "100%" }}>
                    <Space size={6}>
                      <Text strong style={{ fontSize: 12 }}>
                        {t("flows.runDetail")}
                      </Text>
                      <Text style={{ fontFamily: "monospace", fontSize: 11 }}>{runDetail.run_id}</Text>
                      <Tag color={runStatusColor(runDetail.status)}>{runDetail.status}</Tag>
                    </Space>
                    <Button size="small" onClick={() => setRunDetail(null)}>
                      {t("flows.diagnoseClose")}
                    </Button>
                  </Space>
                  {runDetail.inputs && Object.keys(runDetail.inputs).length > 0 && (
                    <div>
                      <Text strong style={{ fontSize: 12 }}>
                        {t("flows.runInputs")}
                      </Text>
                      <CodeViewer value={JSON.stringify(runDetail.inputs, null, 2)} language="json" maxHeight={160} />
                    </div>
                  )}
                  {runDetail.outputs && Object.keys(runDetail.outputs).length > 0 && (
                    <div>
                      <Text strong style={{ fontSize: 12 }}>
                        {t("flows.runOutputs")}
                      </Text>
                      <CodeViewer value={JSON.stringify(runDetail.outputs, null, 2)} language="json" maxHeight={160} />
                    </div>
                  )}
                  {runDetail.error && (
                    <div>
                      <Text strong type="danger" style={{ fontSize: 12 }}>
                        {t("flows.runError")}
                      </Text>
                      <Paragraph type="danger" style={{ fontSize: 12, marginBottom: 0 }}>
                        {runDetail.error}
                      </Paragraph>
                    </div>
                  )}
                  <div>
                    <Text strong style={{ fontSize: 12 }}>
                      {t("flows.debugEvents")}
                    </Text>
                    {runDetailEventsQuery.data && runDetailEventsQuery.data.length > 0 ? (
                      <div
                        style={{
                          border: "1px solid #eee",
                          borderRadius: 6,
                          background: "#fff",
                          maxHeight: 200,
                          overflowY: "auto",
                          padding: 6,
                          fontFamily: "monospace",
                          fontSize: 11,
                        }}
                      >
                        {runDetailEventsQuery.data.map((ev, i) => {
                          const at = ev.at ? new Date(ev.at as string).toLocaleTimeString() : "";
                          const node = ev.node_id ? `[${ev.node_id}]` : "";
                          const { event, node_id: _n, at: _a, ...rest } = ev;
                          const payload = Object.keys(rest).length > 0 ? JSON.stringify(rest) : "";
                          return (
                            <div key={i} style={{ whiteSpace: "pre-wrap", lineHeight: 1.5 }}>
                              {`${at} ${event} ${node} ${payload}`.trim()}
                            </div>
                          );
                        })}
                      </div>
                    ) : (
                      <Paragraph type="secondary" style={{ fontSize: 11, marginBottom: 0 }}>
                        {t("flows.noEvents")}
                      </Paragraph>
                    )}
                  </div>
                </div>
              )}
              {diagnosis && (
                <div
                  style={{
                    marginTop: 12,
                    border: "1px solid #e5e5e5",
                    borderRadius: 8,
                    padding: 10,
                    background: "#fafafa",
                    display: "flex",
                    flexDirection: "column",
                    gap: 8,
                  }}
                >
                  <Space align="center" style={{ justifyContent: "space-between", width: "100%" }}>
                    <Text strong style={{ fontSize: 12 }}>
                      {t("flows.diagnosePanel")} · {diagnosis.run_id.slice(0, 12)}…
                    </Text>
                    <Button size="small" onClick={() => setDiagnosis(null)}>
                      {t("flows.diagnoseClose")}
                    </Button>
                  </Space>
                  <div>
                    <Text strong style={{ fontSize: 12 }}>
                      {t("flows.diagnoseRootCause")}
                    </Text>
                    <Paragraph style={{ fontSize: 12, marginBottom: 0 }}>
                      {diagnosis.root_cause || diagnosis.summary}
                    </Paragraph>
                  </div>
                  {diagnosis.suggestions.length > 0 && (
                    <div>
                      <Text strong style={{ fontSize: 12 }}>
                        {t("flows.diagnoseSuggestions")}
                      </Text>
                      <ul style={{ margin: "4px 0 0", paddingLeft: 20, fontSize: 12 }}>
                        {diagnosis.suggestions.map((s, i) => (
                          <li key={i}>{s}</li>
                        ))}
                      </ul>
                    </div>
                  )}
                  {diagnosis.fixed_definition && (
                    <Space>
                      <Button
                        size="small"
                        type="primary"
                        icon={<SaveOutlined />}
                        loading={applyFixedMutation.isPending}
                        onClick={() => applyFixedMutation.mutate(diagnosis.fixed_definition!)}
                      >
                        {t("flows.applyFix")} v{nextVersion}
                      </Button>
                      <Text type="secondary" style={{ fontSize: 11 }}>
                        {t("flows.applyFixHint")}
                      </Text>
                    </Space>
                  )}
                </div>
              )}
              </>
            ),
          },
          {
            key: "naturallang",
            label: t("flows.naturalLanguage"),
            children: (
              <FlowChatPanel
                flowId={flow.flow_id}
                token={token}
                designerSessionId={flow.designer_session_id}
                initialPrompt={designerInitialPrompt}
                onInitialPromptConsumed={() => setDesignerInitialPrompt("")}
                onVersionApplied={onRefresh}
                getCanvasSnapshot={() =>
                  editorHandleRef.current?.getCurrentDefinition() ?? undefined
                }
              />
            ),
          },
          {
            key: "session-config",
            label: t("flows.sessionConfiguration"),
            children: (
              <SessionWorkflowTab
                flowId={flow.flow_id}
                token={token}
                versions={versions}
                getCurrentDefinition={() =>
                  editorHandleRef.current?.getCurrentDefinition() ?? undefined
                }
                onSaved={onRefresh}
                t={t}
              />
            ),
          },
          {
            key: "session-runtime",
            label: t("flows.sessionRuntime"),
            children: (
              <FlowSessionsPanel
                flowId={flow.flow_id}
                token={token}
                versions={versions}
                t={t}
              />
            ),
          },
          {
            key: "voice-agent",
            label: t("flows.voiceAgentTab"),
            children: (
              <VoiceAgentPanel
                flowId={flow.flow_id}
                token={token}
                versions={versions}
                t={t}
                onRefresh={onRefresh}
                onPublish={onPublish}
                onOpenDesigner={(prompt) => {
                  setDesignerInitialPrompt(prompt ?? "");
                  setActiveTab("naturallang");
                }}
              />
            ),
          },
          {
            key: "variables",
            label: t("flows.variables"),
            children: latestDef ? (
              <VariableScopePanel
                def={latestDef}
                token={token}
                flowId={flow.flow_id}
                t={t}
                message={message}
                onRefresh={onRefresh}
                nextVersion={nextVersion}
              />
            ) : (
              <Empty description={t("flows.canvasEmpty")} />
            ),
          },
          {
            key: "json",
            label: t("flows.definition"),
            children: (
              <DefinitionJsonTab
                versions={versions}
                t={t}
                message={message}
                onRefresh={onRefresh}
                onApplyJson={onApplyJson}
                editorHandleRef={editorHandleRef}
              />
            ),
          },
          {
            key: "production",
            label: t("flows.production"),
            children: (
              <ProductionGatewayTab flow={flow} token={token} t={t} versions={versions} />
            ),
          },
        ]}
      />
    </div>
  );
}
