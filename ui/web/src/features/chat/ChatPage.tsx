import { useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  App as AntApp,
  Badge,
  Button,
  Card,
  Descriptions,
  Drawer,
  Empty,
  Grid,
  Input,
  List,
  Popconfirm,
  Progress,
  Segmented,
  Select,
  Space,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { ApartmentOutlined, CheckOutlined, CopyOutlined, DeleteOutlined, EyeOutlined, FileTextOutlined, HistoryOutlined, MenuOutlined, PlayCircleOutlined, PlusOutlined, RedoOutlined, SafetyCertificateOutlined, StopOutlined } from "@ant-design/icons";
import { Conversations } from "@ant-design/x";
import { Composer, type ComposerSubmission } from "../../components/Composer";
import { MessageFeed } from "../../components/MessageFeed";
import { ResizeHandle } from "../../components/ResizeHandle";
import { SubagentCard } from "../../components/SubagentCard";
import { useI18n } from "../../i18n";
import {
  APIError,
  approveSessionPermission,
  cancelSessionLongTask,
  cancelSessionTurn,
  cancelSessionSubagent,
  deleteSession,
  denySessionPermission,
  executeCommand,
  getNote,
  forkSession,
  finalizeSessionLongTaskStory,
  getModels,
  getMeta,
  getSessionContextInspector,
  getSessionTimeline,
  getSessionTimelinePage,
  getSnapshot,
  getActiveSessionSkills,
  listPackageCommands,
  listPackageRoles,
  listSessionLongTasks,
  listSessionSubagents,
  listSessions,
  mergeSessionSubagent,
  openSession,
  reviewSessionSubagent,
  retrySessionTurn,
  runSessionLongTask,
  resumeSessionSubagent,
  resumeSessionTurn,
  saveNote,
  setSessionModel,
  submitMessage,
  unloadSessionSkill,
  uploadAttachments,
} from "../../lib/api";
import { buildChatRoute, buildChatRouteForSession, locatorMatchesRoute } from "../../lib/chatRoutes";
import { writeClipboardText } from "../../lib/clipboard";
import { streamEvents } from "../../lib/sse";
import type {
  ListedSession,
  LongTaskView,
  PendingPermission,
  RuntimeEvent,
  SkillActivation,
  SessionContextInspector,
  SessionTimelineEntry,
  TimelinePage,
  DurableSubagentJob,
  DurableSubagentReview,
  FeedItem,
  Note,
  PackageRoleEntry,
  SubagentProgressItem,
  TurnRecord,
} from "../../lib/types";
import { useChatStore } from "../../store/chat";
import { useSettingsStore } from "../../store/settings";
import { useResizableWidth } from "../../hooks/useResizableWidth";

function makeSessionKey() {
  return crypto.randomUUID();
}

type TimelineFilterState = {
  types: string[];
  q: string;
  jobId: string;
  turnId: string;
  limit: number;
  currentTurnOnly: boolean;
};

const reasoningEffortOptions = [
  { value: "none", label: "None" },
  { value: "minimal", label: "Minimal" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "X High" },
];

const defaultTimelineTypes = [
  "user_message_accepted",
  "runner_phase_changed",
  "subagent_job_updated",
  "tool_call_started",
  "tool_call_finished",
  "error_raised",
  "turn_completed",
];

function defaultTimelineFilters(): TimelineFilterState {
  return {
    types: [...defaultTimelineTypes],
    q: "",
    jobId: "",
    turnId: "",
    limit: 50,
    currentTurnOnly: false,
  };
}

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
  const defaultSessionKey = useSettingsStore((state) => state.defaultSessionKey);
  const setDefaultSessionKey = useSettingsStore((state) => state.setDefaultSessionKey);

  const sessionId = useChatStore((state) => state.sessionId);
  const historyItems = useChatStore((state) => state.historyItems);
  const overlayItems = useChatStore((state) => state.overlayItems);
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
  const [uploadProgress, setUploadProgress] = useState<number | null>(null);
  const [uploading, setUploading] = useState(false);
  const [timelineItems, setTimelineItems] = useState<SessionTimelineEntry[]>([]);
  const [subagentReview, setSubagentReview] = useState<DurableSubagentReview | null>(null);
  const [subagentReviewOpen, setSubagentReviewOpen] = useState(false);
  const [channelFilter, setChannelFilter] = useState("all");
  const [queueMode, setQueueMode] = useState<"follow_up" | "steering">("follow_up");
  const [pendingModelProfileID, setPendingModelProfileID] = useState<string | null>(null);
  const [pendingReasoningEffort, setPendingReasoningEffort] = useState<string | null>(null);
  const [timelineFilters, setTimelineFilters] = useState<TimelineFilterState>(() => defaultTimelineFilters());
  const [timelineCursor, setTimelineCursor] = useState("");
  const [timelineCursorStack, setTimelineCursorStack] = useState<string[]>([]);
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  const [sessionPaneWidth, beginSessionPaneResize] = useResizableWidth({
    storageKey: "godex.chatSessionsWidth",
    defaultWidth: 320,
    min: 240,
    max: 560,
  });
  const [inspectorPaneWidth, beginInspectorPaneResize] = useResizableWidth({
    storageKey: "godex.chatInspectorWidth",
    defaultWidth: 380,
    min: 320,
    max: 720,
    direction: "left",
  });

  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const routeUserId = searchParams.get("user_id");
  const noteContextId = searchParams.get("note_id")?.trim() || "";
  const sessionKey = routeSessionKey || defaultSessionKey || "";
  const sessionLocator = useMemo(
    () => ({
      channel: routeChannel || "web",
      key: sessionKey,
      ...(routeUserId ? { user_id: routeUserId } : {}),
    }),
    [routeChannel, routeUserId, sessionKey],
  );

  useEffect(() => {
    if (routeSessionKey && routeChannel) {
      return;
    }
    const next = defaultSessionKey || makeSessionKey();
    if (!defaultSessionKey) {
      setDefaultSessionKey(next);
    }
    navigate(`/chat/web/${next}`, { replace: true });
  }, [defaultSessionKey, navigate, routeChannel, routeSessionKey, setDefaultSessionKey]);

  const openQuery = useQuery({
    queryKey: ["session-open", token, sessionLocator.channel, sessionLocator.key, sessionLocator.user_id],
    enabled: !!sessionKey && (!authRequired || !!token),
    queryFn: async () => openSession(token || null, sessionLocator),
  });

  const noteContextQuery = useQuery({
    queryKey: ["note-context", token, noteContextId],
    enabled: !!noteContextId && (!authRequired || !!token),
    queryFn: () => getNote(token || null, noteContextId),
  });
  const saveMessageToNoteMutation = useMutation({
    mutationFn: async (item: FeedItem) => {
      const body = item.body.trim();
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
      syncSnapshot(snapshotQuery.data.display_messages ?? snapshotQuery.data.messages, snapshotQuery.data.running, snapshotQuery.data.active_turn_id);
    }
  }, [snapshotQuery.data, syncSnapshot]);

  const timelineQuery = useQuery({
    queryKey: ["timeline", token, openQuery.data?.session_id],
    enabled: !!openQuery.data?.session_id && (!authRequired || !!token),
    queryFn: async () => getSessionTimeline(token || null, openQuery.data!.session_id, 80),
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
  });

  const packageCommandsQuery = useQuery({
    queryKey: ["package-commands", token],
    enabled: !authRequired || !!token,
    queryFn: async () => listPackageCommands(token || null, false),
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
    queryKey: ["sessions", token],
    enabled: !authRequired || !!token,
    queryFn: async () => listSessions(token || null),
  });

  useEffect(() => {
    if (openQuery.data?.session_id) {
      void queryClient.invalidateQueries({ queryKey: ["sessions", token] });
    }
  }, [openQuery.data?.session_id, queryClient, token]);

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
          () => setStreamConnected(true),
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
  const subagentJobs = useMemo(
    () => mergeSubagentItems((subagentsQuery.data ?? []).map(subagentJobToFeedItem), collectSubagentJobs(timelineItems)),
    [subagentsQuery.data, timelineItems],
  );
  const pendingPermissions = snapshotQuery.data?.pending_permissions ?? [];
  const turnRecords = snapshotQuery.data?.turns ?? [];
  const queuedTurns = snapshotQuery.data?.queued_turns ?? [];
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
      navigate(buildChatRoute(opened.locator));
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
      setSubagentReviewOpen(true);
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

  useEffect(() => {
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
      const commandResult = await executeCommand(token || null, activeSessionId, text, noteContextMetadata(noteContextQuery.data, noteContextId));
      if (commandResult.dispatched_turn_id) {
        setRunningTurn(commandResult.dispatched_turn_id);
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

  const createSession = (replace = false) => {
    const next = makeSessionKey();
    setDefaultSessionKey(next);
    reset();
    navigate(`/chat/web/${next}`, { replace });
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

  const sessionPanel = (
    <SessionPanel
      sessions={filteredSessions}
      allChannels={channels}
      channelFilter={channelFilter}
      activeSessionId={openQuery.data?.session_id ?? ""}
      deletingSessionId={deleteSessionMutation.variables?.session_id ?? ""}
      onChannelChange={setChannelFilter}
      onCreate={() => createSession()}
      onDelete={(session) => deleteSessionMutation.mutate(session)}
      onSelect={(session) => {
        navigate(buildChatRouteForSession(session));
        setSessionsOpen(false);
      }}
    />
  );
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
      pendingPermissions={pendingPermissions}
      longTasks={longTasksQuery.data ?? []}
      longTasksLoading={longTasksQuery.isLoading || longTasksQuery.isFetching}
      subagentJobs={subagentJobs}
      packageRoles={packageRolesQuery.data ?? []}
      packageRolesLoading={packageRolesQuery.isLoading}
      turnRecords={turnRecords}
      timelineItems={timelineItems}
      timelinePage={timelinePageQuery.data}
      timelinePageLoading={timelinePageQuery.isLoading || timelinePageQuery.isFetching}
      timelineFilters={timelineFilters}
      currentTurnId={currentTimelineTurnId}
      canPreviousTimelinePage={timelineCursorStack.length > 0}
      onTimelineFiltersChange={updateTimelineFilters}
      onNextTimelinePage={goToNextTimelinePage}
      onPreviousTimelinePage={goToPreviousTimelinePage}
      contextInspector={contextInspector}
      contextLoading={contextInspectorQuery.isLoading}
      activeSkills={activeSkillsQuery.data ?? []}
      activeSkillsLoading={activeSkillsQuery.isLoading}
      unloadingSkill={unloadSkillMutation}
      approving={approvePermissionMutation}
      denying={denyPermissionMutation}
      reviewingSubagent={reviewSubagentMutation}
      cancelingSubagent={cancelSubagentMutation}
      resumingSubagent={resumeSubagentMutation}
      mergingSubagent={mergeSubagentMutation}
      onReviewSubagent={(jobId) => reviewSubagentMutation.mutate(jobId)}
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
    <div
      className="chat-page"
      style={{ "--chat-sessions-width": `${sessionPaneWidth}px`, "--chat-inspector-width": `${inspectorPaneWidth}px` } as CSSProperties}
    >
      <aside className="chat-sessions">
        {sessionPanel}
        <ResizeHandle label="Resize sessions" onPointerDown={beginSessionPaneResize} />
      </aside>
      <section className="chat-main">
        <Card className="chat-session-card" size="small" styles={{ body: { padding: "10px 16px" } }}>
          <div className="chat-session-header">
            <Tooltip title={t("chat.copySessionInfo")}>
              <button type="button" className="chat-session-summary" onClick={() => void copySessionInfo()}>
                <Typography.Text className="chat-session-title" strong ellipsis={{ tooltip: sessionTitle }}>
                  {sessionTitle}
                </Typography.Text>
                <Typography.Text className="chat-session-subtitle" type="secondary" ellipsis={{ tooltip: headerSubtitle }}>
                  {headerSubtitle}
                </Typography.Text>
              </button>
            </Tooltip>
            <Space className="chat-session-actions" size={compactHeader ? 8 : 12}>
              {modelsQuery.data?.profiles.length ? (
                <Select
                  size={compactHeader ? "small" : "middle"}
                  value={selectedProfile?.id}
                  style={{ minWidth: compactHeader ? 118 : 180 }}
                  loading={modelsQuery.isLoading || modelMutation.isPending}
                  disabled={modelMutation.isPending}
                  onChange={(value) => modelMutation.mutate({ profileId: value, reasoningEffort: sessionReasoningEffort || undefined })}
                  options={modelsQuery.data.profiles.map((profile) => ({
                    value: profile.id,
                    label: `${profile.name || profile.id}`,
                    title: `${profile.name || profile.id} · ${profile.model}`,
                  }))}
                />
              ) : null}
              {modelsQuery.data?.profiles.length ? (
                <Select
                  allowClear
                  size={compactHeader ? "small" : "middle"}
                  placeholder="Reasoning"
                  value={selectedReasoningEffort || undefined}
                  style={{ minWidth: compactHeader ? 96 : 128 }}
                  loading={modelsQuery.isLoading || modelMutation.isPending}
                  disabled={modelMutation.isPending || !selectedProfile?.id}
                  onChange={(value) => modelMutation.mutate({ profileId: selectedProfile!.id, reasoningEffort: value || "" })}
                  options={reasoningEffortOptions}
                />
              ) : null}
              {modelsQuery.data?.profiles.length ? <Tag color={modelScopeColor}>{modelScopeLabel}</Tag> : null}
              <Tooltip title={streamConnected ? t("chat.streamConnected") : t("chat.streamReconnecting")}>
                <Badge
                  status={streamConnected ? "success" : "processing"}
                  text={compactHeader ? undefined : streamConnected ? t("chat.streamConnected") : t("chat.streamReconnecting")}
                />
              </Tooltip>
              <Tag color={running ? "processing" : "default"}>{running ? t("chat.running") : t("chat.idle")}</Tag>
              {running && currentTurnId ? (
                <Tooltip title={t("chat.cancelTurn")}>
                  <Button
                    danger
                    size={compactHeader ? "small" : "middle"}
                    icon={<StopOutlined />}
                    aria-label={t("chat.cancelTurn")}
                    loading={cancelTurnMutation.isPending}
                    onClick={() => cancelTurnMutation.mutate({ sessionId, turnId: currentTurnId })}
                  />
                </Tooltip>
              ) : null}
              <Tooltip title={t("chat.copySessionInfo")}>
                <Button
                  size={compactHeader ? "small" : "middle"}
                  icon={<CopyOutlined />}
                  aria-label={t("chat.copySessionInfo")}
                  onClick={() => void copySessionInfo()}
                />
              </Tooltip>
              <Tooltip title="Fork session">
                <Button
                  size={compactHeader ? "small" : "middle"}
                  icon={<ApartmentOutlined />}
                  aria-label="Fork session"
                  loading={forkMutation.isPending}
                  onClick={() => forkMutation.mutate()}
                />
              </Tooltip>
              <Button
                icon={<MenuOutlined />}
                aria-label="Open sessions"
                onClick={() => setSessionsOpen(true)}
                className="chat-mobile-action"
              />
              <Badge count={pendingPermissions.length} size="small">
                <Button
                  icon={<HistoryOutlined />}
                  aria-label="Open inspector"
                  onClick={() => setInspectorOpen(true)}
                  className="chat-mobile-action"
                />
              </Badge>
            </Space>
          </div>
        </Card>

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
          <>
            <div ref={scrollerRef} className="chat-feed">
              <div className="chat-feed-inner">
                <MessageFeed
                  items={items}
                  onToggleTool={toggleTool}
                  onSaveToNote={(item) => saveMessageToNoteMutation.mutate(item)}
                  savingToNote={saveMessageToNoteMutation.isPending}
                  hasNoteContext={!!noteContextQuery.data}
                />
              </div>
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
            <div style={{ borderTop: "1px solid var(--godex-border)", padding: "6px 16px" }}>
              <Space style={{ width: "100%", justifyContent: "space-between" }} wrap>
                <Typography.Text type="secondary">
                  {modelMutation.isPending
                    ? t("chat.modelSwitching")
                    : uploading
                      ? `${t("chat.uploadingAttachments")} ${uploadProgress ?? 0}%`
                      : queuedTurns.length
                        ? `${status} · ${queuedTurns.length} queued`
                        : status}
                </Typography.Text>
                <ContextStatusInline summary={contextStatus} />
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
              </Space>
            </div>
            <Composer
              disabled={!openQuery.data?.session_id || modelMutation.isPending}
              uploading={uploading}
              uploadProgress={uploadProgress}
              packageCommands={packageCommandsQuery.data ?? []}
              onSubmit={onSend}
            />
          </>
        )}
      </section>
      <aside className="chat-inspector">
        <ResizeHandle label="Resize inspector" placement="left" onPointerDown={beginInspectorPaneResize} />
        {inspectorPanel}
      </aside>
      <Drawer title={t("sessions.title")} placement="left" width={320} open={sessionsOpen} onClose={() => setSessionsOpen(false)}>
        {sessionPanel}
      </Drawer>
      <Drawer title={t("chat.contextRecallTitle")} placement="right" width={380} open={inspectorOpen} onClose={() => setInspectorOpen(false)}>
        {inspectorPanel}
      </Drawer>
      <Drawer
        title={`Subagent review${subagentReview?.job_id ? ` · ${shortTurnId(subagentReview.job_id)}` : ""}`}
        placement="right"
        width={720}
        open={subagentReviewOpen}
        onClose={() => setSubagentReviewOpen(false)}
      >
        <SubagentReviewPanel review={subagentReview} loading={reviewSubagentMutation.isPending} />
      </Drawer>
    </div>
  );
}

function SessionPanel(props: {
  sessions: ListedSession[];
  allChannels: string[];
  channelFilter: string;
  activeSessionId: string;
  deletingSessionId: string;
  onChannelChange: (value: string) => void;
  onCreate: () => void;
  onSelect: (session: ListedSession) => void;
  onDelete: (session: ListedSession) => void;
}) {
  const { t } = useI18n();
  const active = props.sessions.find((session) => session.session_id === props.activeSessionId);
  return (
    <div className="panel-scroll">
      <Space direction="vertical" size={14} style={{ width: "100%" }}>
        <Button block type="primary" icon={<PlusOutlined />} aria-label={t("sessions.new")} onClick={props.onCreate}>
          {t("sessions.new")}
        </Button>
        <Tabs
          size="small"
          activeKey={props.channelFilter}
          onChange={props.onChannelChange}
          items={props.allChannels.map((channel) => ({
            key: channel,
            label: channel === "all" ? t("sessions.channelAll") : channel,
          }))}
        />
        <Conversations
          activeKey={props.activeSessionId}
          items={props.sessions.map((session) => ({
            key: session.session_id,
            label: (
              <Space direction="vertical" size={2} style={{ width: "100%" }}>
                <Space wrap>
                  <Typography.Text strong ellipsis={{ tooltip: session.title || session.locator.key || session.session_id }} style={{ maxWidth: 180 }}>
                    {session.title || session.locator.key || session.session_id}
                  </Typography.Text>
                  {session.running ? <Badge status="processing" /> : null}
                  {session.parent_session_id ? <Tag color="blue">branch</Tag> : null}
                </Space>
                <Typography.Text type="secondary">
                  {session.branch_title || session.locator.channel || "web"} · {new Date(session.updated_at).toLocaleString()}
                </Typography.Text>
              </Space>
            ),
            group: session.locator.channel || "web",
          }))}
          groupable
          onActiveChange={(key) => {
            const session = props.sessions.find((item) => item.session_id === key);
            if (session) {
              props.onSelect(session);
            }
          }}
        />
        {active ? (
          <Popconfirm title={t("sessions.deleteConfirm")} onConfirm={() => props.onDelete(active)}>
            <Button block danger icon={<DeleteOutlined />} aria-label={t("sessions.delete")} loading={props.deletingSessionId === active.session_id}>
              {t("sessions.delete")}
            </Button>
          </Popconfirm>
        ) : null}
      </Space>
    </div>
  );
}

function InspectorTabs(props: {
  pendingPermissions: PendingPermission[];
  longTasks: LongTaskView[];
  longTasksLoading: boolean;
  subagentJobs: FeedItem[];
  packageRoles: PackageRoleEntry[];
  packageRolesLoading: boolean;
  turnRecords: TurnRecord[];
  timelineItems: SessionTimelineEntry[];
  timelinePage?: TimelinePage;
  timelinePageLoading: boolean;
  timelineFilters: TimelineFilterState;
  currentTurnId: string;
  canPreviousTimelinePage: boolean;
  onTimelineFiltersChange: (filters: TimelineFilterState) => void;
  onNextTimelinePage: () => void;
  onPreviousTimelinePage: () => void;
  contextInspector: SessionContextInspector | null;
  contextLoading: boolean;
  activeSkills: SkillActivation[];
  activeSkillsLoading: boolean;
  unloadingSkill: ReturnType<typeof useMutation<SkillActivation, Error, string>>;
  approving: ReturnType<typeof useMutation<unknown, Error, { requestId: string; scope: "once" | "session" }>>;
  denying: ReturnType<typeof useMutation<unknown, Error, { requestId: string }>>;
  reviewingSubagent: ReturnType<typeof useMutation<unknown, Error, string>>;
  cancelingSubagent: ReturnType<typeof useMutation<unknown, Error, string>>;
  resumingSubagent: ReturnType<typeof useMutation<unknown, Error, string>>;
  mergingSubagent: ReturnType<typeof useMutation<unknown, Error, string>>;
  onReviewSubagent: (jobId: string) => void;
  onCancelSubagent: (jobId: string) => void;
  onResumeSubagent: (jobId: string) => void;
  onMergeSubagent: (jobId: string) => void;
  runningLongTask: ReturnType<typeof useMutation<unknown, Error, string>>;
  cancelingLongTask: ReturnType<typeof useMutation<unknown, Error, { workflowId: string; nodeId: string }>>;
  finalizingLongTask: ReturnType<typeof useMutation<unknown, Error, { workflowId: string; nodeId: string }>>;
  onRunLongTask: (workflowId: string) => void;
  onCancelLongTask: (workflowId: string, nodeId: string) => void;
  onFinalizeLongTask: (workflowId: string, nodeId: string) => void;
  retrying: ReturnType<typeof useMutation<unknown, Error, { sessionId: string; turnId: string }>>;
  resuming: ReturnType<typeof useMutation<unknown, Error, { sessionId: string; turnId: string }>>;
  onRetryTurn: (turnId: string) => void;
  onResumeTurn: (turnId: string) => void;
  onUnloadSkill: (skillId: string) => void;
}) {
  const { t } = useI18n();
  return (
    <div className="panel-scroll">
      <Tabs
        size="small"
        items={[
          {
            key: "approvals",
            label: (
              <Badge count={props.pendingPermissions.length} size="small">
                <span>{t("chat.pendingApprovalsTitle")}</span>
              </Badge>
            ),
            children: (
              <ApprovalList
                items={props.pendingPermissions}
                approving={props.approving}
                denying={props.denying}
              />
            ),
          },
          {
            key: "context",
            label: t("chat.contextRecallTitle"),
            children: (
              <ContextRecallPanel
                inspector={props.contextInspector}
                loading={props.contextLoading}
                activeSkills={props.activeSkills}
                activeSkillsLoading={props.activeSkillsLoading}
                unloadingSkill={props.unloadingSkill}
                onUnloadSkill={props.onUnloadSkill}
              />
            ),
          },
          {
            key: "turns",
            label: t("chat.turnsTitle"),
            children: (
              <TurnList
                items={props.turnRecords}
                retrying={props.retrying}
                resuming={props.resuming}
                onRetry={props.onRetryTurn}
                onResume={props.onResumeTurn}
              />
            ),
          },
          {
            key: "subagents",
            label: (
              <Badge count={props.subagentJobs.length} size="small">
                <span>Subagents</span>
              </Badge>
            ),
            children: (
              <Space direction="vertical" size={12} style={{ width: "100%" }}>
                <AvailableSubagentRoles items={props.packageRoles} loading={props.packageRolesLoading} />
                <SubagentList
                  items={props.subagentJobs}
                  reviewing={props.reviewingSubagent}
                  canceling={props.cancelingSubagent}
                  resuming={props.resumingSubagent}
                  merging={props.mergingSubagent}
                  onReview={props.onReviewSubagent}
                  onCancel={props.onCancelSubagent}
                  onResume={props.onResumeSubagent}
                  onMerge={props.onMergeSubagent}
                />
              </Space>
            ),
          },
          {
            key: "longtasks",
            label: (
              <Badge count={props.longTasks.filter((item) => item.status !== "completed").length} size="small">
                <span>LongTasks</span>
              </Badge>
            ),
            children: (
              <LongTaskList
                items={props.longTasks}
                loading={props.longTasksLoading}
                running={props.runningLongTask}
                canceling={props.cancelingLongTask}
                finalizing={props.finalizingLongTask}
                onRun={props.onRunLongTask}
                onCancel={props.onCancelLongTask}
                onFinalize={props.onFinalizeLongTask}
              />
            ),
          },
          {
            key: "timeline",
            label: t("chat.sessionTimelineTitle"),
            children: (
              <TimelineList
                page={props.timelinePage}
                fallbackItems={props.timelineItems}
                loading={props.timelinePageLoading}
                filters={props.timelineFilters}
                currentTurnId={props.currentTurnId}
                canPrevious={props.canPreviousTimelinePage}
                onFiltersChange={props.onTimelineFiltersChange}
                onNextPage={props.onNextTimelinePage}
                onPreviousPage={props.onPreviousTimelinePage}
              />
            ),
          },
        ]}
      />
    </div>
  );
}

function compactWorkspaceName(path: string) {
  const normalized = path.trim();
  if (!normalized) {
    return "";
  }
  const parts = normalized.split(/[\\/]/).filter(Boolean);
  return parts.at(-1) || normalized;
}

function noteContextMetadata(note?: Note, noteId?: string): Record<string, string> | undefined {
  const id = note?.id || noteId?.trim() || "";
  if (!id) {
    return undefined;
  }
  const metadata: Record<string, string> = {
    note_id: id,
    app_object_type: "note",
    app_object_id: id,
  };
  if (note?.title) {
    metadata.app_object_title = note.title;
  }
  return metadata;
}

function NoteContextBanner({
  note,
  loading,
  error,
  onClear,
}: {
  note?: Note;
  loading: boolean;
  error: unknown;
  onClear: () => void;
}) {
  const { t } = useI18n();
  if (!loading && !error && !note) {
    return null;
  }
  return (
    <div className="chat-note-context">
      {error ? (
        <Alert type="warning" showIcon message={t("chat.noteContextError")} action={<Button size="small" onClick={onClear}>{t("chat.noteContextClear")}</Button>} />
      ) : (
        <Alert
          type="info"
          showIcon
          message={loading ? t("chat.noteContextLoading") : t("chat.noteContextTitle")}
          description={note ? (
            <Space size={6} wrap>
              <FileTextOutlined />
              <Typography.Text strong>{note.title}</Typography.Text>
              <Typography.Text type="secondary">{note.id}</Typography.Text>
            </Space>
          ) : null}
          action={<Button size="small" onClick={onClear}>{t("chat.noteContextClear")}</Button>}
        />
      )}
    </div>
  );
}

function ApprovalBanner({
  items,
  approving,
  denying,
}: {
  items: PendingPermission[];
  approving: ReturnType<typeof useMutation<unknown, Error, { requestId: string; scope: "once" | "session" }>>;
  denying: ReturnType<typeof useMutation<unknown, Error, { requestId: string }>>;
}) {
  const { t } = useI18n();
  const item = items[0];
  if (!item) {
    return null;
  }
  const busy = approving.isPending || denying.isPending;
  const title = items.length > 1 ? `${t("chat.pendingApprovalsTitle")} (${items.length})` : t("chat.pendingApprovalsTitle");
  return (
    <div style={{ borderTop: "1px solid var(--godex-border)", padding: "8px 16px" }}>
      <Alert
        type="warning"
        showIcon
        message={
          <Space size={8} wrap>
            <Typography.Text strong>{title}</Typography.Text>
            <Tag color="gold">{permissionRequestTitle(item)}</Tag>
          </Space>
        }
        description={
          <Space direction="vertical" size={6} style={{ width: "100%" }}>
            <Typography.Text>{permissionRequestSummary(item)}</Typography.Text>
            {item.reason ? <Typography.Text type="secondary">{item.reason}</Typography.Text> : null}
            <Space wrap>
              <Button size="small" disabled={busy} onClick={() => approving.mutate({ requestId: item.id, scope: "once" })}>
                {t("chat.allowOnce")}
              </Button>
              <Button size="small" type="primary" disabled={busy} onClick={() => approving.mutate({ requestId: item.id, scope: "session" })}>
                {t("chat.allowSession")}
              </Button>
              <Button size="small" danger disabled={busy} onClick={() => denying.mutate({ requestId: item.id })}>
                {t("chat.deny")}
              </Button>
            </Space>
          </Space>
        }
      />
    </div>
  );
}

function ApprovalList({
  items,
  approving,
  denying,
}: {
  items: PendingPermission[];
  approving: ReturnType<typeof useMutation<unknown, Error, { requestId: string; scope: "once" | "session" }>>;
  denying: ReturnType<typeof useMutation<unknown, Error, { requestId: string }>>;
}) {
  const { t } = useI18n();
  if (items.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.noPendingApprovals")} />;
  }
  return (
    <List
      dataSource={items}
      renderItem={(item) => {
        const busy = approving.isPending || denying.isPending;
        return (
          <List.Item>
            <Card size="small" style={{ width: "100%" }}>
              <Space direction="vertical" size={8} style={{ width: "100%" }}>
                <Space style={{ justifyContent: "space-between", width: "100%" }}>
                  <Typography.Text strong>{permissionRequestTitle(item)}</Typography.Text>
                  <Typography.Text type="secondary">{formatTimelineTime(item.created_at)}</Typography.Text>
                </Space>
                <Typography.Text type="secondary">{permissionRequestSummary(item)}</Typography.Text>
                {item.reason ? <Alert type="info" message={item.reason} /> : null}
                <Space wrap>
                  <Button size="small" disabled={busy} onClick={() => approving.mutate({ requestId: item.id, scope: "once" })}>
                    {t("chat.allowOnce")}
                  </Button>
                  <Button size="small" type="primary" disabled={busy} onClick={() => approving.mutate({ requestId: item.id, scope: "session" })}>
                    {t("chat.allowSession")}
                  </Button>
                  <Button size="small" danger disabled={busy} onClick={() => denying.mutate({ requestId: item.id })}>
                    {t("chat.deny")}
                  </Button>
                </Space>
              </Space>
            </Card>
          </List.Item>
        );
      }}
    />
  );
}

type ContextStatusSummary = {
  text: string;
  tooltip: string;
  budgetPercent: number;
  suggestCompact: boolean;
};

function ContextStatusInline({ summary }: { summary: ContextStatusSummary }) {
  const color = summary.suggestCompact || summary.budgetPercent >= 85 ? "gold" : summary.budgetPercent >= 65 ? "blue" : "default";
  return (
    <Tooltip title={summary.tooltip}>
      <Tag color={color} className="chat-context-status">
        {summary.text}
      </Tag>
    </Tooltip>
  );
}

function ContextRecallPanel({
  inspector,
  loading,
  activeSkills,
  activeSkillsLoading,
  unloadingSkill,
  onUnloadSkill,
}: {
  inspector: SessionContextInspector | null;
  loading: boolean;
  activeSkills: SkillActivation[];
  activeSkillsLoading: boolean;
  unloadingSkill: ReturnType<typeof useMutation<SkillActivation, Error, string>>;
  onUnloadSkill: (skillId: string) => void;
}) {
  const { t } = useI18n();
  if (loading && !inspector) {
    return <Alert type="info" showIcon message={t("chat.contextInspectorLoading")} />;
  }
  const context = inspector?.context;
  const breakdown = context?.token_breakdown;
  const totalTokens = context?.total_token_estimate ?? context?.token_estimate ?? breakdown?.total ?? 0;
  const historyTokens = context?.history_token_estimate ?? breakdown?.history ?? 0;
  const compressionReasons = context?.compression_reasons ?? [];
  const toolRefs = context?.tool_result_references ?? [];
  const budgetPercent =
    context && context.compress_threshold > 0 ? Math.min(100, Math.round((totalTokens / context.compress_threshold) * 100)) : 0;
  const memoryPreview = {
    identity: inspector?.memory_preview?.identity ?? [],
    core: inspector?.memory_preview?.core ?? [],
    relevant: inspector?.memory_preview?.relevant ?? [],
  };
  const breakdownItems = [
    { key: "system", label: t("chat.contextInspectorBreakdownSystem"), value: breakdown?.system ?? 0 },
    { key: "history", label: t("chat.contextInspectorBreakdownHistory"), value: breakdown?.history ?? historyTokens },
    { key: "memory", label: t("chat.contextInspectorBreakdownMemory"), value: breakdown?.memory ?? 0 },
    { key: "runtime", label: t("chat.contextInspectorBreakdownRuntime"), value: breakdown?.runtime ?? 0 },
    { key: "tool_schemas", label: t("chat.contextInspectorBreakdownToolSchemas"), value: breakdown?.tool_schemas ?? 0 },
    { key: "tool_results", label: t("chat.contextInspectorBreakdownToolResults"), value: breakdown?.tool_results ?? 0 },
    { key: "attachments", label: t("chat.contextInspectorBreakdownAttachments"), value: breakdown?.attachments ?? 0 },
  ];
  return (
    <Space direction="vertical" size={14} style={{ width: "100%" }}>
      <Descriptions
        bordered
        size="small"
        column={1}
        items={[
          { key: "messages", label: t("chat.contextInspectorMessages"), children: context?.message_count ?? 0 },
          { key: "tokens", label: t("chat.contextInspectorTokenEstimate"), children: totalTokens },
          { key: "history_tokens", label: t("chat.contextInspectorHistoryTokens"), children: historyTokens },
          { key: "threshold", label: t("chat.contextInspectorThreshold"), children: context?.compress_threshold ?? 0 },
          { key: "skills", label: t("chat.contextInspectorActiveSkills"), children: context?.active_skill_count ?? 0 },
          { key: "approvals", label: t("chat.contextInspectorPendingPermissions"), children: context?.pending_permission_count ?? 0 },
        ]}
      />
      <Card size="small" title={t("chat.activeSkillsTitle")} loading={activeSkillsLoading && activeSkills.length === 0}>
        {activeSkills.length === 0 ? (
          <Typography.Text type="secondary">{t("chat.noActiveSkills")}</Typography.Text>
        ) : (
          <List
            size="small"
            dataSource={activeSkills}
            renderItem={(item) => (
              <List.Item
                actions={[
                  <Popconfirm
                    key="unload"
                    title={t("chat.unloadSkillConfirm")}
                    onConfirm={() => onUnloadSkill(item.id)}
                  >
                    <Button
                      danger
                      size="small"
                      icon={<DeleteOutlined />}
                      loading={unloadingSkill.isPending && unloadingSkill.variables === item.id}
                    >
                      {t("chat.unloadSkill")}
                    </Button>
                  </Popconfirm>,
                ]}
              >
                <List.Item.Meta
                  title={<Typography.Text strong>{item.name || item.id}</Typography.Text>}
                  description={
                    <Space direction="vertical" size={4}>
                      {item.description ? <Typography.Text type="secondary">{item.description}</Typography.Text> : null}
                      {item.loaded_sections?.length ? (
                        <Space wrap size={4}>
                          {item.loaded_sections.map((section) => (
                            <Tag key={section}>{section}</Tag>
                          ))}
                        </Space>
                      ) : null}
                    </Space>
                  }
                />
              </List.Item>
            )}
          />
        )}
      </Card>
      <Card size="small" title={t("chat.contextInspectorStatusTitle")}>
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Typography.Text>{context?.suggest_compact ? t("chat.contextInspectorSuggestCompact") : t("chat.contextInspectorNoCompact")}</Typography.Text>
          <Progress percent={budgetPercent} size="small" status={budgetPercent > 85 ? "exception" : "active"} />
          <Typography.Text type="secondary">
            {context
              ? t("chat.contextInspectorBudgetUsage", {
                  used: totalTokens,
                  limit: context.compress_threshold,
                  percent: budgetPercent,
                })
              : t("chat.contextInspectorNoArchive")}
          </Typography.Text>
          {compressionReasons.length > 0 ? (
            <Space wrap size={4}>
              {compressionReasons.map((reason) => (
                <Tag key={reason}>{reason}</Tag>
              ))}
            </Space>
          ) : null}
        </Space>
      </Card>
      <Card size="small" title={t("chat.contextInspectorBreakdownTitle")}>
        <Descriptions
          size="small"
          column={1}
          items={breakdownItems.map((item) => ({
            key: item.key,
            label: item.label,
            children: item.value,
          }))}
        />
      </Card>
      <Card size="small" title={t("chat.contextInspectorToolResultTitle")}>
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Typography.Text type="secondary">
            {t("chat.contextInspectorToolResultSummary", {
              count: context?.large_tool_result_reference_count ?? toolRefs.length,
            })}
          </Typography.Text>
          {toolRefs.length > 0 ? (
            <List
              size="small"
              dataSource={toolRefs}
              renderItem={(item) => (
                <List.Item>
                  <Space direction="vertical" size={2}>
                    <Typography.Text strong>{item.tool_name || item.tool_use_id || t("chat.contextInspectorToolResultUnknown")}</Typography.Text>
                    <Typography.Text type="secondary">
                      {[item.artifact_path, item.bytes ? `${item.bytes} bytes` : "", item.sha256 ? item.sha256.slice(0, 12) : ""]
                        .filter(Boolean)
                        .join(" · ")}
                    </Typography.Text>
                  </Space>
                </List.Item>
              )}
            />
          ) : (
            <Typography.Text type="secondary">{t("chat.contextInspectorToolResultEmpty")}</Typography.Text>
          )}
        </Space>
      </Card>
      <Card size="small" title={t("chat.contextInspectorRecallTitle")}>
        <Space direction="vertical" size={6}>
          <Typography.Text type="secondary">{t("chat.contextInspectorQueryLabel")}</Typography.Text>
          <Typography.Text>{inspector?.recall_query?.trim() || t("chat.contextInspectorNoQuery")}</Typography.Text>
          {inspector?.history_recall ? (
            <Tag color={inspector.history_recall.allow_tool ? "green" : "default"}>
              {inspector.history_recall.allow_tool ? t("chat.contextInspectorHistoryAllowed") : t("chat.contextInspectorHistoryBlocked")}
            </Tag>
          ) : (
            <Typography.Text type="secondary">{t("chat.contextInspectorNoHistoryDecision")}</Typography.Text>
          )}
        </Space>
      </Card>
      <MemoryPreviewSection title={t("chat.contextInspectorMemoryIdentity")} items={memoryPreview.identity} />
      <MemoryPreviewSection title={t("chat.contextInspectorMemoryCore")} items={memoryPreview.core} />
      <MemoryPreviewSection title={t("chat.contextInspectorMemoryRelevant")} items={memoryPreview.relevant} />
    </Space>
  );
}

function MemoryPreviewSection({
  title,
  items,
}: {
  title: string;
  items: SessionContextInspector["memory_preview"]["identity"];
}) {
  return (
    <Card size="small" title={title} extra={<Tag>{items.length}</Tag>}>
      {items.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
      ) : (
        <List
          size="small"
          dataSource={items}
          renderItem={(item) => (
            <List.Item>
              <Space direction="vertical" size={2}>
                <Typography.Text strong>{item.title}</Typography.Text>
                <Typography.Text type="secondary">{item.summary}</Typography.Text>
                <Space wrap>
                  {item.score ? <Tag>score {item.score}</Tag> : null}
                  {(item.tags ?? []).map((tag) => (
                    <Tag key={tag}>{tag}</Tag>
                  ))}
                </Space>
              </Space>
            </List.Item>
          )}
        />
      )}
    </Card>
  );
}

