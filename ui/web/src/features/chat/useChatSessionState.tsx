import { App as AntApp, Grid, Space, Tooltip, Button, Divider, Typography, Alert, Select, Badge, Drawer, Spin, Tag } from "antd";
import { useParams, useSearchParams, useLocation, useNavigate, Link } from "react-router-dom";
import { useQueryClient, useQuery, useMutation } from "@tanstack/react-query";
import { useI18n } from "../../i18n";
import { useSettingsStore } from "../../store/settings";
import { useNodeContextStore } from "../../store/nodeContext";
import { useChatStore, groupFeedItemsIntoTurns } from "../../store/chat";
import { useState, useRef, useEffect, useCallback, type PointerEvent as ReactPointerEvent, useMemo, type CSSProperties } from "react";
import { useLayoutStore } from "../../store/layout";
import type { SessionTimelineEntry, DurableSubagentReview, DurableSubagentMerge, FeedItem, ListedSession } from "../../lib/types";
import { type ReviewMergeFilter, buildReviewMergeSummary, defaultReviewMergeJobId, shouldAutoLoadReview } from "./reviewMergeCenter";
import { useConversationLayoutStore, type DockTab, DOCK_TABS } from "./layout/layoutStore";
import { getMeta, openSession, getNote, saveNote, getSnapshot, getSessionTimeline, getSessionTimelinePage, getSessionCompactions, listSessionSubagents, listSessionLongTasks, listPackageCommands, listCommands, listPackageRoles, getSessionContextInspector, getActiveSessionSkills, getModels, listSessions, approveSessionPermission, denySessionPermission, deleteSession, renameSession, APIError, cancelSessionTurn, cancelQueuedTurn, steerQueuedTurn, retrySessionTurn, resumeSessionTurn, setSessionModel, unloadSessionSkill, forkSession, reviewSessionSubagent, cancelSessionSubagent, resumeSessionSubagent, mergeSessionSubagent, runSessionLongTask, cancelSessionLongTask, finalizeSessionLongTaskStory, executeCommand, uploadAttachments, submitMessage, listSkillsCatalog, listAgentTemplates } from "../../lib/api";
import type { SkillCatalogEntry } from "../../lib/types";
import type { TerminalExecutionConfig } from "../../lib/terminalClient";
import { streamEvents } from "../../lib/sse";
import { isLongTaskRefluxMessage, LongTaskRefluxBubble } from "./LongTaskRefluxBubble";
import { readPersistedRefluxDismissed, writePersistedRefluxDismissed } from "./refluxDismissPersistence";
import { buildTaskOutcomes } from "./taskCenterOutcome";
import { locatorMatchesRoute, buildChatRouteForSession } from "../../lib/chatRoutes";
import { writeClipboardText } from "../../lib/clipboard";
import { type ComposerSubmission, Composer, type ComposerHandle } from "../../components/Composer";
import { VoiceBar } from "../../components/VoiceBar";
import { TaskCenterPanel } from "./TaskCenterPanel";
import { SessionsRail } from "./layout/SessionsRail";
import { VerticalRightOutlined, VerticalLeftOutlined, StopOutlined, CloseOutlined, PlusOutlined, ReloadOutlined, LogoutOutlined, BellOutlined, EditOutlined, EnterOutlined } from "@ant-design/icons";
import { DOCK_TAB_META } from "./layout/DockRail";
import { MessageFeedV2 } from "../../components/MessageFeedV2";
import { FilesPanel } from "../files/FilesPanel";
import { TerminalPanel } from "../terminal/TerminalPanel";
import { PreviewPanel } from "../preview/PreviewPanel";
import { ReviewMergeCenterPanel } from "./ReviewMergeCenterPanel";
import { type TimelineFilterState, defaultTimelineFilters, appendTimelineEvent, mergeChronologicalFeedItems, pendingSendToFeedItem, mergeSubagentItems, subagentJobToFeedItem, collectSubagentJobs, buildContextStatusSummary, shortTurnId } from "../../lib/timelineUtils";
import { compactWorkspaceName, noteContextMetadata, NoteContextBanner } from "./panels/NoteContextBanner";
import { InspectorTabs } from "./panels/InspectorTabs";
import { ApprovalBanner } from "./panels/ApprovalPanels";
import { ContextStatusInline } from "./panels/ContextPanels";
import { SubagentReviewPanel } from "./panels/TurnSubagentPanels";

