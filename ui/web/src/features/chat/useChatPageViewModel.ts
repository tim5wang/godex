import { composeTranscriptArchives, groupFeedItemsIntoTurns, overlappingSnapshotMessageIndexes, snapshotToItems, transcriptArchiveRefs } from "../../store/chat";
import { useState, useRef, useEffect, useLayoutEffect, useCallback, useMemo } from "react";
import type { FeedItem, ProtocolMessage } from "../../lib/types";
import { buildReviewMergeSummary } from "./reviewMergeCenter";
import { getSessionTranscript, APIError } from "../../lib/api";
import { isLongTaskRefluxMessage } from "./LongTaskRefluxBubble";
import { buildTaskOutcomes } from "./taskCenterOutcome";
import { locatorMatchesRoute } from "../../lib/chatRoutes";
import { writeClipboardText } from "../../lib/clipboard";
import { mergeChronologicalFeedItems, pendingSendToFeedItem, pendingSendsForFeed, mergeSubagentItems, subagentJobToFeedItem, collectSubagentJobs, collectToolCalls, collectThinkingDeltas, collectTextDeltas, buildContextStatusSummary, alignAssistantTextTurnIds } from "../../lib/timelineUtils";
import { compactWorkspaceName } from "./panels/NoteContextBanner";
import type { useChatLayoutState } from "./useChatLayoutState";
import type { useChatSessionState } from "./useChatSessionState";
export function useChatPageViewModel(
  layout: ReturnType<typeof useChatLayoutState>,
  session: ReturnType<typeof useChatSessionState>,
) {
  const { channelFilter, currentTurnId, historyItems, message, overlayItems, pendingModelProfileID, pendingReasoningEffort, pendingSends, queryClient, refluxDismissed, routeChannel, running, screens, scrollerRef, sessionId, status, subagentReview, t, timelineItems, toggleTool, token } = layout;
  const { compactionsQuery, contextInspectorQuery, contextUsageQuery, longTasksQuery, metaQuery, modelsQuery, openQuery, routeUserId, sessionKey, sessionsQuery, snapshotQuery, subagentsQuery } = session;
const activeHistorySessionID = openQuery.data?.session_id ?? "";
  const [historyArchiveState, setHistoryArchiveState] = useState<{
    sessionId: string;
    pages: Record<string, ProtocolMessage[]>;
  }>({ sessionId: "", pages: {} });
  const [historyArchiveLoading, setHistoryArchiveLoading] = useState<{ sessionId: string; ref: string } | null>(null);
  const [expandedHistoryArchiveTools, setExpandedHistoryArchiveTools] = useState<Record<string, boolean>>({});
  const historyArchiveRequests = useRef(new Set<string>());
  const historyScrollAnchorRef = useRef<{ sessionId: string; scrollTop: number; scrollHeight: number } | null>(null);
  const activeHistorySessionRef = useRef(activeHistorySessionID);
  activeHistorySessionRef.current = activeHistorySessionID;
  const loadedHistoryArchives = historyArchiveState.sessionId === activeHistorySessionID ? historyArchiveState.pages : {};
  const currentSnapshotMessages = snapshotQuery.data?.display_messages ?? snapshotQuery.data?.messages ?? [];
  // The compactions endpoint returns newest-first. Load the newest archive
  // first (right before the current snapshot), then follow old summary
  // references as each archive is loaded.
  const transcriptRefs = useMemo(
    () => {
      const snapshotRefs = currentSnapshotMessages
        .filter((item) => item.metadata?.kind === "summary")
        .map((item) => item.metadata?.transcript?.trim() ?? "");
      const compactionRefs = (compactionsQuery.data ?? []).map((record) => record.transcript_ref?.trim() ?? "");
      return transcriptArchiveRefs([...snapshotRefs, ...compactionRefs], loadedHistoryArchives);
    },
    [compactionsQuery.data, currentSnapshotMessages, loadedHistoryArchives],
  );
  const canLoadEarlierHistory = transcriptRefs.some((ref) => !loadedHistoryArchives[ref]);
  const isLoadingEarlierHistory = historyArchiveLoading?.sessionId === activeHistorySessionID;
  useEffect(() => {
    setHistoryArchiveState({ sessionId: activeHistorySessionID, pages: {} });
    setHistoryArchiveLoading(null);
    setExpandedHistoryArchiveTools({});
    historyScrollAnchorRef.current = null;
  }, [activeHistorySessionID]);

  const loadEarlierHistory = useCallback(async () => {
    if (!activeHistorySessionID) return;
    const ref = transcriptRefs.find((candidate) => !loadedHistoryArchives[candidate]);
    if (!ref) return;
    const requestKey = `${activeHistorySessionID}\0${ref}`;
    if (historyArchiveRequests.current.has(requestKey)) return;
    historyArchiveRequests.current.add(requestKey);
    setHistoryArchiveLoading({ sessionId: activeHistorySessionID, ref });
    try {
      const transcript = await queryClient.fetchQuery({
        queryKey: ["session-transcript", token, activeHistorySessionID, ref],
        queryFn: () => getSessionTranscript(token || null, activeHistorySessionID, ref),
        staleTime: 5 * 60 * 1000,
      });
      if (activeHistorySessionRef.current === activeHistorySessionID) {
        const scroller = scrollerRef.current;
        historyScrollAnchorRef.current = scroller
          ? { sessionId: activeHistorySessionID, scrollTop: scroller.scrollTop, scrollHeight: scroller.scrollHeight }
          : null;
        setHistoryArchiveState((current) => ({
          sessionId: activeHistorySessionID,
          pages: {
            ...(current.sessionId === activeHistorySessionID ? current.pages : {}),
            [ref]: transcript.messages,
          },
        }));
      }
    } catch (error) {
      if (activeHistorySessionRef.current === activeHistorySessionID) {
        message.error(error instanceof APIError ? error.message : t("chat.loadEarlierHistoryFailed"));
      }
    } finally {
      historyArchiveRequests.current.delete(requestKey);
      setHistoryArchiveLoading((current) =>
        current?.sessionId === activeHistorySessionID && current.ref === ref ? null : current,
      );
    }
  }, [activeHistorySessionID, loadedHistoryArchives, message, queryClient, t, token, transcriptRefs]);

  const loadedHistoryArchivePages = useMemo(
    () =>
      transcriptRefs
        .filter((ref) => loadedHistoryArchives[ref])
        .reverse()
        .map((ref) => ({ ref, messages: loadedHistoryArchives[ref] })),
    [loadedHistoryArchives, transcriptRefs],
  );
  useLayoutEffect(() => {
    const anchor = historyScrollAnchorRef.current;
    if (!anchor || anchor.sessionId !== activeHistorySessionID) return;
    historyScrollAnchorRef.current = null;
    const scroller = scrollerRef.current;
    if (scroller) {
      scroller.scrollTop = anchor.scrollTop + scroller.scrollHeight - anchor.scrollHeight;
    }
  }, [activeHistorySessionID, loadedHistoryArchivePages.length]);
  const archivedHistoryMessages = useMemo(
    () => composeTranscriptArchives(loadedHistoryArchivePages),
    [loadedHistoryArchivePages],
  );
  const archivedHistoryItems = useMemo(() => {
    const groups: Array<{ ref: string; messages: ProtocolMessage[]; sourceIndexes: number[] }> = [];
    for (const entry of archivedHistoryMessages) {
      let group = groups[groups.length - 1];
      if (!group || group.ref !== entry.ref) {
        group = { ref: entry.ref, messages: [], sourceIndexes: [] };
        groups.push(group);
      }
      group.messages.push(entry.message);
      group.sourceIndexes.push(entry.sourceIndex);
    }
    return groups.flatMap((group) =>
      snapshotToItems(group.messages, expandedHistoryArchiveTools, {
        idPrefix: `archive:${group.ref}:`,
        archiveOnly: true,
        sourceIndexes: group.sourceIndexes,
      }),
    );
  }, [archivedHistoryMessages, expandedHistoryArchiveTools]);
  const duplicateSnapshotMessageIndexes = useMemo(
    () => overlappingSnapshotMessageIndexes(archivedHistoryMessages, currentSnapshotMessages),
    [archivedHistoryMessages, currentSnapshotMessages],
  );


  const items = useMemo(() => {
    const alignedArchiveItems = alignAssistantTextTurnIds(archivedHistoryItems, timelineItems);
    const archivedAssistantText = new Set(
      alignedArchiveItems
        .filter((item) => item.kind === "assistant" && item.body.trim())
        .map((item) => `${item.turnId ?? ""}\0${item.body.trim()}`),
    );
    const archivedToolCallIDs = new Set(
      archivedHistoryMessages.flatMap((entry) =>
        (entry.message.content ?? [])
          .filter((block) => block.type === "tool_use" && block.id)
          .map((block) => block.id!),
      ),
    );
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
    const timelineTools = collectToolCalls(timelineItems).filter((item) => {
      if (overlayById.has(item.id)) return false;
      return !(item.id.startsWith("tool:") && archivedToolCallIDs.has(item.id.slice("tool:".length)));
    });
    // Rebuild reasoning ("Thinking…") segments from the persisted timeline the
    // same way tools are rebuilt: the ACP harness streams assistant_thinking_delta
    // events between tool calls, live shows them as overlay bubbles, and they
    // are now persisted so a re-entry can reconstruct the thinking↔tool
    // alternation. During a live turn the overlay already carries the current
    // turn's thinking bubbles (growing incrementally), so skip any turn the
    // overlay already has thinking for; on re-entry the overlay is empty and
    // every persisted thinking segment is rebuilt.
    const overlayThinkingTurns = new Set<string>();
    for (const item of overlayItems) {
      if (item.kind === "background" && item.title === "Thinking…" && item.turnId) {
        overlayThinkingTurns.add(item.turnId);
      }
    }
    const timelineThinking = collectThinkingDeltas(timelineItems).filter(
      (item) => !(item.turnId && overlayThinkingTurns.has(item.turnId)),
    );
    // Rebuild the SHORT process text the model streams between tool calls
    // ("收到两个问题。先诊断…" before each tool). Same persistence story as
    // thinking: assistant_text_delta events are now recordable, so re-entry
    // (overlay cleared) reconstructs them split at tool boundaries and lets
    // mergeChronologicalFeedItems interleave them with the tool log. During a
    // live turn the overlay already carries the current turn's text segments,
    // so skip any turn the overlay already has assistant text for.
    const overlayTextTurns = new Set<string>();
    for (const item of overlayItems) {
      if (item.kind === "assistant" && item.title === "GoDex" && item.turnId) {
        overlayTextTurns.add(item.turnId);
      }
    }
    const timelineText = collectTextDeltas(timelineItems).filter(
      (item) =>
        !(item.turnId && overlayTextTurns.has(item.turnId)) &&
        !archivedAssistantText.has(`${item.turnId ?? ""}\0${item.body.trim()}`),
    );
    const mergedOverlay = [...overlayItems, ...timelineTools, ...timelineThinking, ...timelineText];
    // Re-bind snapshot assistant text to its real backend turn id (from the
    // timeline's assistant_message_completed) so re-entered ACP turns group
    // their text with the tool log instead of splitting into two big segments.
    const alignedHistory = alignAssistantTextTurnIds(
      historyItems.filter((item) => item.messageIndex === undefined || !duplicateSnapshotMessageIndexes.has(item.messageIndex)),
      timelineItems,
    );
    // For turns that have persisted text deltas, the process text is now
    // rebuilt above (timelineText) split at tool boundaries, so drop the
    // snapshot's consolidated full-text body for that turn to avoid showing
    // the same narration twice (once interleaved, once concatenated). Keep
    // messageIndex / turnId / timestamp so grouping and fork still work.
    const textDeltaTurns = new Set<string>();
    for (const event of timelineItems) {
      if (event.type === "assistant_text_delta" && event.turn_id) {
        textDeltaTurns.add(event.turn_id);
      }
    }
    const deDupedHistory = alignedHistory.map((item) =>
      item.kind === "assistant" && item.turnId && textDeltaTurns.has(item.turnId) ? { ...item, body: "" } : item,
    );
    return [...alignedArchiveItems, ...mergeChronologicalFeedItems(deDupedHistory, mergedOverlay)];
  }, [archivedHistoryItems, archivedHistoryMessages, duplicateSnapshotMessageIndexes, historyItems, overlayItems, timelineItems]);
  const toggleFeedTool = useCallback(
    (id: string) => {
      if (id.startsWith("archive:")) {
        setExpandedHistoryArchiveTools((current) => ({ ...current, [id]: !current[id] }));
        return;
      }
      toggleTool(id);
    },
    [toggleTool],
  );
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
  const contextUsage = contextUsageQuery.data ?? null;
  const contextStatus = useMemo(
    () => buildContextStatusSummary(contextInspector, timelineItems, subagentJobs, contextUsage),
    [contextInspector, contextUsage, subagentJobs, timelineItems],
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
  return {
    items,
    v2Items,
    v2ItemsWithPending,
    canLoadEarlierHistory,
    isLoadingEarlierHistory,
    loadEarlierHistory,
    toggleFeedTool,
    refluxBubbles,
    subagentJobs,
    reviewMergeSummary,
    pendingPermissions,
    turnRecords,
    queuedTurns,
    taskOutcomes,
    contextInspector,
    contextUsage,
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
  };
}