function TurnList({
  items,
  retrying,
  resuming,
  onRetry,
  onResume,
}: {
  items: TurnRecord[];
  retrying: ReturnType<typeof useMutation<unknown, Error, { sessionId: string; turnId: string }>>;
  resuming: ReturnType<typeof useMutation<unknown, Error, { sessionId: string; turnId: string }>>;
  onRetry: (turnId: string) => void;
  onResume: (turnId: string) => void;
}) {
  const { t } = useI18n();
  if (items.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.noTurns")} />;
  }
  return (
    <List
      size="small"
      dataSource={items.slice().reverse()}
      renderItem={(turn) => (
        <List.Item
          actions={[
            turn.can_resume ? (
              <Tooltip key="resume" title={t("chat.resumeTurn")}>
                <Button
                  size="small"
                  icon={<PlayCircleOutlined />}
                  loading={resuming.isPending && resuming.variables?.turnId === turn.id}
                  onClick={() => onResume(turn.id)}
                />
              </Tooltip>
            ) : null,
            turn.can_retry ? (
              <Tooltip key="retry" title={t("chat.retryTurn")}>
                <Button
                  size="small"
                  icon={<RedoOutlined />}
                  loading={retrying.isPending && retrying.variables?.turnId === turn.id}
                  onClick={() => onRetry(turn.id)}
                />
              </Tooltip>
            ) : null,
          ].filter(Boolean)}
        >
          <List.Item.Meta
            title={
              <Space wrap>
                <Typography.Text strong>{turn.summary || turn.id}</Typography.Text>
                <Tag color={turnStatusColor(turn.status)}>{turn.status || "unknown"}</Tag>
                {turn.retry_of ? <Tag>{t("chat.retryOf", { id: shortTurnId(turn.retry_of) })}</Tag> : null}
              </Space>
            }
            description={
              <Space direction="vertical" size={2}>
                <Typography.Text type="secondary">
                  {turn.source || "web"} · {formatTimelineTime(turn.updated_at)}
                </Typography.Text>
                {turn.error ? <Typography.Text type="danger">{formatTurnError(turn.error)}</Typography.Text> : null}
              </Space>
            }
          />
        </List.Item>
      )}
    />
  );
}

