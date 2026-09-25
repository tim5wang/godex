import { useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type RefObject } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  App as AntApp,
  Alert,
  Button,
  Card,
  Checkbox,
  Collapse,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Segmented,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  ApartmentOutlined,
  ApiOutlined,
  BugOutlined,
  CloseOutlined,
  DeleteOutlined,
  DownOutlined,
  DownloadOutlined,
  EyeOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  RightOutlined,
  RocketOutlined,
  SaveOutlined,
  StepForwardOutlined,
  UploadOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import { CodeViewer } from "../../components/CodeViewer";
import CodeEditor from "../files/CodeEditor";
import {
  cancelFlowRun,
  createBizKey,
  createFlow,
  createFlowRun,
  deleteFlow,
  deleteFlowVersion,
  diagnoseFlowRun,
  flowRunEvents,
  getFlowRun,
  inspectFlows,
  listBizKeys,
  listFlowRuns,
  listFlowVersions,
  listFlows,
  publishFlow,
  revealBizKey,
  replyFlowRunHuman,
  stepFlowRun,
  streamFlowRunEvents,
  type FlowDefinition,
  type FlowDiagnosis,
  type FlowInspectionReport,
  type FlowNetworkPolicy,
  type FlowRunEvent,
  type FlowRunView,
  type NodeStepView,
  type StepFlowView,
  type FlowSummaryView,
  type FlowVersionView,
} from "../../lib/api";
import { FlowGramFlowEditor, type FlowGramFlowEditorHandle } from "./FlowGramFlowEditor";
import { FLOW_TEMPLATES, flowTemplateById } from "./flowTemplates";
import { TemplateLibrary } from "./TemplateLibrary";
import { FlowChatPanel } from "./FlowChatPanel";
import { SchemaTreeEditor, type SchemaNode } from "./SchemaTreeEditor";
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
  const [lastCreatedId, setLastCreatedId] = useState<string>();
  const [listCollapsed, setListCollapsed] = useState(false);
  const [detailCollapsed, setDetailCollapsed] = useState(false);
  const [listWidth, setListWidth] = useState(300);
  const [detailWidth, setDetailWidth] = useState(460);
  const [form] = Form.useForm<FlowFormValues>();

  // Left/right rail drag-resize (same pointer pattern as chat-v2 rails).
  const beginListResize = (event: ReactPointerEvent<HTMLElement>) => {
    if (event.button !== 0) return;
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = listWidth;
    const onPointerMove = (moveEvent: PointerEvent) => {
      setListWidth(Math.min(600, Math.max(200, startWidth + (moveEvent.clientX - startX))));
    };
    const stopResize = () => {
      document.body.classList.remove("is-resizing-column");
      document.removeEventListener("pointermove", onPointerMove);
      document.removeEventListener("pointerup", stopResize);
      document.removeEventListener("pointercancel", stopResize);
    };
    document.body.classList.add("is-resizing-column");
    document.addEventListener("pointermove", onPointerMove);
    document.addEventListener("pointerup", stopResize);
    document.addEventListener("pointercancel", stopResize);
  };
  const beginDetailResize = (event: ReactPointerEvent<HTMLElement>) => {
    if (event.button !== 0) return;
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = detailWidth;
    const onPointerMove = (moveEvent: PointerEvent) => {
      setDetailWidth(Math.min(900, Math.max(320, startWidth - (moveEvent.clientX - startX))));
    };
    const stopResize = () => {
      document.body.classList.remove("is-resizing-column");
      document.removeEventListener("pointermove", onPointerMove);
      document.removeEventListener("pointerup", stopResize);
      document.removeEventListener("pointercancel", stopResize);
    };
    document.body.classList.add("is-resizing-column");
    document.addEventListener("pointermove", onPointerMove);
    document.addEventListener("pointerup", stopResize);
    document.addEventListener("pointercancel", stopResize);
  };

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
    onSuccess: (_data, values) => {
      message.success(t("flows.created"));
      setLastCreatedId(values.flow_id);
      setCreateOpen(false);
      form.resetFields();
      refresh();
    },
    onError: (err) => showError(message, err, t("flows.saveFailed")),
  });

  const publishMutation = useMutation({
    mutationFn: ({ flowId, version }: { flowId: string; version: string }) => publishFlow(token, flowId, version),
    onSuccess: (_data, { version }) => {
      message.success(t("flows.publishedVersion", { v: version }));
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

  // 删除整个 Flow（C2）：有运行中的流程后端拒绝（409）。删除后清空当前选中
  // 与右侧详情列状态。
  const deleteFlowMutation = useMutation({
    mutationFn: ({ flowId }: { flowId: string }) => deleteFlow(token, flowId),
    onSuccess: (_data, { flowId }) => {
      message.success(t("flows.flowDeleted", { id: flowId }));
      if (detail?.flow_id === flowId) setDetail(null);
      refresh();
    },
    onError: (err) => showError(message, err, t("flows.deleteFailed")),
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
    <div style={{ padding: 16, height: "100vh", display: "flex", flexDirection: "column", overflow: "hidden" }}>
      <Space style={{ marginBottom: 10, justifyContent: "space-between", width: "100%" }} align="center">
        <Space align="center" size={8}>
          <Title level={5} style={{ margin: 0 }}>
            {t("flows.pageTitle")}
          </Title>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {t("flows.pageSubtitle")}
          </Text>
        </Space>
        <Space size={6}>
          <Button size="small" icon={<ReloadOutlined />} onClick={refresh}>
            {t("flows.refresh")}
          </Button>
          <Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
            {t("flows.create")}
          </Button>
        </Space>
      </Space>

      {inspection && inspection.total > 0 && (
        <div
          onClick={() => setInspectionOpen(!inspectionOpen)}
          style={{
            border: "1px solid #e5e5e5",
            borderRadius: 6,
            padding: "4px 10px",
            marginBottom: 8,
            background: inspection.failed > 0 ? "#fff2f0" : "#f6ffed",
            cursor: "pointer",
            display: "flex",
            alignItems: "center",
            flexWrap: "wrap",
            gap: 12,
            flexShrink: 0,
          }}
        >
          <Space size={6} align="center">
            <Text strong style={{ fontSize: 12 }}>
              {t("flows.inspectionPanel")}
            </Text>
            <Tag color={inspection.failed > 0 ? "red" : "green"} style={{ marginRight: 0 }}>
              {t("flows.inspectionWindow", { hours: inspection.window_hours })}
            </Tag>
          </Space>
          <Space size={12} style={{ flex: 1 }} wrap>
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
          <Text type="secondary" style={{ fontSize: 11 }}>
            {formatTime(inspection.generated_at)} {inspectionOpen ? <DownOutlined /> : <RightOutlined />}
          </Text>
          {inspectionOpen && (
            <div style={{ borderTop: "1px solid #eee", paddingTop: 6, width: "100%" }}>
              {inspection.flows.map((f) => (
                <div
                  key={f.flow_id}
                  onClick={(e) => {
                    e.stopPropagation();
                    const target = (flowsQuery.data ?? []).find((x) => x.flow_id === f.flow_id);
                    if (target) setDetail(target);
                  }}
                  style={{
                    display: "flex",
                    gap: 12,
                    fontSize: 11,
                    padding: "4px 0",
                    cursor: "pointer",
                    borderRadius: 4,
                  }}
                  onMouseEnter={(e) => (e.currentTarget.style.background = "#f0f5ff")}
                  onMouseLeave={(e) => (e.currentTarget.style.background = "transparent")}
                >
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

      <div style={{ display: "flex", gap: 12, flex: 1, minHeight: 0 }}>
        {/* Left: flow list — click to select; collapsible + drag-resizable. */}
        {listCollapsed ? (
          <div
            style={{
              width: 36,
              flexShrink: 0,
              border: "1px solid #e5e5e5",
              borderRadius: 8,
              background: "#fff",
              display: "flex",
              flexDirection: "column",
              alignItems: "center",
              padding: "8px 0",
            }}
          >
            <Tooltip title={t("flows.expandList")}>
              <Button size="small" type="text" icon={<MenuUnfoldOutlined />} onClick={() => setListCollapsed(false)} />
            </Tooltip>
          </div>
        ) : (
          <div style={{ width: listWidth, flexShrink: 0, display: "flex", flexDirection: "column", border: "1px solid #e5e5e5", borderRadius: 8, overflow: "hidden", background: "#fff", position: "relative" }}>
            <div
              onPointerDown={beginListResize}
              role="separator"
              aria-label="Resize list panel"
              style={{
                position: "absolute",
                top: 0,
                right: 0,
                width: 4,
                height: "100%",
                cursor: "col-resize",
                zIndex: 10,
                background: "transparent",
              }}
            />
            <div
              style={{
                display: "flex",
                justifyContent: "space-between",
                alignItems: "center",
                padding: "6px 10px",
                borderBottom: "1px solid #f0f0f0",
                flexShrink: 0,
              }}
            >
              <Text strong style={{ fontSize: 12 }}>
                {t("flows.listTitle")} ({(flowsQuery.data ?? []).length})
              </Text>
              <Tooltip title={t("flows.collapseList")}>
                <Button size="small" type="text" icon={<MenuFoldOutlined />} onClick={() => setListCollapsed(true)} />
              </Tooltip>
            </div>
            <div style={{ flex: 1, overflowY: "auto" }}>
          {flowsQuery.isLoading ? (
            <Card loading style={{ height: "100%" }} />
          ) : (flowsQuery.data ?? []).length === 0 ? (
            <div style={{ padding: 24, textAlign: "center" }}>
              <Empty description={t("flows.empty")} style={{ marginBottom: 12 }} />
              <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
                {t("flows.create")}
              </Button>
            </div>
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
                  <Space size={2}>
                    <Tooltip
                      title={f.published ? t("flows.runTooltip", { v: f.published }) : t("flows.runUnpublished")}
                    >
                      <Button
                        size="small"
                        icon={<PlayCircleOutlined />}
                        disabled={!f.published}
                        onClick={(e) => {
                          e.stopPropagation();
                          runMutation.mutate({ flowId: f.flow_id, version: f.published ?? "" });
                        }}
                      />
                    </Tooltip>
                    <Popconfirm
                      title={t("flows.deleteFlowConfirm", { id: f.flow_id })}
                      okText={t("flows.delete")}
                      okButtonProps={{ danger: true }}
                      onConfirm={(e) => {
                        e?.stopPropagation?.();
                        deleteFlowMutation.mutate({ flowId: f.flow_id });
                      }}
                    >
                      <Button
                        size="small"
                        danger
                        icon={<DeleteOutlined />}
                        loading={deleteFlowMutation.isPending}
                        onClick={(e) => e.stopPropagation()}
                      />
                    </Popconfirm>
                  </Space>
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
          </div>
        )}

        {/* Center: canvas editor as the main body. */}
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
              onOpenDetail={() => setDetailCollapsed((v) => !v)}
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

        {/* Right: inline detail column (replaces the old floating Drawer). */}
        {detail && !detailCollapsed && (
          <div
            style={{
              width: detailWidth,
              flexShrink: 0,
              display: "flex",
              flexDirection: "column",
              border: "1px solid #e5e5e5",
              borderRadius: 8,
              overflow: "hidden",
              background: "#fff",
              position: "relative",
              minHeight: 0,
            }}
          >
            <div
              onPointerDown={beginDetailResize}
              role="separator"
              aria-label="Resize detail panel"
              style={{
                position: "absolute",
                top: 0,
                left: 0,
                width: 4,
                height: "100%",
                cursor: "col-resize",
                zIndex: 10,
                background: "transparent",
              }}
            />
            <FlowDetailColumn
              flow={detail}
              token={token}
              t={t}
              message={message}
              onClose={() => setDetailCollapsed(true)}
              onRefresh={refresh}
              onPublish={(version) => publishMutation.mutate({ flowId: detail.flow_id, version })}
              onRun={(version) => runMutation.mutate({ flowId: detail.flow_id, version })}
              onCancelRun={(runId) => cancelRunMutation.mutate({ flowId: detail.flow_id, runId })}
              onApplyJson={(def) => setExternalDef({ def, stamp: Date.now() })}
              editorHandleRef={editorHandleRef}
            />
          </div>
        )}
      </div>

      {/* Create flow drawer */}
      <Drawer
        title={t("flows.create")}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        width={640}
        destroyOnClose
      >
        {lastCreatedId && (
          <Alert
            type="success"
            showIcon
            style={{ marginBottom: 12 }}
            message={t("flows.lastCreated", { id: lastCreatedId })}
          />
        )}
        <Form form={form} layout="vertical" onFinish={(v) => createMutation.mutate(v)}>
          <Paragraph type="secondary" style={{ fontSize: 12 }}>
            {t("flows.createHint")}
          </Paragraph>
          <Form.Item
            name="flow_id"
            label={t("flows.id")}
            rules={[
              { required: true, message: t("flows.idRequired") },
              {
                pattern: /^[a-z][a-z0-9_]*$/,
                message: t("flows.idPattern"),
              },
            ]}
          >
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

  const queryClient = useQueryClient();

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
  // Step mode (单步运行): the run is created WITHOUT auto-start (step_mode=1)
  // and the user advances it node-by-node via POST /step; each step returns
  // the per-node outputs/context so the debug panel shows live variable values.
  const [debugMode, setDebugMode] = useState(false);
  const [debugVersion, setDebugVersion] = useState<string>();
  // 表单模式：声明字段值（key=name）+ 自定义字段（原始字符串，提交时解析）
  const [debugForm, setDebugForm] = useState<Record<string, unknown>>({});
  const [debugCustomFields, setDebugCustomFields] = useState<{ name: string; raw: string }[]>([]);
  // JSON 模式：整块文本兑底（手工编辑复杂结构时用）
  const [debugJsonMode, setDebugJsonMode] = useState(false);
  const [debugJsonText, setDebugJsonText] = useState("{}");
  const [debugRunId, setDebugRunId] = useState<string>();
  const [debugStarted, setDebugStarted] = useState(false);
  const [debugStepMode, setDebugStepMode] = useState(false);
  const [stepView, setStepView] = useState<StepFlowView | null>(null);
  const [stepBusy, setStepBusy] = useState(false);
  // 单步调试中 waiting_human 节点的回复值（提交后推进到下一步）。
  const [humanReplyValue, setHumanReplyValue] = useState<Record<string, string>>({});
  const [humanReplyBusy, setHumanReplyBusy] = useState(false);

  // 调试所用版本的定义（表单字段按它的 inputs 声明生成）。
  const debugDef = useMemo(() => {
    const pick = debugVersion ?? [...versions].reverse().find((v) => v.definition)?.version;
    return versions.find((v) => v.version === pick)?.definition;
  }, [versions, debugVersion]);

  const debugMutation = useMutation({
    mutationFn: ({ version, inputs }: { version: string; inputs: Record<string, unknown> }) =>
      createFlowRun(token, flow.flow_id, { version, inputs, step_mode: debugStepMode }),
    onSuccess: (run) => {
      setDebugRunId(run.run_id);
      setDebugStarted(true);
      message.success(`${t("flows.debugStarted")} ${run.run_id}`);
    },
    onError: (err) => showError(message, err, t("flows.runFailed")),
  });

  // Single-step: advance the debug run by one node and refresh per-node state.
  const stepOnce = async () => {
    if (!debugRunId || !debugStepMode) return;
    setStepBusy(true);
    try {
      const view = await stepFlowRun(token, debugRunId, flow.flow_id);
      setStepView(view);
      // Terminal: stop the SSE stream and refresh the run record.
      if (view.terminal) setDebugStarted(false);
    } catch (err) {
      showError(message, err, t("flows.stepFailed"));
    } finally {
      setStepBusy(false);
    }
  };

  // Submit a value for a waiting_human node (单步调试中人工节点完成并推进).
  const replyHuman = async (nodeId: string) => {
    if (!debugRunId) return;
    const value = humanReplyValue[nodeId] ?? "";
    if (value.trim() === "") {
      message.warning(t("flows.humanReplyEmpty"));
      return;
    }
    setHumanReplyBusy(true);
    try {
      const view = await replyFlowRunHuman(token, debugRunId, flow.flow_id, nodeId, value.trim());
      // 回复完成节点后刷新 step 视图（后续节点变为 ready）。
      if (debugStepMode) {
        const next = await stepFlowRun(token, debugRunId, flow.flow_id);
        setStepView(next);
        if (next.terminal) setDebugStarted(false);
      } else {
        void queryClient.invalidateQueries({ queryKey: ["flow-run", flow.flow_id, debugRunId] });
      }
      message.success(`${t("flows.humanReplied")} ${nodeId}`);
    } catch (err) {
      showError(message, err, t("flows.humanReplyFailed"));
    } finally {
      setHumanReplyBusy(false);
    }
  };

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
    if (debugJsonMode) {
      // JSON 模式：整块文本解析（兑底）。
      try {
        inputs = debugJsonText.trim() ? (JSON.parse(debugJsonText) as Record<string, unknown>) : {};
      } catch (err) {
        message.error(
          `${t("flows.jsonInvalid")}: ${err instanceof Error ? err.message : String(err)}`,
        );
        return;
      }
    } else {
      inputs = mergeDebugInputs();
    }
    debugMutation.mutate({ version, inputs });
  };

  // mergeDebugInputs 把表单模式的声明字段 + 自定义字段合并为提交值：
  // 声明字段按 type 已转换（number/boolean 由控件直出）；object/array/any 与
  // 自定义字段若是字符串则尝试 JSON.parse（失败保留字面量）。
  const mergeDebugInputs = (): Record<string, unknown> => {
    const merged: Record<string, unknown> = { ...debugForm };
    for (const inp of debugDef?.inputs ?? []) {
      const t = (inp.type ?? "string").toLowerCase();
      if ((t === "object" || t === "array" || t === "any") && typeof merged[inp.name] === "string") {
        const s = (merged[inp.name] as string).trim();
        if (s !== "") {
          try {
            merged[inp.name] = JSON.parse(s);
          } catch {
            // 保留字符串字面量
          }
        }
      }
    }
    for (const f of debugCustomFields) {
      const name = f.name.trim();
      if (!name) continue;
      const raw = f.raw.trim();
      if (raw === "") continue;
      try {
        merged[name] = JSON.parse(raw);
      } catch {
        merged[name] = raw;
      }
    }
    return merged;
  };

  const switchDebugMode = (json: boolean) => {
    setDebugJsonMode(json);
    if (json) {
      // 切到 JSON 模式时把当前表单值序列化进去，方便继续微调。
      try {
        setDebugJsonText(JSON.stringify(mergeDebugInputs(), null, 2));
      } catch {
        // 保留原文本
      }
    }
  };

  const clearDebug = () => {
    setDebugRunId(undefined);
    setDebugStarted(false);
  };

  const debugRun = debugRunQuery.data;
  const debugStatus = debugRun?.status ?? (debugStarted ? "running" : undefined);

  // Aggregate run context for the debug panel: the exact shape a function
  // node handler sees as ctx = { inputs, outputs } at the current step point —
  // inputs from the run + completed nodes' outputs keyed by node id. This is
  // the "执行到某个节点时的上下文变量值" the user asked for in step mode.
  const stepCtx = useMemo(() => {
    if (!stepView) return null;
    const outputs: Record<string, unknown> = {};
    for (const n of stepView.nodes) {
      if (n.status === "completed" && n.outputs && Object.keys(n.outputs).length > 0) {
        outputs[n.id] = n.outputs;
      }
    }
    return { inputs: debugRun?.inputs ?? {}, outputs };
  }, [stepView, debugRun]);

  // 模板库（B3）：从抽屉移到主界面 — 工具栏「模板库」按钮打开 Modal，
  // 选模板一键应用为新版本（不改动画布内容）。
  const [tplOpen, setTplOpen] = useState(false);

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
          <Button size="small" icon={<ApartmentOutlined />} onClick={() => setTplOpen(true)}>
            {t("flows.templates")}
          </Button>
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

      <div style={{ flex: 1, minHeight: 420, display: "flex", gap: 8, minWidth: 0 }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <FlowGramFlowEditor
            ref={editorHandleRef}
            flowId={flow.flow_id}
            token={token}
            t={t}
            versions={versions}
            externalDef={externalDef}
            runEvents={debugEvents}
            onSaved={(savedVersion) => {
              message.success(t("flows.savedVersion", { v: savedVersion }));
              onExternalDefConsumed();
              onRefresh();
            }}
            onSaveError={(err) => showError(message, err, t("flows.saveFailed"))}
          />
        </div>

        {debugMode && (
          <div
            style={{
              width: 360,
              flexShrink: 0,
              border: "1px solid #e5e5e5",
              borderRadius: 8,
              padding: 10,
              background: "#fafafa",
              display: "flex",
              flexDirection: "column",
              gap: 8,
              overflowY: "auto",
              maxHeight: "100%",
            }}
          >
            <Space wrap align="center">
              <Text strong style={{ fontSize: 12 }}>
                {t("flows.debugPanel")}
              </Text>
              <Select
                size="small"
                style={{ width: 140 }}
                placeholder={t("flows.version")}
                value={debugVersion}
                onChange={setDebugVersion}
                options={versions.map((v) => ({ value: v.version, label: `${v.version} (${v.status})` }))}
              />
            </Space>
            <Space align="center" style={{ justifyContent: "space-between", width: "100%" }}>
              <Segmented
                size="small"
                value={debugJsonMode ? "json" : "form"}
                options={[
                  { label: t("flows.debugInputFormMode"), value: "form" },
                  { label: t("flows.debugInputJsonMode"), value: "json" },
                ]}
                onChange={(v) => switchDebugMode(v === "json")}
              />
              <Text type="secondary" style={{ fontSize: 10 }}>
                {t("flows.debugInputTitle")}
              </Text>
            </Space>
            {debugJsonMode ? (
              <Input.TextArea
                size="small"
                style={{ width: "100%", fontFamily: "monospace", fontSize: 11 }}
                rows={4}
                placeholder='{"task": "..."}'
                value={debugJsonText}
                onChange={(e) => setDebugJsonText(e.target.value)}
              />
            ) : (
              <div
                style={{
                  display: "flex",
                  flexDirection: "column",
                  gap: 4,
                  maxHeight: 220,
                  overflowY: "auto",
                  border: "1px solid #f0f0f0",
                  borderRadius: 6,
                  padding: 6,
                  background: "#fff",
                }}
              >
                {(debugDef?.inputs ?? []).length === 0 && debugCustomFields.length === 0 && (
                  <Text type="secondary" style={{ fontSize: 11 }}>
                    {t("flows.debugInputEmpty")}
                  </Text>
                )}
                {(debugDef?.inputs ?? []).map((inp) => {
                  const key = inp.name;
                  const value = debugForm[key];
                  const type = (inp.type ?? "string").toLowerCase();
                  return (
                    <div key={key} style={{ display: "flex", gap: 6, alignItems: "center" }}>
                      <Text style={{ fontSize: 11, width: 96, flexShrink: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={inp.desc ?? inp.name}>
                        {key}
                      </Text>
                      {type === "number" ? (
                        <InputNumber
                          size="small"
                          style={{ width: "100%" }}
                          placeholder={inp.desc ?? "number"}
                          value={typeof value === "number" ? value : undefined}
                          onChange={(v) => setDebugForm((prev) => ({ ...prev, [key]: v }))}
                        />
                      ) : type === "boolean" ? (
                        <Switch
                          size="small"
                          checked={value === true}
                          onChange={(v) => setDebugForm((prev) => ({ ...prev, [key]: v }))}
                        />
                      ) : type === "object" || type === "array" || type === "any" ? (
                        <Input
                          size="small"
                          style={{ width: "100%", fontFamily: "monospace", fontSize: 11 }}
                          placeholder={inp.desc ?? (type === "array" ? '[{"k": 1}]' : '{"k": 1}')}
                          value={typeof value === "string" ? value : value === undefined ? "" : JSON.stringify(value)}
                          onChange={(e) => setDebugForm((prev) => ({ ...prev, [key]: e.target.value }))}
                        />
                      ) : (
                        <Input
                          size="small"
                          style={{ width: "100%" }}
                          placeholder={inp.desc ?? "string"}
                          value={typeof value === "string" ? value : value === undefined ? "" : String(value)}
                          onChange={(e) => setDebugForm((prev) => ({ ...prev, [key]: e.target.value }))}
                        />
                      )}
                    </div>
                  );
                })}
                {debugCustomFields.map((f, idx) => (
                  <div key={idx} style={{ display: "flex", gap: 6, alignItems: "center" }}>
                    <Input
                      size="small"
                      style={{ width: 96, flexShrink: 0, fontSize: 11 }}
                      placeholder={t("flows.debugInputName")}
                      value={f.name}
                      onChange={(e) =>
                        setDebugCustomFields((prev) =>
                          prev.map((x, i) => (i === idx ? { ...x, name: e.target.value } : x)),
                        )
                      }
                    />
                    <Input
                      size="small"
                      style={{ width: "100%", fontSize: 11 }}
                      placeholder={t("flows.debugInputPlaceholder")}
                      value={f.raw}
                      onChange={(e) =>
                        setDebugCustomFields((prev) =>
                          prev.map((x, i) => (i === idx ? { ...x, raw: e.target.value } : x)),
                        )
                      }
                    />
                    <Button
                      size="small"
                      type="text"
                      icon={<CloseOutlined />}
                      onClick={() =>
                        setDebugCustomFields((prev) => prev.filter((_, i) => i !== idx))
                      }
                    />
                  </div>
                ))}
                <Button
                  size="small"
                  type="dashed"
                  icon={<PlusOutlined />}
                  style={{ fontSize: 11 }}
                  onClick={() => setDebugCustomFields((prev) => [...prev, { name: "", raw: "" }])}
                >
                  {t("flows.debugInputAdd")}
                </Button>
              </div>
            )}
            <Space wrap>
              <Checkbox
                checked={debugStepMode}
                onChange={(e) => setDebugStepMode(e.target.checked)}
              >
                <Text style={{ fontSize: 12 }}>{t("flows.debugStepMode")}</Text>
              </Checkbox>
              <Button
                size="small"
                type="primary"
                icon={<PlayCircleOutlined />}
                loading={debugMutation.isPending}
                onClick={startDebug}
              >
                {t("flows.debugStart")}
              </Button>
            </Space>
            {debugRunId && (
              <Space wrap>
                <Tag color={debugStatus === "completed" ? "green" : debugStatus === "error" ? "red" : "processing"}>
                  {debugRunId.slice(0, 12)}… {debugStatus ?? "running"}
                </Tag>
                {debugStepMode && !stepView?.terminal && (
                  <Button
                    size="small"
                    type="primary"
                    icon={<StepForwardOutlined />}
                    loading={stepBusy}
                    onClick={stepOnce}
                  >
                    {t("flows.stepOnce")}
                  </Button>
                )}
                <Button size="small" onClick={clearDebug}>
                  {t("flows.debugClear")}
                </Button>
              </Space>
            )}
            <Paragraph type="secondary" style={{ fontSize: 11, marginBottom: 0 }}>
              {t("flows.debugHint")}
              {debugRunId && ` · ${t("flows.debugStatus", { status: debugStatus ?? "running" })}`}
            </Paragraph>

            {debugStepMode && stepView && stepCtx && (
              <div
                style={{
                  border: "1px solid #d9d9d9",
                  borderRadius: 6,
                  background: "#fffbe6",
                  padding: 6,
                  maxHeight: 180,
                  overflowY: "auto",
                }}
              >
                <Text strong style={{ fontSize: 11 }}>
                  {t("flows.stepCtxTitle")}
                </Text>
                <pre
                  style={{
                    margin: "4px 0 0 0",
                    fontSize: 10,
                    whiteSpace: "pre-wrap",
                    wordBreak: "break-all",
                    fontFamily: "monospace",
                  }}
                >
                  {JSON.stringify(stepCtx, null, 2)}
                </pre>
                <Text type="secondary" style={{ fontSize: 10 }}>
                  {t("flows.stepCtxHint")}
                </Text>
              </div>
            )}

            {debugStepMode && stepView && (
              <div style={{ border: "1px solid #eee", borderRadius: 6, background: "#fff", padding: 6 }}>
                <Text type="secondary" style={{ fontSize: 11 }}>
                  {t("flows.stepNodeContext")}
                </Text>
                <div style={{ marginTop: 4, display: "flex", flexDirection: "column", gap: 4 }}>
                  {stepView.nodes.map((n) => (
                    <div
                      key={n.id}
                      style={{
                        border: "1px solid #f0f0f0",
                        borderRadius: 4,
                        padding: "4px 6px",
                        background: n.status === "completed" ? "#f6ffed" : n.status === "error" ? "#fff2f0" : "#fafafa",
                      }}
                    >
                      <Space size={6} style={{ width: "100%", justifyContent: "space-between" }}>
                        <Space size={4} style={{ minWidth: 0 }}>
                          <Text style={{ fontSize: 11, fontFamily: "monospace" }} strong>
                            {n.id}
                          </Text>
                          <Tag style={{ marginRight: 0, fontSize: 10, color: "#555", background: "#f0f0f0", border: "none" }}>
                            {n.kind || "?"}
                          </Tag>
                        </Space>
                        <Tag style={{ marginRight: 0, fontSize: 10 }}>{n.status}</Tag>
                      </Space>
                      {n.error && (
                        <Text type="danger" style={{ fontSize: 10, display: "block" }}>
                          {n.error}
                        </Text>
                      )}
                      {n.outputs && Object.keys(n.outputs).length > 0 && (
                        <div style={{ marginTop: 2 }}>
                          <pre
                            style={{
                              margin: 0,
                              fontSize: 10,
                              whiteSpace: "pre-wrap",
                              wordBreak: "break-all",
                              maxHeight: 90,
                              overflowY: "auto",
                            }}
                          >
                            {JSON.stringify(n.outputs, null, 2)}
                          </pre>
                        </div>
                      )}
                      {n.status === "waiting_human" && (
                        <Space.Compact style={{ width: "100%", marginTop: 4 }}>
                          <Input
                            size="small"
                            placeholder={t("flows.humanReplyPlaceholder")}
                            value={humanReplyValue[n.id] ?? ""}
                            onChange={(e) =>
                              setHumanReplyValue((prev) => ({ ...prev, [n.id]: e.target.value }))
                            }
                            onPressEnter={() => replyHuman(n.id)}
                          />
                          <Button
                            size="small"
                            type="primary"
                            loading={humanReplyBusy}
                            onClick={() => replyHuman(n.id)}
                          >
                            {t("flows.humanReplySubmit")}
                          </Button>
                        </Space.Compact>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            )}

            {debugRunId && debugEvents.length > 0 && (
              <div
                style={{
                  border: "1px solid #eee",
                  borderRadius: 6,
                  background: "#fff",
                  maxHeight: 160,
                  overflowY: "auto",
                  padding: 6,
                  fontFamily: "monospace",
                  fontSize: 11,
                }}
              >
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    alignItems: "center",
                    marginBottom: 4,
                  }}
                >
                  <Text type="secondary" style={{ fontSize: 11 }}>
                    {t("flows.debugEvents")}
                  </Text>
                  <Button
                    size="small"
                    type="text"
                    style={{ fontSize: 11, height: 20, padding: "0 4px" }}
                    onClick={() => setDebugEvents([])}
                  >
                    {t("flows.debugClearEvents")}
                  </Button>
                </div>
                {debugEvents.map((ev, i) => {
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
            )}
          </div>
        )}
      </div>

      {/* 模板库（B3）：主界面 Modal，选模板一键应用为新版本 */}
      <Modal
        title={t("flows.templates")}
        open={tplOpen}
        onCancel={() => setTplOpen(false)}
        footer={null}
        width={680}
      >
        <TemplateLibrary
          flowId={flow.flow_id}
          token={token}
          t={t}
          versions={versions}
          onApplied={() => {
            onRefresh();
            setTplOpen(false);
          }}
        />
      </Modal>
    </div>
  );
}

function FlowDetailColumn(props: {
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
                onVersionApplied={onRefresh}
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

/**
 * VariableScopePanel — renders the Flow Spec variable scope chain (§9 变量
 * 面板): flow-level inputs/outputs + each node's typed outputs, with the
 * nodes that reference each variable ({{inputs.<name>}} /
 * {{nodes.<id>.outputs.<field>}} in prompts and edge conditions).
 */
function VariableScopePanel(props: {
  def: FlowDefinition;
  token: string | null;
  flowId: string;
  t: (k: string, v?: Record<string, string | number>) => string;
  message: ReturnType<typeof AntApp.useApp>["message"];
  onRefresh: () => void;
  nextVersion: string;
}) {
  const { def, t, token, flowId, message, onRefresh, nextVersion } = props;
  const nodes = def.nodes ?? [];
  const edges = def.edges ?? [];

  // Editable flow-level inputs/outputs (B5) — previously read-only. Node
  // outputs stay read-only (they belong to the node spec on the canvas).
  // C3: type is a dropdown (string/number/boolean/object/array/any) and
  // object/array/any may carry a nested JSON-Schema-ish fragment (schema)
  // edited inline — “像定义 json schema 一样定义变量”。
  type FlowVarDef = { name: string; type?: string; desc?: string; schema?: unknown };
  const VAR_TYPES = ["string", "number", "boolean", "object", "array", "any"];
  const [inputs, setInputs] = useState<FlowVarDef[]>(def.inputs ?? []);
  const [outputs, setOutputs] = useState<FlowVarDef[]>(def.outputs ?? []);
  const [network, setNetwork] = useState<FlowNetworkPolicy | undefined>(def.network);
  const [targetVersion, setTargetVersion] = useState(nextVersion);
  useEffect(() => {
    setInputs(def.inputs ?? []);
    setOutputs(def.outputs ?? []);
    setNetwork(def.network);
  }, [def]);

  const patchVar = (list: FlowVarDef[], setter: (v: FlowVarDef[]) => void, i: number, patch: Partial<FlowVarDef>) =>
    setter(list.map((x, j) => (j === i ? { ...x, ...patch } : x)));

  const saveMutation = useMutation({
    mutationFn: async (v: string) => {
      const updated: FlowDefinition = {
        ...def,
        inputs,
        outputs,
        network,
        version: v,
        status: "draft",
      };
      return createFlow(token, { flow_id: flowId, version: v, status: "draft", definition: updated });
    },
    onSuccess: () => {
      message.success(t("flows.varSaved", { v: targetVersion }));
      onRefresh();
    },
    onError: (err) => showError(message, err, t("flows.saveFailed")),
  });

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

  const editableVarList = (
    list: FlowVarDef[],
    setter: (v: FlowVarDef[]) => void,
    kind: "inputs" | "outputs",
  ) => (
    <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      {list.length === 0 && (
        <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
      )}
      {list.map((v, i) => {
        const nested = v.type === "object" || v.type === "array" || v.type === "any";
        return (
          <div
            key={i}
            style={{
              display: "flex",
              flexDirection: "column",
              gap: 4,
              border: "1px solid #f0f0f0",
              borderRadius: 6,
              padding: 6,
            }}
          >
            <Space size={4} style={{ width: "100%" }}>
              <Input
                size="small"
                style={{ width: 120, fontFamily: "monospace", fontSize: 11 }}
                placeholder={t("flows.varName")}
                value={v.name}
                onChange={(e) => patchVar(list, setter, i, { name: e.target.value })}
              />
              <Select
                size="small"
                style={{ width: 96 }}
                value={v.type ?? "string"}
                onChange={(val) =>
                  patchVar(list, setter, i, {
                    type: val,
                    // Switching away from object/array drops the nested schema.
                    schema: val === "object" || val === "array" || val === "any" ? v.schema : undefined,
                  })
                }
                options={VAR_TYPES.map((tp) => ({ value: tp, label: tp }))}
              />
              <Input
                size="small"
                style={{ flex: 1, minWidth: 0, fontSize: 11 }}
                placeholder={t("flows.varDesc")}
                value={v.desc ?? ""}
                onChange={(e) => patchVar(list, setter, i, { desc: e.target.value })}
              />
              <Button
                size="small"
                type="text"
                danger
                icon={<DeleteOutlined />}
                onClick={() => setter(list.filter((_, j) => j !== i))}
              />
            </Space>
            {nested && (
              <div>
                <Text type="secondary" style={{ fontSize: 10 }}>
                  {t("flows.varSchema")}
                </Text>
                <SchemaTreeEditor
                  value={(v.schema as SchemaNode) ?? undefined}
                  onChange={(next) => patchVar(list, setter, i, { schema: next })}
                />
              </div>
            )}
          </div>
        );
      })}
      <Button
        size="small"
        type="dashed"
        icon={<PlusOutlined />}
        onClick={() => setter([...list, { name: "", type: "string" }])}
      >
        {t("flows.varAdd", { kind })}
      </Button>
    </div>
  );

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
      <div
        style={{
          border: "1px solid #f0f0f0",
          borderRadius: 6,
          padding: "6px 8px",
          background: "#fafafa",
        }}
      >
        <Collapse
          ghost
          size="small"
          items={[
            {
              key: "network",
              label: (
                <Space size={6}>
                  <Text strong style={{ fontSize: 12 }}>
                    {t("flows.networkPolicy")}
                  </Text>
                  <Tag color={network?.policy === "allowlist" ? "orange" : "green"} style={{ fontSize: 10 }}>
                    {network?.policy ?? "allow_all"}
                  </Tag>
                </Space>
              ),
              children: (
                <div style={{ display: "flex", flexDirection: "column", gap: 8, paddingTop: 4 }}>
                  <Space size={6} wrap>
                    <Text style={{ fontSize: 11 }}>策略</Text>
                    <Select
                      size="small"
                      style={{ width: 130 }}
                      value={network?.policy ?? "allow_all"}
                      onChange={(p) => setNetwork({ ...(network ?? {}), policy: p })}
                      options={[
                        { value: "allow_all", label: "allow_all" },
                        { value: "allowlist", label: "allowlist" },
                      ]}
                    />
                    <Text style={{ fontSize: 11 }}>超时(秒)</Text>
                    <InputNumber
                      size="small"
                      style={{ width: 70 }}
                      min={1}
                      max={120}
                      value={network?.timeout_seconds ?? 15}
                      onChange={(v) => setNetwork({ ...(network ?? {}), timeout_seconds: v ?? 15 })}
                    />
                    <Text style={{ fontSize: 11 }}>响应上限(字符)</Text>
                    <InputNumber
                      size="small"
                      style={{ width: 90 }}
                      min={1024}
                      value={network?.max_response_chars ?? 1048576}
                      onChange={(v) => setNetwork({ ...(network ?? {}), max_response_chars: v ?? 1048576 })}
                    />
                  </Space>
                  <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
                    <Text type="secondary" style={{ fontSize: 11 }}>
                      {t("flows.networkAllowed")}（allowlist 时生效，支持 *.suffix）
                    </Text>
                    <Select
                      mode="tags"
                      size="small"
                      style={{ width: "100%" }}
                      placeholder="api.openai.com"
                      value={network?.allowed_domains ?? []}
                      onChange={(v: string[]) => setNetwork({ ...(network ?? {}), allowed_domains: v })}
                      tokenSeparators={[",", " "]}
                    />
                    <Text type="secondary" style={{ fontSize: 11 }}>
                      {t("flows.networkBlocked")}（始终拦截，两种策略都生效）
                    </Text>
                    <Select
                      mode="tags"
                      size="small"
                      style={{ width: "100%" }}
                      placeholder="*.internal.example"
                      value={network?.blocked_domains ?? []}
                      onChange={(v: string[]) => setNetwork({ ...(network ?? {}), blocked_domains: v })}
                      tokenSeparators={[",", " "]}
                    />
                  </div>
                  <Text type="secondary" style={{ fontSize: 10 }}>
                    {t("flows.networkHint")}
                  </Text>
                </div>
              ),
            },
          ]}
        />
      </div>
      <div>
        <Text strong style={{ fontSize: 12 }}>{t("flows.varFlowInputs")}</Text>
        <div style={{ marginTop: 4 }}>{editableVarList(inputs, setInputs, "inputs")}</div>
      </div>
      <div>
        <Text strong style={{ fontSize: 12 }}>{t("flows.varFlowOutputs")}</Text>
        <div style={{ marginTop: 4 }}>{editableVarList(outputs, setOutputs, "outputs")}</div>
      </div>
      <Space wrap>
        <Text type="secondary">{t("flows.version")}</Text>
        <Input
          style={{ width: 100 }}
          value={targetVersion}
          onChange={(e) => setTargetVersion(e.target.value.trim())}
          placeholder="1"
        />
        <Button
          type="primary"
          size="small"
          icon={<SaveOutlined />}
          loading={saveMutation.isPending}
          onClick={() => saveMutation.mutate(targetVersion || "1")}
        >
          {t("flows.save")}
        </Button>
        <Text type="secondary" style={{ fontSize: 11 }}>{t("flows.varSaveHint")}</Text>
      </Space>
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
      <div style={{ border: "1px solid #e5e5e5", borderRadius: 6, overflow: "hidden", height: 420 }}>
        <CodeEditor
          content={jsonText}
          filePath="flow.json"
          onChange={(v) => {
            setJsonText(v);
            setDirty(true);
          }}
        />
      </div>
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
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  versions: FlowVersionView[];
}) {
  const { flow, token, t, versions } = props;
  const { message } = AntApp.useApp();
  const published = versions.find((v) => v.status === "published")?.version;

  // 复用业务 agent 的 BizKey 接入机制（B7）：列出绑定此 flow 的业务 key，
  // 支持一键创建绑定 key + PIN 解锁查看完整密钥，并用真实 key 生成调用示例。
  const keysQuery = useQuery({
    queryKey: ["biz-keys", token],
    enabled: Boolean(token),
    queryFn: () => listBizKeys(token),
  });
  const flowKeys = useMemo(
    () => (keysQuery.data ?? []).filter((k) => k.flow_id === flow.flow_id),
    [keysQuery.data, flow.flow_id],
  );

  const [newSecret, setNewSecret] = useState<string>();
  const createKeyMutation = useMutation({
    mutationFn: () =>
      createBizKey(token, {
        name: `flow-${flow.flow_id}`,
        flow_id: flow.flow_id,
        description: `Business Flow ${flow.flow_id} gateway key`,
      }),
    onSuccess: (res) => {
      setNewSecret(res.secret);
      void keysQuery.refetch();
      message.success(t("flows.productionKeyCreated"));
    },
    onError: (err) => showError(message, err, t("flows.saveFailed")),
  });

  // PIN 解锁完整密钥（与业务 agent 管理页一致）。
  const [revealed, setRevealed] = useState<Record<string, string>>({});
  const [pinInput, setPinInput] = useState<Record<string, string>>({});
  const revealMutation = useMutation({
    mutationFn: ({ id, pin }: { id: string; pin: string }) => revealBizKey(token, id, pin),
    onSuccess: (res, vars) => {
      setRevealed((prev) => ({ ...prev, [vars.id]: res.secret }));
      message.success(t("flows.productionRevealed"));
    },
    onError: (err) => showError(message, err, t("flows.productionRevealFailed")),
  });

  const activeKey = flowKeys[0];
  const curlKey = (activeKey && revealed[activeKey.id]) || activeKey?.key_prefix || "<biz_key>";
  const curl = `curl -X POST http://127.0.0.1:8088/v1/gateway/${encodeURIComponent(flow.flow_id)} \\
  -H "Authorization: Bearer ${curlKey}" \\
  -H "Content-Type: application/json" \\
  -d '{"inputs": {"task": "..."}, "wait_ms": 60000}'`;

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      <Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t("flows.productionHint")}
      </Paragraph>

      {/* 绑定此 flow 的业务 key */}
      <div>
        <Text strong style={{ fontSize: 12 }}>
          {t("flows.productionBoundKeys")}
        </Text>
        {flowKeys.length === 0 ? (
          <Paragraph type="secondary" style={{ fontSize: 11, marginBottom: 4 }}>
            {t("flows.productionNoKey")}
          </Paragraph>
        ) : (
          <div style={{ marginTop: 4, display: "flex", flexDirection: "column", gap: 4 }}>
            {flowKeys.map((k) => (
              <Space key={k.id} size={6} style={{ width: "100%" }} wrap>
                <Tag color="blue">{k.name}</Tag>
                <Text code style={{ fontSize: 11 }}>
                  {k.key_prefix}
                </Text>
                {k.enabled ? <Tag color="green">enabled</Tag> : <Tag color="red">disabled</Tag>}
                {revealed[k.id] ? (
                  <Text code style={{ fontSize: 10 }}>
                    {revealed[k.id]}
                  </Text>
                ) : (
                  <Space size={4}>
                    <Input.Password
                      size="small"
                      style={{ width: 110, fontSize: 11 }}
                      placeholder={t("flows.productionPin")}
                      value={pinInput[k.id] ?? ""}
                      onChange={(e) => setPinInput((prev) => ({ ...prev, [k.id]: e.target.value }))}
                    />
                    <Button
                      size="small"
                      icon={<EyeOutlined />}
                      loading={revealMutation.isPending}
                      onClick={() => revealMutation.mutate({ id: k.id, pin: pinInput[k.id] ?? "" })}
                    >
                      {t("flows.productionReveal")}
                    </Button>
                  </Space>
                )}
              </Space>
            ))}
          </div>
        )}
        <Space style={{ marginTop: 8 }} wrap>
          <Button
            size="small"
            type="primary"
            icon={<PlusOutlined />}
            loading={createKeyMutation.isPending}
            onClick={() => createKeyMutation.mutate()}
          >
            {t("flows.productionCreateKey")}
          </Button>
          {newSecret && (
            <Text code style={{ fontSize: 11 }}>
              {newSecret}
            </Text>
          )}
        </Space>
        {newSecret && (
          <Paragraph type="warning" style={{ fontSize: 11, marginTop: 4, marginBottom: 0 }}>
            {t("flows.productionSecretOnce")}
          </Paragraph>
        )}
      </div>

      {/* 调用示例（真实 key） */}
      <pre
        style={{
          background: "#f5f5f5",
          padding: 12,
          borderRadius: 6,
          fontSize: 11,
          overflow: "auto",
          marginBottom: 0,
        }}
      >
        {curl}
      </pre>
      <Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
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

function versionStatusColor(status: string): string {
  switch (status) {
    case "published":
      return "green";
    case "gray":
      return "orange";
    case "draft":
      return "blue";
    default:
      return "default";
  }
}

/** Unified timestamp display across the flows UI (local date + HH:mm). */
function formatTime(v?: string): string {
  if (!v) return "—";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return "—";
  return `${d.toLocaleDateString()} ${d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}
