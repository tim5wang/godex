import { useMemo, useState, type Key, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Checkbox,
  Drawer,
  Empty,
  Form,
  Input,
  Popconfirm,
  Select,
  Space,
  Switch,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  DatabaseOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  FileSearchOutlined,
  FolderOpenOutlined,
  InboxOutlined,
  PlusOutlined,
  ReloadOutlined,
  StopOutlined,
  UndoOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import {
  acceptMemoryCandidate,
  archiveMemory,
  archiveMilestoneMemories,
  dismissMemoryCandidate,
  digestMemory,
  forgetMemory,
  getMeta,
  listMemory,
  listMemoryAudit,
  listMemoryCandidates,
  listMemorySuppressions,
  mineProjectMemory,
  previewMemoryContext,
  rememberMemory,
  removeMemorySuppression,
  restoreMemoryAudit,
  restoreMemoryStatus,
  updateMemory,
} from "../../lib/api";
import type { MemoryAuditLogEntry, MemoryCandidate, MemoryContextLayers, MemoryDigestResult, MemoryRecord, MemorySuppression, MemoryType } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";
import { ContextGroup, formatDate, MemoryAuditViewer, MemoryDigestViewer, MemoryViewer, Metric } from "./MemoryViewers";

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

const memoryTypes: MemoryType[] = ["identity", "user", "workflow", "project", "warning", "work_method", "work_fact"];

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
  const [selectedDurable, setSelectedDurable] = useState<Key[]>([]);
  const [selectedBlocked, setSelectedBlocked] = useState<Key[]>([]);
  const [form] = Form.useForm<MemoryFormValues>();

  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const canReachMemory = !authRequired || !!token;

  const memoriesQuery = useQuery({
    queryKey: ["memory", token, search, memoryType, sourceFilter],
    enabled: canReachMemory,
    queryFn: async () => listMemory(token || null, { query: search, memoryType, source: sourceFilter, limit: 500 }),
  });
  const archivedQuery = useQuery({
    queryKey: ["memory-archived", token],
    enabled: canReachMemory,
    queryFn: async () => listMemory(token || null, { status: "archived", limit: 500 }),
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
  const archived = Array.isArray(archivedQuery.data) ? archivedQuery.data : [];
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
      queryClient.invalidateQueries({ queryKey: ["memory-archived", token] }),
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

  const archiveMutation = useMutation({
    mutationFn: async (record: MemoryRecord) => archiveMemory(token || null, { file: record.file }),
    onSuccess: async () => {
      void message.success(t("memory.notice.archived", { title: "memory" }));
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to archive memory."),
  });

  const restoreStatusMutation = useMutation({
    mutationFn: async (record: MemoryRecord) => restoreMemoryStatus(token || null, { file: record.file }),
    onSuccess: async () => {
      void message.success("Memory restored.");
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to restore memory."),
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

  const batchCandidateMutation = useMutation({
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

  const batchDurableMutation = useMutation({
    mutationFn: async (action: "archive" | "forget") => {
      const selected = memories.filter((item) => selectedDurable.includes(item.id));
      for (const item of selected) {
        if (action === "archive") {
          await archiveMemory(token || null, { file: item.file });
        } else {
          await forgetMemory(token || null, { file: item.file });
        }
      }
      return selected.length;
    },
    onSuccess: async (count) => {
      void message.success(`Processed ${count} memory${count === 1 ? "" : "ies"}.`);
      setSelectedDurable([]);
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to process memories."),
  });

  const batchBlockedMutation = useMutation({
    mutationFn: async (action: "restore" | "delete" | "unsuppress") => {
      const selectedMemories = archived.filter((item) => selectedBlocked.includes(item.id));
      const selectedSuppressions = suppressions.filter((item) => selectedBlocked.includes(suppressionKey(item)));
      if (action === "restore") {
        for (const item of selectedMemories) {
          await restoreMemoryStatus(token || null, { file: item.file });
        }
      } else if (action === "delete") {
        for (const item of selectedMemories) {
          await forgetMemory(token || null, { file: item.file });
        }
      } else {
        for (const item of selectedSuppressions) {
          await removeMemorySuppression(token || null, suppressionKey(item));
        }
      }
      return selectedMemories.length + selectedSuppressions.length;
    },
    onSuccess: async (count) => {
      void message.success(`Processed ${count} item${count === 1 ? "" : "s"}.`);
      setSelectedBlocked([]);
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to process blocked items."),
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

  const milestonesMutation = useMutation({
    mutationFn: async () => archiveMilestoneMemories(token || null),
    onSuccess: async (result) => {
      void message.success(`Archived ${result.archived.length} milestone memories.`);
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to archive milestones."),
  });

  const removeSuppressionMutation = useMutation({
    mutationFn: async (key: string) => removeMemorySuppression(token || null, key),
    onSuccess: async () => {
      void message.success("Suppression removed.");
      await refreshAll();
    },
    onError: (error) => showError(message, error, "Failed to remove suppression."),
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

  const blockedItems: BlockedItem[] = [
    ...archived.map((item) => ({ id: item.id, kind: "archived" as const, record: item })),
    ...suppressions.map((item) => ({ id: suppressionKey(item), kind: "suppression" as const, suppression: item })),
  ];

  return (
    <div className="page-pad">
      <div className="page-action-row">
        <Space wrap>
          <Button icon={<ReloadOutlined />} onClick={() => void refreshAll()}>Refresh</Button>
          <Button icon={<FileSearchOutlined />} loading={digestMutation.isPending} onClick={() => digestMutation.mutate()}>Digest</Button>
          <Button loading={mineMutation.isPending} onClick={() => mineMutation.mutate()}>{t("memory.actions.mineProjectDocs")}</Button>
          <Button loading={milestonesMutation.isPending} onClick={() => milestonesMutation.mutate()}>{t("memory.board.archiveMilestones")}</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openNew}>{t("memory.actions.saveDurable")}</Button>
        </Space>
      </div>

      <div className="stat-grid" style={{ marginBottom: 16 }}>
        <Metric title={t("memory.board.candidatesTitle")} value={candidates.length} />
        <Metric title={t("memory.board.durableTitle")} value={memories.length} />
        <Metric title={t("memory.board.blockedTitle")} value={blockedItems.length} />
        <Metric title="Audit" value={auditEntries.length} />
        <Metric title={t("memory.stats.preview")} value={`${contextLayers.identity.length}/${contextLayers.core.length}/${contextLayers.relevant.length}`} />
      </div>

      <div className="memory-board">
        <MemoryColumn
          title={t("memory.board.candidatesTitle")}
          subtitle="自动捕获的建议，等待你采纳或忽略"
          count={candidates.length}
          selectedKeys={selectedCandidates}
          onSelect={setSelectedCandidates}
          selectAll={() => setSelectedCandidates(candidates.map((item) => item.fingerprint))}
          clearSelection={() => setSelectedCandidates([])}
          tone="candidate"
          icon={<InboxOutlined />}
          batchActions={[
            { label: t("memory.board.batchAccept", { count: selectedCandidates.length }), disabled: selectedCandidates.length === 0, loading: batchCandidateMutation.isPending, onClick: () => batchCandidateMutation.mutate({ action: "accept" }) },
            { label: t("memory.board.batchAcceptCore", { count: selectedCandidates.length }), disabled: selectedCandidates.length === 0, loading: batchCandidateMutation.isPending, onClick: () => batchCandidateMutation.mutate({ action: "accept", alwaysInclude: true }) },
            { label: t("memory.board.batchDismiss", { count: selectedCandidates.length }), disabled: selectedCandidates.length === 0, danger: true, loading: batchCandidateMutation.isPending, onClick: () => batchCandidateMutation.mutate({ action: "dismiss" }) },
          ]}
          footer={
            <div className="memory-board-tools">
              <Input.Search size="small" value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t("memory.board.searchPlaceholder")} allowClear />
              <Select size="small" value={memoryType} onChange={(v) => setMemoryType(v)} options={[{ value: "", label: t("memory.filters.typeAll") }, ...memoryTypes.map((value) => ({ value, label: t(`memory.types.${value}`) }))]} />
            </div>
          }
        >
          {candidates.map((item) => (
            <CandidateCard
              key={item.fingerprint}
              item={item}
              checked={selectedCandidates.includes(item.fingerprint)}
              onCheck={(checked) => toggleKey(selectedCandidates, item.fingerprint, checked, setSelectedCandidates)}
              onView={() => setViewing(item)}
              onAccept={(core) => acceptMutation.mutate({ fingerprint: item.fingerprint, alwaysInclude: core })}
              onDismiss={() => dismissMutation.mutate(item.fingerprint)}
              t={t}
            />
          ))}
        </MemoryColumn>

        <MemoryColumn
          title={t("memory.board.durableTitle")}
          subtitle="已采纳的长期记忆，可切换到核心或归档"
          count={memories.length}
          selectedKeys={selectedDurable}
          onSelect={setSelectedDurable}
          selectAll={() => setSelectedDurable(memories.map((item) => item.id))}
          clearSelection={() => setSelectedDurable([])}
          tone="durable"
          icon={<DatabaseOutlined />}
          batchActions={[
            { label: t("memory.board.batchArchive", { count: selectedDurable.length }), disabled: selectedDurable.length === 0, loading: batchDurableMutation.isPending, onClick: () => batchDurableMutation.mutate("archive") },
            {
              label: t("memory.board.batchDelete", { count: selectedDurable.length }),
              disabled: selectedDurable.length === 0,
              danger: true,
              loading: batchDurableMutation.isPending,
              confirmTitle: "删除选中的长期记忆？",
              onClick: () => batchDurableMutation.mutate("forget"),
            },
          ]}
        >
          {memories.map((item) => (
            <DurableCard
              key={item.id}
              item={item}
              checked={selectedDurable.includes(item.id)}
              onCheck={(checked) => toggleKey(selectedDurable, item.id, checked, setSelectedDurable)}
              onView={() => setViewing(item)}
              onEdit={() => openEdit(item)}
              onArchive={() => archiveMutation.mutate(item)}
              onForget={() => forgetMutation.mutate(item)}
              onToggleCore={() => toggleCore(item, rememberMutation)}
              t={t}
            />
          ))}
        </MemoryColumn>

        <MemoryColumn
          title={t("memory.board.blockedTitle")}
          subtitle={t("memory.board.blockedSubtitle")}
          count={blockedItems.length}
          selectedKeys={selectedBlocked}
          onSelect={setSelectedBlocked}
          selectAll={() => setSelectedBlocked(blockedItems.map((item) => item.id))}
          clearSelection={() => setSelectedBlocked([])}
          tone="suppressed"
          icon={<StopOutlined />}
          batchActions={[
            { label: t("memory.board.batchRestore", { count: selectedBlocked.length }), disabled: selectedBlocked.length === 0, loading: batchBlockedMutation.isPending, onClick: () => batchBlockedMutation.mutate("restore") },
            { label: t("memory.board.batchUnsuppress", { count: selectedBlocked.length }), disabled: selectedBlocked.length === 0, loading: batchBlockedMutation.isPending, onClick: () => batchBlockedMutation.mutate("unsuppress") },
            {
              label: t("memory.board.batchDelete", { count: selectedBlocked.length }),
              disabled: selectedBlocked.length === 0,
              danger: true,
              loading: batchBlockedMutation.isPending,
              confirmTitle: "永久删除选中的已归档记忆？",
              onClick: () => batchBlockedMutation.mutate("delete"),
            },
          ]}
        >
          {blockedItems.map((item) =>
            item.kind === "archived" ? (
              <BlockedMemoryCard
                key={item.id}
                item={item.record}
                checked={selectedBlocked.includes(item.id)}
                onCheck={(checked) => toggleKey(selectedBlocked, item.id, checked, setSelectedBlocked)}
                onView={() => setViewing(item.record)}
                onRestore={() => restoreStatusMutation.mutate(item.record)}
                onDelete={() => forgetMutation.mutate(item.record)}
                t={t}
              />
            ) : (
              <SuppressionCard
                key={item.id}
                item={item.suppression!}
                checked={selectedBlocked.includes(item.id)}
                onCheck={(checked) => toggleKey(selectedBlocked, item.id, checked, setSelectedBlocked)}
                onUnsuppress={() => removeSuppressionMutation.mutate(suppressionKey(item.suppression!))}
                t={t}
              />
            ),
          )}
          {blockedItems.length === 0 && <MemoryBoardEmpty text={t("memory.empty.suppressionsTitle")} />}
        </MemoryColumn>
      </div>

      <div className="memory-secondary-panel">
        <Card size="small" title="Audit 与上下文预览">
          <div className="memory-secondary-grid">
            <div>
              <Typography.Text strong>Audit</Typography.Text>
              <div className="memory-audit-mini">
                {auditEntries.slice(0, 10).map((entry) => (
                  <div key={entry.id} className="memory-audit-mini-row">
                    <Tag>{entry.action}</Tag>
                    <Typography.Text ellipsis style={{ maxWidth: 200 }}>{entry.title || entry.after?.title || entry.before?.title || "-"}</Typography.Text>
                    <Typography.Text type="secondary">{formatDate(entry.created_at)}</Typography.Text>
                    <Button size="small" icon={<EyeOutlined />} onClick={() => setViewingAudit(entry)} />
                  </div>
                ))}
              </div>
            </div>
            <div>
              <Typography.Text strong>{t("memory.previewTitle")}</Typography.Text>
              <Input.Search size="small" value={previewQuery} onChange={(e) => setPreviewQuery(e.target.value)} placeholder={t("memory.placeholders.previewQuery")} allowClear />
              <ContextGroup title={t("memory.previewIdentityTitle")} items={contextLayers.identity} />
              <ContextGroup title={t("memory.previewCoreTitle")} items={contextLayers.core} />
              <ContextGroup title={t("memory.previewRelevantTitle")} items={contextLayers.relevant} />
            </div>
          </div>
        </Card>
      </div>

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

type BatchAction = {
  label: string;
  disabled: boolean;
  danger?: boolean;
  loading?: boolean;
  confirmTitle?: string;
  onClick: () => void;
};

function MemoryColumn({
  title,
  subtitle,
  count,
  selectedKeys,
  onSelect,
  selectAll,
  clearSelection,
  tone,
  icon,
  batchActions,
  footer,
  children,
}: {
  title: string;
  subtitle?: string;
  count: number;
  selectedKeys: Key[];
  onSelect: (keys: Key[]) => void;
  selectAll: () => void;
  clearSelection: () => void;
  tone: "candidate" | "durable" | "suppressed";
  icon: ReactNode;
  batchActions: BatchAction[];
  footer?: ReactNode;
  children: ReactNode;
}) {
  const allChecked = selectedKeys.length > 0;
  return (
    <Card className={`memory-column memory-column-${tone}`} size="small">
      <div className="memory-column-header">
        <span className="memory-column-icon">{icon}</span>
        <Space direction="vertical" size={0} style={{ minWidth: 0 }}>
          <Typography.Text strong>{title}</Typography.Text>
          {subtitle ? <Typography.Text type="secondary" className="memory-column-subtitle">{subtitle}</Typography.Text> : null}
        </Space>
        <Tag className="memory-column-count">{count}</Tag>
      </div>
      {footer ? <div className="memory-column-footer">{footer}</div> : null}
      {batchActions.length > 0 && (
        <div className="memory-column-batch">
          <Space wrap size={4}>
            {batchActions.map((action, index) =>
              action.confirmTitle ? (
                <Popconfirm key={index} title={action.confirmTitle} onConfirm={action.onClick}>
                  <Button size="small" disabled={action.disabled} danger={action.danger} loading={action.loading}>{action.label}</Button>
                </Popconfirm>
              ) : (
                <Button key={index} size="small" disabled={action.disabled} danger={action.danger} loading={action.loading} onClick={action.onClick}>{action.label}</Button>
              ),
            )}
          </Space>
          <Space size={4}>
            <Button size="small" type="text" disabled={allChecked} onClick={selectAll}>全选</Button>
            {allChecked ? <Button size="small" type="text" onClick={clearSelection}>清空</Button> : null}
          </Space>
        </div>
      )}
      <div className="memory-column-list">
        <div className="memory-column-select-bar">
          <Checkbox
            indeterminate={selectedKeys.length > 0}
            checked={selectedKeys.length === count && count > 0}
            onChange={(e) => (e.target.checked ? selectAll() : clearSelection())}
          >
            <Typography.Text type="secondary">{selectedKeys.length} 已选</Typography.Text>
          </Checkbox>
        </div>
        {children}
      </div>
    </Card>
  );
}

function MemoryCardCheckbox({ checked, onChange }: { checked: boolean; onChange: (checked: boolean) => void }) {
  return <Checkbox checked={checked} onChange={(e) => onChange(e.target.checked)} />;
}

function CandidateCard({
  item,
  checked,
  onCheck,
  onView,
  onAccept,
  onDismiss,
  t,
}: {
  item: MemoryCandidate;
  checked: boolean;
  onCheck: (checked: boolean) => void;
  onView: () => void;
  onAccept: (core: boolean) => void;
  onDismiss: () => void;
  t: Translate;
}) {
  return (
    <div className={`memory-card memory-card-candidate ${checked ? "memory-card-checked" : ""}`}>
      <div className="memory-card-main">
        <MemoryCardCheckbox checked={checked} onChange={onCheck} />
        <div className="memory-card-body">
          <div className="memory-card-title-row">
            <Typography.Text strong ellipsis style={{ maxWidth: "100%" }}>{item.title}</Typography.Text>
          </div>
          <Typography.Text type="secondary" className="memory-card-summary">{item.summary}</Typography.Text>
          <Space size={4} wrap className="memory-card-meta">
            <Tag color={memoryTypeColor(item.memory_type)}>{t(`memory.types.${item.memory_type}`)}</Tag>
            <SourceBadge source={item.source} t={t} />
            <Typography.Text type="secondary" className="memory-card-date">{formatDate(item.created_at)}</Typography.Text>
          </Space>
        </div>
      </div>
      <div className="memory-card-actions">
        <Button size="small" aria-label="View candidate" icon={<EyeOutlined />} onClick={onView} />
        <Button size="small" onClick={() => onAccept(false)}>{t("memory.actions.accept")}</Button>
        <Button size="small" onClick={() => onAccept(true)}>{t("memory.actions.acceptCore")}</Button>
        <Button size="small" danger onClick={onDismiss}>{t("memory.actions.dismiss")}</Button>
      </div>
    </div>
  );
}

function DurableCard({
  item,
  checked,
  onCheck,
  onView,
  onEdit,
  onArchive,
  onForget,
  onToggleCore,
  t,
}: {
  item: MemoryRecord;
  checked: boolean;
  onCheck: (checked: boolean) => void;
  onView: () => void;
  onEdit: () => void;
  onArchive: () => void;
  onForget: () => void;
  onToggleCore: () => void;
  t: Translate;
}) {
  const core = isCoreTagged(item.tags);
  const stale = isStaleMemory(item);
  return (
    <div className={`memory-card memory-card-durable ${checked ? "memory-card-checked" : ""}`}>
      <div className="memory-card-main">
        <MemoryCardCheckbox checked={checked} onChange={onCheck} />
        <div className="memory-card-body">
          <div className="memory-card-title-row">
            <Typography.Text strong ellipsis style={{ maxWidth: "100%" }}>{item.title}</Typography.Text>
            {core ? <Tag color="blue" className="memory-card-core-badge">{t("memory.board.core")}</Tag> : null}
            {stale ? <Tag color="gold" className="memory-card-stale-badge">{t("memory.board.stale")}</Tag> : null}
          </div>
          <Typography.Text type="secondary" className="memory-card-summary">{item.summary}</Typography.Text>
          <Space size={4} wrap className="memory-card-meta">
            <Tag color={memoryTypeColor(item.type)}>{t(`memory.types.${item.type}`)}</Tag>
            <SourceBadge source={item.source} t={t} />
            <Typography.Text type="secondary" className="memory-card-date">{formatDate(item.updated_at)}</Typography.Text>
            <ReferenceBadge item={item} t={t} />
          </Space>
        </div>
      </div>
      <div className="memory-card-actions">
        <Button size="small" aria-label="View memory" icon={<EyeOutlined />} onClick={onView} />
        <Button size="small" aria-label="Edit memory" icon={<EditOutlined />} onClick={onEdit} />
        <Button size="small" onClick={onToggleCore}>{core ? "退核心" : t("memory.board.core")}</Button>
        <Button size="small" icon={<FolderOpenOutlined />} onClick={onArchive}>{t("memory.board.archive")}</Button>
        <Popconfirm title={t("memory.actions.forget")} onConfirm={onForget}>
          <Button size="small" danger aria-label="Forget memory" icon={<DeleteOutlined />} />
        </Popconfirm>
      </div>
    </div>
  );
}

function BlockedMemoryCard({
  item,
  checked,
  onCheck,
  onView,
  onRestore,
  onDelete,
  t,
}: {
  item: MemoryRecord;
  checked: boolean;
  onCheck: (checked: boolean) => void;
  onView: () => void;
  onRestore: () => void;
  onDelete: () => void;
  t: Translate;
}) {
  return (
    <div className={`memory-card memory-card-blocked ${checked ? "memory-card-checked" : ""}`}>
      <div className="memory-card-main">
        <MemoryCardCheckbox checked={checked} onChange={onCheck} />
        <div className="memory-card-body">
          <div className="memory-card-title-row">
            <Typography.Text strong ellipsis style={{ maxWidth: "100%" }}>{item.title}</Typography.Text>
            <Tag color="default">{t("memory.board.archived")}</Tag>
          </div>
          <Typography.Text type="secondary" className="memory-card-summary">{item.summary}</Typography.Text>
          <Space size={4} wrap className="memory-card-meta">
            <Tag color={memoryTypeColor(item.type)}>{t(`memory.types.${item.type}`)}</Tag>
            <SourceBadge source={item.source} t={t} />
            <Typography.Text type="secondary" className="memory-card-date">{formatDate(item.updated_at)}</Typography.Text>
          </Space>
        </div>
      </div>
      <div className="memory-card-actions">
        <Button size="small" aria-label="View memory" icon={<EyeOutlined />} onClick={onView} />
        <Button size="small" icon={<UndoOutlined />} onClick={onRestore}>恢复</Button>
        <Popconfirm title="永久删除这条已归档记忆？" onConfirm={onDelete}>
          <Button size="small" danger aria-label="Delete memory" icon={<DeleteOutlined />} />
        </Popconfirm>
      </div>
    </div>
  );
}

function SuppressionCard({
  item,
  checked,
  onCheck,
  onUnsuppress,
  t,
}: {
  item: MemorySuppression;
  checked: boolean;
  onCheck: (checked: boolean) => void;
  onUnsuppress: () => void;
  t: Translate;
}) {
  return (
    <div className={`memory-card memory-card-suppression ${checked ? "memory-card-checked" : ""}`}>
      <div className="memory-card-main">
        <MemoryCardCheckbox checked={checked} onChange={onCheck} />
        <div className="memory-card-body">
          <div className="memory-card-title-row">
            <Typography.Text strong ellipsis style={{ maxWidth: "100%" }}>{item.fingerprint || item.key || "-"}</Typography.Text>
            <Tag>{t("memory.suppressionsTitle")}</Tag>
          </div>
          <Space size={4} wrap className="memory-card-meta">
            <SourceBadge source={item.source} t={t} />
            <Typography.Text type="secondary" className="memory-card-date">{formatDate(item.created_at)}</Typography.Text>
            {item.expires_at ? <Typography.Text type="secondary" className="memory-card-date">过期 {formatDate(item.expires_at)}</Typography.Text> : null}
          </Space>
        </div>
      </div>
      <div className="memory-card-actions">
        <Button size="small" icon={<UndoOutlined />} onClick={onUnsuppress}>解除屏蔽</Button>
      </div>
    </div>
  );
}

function SourceBadge({ source, t }: { source?: string; t: Translate }) {
  if (!source) {
    return null;
  }
  const auto = isAutoSource(source);
  return (
    <Tooltip title={t(`memory.sources.${sourceKey(source)}`)}>
      <Tag color={auto ? "purple" : "green"} className="memory-source-badge">
        {auto ? t("memory.board.sourceAuto") : t("memory.board.sourceManual")} · {source}
      </Tag>
    </Tooltip>
  );
}

function sourceKey(source: string): string {
  if (source.includes("turn-end")) return "turnEndExtractor";
  if (source.includes("insights")) return "insightsBridge";
  if (source.includes("timeline")) return "timelineBridge";
  if (source.includes("project-miner:readme")) return "projectMinerReadme";
  if (source.includes("project-miner:agents")) return "projectMinerAgents";
  if (source.includes("project-miner:docs")) return "projectMinerDocs";
  if (source.includes("manual")) return "manual";
  if (source.includes("manual-web")) return "manualWeb";
  if (source.includes("session")) return "system";
  return "default";
}

function isAutoSource(source: string): boolean {
  return /turn-end|insights|timeline|project-miner/i.test(source);
}

function isStaleMemory(item: MemoryRecord): boolean {
  const updated = new Date(item.updated_at).getTime();
  if (Number.isNaN(updated)) return false;
  const days = (Date.now() - updated) / 86_400_000;
  if (days < 30) return false;
  // Not stale if recently referenced.
  if (hasRealReference(item)) {
    const referenced = new Date(item.last_referenced_at!).getTime();
    if (!Number.isNaN(referenced) && (Date.now() - referenced) / 86_400_000 < 30) return false;
  }
  return true;
}

// hasRealReference distinguishes a real LastReferencedAt timestamp from the
// Go zero time ("0001-01-01T00:00:00Z" or 0001-01-01T00:00:00) that the
// backend serializes for never-referenced memories.
function hasRealReference(item: MemoryRecord): boolean {
  const raw = item.last_referenced_at;
  if (!raw) return false;
  const ts = new Date(raw).getTime();
  if (Number.isNaN(ts)) return false;
  return ts > 0;
}

function ReferenceBadge({ item, t }: { item: MemoryRecord; t: Translate }) {
  if (!hasRealReference(item)) {
    return <Tag color="default" className="memory-reference-badge">{t("memory.board.neverReferenced")}</Tag>;
  }
  return <Tag color="cyan" className="memory-reference-badge">{t("memory.board.lastReferenced", { time: formatDate(item.last_referenced_at) })}</Tag>;
}

function MemoryBoardEmpty({ text }: { text: string }) {
  return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={text} />;
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

function suppressionKey(item: MemorySuppression): string {
  return item.fingerprint || item.key || `${item.source}:${item.created_at}`;
}

function toggleKey(keys: Key[], key: string, checked: boolean, setter: (keys: Key[]) => void) {
  setter(checked ? [...keys, key] : keys.filter((k) => k !== key));
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
    case "work_method":
      return "orange";
    case "work_fact":
      return "lime";
    default:
      return "default";
  }
}

type BlockedItem =
  | { id: string; kind: "archived"; record: MemoryRecord; suppression?: never }
  | { id: string; kind: "suppression"; record?: never; suppression: MemorySuppression };