function SubagentList({
  items,
  reviewing,
  canceling,
  resuming,
  merging,
  onReview,
  onCancel,
  onResume,
  onMerge,
}: {
  items: FeedItem[];
  reviewing: ReturnType<typeof useMutation<unknown, Error, string>>;
  canceling: ReturnType<typeof useMutation<unknown, Error, string>>;
  resuming: ReturnType<typeof useMutation<unknown, Error, string>>;
  merging: ReturnType<typeof useMutation<unknown, Error, string>>;
  onReview: (jobId: string) => void;
  onCancel: (jobId: string) => void;
  onResume: (jobId: string) => void;
  onMerge: (jobId: string) => void;
}) {
  if (items.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No subagent jobs" />;
  }
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <SubagentOverview items={items} />
      {items.map((item) => (
        <div key={item.id} className="subagent-inspector-item">
          <SubagentQuickMeta item={item} />
          <SubagentCard
            item={item}
            defaultExpanded
            actions={
              <SubagentActions
                item={item}
                reviewing={reviewing.isPending && reviewing.variables === item.jobId}
                canceling={canceling.isPending && canceling.variables === item.jobId}
                resuming={resuming.isPending && resuming.variables === item.jobId}
                merging={merging.isPending && merging.variables === item.jobId}
                onReview={onReview}
                onCancel={onCancel}
                onResume={onResume}
                onMerge={onMerge}
              />
            }
          />
        </div>
      ))}
    </Space>
  );
}

