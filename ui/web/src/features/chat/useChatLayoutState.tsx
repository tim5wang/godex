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

export function useChatLayoutState() {
  const { message } = AntApp.useApp();
  const { channel: routeChannel, sessionKey: routeSessionKey } = useParams<{ channel: string; sessionKey: string }>();
  const [searchParams] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const screens = Grid.useBreakpoint();
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  const remoteNodeID = useNodeContextStore((state) => state.nodeID);
  const remoteNodeName = useNodeContextStore((state) => state.nodeName);
  const clearRemoteNode = useNodeContextStore((state) => state.clearNode);
  const defaultSessionKey = useSettingsStore((state) => state.defaultSessionKey);
  const setDefaultSessionKey = useSettingsStore((state) => state.setDefaultSessionKey);

  const sessionId = useChatStore((state) => state.sessionId);
  const historyItems = useChatStore((state) => state.historyItems);
  const overlayItems = useChatStore((state) => state.overlayItems);
  const pendingSends = useChatStore((state) => state.pendingSends);
  const addPendingSend = useChatStore((state) => state.addPendingSend);
  const removePendingSend = useChatStore((state) => state.removePendingSend);
  const status = useChatStore((state) => state.status);
  const running = useChatStore((state) => state.running);
  const currentTurnId = useChatStore((state) => state.currentTurnId);
  const streamConnected = useChatStore((state) => state.streamConnected);
  const setSession = useChatStore((state) => state.setSession);
  const syncSnapshot = useChatStore((state) => state.syncSnapshot);
  const setRunningTurn = useChatStore((state) => state.setRunningTurn);
  const handleEvent = useChatStore((state) => state.handleEvent);
  const toggleTool = useChatStore((state) => state.toggleTool);
  const setStreamConnected = useChatStore((state) => state.setStreamConnected);
  const reset = useChatStore((state) => state.reset);

  const [sessionsOpen, setSessionsOpen] = useState(false);
  const [inspectorOpen, setInspectorOpen] = useState(false);
  const inspectorCollapsed = !useLayoutStore((state) => state.taskCenterDrawerOpen);
  const openInspector = useLayoutStore((state) => state.openTaskCenterDrawer);
  const closeInspector = useLayoutStore((state) => state.closeTaskCenterDrawer);
  const [inspectorActiveKey, setInspectorActiveKey] = useState("approvals");
  const [uploadProgress, setUploadProgress] = useState<number | null>(null);
  const [uploading, setUploading] = useState(false);
  const [queuedComposerFiles, setQueuedComposerFiles] = useState<File[]>([]);
  const [timelineItems, setTimelineItems] = useState<SessionTimelineEntry[]>([]);
  const [subagentReview, setSubagentReview] = useState<DurableSubagentReview | null>(null);
  const [subagentMergeResult, setSubagentMergeResult] = useState<DurableSubagentMerge | null>(null);
  const [subagentReviewOpen, setSubagentReviewOpen] = useState(false);
  const [reviewMergeOpen, setReviewMergeOpen] = useState(false);
  const [reviewMergeFilter, setReviewMergeFilter] = useState<ReviewMergeFilter>("reviewable");
  const [reviewMergeSelectedJobId, setReviewMergeSelectedJobId] = useState("");
  const reviewSubagentTargetRef = useRef<"drawer" | "center">("drawer");
  const reviewMergeAutoLoadJobRef = useRef("");
  const [channelFilter, setChannelFilter] = useState("all");
  // #8 通知我：非空表示已要求 turn 完成后通知（存 turnId，"any" 表示任意完成）。
  const [notifyArmed, setNotifyArmed] = useState<string | null>(null);
  const [pendingModelProfileID, setPendingModelProfileID] = useState<string | null>(null);
  const [pendingReasoningEffort, setPendingReasoningEffort] = useState<string | null>(null);
  const [timelineFilters, setTimelineFilters] = useState<TimelineFilterState>(() => defaultTimelineFilters());
  const [timelineCursor, setTimelineCursor] = useState("");
  const [timelineCursorStack, setTimelineCursorStack] = useState<string[]>([]);
  const openTaskCenterPanel = () => {
    if (inspectorActiveKey === "taskCenter" && !inspectorCollapsed) {
      closeInspector();
      setInspectorOpen(false);
      return;
    }
    openInspector();
    setInspectorActiveKey("taskCenter");
    setInspectorOpen(!screens.lg);
  };
  // T15: track longtask reflux bubbles. The chat list still
  // renders the message itself; the bubble is a floating
  // notification that the user can act on without scrolling to
  // the reflux message. We keep the last 5 refluxes (newest at
  // the front) and let the user dismiss them. The dismissed set is
  // persisted to localStorage (keyed by longtaskId:status) so a
  // dismissed popup does not reappear after a refresh or session
  // re-entry.
  const [refluxDismissed, setRefluxDismissed] = useState<Set<string>>(() => readPersistedRefluxDismissed());
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  // 语音识别文本注入输入框（识别结果填入，用户编辑后发送）。
  const composerRef = useRef<ComposerHandle>(null);
  // Whether the feed should keep scrolling to the newest content. When the
  // user scrolls up to read history, stickToBottom turns off and new model
  // output no longer drags the scrollbar down.
  const [stickToBottom, setStickToBottom] = useState(true);
  const stickToBottomRef = useRef(true);

  const handleFeedScroll = () => {
    const scroller = scrollerRef.current;
    if (!scroller) return;
    const nearBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 120;
    stickToBottomRef.current = nearBottom;
    setStickToBottom(nearBottom);
  };

  // V2 layout state: left sessions rail + right dock rail (independent
  // persistence, does not touch the legacy layout store keys).
  const v2LeftCollapsed = useConversationLayoutStore((s) => s.leftCollapsed);
  const v2RightCollapsed = useConversationLayoutStore((s) => s.rightCollapsed);
  const v2ActiveDockTab = useConversationLayoutStore((s) => s.activeDockTab);
  const v2LeftWidth = useConversationLayoutStore((s) => s.leftWidth);
  const v2RightWidth = useConversationLayoutStore((s) => s.rightWidth);
  const v2ToggleLeft = useConversationLayoutStore((s) => s.toggleLeft);
  const v2ToggleRight = useConversationLayoutStore((s) => s.toggleRight);
  const v2CloseRight = useConversationLayoutStore((s) => s.closeRight);
  const v2SetActiveDockTab = useConversationLayoutStore((s) => s.setActiveDockTab);
  const v2SetLeftWidth = useConversationLayoutStore((s) => s.setLeftWidth);
  const v2SetRightWidth = useConversationLayoutStore((s) => s.setRightWidth);

  const [v2SessionSearch, setV2SessionSearch] = useState("");

  // File the user asked to open in the Files dock (from a Changes card).
  const [filesFocusPath, setFilesFocusPath] = useState<string | null>(null);

  // Dock panels are lazily mounted on first activation and then kept
  // mounted (hidden via CSS) so switching tabs preserves panel state
  // (xterm session, selected file, preview address).
  const [mountedDockTabs, setMountedDockTabs] = useState<Set<DockTab>>(() => new Set([v2ActiveDockTab]));

  useEffect(() => {
    setMountedDockTabs((prev) => {
      if (prev.has(v2ActiveDockTab)) return prev;
      const next = new Set(prev);
      next.add(v2ActiveDockTab);
      return next;
    });
  }, [v2ActiveDockTab]);

  // #8 通知我：running 从 true→false（turn 完成）且已 armed 时触发浏览器通知。
  useEffect(() => {
    if (running || !notifyArmed) return;
    setNotifyArmed(null);
    if (typeof window === "undefined" || !("Notification" in window)) return;
    try {
      if (Notification.permission === "granted") {
        new Notification(t("chat.notifyDone"), { body: t("chat.notifyTurnDone") });
      }
    } catch {
      /* 部分环境 new Notification 会抛错，忽略 */
    }
  }, [running, notifyArmed, t]);

  // #8 点击“通知我”：请求权限后 armed；已 armed 则取消。
  const toggleNotifyMe = async () => {
    if (notifyArmed) {
      setNotifyArmed(null);
      return;
    }
    if (typeof window !== "undefined" && "Notification" in window) {
      if (Notification.permission === "default") {
        await Notification.requestPermission();
      }
      if (Notification.permission === "denied") {
        void message.warning(t("chat.notifyDenied"));
        return;
      }
    }
    setNotifyArmed(currentTurnId || "any");
  };

  // Terminal dock supports multiple live instances (multi-open tabs). Each
  // tab keeps its own xterm/PTY session mounted; switching tabs only toggles
  // visibility via CSS data-active, so sessions survive tab switches.
  const [terminalTabs, setTerminalTabs] = useState<number[]>([0]);
  const [activeTerminal, setActiveTerminal] = useState(0);
  const addTerminalTab = () => {
    const next = terminalTabs.length ? Math.max(...terminalTabs) + 1 : 0;
    setTerminalTabs((prev) => [...prev, next]);
    setActiveTerminal(next);
  };
  const closeTerminalTab = (id: number) => {
    setTerminalTabs((prev) => {
      const next = prev.filter((t) => t !== id);
      if (activeTerminal === id) {
        setActiveTerminal(next.length ? next[next.length - 1] : 0);
      }
      return next;
    });
  };

  const beginV2LeftResize = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (event.button !== 0) return;
      event.preventDefault();
      const startX = event.clientX;
      const startWidth = useConversationLayoutStore.getState().leftWidth;
      const onPointerMove = (moveEvent: PointerEvent) => {
        const delta = moveEvent.clientX - startX;
        v2SetLeftWidth(startWidth + delta);
      };
      const stopResize = () => {
        document.body.classList.remove("is-resizing-column");
        document.removeEventListener("pointermove", onPointerMove);
        document.removeEventListener("pointerup", stopResize);
        document.removeEventListener("pointercancel", stopResize);
      };
      document.body.classList.add("is-resizing-column");
      document.addEventListener("pointermove", onPointerMove);
      document.addEventListener("pointerup", stopResize);
      document.addEventListener("pointercancel", stopResize);
    },
    [v2SetLeftWidth],
  );

  const beginV2RightResize = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (event.button !== 0) return;
      event.preventDefault();
      const startX = event.clientX;
      const startWidth = useConversationLayoutStore.getState().rightWidth;
      const onPointerMove = (moveEvent: PointerEvent) => {
        const delta = moveEvent.clientX - startX;
        v2SetRightWidth(startWidth - delta);
      };
      const stopResize = () => {
        document.body.classList.remove("is-resizing-column");
        document.removeEventListener("pointermove", onPointerMove);
        document.removeEventListener("pointerup", stopResize);
        document.removeEventListener("pointercancel", stopResize);
      };
      document.body.classList.add("is-resizing-column");
      document.addEventListener("pointermove", onPointerMove);
      document.addEventListener("pointerup", stopResize);
      document.addEventListener("pointercancel", stopResize);
    },
    [v2SetRightWidth],
  );
  return {
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
  };
}

export type ChatLayoutState = ReturnType<typeof useChatLayoutState>;
