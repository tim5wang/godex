import { useMemo } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { executeTaskboardCard, listAgentTemplates, listSessions, listTaskboardProjects, patchTaskboardCard } from "../../lib/api";
import { buildChatRouteForSession } from "../../lib/chatRoutes";
import { showError } from "../../lib/notifications";
import { useI18n } from "../../i18n";
import { useSettingsStore } from "../../store/settings";
import { useNodeContextStore } from "../../store/nodeContext";
import {
  addWorkRelation,
  addWorkResource,
  createWorkContext,
  createWorkDelegation,
  createWorkFact,
  createWorkTodo,
  deleteWorkTodo,
  getWorkAttention,
  getWorkContext,
  getWorkContextBrief,
  getWorkContextSessionIndex,
  getWorkFocus,
  linkWorkSession,
  listWorkContexts,
  listWorkResources,
  markWorkContextSeen,
  organizeWorkContext,
  patchWorkContext,
  patchWorkTodo,
  promoteWorkFactMemoryCandidate,
  recallWorkFacts,
  recordWorkAttentionAction,
  removeWorkRelation,
  removeWorkResource,
  reviewWorkFact,
  saveCheckpoint,
  undoWorkAttentionAction,
  unlinkWorkSession,
  workContextQueryKey,
  type AttentionVerb,
  type CheckpointInput,
  type WorkContext,
  type WorkContextDetail,
  type WorkResource,
  type WorkTodo,
  type WorkTodoInput,
  type WorkTodoStatus,
} from "../../lib/apiWorkContext";
import { useWorkDesktopStore, sectionForTarget, type EditorValues } from "./workDesktopStore";
import { delegationsNeedingReview, filterContexts, isReadOnly, splitLines } from "./workDesktopView";

/**
 * Everything the work desktop needs from the server: one place for the queries and
 * mutations, consumed by BOTH the chat left rail (the desktop list) and the chat
 * right dock (the desktop boards).
 *
 * This hook exists because the fusion split one page into two components that are
 * not in a parent/child relation — the rail and the dock are siblings in the chat
 * layout. Duplicating ~350 lines of query/mutation wiring in both would guarantee
 * they drift; this is the single implementation.
 *
 * React Query makes calling it twice safe: identical query keys share one cache
 * entry and one in-flight request, so the rail and the dock do not double-fetch.
 * Mutations are per-call instances, which is harmless (they only fire on user
 * action, and the shared invalidation fans out through the query cache).
 */
