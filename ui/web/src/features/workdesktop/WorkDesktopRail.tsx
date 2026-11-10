import { useEffect, type ReactNode } from "react";
import { App as AntApp, Badge, Button, Empty, Input, Popconfirm, Skeleton, Space, Tag, Tooltip, Typography } from "antd";
import { CommentOutlined, DeleteOutlined, LinkOutlined, PlusOutlined, ReloadOutlined, SearchOutlined, VerticalRightOutlined } from "@ant-design/icons";
import { useWorkDesktopStore } from "./workDesktopStore";
import { useConversationLayoutStore } from "../chat/layout/layoutStore";
import { WorkDesktopEditorModal } from "./WorkDesktopEditorModal";
import { useOpenWorkConversation, useWorkDesktopData } from "./useWorkDesktopData";
import { mostRecentlyUsedConversation, railAggregates, railStatus } from "./workDesktopView";
import { ensurePushSubscriptionSilent } from "../../lib/push";
import { useSettingsStore } from "../../store/settings";
import "./workDesktop.css";

// Fixed palette per status: a status never borrows another status's colour, so the
// rail, the header and the delegation rows cannot disagree about what "in_review" looks like.
const statusColor: Record<string, string> = { active: "processing", paused: "orange", archived: "default" };

// The label and tooltip for each conversation state a card can report. A map rather
// than a template-built key: the two halves must stay in step with railStatus, and a
// typo would otherwise surface as a raw "workDesktop.railXxx" string in the UI.
const railStateCopy: Record<"running" | "finished" | "error", { label: string; hint: string }> = {
  running: { label: "railRunning", hint: "railRunningHint" },
  finished: { label: "railFinished", hint: "railFinishedHint" },
  error: { label: "railError", hint: "railErrorHint" },
};

// Aggregate chips: the label and the tooltip that explains what is counted. Built from maps
// for the same reason as above — a mismatch would render a raw key, and the tooltip is the
// only place that says WHICH work the number stands for.
const AGG_LABEL_KEYS: Record<"attention" | "next", string> = {
  attention: "railAggAttention",
  next: "railAggNext",
};
const AGG_HINT_KEYS: Record<"attention" | "next", string> = {
  attention: "railAggAttentionHint",
  next: "railAggNextHint",
};

/**
 * The work desktop list, shown in the chat LEFT RAIL when the rail is switched to
 * the desktop view.
 *
 * This is the standalone desktop page's left column moved into the chat rail: the
 * chat page already owns the conversation list and the mature middle column, so
 * the desktop contributes its list of long-running work streams here instead of
 * shipping a whole separate page with its own layout and its own mobile workarounds.
 *
 * Opening a linked conversation navigates the middle column's chat route rather
 * than embedding a second chat — there is one chat implementation, and the middle
 * column already is it.
 *
 * It also re-registers the push subscription on mount, which the standalone
 * desktop page used to do. The desktop is where nudges land, so a user with
 * notification permission already granted starts receiving them as soon as they
 * open this view — silently, without ever prompting (see ensurePushSubscriptionSilent).
 */
