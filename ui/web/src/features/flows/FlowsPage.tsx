import { useMemo, useState } from "react";
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
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  RocketOutlined,
  SaveOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import {
  cancelFlowRun,
  createFlow,
  createFlowRun,
  flowRunEvents,
  listFlowRuns,
  listFlowVersions,
  listFlows,
  publishFlow,
  type FlowDefinition,
  type FlowRunView,
  type FlowSummaryView,
  type FlowVersionView,
} from "../../lib/api";
import { FlowGramCanvas } from "./FlowGramCanvas";
import { FlowGramEditor } from "./FlowGramEditor";
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

  const flowsQuery = useQuery({
    queryKey: ["flows"],
    queryFn: () => listFlows(token),
  });

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
}) {
  const { flow, token, t, message, onRefresh, onPublish, onRun, onOpenDetail } = props;

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
          <Button size="small" icon={<PlayCircleOutlined />} disabled={!editorDef} onClick={() => editorDef && onRun(editorDef.version)}>
            {t("flows.run")}
          </Button>
          <Button size="small" onClick={onOpenDetail}>
            {t("flows.detail")}
          </Button>
        </Space>
      </Space>

      <div style={{ flex: 1, minHeight: 420 }}>
        <FlowGramEditor
          flowId={flow.flow_id}
          token={token}
          t={t}
          versions={versions}
          onSaved={() => {
            message.success(t("flows.templateApplied"));
            onRefresh();
          }}
          onSaveError={(err) => showError(message, err, t("flows.saveFailed"))}
        />
      </div>
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
}) {
  const { flow, token, t, onClose, onRefresh, onPublish, onRun, onCancelRun } = props;

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
                      <Button
                        size="small"
                        danger
                        disabled={row.status === "canceled" || row.status === "completed" || row.status === "error"}
                        onClick={() => onCancelRun(row.run_id)}
                      >
                        {t("flows.cancel")}
                      </Button>
                    ),
                  },
                ]}
              />
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
            key: "json",
            label: t("flows.definition"),
            children: (
              <div>
                {versions.length > 0 && versions[0].definition ? (
                  <pre
                    style={{
                      background: "#f5f5f5",
                      padding: 12,
                      borderRadius: 6,
                      fontSize: 11,
                      maxHeight: 480,
                      overflow: "auto",
                    }}
                  >
                    {JSON.stringify(versions[0].definition, null, 2)}
                  </pre>
                ) : (
                  <Empty description={t("flows.noDefinition")} />
                )}
                <Space style={{ marginTop: 12 }}>
                  <Button icon={<ReloadOutlined />} onClick={onRefresh}>
                    {t("flows.refresh")}
                  </Button>
                </Space>
              </div>
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