function LongTaskList({
  items,
  loading,
  running,
  canceling,
  finalizing,
  onRun,
  onCancel,
  onFinalize,
}: {
  items: LongTaskView[];
  loading: boolean;
  running: ReturnType<typeof useMutation<unknown, Error, string>>;
  canceling: ReturnType<typeof useMutation<unknown, Error, { workflowId: string; nodeId: string }>>;
  finalizing: ReturnType<typeof useMutation<unknown, Error, { workflowId: string; nodeId: string }>>;
  onRun: (workflowId: string) => void;
  onCancel: (workflowId: string, nodeId: string) => void;
  onFinalize: (workflowId: string, nodeId: string) => void;
}) {
  if (!loading && items.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No LongTasks" />;
  }
  return (
    <List
      loading={loading}
      dataSource={items}
      rowKey={(item) => item.workflow_id}
      renderItem={(item) => {
        const done = item.stories.filter((story) => story.passes).length;
        const percent = item.total > 0 ? Math.round((done / item.total) * 100) : 0;
        const activeStory = item.stories.find((story) => story.status === "running") ?? item.stories.find((story) => story.status === "error");
        const finalizable = item.stories.find(
          (story) => story.status === "completed" && story.verdict === "pass" && story.validation_status === "pending" && story.node_id,
        );
        return (
          <List.Item>
            <Card size="small" style={{ width: "100%" }}>
              <Space direction="vertical" size={8} style={{ width: "100%" }}>
                <Space wrap align="center">
                  <Typography.Text strong ellipsis style={{ maxWidth: 260 }}>
                    {item.project || item.longtask_id || item.workflow_id}
                  </Typography.Text>
                  <Tag color={item.status === "completed" ? "green" : item.failed ? "red" : item.running ? "processing" : "default"}>
                    {item.status}
                  </Tag>
                  {item.run?.status ? <Tag>{item.run.status}</Tag> : null}
                </Space>
                {item.description ? (
                  <Typography.Paragraph type="secondary" ellipsis={{ rows: 2 }} style={{ marginBottom: 0 }}>
                    {item.description}
                  </Typography.Paragraph>
                ) : null}
                <Progress percent={percent} size="small" status={item.failed ? "exception" : item.status === "completed" ? "success" : "active"} />
                <Space wrap size={[6, 6]}>
                  <Tag>{done}/{item.total} passed</Tag>
                  <Tag>{item.running} running</Tag>
                  <Tag>{item.failed} failed</Tag>
                  {item.run?.blocked_by ? <Tag color="red">blocked: {item.run.blocked_by}</Tag> : null}
                </Space>
                <Space direction="vertical" size={4} style={{ width: "100%" }}>
                  {item.stories.map((story) => (
                    <div key={story.id} className="longtask-story-row">
                      <Space wrap size={[6, 4]}>
                        <Tag color={story.passes ? "green" : story.status === "error" ? "red" : story.status === "running" ? "processing" : "default"}>
                          {story.id}
                        </Tag>
                        <Typography.Text ellipsis style={{ maxWidth: 220 }}>
                          {story.title || story.node_id || story.id}
                        </Typography.Text>
                        {story.validation_status ? <Tag>validation {story.validation_status}</Tag> : null}
                        {story.merge_status ? <Tag>merge {story.merge_status}</Tag> : null}
                        {story.commit_status ? <Tag>commit {story.commit_status}</Tag> : null}
                        {story.repair_attempts ? <Tag color="orange">repair x{story.repair_attempts}</Tag> : null}
                      </Space>
                    </div>
                  ))}
                </Space>
                <Space wrap>
                  <Button
                    size="small"
                    icon={<PlayCircleOutlined />}
                    loading={running.isPending && running.variables === item.workflow_id}
                    onClick={() => onRun(item.workflow_id)}
                  >
                    Run
                  </Button>
                  {finalizable?.node_id ? (
                    <Button
                      size="small"
                      icon={<CheckOutlined />}
                      loading={
                        finalizing.isPending &&
                        finalizing.variables?.workflowId === item.workflow_id &&
                        finalizing.variables?.nodeId === finalizable.node_id
                      }
                      onClick={() => onFinalize(item.workflow_id, finalizable.node_id!)}
                    >
                      Finalize
                    </Button>
                  ) : null}
                  {activeStory?.node_id ? (
                    <Popconfirm title="Cancel this LongTask node?" onConfirm={() => onCancel(item.workflow_id, activeStory.node_id!)}>
                      <Button
                        size="small"
                        danger
                        icon={<StopOutlined />}
                        loading={
                          canceling.isPending &&
                          canceling.variables?.workflowId === item.workflow_id &&
                          canceling.variables?.nodeId === activeStory.node_id
                        }
                      >
                        Cancel
                      </Button>
                    </Popconfirm>
                  ) : null}
                </Space>
              </Space>
            </Card>
          </List.Item>
        );
      }}
    />
  );
}

