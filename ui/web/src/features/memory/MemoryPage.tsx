import { useMemo, useState, type Key, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Descriptions,
  Drawer,
  Empty,
  Form,
  Input,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Typography,
} from "antd";
import {
  ApartmentOutlined,
  ArrowRightOutlined,
  DatabaseOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  FileSearchOutlined,
  HistoryOutlined,
  InboxOutlined,
  PlusOutlined,
  ReloadOutlined,
  RollbackOutlined,
  StopOutlined,
} from "@ant-design/icons";
import { MarkdownContent } from "../../components/MarkdownContent";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import {
  acceptMemoryCandidate,
  dismissMemoryCandidate,
  digestMemory,
  forgetMemory,
  getMeta,
  listMemoryAudit,
  listMemory,
  listMemoryCandidates,
  listMemorySuppressions,
  mineProjectMemory,
  previewMemoryContext,
  rememberMemory,
  restoreMemoryAudit,
  updateMemory,
} from "../../lib/api";
import type { MemoryAuditLogEntry, MemoryCandidate, MemoryContextLayers, MemoryDigestResult, MemoryRecord, MemorySuppression, MemoryType } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";

type MemoryFormValues = {
  title: string;
  summary: string;
  content: string;
  memoryType: MemoryType;
  source?: string;
  tags?: string;
  alwaysInclude?: boolean;
  matchFile?: string;
  matchTitle?: string;
};

const memoryTypes: MemoryType[] = ["identity", "user", "workflow", "project", "warning"];

