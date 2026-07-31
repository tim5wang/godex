import { Badge, Button, Empty, Input, Popconfirm, Popover, Typography } from "antd";
import {
  ApiOutlined,
  ClockCircleOutlined,
  DeleteOutlined,
  FolderOpenOutlined,
  FolderOutlined,
  MessageOutlined,
  PlusOutlined,
  QuestionCircleOutlined,
  SearchOutlined,
  SwapOutlined,
  VerticalLeftOutlined,
  VerticalRightOutlined,
} from "@ant-design/icons";
import { useMemo, useState } from "react";
import type { ListedSession } from "../../lib/types";
import { useI18n } from "../../i18n";
import { filterSessions, groupSessionsByWorkspace } from "./sessionGroups";
import type { WorkspaceGroup, WorkspaceGroupType } from "./sessionGroups";

interface SessionsRailProps {
  collapsed: boolean;
  sessions: ListedSession[];
  activeSessionId: string;
  searchQuery: string;
  deletingSessionId?: string;
  onSearchChange: (query: string) => void;
  onCreate: () => void;
  onSelect: (session: ListedSession) => void;
  onDelete: (session: ListedSession) => void;
  onToggleCollapsed: () => void;
}

const GROUP_ICONS: Record<WorkspaceGroupType, React.ReactNode> = {
  directory: <FolderOutlined />,
  acp: <ApiOutlined />,
  cron: <ClockCircleOutlined />,
  channel: <SwapOutlined />,
  other: <QuestionCircleOutlined />,
};

const GROUP_ICONS_OPEN: Record<WorkspaceGroupType, React.ReactNode> = {
  directory: <FolderOpenOutlined />,
  acp: <ApiOutlined />,
  cron: <ClockCircleOutlined />,
  channel: <SwapOutlined />,
  other: <QuestionCircleOutlined />,
};

function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString();
}

function WorkspacePopover({ w, t }: { w: WorkspaceGroup; t: ReturnType<typeof useI18n>["t"] }) {
  return (
    <div className="ctx-popover">
      <div className="ctx-popover-group">
        <div className="ctx-popover-group-title">{t("chat.chatV2Rail.popoverWorkspace")}</div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.chatV2Rail.popoverPath")}</span>
          <span className="ctx-popover-value">{w.path || "—"}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.chatV2Rail.groupSessions")}</span>
          <span className="ctx-popover-value">{w.count}</span>
        </div>
      </div>
    </div>
  );
}

function SessionPopover({ s, onDelete, isDeleting, t }: { s: ListedSession; onDelete: (s: ListedSession) => void; isDeleting?: boolean; t: ReturnType<typeof useI18n>["t"] }) {
  return (
    <div className="ctx-popover" style={{ minWidth: 220 }}>
      <div className="ctx-popover-group">
        <div className="ctx-popover-group-title">{t("chat.chatV2Rail.popoverSession")}</div>
        {s.title ? (
          <div className="ctx-popover-row">
            <span className="ctx-popover-label">{t("chat.chatV2Rail.popoverTitle")}</span>
            <span className="ctx-popover-value">{s.title}</span>
          </div>
        ) : null}
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.chatV2Rail.popoverId")}</span>
          <span className="ctx-popover-value" style={{ fontSize: 10 }}>{s.session_id.slice(0, 12)}…</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.chatV2Rail.popoverCreated")}</span>
          <span className="ctx-popover-value">{formatTime(s.created_at)}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.chatV2Rail.popoverLastActive")}</span>
          <span className="ctx-popover-value">{formatTime(s.last_activity_at)}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.chatV2Rail.popoverSource")}</span>
          <span className="ctx-popover-value">{s.locator?.channel ?? "web"}</span>
        </div>
        {s.locator?.key ? (
          <div className="ctx-popover-row">
            <span className="ctx-popover-label">{t("chat.chatV2Rail.popoverKey")}</span>
            <span className="ctx-popover-value">{s.locator.key}</span>
          </div>
        ) : null}
      </div>
      <div className="ctx-popover-group" style={{ paddingTop: 6 }}>
        <Popconfirm title={t("chat.chatV2Rail.popoverDeleteConfirm")} onConfirm={() => onDelete(s)}>
          <Button block danger size="small" icon={<DeleteOutlined />} loading={isDeleting}>
            {t("chat.chatV2Rail.popoverDelete")}
          </Button>
        </Popconfirm>
      </div>
    </div>
  );
}

/** Chat V2 left rail: brand-adjacent session list with New chat, search and
 *  workspace-grouped conversations. Collapses to a 48px icon strip. */
