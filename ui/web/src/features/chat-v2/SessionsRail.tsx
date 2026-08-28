import { AutoComplete, Badge, Button, Empty, Input, Popconfirm, Popover, Select, Skeleton, Tooltip, Typography } from "antd";
import {
  ApiOutlined,
  ClockCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  FolderOpenOutlined,
  FolderOutlined,
  MessageOutlined,
  PlusOutlined,
  QuestionCircleOutlined,
  ReloadOutlined,
  SearchOutlined,
  SwapOutlined,
  VerticalRightOutlined,
} from "@ant-design/icons";
import { useMemo, useState } from "react";
import type { ListedSession, SkillCatalogEntry } from "../../lib/types";
import type { AgentTemplate } from "../../lib/api";
import { useI18n } from "../../i18n";
import { filterSessions, groupSessionsByWorkspace, isTempDir } from "./sessionGroups";
import type { WorkspaceGroup, WorkspaceGroupType } from "./sessionGroups";

interface SessionsRailProps {
  collapsed: boolean;
  sessions: ListedSession[];
  loading?: boolean;
  error?: boolean;
  onRetry?: () => void;
  activeSessionId: string;
  searchQuery: string;
  deletingSessionId?: string;
  renamingSessionId?: string;
  onSearchChange: (query: string) => void;
  /** Installed-skill catalog for the new-chat skill picker (global, session-independent). */
  skillsCatalog?: SkillCatalogEntry[];
  skillsLoading?: boolean;
  templates?: AgentTemplate[];
  templatesLoading?: boolean;
  /** workspaceDir 为空/undefined 时使用服务默认运行目录；template 为新建会话选择的 agent 模板 ID（缺省 default）；skills 为新建会话要加载的已安装 skill 名。 */
  onCreate: (workspaceDir?: string, template?: string, skills?: string[]) => void;
  onSelect: (session: ListedSession) => void;
  onDelete: (session: ListedSession) => void;
  onRename: (session: ListedSession, title: string) => void;
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

/** Shorten a long absolute path for the new-chat workspace picker by cutting
 *  the head and keeping the trailing segments (the distinguishable part).
 *  Falls back to a character-based tail when the kept segments are still too
 *  long.  The full path is always available via the option's title tooltip. */
function shortenDirPath(path: string, maxLen = 44): string {
  if (path.length <= maxLen) {
    return path;
  }
  const tail = path.split("/").filter(Boolean).slice(-2).join("/");
  const candidate = `…/${tail}`;
  if (candidate.length <= maxLen) {
    return candidate;
  }
  return `…${path.slice(-(maxLen - 1))}`;
}

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

function SessionPopover({ s, onDelete, isDeleting, onRename, renaming, t }: { s: ListedSession; onDelete: (s: ListedSession) => void; isDeleting?: boolean; onRename: (s: ListedSession, title: string) => void; renaming?: boolean; t: ReturnType<typeof useI18n>["t"] }) {
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState(s.title || s.locator?.key || s.session_id);
  const commit = () => {
    const next = title.trim();
    if (next && next !== s.title) {
      onRename(s, next);
    }
    setEditing(false);
  };
  return (
    <div className="ctx-popover" style={{ minWidth: 240 }}>
      <div className="ctx-popover-group">
        <div className="ctx-popover-group-title">{t("chat.chatV2Rail.popoverSession")}</div>
        {editing ? (
          <div className="ctx-popover-row">
            <span className="ctx-popover-label">{t("chat.chatV2Rail.popoverTitle")}</span>
            <Input
              size="small"
              value={title}
              autoFocus
              placeholder={t("chat.chatV2Rail.renamePlaceholder")}
              onChange={(event) => setTitle(event.target.value)}
              onPressEnter={commit}
              onBlur={commit}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  setTitle(s.title || s.locator?.key || s.session_id);
                  setEditing(false);
                }
              }}
            />
          </div>
        ) : (
          <div className="ctx-popover-row">
            <span className="ctx-popover-label">{t("chat.chatV2Rail.popoverTitle")}</span>
            <span className="ctx-popover-value" style={{ flex: 1 }}>{s.title || s.locator?.key || s.session_id}</span>
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              aria-label={t("chat.chatV2Rail.renameSession")}
              loading={renaming}
              onClick={() => setEditing(true)}
            />
          </div>
        )}
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