function SubagentOverview({ items }: { items: FeedItem[] }) {
  const counts = items.reduce<Record<string, number>>((acc, item) => {
    const status = (item.status || "updated").toLowerCase();
    acc[status] = (acc[status] ?? 0) + 1;
    return acc;
  }, {});
  const active = (counts.running ?? 0) + (counts.pending ?? 0) + (counts.pending_approval ?? 0);
  const failed = (counts.error ?? 0) + (counts.failed ?? 0) + (counts.interrupted ?? 0);
  const completed = counts.completed ?? 0;
  return (
    <Card size="small">
      <Space wrap>
        <Tag color={active ? "processing" : "default"}>{active} active</Tag>
        <Tag color={failed ? "red" : "default"}>{failed} needs attention</Tag>
        <Tag color={completed ? "green" : "default"}>{completed} completed</Tag>
        <Typography.Text type="secondary">{items.length} total subagents</Typography.Text>
      </Space>
    </Card>
  );
}

function SubagentQuickMeta({ item }: { item: FeedItem }) {
  const role = item.roleName || item.roleId || item.agentType || "subagent";
  const lastActivity = item.lastRecoveryHint || item.lastRunnerPhase || item.phase || item.lastToolName || item.lastMessage || item.summary || item.status || "";
  return (
    <div className="subagent-quick-meta">
      <Space direction="vertical" size={4} style={{ width: "100%" }}>
        <Space wrap>
          <Tooltip title={item.displayTitle || item.title}>
            <Typography.Text strong ellipsis style={{ maxWidth: 260 }}>
              {item.displayTitle || item.title}
            </Typography.Text>
          </Tooltip>
          {item.sequence ? <Tag>#{item.sequence}</Tag> : null}
          <Tag color="purple">{role}</Tag>
          {item.parentTurnId ? (
            <Tooltip title={item.parentTurnId}>
              <Tag color="blue">parent {shortTurnId(item.parentTurnId)}</Tag>
            </Tooltip>
          ) : null}
          {item.modelRequestCount ? <Tag>{item.modelRequestCount} calls</Tag> : null}
          {item.toolCallCount ? <Tag>{item.toolCallCount} tools</Tag> : null}
        </Space>
        <Typography.Text type="secondary" ellipsis={{ tooltip: item.objective || lastActivity }}>
          {item.objective ? `Objective: ${item.objective}` : "Objective not recorded"}
        </Typography.Text>
        <Typography.Text type="secondary" ellipsis={{ tooltip: lastActivity }}>
          Last activity: {lastActivity || "none"}
        </Typography.Text>
      </Space>
    </div>
  );
}

function SubagentActions({
  item,
  reviewing,
  canceling,
  resuming,
  merging,
  onReview,
  onCancel,
  onResume,
  onMerge,
}: {
  item: FeedItem;
  reviewing: boolean;
  canceling: boolean;
  resuming: boolean;
  merging: boolean;
  onReview: (jobId: string) => void;
  onCancel: (jobId: string) => void;
  onResume: (jobId: string) => void;
  onMerge: (jobId: string) => void;
}) {
  const jobId = item.jobId || "";
  const status = (item.status || "").toLowerCase();
  const canCancel = jobId && status === "running";
  const canResume = jobId && ["canceled", "interrupted", "error", "failed"].includes(status);
  const canReview = jobId && item.writeScope?.length && status !== "running";
  const canMerge = canReview && item.mergeStatus !== "merged";
  return (
    <Space size={4}>
      {canReview ? (
        <Tooltip title="Review subagent changes">
          <Button
            size="small"
            type="text"
            aria-label="Review subagent changes"
            icon={<EyeOutlined />}
            loading={reviewing}
            onClick={() => onReview(jobId)}
          />
        </Tooltip>
      ) : null}
      {canCancel ? (
        <Tooltip title="Cancel subagent">
          <Button
            size="small"
            danger
            type="text"
            aria-label="Cancel subagent"
            icon={<StopOutlined />}
            loading={canceling}
            onClick={() => onCancel(jobId)}
          />
        </Tooltip>
      ) : null}
      {canResume ? (
        <Tooltip title="Resume subagent">
          <Button
            size="small"
            type="text"
            aria-label="Resume subagent"
            icon={<PlayCircleOutlined />}
            loading={resuming}
            onClick={() => onResume(jobId)}
          />
        </Tooltip>
      ) : null}
      {canMerge ? (
        <Popconfirm title="Merge subagent changes?" onConfirm={() => onMerge(jobId)}>
          <Tooltip title="Merge subagent changes">
            <Button size="small" type="text" aria-label="Merge subagent changes" icon={<CheckOutlined />} loading={merging} />
          </Tooltip>
        </Popconfirm>
      ) : null}
    </Space>
  );
}

function SubagentReviewPanel({ review, loading }: { review: DurableSubagentReview | null; loading: boolean }) {
  if (loading && !review) {
    return <Alert type="info" showIcon message="Loading subagent review..." />;
  }
  if (!review) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No review loaded" />;
  }
  return (
    <Space direction="vertical" size={14} style={{ width: "100%" }}>
      <Descriptions
        bordered
        size="small"
        column={1}
        items={[
          { key: "job", label: "Job", children: <Typography.Text copyable>{review.job_id}</Typography.Text> },
          { key: "worktree", label: "Worktree", children: review.worktree_dir ? <Typography.Text copyable>{review.worktree_dir}</Typography.Text> : "-" },
          { key: "scope", label: "Write scope", children: review.write_scope?.length ? review.write_scope.join(", ") : "-" },
          { key: "diff", label: "Diff", children: review.diff_truncated ? <Tag color="gold">truncated</Tag> : <Tag>complete</Tag> },
        ]}
      />
      {review.conflicts?.length ? <Alert type="warning" showIcon message="Merge conflicts" description={review.conflicts.join("\n")} /> : null}
      <Card size="small" title="Changed files">
        {review.changes.length === 0 ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No changes" />
        ) : (
          <List
            size="small"
            dataSource={review.changes}
            renderItem={(change) => (
              <List.Item>
                <Space wrap>
                  <Tag color={fileChangeColor(change.status)}>{change.status}</Tag>
                  <Typography.Text code>{change.path}</Typography.Text>
                  {change.binary ? <Tag>binary</Tag> : null}
                  {typeof change.bytes === "number" ? <Typography.Text type="secondary">{change.bytes} bytes</Typography.Text> : null}
                </Space>
              </List.Item>
            )}
          />
        )}
      </Card>
      <Card size="small" title="Diff">
        {review.diff?.trim() ? (
          <Typography.Paragraph>
            <pre className="subagent-review-diff">{review.diff}</pre>
          </Typography.Paragraph>
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No diff" />
        )}
      </Card>
    </Space>
  );
}

