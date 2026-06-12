import { useI18n } from "../i18n";
import type { ListedSession } from "../lib/types";
import { useLayoutStore, selectSessionListLayoutState } from "../store/layout";

interface SidebarProps {
  sessions: ListedSession[];
  activeSessionId: string;
  channelFilter: string;
  deletingSessionId?: string | null;
  onChannelFilterChange: (channel: string) => void;
  onSelect: (session: ListedSession) => void;
  onDelete: (session: ListedSession) => void;
  onCreate: () => void;
  mobileOpen?: boolean;
  onClose?: () => void;
}

export function Sidebar({
  sessions,
  activeSessionId,
  channelFilter,
  deletingSessionId = null,
  onChannelFilterChange,
  onSelect,
  onDelete,
  onCreate,
  mobileOpen = false,
  onClose,
}: SidebarProps) {
  // P0-C (SPEC §3.2): sessions column width is driven by the layout store.
  const sessionsLayout = useLayoutStore(selectSessionListLayoutState);
  const sessionsCollapsed = sessionsLayout.collapsed;
  const sessionsWidth = sessionsCollapsed ? sessionsLayout.iconOnlyWidth : sessionsLayout.width;
  const panel = (
    <SidebarPanel
      sessions={sessions}
      activeSessionId={activeSessionId}
      channelFilter={channelFilter}
      deletingSessionId={deletingSessionId}
      onChannelFilterChange={onChannelFilterChange}
      onSelect={onSelect}
      onDelete={onDelete}
      onCreate={onCreate}
      onClose={onClose}
      collapsed={sessionsCollapsed}
    />
  );

  return (
    <>
      <aside
        className="hidden h-full shrink-0 border-r border-[color:var(--border)] bg-[color:var(--panel)] md:flex md:flex-col"
        style={{ width: sessionsWidth }}
        data-testid="sessions-rail"
        data-collapsed={sessionsCollapsed ? "true" : "false"}
      >
        {panel}
      </aside>

      {mobileOpen ? (
        <div
          className="fixed inset-0 z-30 bg-black/30 backdrop-blur-[2px] md:hidden"
          onClick={onClose}
        >
          <aside
            className="flex h-full w-[min(92vw,23rem)] flex-col border-r border-[color:var(--border)] bg-[color:var(--panel)] shadow-2xl"
            onClick={(event) => event.stopPropagation()}
          >
            {panel}
          </aside>
        </div>
      ) : null}
    </>
  );
}

