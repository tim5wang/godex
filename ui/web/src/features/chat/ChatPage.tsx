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
import { getMeta, openSession, getNote, saveNote, getSnapshot, getSessionTimeline, getSessionTimelinePage, getSessionCompactions, listSessionSubagents, listSessionLongTasks, listPackageCommands, listCommands, listPackageRoles, getSessionContextInspector, getActiveSessionSkills, getModels, listSessions, approveSessionPermission, denySessionPermission, deleteSession, renameSession, APIError, cancelSessionTurn, cancelQueuedTurn, steerQueuedTurn, retrySessionTurn, resumeSessionTurn, setSessionModel, setSessionACPAgentModel, discoverACPAgentModels, unloadSessionSkill, forkSession, reviewSessionSubagent, cancelSessionSubagent, resumeSessionSubagent, mergeSessionSubagent, runSessionLongTask, cancelSessionLongTask, finalizeSessionLongTaskStory, listSkillsCatalog, listAgentTemplates } from "../../lib/api";
import type { SkillCatalogEntry } from "../../lib/types";
import type { TerminalExecutionConfig } from "../../lib/terminalClient";
import { streamEvents } from "../../lib/sse";
import { isLongTaskRefluxMessage, LongTaskRefluxBubble } from "./LongTaskRefluxBubble";
import { readPersistedRefluxDismissed, writePersistedRefluxDismissed } from "./refluxDismissPersistence";
import { buildTaskOutcomes } from "./taskCenterOutcome";
import { locatorMatchesRoute, buildChatRouteForSession } from "../../lib/chatRoutes";
import { writeClipboardText } from "../../lib/clipboard";
import { Composer, type ComposerHandle } from "../../components/Composer";
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
import { type TimelineFilterState, defaultTimelineFilters, appendTimelineEvent, mergeChronologicalFeedItems, pendingSendToFeedItem, pendingSendsForFeed, mergeSubagentItems, subagentJobToFeedItem, collectSubagentJobs, collectToolCalls, buildContextStatusSummary, shortTurnId } from "../../lib/timelineUtils";
import { compactWorkspaceName, noteContextMetadata, NoteContextBanner } from "./panels/NoteContextBanner";
import { InspectorTabs } from "./panels/InspectorTabs";
import { ApprovalBanner } from "./panels/ApprovalPanels";
import { ContextStatusInline } from "./panels/ContextPanels";
import { SubagentReviewPanel } from "./panels/TurnSubagentPanels";
import { createChatSubmissionHandler } from "./chatSubmission";

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

import { ChatPageView } from "./ChatPageView";
import { useChatLayoutState } from "./useChatLayoutState";
import { useChatSessionState } from "./useChatSessionState";

