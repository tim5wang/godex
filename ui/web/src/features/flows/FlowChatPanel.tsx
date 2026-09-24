import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { App as AntApp, Button, Empty, Space, Spin, Typography } from "antd";
import { MessageOutlined } from "@ant-design/icons";
import { useNavigate } from "react-router-dom";
import { useSettingsStore } from "../../store/settings";
import { createChatStore, groupFeedItemsIntoTurns } from "../../store/chat";
import { openSession, getSnapshot, submitMessage } from "../../lib/api";
import { streamEvents } from "../../lib/sse";
import {
  mergeChronologicalFeedItems,
  pendingSendToFeedItem,
  pendingSendsForFeed,
} from "../../lib/timelineUtils";
import { MessageFeedV2 } from "../../components/MessageFeedV2";
import { Composer, type ComposerSubmission } from "../../components/Composer";
import { buildChatRoute } from "../../lib/chatRoutes";
import { useI18n } from "../../i18n";

const { Text } = Typography;

const FLOW_DESIGNER_TEMPLATE = "flow-designer";

/**
 * FlowChatPanel — the natural-language flow designer as a REAL chat session.
 *
 * Instead of a one-shot LLM call (which can time out and whose messages die
 * with the drawer), this panel opens a persistent session per flow
 * (locator key `flow:<flow_id>`, agent template flow-designer) and drives it
 * through the same machinery as the chat page: SSE streaming, snapshot
 * polling, MessageFeedV2 rendering and the Composer. The conversation lives
 * in the session store, so leaving the panel never loses it, and the Agent
 * (with flow_design / flow_inspect / create_flow) designs, validates and
 * persists new flow versions itself.
 */
export function FlowChatPanel(props: {
  flowId: string;
  token: string | null;
  /** Called after a turn ends (the agent may have saved a new version). */
  onVersionApplied?: () => void;
}) {
  const { flowId, token, onVersionApplied } = props;
  const { t } = useI18n();
  const { message } = AntApp.useApp();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  // Independent chat-store instance: the flow designer panel must not share
  // the global chat singleton (which the Chat page also uses), or the two
  // conversations would overwrite each other's feed state.
  const [store] = useState(() => createChatStore());
  const chat = store(); // subscribe: returns the current state snapshot
  const tokenFromStore = useSettingsStore((state) => state.token);
  const effectiveToken = token ?? tokenFromStore;

  const sessionKey = `flow:${flowId}`;
  const locator = {
    channel: "web",
    key: sessionKey,
    metadata: { template: FLOW_DESIGNER_TEMPLATE },
  };
  const [sessionId, setSessionId] = useState("");

  // Open (or resume) the flow's designer session once per flow.
  useEffect(() => {
    let cancelled = false;
    if (!effectiveToken) return;
    setSessionId("");
    openSession(effectiveToken, locator)
      .then((r) => {
        if (cancelled) return;
        setSessionId(r.session_id);
        store.getState().setSession(r.session_id, sessionKey);
      })
      .catch((err) => {
        if (cancelled) return;
        message.error(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [flowId, effectiveToken]);

  const snapshotQuery = useQuery({
    queryKey: ["flow-chat-snapshot", effectiveToken, sessionId],
    queryFn: () => getSnapshot(effectiveToken, sessionId),
    enabled: !!effectiveToken && !!sessionId,
    refetchInterval: (q) => (q.state.data?.running ? 2000 : false),
  });

  // Snapshot → feed history (same normalization the chat page uses).
  useEffect(() => {
    const snap = snapshotQuery.data;
    if (!snap || !sessionId) return;
    store
      .getState()
      .syncSnapshot(
        snap.display_messages ?? snap.messages ?? [],
        snap.running,
        snap.active_turn_id,
      );
  }, [snapshotQuery.data, sessionId, store]);

  // SSE live stream: text deltas / tool calls render live; snapshot_ready
  // refetches the authoritative state.
  useEffect(() => {
    if (!effectiveToken || !sessionId) return;
    const controller = new AbortController();
    const connect = async () => {
      try {
        await streamEvents(sessionId, effectiveToken, controller.signal, (event) => {
          store.getState().handleEvent(event);
          if (event.type === "snapshot_ready") {
            void queryClient.invalidateQueries({ queryKey: ["flow-chat-snapshot", effectiveToken, sessionId] });
          }
          if (event.type === "user_message_accepted" && event.turn_id) {
            store.getState().setRunningTurn(event.turn_id);
          }
          if (event.type === "turn_completed") {
            onVersionApplied?.();
          }
        });
      } catch {
        // Stream dropped (timeout/reconnect): snapshot polling keeps the
        // conversation visible; the next send reconnects.
      }
    };
    void connect();
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, effectiveToken, onVersionApplied]);

  const sendMutation = useMutation({
    mutationFn: async ({ text, files }: ComposerSubmission) => {
      if (!sessionId || (!text && files.length === 0)) return;
      const pendingId = `user:${Date.now()}`;
      store.getState().addPendingSend({ id: pendingId, kind: "user", text, sender: "You" });
      try {
        const result = await submitMessage(effectiveToken, sessionId, {
          source: "web",
          sender: "You",
          text,
          content: text,
        });
        if (result.turn_id) store.getState().setRunningTurn(result.turn_id);
      } catch (err) {
        store.getState().removePendingSend(pendingId);
        throw err;
      }
    },
    onError: (err) => {
      message.error(err instanceof Error ? err.message : String(err));
    },
  });

  const v2Items = useMemo(() => {
    const merged = mergeChronologicalFeedItems(chat.historyItems, chat.overlayItems);
    const grouped = groupFeedItemsIntoTurns(merged);
    const pends = pendingSendsForFeed(chat.pendingSends);
    return pends.length > 0 ? [...grouped, ...pends.map(pendingSendToFeedItem)] : grouped;
  }, [chat.historyItems, chat.overlayItems, chat.pendingSends]);

  return (
    <div className="flow-chat-panel">
      <div className="flow-chat-head">
        <Space size={6} align="center">
          <span>🧩</span>
          <Text strong style={{ fontSize: 13 }}>
            {t("flows.flowChatTitle")}
          </Text>
          {chat.running && <Spin size="small" />}
        </Space>
        <Button
          type="text"
          size="small"
          icon={<MessageOutlined />}
          onClick={() => navigate(buildChatRoute(locator))}
        >
          {t("flows.openInChat")}
        </Button>
      </div>
      {!sessionId ? (
        <div className="flow-chat-empty">
          <Empty description={t("flows.flowChatOpening")} />
        </div>
      ) : (
        <>
          <div className="flow-chat-feed">
            <div className="chat-feed-v2-scrollport">
              <div className="chat-feed chat-feed-v2-scroll">
                <MessageFeedV2
                  items={v2Items}
                  onToggleTool={(id) => store.getState().toggleTool(id)}
                  running={chat.running}
                  activeTurnId={chat.currentTurnId || undefined}
                  botName="Flow 设计师"
                  botAvatar="🧩"
                  botColor="#0f766e"
                />
              </div>
            </div>
          </div>
          <div className="flow-chat-composer">
            <Composer onSubmit={(s) => sendMutation.mutateAsync(s)} disabled={!sessionId} />
          </div>
        </>
      )}
    </div>
  );
}