function SidebarPanel({
  sessions,
  activeSessionId,
  channelFilter,
  deletingSessionId,
  onChannelFilterChange,
  onSelect,
  onDelete,
  onCreate,
  onClose,
  collapsed = false,
}: SidebarProps & { collapsed?: boolean }) {
  const { t } = useI18n();
  const toggleSessions = useLayoutStore((state) => state.toggle);
  const channels = ["all", ...Array.from(new Set(sessions.map((session) => session.locator.channel || "web"))).sort()];
  const filteredSessions = channelFilter === "all" ? sessions : sessions.filter((session) => (session.locator.channel || "web") === channelFilter);

  // Collapsed: render only the strip (SPEC §4.5 bookmark + quick "+ New" affordance).
  if (collapsed) {
    return (
      <div className="flex h-full flex-col items-stretch gap-2 px-1.5 py-3" data-testid="sessions-rail-strip">
        <button
          type="button"
          aria-label={t("sessions.expand") || "Expand sessions"}
          title={t("sessions.expand") || "Expand sessions"}
          onClick={() => toggleSessions("sessions")}
          className="rounded-md border border-[color:var(--border)] px-1.5 py-1 text-[color:var(--muted)] hover:bg-[color:var(--panel-strong)]"
        >
          <span aria-hidden>»</span>
        </button>
        <button
          type="button"
          aria-label={t("sessions.new")}
          title={t("sessions.new")}
          onClick={() => {
            onCreate();
            onClose?.();
          }}
          className="rounded-md bg-[color:var(--accent-soft)] px-1.5 py-1 text-sm font-medium text-[color:var(--accent)] hover:opacity-90"
        >
          +
        </button>
        {onClose ? (
          <button
            type="button"
            aria-label={t("sessions.close")}
            title={t("sessions.close")}
            onClick={onClose}
            className="rounded-md border border-[color:var(--border)] px-1.5 py-1 text-[10px] text-[color:var(--muted)] hover:bg-[color:var(--panel-strong)] md:hidden"
          >
            ×
          </button>
        ) : null}
        <div className="mt-auto text-center text-[10px] text-[color:var(--muted)]" aria-hidden>
          {filteredSessions.length}
        </div>
      </div>
    );
  }

  return (
    <>
      <div className="flex items-start justify-between gap-3 border-b border-[color:var(--border)] px-4 py-3">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold uppercase tracking-[0.18em] text-[color:var(--muted)]">{t("sessions.title")}</h2>
          <p className="mt-1 text-xs text-[color:var(--muted)]">{t("sessions.subtitleAll")}</p>
        </div>
        <div className="flex items-center gap-2">
          <button
            className="rounded-full bg-[color:var(--accent-soft)] px-3 py-1.5 text-sm font-medium text-[color:var(--accent)] hover:opacity-90"
            onClick={() => {
              onCreate();
              onClose?.();
            }}
            type="button"
          >
            {t("sessions.new")}
          </button>
          {onClose ? (
            <button
              type="button"
              onClick={onClose}
              className="rounded-full border border-[color:var(--border)] px-3 py-1.5 text-sm text-[color:var(--muted)] md:hidden"
            >
              {t("sessions.close")}
            </button>
          ) : null}
          {/* P0-C: collapse the sessions column to a 40px strip. */}
          <button
            type="button"
            aria-label={t("sessions.collapse") || "Collapse sessions"}
            title={t("sessions.collapse") || "Collapse sessions"}
            onClick={() => toggleSessions("sessions")}
            className="hidden rounded-full border border-[color:var(--border)] px-2 py-1 text-xs text-[color:var(--muted)] hover:bg-[color:var(--panel-strong)] md:inline-block"
            data-testid="sessions-collapse"
          >
            «
          </button>
        </div>
      </div>
      <div className="border-b border-[color:var(--border)] px-3 py-3">
        <label className="grid gap-1.5">
          <span className="text-[11px] font-medium uppercase tracking-[0.16em] text-[color:var(--muted)]">
            {t("sessions.channelFilter")}
          </span>
          <select
            value={channelFilter}
            onChange={(event) => onChannelFilterChange(event.target.value)}
            className="w-full min-w-0 rounded-xl border border-[color:var(--border)] bg-[color:Canvas] px-3 py-2 text-sm outline-none transition focus:border-[color:var(--accent)]"
          >
            {channels.map((channel) => (
              <option key={channel} value={channel}>
                {channel === "all" ? t("sessions.channelAll") : channel}
              </option>
            ))}
          </select>
        </label>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
        {filteredSessions.length === 0 ? (
          <div className="rounded-2xl border border-dashed border-[color:var(--border)] px-4 py-6 text-sm text-[color:var(--muted)]">
            {t("sessions.empty")}
          </div>
        ) : (
          <div className="space-y-2">
            {filteredSessions.map((session) => {
              const isActive = session.session_id === activeSessionId;
              const deleting = deletingSessionId === session.session_id;
              return (
                <div
                  key={session.session_id}
                  className={`rounded-[1.15rem] border px-3 py-2.5 transition ${
                    isActive
                      ? "border-[color:var(--accent)] bg-[color:var(--accent-soft)]"
                      : "border-[color:var(--border)] bg-transparent hover:bg-[color:var(--panel-strong)]"
                  }`}
                >
                  <div className="flex items-start gap-2.5">
                    <button
                      type="button"
                      onClick={() => {
                        onSelect(session);
                        onClose?.();
                      }}
                      className="min-w-0 flex-1 text-left"
                    >
                      <div className="truncate text-[0.95rem] font-semibold leading-5">
                        {session.title || session.locator.key || session.session_id}
                      </div>
                      <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-[10px]">
                        <span className="rounded-full border border-[color:var(--border)] bg-[color:Canvas] px-2 py-0.5 text-[color:var(--muted)]">
                          {session.locator.channel || "web"}
                        </span>
                        {session.running ? (
                          <span className="rounded-full border border-[color:var(--accent)] bg-[color:var(--accent-soft)] px-2 py-0.5 text-[color:var(--accent)]">
                            {t("sessions.running")}
                          </span>
                        ) : null}
                        {session.locator.user_id ? (
                          <span
                            title={session.locator.user_id}
                            className="max-w-full truncate rounded-full border border-[color:var(--border)] bg-[color:Canvas] px-2 py-0.5 text-[color:var(--muted)]"
                          >
                            {session.locator.user_id}
                          </span>
                        ) : null}
                      </div>
                      <div className="mt-1.5 text-[11px] leading-5 text-[color:var(--muted)]">
                        {t("sessions.updated", { time: new Date(session.updated_at).toLocaleString() })}
                      </div>
                    </button>
                    <button
                      type="button"
                      onClick={() => {
                        onDelete(session);
                        onClose?.();
                      }}
                      disabled={deleting}
                      className="shrink-0 rounded-full border border-[color:var(--border)] px-2.5 py-1 text-[11px] text-[color:var(--muted)] transition hover:border-[color:var(--error)] hover:text-[color:var(--error)] disabled:cursor-not-allowed disabled:opacity-60"
                    >
                      {deleting ? t("sessions.deleting") : t("sessions.delete")}
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </>
  );
}
