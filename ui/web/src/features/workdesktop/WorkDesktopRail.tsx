import { useEffect, type ReactNode } from "react";
import { App as AntApp, Badge, Button, Empty, Input, Popconfirm, Skeleton, Space, Tag, Tooltip, Typography } from "antd";
import { CommentOutlined, DeleteOutlined, LinkOutlined, PlusOutlined, ReloadOutlined, SearchOutlined, VerticalRightOutlined } from "@ant-design/icons";
import { useWorkDesktopStore } from "./workDesktopStore";
import { WorkDesktopEditorModal } from "./WorkDesktopEditorModal";
import { useOpenWorkConversation, useWorkDesktopData } from "./useWorkDesktopData";
import { ensurePushSubscriptionSilent } from "../../lib/push";
import { useSettingsStore } from "../../store/settings";
import "./workDesktop.css";

// Fixed palette per status: a status never borrows another status's colour, so the
// rail, the header and the delegation rows cannot disagree about what "in_review" looks like.
const statusColor: Record<string, string> = { active: "processing", paused: "orange", archived: "default" };

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
  const openEditor = useWorkDesktopStore((state) => state.openEditor);
  const {
    tr, trLabel, filteredContexts, contextId, readOnly, invalidContext,
    unreadByContext, sessions, contextsQuery, unlinkSessionMutation,
  } = data;

  // The editor modal is owned by the rail: the rail is mounted whenever the desktop
  // view is shown, while the dock pane is not (its tab can be switched away or the
  // dock collapsed) — so putting the modal in the dock would silently swallow every
  // editor opened from here. The dock renders a fallback copy only when this rail is
  // not on screen (see WorkDesktopPanel), so exactly one instance exists at a time.
  const modal = <WorkDesktopEditorModal data={data} />;

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
              // Only the ACTIVE desktop shows its linked conversations inline; showing
              // every desktop's conversations at once would turn the list into a wall.
              const linked = active ? sessions : [];
              return (
                <div key={item.id} className="wd-rail-item-wrap">
                  <button
                    type="button"
                    className={`wd-rail-item ${active ? "wd-rail-item-selected" : ""}`}
                    aria-current={active ? "true" : undefined}
                    onClick={() => setContextId(item.id)}
                  >
                    <span className="wd-rail-item-top">
                      <strong>{item.title}</strong>
                      <Space size={4} align="center">
                        {unread > 0 && <Badge count={unread} size="small" title={tr("attentionUnread", { count: unread })} />}
                        <span className={`wd-status-dot wd-status-${item.status}`} />
                      </Space>
                    </span>
                    <span className="wd-rail-item-summary">{item.checkpoint.summary || item.goal || item.background}</span>
                    <span className="wd-rail-item-meta">
                      <Tag color={statusColor[item.status] ?? "default"}>{trLabel("", item.status)}</Tag>
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