/** Popover form letting the user optionally pin a new chat to an explicit
 *  working directory, pick a creation mode, and choose which installed skills
 *  the fresh session starts with.  Empty directory keeps the service default;
 *  empty skills fall back to the global team.default_skills. */
function NewChatWorkspacePopover(props: {
  sessions: ListedSession[];
  skillsCatalog?: SkillCatalogEntry[];
  skillsLoading?: boolean;
  templates?: AgentTemplate[];
  templatesLoading?: boolean;
  onCreate: (workspaceDir?: string, template?: string, skills?: string[]) => void;
  children: React.ReactNode;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [dir, setDir] = useState("");
  const [template, setTemplate] = useState("default");
  const [skills, setSkills] = useState<string[]>([]);
  // Distinct non-temp working directories seen in existing sessions, newest
  // first, so the picker offers real history instead of free-text only.
  const historyDirs = useMemo(() => {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const session of props.sessions) {
      const projectDir = session.locator?.metadata?.project_dir?.trim() ?? "";
      if (projectDir !== "" && !seen.has(projectDir) && !isTempDir(projectDir)) {
        seen.add(projectDir);
        out.push(projectDir);
      }
    }
    return out;
  }, [props.sessions]);
  const skillOptions = useMemo(
    () =>
      (props.skillsCatalog ?? [])
        .filter((skill) => skill.name?.trim())
        .map((skill) => ({
          value: skill.name.trim(),
          label: skill.name.trim(),
          description: skill.description?.trim() || "",
        })),
    [props.skillsCatalog],
  );
  const submit = () => {
    props.onCreate(
      dir.trim() || undefined,
      template === "default" ? undefined : template,
      skills.length > 0 ? [...skills] : undefined,
    );
    setDir("");
    setTemplate("default");
    setSkills([]);
    setOpen(false);
  };
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      trigger="click"
      placement="bottomLeft"
      content={
        <div style={{ display: "flex", flexDirection: "column", gap: 8, width: 320 }}>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {t("chat.chatV2Rail.workspaceDirHint")}
          </Typography.Text>
          <AutoComplete
            autoFocus
            allowClear
            options={historyDirs.map((dir) => ({
              value: dir,
              label: (
                <span className="chat-v2-dir-option" title={dir}>
                  {shortenDirPath(dir)}
                </span>
              ),
            }))}
            placeholder={t("chat.chatV2Rail.workspaceDirPlaceholder")}
            value={dir}
            onChange={(value) => setDir(value)}
            onSelect={(value) => setDir(value)}
          />
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <Typography.Text type="secondary" style={{ fontSize: 12, whiteSpace: "nowrap" }}>
              {t("chat.chatV2Rail.templateLabel")}
            </Typography.Text>
            <Select
              size="small"
              style={{ flex: 1 }}
              value={template}
              onChange={setTemplate}
              loading={props.templatesLoading}
              showSearch
              optionFilterProp="label"
              options={[
                { value: "default", label: t("chat.chatV2Rail.templateDefault") },
                ...(props.templates ?? [])
                  .filter((tpl) => tpl.id?.trim() && tpl.id !== "default")
                  .map((tpl) => ({
                    value: tpl.id,
                    label: tpl.avatar ? `${tpl.avatar} ${tpl.name || tpl.id}` : tpl.name || tpl.id,
                  })),
              ]}
            />
          </div>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {t("chat.chatV2Rail.templateHint")}
          </Typography.Text>
          <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t("chat.chatV2Rail.skillsLabel")}
            </Typography.Text>
            <Select
              mode="multiple"
              size="small"
              allowClear
              loading={props.skillsLoading}
              placeholder={t("chat.chatV2Rail.skillsPlaceholder")}
              value={skills}
              onChange={setSkills}
              options={skillOptions}
              optionRender={(option) => (
                <div style={{ display: "flex", flexDirection: "column" }}>
                  <span>{option.label}</span>
                  {option.data?.description ? (
                    <span style={{ fontSize: 11, color: "rgba(0,0,0,0.45)" }}>{option.data.description}</span>
                  ) : null}
                </div>
              )}
            />
            <Typography.Text type="secondary" style={{ fontSize: 11 }}>
              {t("chat.chatV2Rail.skillsHint")}
            </Typography.Text>
          </div>
          <Button type="primary" size="small" onClick={submit}>
            {t("chat.chatV2Rail.newChat")}
          </Button>
        </div>
      }
    >
      {props.children}
    </Popover>
  );
}