export function WorkDesktopRail(props: { collapsed: boolean; viewSwitcher: ReactNode; onToggleCollapsed: () => void }) {
  const data = useWorkDesktopData();
  const { message } = AntApp.useApp();
  const openConversation = useOpenWorkConversation();
  const token = useSettingsStore((state) => state.token);
  const search = useWorkDesktopStore((state) => state.search);
  const setSearch = useWorkDesktopStore((state) => state.setSearch);
  const setContextId = useWorkDesktopStore((state) => state.setContextId);
  const setSection = useWorkDesktopStore((state) => state.setSection);
  const revealChanges = useWorkDesktopStore((state) => state.revealChanges);
  const openEditor = useWorkDesktopStore((state) => state.openEditor);
  const activateDockTab = useConversationLayoutStore((state) => state.activateDockTab);
  const {
    tr, trLabel, filteredContexts, contextId, readOnly, invalidContext,
    unreadByContext, agedByContext, sessions, contextsQuery, unlinkSessionMutation,
    sessionsByContext, lastTurnByContext, seenByContext, runningSessionIds, activityBySessionId, attentionByContext,
  } = data;

  // Selecting a desktop is also "take me to where the work is": the centre column
  // follows to that stream's most recently used conversation. Only a click does this —
  // an effect on the selection would yank the open conversation away on first paint and
  // while merely peeking at the desktop list. A stream with no linked conversation
  // leaves the centre column alone rather than emptying it, because there is nothing to
  // show and a blank chat would look like work was lost.
  const selectDesktop = (id: string) => {
    setContextId(id);
    const target = mostRecentlyUsedConversation(sessionsByContext[id] ?? [], activityBySessionId);
    if (target) void openConversation(target.session_id);
  };

  // The editor modal is owned by the rail: the rail is mounted whenever the desktop
  // view is shown, while the dock pane is not (its tab can be switched away or the
  // dock collapsed) — so putting the modal in the dock would silently swallow every
  // editor opened from here. The dock renders a fallback copy only when this rail is
  // not on screen (see WorkDesktopPanel), so exactly one instance exists at a time.
  const modal = <WorkDesktopEditorModal data={data} />;

  // The unread badge is the only place a stream announces that it has moved, so it
  // must LEAD somewhere: it selects that desktop, shows the Overview board and
  // reveals the "changes since last seen" card — which is what the count actually
  // counts. A badge that only printed a number is exactly what made "2"
  // unexplainable from inside the desktop, and it was nested inside the card's own
  // <button> (invalid HTML) so it could not have been clicked anyway.
  const openUnreadChanges = (id: string) => {
    setContextId(id);
    setSection("overview");
    revealChanges();
    // The boards live in the right dock, so the badge must also bring it into view.
    activateDockTab("workdesk");
  };

  // Re-register the push subscription once per mount. It never prompts, so a user
  // who has not granted permission is left alone and one who has does not have to
  // revisit settings after every server restart. Failures are swallowed because
  // the user did not ask for this.
  useEffect(() => {
    void ensurePushSubscriptionSilent(token || null);
  }, [token]);

  if (props.collapsed) {
    return (
      <div className="chat-v2-rail chat-v2-rail-collapsed" data-testid="chat-v2-workdesk-collapsed">
        <Tooltip title={tr("viewDesktop")} placement="right">
          <Button type="text" icon={<VerticalRightOutlined />} aria-label={tr("viewDesktop")} onClick={props.onToggleCollapsed} />
        </Tooltip>
        {/* The view switch is deliberately NOT repeated here: the collapsed rail is
            48px wide and cannot fit a segmented control. Expanding the rail brings
            the switch back, so the desktop view is never a dead end. */}
        {modal}
      </div>
    );
  }

  return (
    <div className="chat-v2-rail wd-rail" data-testid="chat-v2-workdesk">
      <div className="chat-v2-rail-top">
        {props.viewSwitcher}
        <Tooltip title={tr("newContext")}>
          <Button size="small" icon={<PlusOutlined />} aria-label={tr("newContext")} onClick={() => openEditor("create")} />
        </Tooltip>
      </div>
      <Input
        className="chat-v2-search"
        allowClear
        prefix={<SearchOutlined />}
        placeholder={tr("searchContexts")}
        aria-label={tr("searchContexts")}
        value={search}
        onChange={(event) => setSearch(event.target.value)}
      />
      <div className="chat-v2-session-scroll">
        {contextsQuery.isError ? (
          <div className="wd-rail-msg">
            <Typography.Text type="danger">{tr("loadFailed")}</Typography.Text>
            <Button size="small" icon={<ReloadOutlined />} onClick={() => void contextsQuery.refetch()}>{tr("retry")}</Button>
          </div>
        ) : contextsQuery.isLoading ? (
          <div data-testid="chat-v2-workdesk-loading"><Skeleton active paragraph={{ rows: 5 }} title={false} /></div>
        ) : filteredContexts.length === 0 ? (
          <div className="wd-rail-empty">
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={search ? tr("emptyContexts") : tr("noContexts")} />
            {/* The storage boundary stays visible in the empty state, as it was on the
                standalone page: the desktop is a personal SQLite store and does not
                verify production state, which the user should know before trusting a
                blank slate. */}
            {!search && <Typography.Paragraph type="secondary" className="wd-rail-store">{tr("store")} · {tr("storeHint")}</Typography.Paragraph>}
          </div>
        ) : (
          <nav className="wd-rail-list" aria-label={tr("contexts")}>
            {filteredContexts.map((item) => {
              const active = item.id === contextId;
              const unread = unreadByContext[item.id] ?? 0;
              // An aged count is still real, but it is no longer a call to action:
              // it renders as a muted number instead of a red dot. Counting happens
              // on the server (see unreadAgingAfter), so the two views cannot
              // disagree about which streams are stale.
              const aged = agedByContext[item.id] === true;
              // What this stream's CONVERSATIONS are doing, which is the part of a
              // stream's state that used to be invisible from the list: running,
              // finished-since-last-look, or failed. Derived only from server facts.
              const liveness = railStatus({
                status: item.status,
                sessions: sessionsByContext[item.id] ?? [],
                lastTurn: lastTurnByContext[item.id],
                seenAt: seenByContext[item.id],
                runningSessionIds,
              });
              // Only the ACTIVE desktop shows its linked conversations inline; showing
              // every desktop's conversations at once would turn the list into a wall.
              const linked = active ? sessions : [];
              // What is outstanding on this stream, as counts. Zeros are dropped by the
              // helper, so a calm stream shows one quiet line instead of a row of zeroes.
              const aggregates = railAggregates({
                checkpoint: item.checkpoint,
                attentionCount: attentionByContext[item.id] ?? 0,
              });
              // The prose is no longer the card's second line, but it is still the most
              // human-readable statement of where the stream stands, so it stays on the
              // tooltip rather than being thrown away.
              const summaryText = item.checkpoint.summary || item.goal || item.background;
              return (
                <div key={item.id} className="wd-rail-item-wrap">
                  {/* The unread badge sits OUTSIDE the card's own button: a button
                      nested in a button is invalid HTML, which is why it could not be
                      clicked before. Its accessible name says what it does. */}
                  {unread > 0 ? (
                    <Tooltip title={aged
                      ? tr("attentionUnreadAgedHint", { count: unread })
                      : tr("attentionUnreadHint", { count: unread })}>
                      <button
                        type="button"
                        className={`wd-rail-unread${aged ? " wd-rail-unread-aged" : ""}`}
                        aria-label={aged
                          ? tr("attentionUnreadAged", { count: unread })
                          : tr("attentionUnread", { count: unread })}
                        onClick={() => openUnreadChanges(item.id)}
                      >
                        {/* A stale count is shown as plain text, not a Badge: the red
                            dot is the part that demands action, and this no longer does. */}
                        {aged
                          ? <span className="wd-rail-unread-muted">{unread}</span>
                          : <Badge count={unread} size="small" />}
                      </button>
                    </Tooltip>
                  ) : null}
                  <button
                    type="button"
                    className={`wd-rail-item ${active ? "wd-rail-item-selected" : ""}`}
                    aria-current={active ? "true" : undefined}
                    onClick={() => selectDesktop(item.id)}
                  >
                    <span className="wd-rail-item-top">
                      <strong>{item.title}</strong>
                      <Space size={4} align="center">
                        <span className={`wd-status-dot wd-status-${item.status}`} />
                      </Space>
                    </span>
                    {/* Counts instead of the checkpoint's prose. The prose is clamped to two
                        lines, so what reached the eye was an arbitrary PREFIX of a long
                        paragraph — a dozen characters that answered nothing. Counts answer the
                        list's actual question (which stream is waiting on me, and for what) at
                        a width that cannot degrade as someone writes a longer summary. The prose
                        is not lost: it stays reachable on the tooltip and in full on Overview. */}
                    <span className="wd-rail-item-aggs">
                      {aggregates.length ? aggregates.map((agg) => (
                        <Tooltip key={agg.key} title={tr(AGG_HINT_KEYS[agg.key], { count: agg.count })}>
                          <span className={`wd-rail-agg wd-rail-agg-${agg.key}`}>
                            {tr(AGG_LABEL_KEYS[agg.key], { count: agg.count })}
                          </span>
                        </Tooltip>
                      )) : (
                        <Tooltip title={summaryText}>
                          <span className="wd-rail-agg wd-rail-agg-none">{tr("railAggNone")}</span>
                        </Tooltip>
                      )}
                    </span>
                    <span className="wd-rail-item-meta">
                      <Tag color={statusColor[item.status] ?? "default"}>{trLabel("", item.status)}</Tag>
                      {/* The live conversation state REPLACES the bare checkpoint revision
                          in the prominent slot: "checkpoint r7" says when the document was
                          written, while "conversation finished" says what is waiting for
                          the user — and the revision is still there right after it. */}
                      {liveness.kind !== "idle" ? (
                        <Tooltip title={tr(railStateCopy[liveness.kind].hint)}>
                          <span className={`wd-rail-state wd-rail-state-${liveness.kind}`} data-testid={`wd-rail-state-${liveness.kind}`}>
                            <span className="wd-rail-state-dot" />
                            {tr(railStateCopy[liveness.kind].label)}
                          </span>
                        </Tooltip>
                      ) : null}
                      <span className="wd-muted">{tr("revision", { revision: item.checkpoint.revision })}</span>
                    </span>
                  </button>
                  {active ? (
                    <div className="wd-rail-linked" data-testid="chat-v2-workdesk-linked">
                      {linked.length ? linked.map((link) => (
                        <div key={link.id} className="wd-rail-linked-row">
                          <Tooltip title={link.session_id}>
                            <button
                              type="button"
                              className="wd-rail-linked-open"
                              aria-label={`${tr("openConversation")}: ${link.topic || tr("topicName")}`}
                              onClick={() => void openConversation(link.session_id)}
                            >
                              <CommentOutlined aria-hidden="true" />
                              <span className="wd-rail-linked-label">{link.topic || tr("topicName")}</span>
                            </button>
                          </Tooltip>
                          {!readOnly && (
                            <Popconfirm title={tr("unlinkConfirm")} onConfirm={() => unlinkSessionMutation.mutate(link.session_id)}>
                              <Button type="text" size="small" danger icon={<DeleteOutlined />} aria-label={tr("unlinkSession")} />
                            </Popconfirm>
                          )}
                        </div>
                      )) : <Typography.Text type="secondary" className="wd-rail-linked-empty">{tr("noSessions")}</Typography.Text>}
                      {!readOnly && (
                        <Space size={2} className="wd-rail-linked-actions">
                          <Button size="small" type="link" icon={<LinkOutlined />} onClick={() => openEditor("session")}>{tr("newSession")}</Button>
                          <Button size="small" type="link" icon={<PlusOutlined />} onClick={() => openEditor("delegate")}>{tr("delegate")}</Button>
                        </Space>
                      )}
                    </div>
                  ) : null}
                </div>
              );
            })}
          </nav>
        )}
      </div>
      <div className="wd-rail-foot">
        {invalidContext ? <Typography.Text type="warning" className="wd-rail-warn">{tr("invalidContext")}</Typography.Text> : null}
        <Button block size="small" icon={<PlusOutlined />} onClick={() => openEditor("create")}>{tr("newContext")}</Button>
      </div>
      {modal}
    </div>
  );
}
