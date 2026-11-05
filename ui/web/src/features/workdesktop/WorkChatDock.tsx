import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { App as AntApp, Badge, Button, Empty, Space, Spin, Tooltip, Typography } from "antd";
import { ReloadOutlined, EditOutlined, EnterOutlined } from "@ant-design/icons";
import { createChatStore, groupFeedItemsIntoTurns } from "../../store/chat";
import { getModels, getSessionContextInspector, getSessionContextUsage, getSessionTimeline, getSnapshot, submitMessage, cancelQueuedTurn, steerQueuedTurn, listSessions, openSession, APIError } from "../../lib/api";
import { streamEvents } from "../../lib/sse";
import { buildContextStatusSummary, collectSubagentJobs, mergeChronologicalFeedItems, pendingSendToFeedItem, pendingSendsForFeed } from "../../lib/timelineUtils";
import { ContextStatusInline } from "../chat/panels/ContextPanels";
import { useI18n } from "../../i18n";
import { MessageFeedV2 } from "../../components/MessageFeedV2";
import { Composer, type ComposerHandle, type ComposerSubmission } from "../../components/Composer";

/**
 * WorkChatDock — the stream's linked conversation embedded IN the work desktop.
 *
 * Opening a chat must not navigate away and drop the working context, so this
 * mounts the same session machinery the Chat page uses (snapshot polling + SSE
 * + MessageFeedV2 + Composer) on the ALREADY-LINKED work session. The chat here
 * is not a separate conversation: it is the session the desktop linked, so the
 * Agent's work_context tool sees the same binding and maintains memory from it.
 *
 * When a stream has no linked session yet, the parent shows an empty state and
 * creates one through the normal link-session path.
 *
 * The dock carries the same status line the Chat page's topbar does (model +
 * context usage + stream state). It reuses those exact pieces
 * (ContextStatusInline + buildContextStatusSummary) rather than a lookalike, so
 * "how full is this conversation's context" reads the same in both places.
 *
 * It also renders the same queued-turn strip the Chat page does, for the same
 * reason: a message sent while a turn is running is queued server-side, and
 * without surfacing `snapshot.queued_turns` the dock silently swallows it.
 *
 * It OPENS the session before reading it, which the Chat page always did and this
 * dock did not. Session reads (snapshot/timeline/context) are served from the
 * resident in-memory session; a persisted session that is not resident answers
 * 404. After a service restart every session is persisted-but-not-resident, so a
 * read-first dock stayed empty until someone opened the conversation on the Chat
 * page (whose POST /sessions hydrates it). Opening here makes the desktop
 * self-sufficient instead of depending on a visit to another page.
 */
