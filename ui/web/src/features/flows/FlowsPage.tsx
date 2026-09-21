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
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
} from "antd";
import {
  DeleteOutlined,
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
  listFlowRuns,
  listFlowVersions,
  listFlows,
  publishFlow,
  type FlowDefinition,
  type FlowRunView,
  type FlowSummaryView,
  type FlowVersionView,
} from "../../lib/api";
import { useSettingsStore } from "../../store/settings";

const { Title, Text, Paragraph } = Typography;

/** The JSON editor form: nodes/edges are authored as a Flow Definition. */
type FlowFormValues = {
  flow_id: string;
  version: string;
  name?: string;
  description?: string;
  definition: string; // JSON text
};

export function FlowsPage() {
  const { t } = useI18n();
  const { message } = AntApp.useApp();
  const token = useSettingsStore((state) => state.token);
  const queryClient = useQueryClient();

  const [createOpen, setCreateOpen] = useState(false);
  const [detail, setDetail] = useState<FlowSummaryView | null>(null);
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
      const def = JSON.parse(values.definition) as FlowDefinition;
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

  const columns = useMemo(
    () => [
      {
        title: t("flows.id"),
        dataIndex: "flow_id",
        key: "flow_id",
        render: (id: string) => (
          <a onClick={() => setDetail(flowsQuery.data?.find((f) => f.flow_id === id) ?? null)}>{id}</a>
        ),
      },
      {
        title: t("flows.draft"),
        dataIndex: "draft",
        key: "draft",
        render: (v: string | undefined) => (v ? <Tag color="default">{v}</Tag> : <Text type="secondary">—</Text>),
      },
      {
        title: t("flows.gray"),
        dataIndex: "gray",
        key: "gray",
        render: (v: string | undefined) => (v ? <Tag color="orange">{v}</Tag> : <Text type="secondary">—</Text>),
      },
      {
        title: t("flows.published"),
        dataIndex: "published",
        key: "published",
        render: (v: string | undefined) => (v ? <Tag color="green">{v}</Tag> : <Text type="secondary">—</Text>),
      },
      {
        title: t("flows.actions"),
        key: "actions",
        render: (_: unknown, row: FlowSummaryView) => (
          <Space>
            <Button
              size="small"
              icon={<RocketOutlined />}
              disabled={!row.published}
              onClick={() => runMutation.mutate({ flowId: row.flow_id, version: row.published ?? "" })}
            >
              {t("flows.run")}
            </Button>
            <Button size="small" onClick={() => setDetail(row)}>
              {t("flows.detail")}
            </Button>
          </Space>
        ),
      },
    ],
    [flowsQuery.data, t, runMutation],
  );

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

      {flowsQuery.isLoading ? (
        <Card loading />
      ) : (
        <Table<FlowSummaryView>
          rowKey="flow_id"
          columns={columns}
          dataSource={flowsQuery.data ?? []}
          locale={{ emptyText: <Empty description={t("flows.empty")} /> }}
        />
      )}

      {/* Create flow drawer */}
      <Drawer
        title={t("flows.create")}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        width={640}
        destroyOnClose
      >
        <Form form={form} layout="vertical" onFinish={(v) => createMutation.mutate(v)}>
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
          <Form.Item name="definition" label={t("flows.definitionJson")} rules={[{ required: true }]}>
            <Input.TextArea
              rows={18}
              style={{ fontFamily: "monospace", fontSize: 12 }}
              placeholder={'{\n  "flow_id": "fl_...",\n  "nodes": [...],\n  "edges": [...]\n}'}
            />
          </Form.Item>
          <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={createMutation.isPending}>
            {t("flows.save")}
          </Button>
        </Form>
      </Drawer>

      {/* Detail drawer */}
      {detail && (
        <FlowDetailDrawer
          flow={detail}
          token={token}
          t={t}
          message={message}
          onClose={() => setDetail(null)}
          onRefresh={refresh}
          onPublish={(version) => publishMutation.mutate({ flowId: detail.flow_id, version })}
          onRun={(version) => runMutation.mutate({ flowId: detail.flow_id, version })}
          onCancelRun={(runId) => cancelRunMutation.mutate({ flowId: detail.flow_id, runId })}
        />
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