export function SessionsRail(props: SessionsRailProps) {
  const { t } = useI18n();

  if (props.collapsed) {
    return (
      <div className="chat-v2-rail chat-v2-rail-collapsed" data-testid="chat-v2-sessions-collapsed">
        <Popover content={t("chat.chatV2Rail.expandSidebar")} trigger="hover" placement="right">
          <Button type="text" icon={<VerticalRightOutlined />} aria-label={t("chat.chatV2Rail.expandSidebar")} onClick={props.onToggleCollapsed} />
        </Popover>
        <Popover content={t("chat.chatV2Rail.newChat")} trigger="hover" placement="right">
          <Button type="text" icon={<PlusOutlined />} aria-label={t("chat.chatV2Rail.newChat")} onClick={props.onCreate} />
        </Popover>
      </div>
    );
  }

  const filtered = filterSessions(props.sessions, props.searchQuery);
  const workspaceGroups = groupSessionsByWorkspace(filtered);

  return (
    <div className="chat-v2-rail" data-testid="chat-v2-sessions">
      <div className="chat-v2-rail-top">
        <Button block type="primary" icon={<PlusOutlined />} className="chat-v2-new-chat" onClick={props.onCreate}>
          {t("chat.chatV2Rail.newChat")}
        </Button>
        <Popover content={t("chat.chatV2Rail.collapseSidebar")} trigger="hover">
          <Button
            type="text"
            icon={<VerticalLeftOutlined />}
            aria-label={t("chat.chatV2Rail.collapseSidebar")}
            className="chat-v2-rail-collapse"
            onClick={props.onToggleCollapsed}
          />
        </Popover>
      </div>
      <Input
        className="chat-v2-search"
        allowClear
        prefix={<SearchOutlined />}
        placeholder={t("chat.chatV2Rail.searchPlaceholder")}
        aria-label={t("chat.chatV2Rail.searchPlaceholder")}
        value={props.searchQuery}
        onChange={(event) => props.onSearchChange(event.target.value)}
      />
      <div className="chat-v2-session-scroll">
        {workspaceGroups.length === 0 ? (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={props.searchQuery ? t("chat.chatV2Rail.searchNoMatch") : t("chat.chatV2Rail.empty")}
          />
        ) : (
          workspaceGroups.map((w) => (
            <WorkspaceSection key={w.key} workspace={w} t={t} {...props} />
          ))
        )}
      </div>
    </div>
  );
}

function WorkspaceSection(props: { workspace: WorkspaceGroup; t: ReturnType<typeof useI18n>["t"] } & Omit<SessionsRailProps, "collapsed" | "sessions" | "searchQuery" | "onSearchChange" | "onToggleCollapsed">) {
  const { workspace, t } = props;
  const [collapsed, setCollapsed] = useState(false);
  const icon = collapsed ? GROUP_ICONS[workspace.type] : GROUP_ICONS_OPEN[workspace.type];

  return (
    <div className="chat-v2-workspace-group">
      <Popover content={<WorkspacePopover w={workspace} t={t} />} trigger="hover" placement="right" mouseEnterDelay={0.5}>
        <div
          className="chat-v2-workspace-header"
          role="button"
          tabIndex={0}
          onClick={() => setCollapsed((v) => !v)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              setCollapsed((v) => !v);
            }
          }}
        >
          <span className="chat-v2-workspace-icon">{icon}</span>
          <span className="chat-v2-workspace-label">{workspace.label}</span>
          <span className="chat-v2-workspace-count">{workspace.count}</span>
        </div>
      </Popover>
      {!collapsed && (
        <div className="chat-v2-workspace-body">
          {workspace.dateGroups.map((group) => (
            <div key={group.label} className="chat-v2-session-group">
              <Typography.Text className="chat-v2-session-group-label">{group.label}</Typography.Text>
              {group.sessions.map((session) => {
                const active = session.session_id === props.activeSessionId;
                const isDeleting = props.deletingSessionId === session.session_id;
                return (
                  <Popover
                    key={session.session_id}
                    content={<SessionPopover s={session} onDelete={props.onDelete} isDeleting={isDeleting} t={t} />}
                    trigger="hover"
                    placement="right"
                    mouseEnterDelay={0.5}
                  >
                    <div
                      role="button"
                      tabIndex={0}
                      data-testid={`chat-v2-session-${session.session_id}`}
                      className={`chat-v2-session-item${active ? " chat-v2-session-item-active" : ""}`}
                      onClick={() => props.onSelect(session)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") {
                          event.preventDefault();
                          props.onSelect(session);
                        }
                      }}
                    >
                      <MessageOutlined className="chat-v2-session-icon" />
                      <span className="chat-v2-session-title">
                        {session.title || session.locator.key || session.session_id}
                      </span>
                      {session.running ? <Badge status="processing" /> : null}
                      {active ? (
                        <Popconfirm title={t("chat.chatV2Rail.deleteConfirm")} onConfirm={() => props.onDelete(session)}>
                          <Button
                            type="text"
                            size="small"
                            icon={<DeleteOutlined />}
                            aria-label={t("chat.chatV2Rail.popoverDelete")}
                            loading={isDeleting}
                            className="chat-v2-session-delete"
                            onClick={(event) => event.stopPropagation()}
                          />
                        </Popconfirm>
                      ) : null}
                    </div>
                  </Popover>
                );
              })}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}