function fileChangeColor(status: string) {
  switch (status) {
    case "added":
      return "green";
    case "deleted":
      return "red";
    case "modified":
      return "blue";
    default:
      return "default";
  }
}

function AvailableSubagentRoles({ items, loading }: { items: PackageRoleEntry[]; loading: boolean }) {
  return (
    <Card size="small" title="Available roles" loading={loading}>
      {items.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No package roles" />
      ) : (
        <List
          size="small"
          dataSource={items}
          renderItem={(role) => {
            const title = `${role.package_name}/${role.id}`;
            return (
              <List.Item>
                <Space direction="vertical" size={4} style={{ width: "100%" }}>
                  <Space wrap>
                    <Tooltip title={title}>
                      <Typography.Text strong ellipsis style={{ maxWidth: 220 }}>
                        {role.name || role.id}
                      </Typography.Text>
                    </Tooltip>
                    <Tag color={role.write_enabled ? "gold" : "default"}>{role.write_enabled ? "write" : "read-only"}</Tag>
                    {role.model_hint ? <Tag>{role.model_hint}</Tag> : null}
                  </Space>
                  <Typography.Text type="secondary" ellipsis={{ tooltip: role.description || title }}>
                    {role.description || title}
                  </Typography.Text>
                  <Space wrap size={4}>
                    <Tooltip title={title}>
                      <Tag>{role.package_name}</Tag>
                    </Tooltip>
                    {role.capabilities?.slice(0, 3).map((capability) => (
                      <Tooltip key={capability} title={capability}>
                        <Tag color="blue">{previewText(capability, 28)}</Tag>
                      </Tooltip>
                    ))}
                    {(role.capabilities?.length ?? 0) > 3 ? <Tag>+{(role.capabilities?.length ?? 0) - 3}</Tag> : null}
                  </Space>
                  {role.tools?.length ? (
                    <Typography.Text type="secondary" ellipsis={{ tooltip: role.tools.join(", ") }}>
                      tools: {role.tools.slice(0, 6).join(", ")}
                      {role.tools.length > 6 ? `, +${role.tools.length - 6}` : ""}
                    </Typography.Text>
                  ) : null}
                </Space>
              </List.Item>
            );
          }}
        />
      )}
    </Card>
  );
}

function TimelineList({
  page,
  fallbackItems,
  loading,
  filters,
  currentTurnId,
  canPrevious,
  onFiltersChange,
  onNextPage,
  onPreviousPage,
}: {
  page?: TimelinePage;
  fallbackItems: SessionTimelineEntry[];
  loading: boolean;
  filters: TimelineFilterState;
  currentTurnId: string;
  canPrevious: boolean;
  onFiltersChange: (filters: TimelineFilterState) => void;
  onNextPage: () => void;
  onPreviousPage: () => void;
}) {
  const { t } = useI18n();
  const items = page?.items ?? fallbackItems.slice().reverse();
  const total = page?.total ?? fallbackItems.length;
  const hasMore = page?.has_more ?? false;
  if (items.length === 0) {
    return (
      <Space direction="vertical" size={12} style={{ width: "100%" }}>
        <TimelineFilters filters={filters} loading={loading} currentTurnId={currentTurnId} onChange={onFiltersChange} />
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.noTimelineEvents")} />
      </Space>
    );
  }
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <TimelineFilters filters={filters} loading={loading} currentTurnId={currentTurnId} onChange={onFiltersChange} />
      <Space style={{ width: "100%", justifyContent: "space-between" }} wrap>
        <Typography.Text type="secondary">{total} events</Typography.Text>
        <Space>
          <Button size="small" disabled={!canPrevious || loading} onClick={onPreviousPage}>
            Previous
          </Button>
          <Button size="small" disabled={!hasMore || loading} onClick={onNextPage}>
            Next
          </Button>
        </Space>
      </Space>
      <List
        size="small"
        loading={loading}
        dataSource={items}
        renderItem={(event) => {
          const payload = (event.payload ?? {}) as Record<string, unknown>;
          const summary = timelineEventSummary(event);
          const fullText = timelineEventFullText(event, summary);
          const jobID = stringFromPayload(payload.job_id);
          return (
            <List.Item>
              <List.Item.Meta
                avatar={<SafetyCertificateOutlined />}
                title={
                  <Space wrap>
                    <Typography.Text strong>{timelineEventLabel(event)}</Typography.Text>
                    <Tag>{event.type}</Tag>
                    {event.turn_id ? (
                      <Tooltip title={event.turn_id}>
                        <Tag color="blue">{shortTurnId(event.turn_id)}</Tag>
                      </Tooltip>
                    ) : null}
                    {jobID ? (
                      <Tooltip title={jobID}>
                        <Tag color="purple">{shortTurnId(jobID)}</Tag>
                      </Tooltip>
                    ) : null}
                    <Typography.Text type="secondary">{formatTimelineTime(event.timestamp)}</Typography.Text>
                  </Space>
                }
                description={
                  <Tooltip title={fullText}>
                    <Typography.Paragraph className="timeline-summary" copyable ellipsis={{ rows: 2, expandable: true, symbol: "more" }}>
                      {summary || fullText || "-"}
                    </Typography.Paragraph>
                  </Tooltip>
                }
              />
            </List.Item>
          );
        }}
      />
      <Space style={{ width: "100%", justifyContent: "flex-end" }}>
        <Button size="small" disabled={!canPrevious || loading} onClick={onPreviousPage}>
          Previous
        </Button>
        <Button size="small" disabled={!hasMore || loading} onClick={onNextPage}>
          Next
        </Button>
      </Space>
    </Space>
  );
}

function TimelineFilters({
  filters,
  loading,
  currentTurnId,
  onChange,
}: {
  filters: TimelineFilterState;
  loading: boolean;
  currentTurnId: string;
  onChange: (filters: TimelineFilterState) => void;
}) {
  const update = (patch: Partial<TimelineFilterState>) => onChange({ ...filters, ...patch });
  const reset = () => onChange(defaultTimelineFilters());
  const effectiveTurnId = filters.currentTurnOnly ? currentTurnId : filters.turnId;
  const defaultActive =
    filters.types.length === defaultTimelineTypes.length &&
    defaultTimelineTypes.every((type) => filters.types.includes(type)) &&
    !filters.q &&
    !filters.jobId &&
    !filters.turnId &&
    !filters.currentTurnOnly &&
    filters.limit === 50;
  return (
    <Space direction="vertical" size={8} style={{ width: "100%" }}>
      <Space style={{ width: "100%", justifyContent: "space-between" }} wrap>
        <Space wrap size={6}>
          <Button
            size="small"
            type={filters.currentTurnOnly ? "primary" : "default"}
            disabled={loading || !currentTurnId}
            onClick={() => update({ currentTurnOnly: !filters.currentTurnOnly, turnId: filters.currentTurnOnly ? filters.turnId : "" })}
          >
            Current turn
          </Button>
          {effectiveTurnId ? (
            <Tooltip title={effectiveTurnId}>
              <Tag color="blue">{shortTurnId(effectiveTurnId)}</Tag>
            </Tooltip>
          ) : null}
        </Space>
        <Button size="small" disabled={loading || defaultActive} onClick={reset}>
          Reset
        </Button>
      </Space>
      <Select
        mode="multiple"
        size="small"
        allowClear
        maxTagCount={1}
        maxTagPlaceholder={(omitted) => `+${omitted.length}`}
        placeholder="Event types"
        popupMatchSelectWidth={false}
        style={{ width: "100%" }}
        value={filters.types}
        disabled={loading}
        onChange={(types) => update({ types })}
        options={timelineEventTypeOptions.map((type) => ({ value: type, label: timelineEventTypeLabel(type) }))}
      />
      <Input.Search
        size="small"
        allowClear
        placeholder="Search label / summary / payload"
        value={filters.q}
        disabled={loading}
        onChange={(event) => update({ q: event.target.value })}
      />
      <Space.Compact style={{ width: "100%" }}>
        <Input
          size="small"
          allowClear
          placeholder="job id"
          value={filters.jobId}
          disabled={loading}
          onChange={(event) => update({ jobId: event.target.value })}
        />
        <Input
          size="small"
          allowClear
          placeholder="turn id"
          value={filters.currentTurnOnly ? currentTurnId : filters.turnId}
          disabled={loading || filters.currentTurnOnly}
          onChange={(event) => update({ turnId: event.target.value })}
        />
      </Space.Compact>
      <Select
        size="small"
        value={filters.limit}
        disabled={loading}
        onChange={(limit) => update({ limit })}
        options={[25, 50, 100, 200].map((value) => ({ value, label: `${value} / page` }))}
      />
    </Space>
  );
}

const timelineEventTypeOptions = [
  "user_message_accepted",
  "assistant_message_completed",
  "tool_call_started",
  "tool_call_finished",
  "warning_raised",
  "error_raised",
  "command_completed",
  "skill_state_changed",
  "history_recall_decision",
  "subagent_job_updated",
  "runner_phase_changed",
  "message_injected",
  "agent_identity_updated",
  "snapshot_ready",
  "turn_completed",
];

function timelineEventTypeLabel(type: string) {
  return `${timelineEventLabel({ type: type as SessionTimelineEntry["type"], timestamp: "" })} · ${type}`;
}

function turnStatusColor(status: string) {
  switch (status) {
    case "completed":
      return "green";
    case "running":
    case "canceling":
      return "processing";
    case "pending_approval":
      return "gold";
    case "canceled":
    case "interrupted":
      return "default";
    case "error":
      return "red";
    default:
      return "default";
  }
}

function formatTurnError(error: string) {
  if (error.toLowerCase().includes("conversation runner reached max turns")) {
    return `${error} · The runner exhausted its turn budget before producing a final answer. Check Timeline for the last phase/tool and consider narrowing the task or raising max_turns.`;
  }
  return error;
}

function shortTurnId(id: string) {
  return id.length <= 10 ? id : `${id.slice(0, 10)}…`;
}

