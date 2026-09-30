import { useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { App as AntApp, Alert, Button, Card, Drawer, Empty, Form, Input, Popconfirm, Select, Space, Tag, Tooltip, Typography } from "antd";
import { DeleteOutlined, DownOutlined, MenuFoldOutlined, MenuUnfoldOutlined, PlayCircleOutlined, PlusOutlined, ReloadOutlined, RightOutlined, SaveOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import { cancelFlowRun, createFlow, createFlowRun, deleteFlow, inspectFlows, listFlows, publishFlow, type FlowDefinition, type FlowSummaryView } from "../../lib/api";
import { type FlowGramFlowEditorHandle } from "./FlowGramFlowEditor";
import { FLOW_TEMPLATES, flowTemplateById } from "./flowTemplates";
import { useSettingsStore } from "../../store/settings";
import { FlowCanvasMain } from "./FlowCanvasMain";
import { FlowDetailColumn } from "./FlowDetailColumn";
import { formatTime } from "./flowPageUtils";


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