/** Chat V2 left rail: brand-adjacent session list with New chat, search and
 *  workspace-grouped conversations. Collapses to a 48px icon strip. */
export function SessionsRail(props: SessionsRailProps) {
  const { t } = useI18n();
  // ChatPage rerenders for every stream delta. Assemble the expensive
  // workspace/date hierarchy only when the list or search changes, and do it
  // while the mobile rail is collapsed so opening the drawer is immediate.
  const workspaceGroups = useMemo(
    () => groupSessionsByWorkspace(filterSessions(props.sessions, props.searchQuery)),
    [props.searchQuery, props.sessions],
  );

  if (props.collapsed) {
    return (
      <div className="chat-v2-rail chat-v2-rail-collapsed" data-testid="chat-v2-sessions-collapsed">
        {props.error ? (
          <Tooltip title="Retry loading sessions">
            <Button type="text" icon={<ReloadOutlined />} aria-label="Reload sessions" onClick={props.onRetry} danger />
          </Tooltip>
        ) : (
          <>
            <Popover content={t("chat.chatV2Rail.expandSidebar")} trigger="hover" placement="right">
              <Button type="text" icon={<VerticalRightOutlined />} aria-label={t("chat.chatV2Rail.expandSidebar")} onClick={props.onToggleCollapsed} />
            </Popover>
            <NewChatWorkspacePopover sessions={props.sessions} skillsCatalog={props.skillsCatalog} skillsLoading={props.skillsLoading} templates={props.templates} templatesLoading={props.templatesLoading} onCreate={props.onCreate}>
              <Button type="text" icon={<PlusOutlined />} aria-label={t("chat.chatV2Rail.newChat")} />
            </NewChatWorkspacePopover>
          </>
        )}
      </div>
    );
  }

  return (
    <div className="chat-v2-rail" data-testid="chat-v2-sessions">
      <div className="chat-v2-rail-top">
        <NewChatWorkspacePopover sessions={props.sessions} skillsCatalog={props.skillsCatalog} skillsLoading={props.skillsLoading} templates={props.templates} templatesLoading={props.templatesLoading} onCreate={props.onCreate}>
          <Button block type="primary" icon={<PlusOutlined />} className="chat-v2-new-chat">
            {t("chat.chatV2Rail.newChat")}
          </Button>
        </NewChatWorkspacePopover>
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
        {props.error ? (
          <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 12, padding: 24 }}>
            <Typography.Text type="danger">{t("chat.chatV2Rail.loadError")}</Typography.Text>
            <Button icon={<ReloadOutlined />} onClick={props.onRetry} size="small">
              {t("chat.chatV2Rail.retry")}
            </Button>
          </div>
        ) : props.loading ? (
          <div className="chat-v2-rail-loading" data-testid="chat-v2-sessions-loading">
            <Skeleton active paragraph={{ rows: 5 }} title={false} />
          </div>
        ) : workspaceGroups.length === 0 ? (
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
                    content={<SessionPopover s={session} onDelete={props.onDelete} onRename={props.onRename} isDeleting={isDeleting} renaming={props.renamingSessionId === session.session_id} t={t} />}
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