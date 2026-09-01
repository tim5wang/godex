import { type Key, type ReactNode } from "react";
import { useMutation } from "@tanstack/react-query";
import { Button, Card, Checkbox, Empty, Form, Input, Popconfirm, Select, Space, Switch, Tag, Tooltip, Typography } from "antd";
import { DeleteOutlined, EditOutlined, EyeOutlined, FolderOpenOutlined, UndoOutlined } from "@ant-design/icons";
import type { MemoryCandidate, MemoryRecord, MemorySuppression, MemoryType } from "../../lib/types";
import { formatDate } from "./MemoryViewers";

export type MemoryFormValues = {
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

export const memoryTypes: MemoryType[] = ["identity", "user", "workflow", "project", "warning", "work_method", "work_fact"];

type Translate = (key: string, vars?: Record<string, string | number>) => string;

type BatchAction = {
  label: string;
  disabled: boolean;
  danger?: boolean;
  loading?: boolean;
  confirmTitle?: string;
  onClick: () => void;
};

export function MemoryColumn({
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

export function CandidateCard({
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

export function DurableCard({
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

export function BlockedMemoryCard({
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

export function SuppressionCard({
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

export function MemoryBoardEmpty({ text }: { text: string }) {
  return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={text} />;
}

export function MemoryForm({ form, saving, onFinish }: { form: ReturnType<typeof Form.useForm<MemoryFormValues>>[0]; saving: boolean; onFinish: (values: MemoryFormValues) => void }) {
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

export function suppressionKey(item: MemorySuppression): string {
  return item.fingerprint || item.key || `${item.source}:${item.created_at}`;
}

export function toggleKey(keys: Key[], key: string, checked: boolean, setter: (keys: Key[]) => void) {
  setter(checked ? [...keys, key] : keys.filter((k) => k !== key));
}

export function buildRememberPayload(values: MemoryFormValues) {
  return {
    title: values.title.trim(),
    summary: values.summary.trim(),
    content: values.content.trim(),
    memory_type: values.memoryType,
    source: values.source?.trim() || "manual-web",
    tags: mergeTagsWithCore(values.tags || "", Boolean(values.alwaysInclude)),
  };
}

export function buildUpdatePayload(values: MemoryFormValues) {
  return {
    match_title: values.matchTitle,
    match_file: values.matchFile,
    ...buildRememberPayload(values),
  };
}

export function toggleCore(record: MemoryRecord, mutation: ReturnType<typeof useMutation<MemoryRecord, Error, MemoryFormValues>>) {
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

export function draftTagsFromRecord(tags?: string[]) {
  return (tags ?? []).filter((tag) => tag !== "core").join(", ");
}

export function isCoreTagged(tags?: string[]) {
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

export type BlockedItem =
  | { id: string; kind: "archived"; record: MemoryRecord; suppression?: never }
  | { id: string; kind: "suppression"; record?: never; suppression: MemorySuppression };