function makeSessionKey() {
  return crypto.randomUUID();
}

const reasoningEffortOptions = [
  { value: "none", label: "None" },
  { value: "minimal", label: "Minimal" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "X High" },
];

import type { ChatLayoutState } from "./useChatLayoutState";
export function useChatSessionState(layout: ChatLayoutState) {
  const {
    message,
    routeChannel,
    routeSessionKey,
    searchParams,
    location,
    navigate,
    queryClient,
    screens,
    t,
    token,
    remoteNodeID,
    remoteNodeName,
    clearRemoteNode,
    defaultSessionKey,
    setDefaultSessionKey,
    sessionId,
    historyItems,
    overlayItems,
    pendingSends,
    addPendingSend,
    removePendingSend,
    status,
    running,
    currentTurnId,
    streamConnected,
    setSession,
    syncSnapshot,
    setRunningTurn,
    handleEvent,
    toggleTool,
    setStreamConnected,
    reset,
    sessionsOpen,
    setSessionsOpen,
    inspectorOpen,
    setInspectorOpen,
    inspectorCollapsed,
    openInspector,
    closeInspector,
    inspectorActiveKey,
    setInspectorActiveKey,
    uploadProgress,
    setUploadProgress,
    uploading,
    setUploading,
    queuedComposerFiles,
    setQueuedComposerFiles,
    timelineItems,
    setTimelineItems,
    subagentReview,
    setSubagentReview,
    subagentMergeResult,
    setSubagentMergeResult,
    subagentReviewOpen,
    setSubagentReviewOpen,
    reviewMergeOpen,
    setReviewMergeOpen,
    reviewMergeFilter,
    setReviewMergeFilter,
    reviewMergeSelectedJobId,
    setReviewMergeSelectedJobId,
    reviewSubagentTargetRef,
    reviewMergeAutoLoadJobRef,
    channelFilter,
    setChannelFilter,
    notifyArmed,
    setNotifyArmed,
    pendingModelProfileID,
    setPendingModelProfileID,
    pendingReasoningEffort,
    setPendingReasoningEffort,
    timelineFilters,
    setTimelineFilters,
    timelineCursor,
    setTimelineCursor,
    timelineCursorStack,
    setTimelineCursorStack,
    openTaskCenterPanel,
    refluxDismissed,
    setRefluxDismissed,
    scrollerRef,
    composerRef,
    stickToBottom,
    setStickToBottom,
    stickToBottomRef,
    handleFeedScroll,
    v2LeftCollapsed,
    v2RightCollapsed,
    v2ActiveDockTab,
    v2LeftWidth,
    v2RightWidth,
    v2ToggleLeft,
    v2ToggleRight,
    v2CloseRight,
    v2SetActiveDockTab,
    v2SetLeftWidth,
    v2SetRightWidth,
    v2SessionSearch,
    setV2SessionSearch,
    filesFocusPath,
    setFilesFocusPath,
    mountedDockTabs,
    setMountedDockTabs,
    toggleNotifyMe,
    terminalTabs,
    setTerminalTabs,
    activeTerminal,
    setActiveTerminal,
    addTerminalTab,
    closeTerminalTab,
    beginV2LeftResize,
    beginV2RightResize,
  } = layout;


  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const routeUserId = searchParams.get("user_id");
  const noteContextId = searchParams.get("note_id")?.trim() || "";
  const workspaceDirParam = searchParams.get("workspace_dir")?.trim() || "";
  const modeParam = searchParams.get("mode")?.trim() || "";
  const templateParam = searchParams.get("template")?.trim() || "";
  const skillsParam = searchParams.get("skills")?.trim() || "";
  const sessionKey = routeSessionKey || defaultSessionKey || "";
  const sessionLocator = useMemo(() => {
    const metadata: Record<string, string> = {};
    if (workspaceDirParam) {
      metadata.project_dir = workspaceDirParam;
    }
    if (modeParam) {
      metadata.mode = modeParam;
    }
    if (templateParam) {
      metadata.template = templateParam;
    }
    if (skillsParam) {
      metadata.requested_skills = skillsParam;
    }
    return {
      channel: routeChannel || "web",
      key: sessionKey,
      ...(routeUserId ? { user_id: routeUserId } : {}),
      ...(Object.keys(metadata).length > 0 ? { metadata } : {}),
    };
  }, [routeChannel, routeUserId, sessionKey, workspaceDirParam, modeParam, templateParam, skillsParam]);

  const openQuery = useQuery({
    queryKey: ["session-open", token, sessionLocator.channel, sessionLocator.key, sessionLocator.user_id],
    enabled: !!sessionKey && (!authRequired || !!token),
    queryFn: async () => openSession(token || null, sessionLocator),
    // Session identity resolves once; a short freshness window makes repeated
    // switches back to the same session (and hover-prefetched warm-ups in
    // ChatPageView) serve from cache instead of re-issuing the disk-loading
    // POST /sessions on every navigation.
    staleTime: 30 * 1000,
  });

  // Installed-skill catalog for the new-chat skill picker (skills install
  // globally, so this is independent of any session).
  const skillsCatalogQuery = useQuery({
    queryKey: ["skills-catalog", token],
    enabled: !authRequired || !!token,
    queryFn: () => listSkillsCatalog(token || null),
  });
  const templatesQuery = useQuery({
    queryKey: ["agent-templates", token],
    enabled: !authRequired || !!token,
    queryFn: () => listAgentTemplates(token || null),
  });
  // Selected agent template (talent market) for the current chat route:
  // shown in the topbar chip and on assistant message avatars/headers.
  const activeTemplate = useMemo(
    () => (templatesQuery.data ?? []).find((tpl) => tpl.id === templateParam) ?? null,
    [templatesQuery.data, templateParam],
  );

  // Resolve the session's working directory from locator metadata,
  // falling back to the service-level workspace_dir from /meta.
  const sessionWorkspaceDir = useMemo(() => {
    const locatorDir = openQuery.data?.locator?.metadata?.project_dir?.trim();
    if (locatorDir) return locatorDir;
    return metaQuery.data?.workspace_dir?.trim() || ".";
  }, [openQuery.data, metaQuery.data]);

  // Build execution config for terminal — derives from /meta which now
  // carries tools.execution.* settings. Falls back to local mode.
  const terminalExecution = useMemo<TerminalExecutionConfig>(() => {
    const mode = metaQuery.data?.execution_mode;
    if (!mode || mode === "local") return {};
    return {
      mode,
      sshTarget: metaQuery.data?.ssh_target ?? undefined,
      sshWorkspace: metaQuery.data?.ssh_workspace ?? undefined,
      sshOptions: metaQuery.data?.ssh_options?.length ? metaQuery.data.ssh_options : undefined,
      dockerImage: metaQuery.data?.docker_image ?? undefined,
      dockerNetwork: metaQuery.data?.docker_network ?? undefined,
    };
  }, [metaQuery.data]);

  const noteContextQuery = useQuery({
    queryKey: ["note-context", token, noteContextId],
    enabled: !!noteContextId && (!authRequired || !!token),
    queryFn: () => getNote(token || null, noteContextId),
  });
  const saveMessageToNoteMutation = useMutation({
    mutationFn: async (item: FeedItem) => {
      // Grouped V2 turns store the answer in finalBody; copy/save use only that.
      const body = (item.finalBody ?? item.body).trim();
      if (!body) {
        throw new Error("No message text to save.");
      }
      const currentNote = noteContextQuery.data;
      if (currentNote) {
        const nextContent = [currentNote.content.trim(), body].filter(Boolean).join("\n\n");
        return saveNote(token || null, {
          id: currentNote.id,
          title: currentNote.title,
          summary: currentNote.summary,
          tags: currentNote.tags,
          content: nextContent,
        });
      }
      return saveNote(token || null, {
        title: item.summary || "Assistant note",
        summary: item.summary,
        tags: ["chat"],
        content: body,
      });
    },
    onSuccess: async (note) => {
      void message.success(noteContextQuery.data ? t("chat.savedToCurrentNote") : t("chat.savedAsNote"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["notes", token] }),
        queryClient.invalidateQueries({ queryKey: ["note-context", token, note.id] }),
      ]);
    },
    onError: (error) => {
      message.error(error instanceof Error ? error.message : String(error));
    },
  });

  useEffect(() => {
    if (openQuery.data) {
      setSession(openQuery.data.session_id, sessionKey);
    }
  }, [openQuery.data, sessionKey, setSession]);

  const snapshotQuery = useQuery({
    queryKey: ["snapshot", token, openQuery.data?.session_id],
    enabled: !!openQuery.data?.session_id && (!authRequired || !!token),
    queryFn: async () => getSnapshot(token || null, openQuery.data!.session_id),
  });

  useEffect(() => {
    if (snapshotQuery.data) {
      syncSnapshot(snapshotQuery.data.display_messages ?? snapshotQuery.data.messages ?? [], snapshotQuery.data.running, snapshotQuery.data.active_turn_id);
    }
  }, [snapshotQuery.data, syncSnapshot]);

  // Session switch in progress: the route key changed and the target session's
  // snapshot has not rendered yet. `openQuery.isPending` covers the slow
  // openSession POST (the multi-second wait), the guarded second clause covers
  // the first snapshot fetch for a session not in the query cache. The guard
  // `!!openQuery.data?.session_id` keeps the veil from sticking when
  // openSession itself failed (a disabled snapshot query reports pending but
  // must not hold the overlay). While true, the chat feed is veiled with a
  // spinner so switching sessions never looks frozen.
  const switchingSession =
    openQuery.isPending || (!!openQuery.data?.session_id && snapshotQuery.isPending);

  const timelineQuery = useQuery({
    queryKey: ["timeline", token, openQuery.data?.session_id],
    enabled: !!openQuery.data?.session_id && (!authRequired || !!token),
    queryFn: async () => getSessionTimeline(token || null, openQuery.data!.session_id, 80),
  });

  // Compaction history is fetched from a dedicated endpoint (not the 80-item
  // timeline window) so early compactions that fell out of the recorder
  // rolling window are still visible.
  const compactionsQuery = useQuery({
    queryKey: ["compactions", token, openQuery.data?.session_id],
    enabled: !!openQuery.data?.session_id && (!authRequired || !!token),
    queryFn: async () => getSessionCompactions(token || null, openQuery.data!.session_id),
  });

  const currentTimelineTurnId = currentTurnId || snapshotQuery.data?.active_turn_id || "";
  const effectiveTimelineTurnId = timelineFilters.currentTurnOnly ? currentTimelineTurnId : timelineFilters.turnId;
  const timelinePageQuery = useQuery({
    queryKey: ["timeline-page", token, openQuery.data?.session_id, timelineFilters, effectiveTimelineTurnId, timelineCursor],
    enabled: !!openQuery.data?.session_id && (!authRequired || !!token),
    queryFn: async () =>
      getSessionTimelinePage(token || null, openQuery.data!.session_id, {
        limit: timelineFilters.limit,
        cursor: timelineCursor,
        types: timelineFilters.types,
        q: timelineFilters.q,
        jobId: timelineFilters.jobId,
        turnId: effectiveTimelineTurnId,
      }),
  });

  const subagentsQuery = useQuery({
    queryKey: ["subagents", token, openQuery.data?.session_id],
    enabled: !!openQuery.data?.session_id && (!authRequired || !!token),
    queryFn: async () => listSessionSubagents(token || null, openQuery.data!.session_id),
  });

  const longTasksQuery = useQuery({
    queryKey: ["longtasks", token, openQuery.data?.session_id],
    enabled: !!openQuery.data?.session_id && (!authRequired || !!token),
    queryFn: async () => listSessionLongTasks(token || null, openQuery.data!.session_id),
    // A2: while any longtask is still running/pending, poll every 3s so the
    // DAG diagram (node status/verdict) stays live without a page refresh.
    refetchInterval: (query) => {
      const items = query.state.data ?? [];
      const active = items.some((lt) => lt.running > 0 || lt.pending > 0 || lt.status === "running");
      return active ? 3000 : false;
    },
  });

  const packageCommandsQuery = useQuery({
    queryKey: ["package-commands", token],
    enabled: !authRequired || !!token,
    queryFn: async () => listPackageCommands(token || null, false),
  });

  const builtinCommandsQuery = useQuery({
    queryKey: ["builtin-commands", token],
    enabled: !authRequired || !!token,
    queryFn: async () => listCommands(token || null),
    staleTime: 5 * 60 * 1000,
  });

  const packageRolesQuery = useQuery({
    queryKey: ["package-roles", token],
    enabled: !authRequired || !!token,
    queryFn: async () => listPackageRoles(token || null, false),
  });

  const contextInspectorQuery = useQuery({
    queryKey: ["context-inspector", token, openQuery.data?.session_id],
    enabled: !!openQuery.data?.session_id && (!authRequired || !!token),
    queryFn: async () => getSessionContextInspector(token || null, openQuery.data!.session_id),
    // While a turn is running, poll so the real cache hit rate and cumulative
    // token counters update live instead of only after snapshot_ready.
    // snapshotQuery.data?.running keeps the interval in sync with the backend
    // even when the local `running` flag lags.
    refetchInterval: running || snapshotQuery.data?.running ? 5000 : false,
  });
  const activeSkillsQuery = useQuery({
    queryKey: ["skills-active", token, openQuery.data?.session_id],
    enabled: !!openQuery.data?.session_id && (!authRequired || !!token),
    queryFn: async () => getActiveSessionSkills(token || null, openQuery.data!.session_id),
  });

  const modelsQuery = useQuery({
    queryKey: ["models", token, openQuery.data?.session_id],
    enabled: !!openQuery.data?.session_id && (!authRequired || !!token),
    queryFn: async () => getModels(token || null, openQuery.data!.session_id),
  });

  useEffect(() => {
    if (timelineQuery.data) {
      setTimelineItems(timelineQuery.data);
      return;
    }
    if (snapshotQuery.data?.timeline) {
      setTimelineItems(snapshotQuery.data.timeline);
    }
  }, [snapshotQuery.data?.timeline, timelineQuery.data]);

  const sessionsQuery = useQuery({
    queryKey: ["sessions", token, remoteNodeID],
    enabled: !authRequired || !!token,
    queryFn: async () => listSessions(token || null),
    // Retry twice on failure — especially important for remote nodes that
    // may be slow to become ready after a node switch or page load.
    retry: 2,
    // Session metadata changes far less often than chat snapshots. Keep the
    // assembled list warm across route/session switches; explicit mutations
    // and turn completion invalidate it when metadata can actually change.
    staleTime: 5 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
  });

  // 修复 bug：从其它页面（如「任务看板」）切到「聊天」不应自动新建对话。
  // 裸 /chat（无 routeSessionKey）时，等会话列表加载完成后恢复到最近一次活跃会话；
  // 仅当确实没有任何历史会话时才新建。用 buildChatRouteForSession 走完整 locator
  // 身份编码，避免「跳转开成新会话」（此前 db1e687/ad9b722 修过同类根因）。
  useEffect(() => {
    if (routeSessionKey && routeChannel) {
      return;
    }
    if (sessionsQuery.isPending || sessionsQuery.isLoading) {
      // 列表还在拉取，先不决策，避免把「切回聊天」误判成「新建对话」。
      return;
    }
    if (authRequired && !token) {
      // 未认证时无法可靠恢复/创建会话，交给 openQuery 的 guard 处理。
      return;
    }
    const sessions = sessionsQuery.data ?? [];
    const mostRecent = [...sessions].sort(
      (left, right) => new Date(right.updated_at).getTime() - new Date(left.updated_at).getTime(),
    )[0];
    if (mostRecent) {
      // 复用最近一次活跃会话（完整 locator 身份编码跳转，避免开成新会话）。
      navigate(buildChatRouteForSession(mostRecent), { replace: true });
      return;
    }
    // 没有任何历史会话（首次使用）才新建一个。
    const next = makeSessionKey();
    setDefaultSessionKey(next);
    navigate(`/chat/web/${next}`, { replace: true });
  }, [
    authRequired,
    navigate,
    routeChannel,
    routeSessionKey,
    setDefaultSessionKey,
    sessionsQuery.data,
    sessionsQuery.isLoading,
    sessionsQuery.isPending,
    token,
  ]);

  useEffect(() => {
    const opened = openQuery.data;
    if (!opened?.session_id) {
      return;
    }
    // Opening/switching a session must not refetch the whole rail. Upsert a
    // newly-created session locally; existing sessions keep their cached title
    // and timestamps until the next meaningful refresh.
    queryClient.setQueryData<ListedSession[]>(["sessions", token, remoteNodeID], (current) => {
      if (current?.some((item) => item.session_id === opened.session_id)) {
        return current;
      }
      const now = opened.updated_at || opened.created_at || new Date().toISOString();
      return [
        {
          session_id: opened.session_id,
          locator: opened.locator,
          title: "New chat",
          created_at: opened.created_at || now,
          updated_at: now,
          last_activity_at: now,
        },
        ...(current ?? []),
      ];
    });
  }, [openQuery.data, queryClient, remoteNodeID, token]);

  // Remote node switch: sessions are naturally isolated by remoteNodeID in
  // their query key. Models still need explicit invalidation because their
  // existing query keys are session-scoped rather than node-scoped.
  useEffect(() => {
    // The sessions query already includes remoteNodeID, so switching nodes
    // naturally selects/fetches the correct cache entry without invalidating
    // and rebuilding every previously cached session list.
    void queryClient.invalidateQueries({ queryKey: ["models", token] });
  }, [queryClient, remoteNodeID, token]);

  useEffect(() => {
    setPendingModelProfileID(null);
    setTimelineItems([]);
    setTimelineFilters(defaultTimelineFilters());
    setTimelineCursor("");
    setTimelineCursorStack([]);
  }, [openQuery.data?.session_id]);

  useEffect(() => {
    if (!sessionId || (!token && authRequired)) {
      setStreamConnected(false);
      return;
    }
    const controller = new AbortController();
    let reconnectTimer: number | undefined;
    let connectedOnce = false;
    const connect = async () => {
      try {
        setStreamConnected(false);
        await streamEvents(
          sessionId,
          token || null,
          controller.signal,
          (event) => {
            // Guard against stale events from a previous session's stream:
            // the closure `sessionId` can lag behind the store during a
            // session switch (cleanup is async), so read the authoritative
            // session id from the store instead.
            const currentSessionId = useChatStore.getState().sessionId;
            if (event.session_id && currentSessionId && event.session_id !== currentSessionId) {
              return;
            }
            handleEvent(event);
            setTimelineItems((current) => appendTimelineEvent(current, event));
            if (event.type === "user_message_accepted") {
              queryClient.setQueryData<ListedSession[]>(["sessions", token, remoteNodeID], (current) =>
                current?.map((item) =>
                  item.session_id === sessionId
                    ? { ...item, running: true, updated_at: event.timestamp, last_activity_at: event.timestamp }
                    : item,
                ) ?? current,
              );
            }
            if (event.type === "snapshot_ready") {
              void queryClient.invalidateQueries({ queryKey: ["snapshot", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["timeline", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["timeline-page", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["subagents", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["context-inspector", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["skills-active", token, sessionId] });
            }
            // Refresh list metadata once per completed turn (title, activity,
            // running badge), not on every snapshot/tool checkpoint.
            if (event.type === "turn_completed") {
              queryClient.setQueryData<ListedSession[]>(["sessions", token, remoteNodeID], (current) =>
                current?.map((item) => (item.session_id === sessionId ? { ...item, running: false } : item)) ?? current,
              );
              void queryClient.invalidateQueries({ queryKey: ["sessions", token, remoteNodeID] });
            }
            if (event.type === "subagent_job_updated") {
              void queryClient.invalidateQueries({ queryKey: ["subagents", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["longtasks", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["timeline-page", token, sessionId] });
            }
          },
          () => {
            setStreamConnected(true);
            const recoveredConnection = connectedOnce;
            connectedOnce = true;
            if (!recoveredConnection) {
              // Initial connection (including a normal session switch): the
              // session-scoped queries are already loading and the warm rail
              // must not be invalidated.
              return;
            }
            // Actual service/network recovery: resync caches without a page
            // reload. This path runs only when this same stream reconnects.
            void queryClient.invalidateQueries({ queryKey: ["snapshot", token, sessionId] });
            void queryClient.invalidateQueries({ queryKey: ["timeline", token, sessionId] });
            void queryClient.invalidateQueries({ queryKey: ["timeline-page", token, sessionId] });
            void queryClient.invalidateQueries({ queryKey: ["context-inspector", token, sessionId] });
            void queryClient.invalidateQueries({ queryKey: ["sessions", token, remoteNodeID] });
          },
        );
      } catch {
        if (!controller.signal.aborted) {
          reconnectTimer = window.setTimeout(connect, 1500);
        }
      } finally {
        if (!controller.signal.aborted) {
          setStreamConnected(false);
        }
      }
    };
    void connect();
    return () => {
      controller.abort();
      if (reconnectTimer) {
        window.clearTimeout(reconnectTimer);
      }
      setStreamConnected(false);
    };
  }, [authRequired, handleEvent, queryClient, remoteNodeID, sessionId, setStreamConnected, token]);
  return {
    metaQuery,
    authRequired,
    routeUserId,
    noteContextId,
    workspaceDirParam,
    modeParam,
    templateParam,
    skillsParam,
    sessionKey,
    sessionLocator,
    openQuery,
    skillsCatalogQuery,
    templatesQuery,
    activeTemplate,
    sessionWorkspaceDir,
    terminalExecution,
    noteContextQuery,
    saveMessageToNoteMutation,
    snapshotQuery,
    switchingSession,
    timelineQuery,
    compactionsQuery,
    currentTimelineTurnId,
    effectiveTimelineTurnId,
    timelinePageQuery,
    subagentsQuery,
    longTasksQuery,
    packageCommandsQuery,
    builtinCommandsQuery,
    packageRolesQuery,
    contextInspectorQuery,
    activeSkillsQuery,
    modelsQuery,
    sessionsQuery,
  };
}

export type ChatSessionState = ReturnType<typeof useChatSessionState>;
