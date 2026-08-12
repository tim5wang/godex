import { App as AntApp, Grid, Space, Tooltip, Button, Divider, Typography, Alert, Select, Badge, Segmented, Drawer } from "antd";
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
import { useChatV2Store, type DockTab, DOCK_TABS } from "../chat-v2/chatV2Store";
import { getMeta, openSession, getNote, saveNote, getSnapshot, getSessionTimeline, getSessionTimelinePage, getSessionCompactions, listSessionSubagents, listSessionLongTasks, listPackageCommands, listCommands, listPackageRoles, getSessionContextInspector, getActiveSessionSkills, getModels, listSessions, approveSessionPermission, denySessionPermission, deleteSession, APIError, cancelSessionTurn, retrySessionTurn, resumeSessionTurn, setSessionModel, unloadSessionSkill, forkSession, reviewSessionSubagent, cancelSessionSubagent, resumeSessionSubagent, mergeSessionSubagent, runSessionLongTask, cancelSessionLongTask, finalizeSessionLongTaskStory, executeCommand, uploadAttachments, submitMessage } from "../../lib/api";
import type { TerminalExecutionConfig } from "../../lib/terminalClient";
import { streamEvents } from "../../lib/sse";
import { isLongTaskRefluxMessage, LongTaskRefluxBubble } from "./LongTaskRefluxBubble";
import { readPersistedRefluxDismissed, writePersistedRefluxDismissed } from "./refluxDismissPersistence";
import { buildTaskOutcomes } from "./taskCenterOutcome";
import { locatorMatchesRoute, buildChatRouteForSession } from "../../lib/chatRoutes";
import { writeClipboardText } from "../../lib/clipboard";
import { type ComposerSubmission, Composer } from "../../components/Composer";
import { TaskCenterPanel } from "./TaskCenterPanel";
import { SessionsRail } from "../chat-v2/SessionsRail";
import { VerticalRightOutlined, VerticalLeftOutlined, StopOutlined, CloseOutlined, PlusOutlined } from "@ant-design/icons";
import { DOCK_TAB_META } from "../chat-v2/DockRail";
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

