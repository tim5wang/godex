import { useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  App as AntApp,
  Button,
  Card,
  Drawer,
  Empty,
  Form,
  Input,
  Popconfirm,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
} from "antd";
import {
  ApiOutlined,
  BugOutlined,
  DownloadOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  RocketOutlined,
  SaveOutlined,
  UploadOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import {
  cancelFlowRun,
  createFlow,
  createFlowRun,
  diagnoseFlowRun,
  flowRunEvents,
  getFlowRun,
  inspectFlows,
  listFlowRuns,
  listFlowVersions,
  listFlows,
  publishFlow,
  streamFlowRunEvents,
  type FlowDefinition,
  type FlowDiagnosis,
  type FlowInspectionReport,
  type FlowRunEvent,
  type FlowRunView,
  type FlowSummaryView,
  type FlowVersionView,
} from "../../lib/api";
import { FlowGramCanvas } from "./FlowGramCanvas";
import { FlowGramFlowEditor, type FlowGramFlowEditorHandle } from "./FlowGramFlowEditor";
import { FLOW_TEMPLATES, flowTemplateById } from "./flowTemplates";
import { TemplateLibrary } from "./TemplateLibrary";
import { NaturalLanguageTab } from "./NaturalLanguageTab";
import { useSettingsStore } from "../../store/settings";

const { Title, Text, Paragraph } = Typography;

/** Create form: basic identity only; the definition is optional (a blank
 * flow can be created first, then filled in from the detail view via
 * templates / visual editor / natural language). */
type FlowFormValues = {
  flow_id: string;
  version: string;
  name?: string;
  description?: string;
  template?: string;
  definition?: string; // optional JSON text (advanced)
};

export function FlowsPage() {
  const { t } = useI18n();
  const { message } = AntApp.useApp();
  const token = useSettingsStore((state) => state.token);
  const queryClient = useQueryClient();

  const [createOpen, setCreateOpen] = useState(false);
  const [detail, setDetail] = useState<FlowSummaryView | null>(null);
  const [detailDrawer, setDetailDrawer] = useState<FlowSummaryView | null>(null);
  const [form] = Form.useForm<FlowFormValues>();

  // JSON editor ⇄ canvas: an externally applied definition (JSON tab → canvas)
  // wins over stored versions until the next save clears it.
  const [externalDef, setExternalDef] = useState<{ def: FlowDefinition; stamp: number }>();
  const editorHandleRef = useRef<FlowGramFlowEditorHandle>(null);

  const flowsQuery = useQuery({
    queryKey: ["flows"],
    queryFn: () => listFlows(token),
  });

  // P3 Agent 闭环 §22.2 定期巡检: aggregate run health across published
  // flows; polled so the report card stays fresh while the page is open.
  const inspectionQuery = useQuery({
    queryKey: ["flow-inspection", 24],
    queryFn: () => inspectFlows(token, 24),
    refetchInterval: 60_000,
  });
  const inspection = inspectionQuery.data;
  const [inspectionOpen, setInspectionOpen] = useState(false);

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["flows"] });
    if (detail) {
      void queryClient.invalidateQueries({ queryKey: ["flow", detail.flow_id] });
    }
  };

  const createMutation = useMutation({
    mutationFn: async (values: FlowFormValues) => {
      // Priority: explicit JSON > template > blank flow.
      let def: FlowDefinition | undefined;
      if (values.definition?.trim()) {
        def = JSON.parse(values.definition) as FlowDefinition;
      } else if (values.template) {
        const tpl = flowTemplateById(values.template);
        if (tpl) {
          def = tpl.build(values.flow_id, values.version);
        }
      }
      return createFlow(token, {
        flow_id: values.flow_id,
        version: values.version,
        status: "draft",
        definition: def,
      });
    },
    onSuccess: () => {
      message.success(t("flows.created"));
      setCreateOpen(false);
      form.resetFields();
      refresh();
    },
    onError: (err) => showError(message, err, t("flows.saveFailed")),
  });

  const publishMutation = useMutation({
    mutationFn: ({ flowId, version }: { flowId: string; version: string }) => publishFlow(token, flowId, version),
    onSuccess: () => {
      message.success(t("flows.published"));
      refresh();
    },
    onError: (err) => showError(message, err, t("flows.publishFailed")),
  });

  const runMutation = useMutation({
    mutationFn: ({ flowId, version }: { flowId: string; version: string }) =>
      createFlowRun(token, flowId, { version: version || undefined, inputs: {} }),
    onSuccess: (run) => {
      message.success(`${t("flows.runStarted")} ${run.run_id}`);
      refresh();
    },
    onError: (err) => showError(message, err, t("flows.runFailed")),
  });

  const cancelRunMutation = useMutation({
    mutationFn: ({ flowId, runId }: { flowId: string; runId: string }) => cancelFlowRun(token, runId, flowId),
    onSuccess: () => {
      message.success(t("flows.runCanceled"));
      refresh();
    },
    onError: (err) => showError(message, err, t("flows.runFailed")),
  });

  return (
    <div style={{ padding: 24 }}>
      <Space style={{ marginBottom: 16, justifyContent: "space-between", width: "100%" }} align="center">
        <div>
          <Title level={4} style={{ marginBottom: 4 }}>
            {t("flows.pageTitle")}
          </Title>
          <Text type="secondary">{t("flows.pageSubtitle")}</Text>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={refresh}>
            {t("flows.refresh")}
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
            {t("flows.create")}
          </Button>
        </Space>
      </Space>

      {inspection && inspection.total > 0 && (
        <div
          onClick={() => setInspectionOpen(!inspectionOpen)}
          style={{
            border: "1px solid #e5e5e5",
            borderRadius: 8,
            padding: "8px 12px",
            marginBottom: 12,
            background: inspection.failed > 0 ? "#fff2f0" : "#f6ffed",
            cursor: "pointer",
            display: "flex",
            flexDirection: "column",
            gap: 6,
          }}
        >
          <Space style={{ justifyContent: "space-between", width: "100%" }} align="center">
            <Space>
              <Text strong style={{ fontSize: 12 }}>
                {t("flows.inspectionPanel")}
              </Text>
              <Tag color={inspection.failed > 0 ? "red" : "green"}>
                {t("flows.inspectionWindow", { hours: inspection.window_hours })}
              </Tag>
            </Space>
            <Text type="secondary" style={{ fontSize: 11 }}>
              {new Date(inspection.generated_at).toLocaleString()}
            </Text>
          </Space>
          <Space wrap size={16}>
            <Text style={{ fontSize: 12 }}>
              {t("flows.inspectionTotal")} <Text strong>{inspection.total}</Text>
            </Text>
            <Text style={{ fontSize: 12 }}>
              {t("flows.inspectionFailed")} <Text strong type="danger">{inspection.failed}</Text>
            </Text>
            <Text style={{ fontSize: 12 }}>
              {t("flows.inspectionFailureRate")}{" "}
              <Text strong>{(inspection.failure_rate * 100).toFixed(0)}%</Text>
            </Text>
            {inspection.waiting > 0 && (
              <Text style={{ fontSize: 12 }}>
                {t("flows.inspectionWaiting")} <Text strong type="warning">{inspection.waiting}</Text>
              </Text>
            )}
          </Space>
          {inspectionOpen && (
            <div style={{ borderTop: "1px solid #eee", paddingTop: 6 }}>
              {inspection.flows.map((f) => (
                <div key={f.flow_id} style={{ display: "flex", gap: 12, fontSize: 11, padding: "2px 0" }}>
                  <Text style={{ fontFamily: "monospace" }}>{f.flow_id}</Text>
                  <Text type="secondary">
                    {t("flows.inspectionRun")} {f.total} · {t("flows.inspectionFail")} {f.failed} ·{" "}
                    {t("flows.inspectionRate")} {(f.failure_rate * 100).toFixed(0)}%
                  </Text>
                  {f.human_waiting > 0 && <Text type="warning">{t("flows.inspectionHumanWait")} {f.human_waiting}</Text>}
                  {f.error_nodes > 0 && <Text type="danger">{t("flows.inspectionErrorNodes")} {f.error_nodes}</Text>}
                  {f.iteration_caps > 0 && <Text type="warning">{t("flows.inspectionIterCaps")} {f.iteration_caps}</Text>}
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      <div style={{ display: "flex", gap: 16, height: "calc(100vh - 180px)", minHeight: 480 }}>
        {/* Left: flow list — click to select; the main area becomes the canvas. */}
        <div style={{ width: 300, flexShrink: 0, border: "1px solid #e5e5e5", borderRadius: 8, overflowY: "auto", background: "#fff" }}>
          {flowsQuery.isLoading ? (
            <Card loading style={{ height: "100%" }} />
          ) : (flowsQuery.data ?? []).length === 0 ? (
            <Empty description={t("flows.empty")} style={{ marginTop: 48 }} />
          ) : (
            (flowsQuery.data ?? []).map((f) => (
              <div
                key={f.flow_id}
                onClick={() => setDetail(f)}
                style={{
                  padding: "10px 12px",
                  cursor: "pointer",
                  borderBottom: "1px solid #f0f0f0",
                  background: detail?.flow_id === f.flow_id ? "#e6f4ff" : "transparent",
                }}
              >
                <Space style={{ justifyContent: "space-between", width: "100%" }}>
                  <Text strong>{f.flow_id}</Text>
                  <Button
                    size="small"
                    icon={<PlayCircleOutlined />}
                    disabled={!f.published}
                    onClick={(e) => {
                      e.stopPropagation();
                      runMutation.mutate({ flowId: f.flow_id, version: f.published ?? "" });
                    }}
                  />
                </Space>
                <Space size={4} style={{ marginTop: 4 }} wrap>
                  {f.draft && <Tag>{`draft ${f.draft}`}</Tag>}
                  {f.gray && <Tag color="orange">{`gray ${f.gray}`}</Tag>}
                  {f.published && <Tag color="green">{`pub ${f.published}`}</Tag>}
                </Space>
              </div>
            ))
          )}
        </div>

        {/* Right: canvas editor as the main body (no drawer to open). */}
        <div style={{ flex: 1, minWidth: 0, display: "flex", flexDirection: "column" }}>
          {detail ? (
            <FlowCanvasMain
              key={detail.flow_id}
              flow={detail}
              token={token}
              t={t}
              message={message}
              onRefresh={refresh}
              onPublish={(version) => publishMutation.mutate({ flowId: detail.flow_id, version })}
              onRun={(version) => runMutation.mutate({ flowId: detail.flow_id, version })}
              onOpenDetail={() => setDetailDrawer(detail)}
              externalDef={externalDef}
              editorHandleRef={editorHandleRef}
              onExternalDefConsumed={() => setExternalDef(undefined)}
            />
          ) : (
            <div
              style={{
                border: "1px solid #e5e5e5",
                borderRadius: 8,
                flex: 1,
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                background: "#fff",
              }}
            >
              <Empty description={t("flows.selectFlowHint")} />
            </div>
          )}
        </div>
      </div>

      {/* Create flow drawer */}
      <Drawer
        title={t("flows.create")}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        width={640}
        destroyOnClose
      >
        <Form form={form} layout="vertical" onFinish={(v) => createMutation.mutate(v)}>
          <Paragraph type="secondary" style={{ fontSize: 12 }}>
            {t("flows.createHint")}
          </Paragraph>
          <Form.Item name="flow_id" label={t("flows.id")} rules={[{ required: true }]}>
            <Input placeholder="fl_order_recovery" />
          </Form.Item>
          <Form.Item name="version" label={t("flows.version")} rules={[{ required: true }]}>
            <Input placeholder="1" />
          </Form.Item>
          <Form.Item name="name" label={t("flows.name")}>
            <Input />
          </Form.Item>
          <Form.Item name="description" label={t("flows.description")}>
            <Input.TextArea rows={2} />
          </Form.Item>
          <Form.Item name="template" label={t("flows.template")}>
            <Select
              allowClear
              placeholder={t("flows.templatePlaceholder")}
              options={FLOW_TEMPLATES.map((tpl) => ({ value: tpl.id, label: `${tpl.name} — ${tpl.description}` }))}
            />
          </Form.Item>
          <Form.Item name="definition" label={t("flows.definitionOptional")}>
            <Input.TextArea
              rows={8}
              style={{ fontFamily: "monospace", fontSize: 12 }}
              placeholder={'{\n  "flow_id": "fl_...",\n  "nodes": [...],\n  "edges": [...]\n}  （可选）'}
            />
          </Form.Item>
          <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={createMutation.isPending}>
            {t("flows.save")}
          </Button>
        </Form>
      </Drawer>

      {/* Detail drawer (secondary: versions/runs/templates/natural-language/json) */}
      {detailDrawer && (
        <FlowDetailDrawer
          flow={detailDrawer}
          token={token}
          t={t}
          message={message}
          onClose={() => setDetailDrawer(null)}
          onRefresh={refresh}
          onPublish={(version) => publishMutation.mutate({ flowId: detailDrawer.flow_id, version })}
          onRun={(version) => runMutation.mutate({ flowId: detailDrawer.flow_id, version })}
          onCancelRun={(runId) => cancelRunMutation.mutate({ flowId: detailDrawer.flow_id, runId })}
          onApplyJson={(def) => setExternalDef({ def, stamp: Date.now() })}
          editorHandleRef={editorHandleRef}
        />
      )}
    </div>
  );
}

function FlowCanvasMain(props: {
  flow: FlowSummaryView;
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  message: ReturnType<typeof AntApp.useApp>["message"];
  onRefresh: () => void;
  onPublish: (version: string) => void;
  onRun: (version: string) => void;
  onOpenDetail: () => void;
  externalDef?: { def: FlowDefinition; stamp: number };
  editorHandleRef: RefObject<FlowGramFlowEditorHandle | null>;
  onExternalDefConsumed: () => void;
}) {
  const {
    flow,
    token,
    t,
    message,
    onRefresh,
    onPublish,
    onRun,
    onOpenDetail,
    externalDef,
    editorHandleRef,
    onExternalDefConsumed,
  } = props;

  const versionsQuery = useQuery({
    queryKey: ["flow", flow.flow_id],
    queryFn: () => listFlowVersions(token, flow.flow_id),
  });
  const versions = versionsQuery.data ?? [];

  const [editorVersion, setEditorVersion] = useState<string>();
  const editorDef = useMemo(() => {
    // Editor defaults to the latest draft with a definition.
    const pick = editorVersion ?? [...versions].reverse().find((v) => v.definition)?.version;
    return versions.find((v) => v.version === pick)?.definition;
  }, [versions, editorVersion]);

  // ---- debug mode ---------------------------------------------------------
  // The canvas is a debugger: pick a version + test inputs, start a run, and
  // the editor highlights per-node status from the polled event log. Production
  // traffic goes through POST /v1/gateway/{route} (see DetailDrawer).
  const [debugMode, setDebugMode] = useState(false);
  const [debugVersion, setDebugVersion] = useState<string>();
  const [debugInputsText, setDebugInputsText] = useState("{}");
  const [debugRunId, setDebugRunId] = useState<string>();
  const [debugStarted, setDebugStarted] = useState(false);

  const debugMutation = useMutation({
    mutationFn: ({ version, inputs }: { version: string; inputs: Record<string, unknown> }) =>
      createFlowRun(token, flow.flow_id, { version, inputs }),
    onSuccess: (run) => {
      setDebugRunId(run.run_id);
      setDebugStarted(true);
      message.success(`${t("flows.debugStarted")} ${run.run_id}`);
    },
    onError: (err) => showError(message, err, t("flows.runFailed")),
  });

  // Live debug events: SSE stream (P3 余项 4) — the backend pushes new
  // workflow events incrementally (500ms tick) until the run reaches a
  // terminal state; the canvas highlights nodes as events arrive (replaces
  // the previous 1.5s full-log polling).
  const [debugEvents, setDebugEvents] = useState<FlowRunEvent[]>([]);
  useEffect(() => {
    if (!debugRunId || !debugStarted) {
      setDebugEvents([]);
      return;
    }
    const ctrl = new AbortController();
    void streamFlowRunEvents(token, debugRunId, flow.flow_id, (ev) => {
      setDebugEvents((prev) => [...prev, ev]);
    }, ctrl.signal).catch(() => {
      /* stream closed / aborted — snapshot queries still work */
    });
    return () => ctrl.abort();
  }, [debugRunId, debugStarted, token, flow.flow_id]);

  const debugRunQuery = useQuery({
    queryKey: ["flow-run", flow.flow_id, debugRunId],
    queryFn: () =>
      debugRunId
        ? getFlowRun(token, debugRunId, flow.flow_id)
        : Promise.reject(new Error("no run")),
    enabled: Boolean(debugRunId),
    refetchInterval: debugStarted ? 1500 : false,
  });

  const startDebug = () => {
    const version = debugVersion ?? editorDef?.version;
    if (!version) {
      message.warning(t("flows.noDefinition"));
      return;
    }
    let inputs: Record<string, unknown> = {};
    try {
      inputs = debugInputsText.trim() ? (JSON.parse(debugInputsText) as Record<string, unknown>) : {};
    } catch (err) {
      message.error(
        `${t("flows.jsonInvalid")}: ${err instanceof Error ? err.message : String(err)}`,
      );
      return;
    }
    debugMutation.mutate({ version, inputs });
  };

  const clearDebug = () => {
    setDebugRunId(undefined);
    setDebugStarted(false);
  };

  const debugRun = debugRunQuery.data;
  const debugStatus = debugRun?.status ?? (debugStarted ? "running" : undefined);

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8, height: "100%" }}>
      <Space style={{ justifyContent: "space-between", width: "100%" }} align="center" wrap>
        <Space align="center">
          <Title level={5} style={{ margin: 0 }}>
            {flow.flow_id}
          </Title>
          {flow.published && <Tag color="green">{`pub ${flow.published}`}</Tag>}
        </Space>
        <Space wrap>
          <Select
            size="small"
            style={{ width: 160 }}
            placeholder={t("flows.version")}
            value={editorVersion}
            onChange={setEditorVersion}
            options={versions.map((v) => ({ value: v.version, label: `${v.version} (${v.status})` }))}
          />
          <Popconfirm
            title={t("flows.publishConfirm")}
            onConfirm={() => editorDef && onPublish(editorDef.version)}
          >
            <Button size="small" icon={<RocketOutlined />} disabled={!editorDef}>
              {t("flows.publish")}
            </Button>
          </Popconfirm>
          <Button
            size="small"
            icon={<BugOutlined />}
            type={debugMode ? "primary" : "default"}
            disabled={!editorDef}
            onClick={() => setDebugMode((v) => !v)}
          >
            {t("flows.debug")}
          </Button>
          <Button size="small" onClick={onOpenDetail}>
            {t("flows.detail")}
          </Button>
        </Space>
      </Space>

      <div style={{ flex: 1, minHeight: 420 }}>
        <FlowGramFlowEditor
          ref={editorHandleRef}
          flowId={flow.flow_id}
          token={token}
          t={t}
          versions={versions}
          externalDef={externalDef}
          runEvents={debugEvents}
          onSaved={() => {
            message.success(t("flows.templateApplied"));
            onExternalDefConsumed();
            onRefresh();
          }}
          onSaveError={(err) => showError(message, err, t("flows.saveFailed"))}
        />
      </div>

      {debugMode && (
        <div
          style={{
            border: "1px solid #e5e5e5",
            borderRadius: 8,
            padding: 10,
            background: "#fafafa",
            display: "flex",
            flexDirection: "column",
            gap: 8,
          }}
        >
          <Space wrap align="center">
            <Text strong style={{ fontSize: 12 }}>
              {t("flows.debugPanel")}
            </Text>
            <Select
              size="small"
              style={{ width: 160 }}
              placeholder={t("flows.version")}
              value={debugVersion}
              onChange={setDebugVersion}
              options={versions.map((v) => ({ value: v.version, label: `${v.version} (${v.status})` }))}
            />
            <Input.TextArea
              size="small"
              style={{ width: 260, fontFamily: "monospace", fontSize: 11 }}
              rows={1}
              placeholder='{"task": "..."}'
              value={debugInputsText}
              onChange={(e) => setDebugInputsText(e.target.value)}
            />
            <Button
              size="small"
              type="primary"
              icon={<PlayCircleOutlined />}
              loading={debugMutation.isPending}
              onClick={startDebug}
            >
              {t("flows.debugStart")}
            </Button>
            {debugRunId && (
              <>
                <Tag color={debugStatus === "completed" ? "green" : debugStatus === "error" ? "red" : "processing"}>
                  {debugRunId.slice(0, 12)}… {debugStatus ?? "running"}
                </Tag>
                <Button size="small" onClick={clearDebug}>
                  {t("flows.debugClear")}
                </Button>
              </>
            )}
          </Space>
          <Paragraph type="secondary" style={{ fontSize: 11, marginBottom: 0 }}>
            {t("flows.debugHint")}
          </Paragraph>
          {(debugEvents.length > 0) && (
            <div
              style={{
                border: "1px solid #eee",
                borderRadius: 6,
                background: "#fff",
                maxHeight: 120,
                overflowY: "auto",
                padding: 6,
                fontFamily: "monospace",
                fontSize: 11,
              }}
            >
              {debugEvents.map((ev, i) => (
                <div key={i} style={{ whiteSpace: "pre-wrap", lineHeight: 1.5 }}>
                  {JSON.stringify(ev)}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function FlowDetailDrawer(props: {
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

  const versionsQuery = useQuery({
    queryKey: ["flow", flow.flow_id],
    queryFn: () => listFlowVersions(token, flow.flow_id),
  });
  const runsQuery = useQuery({
    queryKey: ["flow-runs", flow.flow_id],
    queryFn: () => listFlowRuns(token, flow.flow_id),
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

  // Canvas tab state: selected version (default: latest with a definition)
  // and optional run selection for run-state event highlight.
  const [canvasVersion, setCanvasVersion] = useState<string>();
  const [canvasRun, setCanvasRun] = useState<string>();
  const canvasDef = useMemo(() => {
    // versions are ascending; the LATEST definition is the last match.
    const pick = canvasVersion ?? [...versions].reverse().find((v) => v.definition)?.version;
    return versions.find((v) => v.version === pick)?.definition ?? undefined;
  }, [versions, canvasVersion]);
  const eventsQuery = useQuery({
    queryKey: ["flow-run-events", flow.flow_id, canvasRun],
    queryFn: () => (canvasRun ? flowRunEvents(token, canvasRun, flow.flow_id) : Promise.resolve([])),
    enabled: Boolean(canvasRun),
  });

  return (
    <Drawer title={flow.flow_id} open onClose={onClose} width={720}>
      <Tabs
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
                    render: (s: string) => <Tag>{s}</Tag>,
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
                    render: (v: string) => new Date(v).toLocaleString(),
                  },
                  {
                    title: t("flows.actions"),
                    key: "actions",
                    render: (_: unknown, row: FlowRunView) => (
                      <Space wrap>
                        <Button
                          size="small"
                          icon={<BugOutlined />}
                          disabled={row.status !== "error"}
                          loading={diagnoseMutation.isPending && diagnoseMutation.variables?.runId === row.run_id}
                          onClick={() => diagnoseMutation.mutate({ runId: row.run_id })}
                        >
                          {t("flows.diagnose")}
                        </Button>
                        <Button
                          size="small"
                          danger
                          disabled={row.status === "canceled" || row.status === "completed" || row.status === "error"}
                          onClick={() => onCancelRun(row.run_id)}
                        >
                          {t("flows.cancel")}
                        </Button>
                      </Space>
                    ),
                  },
                ]}
              />
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
                      {t("flows.debugClear")}
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
              <NaturalLanguageTab
                flowId={flow.flow_id}
                token={token}
                t={t}
                versions={versions}
                onApplied={onRefresh}
              />
            ),
          },
          {
            key: "templates",
            label: t("flows.templates"),
            children: (
              <TemplateLibrary
                flowId={flow.flow_id}
                token={token}
                t={t}
                versions={versions}
                onApplied={onRefresh}
              />
            ),
          },
          {
            key: "canvas",
            label: t("flows.canvas"),
            children: (
              <div>
                {versions.length > 0 && (
                  <Space wrap style={{ marginBottom: 12 }}>
                    <Select
                      style={{ width: 220 }}
                      placeholder={t("flows.version")}
                      value={canvasVersion}
                      onChange={setCanvasVersion}
                      options={versions.map((v) => ({ value: v.version, label: `${v.version} (${v.status})` }))}
                    />
                    <Select
                      style={{ width: 260 }}
                      placeholder={t("flows.canvasRun")}
                      value={canvasRun}
                      onChange={setCanvasRun}
                      allowClear
                      options={runs.map((r) => ({ value: r.run_id, label: `${r.run_id.slice(0, 12)}… (${r.status})` }))}
                    />
                  </Space>
                )}
                {canvasDef ? (
                  <FlowGramCanvas def={canvasDef} events={eventsQuery.data} />
                ) : (
                  <Empty description={t("flows.canvasEmpty")} />
                )}
                {canvasRun && (
                  <Paragraph type="secondary" style={{ marginTop: 8, fontSize: 12 }}>
                    {t("flows.canvasRunHint")}
                  </Paragraph>
                )}
              </div>
            ),
          },
          {
            key: "variables",
            label: t("flows.variables"),
            children: canvasDef ? (
              <VariableScopePanel def={canvasDef} t={t} />
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
              <ProductionGatewayTab flow={flow} t={t} versions={versions} />
            ),
          },
        ]}
      />
      <Paragraph type="secondary" style={{ marginTop: 16, fontSize: 12 }}>
        {t("flows.flowgramHint")}
      </Paragraph>
    </Drawer>
  );
}

/**
 * VariableScopePanel — renders the Flow Spec variable scope chain (§9 变量
 * 面板): flow-level inputs/outputs + each node's typed outputs, with the
 * nodes that reference each variable ({{inputs.<name>}} /
 * {{nodes.<id>.outputs.<field>}} in prompts and edge conditions).
 */
function VariableScopePanel(props: {
  def: FlowDefinition;
  t: (k: string, v?: Record<string, string | number>) => string;
}) {
  const { def, t } = props;
  const nodes = def.nodes ?? [];
  const edges = def.edges ?? [];

  // Scan prompt text for {{...}} variable references.
  const refsByVar = new Map<string, string[]>();
  const addRef = (key: string, from: string) => {
    const list = refsByVar.get(key) ?? [];
    if (!list.includes(from)) list.push(from);
    refsByVar.set(key, list);
  };
  const scanText = (text: string | undefined, from: string) => {
    if (!text) return;
    const re = /\{\{\s*([^}]+?)\s*\}\}/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(text)) !== null) {
      addRef(m[1].trim(), from);
    }
  };
  for (const n of nodes) {
    scanText(n.prompt, n.id);
    if (n.branch?.cases) {
      for (const c of n.branch.cases) scanText(c.condition ? JSON.stringify(c.condition) : undefined, n.id);
    }
  }
  for (const e of edges) {
    if (e.when) scanText(JSON.stringify(e.when), `${e.from}→${e.to}`);
  }

  const varRow = (name: string, type: string | undefined, desc: string | undefined, from: string) => {
    const key = name;
    const refs = refsByVar.get(key) ?? [];
    return (
      <div key={`${from}:${name}`} style={{ display: "flex", gap: 8, alignItems: "center", fontSize: 12, padding: "2px 0" }}>
        <Text code style={{ fontSize: 11 }}>{name}</Text>
        {type && <Tag style={{ fontSize: 10 }}>{type}</Tag>}
        {desc && <Text type="secondary" style={{ fontSize: 11 }}>{desc}</Text>}
        <Text type="secondary" style={{ fontSize: 11 }}>
          {t("flows.varRefs")} {refs.length > 0 ? refs.join(", ") : "—"}
        </Text>
      </div>
    );
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
      <div>
        <Text strong style={{ fontSize: 12 }}>{t("flows.varFlowInputs")}</Text>
        <div style={{ marginTop: 4 }}>
          {(def.inputs ?? []).length === 0 ? (
            <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
          ) : (
            (def.inputs ?? []).map((v) => varRow(v.name, v.type, v.desc, "inputs"))
          )}
        </div>
      </div>
      <div>
        <Text strong style={{ fontSize: 12 }}>{t("flows.varFlowOutputs")}</Text>
        <div style={{ marginTop: 4 }}>
          {(def.outputs ?? []).length === 0 ? (
            <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
          ) : (
            (def.outputs ?? []).map((v) => varRow(v.name, v.type, v.desc, "outputs"))
          )}
        </div>
      </div>
      <div>
        <Text strong style={{ fontSize: 12 }}>{t("flows.varNodeOutputs")}</Text>
        <div style={{ marginTop: 4, display: "flex", flexDirection: "column", gap: 6 }}>
          {nodes.length === 0 ? (
            <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
          ) : (
            nodes.map((n) => {
              const outs = n.outputs ?? [];
              return (
                <div key={n.id} style={{ border: "1px solid #f0f0f0", borderRadius: 6, padding: 6 }}>
                  <Text style={{ fontSize: 11, fontFamily: "monospace" }}>{n.id}</Text>{" "}
                  <Text type="secondary" style={{ fontSize: 11 }}>({n.kind})</Text>
                  {outs.length === 0 ? (
                    <Text type="secondary" style={{ fontSize: 11, marginLeft: 8 }}>—</Text>
                  ) : (
                    <div style={{ marginTop: 2 }}>{outs.map((v) => varRow(`nodes.${n.id}.outputs.${v.name}`, v.type, v.desc, n.id))}</div>
                  )}
                </div>
              );
            })
          )}
        </div>
      </div>
    </div>
  );
}

/**
 * Editable definition JSON tab — the advanced entry that stays in sync with
 * the canvas: "Get from canvas" snapshots the current editor, "Apply to
 * canvas" re-imports the text (fixes the old read-only versions[0] bug that
 * showed the OLDEST definition).
 */
function DefinitionJsonTab(props: {
  versions: FlowVersionView[];
  t: (k: string, v?: Record<string, string | number>) => string;
  message: ReturnType<typeof AntApp.useApp>["message"];
  onRefresh: () => void;
  onApplyJson: (def: FlowDefinition) => void;
  editorHandleRef: RefObject<FlowGramFlowEditorHandle | null>;
}) {
  const { versions, t, message, onRefresh, onApplyJson, editorHandleRef } = props;
  const latestDef = useMemo(
    () => [...versions].reverse().find((v) => v.definition)?.definition,
    [versions],
  );
  const [dirty, setDirty] = useState(false);
  const [jsonText, setJsonText] = useState(() =>
    latestDef ? JSON.stringify(latestDef, null, 2) : "",
  );

  // Refresh the text when versions reload, unless the user has unsaved edits.
  useEffect(() => {
    if (dirty) return;
    setJsonText(latestDef ? JSON.stringify(latestDef, null, 2) : "");
  }, [latestDef, dirty]);

  const getFromCanvas = () => {
    const def = editorHandleRef.current?.getCurrentDefinition();
    if (!def) {
      message.warning(t("flows.noDefinition"));
      return;
    }
    setJsonText(JSON.stringify(def, null, 2));
    setDirty(false);
    message.success(t("flows.jsonFromCanvas"));
  };

  const apply = () => {
    let parsed: FlowDefinition;
    try {
      parsed = JSON.parse(jsonText) as FlowDefinition;
      if (!Array.isArray(parsed.nodes) || !Array.isArray(parsed.edges)) {
        throw new Error("missing nodes/edges arrays");
      }
    } catch (err) {
      message.error(
        `${t("flows.jsonInvalid")}: ${err instanceof Error ? err.message : String(err)}`,
      );
      return;
    }
    onApplyJson(parsed);
    setDirty(false);
    message.success(t("flows.jsonApplied"));
  };

  return (
    <div>
      <Input.TextArea
        rows={18}
        style={{ fontFamily: "monospace", fontSize: 11 }}
        value={jsonText}
        onChange={(e) => {
          setJsonText(e.target.value);
          setDirty(true);
        }}
        placeholder={t("flows.noDefinition")}
      />
      <Space style={{ marginTop: 12 }} wrap>
        <Button icon={<ReloadOutlined />} onClick={onRefresh}>
          {t("flows.refresh")}
        </Button>
        <Button icon={<DownloadOutlined />} onClick={getFromCanvas}>
          {t("flows.getFromCanvas")}
        </Button>
        <Button type="primary" icon={<UploadOutlined />} onClick={apply}>
          {t("flows.applyToCanvas")}
        </Button>
      </Space>
    </div>
  );
}

/**
 * Production gateway tab: shows how external clients call this flow through
 * POST /v1/gateway/{route} (biz-key auth) instead of running it from the
 * canvas. The canvas is a debugger; production traffic goes through the API.
 */
function ProductionGatewayTab(props: {
  flow: FlowSummaryView;
  t: (k: string, v?: Record<string, string | number>) => string;
  versions: FlowVersionView[];
}) {
  const { flow, t, versions } = props;
  const published = versions.find((v) => v.status === "published")?.version;
  const curl = `curl -X POST http://127.0.0.1:8088/v1/gateway/${encodeURIComponent(flow.flow_id)} \\
  -H "Authorization: Bearer <biz_key>" \\
  -H "Content-Type: application/json" \\
  -d '{"inputs": {"task": "..."}, "wait_ms": 60000}'`;
  return (
    <div>
      <Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t("flows.productionHint")}
      </Paragraph>
      <pre
        style={{
          background: "#f5f5f5",
          padding: 12,
          borderRadius: 6,
          fontSize: 11,
          overflow: "auto",
        }}
      >
        {curl}
      </pre>
      <Paragraph type="secondary" style={{ fontSize: 12, marginTop: 8 }}>
        {published
          ? `${t("flows.productionPublished")} ${published}`
          : t("flows.productionNoRoute")}
      </Paragraph>
    </div>
  );
}

function runStatusColor(status: string): string {
  switch (status) {
    case "completed":
      return "green";
    case "running":
      return "blue";
    case "pending":
      return "default";
    case "canceled":
      return "orange";
    case "error":
      return "red";
    default:
      return "default";
  }
}