export function useWorkDesktopData() {
  const { t } = useI18n();
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  const token = useSettingsStore((state) => state.token);
  const nodeID = useNodeContextStore((state) => state.nodeID);
  // Selection + UI filters live in the shared store so the rail and the dock agree
  // on which desktop is open without a common ancestor passing props down.
  const contextIdParam = useWorkDesktopStore((state) => state.contextId);
  const setContextId = useWorkDesktopStore((state) => state.setContextId);
  const briefOpen = useWorkDesktopStore((state) => state.briefOpen);
  const setBriefOpen = useWorkDesktopStore((state) => state.setBriefOpen);
  const recallQuery = useWorkDesktopStore((state) => state.recallQuery);
  const recallScope = useWorkDesktopStore((state) => state.recallScope);
  const attentionScope = useWorkDesktopStore((state) => state.attentionScope);
  const search = useWorkDesktopStore((state) => state.search);
  // Subscribed (not getState) so the two editor-dependent queries actually run when
  // the delegate modal opens: a getState read would never re-render, leaving the
  // project/template pickers permanently empty.
  const editor = useWorkDesktopStore((state) => state.editor);

  const tr = (key: string, vars?: Record<string, string | number>) => t(`workDesktop.${key}`, vars);
  // trLabel resolves a namespaced label and falls back to the raw server value when
  // the server introduces a value this build does not know about.
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
  // An unknown id must not silently fall through to "the first desktop" without
  // saying so: it is reported and the stale selection is dropped below.
  const invalidContext = !!contextIdParam && !contextsQuery.isFetching && !contexts.some((item) => item.id === contextIdParam);
  const selected = contexts.find((item) => item.id === contextIdParam) ?? contexts[0];
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
  // Read-only; the watermark only advances via the explicit mark-seen action.
  const focusQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "focus", contextId),
    queryFn: () => getWorkFocus(token || null, contextId!),
    enabled: !!contextId,
  });
  // Attention is cross-stream: what needs the user across every desktop, plus the
  // unread count per desktop (the rail badge). Polled so a card that finishes in
  // the background shows up without a manual refresh.
  const attentionQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "attention"),
    queryFn: () => getWorkAttention(token || null),
    refetchInterval: 60_000,
  });
  // Every stream's linked conversations in one call, so a rail card can say which
  // streams have a conversation running and which one just finished.
  const sessionIndexQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "session-index"),
    queryFn: () => getWorkContextSessionIndex(token || null),
    refetchInterval: 60_000,
  });
  // The conversation list is deliberately the SAME query key the chat page uses: one
  // cache entry and one in-flight request, so naming a stream's most recently used
  // conversation costs no second copy of the heaviest list on this page. It is polled
  // because "running" is a LIVE fact read off this list, and a backgrounded conversation
  // starting would otherwise stay invisible until something else happened to refetch.
  // staleTime matches the chat page's, so the two observers share one warm copy instead
  // of each forcing its own fetch on mount.
  const chatSessionsQuery = useQuery({
    queryKey: ["sessions", token, nodeID],
    queryFn: () => listSessions(token || null),
    refetchInterval: 60_000,
    staleTime: 5 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
  });
  const projectsQuery = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "projects"),
    queryFn: () => listTaskboardProjects(token || null),
    enabled: editor === "delegate",
  });
  const templatesQuery = useQuery({
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

  const createMutation = useMutation({
    mutationFn: (values: EditorValues) => createWorkContext(token || null, { title: values.title!.trim(), background: values.background?.trim() ?? "", goal: values.goal!.trim() }),
    onSuccess: (created) => {
      invalidateAll();
      useWorkDesktopStore.getState().closeEditor();
      setContextId(created.id);
      void message.success(tr("saved"));
    },
    onError: (error) => fail(error, "createFailed"),
  });

  const patchContextMutation = useMutation({
    mutationFn: (input: { id: string; version: number; status?: string; title?: string; background?: string; goal?: string }) =>
      patchWorkContext(token || null, input.id, { title: input.title, background: input.background, goal: input.goal, status: input.status as never, if_version: input.version }),
    onSuccess: () => { invalidateAll(); useWorkDesktopStore.getState().closeEditor(); void message.success(tr("saved")); },
    onError: (error) => (isConflict(error) ? void message.error(tr("conflict409")) : fail(error, "saveFailed")),
  });

  const checkpointMutation = useMutation({
    // A checkpoint is one versioned document, not per-field patches, so editing a
    // single field still submits the whole document with just that field replaced.
    mutationFn: (patch: Partial<CheckpointInput>) => {
      const current = selected?.checkpoint;
      return saveCheckpoint(token || null, contextId!, selected?.version ?? 0, {
        summary: patch.summary ?? current?.summary ?? "",
        completed: patch.completed ?? current?.completed ?? [],
        in_progress: patch.in_progress ?? current?.in_progress ?? [],
        next_actions: patch.next_actions ?? current?.next_actions ?? [],
        blockers: patch.blockers ?? current?.blockers ?? [],
        open_questions: patch.open_questions ?? current?.open_questions ?? [],
        evidence_refs: patch.evidence_refs ?? current?.evidence_refs ?? [],
      });
    },
    onSuccess: () => { invalidateDetail(); void message.success(tr("checkpointSaved")); },
    onError: (error) => (isConflict(error) ? void message.error(tr("conflict409")) : fail(error, "saveFailed")),
  });

  // Retitling an already-linked conversation reuses the same link endpoint: the
  // backend updates the topic of an existing link instead of adding a second one.
  const topicMutation = useMutation({
    mutationFn: (input: { sessionId: string; topic: string }) => linkWorkSession(token || null, contextId!, { topic: input.topic, session_id: input.sessionId }),
    onSuccess: () => { invalidateDetail(); void message.success(tr("saved")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const resourceMutation = useMutation({
    mutationFn: (values: EditorValues) => addWorkResource(token || null, contextId!, { title: values.title!.trim(), kind: values.resourceKind!.trim(), locator: values.locator!.trim(), node_id: values.node?.trim() ?? "", purpose: values.purpose?.trim() ?? "" }),
    onSuccess: () => { invalidateAll(); useWorkDesktopStore.getState().closeEditor(); void message.success(tr("resourceCreated")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const removeResourceMutation = useMutation({
    mutationFn: (resourceId: string) => removeWorkResource(token || null, contextId!, resourceId),
    onSuccess: () => { invalidateAll(); void message.success(tr("deleted")); },
    onError: (error) => fail(error, "deleteFailed"),
  });

  const relationMutation = useMutation({
    mutationFn: (values: EditorValues) => addWorkRelation(token || null, contextId!, { to_id: values.target!, kind: values.relationKind!.trim(), condition: values.condition?.trim() ?? "" }),
    onSuccess: () => { invalidateAll(); useWorkDesktopStore.getState().closeEditor(); void message.success(tr("relationCreated")); },
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
    onSuccess: () => { invalidateDetail(); useWorkDesktopStore.getState().closeEditor(); void message.success(tr("factCreated")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const reviewMutation = useMutation({
    mutationFn: (input: { factId: string; status: string; version: number }) => reviewWorkFact(token || null, contextId!, input.factId, input.status, input.version),
    onSuccess: () => { invalidateDetail(); useWorkDesktopStore.getState().closeEditor(); void message.success(tr("saved")); },
    onError: (error) => (isConflict(error) ? void message.error(tr("conflict409")) : fail(error, "saveFailed")),
  });

  const promoteMutation = useMutation({
    mutationFn: (factId: string) => promoteWorkFactMemoryCandidate(token || null, contextId!, factId),
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

  // Plan items are edited in place; every mutation refreshes the focus query so the
  // plan and the "what changed" list stay consistent.
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

  // Attention verbs. They refresh the inbox (the row moves between the working list
  // and the deferred list) but never touch the stream's version or seen watermark —
  // ruling on a row is not reading the stream, and it is not an edit of the work.
  const attentionActionMutation = useMutation({
    mutationFn: (values: { contextId: string; itemKey: string; verb: AttentionVerb; reason?: string; untilAt?: string }) =>
      recordWorkAttentionAction(token || null, values.contextId, {
        item_key: values.itemKey, verb: values.verb, reason: values.reason, until_at: values.untilAt,
      }),
    onSuccess: () => { void attentionQuery.refetch(); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const attentionUndoMutation = useMutation({
    mutationFn: (values: { contextId: string; itemKey: string }) =>
      undoWorkAttentionAction(token || null, values.contextId, values.itemKey),
    onSuccess: () => { void attentionQuery.refetch(); void message.success(tr("attentionUndone")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const sessionMutation = useMutation({
    mutationFn: (values: EditorValues) => linkWorkSession(token || null, contextId!, { topic: values.topic!.trim(), workspace_dir: values.workspaceDir?.trim() ?? "", session_id: values.sessionId?.trim() ?? "" }),
    onSuccess: () => { invalidateDetail(); useWorkDesktopStore.getState().closeEditor(); void message.success(tr("saved")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const unlinkSessionMutation = useMutation({
    mutationFn: (sessionId: string) => unlinkWorkSession(token || null, contextId!, sessionId),
    onSuccess: () => { invalidateDetail(); void message.success(tr("sessionUnlinked")); },
    onError: (error) => fail(error, "deleteFailed"),
  });

  const delegationMutation = useMutation({
    mutationFn: (values: EditorValues) => createWorkDelegation(token || null, contextId!, {
      // request_id makes the create idempotent: a retry after a dropped response does
      // not create a second card for the same delegation (design §5.3 / WD2 acceptance).
      request_id: `${contextId}-${crypto.randomUUID()}`,
      title: values.title!.trim(),
      description: values.description?.trim() ?? "",
      acceptance: splitLines(values.acceptance),
      project_id: values.project,
      work_dir: values.workDir?.trim() ?? "",
      template_id: values.template,
    }),
    onSuccess: () => { invalidateDetail(); useWorkDesktopStore.getState().closeEditor(); void message.success(tr("delegationCreated")); },
    onError: (error) => fail(error, "createFailed"),
  });

  const executeMutation = useMutation({
    mutationFn: (cardId: string) => executeTaskboardCard(token || null, cardId),
    onSuccess: () => { void detailQuery.refetch(); void message.success(tr("executionStarted")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const reviewCardMutation = useMutation({
    mutationFn: (input: { cardId: string; version: number; action: "complete" | "reject"; reason?: string }) =>
      patchTaskboardCard(token || null, input.cardId, { action: input.action, version: input.version, reason: input.reason }),
    onSuccess: () => { void detailQuery.refetch(); useWorkDesktopStore.getState().closeEditor(); void message.success(tr("reviewed")); },
    onError: (error) => fail(error, "saveFailed"),
  });

  const submitEditor = (values: EditorValues) => {
    const editor = useWorkDesktopStore.getState().editor;
    const reviewTargetId = useWorkDesktopStore.getState().reviewTargetId;
    switch (editor) {
      case "create": return createMutation.mutate(values);
      case "delegate": return delegationMutation.mutate(values);
      case "session": return sessionMutation.mutate(values);
      case "fact": return factMutation.mutate(values);
      case "resource": return resourceMutation.mutate(values);
      case "relation": return relationMutation.mutate(values);
      case "review": {
        const fact = (detailQuery.data?.facts ?? []).find((item) => item.id === reviewTargetId);
        return fact && reviewMutation.mutate({ factId: fact.id, status: "rejected", version: fact.version });
      }
      default: return undefined;
    }
  };

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
  // Per-stream counts of what needs the user, as one card can show them. Derived from the
  // SAME inbox response the board renders, so the card and the board can never disagree
  // about how much is waiting. A grouped row stands for `count` entries, so it contributes
  // its count rather than 1 — otherwise a collapsed row of five would read as one item.
  const attentionByContext = useMemo(() => {
    const out: Record<string, number> = {};
    for (const item of attentionItems) out[item.context_id] = (out[item.context_id] ?? 0) + (item.count ?? 1);
    return out;
  }, [attentionItems]);
  const unreadByContext = attentionQuery.data?.unread ?? {};
  const sessionsByContext = sessionIndexQuery.data?.sessions ?? {};
  const lastTurnByContext = sessionIndexQuery.data?.last_turn ?? {};
  const seenByContext = sessionIndexQuery.data?.seen ?? {};
  // Which conversations are executing RIGHT NOW. Read off the shared session list rather
  // than guessed from a timestamp: "running" is a live fact the server owns.
  const runningSessionIds = useMemo(
    () => new Set((chatSessionsQuery.data ?? []).filter((item) => item.running).map((item) => item.session_id)),
    [chatSessionsQuery.data],
  );
  // Per-session last activity, so "the conversation I used last" is decided by the
  // session's own activity and not by when the link was attached.
  const activityBySessionId = useMemo(() => {
    const out: Record<string, string> = {};
    for (const item of chatSessionsQuery.data ?? []) out[item.session_id] = item.last_activity_at || item.updated_at || "";
    return out;
  }, [chatSessionsQuery.data]);
  // Streams whose unread work has gone stale. Their count is still real, but the UI
  // demotes it from an alert to a muted summary: a marker nobody clears stops being
  // a request and becomes background debt.
  const agedByContext = attentionQuery.data?.aged ?? {};
  const attentionMetrics = attentionQuery.data?.metrics;
  // Rows the owner already ruled on. Kept separate from scopedAttention so a deferred
  // row is never counted or rendered as work still waiting on them.
  const deferredAttention = attentionQuery.data?.deferred ?? [];
  const scopedAttention = attentionScope === "all" ? attentionItems : attentionItems.filter((item) => item.context_id === contextId);
  // Deferred rows are scoped by the SAME switch as the list, so "this desktop" does not
  // show verdicts taken on other streams.
  const scopedDeferred = attentionScope === "all" ? deferredAttention : deferredAttention.filter((entry) => entry.item.context_id === contextId);
  const pendingReview = delegationsNeedingReview(delegations);
  const filteredContexts = useMemo(() => filterContexts(contexts, search), [contexts, search]);
  const contextResources = useMemo(
    () => (resourcesQuery.data ?? []).filter((resource) => resource.context_ids.includes(contextId ?? "")),
    [resourcesQuery.data, contextId],
  );
  // The directory the desktop's file browser opens: its only directory resource.
  const filesRoot = useMemo(
    () => contextResources.find((resource) => resource.kind === "directory" && resource.locator.trim())?.locator.trim() ?? "",
    [contextResources],
  );

  return {
    // selection + derived
    contexts, selected, contextId, readOnly, invalidContext, filteredContexts,
    detail, facts, relations, sessions, delegations, events, todos, focusChanges, lastSeenAt,
    attentionItems, unreadByContext, agedByContext, attentionMetrics, deferredAttention, scopedAttention, scopedDeferred, pendingReview, contextResources, filesRoot,
    sessionsByContext, lastTurnByContext, seenByContext, runningSessionIds, activityBySessionId, attentionByContext,
    // query state (for loading/error surfaces)
    contextsQuery, detailQuery, attentionQuery, recallResult, briefQuery, focusQuery, projectsQuery, templatesQuery, sessionIndexQuery,
    // mutations
    createMutation, patchContextMutation, checkpointMutation, topicMutation,
    resourceMutation, removeResourceMutation, resourceUpdateMutation,
    relationMutation, removeRelationMutation, factMutation, reviewMutation, promoteMutation,
    organizeMutation, todoCreateMutation, todoPatchMutation, todoDeleteMutation,
    markSeenMutation, attentionActionMutation, attentionUndoMutation, sessionMutation, unlinkSessionMutation, delegationMutation,
    executeMutation, reviewCardMutation,
    // helpers
    submitEditor, invalidateAll, invalidateDetail, tr, trLabel, fail, isConflict,
    setContextId, briefOpen, setBriefOpen, recallQuery, recallScope, attentionScope, search,
  };
}

export type WorkDesktopData = ReturnType<typeof useWorkDesktopData>;
export type { WorkContext };

/**
 * Open a linked conversation in the middle chat column.
 *
 * A stored session_id is not a locator, so it is resolved against the server's own
 * session list first: a partial locator would re-hash into a DIFFERENT session and
 * silently open an empty chat. A missing match is reported instead of navigating.
 *
 * The fused desktop deliberately navigates the chat route (rather than embedding a
 * second chat): the middle column already IS the chat, so "open this conversation"
 * means "make it the open conversation" — one chat implementation, not two.
 */
export function useOpenWorkConversation() {
  const navigate = useNavigate();
  const { message } = AntApp.useApp();
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  return async (sessionId: string) => {
    try {
      const sessions = await listSessions(token || null);
      const target = sessions.find((item) => item.session_id === sessionId);
      if (!target) { void message.warning(t("workDesktop.sessionMissing")); return; }
      navigate(buildChatRouteForSession(target));
    } catch (error) {
      showError(message, error, t("workDesktop.sessionJumpFailed"));
    }
  };
}