function appendTimelineEvent(current: SessionTimelineEntry[], event: RuntimeEvent) {
  if (event.type === "assistant_text_delta") {
    return current;
  }
  const next = [...current, event];
  return next.length <= 80 ? next : next.slice(next.length - 80);
}

function timelineEventLabel(event: SessionTimelineEntry) {
  switch (event.type) {
    case "user_message_accepted":
      return "User message";
    case "assistant_message_completed":
      return "Assistant reply";
    case "tool_call_started":
      return "Tool started";
    case "tool_call_finished":
      return "Tool finished";
    case "warning_raised":
      return "Warning";
    case "error_raised":
      return "Error";
    case "command_completed":
      return "Command";
    case "skill_state_changed":
      return "Skill";
    case "history_recall_decision":
      return "History recall";
    case "subagent_job_updated":
      return "Subagent";
    case "runner_phase_changed":
      return "Runner phase";
    case "message_injected":
      return String(((event.payload ?? {}) as Record<string, unknown>).mode ?? "") === "steering" ? "Steer" : "Follow-up";
    case "agent_identity_updated":
      return "Agent identity";
    case "snapshot_ready":
      return "Snapshot refreshed";
    case "turn_completed":
      return "Turn completed";
    default:
      return event.type;
  }
}

function timelineEventSummary(event: SessionTimelineEntry) {
  const payload = (event.payload ?? {}) as Record<string, unknown>;
  switch (event.type) {
    case "user_message_accepted":
      return withAppObjectSummary(payload, previewText(String(payload.text ?? "")) || attachmentTimelineSummary(payload.attachments));
    case "assistant_message_completed":
      return previewText(String(payload.text ?? "")) || "Assistant message completed.";
    case "tool_call_started":
      return String(payload.name ?? "tool");
    case "tool_call_finished":
      return payload.error ? `${String(payload.name ?? "tool")} failed: ${String(payload.error)}` : `${String(payload.name ?? "tool")} completed`;
    case "warning_raised":
    case "error_raised":
      return String(payload.message ?? "");
    case "command_completed":
      return payload.error ? `${String(payload.name ?? "command")} failed` : `${String(payload.name ?? "command")} completed`;
    case "subagent_job_updated":
      return subagentTimelineSummary(payload);
    case "runner_phase_changed":
      return [
        payload.actor_kind,
        payload.display_title || payload.objective || payload.actor_id,
        payload.phase,
        runnerIterationLabel(payload),
        payload.tool_name || payload.message,
        payload.recovery_hint,
      ].filter(Boolean).join(" · ");
    case "message_injected":
      return `${String(payload.mode ?? "follow_up")} · ${String(payload.count ?? 0)} injected${payload.remaining ? `, ${String(payload.remaining)} pending` : ""}${payload.summary ? `: ${previewText(String(payload.summary))}` : ""}`;
    case "agent_identity_updated":
      return [payload.name, payload.kind, payload.role].filter(Boolean).join(" · ");
    case "snapshot_ready":
      if (payload.compacted) {
        const before = Number(payload.token_estimate_before ?? 0);
        const after = Number(payload.token_estimate_after ?? 0);
        const reasons = Array.isArray(payload.compression_reasons) ? payload.compression_reasons.join(", ") : "";
        return [
          "Auto compacted context",
          before > 0 || after > 0 ? `${before} → ${after} tokens` : "",
          reasons,
        ].filter(Boolean).join(" · ");
      }
      return "Snapshot refreshed.";
    case "turn_completed":
      return `Status: ${String(payload.status ?? "completed")}`;
    default:
      return "";
  }
}

function timelineEventFullText(event: SessionTimelineEntry, summary: string) {
  const payload = (event.payload ?? {}) as Record<string, unknown>;
  const focused = [
    summary,
    stringFromPayload(payload.message),
    stringFromPayload(payload.error),
    stringFromPayload(payload.result),
    stringFromPayload(payload.text),
    stringFromPayload(payload.summary),
  ].filter(Boolean);
  if (focused.length > 0) {
    return Array.from(new Set(focused)).join("\n");
  }
  try {
    return JSON.stringify(event.payload ?? {}, null, 2);
  } catch {
    return summary;
  }
}

function withAppObjectSummary(payload: Record<string, unknown>, summary: string) {
  const metadata = payload.metadata as Record<string, unknown> | undefined;
  if (!metadata || metadata.app_object_type !== "note") {
    return summary;
  }
  const title = String(metadata.app_object_title || metadata.app_object_id || "").trim();
  if (!title) {
    return summary;
  }
  return summary ? `${summary} · note: ${title}` : `note: ${title}`;
}

function subagentTimelineSummary(payload: Record<string, unknown>) {
  const jobID = String(payload.job_id ?? "subagent");
  const phase = String(payload.phase ?? "updated");
  const status = String(payload.status ?? "");
  const message = previewText(String(payload.message ?? payload.error ?? payload.result ?? ""));
  const title = stringFromPayload(payload.display_title);
  const objective = stringFromPayload(payload.objective);
  const identityID = stringFromPayload(payload.identity_id);
  const roleID = stringFromPayload(payload.role_id);
  const roleName = stringFromPayload(payload.role_name);
  const agentType = stringFromPayload(payload.agent_type);
  const capabilityCount = stringArrayFromPayload(payload.capability_summary)?.length ?? 0;
  const maxTurns = numberFromPayload(payload.max_turns);
  const calls = numberFromPayload(payload.model_request_count);
  const tools = numberFromPayload(payload.tool_call_count);
  const lastIteration = numberFromPayload(payload.last_iteration);
  const lastRecoveryHint = stringFromPayload(payload.last_recovery_hint);
  return `${title || shortTurnId(jobID)}${!title && identityID ? ` · ${shortTurnId(identityID)}` : ""}${!title && (roleName || roleID || agentType) ? ` · ${roleName || roleID || agentType}` : ""} ${phase}${status ? ` (${status})` : ""}${objective && !title ? ` · ${previewText(objective)}` : ""}${lastIteration && maxTurns ? ` · ${lastIteration}/${maxTurns}` : maxTurns ? ` · max ${maxTurns}` : ""}${calls ? ` · ${calls} calls` : ""}${tools ? ` · ${tools} tools` : ""}${capabilityCount ? ` · ${capabilityCount} caps` : ""}${lastRecoveryHint ? ` · ${previewText(lastRecoveryHint)}` : ""}${message ? `: ${message}` : ""}`;
}

function runnerIterationLabel(payload: Record<string, unknown>) {
  const iteration = numberFromPayload(payload.iteration);
  const maxTurns = numberFromPayload(payload.max_turns);
  if (iteration && maxTurns) {
    return `${iteration}/${maxTurns}`;
  }
  if (iteration) {
    return `turn ${iteration}`;
  }
  if (maxTurns) {
    return `max ${maxTurns}`;
  }
  return "";
}

function buildContextStatusSummary(
  inspector: SessionContextInspector | null,
  timelineItems: SessionTimelineEntry[],
  subagentJobs: FeedItem[],
): ContextStatusSummary {
  const context = inspector?.context;
  const breakdown = context?.token_breakdown;
  const tokens = context?.total_token_estimate ?? context?.token_estimate ?? breakdown?.total ?? 0;
  const threshold = context?.compress_threshold ?? 0;
  const percent = threshold > 0 ? Math.min(100, Math.round((tokens / threshold) * 100)) : 0;
  const mainCalls = timelineItems.filter((event) => {
    if (event.type !== "runner_phase_changed") {
      return false;
    }
    const payload = (event.payload ?? {}) as Record<string, unknown>;
    return stringFromPayload(payload.phase) === "model_request" && stringFromPayload(payload.actor_kind) !== "subagent";
  }).length;
  const subagentCalls = subagentJobs.reduce((sum, item) => sum + (item.modelRequestCount ?? 0), 0);
  const calls = mainCalls + subagentCalls;
  const messages = context?.message_count ?? 0;
  const tokenLabel = threshold > 0 ? `${formatCompactNumber(tokens)}/${formatCompactNumber(threshold)} ${percent}%` : formatCompactNumber(tokens);
  return {
    text: `ctx ${tokenLabel} · calls ${calls} · msgs ${messages}`,
    tooltip: [
      `Context tokens: ${tokens}${threshold > 0 ? ` / ${threshold} (${percent}%)` : ""}`,
      `Model requests seen in current timeline window: ${calls}`,
      `Messages in context: ${messages}`,
      context?.suggest_compact ? "Compaction is suggested." : "Compaction is not currently suggested.",
    ].join("\n"),
    budgetPercent: percent,
    suggestCompact: Boolean(context?.suggest_compact),
  };
}

function collectSubagentJobs(items: SessionTimelineEntry[]): FeedItem[] {
  const jobs = new Map<string, FeedItem>();
  for (const event of items) {
    if (event.type !== "subagent_job_updated") {
      continue;
    }
    const payload = (event.payload ?? {}) as Record<string, unknown>;
    const jobID = stringFromPayload(payload.job_id) || "subagent";
    const agentType = stringFromPayload(payload.agent_type) || "Subagent";
    const roleName = stringFromPayload(payload.role_name);
    const phase = stringFromPayload(payload.phase);
    const status = stringFromPayload(payload.status) || phase || "updated";
    const message = stringFromPayload(payload.message);
    const error = stringFromPayload(payload.error);
    const result = stringFromPayload(payload.result);
    const toolName = stringFromPayload(payload.tool_name);
    const detail = message || error || result || toolName || "Subagent job updated.";
    const displayTitle = stringFromPayload(payload.display_title);
    const objective = stringFromPayload(payload.objective);
    const sequence = numberFromPayload(payload.sequence);
    const existing = jobs.get(jobID);
    const progress = appendTimelineSubagentProgress(existing?.progress, {
      timestamp: stringFromPayload(payload.updated_at) || event.timestamp,
      phase,
      status,
      message,
      toolName,
      error,
      result,
      iteration: numberFromPayload(payload.last_iteration),
      maxTurns: numberFromPayload(payload.max_turns),
      recoveryHint: stringFromPayload(payload.last_recovery_hint),
    });
    jobs.set(jobID, {
      id: `subagent-panel:${jobID}`,
      kind: "subagent",
      title: displayTitle || existing?.displayTitle || `${roleName || agentType} ${shortTurnId(jobID)}`,
      body: detail,
      summary: previewText(detail),
      status,
      jobId: jobID,
      parentTurnId: stringFromPayload(payload.parent_turn_id) || event.turn_id || existing?.parentTurnId,
      identityId: stringFromPayload(payload.identity_id) || existing?.identityId,
      agentType,
      roleId: stringFromPayload(payload.role_id) || existing?.roleId,
      roleName: roleName || existing?.roleName,
      packageName: stringFromPayload(payload.package_name) || existing?.packageName,
      sequence: sequence || existing?.sequence,
      objective: objective || existing?.objective,
      displayTitle: displayTitle || existing?.displayTitle,
      toolNames: stringArrayFromPayload(payload.tool_names) || existing?.toolNames,
      capabilitySummary: stringArrayFromPayload(payload.capability_summary) || existing?.capabilitySummary,
      modelHint: stringFromPayload(payload.model_hint) || existing?.modelHint,
      budgetHint: stringFromPayload(payload.budget_hint) || existing?.budgetHint,
      maxTurns: numberFromPayload(payload.max_turns) || existing?.maxTurns,
      modelRequestCount: numberFromPayload(payload.model_request_count) || existing?.modelRequestCount,
      toolCallCount: numberFromPayload(payload.tool_call_count) || existing?.toolCallCount,
      lastRunnerPhase: stringFromPayload(payload.last_runner_phase) || existing?.lastRunnerPhase,
      lastIteration: numberFromPayload(payload.last_iteration) || existing?.lastIteration,
      lastRecoveryHint: stringFromPayload(payload.last_recovery_hint) || existing?.lastRecoveryHint,
      phase,
      error,
      lastToolName: toolName || existing?.lastToolName,
      lastMessage: message || existing?.lastMessage,
      worktreeDir: stringFromPayload(payload.worktree_dir) || existing?.worktreeDir,
      isolation: stringFromPayload(payload.isolation) || existing?.isolation,
      workspaceOrigin: stringFromPayload(payload.workspace_origin) || existing?.workspaceOrigin,
      gitBranch: stringFromPayload(payload.git_branch) || existing?.gitBranch,
      cleanupState: stringFromPayload(payload.cleanup_state) || existing?.cleanupState,
      writeScope: stringArrayFromPayload(payload.write_scope) || existing?.writeScope,
      mergeStatus: stringFromPayload(payload.merge_status) || existing?.mergeStatus,
      progress,
      expanded: true,
    });
  }
  return [...jobs.values()].sort((left, right) => {
    const leftTime = Date.parse(left.progress?.at(-1)?.timestamp ?? "");
    const rightTime = Date.parse(right.progress?.at(-1)?.timestamp ?? "");
    return (Number.isNaN(rightTime) ? 0 : rightTime) - (Number.isNaN(leftTime) ? 0 : leftTime);
  });
}

