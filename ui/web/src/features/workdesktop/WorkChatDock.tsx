import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { App as AntApp, Button, Empty, Space, Spin, Typography } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { createChatStore, groupFeedItemsIntoTurns } from "../../store/chat";
import { getSnapshot, submitMessage } from "../../lib/api";
import { streamEvents } from "../../lib/sse";
import { mergeChronologicalFeedItems, pendingSendToFeedItem, pendingSendsForFeed } from "../../lib/timelineUtils";
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
 */
export function WorkChatDock(props: {
  sessionId: string;
  token: string | null;
  /** Fired when a turn finishes, so the desktop can refresh its overview. */
  onTurnEnd?: () => void;
}) {
  const { sessionId, token, onTurnEnd } = props;
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  // Independent store instance: sharing the global chat singleton would make
  // this dock and the Chat page overwrite each other's feed state.
  const [store] = useState(() => createChatStore());
  const chat = store();
  const composerRef = useRef<ComposerHandle>(null);

  const snapshotQuery = useQuery({
    queryKey: ["work-chat-snapshot", token, sessionId],
    queryFn: () => getSnapshot(token, sessionId),
    enabled: !!token && !!sessionId,
    refetchInterval: (q) => (q.state.data?.running ? 2000 : false),
  });

  useEffect(() => {
    const snap = snapshotQuery.data;
    if (!snap || !sessionId) return;
    store.getState().syncSnapshot(snap.display_messages ?? snap.messages ?? [], snap.running, snap.active_turn_id);
  }, [snapshotQuery.data, sessionId, store]);

  useEffect(() => {
    if (!token || !sessionId) return;
    const controller = new AbortController();
    const connect = async () => {
      try {
        await streamEvents(sessionId, token, controller.signal, (event) => {
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
      }
    };
    void connect();
    return () => controller.abort();
  }, [sessionId, token, onTurnEnd, queryClient, store]);

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
          <Typography.Text strong style={{ fontSize: 13 }}>{sessionId ? sessionId.slice(0, 8) : ""}</Typography.Text>
          {chat.running && <Spin size="small" />}
        </Space>
        <Button type="text" size="small" icon={<ReloadOutlined aria-hidden="true" />} onClick={() => void snapshotQuery.refetch()} />
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