export function ChatPage() {
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
  const [queueMode, setQueueMode] = useState<"follow_up" | "steering">("follow_up");
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
  const v2LeftCollapsed = useChatV2Store((s) => s.leftCollapsed);
  const v2RightCollapsed = useChatV2Store((s) => s.rightCollapsed);
  const v2ActiveDockTab = useChatV2Store((s) => s.activeDockTab);
  const v2LeftWidth = useChatV2Store((s) => s.leftWidth);
  const v2RightWidth = useChatV2Store((s) => s.rightWidth);
  const v2ToggleLeft = useChatV2Store((s) => s.toggleLeft);
  const v2ToggleRight = useChatV2Store((s) => s.toggleRight);
  const v2SetActiveDockTab = useChatV2Store((s) => s.setActiveDockTab);
  const v2SetLeftWidth = useChatV2Store((s) => s.setLeftWidth);
  const v2SetRightWidth = useChatV2Store((s) => s.setRightWidth);

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
      const startWidth = useChatV2Store.getState().leftWidth;
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
      const startWidth = useChatV2Store.getState().rightWidth;
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

  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const routeUserId = searchParams.get("user_id");
  const noteContextId = searchParams.get("note_id")?.trim() || "";
  const workspaceDirParam = searchParams.get("workspace_dir")?.trim() || "";
  const sessionKey = routeSessionKey || defaultSessionKey || "";
  const sessionLocator = useMemo(
    () => ({
      channel: routeChannel || "web",
      key: sessionKey,
      ...(routeUserId ? { user_id: routeUserId } : {}),
      ...(workspaceDirParam ? { metadata: { project_dir: workspaceDirParam } } : {}),
    }),
    [routeChannel, routeUserId, sessionKey, workspaceDirParam],
  );

  useEffect(() => {
    if (routeSessionKey && routeChannel) {
      return;
    }
    const next = defaultSessionKey || makeSessionKey();
    // DEBUG: delay setState to the next tick to avoid sync setState during render.
    // React 19 production throws #185 if setState is called during render phase.
    const basePath = "/chat";
    setTimeout(() => {
      if (!defaultSessionKey) {
        setDefaultSessionKey(next);
      }
      navigate(`${basePath}/web/${next}`, { replace: true });
    }, 0);
  }, [defaultSessionKey, navigate, routeChannel, routeSessionKey, setDefaultSessionKey]);

  const openQuery = useQuery({
    queryKey: ["session-open", token, sessionLocator.channel, sessionLocator.key, sessionLocator.user_id],
    enabled: !!sessionKey && (!authRequired || !!token),
    queryFn: async () => openSession(token || null, sessionLocator),
  });

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
  });

  useEffect(() => {
    if (openQuery.data?.session_id) {
      void queryClient.invalidateQueries({ queryKey: ["sessions", token] });
    }
  }, [openQuery.data?.session_id, queryClient, token]);

  // Remote node switch: the node-scoped proxy (nodeProxyPath in api.ts) routes
  // /sessions to the newly active node, so any cached list from the previous
  // node (or the local center) must be refetched under the new node key.
  useEffect(() => {
    void queryClient.invalidateQueries({ queryKey: ["sessions", token] });
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
    const connect = async () => {
      try {
        setStreamConnected(false);
        await streamEvents(
          sessionId,
          token || null,
          controller.signal,
          (event) => {
            if (event.session_id && event.session_id !== sessionId) {
              return;
            }
            handleEvent(event);
            setTimelineItems((current) => appendTimelineEvent(current, event));
            if (event.type === "snapshot_ready") {
              void queryClient.invalidateQueries({ queryKey: ["snapshot", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["timeline", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["timeline-page", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["subagents", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["context-inspector", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["skills-active", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["sessions", token] });
            }
            if (event.type === "subagent_job_updated") {
              void queryClient.invalidateQueries({ queryKey: ["subagents", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["longtasks", token, sessionId] });
              void queryClient.invalidateQueries({ queryKey: ["timeline-page", token, sessionId] });
            }
          },
          () => {
            setStreamConnected(true);
            // Service restart / network recovery: the SSE stream is back but
            // the cached snapshot/timeline/sessions may be stale (global
            // refetchOnMount/refetchOnWindowFocus are disabled). Invalidate
            // the node-scoped queries so the UI resyncs without a page reload.
            void queryClient.invalidateQueries({ queryKey: ["snapshot", token, sessionId] });
            void queryClient.invalidateQueries({ queryKey: ["timeline", token, sessionId] });
            void queryClient.invalidateQueries({ queryKey: ["timeline-page", token, sessionId] });
            void queryClient.invalidateQueries({ queryKey: ["context-inspector", token, sessionId] });
            void queryClient.invalidateQueries({ queryKey: ["sessions", token] });
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
  }, [authRequired, handleEvent, queryClient, sessionId, setStreamConnected, token]);

  const items = useMemo(() => mergeChronologicalFeedItems(historyItems, overlayItems), [historyItems, overlayItems]);
  // V2 groups the flat feed into per-turn items (text + tool + todo segments).
  const v2Items = useMemo(() => groupFeedItemsIntoTurns(items), [items]);
  // Append optimistic placeholders (sending message / running command) at
  // the end of the feed so in-flight sends stay visible.
  const v2ItemsWithPending = useMemo(() => {
    if (pendingSends.length === 0) {
      return v2Items;
    }
    return [...v2Items, ...pendingSends.map(pendingSendToFeedItem)];
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
      queryClient.setQueryData<ListedSession[]>(["sessions", token], (current) =>
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
    mutationFn: async () => forkSession(token || null, openQuery.data!.session_id, { title: `${sessionTitle} branch` }),
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

  const onSend = async (submission: ComposerSubmission) => {
    const activeSessionId = openQuery.data?.session_id;
    if (!activeSessionId) {
      return;
    }
    const { text, files } = submission;
    if (!text && files.length === 0) {
      return;
    }
    if (text.startsWith("/") && files.length === 0) {
      // Optimistic: surface the running command immediately so slow
      // commands like /compact show feedback while they execute.
      const commandName = text.trim().split(/\s+/)[0].slice(1) || "command";
      const pendingId = `cmd:${Date.now()}:${Math.random().toString(36).slice(2)}`;
      addPendingSend({ id: pendingId, kind: "command", commandName });
      try {
        const commandResult = await executeCommand(token || null, activeSessionId, text, noteContextMetadata(noteContextQuery.data, noteContextId));
        if (commandResult.dispatched_turn_id) {
          setRunningTurn(commandResult.dispatched_turn_id);
        }
      } finally {
        removePendingSend(pendingId);
      }
    } else {
      try {
        const attachments =
          files.length > 0
            ? await (async () => {
                setUploading(true);
                setUploadProgress(0);
                return uploadAttachments(token || null, activeSessionId, files, setUploadProgress);
              })()
            : [];
        // Optimistic: show the user message as "sending" immediately; it
        // is replaced by the real item when user_message_accepted arrives
        // (or by the next snapshot as a backstop).
        const pendingId = `user:${Date.now()}:${Math.random().toString(36).slice(2)}`;
        addPendingSend({ id: pendingId, kind: "user", text, attachments, sender: metaQuery.data?.lead_name || "web" });
        try {
          const submitResult = await submitMessage(
            token || null,
            activeSessionId,
            {
              source: "web",
              sender: metaQuery.data?.lead_name || "web",
              text,
              content: text,
              attachments,
              metadata: noteContextMetadata(noteContextQuery.data, noteContextId),
            },
            running ? { queueMode } : {},
          );
          if (submitResult.turn_id) {
            setRunningTurn(submitResult.turn_id);
          }
        } catch (error) {
          // Network-level failure (service restart / dropped connection):
          // keep the optimistic placeholder. Once the SSE stream reconnects,
          // the snapshot sync confirms the message (and drops the placeholder)
          // or the user can retry — no page reload needed. Business errors
          // (4xx/5xx from the backend) still surface immediately.
          if (error instanceof TypeError) {
            message.warning(t("chat.submitNetworkError"));
            return;
          }
          removePendingSend(pendingId);
          message.error(error instanceof Error ? error.message : String(error));
          throw error;
        }
      } finally {
        setUploading(false);
        setUploadProgress(null);
      }
    }
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["sessions", token] }),
      queryClient.invalidateQueries({ queryKey: ["snapshot", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["timeline", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["timeline-page", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["subagents", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["context-inspector", token, activeSessionId] }),
      queryClient.invalidateQueries({ queryKey: ["skills-active", token, activeSessionId] }),
    ]);
  };

  const createSession = (replace = false, workspaceDir?: string) => {
    const next = makeSessionKey();
    setDefaultSessionKey(next);
    reset();
    const base = `/chat/web/${next}`;
    const query = workspaceDir?.trim() ? `?workspace_dir=${encodeURIComponent(workspaceDir.trim())}` : "";
    navigate(`${base}${query}`, { replace });
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
  const inspectorPanel = (
    <InspectorTabs
      activeKey={inspectorActiveKey}
      onActiveKeyChange={setInspectorActiveKey}
      onCollapseInspector={() => {
        closeInspector();
        setInspectorOpen(false);
      }}
      taskCenterPanel={(
        <TaskCenterPanel
          outcomes={taskOutcomes}
          collapsed={false}
          onCollapsedChange={() => {
            closeInspector();
            setInspectorOpen(false);
          }}
          reviewingJobId={reviewSubagentMutation.isPending ? reviewSubagentMutation.variables : undefined}
          mergingJobId={mergeSubagentMutation.isPending ? mergeSubagentMutation.variables : undefined}
          resumingJobId={resumeSubagentMutation.isPending ? resumeSubagentMutation.variables : undefined}
          cancelingJobId={cancelSubagentMutation.isPending ? cancelSubagentMutation.variables : undefined}
          runningWorkflowId={runLongTaskMutation.isPending ? runLongTaskMutation.variables : undefined}
          cancelingLongTask={cancelLongTaskMutation.isPending ? cancelLongTaskMutation.variables : undefined}
          finalizingLongTask={finalizeLongTaskMutation.isPending ? finalizeLongTaskMutation.variables : undefined}
          onReviewSubagent={reviewSubagentInDrawer}
          onMergeSubagent={(jobId) => mergeSubagentMutation.mutate(jobId)}
          onResumeSubagent={(jobId) => resumeSubagentMutation.mutate(jobId)}
          onCancelSubagent={(jobId) => cancelSubagentMutation.mutate(jobId)}
          onRunLongTask={(workflowId) => runLongTaskMutation.mutate(workflowId)}
          onCancelLongTask={(workflowId, nodeId) => cancelLongTaskMutation.mutate({ workflowId, nodeId })}
          onFinalizeLongTask={(workflowId, nodeId) => finalizeLongTaskMutation.mutate({ workflowId, nodeId })}
          onOpenReviewMergeCenter={openReviewMergeCenter}
        />
      )}
      pendingPermissions={pendingPermissions}
      longTasks={longTasksQuery.data ?? []}
      longTasksLoading={longTasksQuery.isLoading || longTasksQuery.isFetching}
      subagentJobs={subagentJobs}
      packageRoles={packageRolesQuery.data ?? []}
      packageRolesLoading={packageRolesQuery.isLoading}
      turnRecords={turnRecords}
      timelineItems={timelineItems}
      compactions={compactionsQuery.data}
      compactionsLoading={compactionsQuery.isLoading || compactionsQuery.isFetching}
      timelinePage={timelinePageQuery.data}
      timelinePageLoading={timelinePageQuery.isLoading || timelinePageQuery.isFetching}
      timelineFilters={timelineFilters}
      currentTurnId={currentTimelineTurnId}
      canPreviousTimelinePage={timelineCursorStack.length > 0}
      timelinePageIndex={timelineCursorStack.length}
      onTimelineFiltersChange={updateTimelineFilters}
      onNextTimelinePage={goToNextTimelinePage}
      onPreviousTimelinePage={goToPreviousTimelinePage}
      contextInspector={contextInspector}
      contextLoading={contextInspectorQuery.isLoading}
      sessionId={openQuery.data?.session_id ?? ""}
      activeSkills={activeSkillsQuery.data ?? []}
      activeSkillsLoading={activeSkillsQuery.isLoading}
      unloadingSkill={unloadSkillMutation}
      approving={approvePermissionMutation}
      denying={denyPermissionMutation}
      reviewingSubagent={reviewSubagentMutation}
      cancelingSubagent={cancelSubagentMutation}
      resumingSubagent={resumeSubagentMutation}
      mergingSubagent={mergeSubagentMutation}
      onReviewSubagent={reviewSubagentInDrawer}
      onCancelSubagent={(jobId) => cancelSubagentMutation.mutate(jobId)}
      onResumeSubagent={(jobId) => resumeSubagentMutation.mutate(jobId)}
      onMergeSubagent={(jobId) => mergeSubagentMutation.mutate(jobId)}
      runningLongTask={runLongTaskMutation}
      cancelingLongTask={cancelLongTaskMutation}
      finalizingLongTask={finalizeLongTaskMutation}
      onRunLongTask={(workflowId) => runLongTaskMutation.mutate(workflowId)}
      onCancelLongTask={(workflowId, nodeId) => cancelLongTaskMutation.mutate({ workflowId, nodeId })}
      onFinalizeLongTask={(workflowId, nodeId) => finalizeLongTaskMutation.mutate({ workflowId, nodeId })}
      retrying={retryTurnMutation}
      resuming={resumeTurnMutation}
      onRetryTurn={(turnId) => {
        if (openQuery.data?.session_id) {
          retryTurnMutation.mutate({ sessionId: openQuery.data.session_id, turnId });
        }
      }}
      onResumeTurn={(turnId) => {
        if (openQuery.data?.session_id) {
          resumeTurnMutation.mutate({ sessionId: openQuery.data.session_id, turnId });
        }
      }}
      onUnloadSkill={(skillId) => unloadSkillMutation.mutate(skillId)}
    />
  );

  return (
    <>
        <div className="chat-v2-layout" style={{ "--chat-v2-left-width": v2LeftCollapsed ? "0px" : `${v2LeftWidth}px`, "--chat-v2-right-width": v2RightCollapsed ? "48px" : `${v2RightWidth}px` } as CSSProperties}>
          {/* Left rail */}
          <div className="chat-v2-left" data-collapsed={v2LeftCollapsed ? "true" : "false"}>
            {!v2LeftCollapsed ? (
              <div className="chat-v2-left-resize" onPointerDown={beginV2LeftResize} role="separator" aria-label="Resize left panel" />
            ) : null}
            <SessionsRail
              collapsed={v2LeftCollapsed}
              sessions={filteredSessions}
              loading={sessionsQuery.isLoading || sessionsQuery.isFetching}
              activeSessionId={openQuery.data?.session_id ?? ""}
              searchQuery={v2SessionSearch ?? ""}
              deletingSessionId={deleteSessionMutation.variables?.session_id ?? ""}
              onSearchChange={setV2SessionSearch}
              onCreate={(workspaceDir) => createSession(false, workspaceDir)}
              onSelect={(session) => {
                navigate(buildChatRouteForSession(session));
              }}
              onDelete={(session) => deleteSessionMutation.mutate(session)}
              onToggleCollapsed={() => v2ToggleLeft()}
            />
          </div>
          {/* Center: topbar + feed + composer */}
          <div className="chat-v2-center-wrap">
            <div className="chat-v2-topbar">
              <Space size={4}>
                <Tooltip title={v2LeftCollapsed ? "Expand sidebar" : "Collapse sidebar"}>
                  <Button type="text" size="small" icon={v2LeftCollapsed ? <VerticalRightOutlined /> : <VerticalLeftOutlined />} aria-label="Toggle sidebar" onClick={() => v2ToggleLeft()} />
                </Tooltip>
                <Divider type="vertical" />
                <Tooltip title={t("chat.copySessionInfo")}>
                  <Typography.Text className="chat-v2-topbar-title" strong ellipsis={{ tooltip: sessionTitle }} onClick={() => void copySessionInfo()} style={{ cursor: "pointer", maxWidth: 200 }}>
                    {sessionTitle}
                  </Typography.Text>
                </Tooltip>
              </Space>
              <Space size={4}>
                <div className="chat-v2-topbar-tabs">
                  {DOCK_TABS.map((tab: DockTab) => {
                    const meta = DOCK_TAB_META[tab];
                    const active = tab === v2ActiveDockTab && !v2RightCollapsed;
                    const badge = tab === "tasks" ? (pendingPermissions.length || 0) : 0;
                    return (
                      <Tooltip key={tab} title={meta.label}>
                        <Button
                          type="text"
                          size="small"
                          icon={meta.icon}
                          aria-label={meta.label}
                          className={`chat-v2-topbar-tab${active ? " chat-v2-topbar-tab-active" : ""}`}
                          onClick={() => v2SetActiveDockTab(tab)}
                        >
                          {badge > 0 ? <span className="chat-v2-topbar-badge">{badge}</span> : null}
                        </Button>
                      </Tooltip>
                    );
                  })}
                </div>
                <Tooltip title={v2RightCollapsed ? "Expand panel" : "Collapse panel"}>
                  <Button type="text" size="small" icon={v2RightCollapsed ? <VerticalLeftOutlined /> : <VerticalRightOutlined />} aria-label="Toggle right panel" onClick={() => v2ToggleRight()} />
                </Tooltip>
              </Space>
            </div>
            {authRequired && !token ? (
              <div style={{ padding: 16 }}>
                <Alert
                  type="warning"
                  showIcon
                  message={
                    <>
                      {t("chat.authRequiredPrefix")} <Link to="/settings">{t("app.nav.settings")}</Link> {t("chat.authRequiredSuffix")}
                    </>
                  }
                />
              </div>
            ) : authError ? (
              <div style={{ padding: 16 }}>
                <Alert type="error" showIcon message={authError} />
              </div>
            ) : (
              <div className="chat-v2-center-body">
                <div className="chat-feed chat-feed-v2-scroll" ref={scrollerRef} onScroll={handleFeedScroll} style={{ minHeight: 0 }}>
                  <div className="chat-feed-inner chat-feed-v2-inner">
                    <MessageFeedV2
                      items={v2ItemsWithPending}
                      onToggleTool={toggleTool}
                      onSaveToNote={(item) => saveMessageToNoteMutation.mutate(item)}
                      savingToNote={saveMessageToNoteMutation.isPending}
                      hasNoteContext={!!noteContextQuery.data}
                      workspaceDir={sessionWorkspaceDir}
                      token={token}
                      onOpenInFiles={(path) => {
                        setFilesFocusPath(path);
                        v2SetActiveDockTab("files");
                      }}
                    />
                  </div>
                  {!stickToBottom ? (
                    <button
                      type="button"
                      className="chat-feed-jump-latest"
                      onClick={() => {
                        const scroller = scrollerRef.current;
                        if (scroller) {
                          scroller.scrollTop = scroller.scrollHeight;
                        }
                        stickToBottomRef.current = true;
                        setStickToBottom(true);
                      }}
                    >
                      Jump to latest
                    </button>
                  ) : null}
                </div>
                <NoteContextBanner
                  note={noteContextQuery.data}
                  loading={noteContextQuery.isLoading}
                  error={noteContextQuery.error}
                  onClear={clearNoteContext}
                />
                <ApprovalBanner
                  items={pendingPermissions}
                  approving={approvePermissionMutation}
                  denying={denyPermissionMutation}
                />
                <div style={{ borderTop: "1px solid var(--godex-border)", padding: "6px 12px" }}>
                  <Space style={{ width: "100%", justifyContent: "space-between" }} size={4} wrap>
                    <Space size={4}>
                      {modelsQuery.data?.profiles.length ? (
                        <Select
                          size="small"
                          value={selectedProfile?.id}
                          style={{ minWidth: 100, maxWidth: 140 }}
                          loading={modelsQuery.isLoading || modelMutation.isPending}
                          disabled={modelMutation.isPending}
                          onChange={(value) => modelMutation.mutate({ profileId: value, reasoningEffort: sessionReasoningEffort || undefined })}
                          options={modelsQuery.data.profiles.map((profile) => ({
                            value: profile.id,
                            label: `${profile.name || profile.id}`,
                          }))}
                        />
                      ) : null}
                      {modelsQuery.data?.profiles.length && selectedProfile?.id ? (
                        <Select
                          allowClear
                          size="small"
                          placeholder="Think"
                          value={selectedReasoningEffort || undefined}
                          style={{ minWidth: 72, maxWidth: 96 }}
                          loading={modelsQuery.isLoading || modelMutation.isPending}
                          disabled={modelMutation.isPending}
                          onChange={(value) => modelMutation.mutate({ profileId: selectedProfile!.id, reasoningEffort: value || "" })}
                          options={reasoningEffortOptions}
                        />
                      ) : null}
                      <Tooltip title={streamConnected ? t("chat.streamConnected") : t("chat.streamReconnecting")}>
                        <Badge status={streamConnected ? "success" : "processing"} />
                      </Tooltip>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {modelMutation.isPending
                          ? t("chat.modelSwitching")
                          : uploading
                            ? `${t("chat.uploadingAttachments")} ${uploadProgress ?? 0}%`
                            : queuedTurns.length
                              ? `${status} · ${queuedTurns.length} queued`
                              : status}
                      </Typography.Text>
                    </Space>
                    <Space size={4}>
                      <ContextStatusInline summary={contextStatus} inspector={contextInspector} />
                      {running ? (
                        <Segmented
                          size="small"
                          value={queueMode}
                          onChange={(value) => setQueueMode(value as "follow_up" | "steering")}
                          options={[
                            { value: "follow_up", label: "Follow-up" },
                            { value: "steering", label: "Steer" },
                          ]}
                        />
                      ) : null}
                      {running && currentTurnId ? (
                        <Tooltip title={t("chat.cancelTurn")}>
                          <Button
                            danger
                            size="small"
                            icon={<StopOutlined />}
                            aria-label={t("chat.cancelTurn")}
                            loading={cancelTurnMutation.isPending}
                            onClick={() => cancelTurnMutation.mutate({ sessionId, turnId: currentTurnId })}
                          />
                        </Tooltip>
                      ) : null}
                    </Space>
                  </Space>
                </div>
                <Composer
                  disabled={!openQuery.data?.session_id || modelMutation.isPending}
                  uploading={uploading}
                  uploadProgress={uploadProgress}
                  packageCommands={packageCommandsQuery.data ?? []}
                  builtinCommands={builtinCommandsQuery.data ?? []}
                  queuedFiles={queuedComposerFiles}
                  onQueuedFilesConsumed={() => setQueuedComposerFiles([])}
                  onSubmit={onSend}
                />
              </div>
            )}
          </div>
          {/* Right dock: content pane only (tabs are in topbar) */}
          <div className="chat-v2-right" data-collapsed={v2RightCollapsed ? "true" : "false"}>
            <div className="chat-v2-right-resize" onPointerDown={beginV2RightResize} role="separator" aria-label="Resize right panel" />
            <button className="chat-v2-right-close" onClick={() => v2ToggleRight()} aria-label="Close panel">
              <CloseOutlined />
            </button>
            <div className="chat-v2-dock-pane">
              <div className="chat-v2-dock-pane-body" data-active-tab={v2ActiveDockTab}>
                {mountedDockTabs.has("files") ? (
                  <div className="chat-v2-dock-tab-pane" data-active={v2ActiveDockTab === "files" ? "true" : "false"}>
                    <FilesPanel mode="dock" cwd={sessionWorkspaceDir} fillContainer focusPath={filesFocusPath} onAttachFile={(file) => setQueuedComposerFiles((current) => [...current, file])} />
                  </div>
                ) : null}
                {mountedDockTabs.has("terminal") ? (
                  <div className="chat-v2-dock-tab-pane" data-active={v2ActiveDockTab === "terminal" ? "true" : "false"}>
                    <div className="chat-v2-terminal-tabs" data-testid="chat-v2-terminal-tabs">
                      {terminalTabs.map((id) => (
                        <button
                          key={id}
                          type="button"
                          className={`chat-v2-terminal-tab${activeTerminal === id ? " chat-v2-terminal-tab-active" : ""}`}
                          onClick={() => setActiveTerminal(id)}
                        >
                          <span className="chat-v2-terminal-tab-label">Terminal {id + 1}</span>
                          {terminalTabs.length > 1 ? (
                            <CloseOutlined
                              className="chat-v2-terminal-tab-close"
                              aria-label={`Close terminal ${id + 1}`}
                              onClick={(e) => {
                                e.stopPropagation();
                                closeTerminalTab(id);
                              }}
                            />
                          ) : null}
                        </button>
                      ))}
                      <button type="button" className="chat-v2-terminal-tab-add" aria-label="New terminal" onClick={addTerminalTab}>
                        <PlusOutlined />
                      </button>
                    </div>
                    <div className="chat-v2-terminal-panes">
                      {terminalTabs.map((id) => (
                        <div
                          key={id}
                          className="chat-v2-terminal-pane"
                          data-active={activeTerminal === id ? "true" : "false"}
                        >
                          <TerminalPanel workspaceDir={sessionWorkspaceDir} execution={terminalExecution} />
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}
                {mountedDockTabs.has("tasks") ? (
                  <div className="chat-v2-dock-tab-pane" data-active={v2ActiveDockTab === "tasks" ? "true" : "false"}>
                    <TaskCenterPanel
                      outcomes={taskOutcomes}
                      collapsed={false}
                      onCollapsedChange={() => v2SetActiveDockTab(v2ActiveDockTab)}
                      reviewingJobId={reviewSubagentMutation.isPending ? reviewSubagentMutation.variables : undefined}
                      mergingJobId={mergeSubagentMutation.isPending ? mergeSubagentMutation.variables : undefined}
                      resumingJobId={resumeSubagentMutation.isPending ? resumeSubagentMutation.variables : undefined}
                      cancelingJobId={cancelSubagentMutation.isPending ? cancelSubagentMutation.variables : undefined}
                      runningWorkflowId={runLongTaskMutation.isPending ? runLongTaskMutation.variables : undefined}
                      cancelingLongTask={cancelLongTaskMutation.isPending ? cancelLongTaskMutation.variables : undefined}
                      finalizingLongTask={finalizeLongTaskMutation.isPending ? finalizeLongTaskMutation.variables : undefined}
                      onReviewSubagent={reviewSubagentInDrawer}
                      onMergeSubagent={(jobId) => mergeSubagentMutation.mutate(jobId)}
                      onResumeSubagent={(jobId) => resumeSubagentMutation.mutate(jobId)}
                      onCancelSubagent={(jobId) => cancelSubagentMutation.mutate(jobId)}
                      onRunLongTask={(workflowId) => runLongTaskMutation.mutate(workflowId)}
                      onCancelLongTask={(workflowId, nodeId) => cancelLongTaskMutation.mutate({ workflowId, nodeId })}
                      onFinalizeLongTask={(workflowId, nodeId) => finalizeLongTaskMutation.mutate({ workflowId, nodeId })}
                      onOpenReviewMergeCenter={openReviewMergeCenter}
                    />
                  </div>
                ) : null}
                {mountedDockTabs.has("preview") ? (
                  <div className="chat-v2-dock-tab-pane" data-active={v2ActiveDockTab === "preview" ? "true" : "false"}>
                    <PreviewPanel workspaceDir={sessionWorkspaceDir} token={token} />
                  </div>
                ) : null}
                {mountedDockTabs.has("status") ? (
                  <div className="chat-v2-dock-tab-pane" data-active={v2ActiveDockTab === "status" ? "true" : "false"}>
                    {inspectorPanel}
                  </div>
                ) : null}
              </div>
            </div>
          </div>
        </div>
      <Drawer
        title={`Subagent review${subagentReview?.job_id ? ` · ${shortTurnId(subagentReview.job_id)}` : ""}`}
        placement="right"
        width={720}
        open={subagentReviewOpen}
        onClose={() => setSubagentReviewOpen(false)}
      >
        <SubagentReviewPanel review={subagentReview} loading={reviewSubagentMutation.isPending} />
      </Drawer>
      <ReviewMergeCenterPanel
        open={reviewMergeOpen}
        summary={reviewMergeSummary}
        filter={reviewMergeFilter}
        selectedJobId={reviewMergeSelectedJobId}
        outcomes={taskOutcomes}
        review={subagentReview}
        mergeResult={subagentMergeResult}
        reviewingJobId={reviewSubagentMutation.isPending ? reviewSubagentMutation.variables : undefined}
        mergingJobId={mergeSubagentMutation.isPending ? mergeSubagentMutation.variables : undefined}
        resumingJobId={resumeSubagentMutation.isPending ? resumeSubagentMutation.variables : undefined}
        cancelingJobId={cancelSubagentMutation.isPending ? cancelSubagentMutation.variables : undefined}
        onClose={() => setReviewMergeOpen(false)}
        onFilterChange={setReviewMergeFilter}
        onSelectJob={setReviewMergeSelectedJobId}
        onReview={reviewSubagentInCenter}
        onMerge={(jobId) => {
          setReviewMergeSelectedJobId(jobId);
          mergeSubagentMutation.mutate(jobId);
        }}
        onResume={(jobId) => resumeSubagentMutation.mutate(jobId)}
        onCancel={(jobId) => cancelSubagentMutation.mutate(jobId)}
      />
      {refluxBubbles.length > 0 ? (
        <>
          {refluxBubbles.map((b, i) => (
            <LongTaskRefluxBubble
              key={b.id}
              longtaskId={b.longtaskId}
              status={b.status}
              content={b.content}
              stackIndex={i}
              onDismiss={() => {
                if (refluxDismissed.has(b.dismissKey)) return;
                const next = new Set(refluxDismissed);
                next.add(b.dismissKey);
                setRefluxDismissed(next);
                writePersistedRefluxDismissed(next);
              }}
            />
          ))}
        </>
      ) : null}
    </>
  );
}
