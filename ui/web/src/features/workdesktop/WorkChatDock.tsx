import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { App as AntApp, Badge, Button, Empty, Space, Spin, Tooltip, Typography } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { createChatStore, groupFeedItemsIntoTurns } from "../../store/chat";
import { getModels, getSessionContextInspector, getSessionContextUsage, getSessionTimeline, getSnapshot, submitMessage } from "../../lib/api";
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

  const snapshotQuery = useQuery({
    queryKey: ["work-chat-snapshot", token, sessionId],
    queryFn: () => getSnapshot(token, sessionId),
    // Gated on the session, not on a token: the desktop's own queries work the
    // same way, and an unauthenticated deployment (auth_required=false) has no
    // token but a perfectly readable session. Requiring one here left the whole
    // dock — feed and status line — permanently empty in that case.
    enabled: !!sessionId,
    refetchInterval: (q) => (q.state.data?.running ? 2000 : false),
  });

  useEffect(() => {
    const snap = snapshotQuery.data;
    if (!snap || !sessionId) return;
    store.getState().syncSnapshot(snap.display_messages ?? snap.messages ?? [], snap.running, snap.active_turn_id);
  }, [snapshotQuery.data, sessionId, store]);

  useEffect(() => {
    if (!sessionId) return;
    const controller = new AbortController();
    const connect = async () => {
      try {
        setStreamConnected(false);
        await streamEvents(sessionId, token, controller.signal, (event) => {
          setStreamConnected(true);
          store.getState().handleEvent(event);
          if (event.type === "snapshot_ready") {
            void queryClient.invalidateQueries({ queryKey: ["work-chat-snapshot", token, sessionId] });
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
  }, [sessionId, token, onTurnEnd, queryClient, store]);

  // Status-line data. The inspector is expensive to build server-side, so it is
  // refreshed on load/invalidate (like Chat); provider counters stay live.
  const inspectorQuery = useQuery({
    queryKey: ["work-chat-inspector", token, sessionId],
    queryFn: () => getSessionContextInspector(token, sessionId),
    enabled: !!sessionId,
    staleTime: 30_000,
  });
  const usageQuery = useQuery({
    queryKey: ["work-chat-usage", token, sessionId],
    queryFn: () => getSessionContextUsage(token, sessionId),
    enabled: !!sessionId,
    refetchInterval: snapshotQuery.data?.running ? 5000 : false,
  });
  const timelineQuery = useQuery({
    queryKey: ["work-chat-timeline", token, sessionId],
    queryFn: () => getSessionTimeline(token, sessionId),
    enabled: !!sessionId,
    refetchInterval: snapshotQuery.data?.running ? 5000 : false,
  });
  const modelsQuery = useQuery({
    queryKey: ["work-chat-models", token, sessionId],
    queryFn: () => getModels(token, sessionId),
    enabled: !!sessionId,
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
        const result = await submitMessage(token, sessionId, { source: "web", sender: "You", text, content: text });
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

  return (
    <div className="wd-chat-dock">
      <div className="wd-chat-head">
        <Space size={6} align="center">
          <span className="wd-chat-id" title={sessionId}>{sessionId ? sessionId.slice(0, 8) : ""}</span>
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
          </Typography.Text>
          <Button type="text" size="small" aria-label={t("workDesktop.refresh")} icon={<ReloadOutlined aria-hidden="true" />} onClick={() => { void snapshotQuery.refetch(); void usageQuery.refetch(); }} />
        </Space>
      </div>
      {snapshotQuery.isLoading ? (
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
      <div className="wd-chat-composer">
        <Composer ref={composerRef} onSubmit={(s) => sendMutation.mutateAsync(s)} />
      </div>
    </div>
  );
}