export function WorkChatDock(props: {
  sessionId: string;
  token: string | null;
  /** Fired when a turn finishes, so the desktop can refresh its overview. */
  onTurnEnd?: () => void;
}) {
  const { sessionId, token, onTurnEnd } = props;
  const { message } = AntApp.useApp();
  const { t } = useI18n();
  const queryClient = useQueryClient();
  // Independent store instance: sharing the global chat singleton would make
  // this dock and the Chat page overwrite each other's feed state.
  const [store] = useState(() => createChatStore());
  const chat = store();
  const composerRef = useRef<ComposerHandle>(null);
  const [streamConnected, setStreamConnected] = useState(false);

  // Resolve the stored session_id back into its FULL locator and open it, exactly
  // as the Chat page does before reading. A bare session_id cannot be re-hashed
  // into the same session, so the locator must come from the server's own list
  // (the desktop only stores the id). This is what makes the dock work after a
  // restart: without it, every read 404s until Chat opens the session once.
  const openQuery = useQuery({
    queryKey: ["work-chat-open", token, sessionId],
    enabled: !!sessionId,
    staleTime: 30_000,
    queryFn: async () => {
      const sessions = await listSessions(token || null);
      const target = sessions.find((item) => item.session_id === sessionId);
      if (!target) throw new Error("session not found");
      return openSession(token || null, target.locator);
    },
  });
  // Every session read below is gated on the OPENED session, not the stored id,
  // so nothing fires before the session is resident.
  const openSessionId = openQuery.data?.session_id ?? "";

  const snapshotQuery = useQuery({
    queryKey: ["work-chat-snapshot", token, openSessionId],
    queryFn: () => getSnapshot(token, openSessionId),
    // Gated on the session, not on a token: the desktop's own queries work the
    // same way, and an unauthenticated deployment (auth_required=false) has no
    // token but a perfectly readable session. Requiring one here left the whole
    // dock — feed and status line — permanently empty in that case.
    enabled: !!openSessionId,
    refetchInterval: (q) => (q.state.data?.running ? 2000 : false),
  });

  useEffect(() => {
    const snap = snapshotQuery.data;
    if (!snap || !openSessionId) return;
    store.getState().syncSnapshot(snap.display_messages ?? snap.messages ?? [], snap.running, snap.active_turn_id);
  }, [snapshotQuery.data, openSessionId, store]);

  useEffect(() => {
    if (!openSessionId) return;
    const controller = new AbortController();
    const connect = async () => {
      try {
        setStreamConnected(false);
        await streamEvents(openSessionId, token, controller.signal, (event) => {
          setStreamConnected(true);
          store.getState().handleEvent(event);
          if (event.type === "snapshot_ready") {
            void queryClient.invalidateQueries({ queryKey: ["work-chat-snapshot", token, openSessionId] });
          }
          if (event.type === "user_message_accepted" && event.turn_id) {
            store.getState().setRunningTurn(event.turn_id);
          }
          if (event.type === "turn_completed") {
            onTurnEnd?.();
          }
        });
      } catch {
        // A dropped stream is tolerable: snapshot polling keeps the feed
        // visible and the next send reconnects.
        setStreamConnected(false);
      }
    };
    void connect();
    return () => controller.abort();
  }, [openSessionId, token, onTurnEnd, queryClient, store]);

  // Status-line data. The inspector is expensive to build server-side, so it is
  // refreshed on load/invalidate (like Chat); provider counters stay live.
  const inspectorQuery = useQuery({
    queryKey: ["work-chat-inspector", token, openSessionId],
    queryFn: () => getSessionContextInspector(token, openSessionId),
    enabled: !!openSessionId,
    staleTime: 30_000,
  });
  const usageQuery = useQuery({
    queryKey: ["work-chat-usage", token, openSessionId],
    queryFn: () => getSessionContextUsage(token, openSessionId),
    enabled: !!openSessionId,
    refetchInterval: snapshotQuery.data?.running ? 5000 : false,
  });
  const timelineQuery = useQuery({
    queryKey: ["work-chat-timeline", token, openSessionId],
    queryFn: () => getSessionTimeline(token, openSessionId),
    enabled: !!openSessionId,
    refetchInterval: snapshotQuery.data?.running ? 5000 : false,
  });
  const modelsQuery = useQuery({
    queryKey: ["work-chat-models", token, openSessionId],
    queryFn: () => getModels(token, openSessionId),
    enabled: !!openSessionId,
  });
  const contextStatus = useMemo(
    () => buildContextStatusSummary(
      inspectorQuery.data ?? null,
      timelineQuery.data ?? [],
      collectSubagentJobs(timelineQuery.data ?? []),
      usageQuery.data ?? null,
    ),
    [inspectorQuery.data, timelineQuery.data, usageQuery.data],
  );
  const modelLabel = useMemo(() => {
    const view = modelsQuery.data;
    if (!view) return "";
    const profile = view.profiles.find((item) => item.id === (view.session_profile_id || view.default_profile_id));
    return profile ? `${profile.name || profile.id} · ${profile.model}` : "";
  }, [modelsQuery.data]);

  const sendMutation = useMutation({
    mutationFn: async ({ text, files }: ComposerSubmission) => {
      if (!text && files.length === 0) return;
      const pendingId = `user:${Date.now()}`;
      store.getState().addPendingSend({ id: pendingId, kind: "user", text, sender: "You" });
      try {
        const result = await submitMessage(token, openSessionId, { source: "web", sender: "You", text, content: text });
        if (result.turn_id) store.getState().setRunningTurn(result.turn_id);
      } catch (err) {
        store.getState().removePendingSend(pendingId);
        throw err;
      }
    },
    onError: (err) => message.error(err instanceof Error ? err.message : String(err)),
  });

  const v2Items = useMemo(() => {
    const merged = mergeChronologicalFeedItems(chat.historyItems, chat.overlayItems);
    const grouped = groupFeedItemsIntoTurns(merged);
    const pends = pendingSendsForFeed(chat.pendingSends);
    return pends.length > 0 ? [...grouped, ...pends.map(pendingSendToFeedItem)] : grouped;
  }, [chat.historyItems, chat.overlayItems, chat.pendingSends]);

  // Queued turns live on the snapshot (the server owns the queue), not in the
  // local store: a send issued while a turn is running becomes a queued turn
  // rather than a pending bubble, so it is invisible unless it is read here.
  const queuedTurns = snapshotQuery.data?.queued_turns ?? [];
  const invalidateQueued = () => Promise.all([
    queryClient.invalidateQueries({ queryKey: ["work-chat-snapshot", token, openSessionId] }),
    queryClient.invalidateQueries({ queryKey: ["work-chat-timeline", token, openSessionId] }),
  ]);

  // "Edit" pulls the queued text back out and drops it in the composer (the
  // server returns it from the cancel endpoint), so the user edits and resends
  // instead of retyping.
  const cancelQueuedMutation = useMutation({
    mutationFn: (queueId: string) => cancelQueuedTurn(token, openSessionId, queueId),
    onSuccess: async (result) => {
      await invalidateQueued();
      if (result.text) {
        composerRef.current?.setText(result.text);
        message.success(t("chat.editQueued"));
      } else {
        message.success(t("chat.cancelQueued"));
      }
    },
    onError: (error) => { message.error(error instanceof APIError ? error.message : String(error)); },
  });

  const steerQueuedMutation = useMutation({
    mutationFn: (queueId: string) => steerQueuedTurn(token, openSessionId, queueId),
    onSuccess: async () => {
      await invalidateQueued();
      message.success(t("chat.steerQueued"));
    },
    onError: (error) => { message.error(error instanceof APIError ? error.message : String(error)); },
  });

  return (
    <div className="wd-chat-dock">
      <div className="wd-chat-head">
        <Space size={6} align="center">
          <span className="wd-chat-id" title={openSessionId}>{openSessionId ? openSessionId.slice(0, 8) : ""}</span>
          {chat.running && <Spin size="small" />}
          {modelLabel && <Typography.Text type="secondary" className="wd-chat-model" title={modelLabel}>{modelLabel}</Typography.Text>}
        </Space>
        <Space size={6} align="center">
          <ContextStatusInline summary={contextStatus} inspector={inspectorQuery.data ?? null} usage={usageQuery.data ?? null} />
          <Tooltip title={streamConnected ? t("chat.streamConnected") : t("chat.streamReconnecting")}>
            <Badge status={streamConnected ? "success" : "processing"} />
          </Tooltip>
          <Typography.Text type="secondary" className="wd-chat-status">
            {chat.running ? t("chat.running") : t("chat.idle")}
            {queuedTurns.length > 0 ? ` · ${queuedTurns.length} ${t("chat.queuedMessage")}` : ""}
          </Typography.Text>
          <Button type="text" size="small" aria-label={t("workDesktop.refresh")} icon={<ReloadOutlined aria-hidden="true" />} onClick={() => { void snapshotQuery.refetch(); void usageQuery.refetch(); }} />
        </Space>
      </div>
      {openQuery.isError ? (
        // Opening failed (session gone, or the locator is no longer listed). Say so
        // and offer a retry instead of showing an empty conversation as if the
        // history were empty.
        <div className="wd-chat-empty">
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("workDesktop.chatOpenFailed")}>
            <Button size="small" onClick={() => void openQuery.refetch()}>{t("workDesktop.retry")}</Button>
          </Empty>
        </div>
      ) : openQuery.isPending || snapshotQuery.isLoading ? (
        <div className="wd-chat-empty"><Spin /></div>
      ) : v2Items.length === 0 ? (
        <div className="wd-chat-empty"><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /></div>
      ) : (
        <div className="wd-chat-feed">
          <div className="chat-feed-v2-scrollport">
            <div className="chat-feed chat-feed-v2-scroll">
              <MessageFeedV2
                items={v2Items}
                onToggleTool={(id) => store.getState().toggleTool(id)}
                running={chat.running}
                activeTurnId={chat.currentTurnId || undefined}
                token={token}
              />
            </div>
          </div>
        </div>
      )}
      {/* Queued strip reuses the Chat page's markup and CSS so "what is waiting"
          reads identically in both surfaces; the JSON keys (chat.queuedMessage
          etc.) are shared, not duplicated. Rendered above the composer, which is
          where a just-sent message would otherwise silently vanish. */}
      {queuedTurns.length > 0 ? (
        <div className="chat-queued-strip">
          {queuedTurns.map((queued) => (
            <div className="chat-queued-item" key={queued.id}>
              <Typography.Text ellipsis style={{ flex: 1, minWidth: 0 }}>
                {queued.summary || t("chat.queuedMessage")}
              </Typography.Text>
              <Tooltip title={t("chat.editQueued")}>
                <Button
                  type="text"
                  size="small"
                  icon={<EditOutlined />}
                  aria-label={t("chat.editQueued")}
                  disabled={cancelQueuedMutation.isPending || steerQueuedMutation.isPending}
                  onClick={() => cancelQueuedMutation.mutate(queued.id)}
                />
              </Tooltip>
              <Tooltip title={t("chat.steerQueued")}>
                <Button
                  type="text"
                  size="small"
                  icon={<EnterOutlined />}
                  aria-label={t("chat.steerQueued")}
                  disabled={cancelQueuedMutation.isPending || steerQueuedMutation.isPending}
                  onClick={() => steerQueuedMutation.mutate(queued.id)}
                />
              </Tooltip>
            </div>
          ))}
        </div>
      ) : null}
      <div className="wd-chat-composer">
        <Composer ref={composerRef} onSubmit={(s) => sendMutation.mutateAsync(s)} />
      </div>
    </div>
  );
}
