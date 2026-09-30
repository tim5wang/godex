import { App as AntApp, Grid, Space, Tooltip, Button, Divider, Typography, Alert, Select, Badge, Drawer, Spin, Tag } from "antd";
import { useParams, useSearchParams, useLocation, useNavigate, Link } from "react-router-dom";
import { useQueryClient, useQuery, useMutation } from "@tanstack/react-query";
import { useI18n } from "../../i18n";
import { useSettingsStore } from "../../store/settings";
import { useNodeContextStore } from "../../store/nodeContext";
import { useChatStore, composeTranscriptArchives, groupFeedItemsIntoTurns, overlappingSnapshotMessageIndexes, snapshotToItems, transcriptArchiveRefs } from "../../store/chat";
import { useState, useRef, useEffect, useLayoutEffect, useCallback, type PointerEvent as ReactPointerEvent, useMemo, type CSSProperties } from "react";
import { useLayoutStore } from "../../store/layout";
import type { SessionTimelineEntry, DurableSubagentReview, DurableSubagentMerge, FeedItem, ListedSession, ProtocolMessage } from "../../lib/types";
import { type ReviewMergeFilter, buildReviewMergeSummary, defaultReviewMergeJobId, shouldAutoLoadReview } from "./reviewMergeCenter";
import { useConversationLayoutStore, type DockTab, DOCK_TABS } from "./layout/layoutStore";
import { getMeta, openSession, getNote, saveNote, getSnapshot, getSessionTimeline, getSessionTimelinePage, getSessionCompactions, getSessionTranscript, listSessionSubagents, listSessionLongTasks, listPackageCommands, listCommands, listPackageRoles, getSessionContextInspector, getActiveSessionSkills, getModels, listSessions, approveSessionPermission, denySessionPermission, deleteSession, renameSession, APIError, cancelSessionTurn, cancelQueuedTurn, steerQueuedTurn, retrySessionTurn, resumeSessionTurn, setSessionModel, setSessionACPAgentModel, setSessionACPAgentReasoningEffort, discoverACPAgentConfigOptions, unloadSessionSkill, forkSession, reviewSessionSubagent, cancelSessionSubagent, resumeSessionSubagent, mergeSessionSubagent, runSessionLongTask, cancelSessionLongTask, finalizeSessionLongTaskStory, listSkillsCatalog, listAgentTemplates } from "../../lib/api";
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
import { type TimelineFilterState, defaultTimelineFilters, appendTimelineEvent, mergeChronologicalFeedItems, pendingSendToFeedItem, pendingSendsForFeed, mergeSubagentItems, subagentJobToFeedItem, collectSubagentJobs, collectToolCalls, collectThinkingDeltas, collectTextDeltas, buildContextStatusSummary, shortTurnId, alignAssistantTextTurnIds } from "../../lib/timelineUtils";
import { compactWorkspaceName, noteContextMetadata, NoteContextBanner } from "./panels/NoteContextBanner";
import { InspectorTabs } from "./panels/InspectorTabs";
import { ApprovalBanner } from "./panels/ApprovalPanels";
import { ContextStatusInline } from "./panels/ContextPanels";
import { SubagentReviewPanel } from "./panels/TurnSubagentPanels";
import { createChatSubmissionHandler } from "./chatSubmission";
import { useChatPageViewModel } from "./useChatPageViewModel";

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
    contextUsageQuery,
    activeSkillsQuery,
    modelsQuery,
    sessionsQuery,
  } = session;
  const pageModel = useChatPageViewModel(layout, session);
  const { items, reviewMergeSummary, sessionTitle, sortedSessions, workspaceDir } = pageModel;


  const approvePermissionMutation = useMutation({
    mutationFn: async ({ requestId, scope }: { requestId: string; scope: "once" | "session" }) =>
      approveSessionPermission(token || null, openQuery.data!.session_id, requestId, scope),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["snapshot", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["timeline", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["timeline-page", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["context-inspector", token, openQuery.data?.session_id] }),
        queryClient.invalidateQueries({ queryKey: ["context-usage", token, openQuery.data?.session_id] }),
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
        queryClient.invalidateQueries({ queryKey: ["context-usage", token, openQuery.data?.session_id] }),
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
  // agent's own session configOptions (models + reasoning effort), not godex
  // profiles. Discovery spawns the agent process, so the list is cached
  // aggressively.
  const acpAgentID = activeTemplate?.engine?.startsWith("acp:") ? activeTemplate.engine.slice(4) : null;
  const acpModelsQuery = useQuery({
    queryKey: ["acp-config-options", token, acpAgentID],
    enabled: !!acpAgentID && !!openQuery.data?.session_id,
    staleTime: 10 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
    queryFn: () => (acpAgentID ? discoverACPAgentConfigOptions(token || null, acpAgentID) : { models: [], reasoning_efforts: [] }),
  });
  const acpModelOptions = useMemo(
    () => (acpModelsQuery.data?.models ?? []).map((model) => ({ value: model.value, label: model.name || model.value })),
    [acpModelsQuery.data],
  );
  // Reasoning-effort options advertised by the ACP agent (dsh: off/low/high/max
  // with high as the default balance). Falls back to a fixed set when the agent
  // does not advertise them.
  const acpReasoningEffortOptions = useMemo(() => {
    const advertised = (acpModelsQuery.data?.reasoning_efforts ?? []).map((effort) => ({
      value: effort.value,
      label: effort.name || effort.value,
    }));
    if (advertised.length > 0) {
      return advertised;
    }
    return [
      { value: "off", label: "Off" },
      { value: "low", label: "Low" },
      { value: "high", label: "High" },
      { value: "max", label: "Max" },
    ];
  }, [acpModelsQuery.data]);
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
  const acpReasoningEffortMutation = useMutation({
    mutationFn: async ({ effort }: { effort: string }) =>
      setSessionACPAgentReasoningEffort(token || null, openQuery.data!.session_id, effort),
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
        queryClient.invalidateQueries({ queryKey: ["context-usage", token, openQuery.data?.session_id] }),
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

  const createSession = (replace = false, workspaceDir?: string, template?: string, skills?: string[], execMode?: string) => {
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
    if (execMode?.trim()) {
      query.push(`exec_mode=${encodeURIComponent(execMode.trim())}`);
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
    ...pageModel,
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
    acpReasoningEffortOptions,
    acpReasoningEffortMutation,
    selectedACPAgentReasoningEffort: modelsQuery.data?.acp_reasoning_effort ?? "high",
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