export function useChatPageController() {
  const layout = useChatLayoutState();
  const session = useChatSessionState(layout);
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
  const {
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
  } = session;


  const items = useMemo(() => {
    // Tool events stream into the live overlay while a turn runs, but the
    // overlay is transient (cleared on reload/snapshot). Rebuild tool items
    // from the persisted timeline so ACP tool logs survive a re-entry; live
    // overlay items win for the same tool call id (dedupe by id).
    const overlayById = new Map<string, FeedItem>();
    for (const item of overlayItems) {
      if (item.kind === "tool" && item.id) {
        overlayById.set(item.id, item);
      }
    }
    const timelineTools = collectToolCalls(timelineItems).filter((item) => !overlayById.has(item.id));
    const mergedOverlay = [...overlayItems, ...timelineTools];
    return mergeChronologicalFeedItems(historyItems, mergedOverlay);
  }, [historyItems, overlayItems, timelineItems]);
  // V2 groups the flat feed into per-turn items (text + tool + todo segments).
  const v2Items = useMemo(() => groupFeedItemsIntoTurns(items), [items]);
  // User messages sitting in the send queue (pending, not yet accepted by the
  // server) are intentionally NOT rendered as bubbles in the history feed:
  // they only appear once actually sent, when user_message_accepted fires or
  // the next snapshot confirms them. Command placeholders (e.g. /compact)
  // are executing on the server, not queued, so they keep an inline
  // "running" status bubble for feedback.
  const v2ItemsWithPending = useMemo(() => {
    const feedPends = pendingSendsForFeed(pendingSends);
    if (feedPends.length === 0) {
      return v2Items;
    }
    return [...v2Items, ...feedPends.map(pendingSendToFeedItem)];
  }, [pendingSends, v2Items]);
  // T15: derive the list of longtask reflux bubbles from the
  // chat feed. We pick the last 5 reflux items (newest first) and
  // hide the ones the user has dismissed. The strict authority is
  // the metadata.kind marker the agent sets; the body sniff is a
  // fallback for messages that lost the metadata. Dismissal is
  // checked against the stable "longtaskId:status" key (item ids are
  // index/counter-based and change across reloads, so they cannot be
  // used to remember a dismissal).
  const refluxBubbles = useMemo(() => {
    const out: Array<{ id: string; longtaskId: string; status: string; content: string; dismissKey: string }> = [];
    for (let i = items.length - 1; i >= 0 && out.length < 5; i--) {
      const it = items[i];
      if (it.kind !== "assistant") continue;
      // Look for a metadata marker. The chat feed exposes
      // metadata via the protocol envelope upstream of
      // mergeChronologicalFeedItems; we use the body sniff as
      // the lowest-common-denominator check.
      if (!isLongTaskRefluxMessage(it.body)) continue;
      const m = it.body.match(/LongTask\s+(\S+):\s+(\S+)/);
      const longtaskId = m ? m[1] : "";
      const status = m ? m[2] : "";
      // Dismissal key: prefer the stable longtask id + status; fall
      // back to the feed item id (in-memory only) for malformed
      // reflux bodies that could not be parsed.
      const dismissKey = longtaskId ? `${longtaskId}:${status}` : it.id;
      if (refluxDismissed.has(dismissKey)) continue;
      out.push({ id: it.id, longtaskId, status, content: it.body, dismissKey });
    }
    return out;
  }, [items, refluxDismissed]);
  const subagentJobs = useMemo(
    () => mergeSubagentItems((subagentsQuery.data ?? []).map(subagentJobToFeedItem), collectSubagentJobs(timelineItems)),
    [subagentsQuery.data, timelineItems],
  );
  const reviewMergeSummary = useMemo(
    () => buildReviewMergeSummary(subagentJobs, { reviewedJobId: subagentReview?.job_id }),
    [subagentJobs, subagentReview?.job_id],
  );
  const pendingPermissions = snapshotQuery.data?.pending_permissions ?? [];
  const turnRecords = snapshotQuery.data?.turns ?? [];
  const queuedTurns = snapshotQuery.data?.queued_turns ?? [];
  const taskOutcomes = useMemo(
    () =>
      buildTaskOutcomes({
        longTasks: longTasksQuery.data ?? [],
        subagents: subagentJobs,
        pendingPermissions,
        queuedTurns,
        running: snapshotQuery.data?.running ?? running,
        activeTurnId: snapshotQuery.data?.active_turn_id || currentTurnId,
        activePhase: snapshotQuery.data?.active_phase,
      }),
    [currentTurnId, longTasksQuery.data, pendingPermissions, queuedTurns, running, snapshotQuery.data?.active_phase, snapshotQuery.data?.active_turn_id, snapshotQuery.data?.running, subagentJobs],
  );
  const contextInspector = contextInspectorQuery.data ?? null;
  const contextStatus = useMemo(
    () => buildContextStatusSummary(contextInspector, timelineItems, subagentJobs),
    [contextInspector, subagentJobs, timelineItems],
  );
  const sortedSessions = useMemo(
    () =>
      [...(sessionsQuery.data ?? [])].sort(
        (left, right) => new Date(right.updated_at).getTime() - new Date(left.updated_at).getTime(),
      ),
    [sessionsQuery.data],
  );
  const channels = useMemo(
    () => ["all", ...Array.from(new Set(sortedSessions.map((session) => session.locator.channel || "web"))).sort()],
    [sortedSessions],
  );
  const filteredSessions = useMemo(
    () => (channelFilter === "all" ? sortedSessions : sortedSessions.filter((session) => (session.locator.channel || "web") === channelFilter)),
    [channelFilter, sortedSessions],
  );
  const currentSession = useMemo(
    () =>
      sortedSessions.find(
        (session) =>
          session.session_id === openQuery.data?.session_id ||
          locatorMatchesRoute(session.locator, routeChannel || "web", sessionKey, routeUserId),
      ),
    [openQuery.data?.session_id, routeChannel, routeUserId, sessionKey, sortedSessions],
  );
  const compactHeader = !screens.md;
  const sessionTitle = currentSession?.title || t("chat.currentSessionFallback");
  const modelName = metaQuery.data?.model ?? t("chat.modelLoading");
  const selectedProfileID = pendingModelProfileID || modelsQuery.data?.session_profile_id || modelsQuery.data?.default_profile_id;
  const selectedProfile =
    modelsQuery.data?.profiles.find((profile) => profile.id === selectedProfileID) ??
    modelsQuery.data?.profiles.find((profile) => profile.selected) ??
    modelsQuery.data?.profiles.find((profile) => profile.default);
  const sessionReasoningEffort = modelsQuery.data?.reasoning_effort ?? "";
  const selectedReasoningEffort = pendingReasoningEffort ?? (sessionReasoningEffort || selectedProfile?.reasoning_effort || "");
  // Group model profiles by provider so the dropdown is navigable even with
  // many profiles across several providers. The group label resolves to the
  // provider display name (provider_name) with a fallback to the provider
  // protocol type, then the profile id. The selected profile's group is
  // placed first so the current choice is never buried mid-list.
  const modelGroupOptions = useMemo(() => {
    const profiles = modelsQuery.data?.profiles ?? [];
    const groups = new Map<string, { value: string; label: string }[]>();
    for (const profile of profiles) {
      const groupKey = profile.provider_name || profile.provider || profile.id;
      if (!groups.has(groupKey)) groups.set(groupKey, []);
      groups.get(groupKey)!.push({ value: profile.id, label: profile.name || profile.id });
    }
    const entries = Array.from(groups.entries());
    entries.sort((a, b) => {
      const aSelected = selectedProfileID === a[1][0]?.value;
      const bSelected = selectedProfileID === b[1][0]?.value;
      if (aSelected !== bSelected) return aSelected ? -1 : 1;
      return a[0].localeCompare(b[0]);
    });
    return entries.map(([groupKey, options]) => ({ label: groupKey, options }));
  }, [modelsQuery.data?.profiles, selectedProfileID]);
  const activeModelLabel = selectedProfile ? `${selectedProfile.name || selectedProfile.id} · ${selectedProfile.model}` : modelName;
  const modelScopeLabel = pendingModelProfileID
    ? t("chat.modelSwitching")
    : modelsQuery.data?.session_profile_id
      ? t("chat.modelScopeSession")
      : t("chat.modelScopeDefault");
  const modelScopeColor = pendingModelProfileID ? "processing" : modelsQuery.data?.session_profile_id ? "blue" : "default";
  const workspaceDir = metaQuery.data?.workspace_dir ?? t("chat.workspaceLoading");
  const headerWorkspace = compactHeader ? compactWorkspaceName(workspaceDir) : workspaceDir;
  const headerSubtitle = [activeModelLabel, headerWorkspace].filter(Boolean).join(" · ");
  const copySessionInfo = async () => {
    const lines = [
      sessionTitle,
      activeModelLabel,
      metaQuery.data?.workspace_dir,
      openQuery.data?.session_id ? `session: ${openQuery.data.session_id}` : "",
      window.location.href,
    ].filter(Boolean);
    try {
      await writeClipboardText(lines.join("\n"));
      void message.success(t("chat.copied"));
    } catch {
      void message.error(t("chat.copyFailed"));
    }
  };

  const approvePermissionMutation = useMutation({
    mutationFn: async ({ requestId, scope }: { requestId: string; scope: "once" | "session" }) =>
      approveSessionPermission(token || null, openQuery.data!.session_id, requestId, scope),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["timeline", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["timeline-page", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["context-inspector", token, openQuery.data?.session_id] }),
      ]);
    },
  });

  const denyPermissionMutation = useMutation({
    mutationFn: async ({ requestId }: { requestId: string }) =>
      denySessionPermission(token || null, openQuery.data!.session_id, requestId),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["timeline", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["timeline-page", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["context-inspector", token, openQuery.data?.session_id] }),
      ]);
    },
  });

  const deleteSessionMutation = useMutation({
    mutationFn: async (session: ListedSession) => deleteSession(token || null, session.session_id),
    onError: (error) => {
      const text = error instanceof APIError ? error.message : error instanceof Error ? error.message : "Failed to delete session.";
      void message.error(text);
    },
    onSuccess: async (_value, deletedSession) => {
      const deletedActiveSession = deletedSession.session_id === openQuery.data?.session_id;
      const nextSession = deletedActiveSession
        ? sortedSessions.find((session) => session.session_id !== deletedSession.session_id)
        : undefined;
      const deletedOpenQueryKey = [
        "session-open",
        token,
        deletedSession.locator.channel || "web",
        deletedSession.locator.key || "",
        deletedSession.locator.user_id,
      ];
      await queryClient.cancelQueries({ queryKey: deletedOpenQueryKey });
      queryClient.setQueryData<ListedSession[]>(["sessions", token, remoteNodeID], (current) =>
        current?.filter((session) => session.session_id !== deletedSession.session_id) ?? current,
      );
      if (nextSession) {
        reset();
        navigate(buildChatRouteForSession(nextSession), { replace: true });
        setSessionsOpen(false);
      } else if (deletedActiveSession) {
        createSession(true);
      }
      queryClient.removeQueries({ queryKey: deletedOpenQueryKey });
      queryClient.removeQueries({ queryKey: ["snapshot", token, deletedSession.session_id] });
      queryClient.removeQueries({ queryKey: ["timeline", token, deletedSession.session_id] });
      queryClient.removeQueries({ queryKey: ["context-inspector", token, deletedSession.session_id] });
      await queryClient.invalidateQueries({ queryKey: ["sessions", token] });
    },
  });

  const renameSessionMutation = useMutation({
    mutationFn: async ({ sessionId, title }: { sessionId: string; title: string }) => renameSession(token || null, sessionId, title),
    onError: (error) => {
      const text = error instanceof APIError ? error.message : error instanceof Error ? error.message : "Failed to rename session.";
      void message.error(text);
    },
    onSuccess: async (renamed) => {
      message.success(t("chat.chatV2Rail.renameSaved"));
      // Update the rail cache immediately; the topbar title derives from the
      // same sessions list (currentSession), so it reflects the change too.
      queryClient.setQueryData<ListedSession[]>(["sessions", token, remoteNodeID], (current) =>
        current?.map((session) => (session.session_id === renamed.session_id ? { ...session, title: renamed.title } : session)) ?? current,
      );
      await queryClient.invalidateQueries({ queryKey: ["sessions", token] });
    },
  });

  const cancelTurnMutation = useMutation({
    mutationFn: async ({ sessionId, turnId }: { sessionId: string; turnId: string }) => cancelSessionTurn(token || null, sessionId, turnId),
    onSuccess: async () => {
      message.success(t("chat.cancelRequested"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["timeline", token, sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["sessions", token] }),
      ]);
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  // #5：取消排队中的 turn（任意位置），返回原文供编辑重发。edit=true 时回填输入框。
  const cancelQueuedMutation = useMutation({
    mutationFn: async ({ sessionId, queueId }: { sessionId: string; queueId: string; edit?: boolean }) => cancelQueuedTurn(token || null, sessionId, queueId),
    onSuccess: async (result, variables) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, variables.sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["timeline", token, variables.sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["sessions", token] }),
      ]);
      if (result.text && variables.edit) {
        composerRef.current?.setText(result.text);
        message.success(t("chat.editQueued"));
      } else {
        message.success(t("chat.cancelQueued"));
      }
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  // #5：把排队中的消息以 steering 方式注入当前运行中的 turn（"_↑" 引导按钮）。
  const steerQueuedMutation = useMutation({
    mutationFn: async ({ sessionId, queueId }: { sessionId: string; queueId: string }) => steerQueuedTurn(token || null, sessionId, queueId),
    onSuccess: async (_result, variables) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, variables.sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["timeline", token, variables.sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["sessions", token] }),
      ]);
      message.success(t("chat.steerQueued"));
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const retryTurnMutation = useMutation({
    mutationFn: async ({ sessionId, turnId }: { sessionId: string; turnId: string }) => retrySessionTurn(token || null, sessionId, turnId),
    onSuccess: async (result) => {
      if (result.turn_id) {
        setRunningTurn(result.turn_id);
      }
      message.success(t("chat.retryRequested"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["timeline", token, sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["sessions", token] }),
      ]);
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const resumeTurnMutation = useMutation({
    mutationFn: async ({ sessionId, turnId }: { sessionId: string; turnId: string }) => resumeSessionTurn(token || null, sessionId, turnId),
    onSuccess: async (result) => {
      if (result.turn_id) {
        setRunningTurn(result.turn_id);
      }
      message.success(t("chat.resumeRequested"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["timeline", token, sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["sessions", token] }),
      ]);
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const modelMutation = useMutation({
    mutationFn: async ({ profileId, reasoningEffort }: { profileId: string; reasoningEffort?: string }) =>
      setSessionModel(token || null, openQuery.data!.session_id, profileId, reasoningEffort),
    onMutate: ({ profileId, reasoningEffort }) => {
      setPendingModelProfileID(profileId);
      setPendingReasoningEffort(reasoningEffort ?? null);
    },
    onSuccess: async (view) => {
      queryClient.setQueryData(["models", token, openQuery.data?.session_id], view);
      setPendingModelProfileID(null);
      setPendingReasoningEffort(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["models", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["sessions", token] }),
      ]);
    },
    onError: (error) => {
      setPendingModelProfileID(null);
      setPendingReasoningEffort(null);
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  // ACP sessions (template engine "acp:<agent-id>") select from the external
  // agent's own model list (its session configOptions), not godex profiles.
  // Discovery spawns the agent process, so the list is cached aggressively.
  const acpAgentID = activeTemplate?.engine?.startsWith("acp:") ? activeTemplate.engine.slice(4) : null;
  const acpModelsQuery = useQuery({
    queryKey: ["acp-models", token, acpAgentID],
    enabled: !!acpAgentID && !!openQuery.data?.session_id,
    staleTime: 10 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
    queryFn: () => (acpAgentID ? discoverACPAgentModels(token || null, acpAgentID) : { models: [] }),
  });
  const acpModelOptions = useMemo(
    () => (acpModelsQuery.data?.models ?? []).map((model) => ({ value: model.value, label: model.name || model.value })),
    [acpModelsQuery.data],
  );
  const acpModelMutation = useMutation({
    mutationFn: async ({ model }: { model: string }) =>
      setSessionACPAgentModel(token || null, openQuery.data!.session_id, model),
    onSuccess: async (view) => {
      queryClient.setQueryData(["models", token, openQuery.data?.session_id], view);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["models", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["sessions", token] }),
      ]);
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const unloadSkillMutation = useMutation({
    mutationFn: async (skillId: string) => unloadSessionSkill(token || null, openQuery.data!.session_id, skillId),
    onSuccess: async (result) => {
      message.success(t("chat.skillUnloaded", { name: result.name || result.id }));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["skills-active", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["context-inspector", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["timeline", token, openQuery.data?.session_id] }),
      ]);
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const forkMutation = useMutation({
    mutationFn: async ({ turnId, messageIndex }: { turnId?: string; messageIndex?: number } = {}) =>
      forkSession(token || null, openQuery.data!.session_id, {
        title: `${sessionTitle}${turnId || messageIndex !== undefined ? " (fork)" : " branch"}`,
        ...(turnId ? { turn_id: turnId } : {}),
        ...(messageIndex !== undefined ? { message_index: messageIndex } : {}),
      }),
    onSuccess: async (opened) => {
      reset();
      setDefaultSessionKey(opened.locator.key || makeSessionKey());
      navigate(buildChatRouteForSession({ locator: opened.locator }));
      await queryClient.invalidateQueries({ queryKey: ["sessions", token] });
      message.success("Session forked.");
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const refreshSubagentViews = async () => {
    const activeSessionId = openQuery.data?.session_id;
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["subagents", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["timeline-page", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["timeline", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["snapshot", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["sessions", token] }),
    ]);
  };

  const refreshLongTaskViews = async () => {
    const activeSessionId = openQuery.data?.session_id;
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["longtasks", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["subagents", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["timeline-page", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["timeline", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["snapshot", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["sessions", token] }),
    ]);
  };

  const reviewSubagentMutation = useMutation({
    mutationFn: async (jobId: string) => reviewSessionSubagent(token || null, openQuery.data!.session_id, jobId),
    onSuccess: (review) => {
      setSubagentReview(review);
      if (reviewSubagentTargetRef.current === "drawer") {
        setSubagentReviewOpen(true);
      }
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const cancelSubagentMutation = useMutation({
    mutationFn: async (jobId: string) => cancelSessionSubagent(token || null, openQuery.data!.session_id, jobId),
    onSuccess: async () => {
      message.success("Subagent cancel requested.");
      await refreshSubagentViews();
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const resumeSubagentMutation = useMutation({
    mutationFn: async (jobId: string) => resumeSessionSubagent(token || null, openQuery.data!.session_id, jobId),
    onSuccess: async () => {
      message.success("Subagent resumed.");
      await refreshSubagentViews();
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const mergeSubagentMutation = useMutation({
    mutationFn: async (jobId: string) => mergeSessionSubagent(token || null, openQuery.data!.session_id, jobId),
    onSuccess: async (result) => {
      setSubagentMergeResult(result);
      message.success(`Subagent merge ${result.status}.`);
      await refreshSubagentViews();
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const runLongTaskMutation = useMutation({
    mutationFn: async (workflowId: string) => runSessionLongTask(token || null, openQuery.data!.session_id, workflowId),
    onSuccess: async (result) => {
      message.success(`LongTask ${result.run?.status || result.status}.`);
      await refreshLongTaskViews();
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const cancelLongTaskMutation = useMutation({
    mutationFn: async ({ workflowId, nodeId }: { workflowId: string; nodeId: string }) =>
      cancelSessionLongTask(token || null, openQuery.data!.session_id, workflowId, nodeId),
    onSuccess: async () => {
      message.success("LongTask node cancel requested.");
      await refreshLongTaskViews();
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const finalizeLongTaskMutation = useMutation({
    mutationFn: async ({ workflowId, nodeId }: { workflowId: string; nodeId: string }) =>
      finalizeSessionLongTaskStory(token || null, openQuery.data!.session_id, workflowId, nodeId),
    onSuccess: async () => {
      message.success("LongTask story finalized.");
      await refreshLongTaskViews();
    },
    onError: (error) => {
      message.error(error instanceof APIError ? error.message : String(error));
    },
  });

  const reviewSubagentInDrawer = (jobId: string) => {
    reviewSubagentTargetRef.current = "drawer";
    reviewSubagentMutation.mutate(jobId);
  };

  const reviewSubagentInCenter = (jobId: string) => {
    setReviewMergeSelectedJobId(jobId);
    reviewSubagentTargetRef.current = "center";
    reviewSubagentMutation.mutate(jobId);
  };

  const openReviewMergeCenter = (jobId?: string) => {
    setReviewMergeFilter("reviewable");
    const selectedJobId = jobId || defaultReviewMergeJobId(reviewMergeSummary.items);
    reviewMergeAutoLoadJobRef.current = "";
    setReviewMergeSelectedJobId(selectedJobId);
    setReviewMergeOpen(true);
  };

  useEffect(() => {
    if (!reviewMergeOpen) {
      return;
    }
    const selected = reviewMergeSummary.items.find((item) => item.jobId === reviewMergeSelectedJobId);
    if (!selected) {
      const nextJobId = defaultReviewMergeJobId(reviewMergeSummary.items);
      if (nextJobId && nextJobId !== reviewMergeSelectedJobId) {
        setReviewMergeSelectedJobId(nextJobId);
      }
      return;
    }
    if (
      reviewMergeAutoLoadJobRef.current !== selected.jobId &&
      shouldAutoLoadReview(selected, subagentReview?.job_id, reviewSubagentMutation.isPending ? reviewSubagentMutation.variables : undefined)
    ) {
      reviewMergeAutoLoadJobRef.current = selected.jobId;
      reviewSubagentTargetRef.current = "center";
      reviewSubagentMutation.mutate(selected.jobId);
    }
  }, [reviewMergeOpen, reviewMergeSelectedJobId, reviewMergeSummary.items, reviewSubagentMutation, subagentReview?.job_id]);

  useEffect(() => {
    if (!stickToBottomRef.current) {
      return;
    }
    const scroller = scrollerRef.current;
    if (scroller) {
      scroller.scrollTop = scroller.scrollHeight;
    }
  }, [items.length]);

  const onSend = createChatSubmissionHandler({
    activeSessionId: openQuery.data?.session_id,
    token,
    sender: metaQuery.data?.lead_name || "web",
    metadata: noteContextMetadata(noteContextQuery.data, noteContextId),
    addPendingSend,
    removePendingSend,
    setRunningTurn,
    setUploading,
    setUploadProgress,
    message,
    t,
    queryClient,
  });

  const createSession = (replace = false, workspaceDir?: string, template?: string, skills?: string[]) => {
    const next = makeSessionKey();
    setDefaultSessionKey(next);
    reset();
    const base = `/chat/web/${next}`;
    const query: string[] = [];
    if (workspaceDir?.trim()) {
      query.push(`workspace_dir=${encodeURIComponent(workspaceDir.trim())}`);
    }
    if (template?.trim() && template.trim() !== "default") {
      query.push(`template=${encodeURIComponent(template.trim())}`);
    }
    const pickedSkills = (skills ?? []).map((s) => s.trim()).filter(Boolean);
    if (pickedSkills.length > 0) {
      query.push(`skills=${encodeURIComponent(pickedSkills.join(","))}`);
    }
    navigate(`${base}${query.length > 0 ? `?${query.join("&")}` : ""}`, { replace });
    setSessionsOpen(false);
  };

  const clearNoteContext = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("note_id");
    const search = next.toString();
    navigate(`${location.pathname}${search ? `?${search}` : ""}`, { replace: true });
  };

  const authError =
    openQuery.error instanceof APIError && openQuery.error.status === 401
      ? "Missing or invalid bearer token. Open Settings to configure it."
      : null;

  const updateTimelineFilters = (next: TimelineFilterState) => {
    setTimelineFilters(next);
    setTimelineCursor("");
    setTimelineCursorStack([]);
  };
  const goToNextTimelinePage = () => {
    const nextCursor = timelinePageQuery.data?.next_cursor;
    if (!nextCursor) {
      return;
    }
    setTimelineCursorStack((current) => [...current, timelineCursor]);
    setTimelineCursor(nextCursor);
  };
  const goToPreviousTimelinePage = () => {
    setTimelineCursorStack((current) => {
      if (current.length === 0) {
        return current;
      }
      const previous = current[current.length - 1] ?? "";
      setTimelineCursor(previous);
      return current.slice(0, -1);
    });
  };
  return {
    ...layout,
    ...session,
    items,
    v2Items,
    v2ItemsWithPending,
    refluxBubbles,
    subagentJobs,
    reviewMergeSummary,
    pendingPermissions,
    turnRecords,
    queuedTurns,
    taskOutcomes,
    contextInspector,
    contextStatus,
    sortedSessions,
    channels,
    filteredSessions,
    currentSession,
    compactHeader,
    sessionTitle,
    modelName,
    selectedProfileID,
    selectedProfile,
    sessionReasoningEffort,
    selectedReasoningEffort,
    modelGroupOptions,
    activeModelLabel,
    modelScopeLabel,
    modelScopeColor,
    workspaceDir,
    headerWorkspace,
    headerSubtitle,
    copySessionInfo,
    approvePermissionMutation,
    denyPermissionMutation,
    deleteSessionMutation,
    renameSessionMutation,
    cancelTurnMutation,
    cancelQueuedMutation,
    steerQueuedMutation,
    retryTurnMutation,
    resumeTurnMutation,
    modelMutation,
    acpAgentID,
    acpModelOptions,
    acpModelsLoading: acpModelsQuery.isLoading,
    acpModelMutation,
    selectedACPAgentModel: modelsQuery.data?.acp_model ?? "",
    unloadSkillMutation,
    forkMutation,
    refreshSubagentViews,
    refreshLongTaskViews,
    reviewSubagentMutation,
    cancelSubagentMutation,
    resumeSubagentMutation,
    mergeSubagentMutation,
    runLongTaskMutation,
    cancelLongTaskMutation,
    finalizeLongTaskMutation,
    reviewSubagentInDrawer,
    reviewSubagentInCenter,
    openReviewMergeCenter,
    onSend,
    createSession,
    clearNoteContext,
    authError,
    updateTimelineFilters,
    goToNextTimelinePage,
    goToPreviousTimelinePage,
  };
}

export type ChatPageController = ReturnType<typeof useChatPageController>;

export function ChatPage() {
  return <ChatPageView controller={useChatPageController()} />;
}