function subagentJobToFeedItem(job: DurableSubagentJob): FeedItem {
  const progress = (job.progress ?? []).map((entry) => ({
    timestamp: entry.timestamp,
    phase: entry.phase,
    status: job.status,
    message: entry.message,
    toolName: entry.tool_name,
    error: entry.error,
    result: entry.result,
    iteration: entry.iteration,
    maxTurns: entry.max_turns,
    model: entry.model,
    recoveryHint: entry.recovery_hint,
  }));
  const detail = job.error || job.last_message || job.result || job.last_tool_name || "Subagent job updated.";
  const title = job.display_title || `${job.role_name || job.agent_type || "Subagent"} ${shortTurnId(job.job_id)}`;
  return {
    id: `subagent-api:${job.job_id}`,
    kind: "subagent",
    title,
    body: detail,
    summary: previewText(detail),
    status: job.status || job.last_phase || "updated",
    jobId: job.job_id,
    parentTurnId: job.parent_turn_id,
    agentType: job.agent_type,
    roleId: job.role_id,
    roleName: job.role_name,
    identity: job.identity,
    identityId: job.identity_id,
    sequence: job.sequence,
    objective: job.objective,
    displayTitle: job.display_title,
    capabilitySummary: job.identity?.capability_summary,
    modelHint: job.identity?.model_hint,
    budgetHint: job.identity?.budget_hint,
    maxTurns: job.max_turns,
    modelRequestCount: job.model_request_count,
    toolCallCount: job.tool_call_count,
    lastRunnerPhase: job.last_runner_phase,
    lastIteration: job.last_iteration,
    lastRecoveryHint: job.last_recovery_hint,
    packageName: job.package_name,
    prompt: job.prompt,
    phase: job.last_phase,
    lastToolName: job.last_tool_name,
    lastMessage: job.last_message,
    toolNames: job.tool_names,
    createdAt: job.created_at,
    updatedAt: job.updated_at,
    finishedAt: job.finished_at,
    error: job.error,
    worktreeDir: job.worktree_dir,
    isolation: job.isolation,
    workspaceOrigin: job.workspace_origin,
    gitBranch: job.git_branch,
    cleanupState: job.cleanup_state,
    writeScope: job.write_scope,
    mergeStatus: job.merge_status,
    progress,
    expanded: true,
  };
}

function mergeChronologicalFeedItems(historyItems: FeedItem[], overlayItems: FeedItem[]) {
  return [...historyItems, ...overlayItems]
    .map((item, index) => ({ item, index, time: Date.parse(item.timestamp ?? "") }))
    .sort((left, right) => {
      const leftHasTime = !Number.isNaN(left.time);
      const rightHasTime = !Number.isNaN(right.time);
      if (leftHasTime && rightHasTime && left.time !== right.time) {
        return left.time - right.time;
      }
      if (leftHasTime !== rightHasTime) {
        return left.index - right.index;
      }
      return left.index - right.index;
    })
    .map((entry) => entry.item);
}

function mergeSubagentItems(apiItems: FeedItem[], timelineItems: FeedItem[]) {
  const byJob = new Map<string, FeedItem>();
  for (const item of apiItems) {
    byJob.set(item.jobId || item.id, item);
  }
  for (const item of timelineItems) {
    const key = item.jobId || item.id;
    const existing = byJob.get(key);
    if (!existing) {
      byJob.set(key, item);
      continue;
    }
    byJob.set(key, {
      ...existing,
      ...item,
      id: existing.id,
      prompt: existing.prompt || item.prompt,
      createdAt: existing.createdAt || item.createdAt,
      updatedAt: item.updatedAt || existing.updatedAt,
      finishedAt: item.finishedAt || existing.finishedAt,
      parentTurnId: item.parentTurnId || existing.parentTurnId,
      identity: existing.identity || item.identity,
      identityId: existing.identityId || item.identityId,
      sequence: existing.sequence || item.sequence,
      objective: existing.objective || item.objective,
      displayTitle: existing.displayTitle || item.displayTitle,
      title: existing.displayTitle || item.displayTitle || existing.title || item.title,
      toolNames: existing.toolNames || item.toolNames,
      capabilitySummary: existing.capabilitySummary || item.capabilitySummary,
      modelHint: existing.modelHint || item.modelHint,
      budgetHint: existing.budgetHint || item.budgetHint,
      modelRequestCount: item.modelRequestCount || existing.modelRequestCount,
      toolCallCount: item.toolCallCount || existing.toolCallCount,
      lastRunnerPhase: item.lastRunnerPhase || existing.lastRunnerPhase,
      lastIteration: item.lastIteration || existing.lastIteration,
      lastRecoveryHint: item.lastRecoveryHint || existing.lastRecoveryHint,
      progress: mergeSubagentProgress(existing.progress, item.progress),
      expanded: existing.expanded ?? item.expanded,
    });
  }
  return [...byJob.values()].sort((left, right) => {
    const statusDelta = subagentStatusSortRank(left.status) - subagentStatusSortRank(right.status);
    if (statusDelta !== 0) {
      return statusDelta;
    }
    if (left.sequence && right.sequence && left.sequence !== right.sequence) {
      return left.sequence - right.sequence;
    }
    const leftTime = Date.parse(left.progress?.at(-1)?.timestamp ?? left.updatedAt ?? "");
    const rightTime = Date.parse(right.progress?.at(-1)?.timestamp ?? right.updatedAt ?? "");
    return (Number.isNaN(rightTime) ? 0 : rightTime) - (Number.isNaN(leftTime) ? 0 : leftTime);
  });
}

function subagentStatusSortRank(status?: string) {
  switch ((status || "").toLowerCase()) {
    case "running":
      return 0;
    case "pending":
    case "pending_approval":
      return 1;
    case "interrupted":
    case "error":
    case "failed":
    case "timeout":
      return 2;
    case "completed":
      return 3;
    case "canceled":
      return 4;
    default:
      return 5;
  }
}

function mergeSubagentProgress(left: SubagentProgressItem[] | undefined, right: SubagentProgressItem[] | undefined) {
  const entries: SubagentProgressItem[] = [];
  for (const entry of [...(left ?? []), ...(right ?? [])]) {
    if (!entries.some((existing) => subagentProgressKey(existing) === subagentProgressKey(entry))) {
      entries.push(entry);
    }
  }
  return entries.slice(-30);
}

function appendTimelineSubagentProgress(progress: SubagentProgressItem[] | undefined, next: SubagentProgressItem) {
  const key = subagentProgressKey(next);
  const entries = [...(progress ?? [])];
  if (!entries.some((entry) => subagentProgressKey(entry) === key)) {
    entries.push(next);
  }
  return entries.slice(-20);
}

function subagentProgressKey(item: SubagentProgressItem) {
  return [item.timestamp, item.phase, item.status, item.toolName, item.message, item.error, item.result, item.iteration, item.recoveryHint].filter(Boolean).join("|");
}

function stringFromPayload(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

function numberFromPayload(value: unknown) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  if (typeof value === "string" && value.trim() !== "") {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : 0;
  }
  return 0;
}

function stringArrayFromPayload(value: unknown) {
  if (!Array.isArray(value)) {
    return undefined;
  }
  return value.map((item) => (typeof item === "string" ? item.trim() : "")).filter(Boolean);
}

function attachmentTimelineSummary(value: unknown) {
  if (!Array.isArray(value) || value.length === 0) {
    return "";
  }
  return `${value.length} attachment${value.length === 1 ? "" : "s"} uploaded`;
}

function previewText(value: string, maxLength = 96) {
  const normalized = value.trim().replace(/\s+/g, " ");
  return normalized.length <= maxLength ? normalized : `${normalized.slice(0, Math.max(0, maxLength - 3))}...`;
}

function formatCompactNumber(value: number) {
  if (!Number.isFinite(value)) {
    return "0";
  }
  if (Math.abs(value) >= 1_000_000) {
    return `${(value / 1_000_000).toFixed(1).replace(/\.0$/, "")}m`;
  }
  if (Math.abs(value) >= 1_000) {
    return `${(value / 1_000).toFixed(1).replace(/\.0$/, "")}k`;
  }
  return String(Math.round(value));
}

function formatTimelineTime(value?: string) {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function permissionRequestTitle(item: PendingPermission) {
  const tool = item.request.tool_name || "tool";
  const action = item.request.action?.trim();
  return action ? `${tool} · ${action}` : tool;
}

function permissionRequestSummary(item: PendingPermission) {
  const parts: string[] = [];
  if (item.request.command) {
    parts.push(`Command: ${item.request.command}`);
  }
  if (item.request.paths?.length) {
    parts.push(`Paths: ${item.request.paths.join(", ")}`);
  }
  if (item.request.source) {
    parts.push(`Source: ${item.request.source}`);
  }
  return parts.length === 0 ? "Awaiting approval for this tool call." : parts.join(" ");
}
