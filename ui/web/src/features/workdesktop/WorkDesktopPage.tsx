import { useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, App as AntApp, Badge, Button, Card, Drawer, Empty, Form, Input, Modal, Select, Space, Spin, Tabs, Tag, Timeline, Tooltip, Typography } from "antd";
import { ApartmentOutlined, ArrowRightOutlined, CheckCircleOutlined, ClockCircleOutlined, CodeOutlined, CommentOutlined, EditOutlined, FileTextOutlined, FolderOutlined, LinkOutlined, MessageOutlined, PauseCircleOutlined, PlayCircleOutlined, PlusOutlined, ProfileOutlined, SearchOutlined, ThunderboltOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { useSettingsStore } from "../../store/settings";
import { useNodeContextStore } from "../../store/nodeContext";
import { executeTaskboardCard, listAgentTemplates, listSessions, listTaskboardProjects, patchTaskboardCard } from "../../lib/api";
import { buildChatRouteForSession } from "../../lib/chatRoutes";
import { showError } from "../../lib/notifications";
import type { AgentTemplate } from "../../lib/apiAgent";
import type { TaskboardCard } from "../../lib/types";
import {
  addWorkRelation,
  addWorkResource,
  createWorkContext,
  createWorkDelegation,
  createWorkFact,
  createWorkTodo,
  deleteWorkTodo,
  getWorkContext,
  getWorkContextBrief,
  getWorkFocus,
  getWorkAttention,
  linkWorkSession,
  listWorkContexts,
  listWorkResources,
  markWorkContextSeen,
  organizeWorkContext,
  patchWorkContext,
  patchWorkTodo,
  promoteWorkFactMemoryCandidate,
  recallWorkFacts,
  removeWorkRelation,
  removeWorkResource,
  reviewWorkFact,
  saveCheckpoint,
  unlinkWorkSession,
  workContextQueryKey,
  type RecallScope,
  type WorkContext,
  type WorkContextBrief,
  type WorkContextDetail,
  type WorkDelegation,
  type WorkFact,
  type WorkResource,
  type WorkTodo,
  type WorkTodoInput,
  type WorkTodoStatus,
} from "../../lib/apiWorkContext";
import { InfoHint } from "./workDesktopHints";
import { AttentionInbox } from "./AttentionInbox";
import { InlineEdit } from "./workDesktopInline";
import { ResourceChips } from "./ResourceChips";
import { TodoBoard } from "./TodoBoard";
import { WorkChatDock } from "./WorkChatDock";
import { FilesPanel } from "../files/FilesPanel";
import { delegationsNeedingReview, formatTimestamp, isReadOnly, joinLines, otherSide, splitLines } from "./workDesktopView";
import "./workDesktop.css";

type Editor =
  | "create"
  | "editContext"
  | "checkpoint"
  | "delegate"
  | "session"
  | "fact"
  | "resource"
  | "relation"
  | "review";

type EditorValues = {
  title?: string;
  background?: string;
  goal?: string;
  summary?: string;
  next?: string;
  completed?: string;
  inProgress?: string;
  blockers?: string;
  openQuestions?: string;
  evidenceRefs?: string;
  description?: string;
  acceptance?: string;
  project?: string;
  workDir?: string;
  template?: string;
  topic?: string;
  sessionId?: string;
  workspaceDir?: string;
  resourceKind?: string;
  locator?: string;
  node?: string;
  purpose?: string;
  target?: string;
  relationKind?: string;
  condition?: string;
  content?: string;
  scope?: string;
  subject?: string;
  predicate?: string;
  source?: string;
  sourceExcerpt?: string;
  observedAt?: string;
  provenance?: string;
  supersedes?: string;
  factKind?: string;
  reason?: string;
};

// Fixed palette per status: a status never borrows another status's colour, so the
// sidebar, header and delegation rows cannot disagree about what "in_review" looks like.
const statusColor: Record<string, string> = { active: "processing", paused: "orange", archived: "default" };
const cardStatusColor: Record<string, string> = { backlog: "default", todo: "blue", in_progress: "processing", in_review: "gold", done: "green" };
const factStatusColor: Record<string, string> = { candidate: "gold", confirmed: "green", stale: "orange", superseded: "default", rejected: "default" };
const nonBlank = { required: true, whitespace: true };
const tabs = ["overview", "plan", "chat", "conversations", "tasks", "knowledge", "files", "timeline"] as const;
const resourceKinds = ["web", "directory", "repository", "note", "document", "dashboard", "service", "model", "artifact"];

export function WorkDesktopPage() {
  const { t } = useI18n();
  const { message } = AntApp.useApp();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const token = useSettingsStore((state) => state.token);
  const nodeID = useNodeContextStore((state) => state.nodeID);
  const [params, setParams] = useSearchParams();
  const [search, setSearch] = useState("");
  const [recallQuery, setRecallQuery] = useState("");
  const [recallScope, setRecallScope] = useState<RecallScope>("current");
  const [briefOpen, setBriefOpen] = useState(false);
  const [editor, setEditor] = useState<Editor>();
  const [reviewTarget, setReviewTarget] = useState<WorkFact>();
  const [form] = Form.useForm<EditorValues>();
  const tr = (key: string, vars?: Record<string, string | number>) => t(`workDesktop.${key}`, vars);
  // trLabel resolves a namespaced label and falls back to the raw server value when the
  // server introduces a value this build does not know about (never renders a raw key).
  const trLabel = (prefix: string, value: string) => {
    const resolved = tr(prefix ? `${prefix}.${value}` : value);
    return resolved.startsWith("workDesktop.") ? value : resolved;
  };
  const fail = (error: unknown, key: string) => showError(message, error, tr(key));
  const isConflict = (error: unknown) => (error as { status?: number })?.status === 409;

  const contextsQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "contexts"),
    queryFn: () => listWorkContexts(token || null),
  });
  const contexts = contextsQuery.data ?? [];
  const requestedId = params.get("context") ?? undefined;
  const requestedTab = params.get("tab") ?? "overview";
  const tab = (tabs as readonly string[]).includes(requestedTab) ? requestedTab : "overview";
  // An unknown id must not silently fall through to "the first stream" without saying so;
  // the mismatch is surfaced and the stale query parameter is dropped (below).
  const invalidContext = !!requestedId && !contextsQuery.isFetching && !contexts.some((item) => item.id === requestedId);
  const selected = contexts.find((item) => item.id === requestedId) ?? contexts[0];
  const contextId = selected?.id;
  const readOnly = isReadOnly(selected?.status);

  const detailQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "detail", contextId),
    queryFn: () => getWorkContext(token || null, contextId!),
    enabled: !!contextId,
  });
  const resourcesQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "resources"),
    queryFn: () => listWorkResources(token || null),
  });
  const recallResult = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "recall", contextId, recallScope, recallQuery),
    queryFn: () => recallWorkFacts(token || null, contextId!, recallScope, recallQuery),
    enabled: !!contextId,
  });
  const briefQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "brief", contextId),
    queryFn: () => getWorkContextBrief(token || null, contextId!),
    enabled: !!contextId && briefOpen,
  });
  // Focus returns the plan items and what changed since the stream was last seen.
  // It is read-only; the watermark only advances via the explicit mark-seen action.
  const focusQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "focus", contextId),
    queryFn: () => getWorkFocus(token || null, contextId!),
    enabled: !!contextId,
  });
  // Attention is cross-stream: what needs the user across every stream, plus the
  // unread count per stream (the wall's badge). Polled so a card that finishes in
  // the background shows up without a manual refresh.
  const attentionQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "attention"),
    queryFn: () => getWorkAttention(token || null),
    refetchInterval: 60_000,
  });
  const projectsQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "projects"),
    queryFn: () => listTaskboardProjects(token || null),
    enabled: !!editor && (editor === "delegate"),
  });  const templatesQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "templates"),
    queryFn: () => listAgentTemplates(token || null),
    enabled: editor === "delegate",
  });

  const invalidateAll = () => void queryClient.invalidateQueries({ queryKey: ["work-context"] });
  const invalidateDetail = () => {
    invalidateAll();
    void detailQuery.refetch();
    void recallResult.refetch();
  };

  function selectContext(id: string) {
    setParams({ context: id, tab });
    setBriefOpen(false);
  }

  // selectContextTab switches stream AND tab in one navigation: the attention
  // inbox knows where an item is resolved (tasks / plan), so landing on the
  // stream's default tab would make the user hunt for it again.
  function selectContextTab(id: string, target: string) {
    const known = (tabs as readonly string[]).includes(target) ? target : "overview";
    setParams({ context: id, tab: known });
    setBriefOpen(false);
  }

  function openEditor(kind: Editor) {
    form.resetFields();
    if (kind === "checkpoint" && selected) {
      form.setFieldsValue({
        summary: selected.checkpoint.summary,
        next: joinLines(selected.checkpoint.next_actions),
        completed: joinLines(selected.checkpoint.completed),
        inProgress: joinLines(selected.checkpoint.in_progress),
        blockers: joinLines(selected.checkpoint.blockers),
        openQuestions: joinLines(selected.checkpoint.open_questions),
        evidenceRefs: joinLines(selected.checkpoint.evidence_refs),
      });
    } else if (kind === "editContext" && selected) {
      form.setFieldsValue({ title: selected.title, background: selected.background, goal: selected.goal });
    } else if (kind === "fact") {
      form.setFieldsValue({ factKind: "fact" });
    } else if (kind === "resource") {
      form.setFieldsValue({ resourceKind: "web" });
    } else if (kind === "relation") {
      form.setFieldsValue({ relationKind: "related_to" });
    }
    setEditor(kind);
  }

  const createMutation = useMutation({
    mutationFn: (values: EditorValues) => createWorkContext(token || null, { title: values.title!.trim(), background: values.background?.trim() ?? "", goal: values.goal!.trim() }),
    onSuccess: (created) => {
      invalidateAll();
      setEditor(undefined);
      setParams({ context: created.id, tab: "overview" });
      void message.success(tr("saved"));
    },
    onError: (error) => fail(error, "createFailed"),
  });

  const patchContextMutation = useMutation({
    mutationFn: (input: { id: string; version: number; status?: string; title?: string; background?: string; goal?: string }) =>
      patchWorkContext(token || null, input.id, { title: input.title, background: input.background, goal: input.goal, status: input.status as never, if_version: input.version }),
    onSuccess: () => { invalidateAll(); setEditor(undefined); void message.success(tr("saved")); },
    onError: (error) => (isConflict(error) ? void message.error(tr("conflict409")) : fail(error, "saveFailed")),
  });

  const checkpointMutation = useMutation({
    mutationFn: (values: EditorValues) => saveCheckpoint(token || null, contextId!, selected?.version ?? 0, {
      summary: values.summary!.trim(),
      completed: splitLines(values.completed),
      in_progress: splitLines(values.inProgress),
      next_actions: splitLines(values.next),
      blockers: splitLines(values.blockers),
      open_questions: splitLines(values.openQuestions),
      evidence_refs: splitLines(values.evidenceRefs),
    }),
    onSuccess: () => { invalidateDetail(); setEditor(undefined); void message.success(tr("checkpointSaved")); },
    onError: (error) => (isConflict(error) ? void message.error(tr("conflict409")) : fail(error, "saveFailed")),
  });

  const resourceMutation = useMutation({
    mutationFn: (values: EditorValues) => addWorkResource(token || null, contextId!, { title: values.title!.trim(), kind: values.resourceKind!.trim(), locator: values.locator!.trim(), node_id: values.node?.trim() ?? "", purpose: values.purpose?.trim() ?? "" }),
    onSuccess: () => { invalidateAll(); setEditor(undefined); void message.success(tr("resourceCreated")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const removeResourceMutation = useMutation({
    mutationFn: (resourceId: string) => removeWorkResource(token || null, contextId!, resourceId),
    onSuccess: () => { invalidateAll(); void message.success(tr("deleted")); },
    onError: (error) => fail(error, "deleteFailed"),
  });

  const relationMutation = useMutation({
    mutationFn: (values: EditorValues) => addWorkRelation(token || null, contextId!, { to_id: values.target!, kind: values.relationKind!.trim(), condition: values.condition?.trim() ?? "" }),
    onSuccess: () => { invalidateAll(); setEditor(undefined); void message.success(tr("relationCreated")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const removeRelationMutation = useMutation({
    mutationFn: (relationId: string) => removeWorkRelation(token || null, contextId!, relationId),
    onSuccess: () => { invalidateAll(); void message.success(tr("deleted")); },
    onError: (error) => fail(error, "deleteFailed"),
  });

  const factMutation = useMutation({
    mutationFn: (values: EditorValues) => createWorkFact(token || null, contextId!, {
      title: values.title!.trim(),
      content: values.content!.trim(),
      kind: values.factKind?.trim() || "fact",
      scope: values.scope?.trim() ?? "",
      subject: values.subject?.trim() ?? "",
      predicate: values.predicate?.trim() ?? "",
      source: values.source?.trim() ?? "",
      source_excerpt: values.sourceExcerpt?.trim() ?? "",
      observed_at: values.observedAt?.trim() ?? "",
      provenance: values.provenance?.trim() ?? "",
      supersedes_id: values.supersedes?.trim() ?? "",
    }),
    onSuccess: () => { invalidateDetail(); setEditor(undefined); void message.success(tr("factCreated")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const reviewMutation = useMutation({
    mutationFn: (input: { fact: WorkFact; status: string; version: number }) => reviewWorkFact(token || null, contextId!, input.fact.id, input.status, input.version),
    onSuccess: () => { invalidateDetail(); setEditor(undefined); setReviewTarget(undefined); void message.success(tr("saved")); },
    onError: (error) => (isConflict(error) ? void message.error(tr("conflict409")) : fail(error, "saveFailed")),
  });

  const promoteMutation = useMutation({
    mutationFn: (fact: WorkFact) => promoteWorkFactMemoryCandidate(token || null, contextId!, fact.id),
    onSuccess: () => { invalidateDetail(); void message.success(tr("promoted")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const organizeMutation = useMutation({
    // On-demand AI maintenance: a real queued turn on the linked session, not a
    // client-side shuffle. Files inside the sweep stay maintenance-only.
    mutationFn: () => organizeWorkContext(token || null, contextId!),
    onSuccess: () => { invalidateDetail(); void message.success(tr("organized")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  // Plan items are edited in place; every mutation refreshes the focus query so
  // the plan and the "what changed" list stay consistent.
  const refreshFocus = () => { void focusQuery.refetch(); invalidateDetail(); };
  const todoCreateMutation = useMutation({
    mutationFn: (input: WorkTodoInput) => createWorkTodo(token || null, contextId!, input),
    onSuccess: () => refreshFocus(),
    onError: (error) => fail(error, "saveFailed"),
  });
  const todoPatchMutation = useMutation({
    mutationFn: (input: { todo: WorkTodo; patch: { text?: string; status?: WorkTodoStatus; owner?: string; due_at?: string } }) =>
      patchWorkTodo(token || null, contextId!, input.todo.id, { ...input.patch, if_version: input.todo.version }),
    onSuccess: () => refreshFocus(),
    onError: (error) => (isConflict(error) ? void message.error(tr("conflict409")) : fail(error, "saveFailed")),
  });
  const todoDeleteMutation = useMutation({
    mutationFn: (todo: WorkTodo) => deleteWorkTodo(token || null, contextId!, todo.id),
    onSuccess: () => { refreshFocus(); void message.success(tr("todoDeleted")); },
    onError: (error) => fail(error, "deleteFailed"),
  });

  // Resource edits reuse the same upsert endpoint with the existing id so a shared
  // resource is updated, not duplicated.
  const resourceUpdateMutation = useMutation({
    mutationFn: (input: { resource: WorkResource; patch: Partial<Pick<WorkResource, "title" | "kind" | "locator" | "purpose">> }) =>
      addWorkResource(token || null, contextId!, {
        id: input.resource.id,
        title: input.patch.title ?? input.resource.title,
        kind: input.patch.kind ?? input.resource.kind,
        locator: input.patch.locator ?? input.resource.locator,
        purpose: input.patch.purpose ?? input.resource.purpose,
      }),
    onSuccess: () => { invalidateAll(); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const markSeenMutation = useMutation({
    mutationFn: () => markWorkContextSeen(token || null, contextId!),
    onSuccess: () => { void focusQuery.refetch(); void message.success(tr("seenMarked")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const sessionMutation = useMutation({
    mutationFn: (values: EditorValues) => linkWorkSession(token || null, contextId!, { topic: values.topic!.trim(), workspace_dir: values.workspaceDir?.trim() ?? "", session_id: values.sessionId?.trim() ?? "" }),
    onSuccess: () => { invalidateDetail(); setEditor(undefined); void message.success(tr("saved")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const unlinkSessionMutation = useMutation({
    mutationFn: (sessionId: string) => unlinkWorkSession(token || null, contextId!, sessionId),
    onSuccess: () => { invalidateDetail(); void message.success(tr("sessionUnlinked")); },
    onError: (error) => fail(error, "deleteFailed"),
  });

  const delegationMutation = useMutation({
    mutationFn: (values: EditorValues) => createWorkDelegation(token || null, contextId!, {
      // request_id makes the create idempotent: a retry after a dropped response does not
      // create a second card for the same delegation (design §5.3 / WD2 acceptance).
      request_id: `${contextId}-${crypto.randomUUID()}`,
      title: values.title!.trim(),
      description: values.description?.trim() ?? "",
      acceptance: splitLines(values.acceptance),
      project_id: values.project,
      work_dir: values.workDir?.trim() ?? "",
      template_id: values.template,
    }),
    onSuccess: () => { invalidateDetail(); setEditor(undefined); setParams({ context: contextId, tab: "tasks" }); void message.success(tr("delegationCreated")); },
    onError: (error) => fail(error, "createFailed"),
  });

  const executeMutation = useMutation({
    mutationFn: (cardId: string) => executeTaskboardCard(token || null, cardId),
    onSuccess: () => { void detailQuery.refetch(); void message.success(tr("executionStarted")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const reviewCardMutation = useMutation({
    mutationFn: (input: { card: TaskboardCard; action: "complete" | "reject"; reason?: string }) =>
      patchTaskboardCard(token || null, input.card.id, { action: input.action, version: input.card.version, reason: input.reason }),
    onSuccess: () => { void detailQuery.refetch(); setEditor(undefined); setReviewTarget(undefined); void message.success(tr("reviewed")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  function submitEditor(values: EditorValues) {
    switch (editor) {
      case "create": return createMutation.mutate(values);
      case "editContext": return selected && patchContextMutation.mutate({ id: selected.id, version: selected.version, title: values.title!.trim(), background: values.background?.trim() ?? "", goal: values.goal!.trim() });
      case "checkpoint":
        if (!values.summary?.trim() || !splitLines(values.next).length) { void message.warning(tr("checkBlank")); return; }
        return checkpointMutation.mutate(values);
      case "delegate": return delegationMutation.mutate(values);
      case "session": return sessionMutation.mutate(values);
      case "fact": return factMutation.mutate(values);
      case "resource": return resourceMutation.mutate(values);
      case "relation": return relationMutation.mutate(values);
      case "review": return reviewTarget && reviewMutation.mutate({ fact: reviewTarget, status: "rejected", version: reviewTarget.version });
      default: return undefined;
    }
  }

  async function openSession(sessionId: string) {
    try {
      const sessions = await listSessions(token || null);
      const target = sessions.find((item) => item.session_id === sessionId);
      // The stored session_id alone is not a locator: a partial locator would re-hash into
      // a different session and open an empty chat, so a missing match is reported instead.
      if (!target) { void message.warning(tr("sessionMissing")); return; }
      navigate(buildChatRouteForSession(target));
    } catch (error) {
      fail(error, "sessionJumpFailed");
    }
  }

  const filteredContexts = useMemo(() => {
    const needle = search.trim().toLocaleLowerCase();
    if (!needle) return contexts;
    return contexts.filter((item) => `${item.title} ${item.background} ${item.goal}`.toLocaleLowerCase().includes(needle));
  }, [contexts, search]);

  const contextResources = useMemo(
    () => (resourcesQuery.data ?? []).filter((resource) => resource.context_ids.includes(contextId ?? "")),
    [resourcesQuery.data, contextId],
  );
  const detail: WorkContextDetail | undefined = detailQuery.data;
  const facts = recallResult.data ?? detail?.facts ?? [];
  const relations = detail?.relations ?? [];
  const sessions = detail?.sessions ?? [];
  const delegations = detail?.delegations ?? [];
  const events = detail?.events ?? [];
  const todos = focusQuery.data?.todos ?? detail?.todos ?? [];
  const focusChanges = focusQuery.data?.changes ?? [];
  const lastSeenAt = focusQuery.data?.last_seen_at ?? "";
  const attentionItems = attentionQuery.data?.items ?? [];
  const unreadByContext = attentionQuery.data?.unread ?? {};
  const pendingReview = delegationsNeedingReview(delegations);
  // The chat dock and file browser reuse the stream's linked session and its
  // project directory, so opening either one keeps the working context instead
  // of navigating away to the Chat or Files page.
  const chatSessionId = sessions[0]?.session_id ?? "";
  const filesRoot = useMemo(
    () => contextResources.find((resource) => resource.kind === "directory" && resource.locator.trim())?.locator.trim() ?? "",
    [contextResources],
  );

  if (contextsQuery.isLoading) {
    return <div className="wd-page wd-center"><Spin /> <Typography.Text type="secondary">{tr("loading")}</Typography.Text></div>;
  }
  if (contextsQuery.isError) {
    return <div className="wd-page"><Alert type="error" showIcon title={tr("loadFailed")} description={(contextsQuery.error as Error)?.message} action={<Button size="small" onClick={() => void contextsQuery.refetch()}>{tr("retry")}</Button>} /></div>;
  }
  if (!contexts.length) {
    return <div className="wd-page wd-center">
      <Card title={<Space><ApartmentOutlined />{tr("noContexts")}</Space>} className="wd-empty-card">
        <Alert className="wd-store" type="info" showIcon title={tr("store")} description={tr("storeHint")} />
        <Typography.Paragraph type="secondary">{tr("noContextsHint")}</Typography.Paragraph>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => openEditor("create")}>{tr("newContext")}</Button>
      </Card>
      {editorModal()}
    </div>;
  }

  return <div className="wd-page">
    {invalidContext && <Alert className="wd-notice" type="warning" showIcon title={tr("invalidContext")} />}
    <div className="wd-layout">
      <aside className="wd-streams" aria-label={tr("contexts")}>
        <div className="wd-item-heading"><Typography.Text strong>{tr("contexts")}</Typography.Text><Badge count={contexts.length} color="var(--godex-accent)" /></div>
        <Input aria-label={tr("searchContexts")} placeholder={tr("searchContexts")} prefix={<SearchOutlined aria-hidden="true" />} value={search} onChange={(event) => setSearch(event.target.value)} allowClear />
        <Select className="wd-mobile-switch" aria-label={tr("contexts")} value={contextId} options={contexts.map((item) => ({ value: item.id, label: item.title }))} onChange={selectContext} />
        <nav className="wd-stream-list">{filteredContexts.length ? filteredContexts.map((item) => <button type="button" key={item.id} className={`wd-stream ${contextId === item.id ? "wd-stream-selected" : ""}`} aria-current={contextId === item.id ? "page" : undefined} onClick={() => selectContext(item.id)}>
          <span className="wd-item-heading"><strong>{item.title}</strong><Space size={4} align="center">{(unreadByContext[item.id] ?? 0) > 0 && <Badge count={unreadByContext[item.id]} size="small" title={tr("attentionUnread", { count: unreadByContext[item.id] })} />}<span className={`wd-status-dot wd-status-${item.status}`} /></Space></span>
          <span className="wd-stream-summary">{item.checkpoint.summary || item.goal || item.background}</span>
          <span><Tag color={statusColor[item.status] ?? "default"}>{trLabel("", item.status)}</Tag><span className="wd-muted">{tr("revision", { revision: item.checkpoint.revision })}</span></span>
        </button>) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("emptyContexts")} />}</nav>
        <Button block icon={<PlusOutlined aria-hidden="true" />} onClick={() => openEditor("create")}>{tr("newContext")}</Button>
      </aside>
      <main className="wd-main">
        <AttentionInbox items={attentionItems} loading={attentionQuery.isLoading} onOpen={selectContextTab} />
        <header className="wd-context-header">
          <div className="wd-title-row">
            <Space size={8} align="center" wrap>
              <Typography.Title level={4} className="wd-title">{selected!.title}</Typography.Title>
              <Tag color={statusColor[selected!.status] ?? "default"}>{trLabel("", selected!.status)}</Tag>
              {pendingReview > 0 && <Tag color="gold" icon={<CheckCircleOutlined aria-hidden="true" />}>{tr("pendingCount", { count: pendingReview })}</Tag>}
            </Space>
            <Space size={6} wrap>
              <InfoHint title={tr("maintain")} text={tr("maintainHint")} />
              <Button size="small" type="primary" icon={<PlayCircleOutlined aria-hidden="true" />} onClick={() => setBriefOpen(true)}>{tr("continueWork")}</Button>
              <Button size="small" icon={<EditOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("checkpoint")}>{tr("checkpoint")}</Button>
              <Button size="small" icon={<PlusOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("delegate")}>{tr("delegate")}</Button>
              <Button size="small" icon={<EditOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("editContext")}>{tr("editContext")}</Button>
            </Space>
          </div>
          <Typography.Paragraph type="secondary" className="wd-subtitle">{selected!.goal || selected!.background}</Typography.Paragraph>
        </header>
        <div className="wd-stat-row">
          <span><FolderOutlined aria-hidden="true" /> {tr("resourcesCount", { count: contextResources.length })}</span>
          <span><CommentOutlined aria-hidden="true" /> {tr("sessionsCount", { count: sessions.length })}</span>
          <span><ProfileOutlined aria-hidden="true" /> {tr("tasksCount", { count: delegations.length })}</span>
          <span><ClockCircleOutlined aria-hidden="true" /> {tr("savedAt")} · {formatTimestamp(selected!.checkpoint.created_at) || tr("noItems")}</span>
        </div>
        {readOnly && <Alert className="wd-notice" type="warning" showIcon title={tr("readOnly")} />}
        {detailQuery.isError && <Alert className="wd-notice" type="error" showIcon title={tr("loadFailed")} action={<Button size="small" onClick={() => void detailQuery.refetch()}>{tr("retry")}</Button>} />}
        <Tabs activeKey={tab} onChange={(key) => setParams({ context: selected!.id, tab: key })} items={[
          { key: "overview", label: tr("overview"), children: overviewView() },
          { key: "plan", label: <Space size={4}><ProfileOutlined aria-hidden="true" />{tr("todoTitle")}</Space>, children: planView() },
          { key: "chat", label: <Space size={4}><MessageOutlined aria-hidden="true" />{tr("chatTab")}</Space>, children: chatView() },
          { key: "files", label: <Space size={4}><CodeOutlined aria-hidden="true" />{tr("filesTab")}</Space>, children: filesView() },
          { key: "conversations", label: <Space size={4}><CommentOutlined aria-hidden="true" />{tr("conversations")}</Space>, children: conversationsView() },
          { key: "tasks", label: tr("tasks"), children: tasksView() },
          { key: "knowledge", label: tr("knowledge"), children: knowledgeView() },
          { key: "timeline", label: tr("timeline"), children: timelineView() },
        ]} />
      </main>
      <aside className="wd-sidebar" aria-label={tr("resourcesTitle")}>
        <Card size="small" title={<Space size={6}><LinkOutlined aria-hidden="true" />{tr("resourcesTitle")}<InfoHint title={tr("showHint")} text={tr("resourceHint")} /></Space>} extra={<Button size="small" type="link" disabled={readOnly} onClick={() => openEditor("resource")}>{tr("addResource")}</Button>}>
          <ResourceChips
            resources={contextResources}
            readOnly={readOnly}
            onUpdate={(resource, patch) => resourceUpdateMutation.mutateAsync({ resource, patch })}
            onRemove={(resource) => { if (window.confirm(tr("removeResourceConfirm"))) removeResourceMutation.mutate(resource.id); }}
            onOpenFiles={() => setParams({ context: contextId!, tab: "files" })}
          />
        </Card>
        <Card size="small" title={<Space size={6}><ApartmentOutlined aria-hidden="true" />{tr("relationsTitle")}<InfoHint title={tr("showHint")} text={tr("relationHint")} /></Space>} extra={<Button size="small" type="link" disabled={readOnly || contexts.length < 2} onClick={() => openEditor("relation")}>{tr("addRelation")}</Button>}>
          {relationList()}
        </Card>
        <Card size="small"><Space wrap>{readOnly
          ? <Button type="primary" onClick={() => patchContextMutation.mutate({ id: selected!.id, version: selected!.version, status: "active" })}>{tr("restore")}</Button>
          : <>
            <Button onClick={() => patchContextMutation.mutate({ id: selected!.id, version: selected!.version, status: selected!.status === "active" ? "paused" : "active" })}>{tr(selected!.status === "active" ? "pause" : "resume")}</Button>
            <Button danger onClick={() => { if (window.confirm(tr("archive"))) patchContextMutation.mutate({ id: selected!.id, version: selected!.version, status: "archived" }); }}>{tr("archive")}</Button>
          </>}
        </Space></Card>
      </aside>
    </div>

    <Drawer zIndex={1300} styles={{ wrapper: { maxWidth: "100vw" } }} title={tr("briefTitle")} open={briefOpen} onClose={() => setBriefOpen(false)} size={600}>
      {briefQuery.isLoading ? <Spin /> : briefQuery.data ? briefBody(briefQuery.data) : <Alert type="error" showIcon title={tr("loadFailed")} />}
    </Drawer>

    {editorModal()}
  </div>;

  function briefBody(brief: WorkContextBrief) {
    const checkpoint = brief.context.checkpoint;
    return <div className="wd-stack">
      <Alert type="info" showIcon title={tr("briefHint")} />
      <Typography.Title level={4}>{brief.context.title}</Typography.Title>
      <Typography.Paragraph>{brief.context.background || tr("noItems")}</Typography.Paragraph>
      <Typography.Text strong>{tr("goal")}</Typography.Text><Typography.Paragraph>{brief.context.goal}</Typography.Paragraph>
      <Typography.Text type="secondary">{tr("basedOnRevision", { revision: checkpoint.revision })}</Typography.Text>
      {bullets(tr("completed"), checkpoint.completed)}
      {bullets(tr("inProgress"), checkpoint.in_progress)}
      {bullets(tr("next"), checkpoint.next_actions)}
      {bullets(tr("blockers"), checkpoint.blockers)}
      {bullets(tr("openQuestions"), checkpoint.open_questions)}
      {bullets(tr("changes"), brief.events.slice(0, 5).map((event) => `${event.title} · ${formatTimestamp(event.created_at)}`))}
      {brief.facts.length > 0 && <section className="wd-section"><Typography.Text strong>{tr("evidence")}</Typography.Text>{brief.facts.map((fact) => <div className="wd-fact-line" key={fact.id}><Tag color={factStatusColor[fact.status] ?? "default"}>{trLabel("factStatus", fact.status)}</Tag>{fact.title}<div className="wd-muted">{formatTimestamp(fact.observed_at)} · {fact.scope || tr("noItems")}</div></div>)}</section>}
      {staleWarning(brief)}
      <Alert type="warning" showIcon title={tr("currentUnknown")} description={tr("currentUnknownHint")} />
    </div>;
  }

  function staleWarning(brief: WorkContextBrief) {
    const stale = brief.facts.filter((fact) => fact.status === "stale");
    return stale.length ? <Alert type="warning" showIcon title={tr("factStatus.stale")} description={stale.map((fact) => fact.title).join(" · ")} /> : null;
  }

  function bullets(title: string, items: string[]) {
    return <section className="wd-section">{title && <Typography.Text strong>{title}</Typography.Text>}{items.length ? <ul>{items.map((item, index) => <li key={index}>{item}</li>)}</ul> : <Typography.Paragraph type="secondary">{tr("noItems")}</Typography.Paragraph>}</section>;
  }

  function overviewView() {
    const checkpoint = selected!.checkpoint;
    return <div className="wd-stack">
      <Card size="small" className="wd-next-card" title={<Space><PlayCircleOutlined aria-hidden="true" />{tr("next")}</Space>} extra={<Tag>{tr("revision", { revision: checkpoint.revision })}</Tag>}>
        <Typography.Paragraph>{checkpoint.summary || tr("noItems")}</Typography.Paragraph>
        {checkpoint.next_actions.length ? <ol className="wd-next-list">{checkpoint.next_actions.map((item, index) => <li key={index}>{item}</li>)}</ol> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noItems")} />}
        <Typography.Text type="secondary">{tr("savedAt")} · {formatTimestamp(checkpoint.created_at) || tr("noItems")}</Typography.Text>
      </Card>
      <div className="wd-overview-grid">
        <Card size="small" title={<Space><FileTextOutlined aria-hidden="true" />{tr("background")}</Space>}><Typography.Paragraph>{selected!.background || tr("noItems")}</Typography.Paragraph><Typography.Text strong>{tr("goal")}</Typography.Text><Typography.Paragraph>{selected!.goal || tr("noItems")}</Typography.Paragraph></Card>
        <Card size="small" title={<Space><CheckCircleOutlined aria-hidden="true" />{tr("completed")}</Space>}>{bullets("", checkpoint.completed)}</Card>
      </div>
      <div className="wd-overview-grid">
        <Card size="small" title={<Space><PauseCircleOutlined aria-hidden="true" />{tr("blockers")}</Space>}>{bullets("", checkpoint.blockers)}{bullets("", checkpoint.open_questions)}</Card>
        <Card size="small" title={<Space size={6}><ClockCircleOutlined aria-hidden="true" />{tr("changes")}<InfoHint title={tr("showHint")} text={tr("changesHint")} /></Space>}>
          {events.length ? <div className="wd-stack">{events.slice(0, 3).map((event) => <div key={event.id}><Typography.Text>{event.title}</Typography.Text><div className="wd-muted">{formatTimestamp(event.created_at)}</div></div>)}</div> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noChanges")} />}
        </Card>
      </div>
      {changesCard()}
      <Typography.Paragraph type="secondary" className="wd-foot-note">
        <InfoHint title={tr("currentUnknown")} text={tr("currentUnknownHint")} /> {tr("currentUnknown")}
      </Typography.Paragraph>
    </div>;
  }

  // planView is the stream's plan: first-class items with status/owner/due, edited
  // in place. It is separate from the checkpoint's free-text next_actions.
  function planView() {
    return <div className="wd-stack">
      <div className="wd-inline-head">
        <Space size={6} align="center"><ProfileOutlined aria-hidden="true" /><Typography.Text strong>{tr("todoTitle")}</Typography.Text><InfoHint title={tr("showHint")} text={tr("inlineHint")} /></Space>
        <Typography.Text type="secondary">{tr("pendingCount", { count: todos.filter((item) => item.status !== "done").length })}</Typography.Text>
      </div>
      <TodoBoard
        todos={todos}
        readOnly={readOnly}
        onCreate={(input: WorkTodoInput) => todoCreateMutation.mutateAsync(input)}
        onPatch={(todo: WorkTodo, patch) => todoPatchMutation.mutateAsync({ todo, patch })}
        onDelete={(todo: WorkTodo) => todoDeleteMutation.mutateAsync(todo)}
      />
    </div>;
  }

  // changesCard surfaces what changed since the user last looked at the stream, so
  // picking the work back up does not require re-reading the timeline.
  function changesCard() {
    const seenLabel = lastSeenAt ? formatTimestamp(lastSeenAt) : tr("lastSeenNever");
    return <Card
      size="small"
      className="wd-focus-card"
      title={<Space size={6}><ThunderboltOutlined aria-hidden="true" />{tr("changesSinceTitle")}<span className="wd-muted">{seenLabel}</span></Space>}
      extra={!readOnly && focusChanges.length > 0 ? <Button size="small" loading={markSeenMutation.isPending} onClick={() => markSeenMutation.mutate()}>{tr("markSeen")}</Button> : null}
    >
      {focusChanges.length ? <div className="wd-stack">{focusChanges.slice(0, 8).map((event) => <div key={event.id}><Typography.Text>{event.title}</Typography.Text><div className="wd-muted">{event.kind} · {formatTimestamp(event.created_at)}</div></div>)}</div> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noChanges")} />}
    </Card>;
  }

  function chatView() {
    return <div className="wd-stack wd-chat-view">
      <div className="wd-inline-head">
        <Space size={6} align="center"><MessageOutlined aria-hidden="true" /><Typography.Text strong>{tr("chatTab")}</Typography.Text></Space>
        <Space size={8} align="center">
          <InfoHint title={tr("showHint")} text={tr("chatIntro")} />
          {!readOnly && <Button size="small" loading={organizeMutation.isPending} onClick={() => organizeMutation.mutate()}>{tr("organize")}</Button>}
        </Space>
      </div>
      {chatSessionId
        ? <WorkChatDock sessionId={chatSessionId} token={token || null} onTurnEnd={invalidateDetail} />
        : <div className="wd-center wd-chat-empty"><Empty description={tr("chatNoTopic")} />{!readOnly && <Button type="primary" icon={<PlusOutlined aria-hidden="true" />} onClick={() => openEditor("session")}>{tr("chatNewTopic")}</Button>}</div>}
    </div>;
  }

  function filesView() {
    if (!filesRoot) {
      return <div className="wd-center wd-chat-empty"><Empty description={tr("filesNoRoot")} />{!readOnly && <Button icon={<PlusOutlined aria-hidden="true" />} onClick={() => openEditor("resource")}>{tr("addResource")}</Button>}</div>;
    }
    return <div className="wd-files-view"><FilesPanel mode="dock" cwd={filesRoot} fillContainer /></div>;
  }

  function conversationsView() {
    return <div className="wd-stack">
      <div className="wd-inline-head">
        <Space size={6} align="center"><CommentOutlined aria-hidden="true" /><Typography.Text strong>{tr("conversations")}</Typography.Text><InfoHint title={tr("showHint")} text={tr("topicHint")} /></Space>
        <Button size="small" type="primary" icon={<PlusOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("session")}>{tr("newSession")}</Button>
      </div>
      {sessions.length ? <div className="wd-topic-records">{sessions.map((link) => <Card key={link.id} size="small" title={link.topic || tr("topicName")} extra={<Typography.Text type="secondary">{formatTimestamp(link.created_at)}</Typography.Text>}>
        <div className="wd-muted wd-wrap">{link.session_id}</div>
        <Space wrap style={{ marginTop: 12 }}>
          <Button size="small" icon={<CommentOutlined aria-hidden="true" />} onClick={() => void openSession(link.session_id)}>{tr("openChat")}</Button>
          {!readOnly && <Button size="small" danger onClick={() => { if (window.confirm(tr("unlinkConfirm"))) unlinkSessionMutation.mutate(link.session_id); }}>{tr("unlinkSession")}</Button>}
        </Space>
      </Card>)}</div> : <Empty description={tr("noSessions")} />}
    </div>;
  }

  function tasksView() {
    return <div className="wd-stack">
      <div className="wd-inline-head">
        <Space size={6} align="center"><ProfileOutlined aria-hidden="true" /><Typography.Text strong>{tr("tasks")}</Typography.Text><InfoHint title={tr("taskBoundary")} text={tr("delegationHint")} /></Space>
        <Space size={6}><Button size="small" type="primary" icon={<PlusOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("delegate")}>{tr("delegate")}</Button><Button size="small" icon={<ProfileOutlined aria-hidden="true" />} onClick={() => navigate("/taskboard")}>{tr("actualBoard")}</Button></Space>
      </div>
      {delegations.length ? delegations.map((delegation) => {
        const card = delegation.card ?? undefined;
        return <Card size="small" className="wd-task" key={delegation.id} title={<Space wrap><span>{delegation.title}</span>{card && <Tag color={cardStatusColor[card.status] ?? "default"}>{trLabel("cardStatus", card.status)}</Tag>}</Space>}>
          <div className="wd-muted">{tr("acceptance")}</div>
          <div>{card?.checklist?.length ? <ul>{card.checklist.map((item, index) => <li key={index}>{item.text}</li>)}</ul> : <Typography.Paragraph type="secondary">{delegation.pack || tr("noItems")}</Typography.Paragraph>}</div>
          {!card && <Alert type="warning" showIcon title={tr("cardMissing")} />}
          {card && <div className="wd-muted">{tr("cardState")} · {trLabel("cardStatus", card.status)} · {card.id}</div>}
          <Space wrap style={{ marginTop: 12 }}>
            {card && !readOnly && <Button size="small" type="primary" icon={<ThunderboltOutlined aria-hidden="true" />} onClick={() => executeMutation.mutate(card.id)}>{tr("execute")}</Button>}
            {card && !readOnly && card.status === "in_review" && <>
              <Button size="small" onClick={() => reviewCardMutation.mutate({ card, action: "complete" })}>{tr("reviewAccept")}</Button>
              <Button size="small" danger onClick={() => reviewCardMutation.mutate({ card, action: "reject", reason: tr("reviewReject") })}>{tr("reviewReject")}</Button>
            </>}
            <Button size="small" icon={<ProfileOutlined aria-hidden="true" />} onClick={() => navigate("/taskboard")}>{tr("actualBoard")}</Button>
          </Space>
        </Card>;
      }) : <Empty description={tr("noTasks")} />}
    </div>;
  }

  function knowledgeView() {
    return <div className="wd-stack">
      <div className="wd-inline-head">
        <Space size={6} align="center"><FileTextOutlined aria-hidden="true" /><Typography.Text strong>{tr("knowledge")}</Typography.Text><InfoHint title={tr("showHint")} text={`${tr("recallHint")} ${tr("governanceHint")}`} /></Space>
        <Space size={6}><Typography.Text type="secondary">{tr("searchCount", { count: facts.length })}</Typography.Text><Button size="small" icon={<PlusOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("fact")}>{tr("addFact")}</Button></Space>
      </div>
      <div className="wd-recall-tools">
        <Input prefix={<SearchOutlined aria-hidden="true" />} aria-label={tr("recallPlaceholder")} placeholder={tr("recallPlaceholder")} value={recallQuery} allowClear onChange={(event) => setRecallQuery(event.target.value)} />
        <Select aria-label={tr("recallScope")} value={recallScope} onChange={setRecallScope} options={[{ value: "current", label: tr("scopeCurrent") }, { value: "related", label: tr("scopeRelated") }, { value: "all", label: tr("scopeAll") }]} />
      </div>
      {recallResult.isLoading ? <Spin /> : facts.length ? facts.map((fact) => <Card key={fact.id} size="small" className="wd-knowledge" title={<Space wrap><Tag>{trLabel("factKind", fact.kind)}</Tag><span>{fact.title}</span></Space>} extra={<Tag color={factStatusColor[fact.status] ?? "default"}>{trLabel("factStatus", fact.status)}</Tag>}>
        <Typography.Paragraph>{fact.content}</Typography.Paragraph>
        <div className="wd-muted">{tr("factScopeLabel")} · {fact.scope || tr("noItems")}</div>
        <div className="wd-muted">{tr("factObserved")} · {formatTimestamp(fact.observed_at) || tr("noItems")}</div>
        {fact.verified_at && <div className="wd-muted">{tr("verifiedAt")} · {formatTimestamp(fact.verified_at)}</div>}
        {fact.source && <div className="wd-muted">{tr("factSource")} · <Typography.Text code className="wd-wrap">{fact.source}</Typography.Text></div>}
        {fact.source_excerpt && <Typography.Paragraph type="secondary">“{fact.source_excerpt}”</Typography.Paragraph>}
        {fact.supersedes_id && <div className="wd-muted">{tr("factSupersedes")} · {fact.supersedes_id}</div>}
        <Space wrap style={{ marginTop: 12 }}>
          {contexts.find((item) => item.id === fact.context_id) && <Tag>{contexts.find((item) => item.id === fact.context_id)!.title}</Tag>}
          {fact.status === "candidate" && !readOnly && <>
            <Button size="small" type="primary" onClick={() => reviewMutation.mutate({ fact, status: "confirmed", version: fact.version })}>{tr("accept")}</Button>
            <Button size="small" danger onClick={() => { setReviewTarget(fact); openEditor("review"); }}>{tr("dismiss")}</Button>
          </>}
          {!fact.memory_candidate_id && fact.status === "confirmed" && !readOnly && <Button size="small" icon={<ThunderboltOutlined aria-hidden="true" />} onClick={() => promoteMutation.mutate(fact)}>{tr("promoteMemory")}</Button>}
          {fact.memory_candidate_id && <Tag color="purple">{tr("memoryCandidate")}</Tag>}
        </Space>
      </Card>) : <Empty description={tr("noFacts")} />}
    </div>;
  }

  function timelineView() {
    return <div className="wd-stack">
      <div className="wd-inline-head">
        <Space size={6} align="center"><ClockCircleOutlined aria-hidden="true" /><Typography.Text strong>{tr("timeline")}</Typography.Text><InfoHint title={tr("showHint")} text={tr("timelineHint")} /></Space>
      </div>
      {events.length ? <Timeline items={events.map((event) => ({ content: <div><Typography.Text strong>{event.title}</Typography.Text><div className="wd-muted">{event.source || tr("noItems")} · {formatTimestamp(event.created_at)}</div></div> }))} /> : <Empty description={tr("noChanges")} />}
    </div>;
  }

  function relationList() {
    if (!relations.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noRelations")} />;
    return relations.map((relation) => {
      const otherId = otherSide(relation, contextId!);
      const other = contexts.find((item) => item.id === otherId);
      const directed = relation.kind === "depends_on";
      const label = (id: string) => (id === contextId ? selected!.title : contexts.find((item) => item.id === id)?.title ?? id);
      return <div className="wd-relation" key={relation.id}>
        <Tag color={directed ? "orange" : "default"}>{trLabel("relationKind", relation.kind)}</Tag>
        {directed && <div className="wd-muted">{label(relation.from_id)} → {label(relation.to_id)}</div>}
        <Button type="link" disabled={!other} onClick={() => other && selectContext(other.id)}>{other?.title ?? otherId} <ArrowRightOutlined aria-hidden="true" /></Button>
        {relation.condition && <div className="wd-muted">{relation.condition}</div>}
        {!readOnly && <Button size="small" danger onClick={() => removeRelationMutation.mutate(relation.id)}>{tr("removeRelation")}</Button>}
      </div>;
    });
  }

  function editorModal() {
    const title = editor === "create" ? tr("newContext")
      : editor === "editContext" ? tr("editContext")
      : editor === "checkpoint" ? tr("checkpoint")
      : editor === "delegate" ? tr("delegate")
      : editor === "session" ? tr("newSession")
      : editor === "fact" ? tr("addFact")
      : editor === "resource" ? tr("addResource")
      : editor === "relation" ? tr("addRelation")
      : tr("reviewReject");
    const submitLabel = editor === "create" ? tr("create") : editor === "delegate" ? tr("delegate") : editor === "review" ? tr("reviewReject") : tr("save");
    return <Modal zIndex={1300} title={title} open={!!editor} onCancel={() => { setEditor(undefined); setReviewTarget(undefined); }} footer={null} width={680} destroyOnHidden>
      <Form form={form} layout="vertical" onFinish={submitEditor}>
        {editor === "create" && <>
          <Alert className="wd-store" type="info" showIcon title={tr("createHint")} />
          <Form.Item label={tr("contextName")} name="title" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={160} autoFocus /></Form.Item>
          <Form.Item label={tr("background")} name="background"><Input.TextArea rows={3} maxLength={4000} /></Form.Item>
          <Form.Item label={tr("goal")} name="goal" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={2} maxLength={2000} /></Form.Item>
        </>}
        {editor === "editContext" && <>
          <Form.Item label={tr("contextName")} name="title" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={160} autoFocus /></Form.Item>
          <Form.Item label={tr("background")} name="background"><Input.TextArea rows={3} maxLength={4000} /></Form.Item>
          <Form.Item label={tr("goal")} name="goal" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={2} maxLength={2000} /></Form.Item>
          <Typography.Text type="secondary">{tr("revision", { revision: selected?.checkpoint.revision ?? 0 })}</Typography.Text>
        </>}
        {editor === "checkpoint" && <>
          <Form.Item name="summary" label={tr("checkpointSummary")} rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={3} maxLength={4000} /></Form.Item>
          <Form.Item name="next" label={tr("checkpointNext")} rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={2} maxLength={4000} /></Form.Item>
          <Form.Item name="completed" label={tr("checkpointCompleted")}><Input.TextArea rows={2} maxLength={4000} /></Form.Item>
          <Form.Item name="inProgress" label={tr("checkpointInProgress")}><Input.TextArea rows={2} maxLength={4000} /></Form.Item>
          <Form.Item name="blockers" label={tr("checkpointBlockers")}><Input.TextArea rows={2} maxLength={4000} /></Form.Item>
          <Form.Item name="openQuestions" label={tr("checkpointOpenQuestions")}><Input.TextArea rows={2} maxLength={4000} /></Form.Item>
          <Form.Item name="evidenceRefs" label={tr("checkpointEvidenceRefs")}><Input.TextArea rows={2} maxLength={4000} /></Form.Item>
        </>}
        {editor === "delegate" && <>
          <Form.Item label={tr("delegateTitle")} name="title" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={160} autoFocus placeholder={tr("delegateTitleHint")} /></Form.Item>
          <Form.Item label={tr("delegateDescription")} name="description"><Input.TextArea rows={3} maxLength={6000} /></Form.Item>
          <Form.Item label={tr("delegateAcceptance")} name="acceptance" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={3} maxLength={6000} /></Form.Item>
          <Form.Item label={tr("delegateProject")} name="project"><Select allowClear options={(projectsQuery.data?.projects ?? []).map((project) => ({ value: project.id, label: project.name }))} /></Form.Item>
          <Form.Item label={tr("delegateWorkDir")} name="workDir"><Input maxLength={400} /></Form.Item>
          <Form.Item label={tr("delegateTemplate")} name="template"><Select allowClear options={(templatesQuery.data ?? []).map((tpl: AgentTemplate) => ({ value: tpl.id, label: tpl.name }))} /></Form.Item>
          <Card size="small" title={tr("contextPack")}><Typography.Paragraph type="secondary">{tr("packHint")}</Typography.Paragraph><Typography.Text strong>{selected!.title}</Typography.Text><Typography.Paragraph>{selected!.checkpoint.summary}</Typography.Paragraph><Tag>{tr("revision", { revision: selected!.checkpoint.revision })}</Tag></Card>
        </>}
        {editor === "session" && <>
          <Form.Item label={tr("topicName")} name="topic" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={160} autoFocus /></Form.Item>
          <Form.Item label={tr("workspaceDir")} name="workspaceDir" extra={tr("workspaceDirHint")}><Input maxLength={400} /></Form.Item>
          <Form.Item label={tr("sessionIdLabel")} name="sessionId" extra={tr("sessionIdHint")}><Input maxLength={200} /></Form.Item>
        </>}
        {editor === "fact" && <>
          <Form.Item label={tr("factTitle")} name="title" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={200} autoFocus /></Form.Item>
          <Form.Item label={tr("factContent")} name="content" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={3} maxLength={4000} /></Form.Item>
          <Form.Item label={tr("factKindLabel")} name="factKind" initialValue="fact"><Select options={[{ value: "fact", label: tr("factKind.fact") }, { value: "decision", label: tr("factKind.decision") }, { value: "trace", label: tr("factKind.trace") }]} /></Form.Item>
          <Form.Item label={tr("factScopeLabel")} name="scope" extra={tr("governanceHint")}><Input maxLength={200} /></Form.Item>
          <Form.Item label={tr("factSubjectRef")} name="subject"><Input maxLength={200} /></Form.Item>
          <Form.Item label={tr("factPredicate")} name="predicate"><Input maxLength={200} /></Form.Item>
          <Form.Item label={tr("factSource")} name="source" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={600} /></Form.Item>
          <Form.Item label={tr("factSourceExcerpt")} name="sourceExcerpt"><Input.TextArea rows={2} maxLength={2000} /></Form.Item>
          <Form.Item label={tr("factObserved")} name="observedAt" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={60} placeholder="2026-10-30T09:00:00Z" /></Form.Item>
          <Form.Item label={tr("factProvenance")} name="provenance"><Input maxLength={200} /></Form.Item>
          <Form.Item label={tr("factSupersedes")} name="supersedes"><Input maxLength={200} /></Form.Item>
        </>}
        {editor === "resource" && <>
          <Alert className="wd-store" type="info" showIcon title={tr("sourceBoundary")} />
          <Form.Item label={tr("resourceTitle")} name="title" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={200} autoFocus /></Form.Item>
          <Form.Item label={tr("resourceKindLabel")} name="resourceKind" initialValue="web"><Select options={resourceKinds.map((kind) => ({ value: kind, label: trLabel("kind", kind) }))} /></Form.Item>
          <Form.Item label={tr("resourceLocator")} name="locator" rules={[{ ...nonBlank, message: tr("required") }]} extra={tr("resourceLocatorHint")}><Input maxLength={1000} /></Form.Item>
          <Form.Item label={tr("resourceNode")} name="node"><Input maxLength={200} /></Form.Item>
          <Form.Item label={tr("resourcePurpose")} name="purpose"><Input.TextArea rows={2} maxLength={1000} /></Form.Item>
        </>}
        {editor === "relation" && <>
          <Form.Item label={tr("relationTarget")} name="target" rules={[{ ...nonBlank, message: tr("required") }]}><Select options={contexts.filter((item) => item.id !== contextId).map((item) => ({ value: item.id, label: item.title }))} /></Form.Item>
          <Form.Item label={tr("relationKind")} name="relationKind" initialValue="related_to"><Select options={[{ value: "related_to", label: tr("relationKind.related_to") }, { value: "depends_on", label: tr("relationKind.depends_on") }]} /></Form.Item>
          <Form.Item label={tr("relationCondition")} name="condition" extra={tr("relationHint")}><Input.TextArea rows={2} maxLength={1000} /></Form.Item>
        </>}
        {editor === "review" && <>
          {reviewTarget && <Typography.Paragraph><Typography.Text strong>{reviewTarget.title}</Typography.Text></Typography.Paragraph>}
          <Form.Item label={tr("reviewReason")} name="reason" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={3} maxLength={2000} placeholder={tr("reviewReasonPlaceholder")} /></Form.Item>
        </>}
        <Space style={{ marginTop: 20 }}>
          <Button type="primary" htmlType="submit" loading={createMutation.isPending || checkpointMutation.isPending || delegationMutation.isPending || factMutation.isPending || reviewMutation.isPending || patchContextMutation.isPending}>{submitLabel}</Button>
          <Button onClick={() => { setEditor(undefined); setReviewTarget(undefined); }}>{tr("cancel")}</Button>
        </Space>
      </Form>
    </Modal>;
  }
}