export function MemoryPage() {
  const { message } = AntApp.useApp();
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [memoryType, setMemoryType] = useState<MemoryType | "">("");
  const [sourceFilter, setSourceFilter] = useState("");
  const [previewQuery, setPreviewQuery] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [viewing, setViewing] = useState<MemoryRecord | MemoryCandidate | null>(null);
  const [viewingAudit, setViewingAudit] = useState<MemoryAuditLogEntry | null>(null);
  const [digestResult, setDigestResult] = useState<MemoryDigestResult | null>(null);
  const [editing, setEditing] = useState<MemoryRecord | null>(null);
  const [selectedCandidates, setSelectedCandidates] = useState<Key[]>([]);
  const [form] = Form.useForm<MemoryFormValues>();

  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const canReachMemory = !authRequired || !!token;

  const memoriesQuery = useQuery({
    queryKey: ["memory", token, search, memoryType, sourceFilter],
    enabled: canReachMemory,
    queryFn: async () => listMemory(token || null, { query: search, memoryType, source: sourceFilter, limit: 300 }),
  });
  const candidatesQuery = useQuery({
    queryKey: ["memory-candidates", token],
    enabled: canReachMemory,
    queryFn: async () => listMemoryCandidates(token || null),
  });
  const suppressionsQuery = useQuery({
    queryKey: ["memory-suppressions", token],
    enabled: canReachMemory,
    queryFn: async () => listMemorySuppressions(token || null),
  });
  const auditQuery = useQuery({
    queryKey: ["memory-audit", token],
    enabled: canReachMemory,
    queryFn: async () => listMemoryAudit(token || null, 50),
  });
  const contextQuery = useQuery({
    queryKey: ["memory-context", token, previewQuery],
    enabled: canReachMemory,
    queryFn: async () => previewMemoryContext(token || null, previewQuery),
  });

  const memories = Array.isArray(memoriesQuery.data) ? memoriesQuery.data : [];
  const candidates = Array.isArray(candidatesQuery.data) ? candidatesQuery.data : [];
  const suppressions = Array.isArray(suppressionsQuery.data) ? suppressionsQuery.data : [];
  const auditEntries = Array.isArray(auditQuery.data) ? auditQuery.data : [];
  const contextLayers: MemoryContextLayers = {
    identity: Array.isArray(contextQuery.data?.identity) ? contextQuery.data.identity : [],
    core: Array.isArray(contextQuery.data?.core) ? contextQuery.data.core : [],
    relevant: Array.isArray(contextQuery.data?.relevant) ? contextQuery.data.relevant : [],
  };
  const sourceOptions = useMemo(
    () => Array.from(new Set([...memories.map((item) => item.source), ...candidates.map((item) => item.source)].filter(Boolean) as string[])).sort(),
    [candidates, memories],
  );

  const refreshAll = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["memory"] }),
      queryClient.invalidateQueries({ queryKey: ["memory-candidates", token] }),
      queryClient.invalidateQueries({ queryKey: ["memory-suppressions", token] }),
      queryClient.invalidateQueries({ queryKey: ["memory-audit", token] }),
      queryClient.invalidateQueries({ queryKey: ["memory-context", token] }),
    ]);
  };

  const rememberMutation = useMutation({
    mutationFn: async (values: MemoryFormValues) =>
      values.matchFile || values.matchTitle
        ? updateMemory(token || null, buildUpdatePayload(values))
        : rememberMemory(token || null, buildRememberPayload(values)),
    onSuccess: async () => {
      void message.success(editing ? "Memory updated." : "Memory saved.");
      setDrawerOpen(false);
      setEditing(null);
      form.resetFields();
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to save memory."),
  });

  const forgetMutation = useMutation({
    mutationFn: async (record: MemoryRecord) => forgetMemory(token || null, { file: record.file }),
    onSuccess: async () => {
      void message.success("Memory forgotten.");
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to forget memory."),
  });

  const acceptMutation = useMutation({
    mutationFn: async ({ fingerprint, alwaysInclude }: { fingerprint: string; alwaysInclude?: boolean }) =>
      acceptMemoryCandidate(token || null, fingerprint, { always_include: alwaysInclude }),
    onSuccess: refreshAll,
    onError: (error) => showError(message, error, "Failed to accept candidate."),
  });

  const dismissMutation = useMutation({
    mutationFn: async (fingerprint: string) => dismissMemoryCandidate(token || null, fingerprint),
    onSuccess: refreshAll,
    onError: (error) => showError(message, error, "Failed to dismiss candidate."),
  });

  const batchMutation = useMutation({
    mutationFn: async ({ action, alwaysInclude }: { action: "accept" | "dismiss"; alwaysInclude?: boolean }) => {
      const selected = candidates.filter((item) => selectedCandidates.includes(item.fingerprint));
      for (const item of selected) {
        if (action === "accept") {
          await acceptMemoryCandidate(token || null, item.fingerprint, { always_include: alwaysInclude });
        } else {
          await dismissMemoryCandidate(token || null, item.fingerprint);
        }
      }
      return selected.length;
    },
    onSuccess: async (count) => {
      void message.success(`Processed ${count} candidate${count === 1 ? "" : "s"}.`);
      setSelectedCandidates([]);
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to process candidates."),
  });

  const mineMutation = useMutation({
    mutationFn: async () => mineProjectMemory(token || null),
    onSuccess: async (items) => {
      void message.success(`Mined ${items.length} candidate${items.length === 1 ? "" : "s"}.`);
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to mine project docs."),
  });

  const digestMutation = useMutation({
    mutationFn: async () => digestMemory(token || null),
    onSuccess: async (result) => {
      setDigestResult(result);
      void message.success(`Digest found ${result.candidates.length} candidate${result.candidates.length === 1 ? "" : "s"}.`);
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to digest memory."),
  });

  const restoreAuditMutation = useMutation({
    mutationFn: async ({ id, target }: { id: string; target: "before" | "after" }) => restoreMemoryAudit(token || null, id, target),
    onSuccess: async (entry) => {
      void message.success(`Restored ${entry.title || entry.memory_id || "memory"}.`);
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to restore memory audit entry."),
  });

  const openNew = () => {
    setEditing(null);
    form.setFieldsValue({ memoryType: "project", title: "", summary: "", content: "", source: "manual-web", tags: "", alwaysInclude: false });
    setDrawerOpen(true);
  };

  const openEdit = (record: MemoryRecord) => {
    setEditing(record);
    form.setFieldsValue({
      title: record.title,
      summary: record.summary,
      content: record.content,
      source: record.source || "",
      tags: draftTagsFromRecord(record.tags),
      memoryType: record.type,
      matchFile: record.file,
      alwaysInclude: isCoreTagged(record.tags),
    });
    setDrawerOpen(true);
  };

  if (authRequired && !token) {
    return (
      <div className="page-pad">
        <Alert type="warning" showIcon message={t("memory.authError")} />
      </div>
    );
  }

  return (
    <div className="page-pad">
      <div className="page-action-row">
        <Space wrap>
          <Button icon={<ReloadOutlined />} onClick={() => void refreshAll()}>Refresh</Button>
          <Button icon={<FileSearchOutlined />} loading={digestMutation.isPending} onClick={() => digestMutation.mutate()}>Digest</Button>
          <Button loading={mineMutation.isPending} onClick={() => mineMutation.mutate()}>{t("memory.actions.mineProjectDocs")}</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openNew}>{t("memory.actions.saveDurable")}</Button>
        </Space>
      </div>

      <div className="stat-grid" style={{ marginBottom: 16 }}>
        <Metric title={t("memory.stats.durable")} value={memories.length} />
        <Metric title={t("memory.stats.candidates")} value={candidates.length} />
        <Metric title={t("memory.stats.suppressed")} value={suppressions.length} />
        <Metric title="Audit" value={auditEntries.length} />
        <Metric title={t("memory.stats.preview")} value={`${contextLayers.identity.length}/${contextLayers.core.length}/${contextLayers.relevant.length}`} />
      </div>

      <MemoryFlowPanel
        memories={memories}
        candidates={candidates}
        suppressions={suppressions}
        contextLayers={contextLayers}
        t={t}
      />

      <Tabs
        items={[
          {
            key: "durable",
            label: t("memory.durableTitle"),
            children: (
              <Card>
                <div className="list-toolbar">
                  <Space wrap>
                    <Input.Search value={search} onChange={(event) => setSearch(event.target.value)} placeholder={t("memory.placeholders.search")} style={{ width: 280 }} allowClear />
                    <Select value={memoryType} onChange={(value) => setMemoryType(value)} style={{ width: 160 }} options={[{ value: "", label: t("memory.filters.typeAll") }, ...memoryTypes.map((value) => ({ value, label: value }))]} />
                    <Select value={sourceFilter} onChange={(value) => setSourceFilter(value)} style={{ width: 220 }} options={[{ value: "", label: t("memory.filters.source") }, ...sourceOptions.map((value) => ({ value, label: value }))]} />
                  </Space>
                  <Typography.Text type="secondary">{t("memory.showing", { visible: memories.length, total: memories.length })}</Typography.Text>
                </div>
                <Table<MemoryRecord>
                  rowKey="id"
                  loading={memoriesQuery.isLoading}
                  dataSource={memories}
                  pagination={{ pageSize: 8 }}
                  locale={{
                    emptyText: (
                      <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No durable memory yet.">
                        <Space direction="vertical" size={8} align="center">
                          <Typography.Text type="secondary">Chat with GoDex or save a memory manually to start building context.</Typography.Text>
                          <Button type="primary" icon={<PlusOutlined />} aria-label="Save first memory" onClick={openNew}>
                            Save first memory
                          </Button>
                        </Space>
                      </Empty>
                    ),
                  }}
                  columns={[
                    { title: t("memory.fields.title"), dataIndex: "title", render: (value, record) => <Space direction="vertical" size={0}><Typography.Text strong>{value}</Typography.Text><Typography.Text type="secondary">{record.summary}</Typography.Text></Space> },
                    { title: t("memory.fields.type"), dataIndex: "type", render: (value) => <Tag>{value}</Tag> },
                    { title: t("memory.fields.source"), dataIndex: "source", render: (value) => value || "-" },
                    { title: t("memory.fields.updated"), dataIndex: "updated_at", render: formatDate },
                    {
                      title: "Actions",
                      render: (_value, record) => (
                        <Space wrap>
                          <Button size="small" aria-label="View memory" icon={<EyeOutlined />} onClick={() => setViewing(record)} />
                          <Button size="small" aria-label="Edit memory" icon={<EditOutlined />} onClick={() => openEdit(record)} />
                          <Button size="small" onClick={() => toggleCore(record, rememberMutation)}>Core</Button>
                          <Popconfirm title={t("memory.actions.forget")} onConfirm={() => forgetMutation.mutate(record)}>
                            <Button size="small" danger aria-label="Forget memory" icon={<DeleteOutlined />} />
                          </Popconfirm>
                        </Space>
                      ),
                    },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: "candidates",
            label: t("memory.candidatesTitle"),
            children: (
              <Card>
                <div className="list-toolbar">
                  <Space wrap>
                    <Button disabled={selectedCandidates.length === 0} loading={batchMutation.isPending} onClick={() => batchMutation.mutate({ action: "accept" })}>{t("memory.actions.acceptSelected", { count: selectedCandidates.length })}</Button>
                    <Button disabled={selectedCandidates.length === 0} loading={batchMutation.isPending} onClick={() => batchMutation.mutate({ action: "accept", alwaysInclude: true })}>{t("memory.actions.acceptCoreSelected", { count: selectedCandidates.length })}</Button>
                    <Button disabled={selectedCandidates.length === 0} danger loading={batchMutation.isPending} onClick={() => batchMutation.mutate({ action: "dismiss" })}>{t("memory.actions.dismissSelected", { count: selectedCandidates.length })}</Button>
                  </Space>
                  <Typography.Text type="secondary">{selectedCandidates.length} selected</Typography.Text>
                </div>
                <Table<MemoryCandidate>
                  rowKey="fingerprint"
                  loading={candidatesQuery.isLoading}
                  dataSource={candidates}
                  rowSelection={{ selectedRowKeys: selectedCandidates, onChange: setSelectedCandidates }}
                  pagination={{ pageSize: 8 }}
                  columns={[
                    { title: t("memory.fields.title"), dataIndex: "title", render: (value, record) => <Space direction="vertical" size={0}><Typography.Text strong>{value}</Typography.Text><Typography.Text type="secondary">{record.summary}</Typography.Text></Space> },
                    { title: t("memory.fields.type"), dataIndex: "memory_type", render: (value) => <Tag>{value}</Tag> },
                    { title: t("memory.fields.source"), dataIndex: "source", render: (value) => value || "-" },
                    { title: t("memory.fields.created"), dataIndex: "created_at", render: formatDate },
                    {
                      title: "Actions",
                      render: (_value, record) => (
                        <Space wrap>
                          <Button size="small" aria-label="View memory candidate" icon={<EyeOutlined />} onClick={() => setViewing(record)} />
                          <Button size="small" onClick={() => acceptMutation.mutate({ fingerprint: record.fingerprint })}>{t("memory.actions.accept")}</Button>
                          <Button size="small" onClick={() => acceptMutation.mutate({ fingerprint: record.fingerprint, alwaysInclude: true })}>{t("memory.actions.acceptCore")}</Button>
                          <Button size="small" danger onClick={() => dismissMutation.mutate(record.fingerprint)}>{t("memory.actions.dismiss")}</Button>
                        </Space>
                      ),
                    },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: "suppressions",
            label: t("memory.suppressionsTitle"),
            children: (
              <Card>
                <Table<MemorySuppression>
                  rowKey={(record) => record.fingerprint || record.key || `${record.source}:${record.created_at}`}
                  loading={suppressionsQuery.isLoading}
                  dataSource={suppressions}
                  columns={[
                    { title: "Key", render: (_value, record) => record.fingerprint || record.key || "-" },
                    { title: t("memory.fields.source"), dataIndex: "source", render: (value) => value || "-" },
                    { title: t("memory.fields.created"), dataIndex: "created_at", render: formatDate },
                    { title: "Expires", dataIndex: "expires_at", render: formatDate },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: "audit",
            label: (
              <Space size={6}>
                <HistoryOutlined />
                <span>Audit</span>
              </Space>
            ),
            children: (
              <Card>
                <Table<MemoryAuditLogEntry>
                  rowKey="id"
                  loading={auditQuery.isLoading}
                  dataSource={auditEntries}
                  pagination={{ pageSize: 8 }}
                  columns={[
                    { title: "Action", dataIndex: "action", render: (value) => <Tag>{value}</Tag> },
                    { title: t("memory.fields.title"), render: (_value, record) => record.title || record.after?.title || record.before?.title || "-" },
                    { title: t("memory.fields.type"), render: (_value, record) => record.memory_type || record.after?.type || record.before?.type || "-" },
                    { title: t("memory.fields.source"), dataIndex: "source", render: (value) => value || "-" },
                    { title: t("memory.fields.created"), dataIndex: "created_at", render: formatDate },
                    {
                      title: "Actions",
                      render: (_value, record) => (
                        <Space wrap>
                          <Button size="small" aria-label="View memory audit diff" icon={<EyeOutlined />} onClick={() => setViewingAudit(record)}>Diff</Button>
                          <Button
                            size="small"
                            icon={<RollbackOutlined />}
                            disabled={!record.before}
                            loading={restoreAuditMutation.isPending}
                            onClick={() => restoreAuditMutation.mutate({ id: record.id, target: "before" })}
                          >
                            Restore before
                          </Button>
                          <Button
                            size="small"
                            disabled={!record.after}
                            loading={restoreAuditMutation.isPending}
                            onClick={() => restoreAuditMutation.mutate({ id: record.id, target: "after" })}
                          >
                            Reapply after
                          </Button>
                        </Space>
                      ),
                    },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: "preview",
            label: t("memory.previewTitle"),
            children: (
              <Card>
                <Space direction="vertical" size={16} style={{ width: "100%" }}>
                  <Input.Search value={previewQuery} onChange={(event) => setPreviewQuery(event.target.value)} placeholder={t("memory.placeholders.previewQuery")} allowClear />
                  <ContextGroup title={t("memory.previewIdentityTitle")} items={contextLayers.identity} />
                  <ContextGroup title={t("memory.previewCoreTitle")} items={contextLayers.core} />
                  <ContextGroup title={t("memory.previewRelevantTitle")} items={contextLayers.relevant} />
                </Space>
              </Card>
            ),
          },
        ]}
      />

      <Drawer title={editing ? t("memory.actions.saveChanges") : t("memory.actions.saveDurable")} width={640} open={drawerOpen} onClose={() => setDrawerOpen(false)} destroyOnHidden>
        <MemoryForm form={form} saving={rememberMutation.isPending} onFinish={(values) => rememberMutation.mutate(values)} />
      </Drawer>
      <Drawer title={viewing?.title} width={720} open={!!viewing} onClose={() => setViewing(null)} destroyOnHidden>
        {viewing ? <MemoryViewer item={viewing} /> : null}
      </Drawer>
      <Drawer title="Memory audit" width={920} open={!!viewingAudit} onClose={() => setViewingAudit(null)} destroyOnHidden>
        {viewingAudit ? <MemoryAuditViewer entry={viewingAudit} /> : null}
      </Drawer>
      <Drawer title="Memory digest" width={840} open={!!digestResult} onClose={() => setDigestResult(null)} destroyOnHidden>
        {digestResult ? <MemoryDigestViewer result={digestResult} /> : null}
      </Drawer>
    </div>
  );
}

type Translate = (key: string, vars?: Record<string, string | number>) => string;

function MemoryFlowPanel({
  candidates,
  contextLayers,
  memories,
  suppressions,
  t,
}: {
  candidates: MemoryCandidate[];
  contextLayers: MemoryContextLayers;
  memories: MemoryRecord[];
  suppressions: MemorySuppression[];
  t: Translate;
}) {
  const coreCount = memories.filter((item) => isCoreTagged(item.tags)).length;
  const maxPriorityCount = Math.max(1, contextLayers.identity.length, contextLayers.core.length, contextLayers.relevant.length);
  const typeCounts = memoryTypes.map((type) => ({
    type,
    count: memories.filter((item) => item.type === type).length,
  }));

  return (
    <Card className="memory-flow-card" style={{ marginBottom: 16 }}>
      <div className="memory-flow-header">
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{t("memory.flowTitle")}</Typography.Text>
          <Typography.Text type="secondary">{t("memory.flowSubtitle")}</Typography.Text>
        </Space>
      </div>
      <div className="memory-flow-steps">
        <MemoryFlowNode icon={<InboxOutlined />} title={t("memory.flowCandidates")} value={candidates.length} tone="candidate" />
        <ArrowRightOutlined className="memory-flow-arrow" />
        <MemoryFlowNode icon={<DatabaseOutlined />} title={t("memory.flowDurable")} value={memories.length} meta={`${coreCount} core`} tone="durable" />
        <ArrowRightOutlined className="memory-flow-arrow" />
        <MemoryFlowNode
          icon={<ApartmentOutlined />}
          title={t("memory.flowContext")}
          value={contextLayers.identity.length + contextLayers.core.length + contextLayers.relevant.length}
          meta={`${contextLayers.identity.length}/${contextLayers.core.length}/${contextLayers.relevant.length}`}
          tone="context"
        />
        <ArrowRightOutlined className="memory-flow-arrow memory-flow-arrow-muted" />
        <MemoryFlowNode icon={<StopOutlined />} title={t("memory.flowSuppressed")} value={suppressions.length} tone="suppressed" />
      </div>
      <div className="memory-priority-grid">
        <div className="memory-priority-panel">
          <Typography.Text strong>{t("memory.previewTitle")}</Typography.Text>
          <MemoryPriorityLane title={t("memory.flowIdentity")} value={contextLayers.identity.length} max={maxPriorityCount} tone="identity" />
          <MemoryPriorityLane title={t("memory.flowCore")} value={contextLayers.core.length} max={maxPriorityCount} tone="core" />
          <MemoryPriorityLane title={t("memory.flowRelevant")} value={contextLayers.relevant.length} max={maxPriorityCount} tone="relevant" />
        </div>
        <div className="memory-type-panel">
          <Typography.Text strong>{t("memory.flowMemoryTypes")}</Typography.Text>
          <div className="memory-type-tags">
            {typeCounts.map((item) => (
              <Tag key={item.type} color={memoryTypeColor(item.type)}>
                {t(`memory.types.${item.type}`)} {item.count}
              </Tag>
            ))}
          </div>
        </div>
      </div>
    </Card>
  );
}

function MemoryFlowNode({
  icon,
  meta,
  title,
  tone,
  value,
}: {
  icon: ReactNode;
  meta?: string;
  title: string;
  tone: "candidate" | "durable" | "context" | "suppressed";
  value: number;
}) {
  return (
    <div className={`memory-flow-node memory-flow-node-${tone}`}>
      <span className="memory-flow-icon">{icon}</span>
      <span className="memory-flow-node-text">
        <Typography.Text type="secondary">{title}</Typography.Text>
        <Typography.Title level={3} style={{ margin: 0 }}>{value}</Typography.Title>
        {meta ? <Typography.Text type="secondary">{meta}</Typography.Text> : null}
      </span>
    </div>
  );
}

function MemoryPriorityLane({ max, title, tone, value }: { max: number; title: string; tone: "identity" | "core" | "relevant"; value: number }) {
  const width = value > 0 ? `${Math.max(8, Math.round((value / max) * 100))}%` : "0%";
  return (
    <div className="memory-priority-lane">
      <div className="memory-priority-label">
        <Typography.Text>{title}</Typography.Text>
        <Tag>{value}</Tag>
      </div>
      <div className="memory-priority-track">
        <div className={`memory-priority-bar memory-priority-bar-${tone}`} style={{ width }} />
      </div>
    </div>
  );
}

function MemoryForm({ form, saving, onFinish }: { form: ReturnType<typeof Form.useForm<MemoryFormValues>>[0]; saving: boolean; onFinish: (values: MemoryFormValues) => void }) {
  return (
    <Form form={form} layout="vertical" onFinish={onFinish}>
      <Form.Item name="matchFile" hidden><Input /></Form.Item>
      <Form.Item name="matchTitle" hidden><Input /></Form.Item>
      <Form.Item name="title" label="Title" rules={[{ required: true }]}><Input /></Form.Item>
      <Form.Item name="summary" label="Summary" rules={[{ required: true }]}><Input /></Form.Item>
      <Form.Item name="memoryType" label="Type" rules={[{ required: true }]}><Select options={memoryTypes.map((value) => ({ value, label: value }))} /></Form.Item>
      <Form.Item name="source" label="Source"><Input /></Form.Item>
      <Form.Item name="tags" label="Tags"><Input placeholder="comma,separated,tags" /></Form.Item>
      <Form.Item name="alwaysInclude" label="Always include / core" valuePropName="checked"><Switch /></Form.Item>
      <Form.Item name="content" label="Content" rules={[{ required: true }]}><Input.TextArea rows={10} /></Form.Item>
      <Button block type="primary" htmlType="submit" loading={saving}>Save</Button>
    </Form>
  );
}

function MemoryViewer({ item }: { item: MemoryRecord | MemoryCandidate }) {
  const type = "type" in item ? item.type : item.memory_type;
  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <Descriptions bordered size="small" column={1} items={[
        { key: "type", label: "Type", children: type },
        { key: "source", label: "Source", children: item.source || "-" },
        { key: "summary", label: "Summary", children: item.summary },
      ]} />
      <MarkdownContent content={item.content} />
    </Space>
  );
}

function MemoryAuditViewer({ entry }: { entry: MemoryAuditLogEntry }) {
  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <Descriptions bordered size="small" column={1} items={[
        { key: "id", label: "Audit ID", children: entry.id },
        { key: "action", label: "Action", children: entry.action },
        { key: "title", label: "Title", children: entry.title || entry.after?.title || entry.before?.title || "-" },
        { key: "type", label: "Type", children: entry.memory_type || entry.after?.type || entry.before?.type || "-" },
        { key: "source", label: "Source", children: entry.source || "-" },
        { key: "created", label: "Created", children: formatDate(entry.created_at) },
        { key: "message", label: "Message", children: entry.message || "-" },
      ]} />
      <div className="memory-audit-diff">
        <AuditSnapshot title="Before" record={entry.before} />
        <AuditSnapshot title="After" record={entry.after} />
      </div>
    </Space>
  );
}

function AuditSnapshot({ record, title }: { record?: MemoryRecord; title: string }) {
  return (
    <Card size="small" title={title}>
      {record ? (
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Typography.Text strong>{record.title}</Typography.Text>
          <Typography.Text type="secondary">{record.summary}</Typography.Text>
          <Typography.Text type="secondary">{record.type} / {record.source || "-"}</Typography.Text>
          <Typography.Paragraph style={{ whiteSpace: "pre-wrap", marginBottom: 0 }}>{record.content}</Typography.Paragraph>
        </Space>
      ) : (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
      )}
    </Card>
  );
}

function MemoryDigestViewer({ result }: { result: MemoryDigestResult }) {
  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      {result.report_path ? <Alert type="info" showIcon message={`Report written to ${result.report_path}`} /> : null}
      <Card size="small" title={`Candidates (${result.candidates.length})`}>
        {result.candidates.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> : (
          <Space direction="vertical" size={12} style={{ width: "100%" }}>
            {result.candidates.map((candidate) => (
              <Card key={candidate.fingerprint} size="small">
                <Space direction="vertical" size={2} style={{ width: "100%" }}>
                  <Typography.Text strong>{candidate.title}</Typography.Text>
                  <Typography.Text type="secondary">{candidate.summary}</Typography.Text>
                  <Space wrap>
                    <Tag>{candidate.memory_type}</Tag>
                    {candidate.source ? <Tag>{candidate.source}</Tag> : null}
                  </Space>
                </Space>
              </Card>
            ))}
          </Space>
        )}
      </Card>
      <Card size="small" title="Report">
        <MarkdownContent content={result.report || "_No report returned._"} />
      </Card>
    </Space>
  );
}

function ContextGroup({ title, items }: { title: string; items: MemoryRecord[] }) {
  return (
    <Card size="small" title={title} extra={<Tag>{items.length}</Tag>}>
      {items.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> : (
        <Space direction="vertical" size={10} style={{ width: "100%" }}>
          {items.map((item) => (
            <Card key={item.id} size="small">
              <Typography.Text strong>{item.title}</Typography.Text>
              <Typography.Paragraph type="secondary">{item.summary}</Typography.Paragraph>
            </Card>
          ))}
        </Space>
      )}
    </Card>
  );
}

function Metric({ title, value }: { title: string; value: string | number }) {
  return (
    <Card size="small">
      <Typography.Text type="secondary">{title}</Typography.Text>
      <Typography.Title level={3} style={{ margin: 0 }}>{value}</Typography.Title>
    </Card>
  );
}

function buildRememberPayload(values: MemoryFormValues) {
  return {
    title: values.title.trim(),
    summary: values.summary.trim(),
    content: values.content.trim(),
    memory_type: values.memoryType,
    source: values.source?.trim() || "manual-web",
    tags: mergeTagsWithCore(values.tags || "", Boolean(values.alwaysInclude)),
  };
}

function buildUpdatePayload(values: MemoryFormValues) {
  return {
    match_title: values.matchTitle,
    match_file: values.matchFile,
    ...buildRememberPayload(values),
  };
}

function toggleCore(record: MemoryRecord, mutation: ReturnType<typeof useMutation<MemoryRecord, Error, MemoryFormValues>>) {
  mutation.mutate({
    title: record.title,
    summary: record.summary,
    content: record.content,
    source: record.source || "",
    tags: draftTagsFromRecord(record.tags),
    memoryType: record.type,
    matchFile: record.file,
    alwaysInclude: !isCoreTagged(record.tags),
  });
}

function splitTags(value: string) {
  return value.split(",").map((part) => part.trim()).filter(Boolean);
}

function mergeTagsWithCore(value: string, alwaysInclude: boolean) {
  const tags = splitTags(value).filter((tag) => tag !== "core");
  if (alwaysInclude) {
    tags.push("core");
  }
  return Array.from(new Set(tags));
}

function draftTagsFromRecord(tags?: string[]) {
  return (tags ?? []).filter((tag) => tag !== "core").join(", ");
}

function isCoreTagged(tags?: string[]) {
  return Boolean(tags?.includes("core"));
}

function memoryTypeColor(type: MemoryType) {
  switch (type) {
    case "identity":
      return "cyan";
    case "user":
      return "blue";
    case "workflow":
      return "purple";
    case "project":
      return "green";
    case "warning":
      return "gold";
    default:
      return "default";
  }
}

function formatDate(value?: string) {
  if (!value) {
    return "-";
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}
